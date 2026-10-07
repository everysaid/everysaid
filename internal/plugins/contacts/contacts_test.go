package contacts

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	var got struct {
		method, depth, user, pass, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
