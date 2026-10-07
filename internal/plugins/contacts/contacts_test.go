package contacts

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "everysaid-contacts-test")
	for k, v := range map[string]string{"EVERYSAID_DATA": "data", "EVERYSAID_CACHE": "cache",
		"EVERYSAID_CONFIG": "config", "EVERYSAID_STATE": "state"} {
		os.Setenv(k, filepath.Join(dir, v))
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test-contacts")
	config.Load()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const card30 = "BEGIN:VCARD\r\nVERSION:3.0\r\nN:Οικονόμου;Κατερίνα;;;\r\nitem1.TEL;TYPE=CELL:+30 694 000 0001\r\n" +
	"TEL:2101234567\r\nEMAIL;TYPE=INTERNET,HOME:katerina@example.com\r\nORG:Acme;Sales\r\nNOTE:a long\r\n  folded line\r\n" +
	"END:VCARD\r\n"

func TestParseVcards(t *testing.T) {
	c := ParseVcards(card30)
	if len(c) != 1 {
		t.Fatalf("%d cards", len(c))
	}
	k := c[0]
	// the uid the Python made of a card without one (sha1 of the name and the repr of the phones)
	if k.UID != "bf486a06bef67373b278d12a9e765f0c993ee175" || k.Name != "Κατερίνα Οικονόμου" || k.Org != "Acme" {
		t.Fatalf("%+v", k)
	}
	if !reflect.DeepEqual(k.Phones, []Value{{"+30 694 000 0001", "cell"}, {"2101234567", ""}}) ||
		!reflect.DeepEqual(k.Emails, []Value{{"katerina@example.com", "internet,home"}}) {
		t.Fatalf("%+v %+v", k.Phones, k.Emails)
	}

	jpeg := []byte("\xff\xd8\xff\xe0 a picture")
	b64 := base64.StdEncoding.EncodeToString(jpeg)
	more := "BEGIN:VCARD\nVERSION:2.1\nFN:Μαρία\nUID:u-1\nTEL;CELL;PREF:tel:+306940000002\n" +
		"PHOTO;ENCODING=BASE64;TYPE=JPEG:" + b64[:10] + "\n " + b64[10:] + "\nEND:VCARD\n" +
		"BEGIN:VCARD\nVERSION:4.0\nFN:Nikos\nN:Stelios;Nikos;;;\nPHOTO:data:image/png;base64," + b64 + "\nEND:VCARD\n" +
		"BEGIN:VCARD\nN:;Eleni;;;\nEND:VCARD\nstray:line\n"
	c = ParseVcards(more)
	if len(c) != 3 {
		t.Fatalf("%d cards", len(c))
	}
	if c[0].UID != "u-1" || c[0].Name != "Μαρία" || !reflect.DeepEqual(c[0].Phones, []Value{{"+306940000002", "cell"}}) {
		t.Fatalf("%+v", c[0])
	}
	if c[0].Photo == nil || string(c[0].Photo.Data) != string(jpeg) || c[0].Photo.Ext != "jpeg" {
		t.Fatalf("photo %+v", c[0].Photo)
	}
	if c[1].Name != "Nikos" || c[1].Photo == nil || c[1].Photo.Ext != "png" {
		t.Fatalf("%+v", c[1])
	}
	if c[2].Name != "Eleni" || len(c[2].UID) != 40 { // the given name of an N without a family name
		t.Fatalf("%+v", c[2])
	}
}

func TestReprStr(t *testing.T) {
	for in, want := range map[string]string{"it's": `"it's"`, `a\b`: `'a\\b'`, "é ü": "'é ü'", "\u200b": `'\u200b'`,
		"\u00a0": `'\xa0'`, "a'\"": `'a\'"'`, "\n": `'\n'`} {
		if got := reprStr(in); got != want {
			t.Errorf("%q: %s, not %s", in, got, want)
		}
	}
	if got := reprPhones([]Value{{"1", ""}, {"2", "cell"}}); got != "[('1', None), ('2', 'cell')]" {
		t.Fatal(got)
	}
}

type host struct{ store *core.Store }

func (h *host) Store() *core.Store      { return h.store }
func (h *host) Emit(M)                  {}
func (h *host) Alert(_, _ string)       {}
func (h *host) ImportLock() sync.Locker { return &sync.Mutex{} }

type M = plugins.M

// archiveWith is an archive in a temporary folder with a phone number and an email, and an
// instance of the plugin.
func archiveWith(t *testing.T, plugin string, settings M) (*core.Store, *plugins.Context) {
	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Address(archive.H("phone", "+306940000001"))
	a.Address(archive.H("email", "katerina@example.com"))
	a.Commit()
	a.Close()
	s, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	iid, err := plugins.Create(s, plugin, "Contacts", settings)
	if err != nil {
		t.Fatal(err)
	}
	return s, plugins.NewContext(&host{s}, *plugins.GetInstance(s, iid))
}

func TestVcardFileSync(t *testing.T) {
	vcf := filepath.Join(t.TempDir(), "all.vcf")
	jpeg := base64.StdEncoding.EncodeToString([]byte("picture"))
	os.WriteFile(vcf, []byte(card30+"BEGIN:VCARD\nFN:Nobody\nUID:x\nTEL:+1 555 0100\nPHOTO;ENCODING=b;TYPE=JPEG:"+jpeg+"\nEND:VCARD\n"), 0o600)
	s, c := archiveWith(t, "vcard-file", M{"path": vcf})
	if err := (VcardFile{}).Sync(c); err != nil {
		t.Fatal(err)
	}
	if n := db.Int(s.Read(), "SELECT count(*) FROM contact"); n != 2 {
		t.Fatalf("%d contacts", n)
	}
	// the number written another way and the email are the archive's; the other numbers are not
	labels := db.Strs(s.Read(), "SELECT ifnull(label, '') FROM contact_address ORDER BY label")
	if !reflect.DeepEqual(labels, []string{"cell", "internet,home"}) {
		t.Fatalf("%v", labels)
	}
	photo := db.Str(s.Read(), "SELECT photo FROM contact WHERE uid = 'x'")
	if !strings.HasSuffix(photo, ".jpg") {
		t.Fatal(photo)
	}
	if b, err := os.ReadFile(filepath.Join(Avatars(), photo)); err != nil || string(b) != "picture" {
		t.Fatal(err, string(b))
	}
	if !strings.Contains(strings.Join(c.LastLines(1), ""), "contacts: 2, addresses found in the archive: 2") {
		t.Fatal(c.LastLines(1))
	}
	// a card gone from the file goes from the archive, with its addresses; the other keeps its id
	id := db.Int(s.Read(), "SELECT id FROM contact WHERE uid = 'bf486a06bef67373b278d12a9e765f0c993ee175'")
	os.WriteFile(vcf, []byte(card30), 0o600)
	if err := (VcardFile{}).RunImport(c); err != nil {
		t.Fatal(err)
	}
	if ids := db.Ints(s.Read(), "SELECT id FROM contact"); !reflect.DeepEqual(ids, []int64{id}) {
		t.Fatalf("%v", ids)
	}
	if n := db.Int(s.Read(), "SELECT count(*) FROM contact_address"); n != 2 {
		t.Fatal(n)
	}
}

func TestCardDav(t *testing.T) {
	var mu sync.Mutex // got is the server's goroutines' as well
	var got struct {
		method, depth, user, pass, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		got.method, got.depth, got.body = r.Method, r.Header.Get("Depth"), string(b)
		got.user, got.pass, _ = r.BasicAuth()
		if got.pass != "app-password" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">`+
			`<d:response><d:href>/dav/addressbooks/users/me/contacts/1.vcf</d:href><d:propstat><d:prop>`+
			`<d:getetag>"1"</d:getetag><card:address-data>`+strings.ReplaceAll(card30, "\r\n", "&#13;\n")+
			`</card:address-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`+
			`<d:response><d:href>/dav/addressbooks/users/me/contacts/</d:href><d:propstat><d:prop><d:getetag/></d:prop>`+
			`<d:status>HTTP/1.1 404 Not Found</d:status></d:propstat></d:response></d:multistatus>`)
	}))
	defer srv.Close()
	s, c := archiveWith(t, "carddav", M{"url": srv.URL + "/dav/addressbooks/users/me/contacts/", "username": "me"})
	if ok, _ := plugins.Check(CardDav{}, c); ok {
		t.Fatal("ready without a password")
	}
	if err := (CardDav{}).Sync(c); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal(err)
	}
	if _, err := c.SaveSecret("password", "app-password"); err != nil {
		t.Skip("no place for a secret:", err)
	}
	defer c.DeleteSecret("password")
	if err := (CardDav{}).Sync(c); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.method != "REPORT" || got.depth != "1" || got.user != "me" || !strings.Contains(got.body, "addressbook-query") {
		t.Fatalf("%+v", got)
	}
	var url, name string
	db.Row(s.Read(), "SELECT url, name FROM contact", nil, &url, &name)
	if url != "/dav/addressbooks/users/me/contacts/1.vcf" || name != "Κατερίνα Οικονόμου" {
		t.Fatal(url, name)
	}
	if n := db.Int(s.Read(), "SELECT count(*) FROM contact_address"); n != 2 {
		t.Fatal(n)
	}
}

