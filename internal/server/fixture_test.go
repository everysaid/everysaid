package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/plugins"
)

// The tests run on a small archive of invented people made here (the Go demo is not there yet), in
// folders of their own: the environment points Everysaid's folders there before anything is read,
// and the keyring's name is the tests' own, so the user's archive and secrets are never touched.

var (
	testRoot string
	pristine string
)

const base = "localhost:8520"

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "everysaid-server-test-")
	if err != nil {
		panic(err)
	}
	testRoot = root
	for _, name := range []string{"DATA", "CACHE", "CONFIG", "STATE"} {
		dir := filepath.Join(root, strings.ToLower(name))
		os.MkdirAll(dir, 0o700)
		os.Setenv("EVERYSAID_"+name, dir)
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-go-server-test")
	os.Setenv("EVERYSAID_EXTRA_ORIGINS", "")
	os.Unsetenv("EVERYSAID_DEMO")
	os.WriteFile(filepath.Join(root, "config", "config.toml"),
		[]byte("[owner]\nnumbers = [\"+15550000000\"]\nregion = \"US\"\n"), 0o600)
	config.Load()
	registerTestPlugins()
	pristine = filepath.Join(root, "pristine.db")
	if err := buildFixture(pristine); err != nil {
		panic(err)
	}
	code := m.Run()
	config.DeleteSecret("vapid-private")
	os.RemoveAll(root)
	os.Exit(code)
}

// --- the archive ---------------------------------------------------------------------------------

type fixtureIDs struct {
	maria, nick, bob, unnamed, katerina int64 // addresses
}

var fx fixtureIDs

func tinyJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func buildFixture(path string) (err error) {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	defer a.Close()
	defer archive.Recover(&err)
	t0 := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC).UnixMilli()
	src := a.Source("test/fixture", "", "", "")
	phone := func(n string) archive.Handle { return archive.H("phone", n) }
	me := phone("+15550000000")
	maria, nick, bob := phone("+15557770001"), phone("+15558880002"), phone("+15558880003")
	unnamed, callsOnly := phone("+15550100010"), phone("+15550100000")
	katerina := archive.H("email", "katerina.oikonomou@example.com")
	katPap, giannis := phone("+15556660001"), phone("+15556660002")
	fx = fixtureIDs{a.Address(maria), a.Address(nick), a.Address(bob), a.Address(unnamed), a.Address(katerina)}

	n := 0
	msg := func(service string, conv int64, outgoing bool, sender archive.Handle, kind, text string) int64 {
		n++
		var sid int64
		if !outgoing {
			sid = a.Address(sender)
		}
		return a.AddMessage(src, fmt.Sprint("m", n), archive.Message{Service: service, ConversationID: conv,
			TS: t0 + int64(n)*60_000, Outgoing: outgoing, SenderID: sid, Kind: kind, Text: text,
			Key: fmt.Sprintf("%s-%d", service, n)})
	}
	// one person on SMS and WhatsApp
	sms := a.Conversation("sms", []archive.Handle{maria}, "", "")
	msg("sms", sms, false, maria, "text", "Καλημέρα! Τι κάνεις;")
	msg("sms", sms, true, me, "text", "Καλά, εσύ;")
	wa := a.Conversation("whatsapp", []archive.Handle{maria}, "", "")
	for i := 0; i < 5; i++ {
		msg("whatsapp", wa, i%2 == 0, maria, "text", fmt.Sprintf("μήνυμα %d", i))
	}
	// a picture from them
	pic := msg("whatsapp", wa, false, maria, "image", "")
	data := tinyJPEG()
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	rel := filepath.Join("media", sha[:2], sha+".jpg")
	os.MkdirAll(filepath.Join(archive.MediaRoot(), filepath.Dir(rel)), 0o700)
	if err := os.WriteFile(filepath.Join(archive.MediaRoot(), rel), data, 0o600); err != nil {
		return err
	}
	a.Exec("INSERT INTO media (sha256, size, mime, path) VALUES (?, ?, 'image/jpeg', ?)", sha, len(data), filepath.ToSlash(rel))
	a.Exec("INSERT INTO attachment (message_id, sha256, source_id, source_path) VALUES (?, ?, ?, 'pic.jpg')", pic, sha, src)

	// a WhatsApp group of three; a mention, the user's message with receipts
	group := a.Conversation("whatsapp", []archive.Handle{maria, nick, bob, me}, "group-1@g.us", "Friends")
	m1 := msg("whatsapp", group, false, nick, "text", "@Μαρία look at this")
	a.Exec("INSERT INTO mention VALUES (?, ?, ?)", m1, fx.maria, "@Μαρία")
	mine := msg("whatsapp", group, true, me, "text", "Hello all")
	for _, x := range []int64{fx.maria, fx.nick, fx.bob} {
		a.Exec("INSERT INTO receipt VALUES (?, ?, ?, ?, NULL)", mine, x, t0+1_000_000, t0+2_000_000)
	}
	msg("whatsapp", group, false, bob, "text", "καλημέρα σε όλους")

	// another of the same first name and one more make the first names and surnames known
	kp := a.Conversation("whatsapp", []archive.Handle{katPap}, "", "")
	msg("whatsapp", kp, false, katPap, "text", "Γεια!")
	gi := a.Conversation("whatsapp", []archive.Handle{giannis}, "", "")
	msg("whatsapp", gi, false, giannis, "text", "Τα λέμε")

	// people without a name: a number, and an email whose words say who it is
	un := a.Conversation("sms", []archive.Handle{unnamed}, "", "")
	msg("sms", un, false, unnamed, "text", "Your code is 1234")
	a.Exec("INSERT INTO chat_state SELECT 'p' || person_id, 'read_until', ?, ?, 0 FROM person_address WHERE address_id = ?",
		t0+int64(n+1)*60_000, t0, fx.unnamed) // read: nothing unread keeps it in view
	kat := a.Conversation("imessage", []archive.Handle{katerina}, "", "")
	for i := 0; i < 4; i++ {
		msg("imessage", kat, i%2 == 1, katerina, "text", fmt.Sprintf("Κατερίνα, τα λέμε αύριο %d", i))
	}

	// calls: one from a number with no chat
	a.AddCall(src, "c1", archive.Call{Service: "phone", AddressID: a.Address(callsOnly), TS: t0 + 500_000})
	a.AddCall(src, "c2", archive.Call{Service: "phone", AddressID: fx.maria, TS: t0 + 600_000, Answered: true, Duration: 30})
	// names, as the services show them
	a.HandleName(maria, "whatsapp", "Μαρία Ελένη", "book", 0)
	a.HandleName(nick, "whatsapp", "Nick Tom", "profile", 0)
	a.HandleName(bob, "whatsapp", "Bob Tim", "book", 0)
	a.HandleName(katPap, "whatsapp", "Κατερίνα Σοφία", "book", 0)
	a.HandleName(giannis, "whatsapp", "Θανάσης Οικονόμου", "book", 0)
	a.Exec("INSERT INTO plugin_instance (plugin, kind, label, created_at) VALUES ('test-sender', 'source', 'Sender', 0)")
	a.Resolve()
	a.Commit()
	return nil
}

// --- plugins of the tests ------------------------------------------------------------------------

var no = false

// testSource is a source that sends and marks read, recording what it is asked.
type testSource struct{}

type asked struct {
	what, service, text string
	mentions            []plugins.Mention
	file                *plugins.File
	until               int64
}

var askedCh = make(chan asked, 100)

func (testSource) Info() *plugins.Info {
	return &plugins.Info{ID: "test-sender", Name: "Test sender", Kind: "source",
		Services: []string{"whatsapp", "telegram", "viber", "sms"}, CanSend: true, CanReply: true, CanMention: true,
		CanMarkRead: true, CanSendFiles: true,
		Settings: []plugins.Setting{{Key: "udid", Label: "Serial", Pattern: `[0-9A-Fa-f]{8}-?[0-9A-Fa-f]{16}|[0-9A-Fa-f]{40}`},
			{Key: "token", Label: "Token", Type: "secret"}},
		NameWeights: []plugins.Weight{{Key: "whatsapp/book", Weight: 60}, {Key: "whatsapp/profile", Weight: 30}},
		ServiceInfo: map[string]plugins.ServiceInfo{"whatsapp": {Name: "WhatsApp", Color: "#25d366", Short: "WA"},
			"sms": {Name: "SMS", Color: "#888888", Short: "SMS"}, "phone": {Name: "Phone", Color: "#777777", Short: "Tel", Messages: &no}},
	}
}

func (testSource) Check(c *plugins.Context) (bool, string) { return true, "ready" }

