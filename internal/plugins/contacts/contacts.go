package contacts

// Ports save_cards, VcardFile and CardDav of everysaid/plugins/contacts.py.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

func init() {
	plugins.Register(CardDav{})
	plugins.Register(VcardFile{})
}

// Avatars is where the contacts' photos are kept.
func Avatars() string { return filepath.Join(config.Cache, "avatars") }

// SaveCards replaces the contacts of the instance by the cards; their addresses are joined where
// the archive has them. It returns how many contacts, and how many addresses were linked.
func SaveCards(c *plugins.Context, cards []Card) (int, int, error) {
	return saveCards(c, cards, true, nil, nil)
}

// saveCards saves the cards; with replace, every other contact of the instance goes, else only
// those at the addresses (urls) that changed and are not among the cards, and those gone.
func saveCards(c *plugins.Context, cards []Card, replace bool, changed, gone []string) (int, int, error) {
	if err := os.MkdirAll(Avatars(), 0o700); err != nil {
		return 0, 0, err
	}
	now := time.Now().Unix()
	linked := 0
	err := c.Store().Write(func(tx *sql.Tx) error {
		kinds := map[string]int64{}
		db.Each(tx, "SELECT name, id FROM address_kind", nil, func(scan func(...any)) {
			var name string
			var id int64
			scan(&name, &id)
			kinds[name] = id
		})
		keep := map[int64]bool{}
		for _, card := range cards {
			var photo any
			if card.Photo != nil {
				ext := photoExt(card.Photo)
				sum := sha256.Sum256(card.Photo.Data)
				name := hex.EncodeToString(sum[:]) + "." + ext
				path := filepath.Join(Avatars(), name)
				if _, err := os.Stat(path); err != nil {
					if err := os.WriteFile(path, card.Photo.Data, 0o600); err != nil {
						return err
					}
				}
				photo = name
			}
			var cid int64
			if !db.Row(tx, "INSERT INTO contact (instance_id, uid, url, name, organization, photo, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?) "+
				"ON CONFLICT (instance_id, uid) DO UPDATE SET url = excluded.url, name = excluded.name, "+
				"organization = excluded.organization, photo = excluded.photo, updated_at = excluded.updated_at RETURNING id",
				[]any{c.ID, card.UID, db.NullStr(card.URL), db.NullStr(card.Name), db.NullStr(card.Org), photo, now}, &cid) {
				return fmt.Errorf("contact %s not saved", card.UID)
			}
			keep[cid] = true
			db.Exec(tx, "DELETE FROM contact_address WHERE contact_id = ?", cid)
			for _, v := range append(append([]Value{}, card.Phones...), card.Emails...) {
				kind, value := archive.Address(v.Value, config.Region)
				kid, ok := kinds[kind]
				if !ok {
					continue
				}
				var aid int64
				if db.Row(tx, "SELECT id FROM address WHERE kind_id = ? AND value = ? AND service_id IS NULL",
					[]any{kid, value}, &aid) {
					db.Exec(tx, "INSERT OR IGNORE INTO contact_address VALUES (?, ?, ?)", cid, aid, db.NullStr(v.Label))
					linked++
				}
			}
		}
		var old []int64
		if replace {
			old = db.Ints(tx, "SELECT id FROM contact WHERE instance_id = ?", c.ID)
		} else if urls := append(append([]string{}, changed...), gone...); len(urls) > 0 {
			old = db.Ints(tx, "SELECT id FROM contact WHERE instance_id = ? AND url IN ("+db.Marks(len(urls))+")",
				append([]any{c.ID}, db.Args(urls)...)...)
		}
		for _, cid := range old {
			if !keep[cid] {
				db.Exec(tx, "DELETE FROM contact_address WHERE contact_id = ?", cid)
				db.Exec(tx, "DELETE FROM contact WHERE id = ?", cid)
			}
		}
		return nil
	})
	return len(cards), linked, err
}

var safeExt = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// photoExt is the extension of a photo's file: the kind the card says where it is a plain word (it
// comes from outside, and goes into a path), else the kind its bytes show.
func photoExt(p *Photo) string {
	ext := strings.ToLower(p.Ext)
	if !safeExt.MatchString(ext) {
		ext = "img"
		if kind, ok := strings.CutPrefix(http.DetectContentType(p.Data), "image/"); ok && safeExt.MatchString(kind) {
			ext = kind
		}
	}
	if ext == "jpeg" {
		ext = "jpg"
	}
	return ext
}