// qp is a value in quoted-printable, every byte of it, with a soft break after the first n bytes.
func qp(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i == n {
			b.WriteString("=\n")
		}
		fmt.Fprintf(&b, "=%02X", s[i])
	}
	return b.String()
}

// vCard 2.1 as phones export it: values in quoted-printable over several lines, in a charset of
// their own, and the other ways cards name their types.
func TestVcardEncodings(t *testing.T) {
	cards := ParseVcards("BEGIN:VCARD\r\nVERSION:2.1\r\n" +
		"N;CHARSET=UTF-8;ENCODING=QUOTED-PRINTABLE:" + qp("Οικονόμου", 6) + ";" + qp("Κατερίνα", 4) + ";;;\n" +
		"FN;CHARSET=UTF-8;ENCODING=QUOTED-PRINTABLE:" + qp("Κατερίνα Οικονόμου", 10) + "\n" +
		"TEL;CELL;ENCODING=QUOTED-PRINTABLE:" + qp("+30 694 000 0001", 3) + "\n" +
		"END:VCARD\n" +
		"BEGIN:VCARD\nVERSION:2.1\nFN;CHARSET=WINDOWS-1253;QUOTED-PRINTABLE:=CC=E1=F1=DF=E1\nTEL;TYPE=\"cell,voice\":+1 555 0100\n" +
		"PHOTO;ENCODING=b;TYPE=image/png:" + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n")) + "\n" +
		"NOTE:a back\\\\slash\nEND:VCARD\n")
	if len(cards) != 2 {
		t.Fatalf("%d cards", len(cards))
	}
	if k := cards[0]; k.Name != "Κατερίνα Οικονόμου" || !reflect.DeepEqual(k.Phones, []Value{{"+30 694 000 0001", "cell"}}) {
		t.Fatalf("%+v", k)
	}
	if k := cards[1]; k.Name != "Μαρία" || !reflect.DeepEqual(k.Phones, []Value{{"+1 555 0100", "cell,voice"}}) ||
		k.Photo == nil || k.Photo.Ext != "png" {
		t.Fatalf("%+v", k)
	}
}

