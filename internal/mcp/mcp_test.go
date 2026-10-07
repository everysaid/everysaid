// Ports tests/test_mcp.py, and tests what whatsapp-mcp gave (a contact's chat, the last
// interaction, messages by sender, a file), the HTTP mode and its token.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/plugins"
)

// Everysaid's folders point into a temporary folder before anything reads them: the user's archive
// and settings are never touched.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "everysaid-mcp-test-")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{"DATA", "CACHE", "CONFIG", "STATE"} {
		dir := filepath.Join(root, strings.ToLower(name))
		os.MkdirAll(dir, 0o700)
		os.Setenv("EVERYSAID_"+name, dir)
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test")
	os.WriteFile(filepath.Join(root, "config", "config.toml"),
		[]byte("[owner]\nnumbers = [\"+15550000000\"]\nregion = \"US\"\ntimezone = \"Europe/Athens\"\n"), 0o600)
	config.Load()
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}

// fixture is a small archive: one person on SMS and WhatsApp (a picture among their messages, a call),
// another, and a WhatsApp group of the three.
type fixture struct {
	store              *core.Store
	maria, nikos       int64 // people
	picture, groupNote int64 // messages
	sha                string
}

var t0 = time.Date(2025, 5, 3, 9, 0, 0, 0, time.UTC).UnixMilli()