func saved(c *plugins.Context, cards []Card) error {
	n, linked, err := SaveCards(c, cards)
	if err != nil {
		return err
	}
	c.Log("contacts: {n}, addresses found in the archive: {linked}", map[string]any{"n": n, "linked": linked})
	return nil
}

// VcardFile is the `vcard-file` contacts plugin: a .vcf file.
type VcardFile struct{}

func (VcardFile) Info() *plugins.Info {
	return &plugins.Info{
		ID: "vcard-file", Name: "Contacts file (.vcf)", Kind: "contacts",
		NameWeights: []plugins.Weight{{Key: "contacts", Weight: 100}}, // the user's own address book
		Description: "A vCard file exported from any address book (phone, Google, Outlook, ...).",
		Settings:    []plugins.Setting{{Key: "path", Label: ".vcf file", Type: "path", Required: true}},
	}
}

func (VcardFile) Sync(c *plugins.Context) error {
	data, err := os.ReadFile(config.ExpandUser(c.Str("path")))
	if err != nil {
		return err
	}
	// read as Python's text files are: bad bytes replaced, any line end a \n
	text := strings.ToValidUTF8(string(data), "\uFFFD")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return saved(c, ParseVcards(text))
}

func (p VcardFile) RunImport(c *plugins.Context) error { return p.Sync(c) }

// CardDav is the `carddav` contacts plugin: a CardDAV address book.
type CardDav struct{}

func (CardDav) Info() *plugins.Info {
	return &plugins.Info{
		ID: "carddav", Name: "Address book (CardDAV)", Kind: "contacts",
		NameWeights: []plugins.Weight{{Key: "contacts", Weight: 100}}, // the user's own address book
		Description: "A CardDAV address book: Nextcloud, iCloud, Fastmail, Radicale and others.",
		Settings: []plugins.Setting{
			{Key: "url", Label: "Address book URL", Type: "url", Required: true,
				Help: "e.g. https://cloud.example.org/remote.php/dav/addressbooks/users/NAME/contacts/"},
			{Key: "username", Label: "User", Type: "text", Required: true},
			{Key: "password", Label: "Password (an app password)", Type: "secret", Required: true},
		},
	}
}

const addressbookQuery = `<?xml version="1.0" encoding="utf-8"?><c:addressbook-query xmlns:d="DAV:" ` +
	`xmlns:c="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/><c:address-data/></d:prop>` +
	`</c:addressbook-query>`

// syncCollection is RFC 6578's question: what changed since the token ("": everything).
func syncCollection(token string) string {
	var t bytes.Buffer
	xml.EscapeText(&t, []byte(token))
	return `<?xml version="1.0" encoding="utf-8"?><d:sync-collection xmlns:d="DAV:" ` +
		`xmlns:c="urn:ietf:params:xml:ns:carddav"><d:sync-token>` + t.String() + `</d:sync-token>` +
		`<d:sync-level>1</d:sync-level><d:prop><d:getetag/><c:address-data/></d:prop></d:sync-collection>`
}

// multiget asks for the cards at these addresses (RFC 6352).
func multiget(hrefs []string) string {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><c:addressbook-multiget xmlns:d="DAV:" ` +
		`xmlns:c="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/><c:address-data/></d:prop>`)
	for _, h := range hrefs {
		b.WriteString("<d:href>")
		xml.EscapeText(&b, []byte(h))
		b.WriteString("</d:href>")
	}
	b.WriteString("</c:addressbook-multiget>")
	return b.String()
}

type multistatus struct {
	XMLName   xml.Name `xml:"DAV: multistatus"` // another answer is no address book
	SyncToken string   `xml:"DAV: sync-token"`
	Responses []struct {
		Href     string `xml:"DAV: href"`
		Status   string `xml:"DAV: status"` // of the whole response: 404 gone, 507 more to come
		Propstat []struct {
			Prop struct {
				Data string `xml:"urn:ietf:params:xml:ns:carddav address-data"`
			} `xml:"DAV: prop"`
		} `xml:"DAV: propstat"`
	} `xml:"DAV: response"`
}

// HTTPError is an answer of the server that is not a success.
type HTTPError struct {
	Code int
	URL  string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d %s for %s", e.Code, http.StatusText(e.Code), e.URL)
}