// What a card says of its photo goes into a file's name: only a plain word does, whatever a card
// (from a server, a file) holds.
func TestPhotoNames(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	_, c := archiveWith(t, "vcard-file", M{"path": "unused.vcf"})
	cards := ParseVcards("BEGIN:VCARD\nFN:A\nUID:a\nPHOTO:data:image\\..\\..\\evil;base64," + png + "\nEND:VCARD\n" +
		"BEGIN:VCARD\nFN:B\nUID:b\nPHOTO:data:image/x.y;base64," + png + "\nEND:VCARD\n")
	if _, _, err := SaveCards(c, cards); err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`^[0-9a-f]{64}\.png$`)
	for _, p := range db.Strs(c.Store().Read(), "SELECT photo FROM contact ORDER BY uid") {
		if !name.MatchString(p) {
			t.Fatalf("photo file %q", p)
		}
	}
}

// A CardDAV answer that is not the address book's (a redirect, a page) leaves the contacts as they
// are: nothing is taken as an address book without contacts.
func TestCardDavWrongAnswer(t *testing.T) {
	var mu sync.Mutex // mode is the server's goroutines' as well
	mode := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		mode := mode
		mu.Unlock()
		switch {
		case mode == "redirect" && r.URL.Path != "/moved/":
			http.Redirect(w, r, "/moved/", http.StatusMovedPermanently)
		case mode == "page" || r.URL.Path == "/moved/":
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<?xml version="1.0"?><d:error xmlns:d="DAV:"/>`)
		default:
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">`+
				`<d:response><d:href>/c/1.vcf</d:href><d:propstat><d:prop><card:address-data>`+card30+
				`</card:address-data></d:prop></d:propstat></d:response></d:multistatus>`)
		}
	}))
	defer srv.Close()
	s, c := archiveWith(t, "carddav", M{"url": srv.URL + "/c/", "username": "me"})
	if err := (CardDav{}).Sync(c); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"redirect", "page"} {
		mu.Lock()
		mode = m
		mu.Unlock()
		if err := (CardDav{}).Sync(c); err == nil {
			t.Fatalf("%s: taken as the address book", mode)
		}
		if n := db.Int(s.Read(), "SELECT count(*) FROM contact"); n != 1 {
			t.Fatalf("%s: %d contacts left", mode, n)
		}
	}
}