func (testSource) Send(ctx context.Context, c *plugins.Context, conv plugins.Conversation, text string, reply *plugins.Reply,
	mentions []plugins.Mention, file *plugins.File) (any, error) {
	askedCh <- asked{what: "send", service: conv.Service, text: text, mentions: mentions, file: file}
	if strings.HasPrefix(text, "write:") { // as a source that has what it sent in the archive
		return writeSent(c, conv, text)
	}
	return M{"id": 1}, nil
}

func writeSent(c *plugins.Context, conv plugins.Conversation, text string) (_ any, err error) {
	a, err := archive.Open(c.Store().Path)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	defer archive.Recover(&err)
	key := fmt.Sprint("sent-", time.Now().UnixNano())
	a.AddMessage(a.Source("test/sent", "", "", ""), key, archive.Message{Service: conv.Service, ConversationID: conv.ID,
		TS: time.Now().UnixMilli(), Outgoing: true, Kind: "text", Text: text, Key: key})
	a.Commit()
	return plugins.Sent{Keys: []string{key}}, nil
}

func (testSource) MarkRead(ctx context.Context, c *plugins.Context, conv plugins.Conversation, until int64) (int, error) {
	askedCh <- asked{what: "read", service: conv.Service, until: until}
	return 1, nil
}

// testLibrary keeps files in a folder (as the folder library does).
type testLibrary struct{}

func (testLibrary) Info() *plugins.Info {
	return &plugins.Info{ID: "test-folder", Name: "Test folder", Kind: "library",
		Settings: []plugins.Setting{{Key: "path", Label: "Folder", Type: "path", Required: true}}}
}

func (testLibrary) Find(c *plugins.Context, sha, path string) (string, error) {
	var found string
	filepath.WalkDir(c.Str("path"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			sum := sha256.Sum256(b)
			if hex.EncodeToString(sum[:]) == sha {
				found, _ = filepath.Rel(c.Str("path"), p)
			}
		}
		return nil
	})
	return found, nil
}

func (testLibrary) Store(c *plugins.Context, path string, meta M) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	name := filepath.Join("2026", filepath.Base(path))
	os.MkdirAll(filepath.Join(c.Str("path"), "2026"), 0o700)
	return name, os.WriteFile(filepath.Join(c.Str("path"), name), b, 0o600)
}

func (testLibrary) Fetch(c *plugins.Context, ref, size string) (*plugins.Fetched, error) {
	return &plugins.Fetched{Path: filepath.Join(c.Str("path"), ref)}, nil
}

// testAnalysis reads a person's chat at once (the action person:<id>), as the local models would.
type testAnalysis struct{}

func (testAnalysis) Info() *plugins.Info {
	return &plugins.Info{ID: "test-analysis", Name: "Test analysis", Kind: "analysis",
		Settings: []plugins.Setting{{Key: "models", Label: "Models", Required: true}}}
}

var analysed = make(chan int64, 10)

func (testAnalysis) Action(c *plugins.Context, name string) error {
	var pid int64
	if _, err := fmt.Sscanf(name, "person:%d", &pid); err != nil {
		return err
	}
	c.Log("reading {pid}", M{"pid": pid})
	analysed <- pid
	return nil
}

// testLive stays connected until told to stop, and its import waits until let go.
type testLive struct{}

var (
	liveRuns    = make(chan int64, 10)
	importGoOn  = make(chan struct{})
	liveFailing = make(chan error, 10)
)

func (testLive) Info() *plugins.Info {
	return &plugins.Info{ID: "test-live", Name: "Test live", Kind: "source", Modes: []string{"import", "live"}}
}

func (testLive) Live(ctx context.Context, c *plugins.Context) error {
	liveRuns <- c.ID
	select {
	case err := <-liveFailing:
		return err
	case <-ctx.Done():
		return nil
	}
}

func (testLive) RunImport(c *plugins.Context) error {
	c.Logf("importing")
	<-importGoOn
	return nil
}

// testBroken breaks outside its connection (its check), as a broken archive breaks a log line.
type testBroken struct{}

var brokenChecks = make(chan struct{}, 100)

func (testBroken) Info() *plugins.Info {
	return &plugins.Info{ID: "test-broken", Name: "Test broken", Kind: "source", Modes: []string{"live"}}
}

func (testBroken) Check(c *plugins.Context) (bool, string) {
	select {
	case brokenChecks <- struct{}{}:
	default:
	}
	if c.Bool("broken") {
		panic("broken")
	}
	return true, "ready"
}