// davClient follows no redirect: Go would turn the REPORT into a GET (for 301 to 303), whose answer
// is no address book; the user is told the code instead, as the Python's httpx did.
var davClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// report asks the address book a REPORT: only a 207 Multi-Status answer is one.
func report(c *plugins.Context, body, depth string) (*multistatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	url := c.Str("url")
	req, err := http.NewRequestWithContext(ctx, "REPORT", url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.SetBasicAuth(c.Str("username"), c.Secret("password"))
	r, err := davClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusMultiStatus {
		return nil, &HTTPError{r.StatusCode, url}
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var ms multistatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, err
	}
	return &ms, nil
}

func statusIs(status string, code int) bool {
	f := strings.Fields(status)
	return len(f) > 1 && f[1] == strconv.Itoa(code)
}

// changes is what a sync-collection says from a token on: the cards that changed (with the
// addresses they are at), the addresses gone, and the new token. An answer cut short (507) is
// asked on from where it stopped. ok false: the server does not sync so (or not from this token).
func changes(c *plugins.Context, token string) (cards []Card, changed, gone []string, next string, ok bool, err error) {
	var missing []string // changed, but sent without their card: asked for after
	for range 1000 {
		ms, err := report(c, syncCollection(token), "0")
		if err != nil || ms.SyncToken == "" {
			return nil, nil, nil, "", false, err
		}
		more := false
		for _, resp := range ms.Responses {
			switch {
			case statusIs(resp.Status, 507):
				more = true
			case statusIs(resp.Status, 404):
				gone = append(gone, resp.Href)
			default:
				data := ""
				for _, ps := range resp.Propstat {
					data += ps.Prop.Data
				}
				if strings.TrimSpace(data) == "" {
					missing = append(missing, resp.Href)
					continue
				}
				changed = append(changed, resp.Href)
				for _, card := range ParseVcards(data) {
					card.URL = resp.Href
					cards = append(cards, card)
				}
			}
		}
		token = ms.SyncToken
		if !more {
			break
		}
	}
	for len(missing) > 0 {
		batch := missing[:min(200, len(missing))]
		missing = missing[len(batch):]
		ms, err := report(c, multiget(batch), "1")
		if err != nil {
			return nil, nil, nil, "", true, err // nothing applied: the same token next time
		}
		for _, resp := range ms.Responses {
			for _, ps := range resp.Propstat {
				if strings.TrimSpace(ps.Prop.Data) == "" {
					continue
				}
				changed = append(changed, resp.Href)
				for _, card := range ParseVcards(ps.Prop.Data) {
					card.URL = resp.Href
					cards = append(cards, card)
				}
			}
		}
	}
	return cards, changed, gone, token, true, nil
}

// Sync brings the address book: what changed since the last time where the server keeps a sync
// token (RFC 6578), else all of it, replacing the contacts; only from full answers (207
// Multi-Status) of the address book, so that a wrong one never empties them.
func (CardDav) Sync(c *plugins.Context) error {
	url := c.Str("url")
	token, _ := c.State["sync_token"].(string)
	if at, _ := c.State["sync_url"].(string); at != url {
		token = "" // a token of another address book
	}
	if token != "" {
		cards, changed, gone, next, ok, err := changes(c, token)
		if ok && err != nil {
			return err
		}
		if ok {
			if err := savedSince(c, cards, changed, gone); err != nil {
				return err
			}
			c.SaveState(plugins.M{"sync_token": next, "sync_url": url})
			return nil
		}
		// a token the server no longer takes: everything again
	}
	cards, _, _, next, ok, err := changes(c, "")
	if ok && err != nil {
		return err
	}
	if !ok { // a server without sync-collection: the whole address book, asked for at once
		ms, err := report(c, addressbookQuery, "1")
		if err != nil {
			return err
		}
		cards = nil
		for _, resp := range ms.Responses {
			for _, ps := range resp.Propstat {
				for _, card := range ParseVcards(ps.Prop.Data) {
					card.URL = resp.Href
					cards = append(cards, card)
				}
			}
		}
		next = ""
	}
	if err := saved(c, cards); err != nil {
		return err
	}
	c.SaveState(plugins.M{"sync_token": next, "sync_url": url})
	return nil
}

func savedSince(c *plugins.Context, cards []Card, changed, gone []string) error {
	n, linked, err := saveCards(c, cards, false, changed, gone)
	if err != nil {
		return err
	}
	c.Log("contacts changed: {n}, gone: {gone}, addresses found in the archive: {linked}",
		map[string]any{"n": n, "gone": len(gone), "linked": linked})
	return nil
}

func (p CardDav) RunImport(c *plugins.Context) error { return p.Sync(c) }