// A CardDAV server that keeps sync tokens (RFC 6578) is asked only what changed: cards changed or
// added (one sent without its card, asked for after), and cards gone; a token it no longer takes
// means all again, an answer cut short is asked on.
func TestCardDavSyncToken(t *testing.T) {
	card := func(uid, name, tel string) string {
		return "BEGIN:VCARD\nVERSION:3.0\nUID:" + uid + "\nFN:" + name + "\nTEL:" + tel + "\nEND:VCARD\n"
	}
	type entry struct{ href, data string }
	// what the server was asked, and how it answers: shared with its goroutines, under mu
	var mu sync.Mutex
	var asked []string
	var answer func(token string) (entries []entry, gone []string, next string, more bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		var out strings.Builder
		out.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">`)
		respond := func(href, data string) {
			fmt.Fprintf(&out, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getetag>"1"</d:getetag>`, href)
			if data != "" {
				fmt.Fprintf(&out, `<card:address-data>%s</card:address-data>`, data)
			}
			out.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
		}
		switch {
		case strings.Contains(body, "sync-collection"):
			token := body[strings.Index(body, "<d:sync-token>")+len("<d:sync-token>") : strings.Index(body, "</d:sync-token>")]
			asked = append(asked, "sync:"+token)
			entries, gone, next, more := answer(token)
			if next == "" {
				w.WriteHeader(403)
				io.WriteString(w, `<?xml version="1.0"?><d:error xmlns:d="DAV:"><d:valid-sync-token/></d:error>`)
				return
			}
			for _, e := range entries {
				respond(e.href, e.data)
			}
			for _, g := range gone {
				fmt.Fprintf(&out, `<d:response><d:href>%s</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>`, g)
			}
			if more {
				out.WriteString(`<d:response><d:href>/c/</d:href><d:status>HTTP/1.1 507 Insufficient Storage</d:status></d:response>`)
			}
			fmt.Fprintf(&out, `<d:sync-token>%s</d:sync-token>`, next)
		case strings.Contains(body, "addressbook-multiget"):
			asked = append(asked, "multiget")
			respond("/c/c.vcf", card("c", "Gamma", "+306940000003"))
		default:
			asked = append(asked, "query")
		}
		out.WriteString(`</d:multistatus>`)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		io.WriteString(w, out.String())
	}))
	defer srv.Close()
	s, c := archiveWith(t, "carddav", M{"url": srv.URL + "/c/", "username": "me"})
	names := func() string {
		return strings.Join(db.Strs(s.Read(), "SELECT name FROM contact ORDER BY name"), ",")
	}
	set := func(f func(token string) ([]entry, []string, string, bool)) {
		mu.Lock()
		answer, asked = f, nil
		mu.Unlock()
	}
	sync := func(want string, calls ...string) {
		t.Helper()
		if err := (CardDav{}).Sync(c); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		got := strings.Join(asked, " ")
		mu.Unlock()
		if names() != want || got != strings.Join(calls, " ") {
			t.Fatalf("contacts %s, asked %v", names(), got)
		}
	}
	// the first time everything, in two parts
	set(func(token string) ([]entry, []string, string, bool) {
		if token == "" {
			return []entry{{"/c/a.vcf", card("a", "Alpha", "+306940000001")}}, nil, "p1", true
		}
		return []entry{{"/c/b.vcf", card("b", "Beta", "+306940000002")}}, nil, "t1", false
	})
	sync("Alpha,Beta", "sync:", "sync:p1")
	// then what changed: Alpha renamed, Beta gone, Gamma new (without its card)
	set(func(token string) ([]entry, []string, string, bool) {
		if token != "t1" {
			t.Errorf("token %q", token) // in the server's goroutine: no Fatal
		}
		return []entry{{"/c/a.vcf", card("a", "Alpha Two", "+306940000001")}, {"/c/c.vcf", ""}}, []string{"/c/b.vcf"}, "t2", false
	})
	sync("Alpha Two,Gamma", "sync:t1", "multiget")
	if n := db.Int(s.Read(), "SELECT count(*) FROM contact_address"); n != 1 { // Alpha's: the archive's only number
		t.Fatal(n, "addresses")
	}
	// a token no longer taken: everything again, what is not in it gone
	set(func(token string) ([]entry, []string, string, bool) {
		if token == "t2" {
			return nil, nil, "", false
		}
		return []entry{{"/c/a.vcf", card("a", "Alpha", "+306940000001")}}, nil, "t3", false
	})
	sync("Alpha", "sync:t2", "sync:")
	// a server that has no sync tokens: the whole address book, asked for at once
	set(func(string) ([]entry, []string, string, bool) { return nil, nil, "", false })
	sync("", "sync:t3", "sync:", "query")
}