func (testBroken) Live(ctx context.Context, c *plugins.Context) error { <-ctx.Done(); return nil }

func registerTestPlugins() {
	plugins.Register(testBroken{})
	plugins.Register(testLive{})
	plugins.Register(testSource{})
	plugins.Register(testLibrary{})
	plugins.Register(testAnalysis{})
}

// --- the server and a client ---------------------------------------------------------------------

type client struct {
	t   *testing.T
	s   *Server
	ts  *httptest.Server
	hc  *http.Client
	jar *cookiejar.Jar
}

// hostTransport sends every request as one to localhost:8520 (the server's own address).
type hostTransport struct{ inner http.RoundTripper }

func (h hostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = base
	if h := r.Header.Get("X-Test-Host"); h != "" {
		r.Host = h
		r.Header.Del("X-Test-Host")
	}
	return h.inner.RoundTrip(r)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newServer(t *testing.T) *client {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "archive.db")
	b, err := os.ReadFile(pristine)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(db, b, 0o600)
	s, err := New(Options{Archive: db, AuthDB: filepath.Join(dir, "server.db"), Origin: "http://localhost:8520",
		ExtraOrigins: []string{}, Out: io.Discard, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	ts := httptestServer(s)
	hc, jar := newHTTPClient()
	c := &client{t: t, s: s, ts: ts, jar: jar, hc: hc}
	t.Cleanup(func() {
		ts.Close()
		cancel()
		s.Host.Wait(5 * time.Second)
		s.Close()
	})
	return c
}

func httptestServer(s *Server) *httptest.Server { return httptest.NewServer(s) }

func newHTTPClient() (*http.Client, *cookiejar.Jar) {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Transport: hostTransport{http.DefaultTransport}}, jar
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json() M {
	var m M
	json.Unmarshal(r.body, &m)
	return m
}

func (r resp) code() string {
	if d, ok := r.json()["detail"].(map[string]any); ok {
		c, _ := d["code"].(string)
		return c
	}
	return ""
}

// H is the header every change carries.
var H = map[string]string{"X-Everysaid": "1"}

func (c *client) do(method, path string, body any, headers map[string]string) resp {
	c.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
		ct = "application/json"
	case *multipart.Writer:
		panic("use form")
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
		ct = "application/json"
	}
	req, err := http.NewRequest(method, c.ts.URL+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	r, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer r.Body.Close()
	out, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, out}
}

func (c *client) get(path string) resp { return c.do("GET", path, nil, nil) }

func (c *client) getJSON(path string) M {
	c.t.Helper()
	r := c.get(path)
	if r.status != 200 {
		c.t.Fatalf("GET %s: %d %s", path, r.status, r.body)
	}
	return r.json()
}

func (c *client) post(path string, body any) resp { return c.do("POST", path, body, H) }

func (c *client) form(path string, fields map[string]string, file, name, typ string, data []byte) resp {
	c.t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for k, v := range fields {
		w.WriteField(k, v)
	}
	if file != "" {
		hd := make(map[string][]string)
		hd["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="%s"; filename="%s"`, file, name)}
		hd["Content-Type"] = []string{typ}
		p, _ := w.CreatePart(hd)
		p.Write(data)
	}
	w.Close()
	req, _ := http.NewRequest("POST", c.ts.URL+path, &b)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Everysaid", "1")
	r, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer r.Body.Close()
	out, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, out}
}

// login makes a user and signs the client in as them.
func (c *client) login() int64 {
	uid := c.s.Auth.CreateUser("Test", c.s.Store.Path)
	c.setSession(c.s.Auth.NewSession(uid, "go test", "127.0.0.1", "passkey"))
	return uid
}

func (c *client) setSession(token string) {
	u, _ := url.Parse("http://" + base)
	c.jar.SetCookies(u, []*http.Cookie{{Name: Cookie, Value: token, Path: "/"}})
	u2, _ := url.Parse(c.ts.URL)
	c.jar.SetCookies(u2, []*http.Cookie{{Name: Cookie, Value: token, Path: "/"}})
}

func (c *client) clearCookies() {
	c.jar, _ = cookiejar.New(nil)
	c.hc.Jar = c.jar
}

func items(m M) []M {
	var out []M
	list, _ := m["items"].([]any)
	for _, x := range list {
		if mm, ok := x.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}