func build(t *testing.T) fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	maria, nikos := archive.H("phone", "+306971234567"), archive.H("phone", "+306980000001")
	src := a.Source("test/phone", "test", "", "")
	ma, na := a.Address(maria), a.Address(nikos)
	sms := a.Conversation("sms", []archive.Handle{maria}, "", "")
	wa := a.Conversation("whatsapp", []archive.Handle{maria}, "306971234567@s.whatsapp.net", "")
	group := a.Conversation("whatsapp", []archive.Handle{maria, nikos}, "family@g.us", "Οικογένεια")
	minute := int64(60_000)
	a.AddMessage(src, "1", archive.Message{Service: "sms", ConversationID: sms, TS: t0, SenderID: ma, Kind: "text", Text: "Καλημέρα! Τι κάνεις;"})
	a.AddMessage(src, "2", archive.Message{Service: "sms", ConversationID: sms, TS: t0 + minute, Outgoing: true, Kind: "text", Text: "Όλα καλά, εσύ;"})
	f.picture = a.AddMessage(src, "3", archive.Message{Service: "whatsapp", ConversationID: wa, TS: t0 + 2*minute, SenderID: ma,
		Kind: "image", Key: "W3"})
	f.groupNote = a.AddMessage(src, "4", archive.Message{Service: "whatsapp", ConversationID: group, TS: t0 + 3*minute,
		SenderID: ma, Kind: "text", Text: "Καλημέρα σε όλους", Key: "G4"})
	a.AddMessage(src, "5", archive.Message{Service: "whatsapp", ConversationID: group, TS: t0 + 4*minute, SenderID: na,
		Kind: "text", Text: "Γεια!", Key: "G5"})
	a.AddMessage(src, "6", archive.Message{Service: "whatsapp", ConversationID: group, TS: t0 + 86_400_000, SenderID: na,
		Kind: "image", Key: "G6"}) // a picture never downloaded: no file in the archive
	a.AddCall(src, "c1", archive.Call{Service: "phone", AddressID: ma, TS: t0 + 5*minute, Answered: true, Duration: 65})
	// the picture's file, in the media store
	f.sha = strings.Repeat("ab", 32)
	rel := filepath.Join("media", "ab", f.sha+".jpg")
	full := filepath.Join(archive.MediaRoot(), rel)
	os.MkdirAll(filepath.Dir(full), 0o700)
	if err := os.WriteFile(full, []byte("\xff\xd8\xff\xe0 a picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.Exec("INSERT INTO media (sha256, size, mime, path) VALUES (?, 11, 'image/jpeg', ?)", f.sha, rel)
	a.Exec("INSERT INTO attachment (message_id, sha256, source_id, source_path) VALUES (?, ?, ?, 'x.jpg')", f.picture, f.sha, src)
	a.Exec("UPDATE person SET name = 'Μαρία Ελένη' WHERE id = (SELECT person_id FROM person_address WHERE address_id = ?)", ma)
	a.Exec("UPDATE person SET name = 'Νίκος Γεωργίου' WHERE id = (SELECT person_id FROM person_address WHERE address_id = ?)", na)
	f.maria = a.Int("SELECT person_id FROM person_address WHERE address_id = ?", ma)
	f.nikos = a.Int("SELECT person_id FROM person_address WHERE address_id = ?", na)
	a.Resolve()
	a.Commit()
	a.Close()
	s, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	f.store = s
	return f
}

func connect(t *testing.T, env Env) *sdk.ClientSession {
	t.Helper()
	ct, st := sdk.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := New(env).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call is a tool's answer as JSON values (a list unwrapped from {"result": ...}).
func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) any {
	t.Helper()
	res := callRaw(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s: %s", name, res.Content[0].(*sdk.TextContent).Text)
	}
	var v any
	if err := json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var structured map[string]any
	json.Unmarshal(b, &structured)
	if r, ok := structured["result"]; ok {
		if rb, _ := json.Marshal(r); string(rb) != mustJSON(v) {
			t.Fatalf("%s: structured and text differ", name)
		}
	}
	return v
}

func callRaw(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func obj(v any) map[string]any { return v.(map[string]any) }
func list(v any) []any         { return v.([]any) }

func TestTools(t *testing.T) {
	f := build(t)
	cs := connect(t, Env{Store: f.store})
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, n := range []string{"search_messages", "read_chat", "get_person", "day_timeline", "list_chats", "message_context",
		"find_people", "list_calls", "statistics", "find_media", "send_media_to_library", "set_person_note", "rename_person",
		"get_chat", "get_direct_chat", "last_interaction", "list_messages", "download_media"} {
		if !names[n] {
			t.Errorf("no tool %s", n)
		}
	}
	chats := list(call(t, cs, "list_chats", map[string]any{"limit": 5}))
	if len(chats) == 0 || obj(chats[0])["id"] == "" {
		t.Fatal("no chats")
	}
	page := obj(call(t, cs, "read_chat", map[string]any{"chat": obj(chats[0])["id"], "limit": 10}))
	if len(list(page["items"])) == 0 {
		t.Fatal("an empty page")
	}
	r := obj(call(t, cs, "search_messages", map[string]any{"query": "καλημερα", "limit": 3}))
	if r["total"].(float64) == 0 || obj(list(r["items"])[0])["time"] == nil {
		t.Fatalf("search: %v", r)
	}
	var title []rune
	for _, c := range chats {
		if obj(c)["type"] == "person" {
			title = []rune(obj(c)["title"].(string))
			break
		}
	}
	if people := list(call(t, cs, "find_people", map[string]any{"query": string(title[:4])})); len(people) == 0 {
		t.Fatal("no people")
	}
	// times in the user's zone (Athens: +03:00 in May)
	if got := obj(list(r["items"])[0])["time"]; !strings.HasSuffix(got.(string), "+03:00") {
		t.Fatalf("time %v", got)
	}
	ctx := obj(call(t, cs, "message_context", map[string]any{"message_id": f.groupNote, "n": 2}))
	if ctx["chat"] == nil || len(list(ctx["items"])) == 0 {
		t.Fatalf("context: %v", ctx)
	}
	if e := obj(call(t, cs, "message_context", map[string]any{"message_id": 999999}))["error"]; e != "no such message" {
		t.Fatalf("context of nothing: %v", e)
	}
	p := obj(call(t, cs, "get_person", map[string]any{"person_id": f.maria}))
	if p["chat"] != "p"+itoa(f.maria) || p["calls"].(float64) != 1 || len(list(p["groups"])) != 1 {
		t.Fatalf("person: %v", p)
	}
	if calls := list(call(t, cs, "list_calls", nil)); len(calls) != 1 || obj(calls[0])["with"] == nil {
		t.Fatalf("calls: %v", calls)
	}
	day := obj(call(t, cs, "day_timeline", map[string]any{"date": "2025-05-03"}))
	if n := len(list(day["items"])); n != 6 { // five messages and a call
		t.Fatalf("day: %d items", n)
	}
	if s := obj(call(t, cs, "statistics", nil)); s["first"] == nil {
		t.Fatalf("statistics: %v", s)
	}
	// a bad argument is a tool error, not the end of the server
	if res := callRaw(t, cs, "day_timeline", map[string]any{"date": "3 May"}); !res.IsError {
		t.Fatal("a bad date passed")
	}
	if res := callRaw(t, cs, "read_chat", map[string]any{"chat": "x"}); !res.IsError {
		t.Fatal("a bad chat passed")
	}
}

func itoa(n int64) string { return strings.TrimSpace(mustJSON(n)) }

// until is a day included (the Python's search left it out).
func TestUntilIncluded(t *testing.T) {
	cs := connect(t, Env{Store: build(t).store})
	r := obj(call(t, cs, "search_messages", map[string]any{"query": "καλημερα", "until": "2025-05-03"}))
	if r["total"].(float64) != 2 {
		t.Fatalf("until: %v", r["total"])
	}
	r = obj(call(t, cs, "search_messages", map[string]any{"query": "καλημερα", "since": "2025-05-04"}))
	if r["total"].(float64) != 0 {
		t.Fatalf("since: %v", r["total"])
	}
}

func TestChanges(t *testing.T) {
	f := build(t)
	cs := connect(t, Env{Store: f.store})
	call(t, cs, "set_person_note", map[string]any{"person_id": f.nikos, "note": "football on Wednesdays"})
	if n := obj(call(t, cs, "get_person", map[string]any{"person_id": f.nikos}))["note"]; n != "football on Wednesdays" {
		t.Fatalf("note: %v", n)
	}
	if r := obj(call(t, cs, "rename_person", map[string]any{"person_id": f.nikos, "name": "Νικόλας"})); r["name"] != "Νικόλας" {
		t.Fatalf("rename: %v", r)
	}
	if res := callRaw(t, cs, "rename_person", map[string]any{"person_id": 999999, "name": "x"}); !res.IsError {
		t.Fatal("renamed no one")
	}
	tools, _ := cs.ListTools(context.Background(), nil)
	for _, tl := range tools.Tools {
		writes := tl.Name == "set_person_note" || tl.Name == "rename_person" || tl.Name == "send_media_to_library"
		if tl.Annotations.ReadOnlyHint == writes {
			t.Errorf("%s: read only %v", tl.Name, tl.Annotations.ReadOnlyHint)
		}
	}
}

// What whatsapp-mcp gave, for every service.
func TestWhatsappMCPCoverage(t *testing.T) {
	f := build(t)
	cs := connect(t, Env{Store: f.store})
	maria := "p" + itoa(f.maria)
	// search_contacts / get_direct_chat_by_contact: a number as written anywhere, a name
	for _, contact := range []string{"697 123 4567", "+30 6971234567", "0030-697-1234567", "Ελένη"} {
		got := list(call(t, cs, "get_direct_chat", map[string]any{"contact": contact}))
		if len(got) != 1 || obj(got[0])["chat"] != maria {
			t.Fatalf("%q: %v", contact, got)
		}
	}
	if got := list(call(t, cs, "get_direct_chat", map[string]any{"contact": "6999999999"})); len(got) != 0 {
		t.Fatalf("a stranger: %v", got)
	}
	// get_chat
	chat := obj(call(t, cs, "get_chat", map[string]any{"chat": maria}))
	if chat["type"] != "person" || len(list(chat["services"])) != 3 { // phone, sms, whatsapp
		t.Fatalf("chat: %v", chat)
	}
	groupID := obj(list(obj(call(t, cs, "get_person", map[string]any{"person_id": f.maria}))["groups"])[0])["chat_id"]
	group := obj(call(t, cs, "get_chat", map[string]any{"chat": groupID}))
	if len(list(group["members"])) != 2 {
		t.Fatalf("group: %v", group)
	}
	// get_last_interaction
	last := obj(call(t, cs, "last_interaction", map[string]any{"person_id": f.maria}))
	if obj(last["last"])["type"] != "call" || obj(last["last_in_group"])["id"].(float64) != float64(f.groupNote) ||
		obj(last["last_in_group"])["chat"] != groupID {
		t.Fatalf("last: %v", last)
	}
	// list_messages: by sender, in groups too; by the owner; by text and time
	r := obj(call(t, cs, "list_messages", map[string]any{"sender": maria}))
	if r["total"].(float64) != 3 { // the SMS, the picture, the group's
		t.Fatalf("sender: %v", r)
	}
	r = obj(call(t, cs, "list_messages", map[string]any{"sender": "me"}))
	if r["total"].(float64) != 1 {
		t.Fatalf("me: %v", r)
	}
	r = obj(call(t, cs, "list_messages", map[string]any{"chat": groupID, "query": "γεια", "oldest_first": true}))
	if r["total"].(float64) != 1 || obj(list(r["items"])[0])["chat_title"] != "Οικογένεια" {
		t.Fatalf("query: %v", r)
	}
	r = obj(call(t, cs, "list_messages", map[string]any{"kind": "image", "since": "2025-05-04"}))
	if r["total"].(float64) != 1 {
		t.Fatalf("kind and since: %v", r)
	}
	if res := callRaw(t, cs, "list_messages", nil); !res.IsError {
		t.Fatal("everything listed")
	}
	// hidden services stay hidden
	if err := core.SetSetting(f.store, "hidden_services", []any{"sms"}); err != nil {
		t.Fatal(err)
	}
	if r := obj(call(t, cs, "list_messages", map[string]any{"sender": maria})); r["total"].(float64) != 2 {
		t.Fatalf("hidden sms: %v", r["total"])
	}
	// get_message_context: message_context; download_media
	d := obj(call(t, cs, "download_media", map[string]any{"message_id": f.picture}))
	file := obj(list(d["files"])[0])
	if file["from"] != "archive" || !strings.HasSuffix(file["path"].(string), f.sha+".jpg") {
		t.Fatalf("download: %v", d)
	}
	if e := obj(call(t, cs, "download_media", map[string]any{"message_id": f.picture + 3}))["error"]; e == nil {
		t.Fatal("a file never downloaded, without a source to ask")
	}
}

type fakeFetcher struct{ path string }

func (f fakeFetcher) FetchMedia(ctx context.Context, s *core.Store, messageID int64) (string, error) {
	if f.path == "" {
		return "", ErrNoFetch
	}
	return f.path, nil
}

// Inside the server: a file the archive never had, from the source at work; given itself.
func TestFetchAndInline(t *testing.T) {
	f := build(t)
	p := filepath.Join(t.TempDir(), "x.jpg")
	os.WriteFile(p, []byte("\xff\xd8\xff\xe0 fetched"), 0o600)
	cs := connect(t, Env{Store: f.store, Fetcher: fakeFetcher{p}, Inline: true})
	res := callRaw(t, cs, "download_media", map[string]any{"message_id": f.picture + 3})
	if res.IsError || len(res.Content) != 2 {
		t.Fatalf("fetch: %v", res.Content)
	}
	if img, ok := res.Content[1].(*sdk.ImageContent); !ok || img.MIMEType != "image/jpeg" || !strings.Contains(string(img.Data), "fetched") {
		t.Fatalf("inline: %#v", res.Content[1])
	}
	cs = connect(t, Env{Store: f.store, Fetcher: fakeFetcher{}})
	if e := obj(call(t, cs, "download_media", map[string]any{"message_id": f.picture + 3}))["error"]; !strings.Contains(e.(string), "no source") {
		t.Fatalf("no fetch: %v", e)
	}
}

// folderLib is a library in a folder, enough for the test (the real plugins are elsewhere).
type folderLib struct{}

func (folderLib) Info() *plugins.Info {
	return &plugins.Info{ID: "test-folder", Name: "Test folder", Kind: "library"}
}
func (folderLib) Find(c *plugins.Context, sha, path string) (string, error) { return "", nil }
func (folderLib) Store(c *plugins.Context, path string, meta plugins.M) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	name := filepath.Base(path)
	return name, os.WriteFile(filepath.Join(c.Str("path"), name), data, 0o600)
}
func (folderLib) Fetch(c *plugins.Context, ref, size string) (*plugins.Fetched, error) {
	return &plugins.Fetched{Path: filepath.Join(c.Str("path"), ref)}, nil
}

func TestMediaOnDemand(t *testing.T) {
	plugins.Register(folderLib{})
	f := build(t)
	lib := t.TempDir()
	cs := connect(t, Env{Store: f.store})
	if r := obj(call(t, cs, "send_media_to_library", map[string]any{"sha256": f.sha})); r["error"] != "no photo library is set up" {
		t.Fatalf("no library: %v", r)
	}
	if _, err := plugins.Create(f.store, "test-folder", "Test", plugins.M{"path": lib}); err != nil {
		t.Fatal(err)
	}
	var found []any
	var chat any
	for _, c := range list(call(t, cs, "list_chats", map[string]any{"limit": 50})) {
		chat = obj(c)["id"]
		if found = list(call(t, cs, "find_media", map[string]any{"chat": chat, "kind": "image", "limit": 2})); len(found) > 0 {
			break
		}
	}
	if len(found) == 0 || obj(found[0])["file_here"] != true || obj(found[0])["in_library"] != nil {
		t.Fatalf("found: %v", found)
	}
	r := obj(call(t, cs, "send_media_to_library", map[string]any{"sha256": obj(found[0])["sha256"]}))
	if r["already"] != false {
		t.Fatalf("to the library: %v", r)
	}
	again := list(call(t, cs, "find_media", map[string]any{"chat": chat, "kind": "image", "limit": 2}))
	if obj(again[0])["in_library"] != "Test" {
		t.Fatalf("again: %v", again)
	}
	if r := obj(call(t, cs, "send_media_to_library", map[string]any{"sha256": f.sha})); r["already"] != true {
		t.Fatalf("twice: %v", r)
	}
	// gone from the archive: download_media brings it from the library
	os.Remove(filepath.Join(archive.MediaRoot(), "media", "ab", f.sha+".jpg"))
	d := obj(call(t, cs, "download_media", map[string]any{"sha256": f.sha}))
	if file := obj(list(d["files"])[0]); file["from"] != "library" || !strings.HasPrefix(file["path"].(string), lib) {
		t.Fatalf("from the library: %v", d)
	}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestHTTP(t *testing.T) {
	f := build(t)
	other := build(t)
	stores := map[string]*core.Store{"chk_a": f.store, "chk_b": other.store}
	srv := httptest.NewServer(Handler(func(token string) (Env, bool) {
		s, ok := stores[token]
		return Env{Store: s, Inline: true}, ok
	}))
	defer srv.Close()
	for _, token := range []string{"", "wrong"} {
		req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		resp, err := (&http.Client{Transport: bearer{token}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
			t.Fatalf("token %q: %d", token, resp.StatusCode)
		}
	}
	ctx := context.Background()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx,
		&sdk.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearer{"chk_a"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if chats := list(call(t, cs, "list_chats", nil)); len(chats) == 0 {
		t.Fatal("no chats over HTTP")
	}
	// the scheme's name in any case
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "bearer chk_a")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode == 401 {
		t.Fatalf("bearer in lower case: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	call(t, cs, "set_person_note", map[string]any{"person_id": f.nikos, "note": "only in a"})
	if n := core.Person(other.store, other.nikos)["note"]; n != nil {
		t.Fatalf("written into the other archive: %v", n)
	}
	if n := core.Person(f.store, f.nikos)["note"]; n != "only in a" {
		t.Fatalf("note: %v", n)
	}
}
