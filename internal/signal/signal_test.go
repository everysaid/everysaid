package signal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

func TestMain(m *testing.M) {
	if os.Getenv("EVERYSAID_FAKE_SIGNAL") == "1" {
		fakeHelper()
		os.Exit(0)
	}
	os.Setenv("EVERYSAID_FAKE_SIGNAL", "1") // for the helpers the tests start: this binary, as the fake
	keyring.MockInit()                      // secrets in memory, never the user's keyring
	os.Exit(m.Run())
}

const (
	anna  = "22222222-2222-2222-2222-222222222222"
	bob   = "33333333-3333-3333-3333-333333333333"
	group = "R3JvdXAgaWQgb2YgdGhlIGZyaWVuZHM="
)

// folders: the data, cache, config and state folders in a temporary one, for this test.
func folders(t *testing.T) string {
	t.Cleanup(config.Load) // after the environment is back
	dir := t.TempDir()
	for _, k := range []string{"DATA", "CACHE", "CONFIG", "STATE"} {
		t.Setenv("EVERYSAID_"+k, filepath.Join(dir, strings.ToLower(k)))
	}
	t.Setenv("EVERYSAID_KEYRING", "everysaid-test-signal")
	config.Load()
	return dir
}

func newArchive(t *testing.T, dir string) string {
	path := filepath.Join(dir, "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	return path
}

func ev(t *testing.T, v map[string]any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func apply(t *testing.T, s *Store, events ...map[string]any) {
	for _, e := range events {
		if _, err := s.Apply(ev(t, e)); err != nil {
			t.Fatal(err)
		}
	}
}

func contact(id string) map[string]any { return map[string]any{"kind": "contact", "id": id} }

// scriptEvents are what Signal brings in these tests: contacts, a group, messages of every kind,
// an edit, a reaction, a receipt, the owner's reading on the phone, a call.
func scriptEvents(mediaFile string) []map[string]any {
	return []map[string]any{
		{"event": "contacts", "contacts": []any{
			map[string]any{"aci": anna, "phone": "+306900000001", "name": "Anna Rita", "profile_name": "Anna P"},
			map[string]any{"aci": bob, "phone": nil, "name": nil, "profile_name": "Bob"},
		}},
		{"event": "group", "id": group, "title": "Friends", "revision": 3, "members": []any{fakeOwn, anna, bob}},
		{"event": "message", "chat": contact(anna), "sender": anna, "outgoing": false, "ts": 1000, "server_ts": 1001,
			"text": "hello"},
		{"event": "message", "chat": contact(anna), "sender": fakeOwn, "outgoing": true, "ts": 2000, "server_ts": 2000,
			"text": "hi back", "quote": map[string]any{"ts": 1000, "author": anna, "text": "hello"}},
		{"event": "message", "chat": map[string]any{"kind": "group", "id": group}, "sender": bob, "ts": 3000,
			"text": "look \uFFFC!", "mentions": []any{map[string]any{"start": 5, "length": 1, "aci": anna}},
			"attachments": []any{map[string]any{"content_type": "image/jpeg", "file": mediaFile}}},
		{"event": "message", "chat": map[string]any{"kind": "group", "id": group}, "sender": anna, "ts": 3500,
			"poll": map[string]any{"question": "Where?", "options": []any{"Here", "There"}}},
		{"event": "message", "chat": map[string]any{"kind": "group", "id": group}, "sender": anna, "ts": 3600,
			"group_change": true, "group_revision": 4},
		{"event": "message", "chat": contact(anna), "sender": anna, "ts": 3700,
			"contacts": []any{map[string]any{"name": "Carl", "phones": []any{"+306900000009"}}}},
		{"event": "edit", "chat": contact(anna), "sender": anna, "ts": 1500, "target_ts": 1000, "text": "hello!"},
		{"event": "reaction", "chat": contact(anna), "sender": anna, "ts": 2100, "emoji": "👍", "remove": false,
			"target_author": fakeOwn, "target_ts": 2000},
		{"event": "receipt", "sender": anna, "kind": "read", "timestamps": []any{2000}, "ts": 2200},
		{"event": "read", "messages": []any{map[string]any{"author": anna, "ts": 1000}}, "ts": 2300},
		{"event": "call", "source": "sync", "id": "77", "ts": 4000, "type": "audio", "direction": "incoming",
			"result": "accepted", "chat": contact(anna)},
		{"event": "call", "source": "message", "id": "77", "action": "hangup", "hangup": "normal", "ts": 64000,
			"chat": contact(anna), "sender": anna, "outgoing": false},
		{"event": "call", "source": "message", "id": "78", "action": "offer", "video": true, "ts": 5000,
			"chat": contact(bob), "sender": bob, "outgoing": false},
		{"event": "call", "source": "message", "id": "78", "action": "hangup", "hangup": "normal", "ts": 9000,
			"chat": contact(bob), "sender": bob, "outgoing": false},
	}
}

func TestImport(t *testing.T) {
	dir := folders(t)
	path := newArchive(t, dir)
	media := filepath.Join(dir, "cache", "signal", "1", "media")
	os.MkdirAll(media, 0o700)
	os.WriteFile(filepath.Join(media, "3000-33333333-0.jpg"), []byte("a picture"), 0o600)
	s, err := OpenStore(filepath.Join(dir, "cache", "signal", "1", "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Account(Status{Linked: true, ACI: fakeOwn, Phone: fakePhone})
	apply(t, s, scriptEvents("3000-33333333-0.jpg")...)

	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	n, err := Import(a, s.Path, media, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n.Messages != 6 || n.Calls != 2 || n.Files != 1 {
		t.Fatalf("counts %+v", n)
	}
	str := func(q string, args ...any) string { return db.Str(a.DB, q, args...) }
	num := func(q string, args ...any) int64 { return db.Int(a.DB, q, args...) }

	// conversations by Signal's ids; the group with its members, the owner left out
	if num("SELECT count(*) FROM conversation WHERE key = ? AND NOT is_group", anna) != 1 {
		t.Fatal("anna's chat")
	}
	if str("SELECT title FROM conversation WHERE key = ? AND is_group", group) != "Friends" {
		t.Fatal("the group")
	}
	members := db.Strs(a.DB, "SELECT a.value FROM conversation_member cm JOIN conversation c ON c.id = cm.conversation_id "+
		"JOIN address a ON a.id = cm.address_id WHERE c.key = ? ORDER BY a.value", group)
	if strings.Join(members, ",") != "+306900000001,"+bob {
		t.Fatalf("members %v", members)
	}
	// people: by number where Signal shows it, their ACI joining them; else by ACI
	if num("SELECT count(DISTINCT pa.person_id) FROM address a JOIN person_address pa ON pa.address_id = a.id "+
		"WHERE a.value IN (?, ?)", anna, "+306900000001") != 1 {
		t.Fatal("anna's number and ACI are one person")
	}
	// keys, the edit (its last content), the reply, the mention as @Name
	if str("SELECT text FROM message WHERE key = ?", anna+":1000") != "hello!" ||
		num("SELECT edited FROM message WHERE key = ?", anna+":1000") != 1 {
		t.Fatal("edited message")
	}
	if num("SELECT reply_to FROM message WHERE key = ?", fakeOwn+":2000") != num("SELECT id FROM message WHERE key = ?", anna+":1000") {
		t.Fatal("reply")
	}
	if str("SELECT text FROM message WHERE key = ?", bob+":3000") != "look @Anna Rita!" {
		t.Fatalf("mention text %q", str("SELECT text FROM message WHERE key = ?", bob+":3000"))
	}
	if str("SELECT m.token FROM mention m JOIN message x ON x.id = m.message_id WHERE x.key = ?", bob+":3000") != "@Anna Rita" {
		t.Fatal("mention row")
	}
	kind := func(key string) string {
		return str("SELECT k.name || '/' || coalesce(m.subtype, '') FROM message m JOIN message_kind k ON k.id = m.kind_id WHERE m.key = ?", key)
	}
	for key, want := range map[string]string{bob + ":3000": "image/", anna + ":3500": "text/poll",
		anna + ":3600": "system/group event", anna + ":3700": "contact/"} {
		if got := kind(key); got != want {
			t.Fatalf("%s: %s, not %s", key, got, want)
		}
	}
	if str("SELECT text FROM message WHERE key = ?", anna+":3500") != "Where?\n- Here\n- There" {
		t.Fatal("poll text")
	}
	// the file, in the media store, tied to its message
	var sha, stored string
	if !db.Row(a.DB, "SELECT md.sha256, md.path FROM attachment x JOIN media md ON md.sha256 = x.sha256 "+
		"JOIN message m ON m.id = x.message_id WHERE m.key = ?", []any{bob + ":3000"}, &sha, &stored) {
		t.Fatal("no attachment")
	}
	if b, _ := os.ReadFile(filepath.Join(archive.MediaRoot(), stored)); string(b) != "a picture" {
		t.Fatal("the stored file")
	}
	// the reaction to the owner's message, the receipt, how far the owner read
	if str("SELECT r.emoji FROM reaction r JOIN message m ON m.id = r.message_id WHERE m.key = ?", fakeOwn+":2000") != "👍" {
		t.Fatal("reaction")
	}
	if num("SELECT r.read_at FROM receipt r JOIN message m ON m.id = r.message_id WHERE m.key = ?", fakeOwn+":2000") != 2200 {
		t.Fatal("receipt")
	}
	// (read_until needs the source tied to an instance: none here, see TestPlugin)
	// calls: answered, its length from the phone's record to the hang-up; one missed
	var answered, duration int64
	var detail *string
	db.Row(a.DB, "SELECT answered, duration, detail FROM call WHERE key = '77'", nil, &answered, &duration, &detail)
	if answered != 1 || duration != 60 || detail != nil {
		t.Fatalf("call 77: %d %d %v", answered, duration, detail)
	}
	db.Row(a.DB, "SELECT answered, detail FROM call WHERE key = '78'", nil, &answered, &detail)
	if answered != 0 || detail == nil || *detail != "missed" || num("SELECT video FROM call WHERE key = '78'") != 1 {
		t.Fatal("call 78")
	}
	// the names Signal shows: the address book's and the profile's
	names := db.Strs(a.DB, "SELECT kind || ':' || name FROM handle_name ORDER BY kind, name")
	if strings.Join(names, ",") != "book:Anna Rita,profile:Anna P,profile:Bob" {
		t.Fatalf("names %v", names)
	}
	if num("SELECT count(*) FROM account x JOIN address a ON a.id = x.address_id WHERE a.value = ?", fakeOwn) != 1 {
		t.Fatal("the owner's ACI is an account")
	}

	// later: a reaction taken back, a message deleted; then nothing again
	apply(t, s,
		map[string]any{"event": "reaction", "chat": contact(anna), "sender": anna, "ts": 2500, "remove": true,
			"emoji": "👍", "target_author": fakeOwn, "target_ts": 2000},
		map[string]any{"event": "delete", "chat": map[string]any{"kind": "group", "id": group}, "sender": bob,
			"ts": 3100, "target_author": bob, "target_ts": 3000})
	n, err = Import(a, s.Path, media, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n.Messages != 0 || n.Changes != 2 {
		t.Fatalf("second import %+v", n)
	}
	if num("SELECT count(*) FROM reaction") != 0 || num("SELECT deleted FROM message WHERE key = ?", bob+":3000") != 1 {
		t.Fatal("changes")
	}
	if n, _ = Import(a, s.Path, media, 0, nil); n != (Counts{}) {
		t.Fatalf("third import %+v", n)
	}
	if num("SELECT count(*) FROM message") != 6 || num("SELECT count(*) FROM call") != 2 || num("SELECT count(*) FROM attachment") != 1 {
		t.Fatal("nothing twice")
	}
}

func TestWithMentions(t *testing.T) {
	name := func(aci string) string { return map[string]string{"a": "Μαρία", "b": "Bob"}[aci] }
	// an emoji before the mention: two UTF-16 units
	text, toks := withMentions("😀 \uFFFC and \uFFFC", []mentionEv{{3, 1, "a"}, {9, 1, "b"}}, name)
	if text != "😀 @Μαρία and @Bob" || len(toks) != 2 {
		t.Fatalf("%q %v", text, toks)
	}
	if text, _ := withMentions("x", []mentionEv{{5, 1, "a"}}, name); text != "x" {
		t.Fatal("out of range")
	}
}

func TestKeys(t *testing.T) {
	a, ts, ok := SplitKey(Key("PNI:abc", 42))
	if !ok || a != "PNI:abc" || ts != 42 {
		t.Fatal(a, ts, ok)
	}
	if !isGroupKey(group) || isGroupKey(anna) || isGroupKey("PNI:"+anna) {
		t.Fatal("isGroupKey")
	}
}

type host struct {
	store  *core.Store
	mu     sync.Mutex
	events []M
}

func (h *host) Store() *core.Store { return h.store }
func (h *host) Emit(e M) {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
}
func (h *host) Alert(title, body string) {}

func (h *host) saw(kind string) []M {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []M
	for _, e := range h.events {
		if e["type"] == kind {
			out = append(out, e)
		}
	}
	return out
}

func newPlugin(t *testing.T, settings M) (*plugins.Context, *host, string) {
	dir := folders(t)
	path := newArchive(t, dir)
	store, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	h := &host{store: store}
	all := M{"helper": os.Args[0]}
	for k, v := range settings {
		all[k] = v
	}
	id, err := plugins.Create(store, "signal", "Signal", all)
	if err != nil {
		t.Fatal(err)
	}
	c := plugins.NewContext(h, *plugins.GetInstance(store, id))
	return c, h, path
}

func writeScript(t *testing.T, c *plugins.Context, events []map[string]any) {
	os.MkdirAll(StoreDir(c), 0o700)
	var b strings.Builder
	for _, e := range events {
		b.Write(ev(t, e))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(StoreDir(c), "script.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lines(t *testing.T, path string) []map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		json.Unmarshal([]byte(l), &m)
		out = append(out, m)
	}
	return out
}

func TestPlugin(t *testing.T) {
	c, h, _ := newPlugin(t, M{"read_receipts": true})
	p := Plugin{}
	if ok, why := p.Check(c); !ok {
		t.Fatal(why)
	}
	// not linked: an import says so
	if err := p.RunImport(c); err == nil || !strings.Contains(err.Error(), "Not linked") {
		t.Fatalf("import before linking: %v", err)
	}
	if f := p.InfoFacts(c); f[0].Value != "not linked yet (Link this computer)" {
		t.Fatalf("facts before linking %v", f)
	}
	// a code the phone never scans: said so, in a while
	os.MkdirAll(StoreDir(c), 0o700)
	os.WriteFile(filepath.Join(StoreDir(c), "no-scan"), nil, 0o600)
	linkWait = 300 * time.Millisecond
	err := p.Action(c, "link")
	linkWait = 5 * time.Minute
	os.Remove(filepath.Join(StoreDir(c), "no-scan"))
	if err == nil || !strings.Contains(err.Error(), "No code was scanned") {
		t.Fatalf("link not scanned: %v", err)
	}
	// linking: the code for the phone, said to the user's devices; then the account is known, and
	// the first sync runs at once (the phone asked for its contacts, which did not come by themselves)
	if err := p.Action(c, "link"); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(c.LastLines(50), "\n")
	for _, want := range []string{"Linked to Signal as " + fakePhone, "Bringing what waited on Signal's server",
		"Up to date with Signal", "Asked the phone for its contacts"} {
		if !strings.Contains(log, want) {
			t.Fatalf("no %q in the log:\n%s", want, log)
		}
	}
	if len(lines(t, filepath.Join(StoreDir(c), "sync.jsonl"))) != 1 {
		t.Fatal("contacts asked once")
	}
	qr := h.saw("plugin_qr")
	if len(qr) != 2 || !strings.HasPrefix(qr[1]["code"].(string), "sgnl://linkdevice") { // one a link timed out
		t.Fatalf("plugin_qr %v", qr)
	}
	if c.State["aci"] != fakeOwn || c.State["phone"] != fakePhone {
		t.Fatalf("state %v", c.State)
	}
	if err := p.Action(c, "link"); err == nil || !strings.Contains(err.Error(), "Already linked") {
		t.Fatalf("second link: %v", err)
	}
	if c.Secret("passphrase") == "" {
		t.Fatal("the store's passphrase is kept")
	}

	// an import brings what waited on the server
	writeScript(t, c, scriptEvents("none.jpg")[:5])
	if err := p.RunImport(c); err != nil {
		t.Fatal(err)
	}
	if len(h.saw("new")) == 0 {
		t.Fatal("no new event")
	}
	if !strings.Contains(strings.Join(c.LastLines(20), "\n"), "Contacts from the phone: 2") || c.State["contacts_synced"] != true {
		t.Fatal("contacts not said")
	}
	facts := p.InfoFacts(c)
	if len(facts) != 4 || facts[2].Value != "2" || facts[3].Value != "1" {
		t.Fatalf("facts %v", facts)
	}
	num := func(q string, args ...any) int64 { return db.Int(c.Store().Read(), q, args...) }
	if num("SELECT count(*) FROM message") != 3 {
		t.Fatalf("messages: %d", num("SELECT count(*) FROM message"))
	}
	if num("SELECT count(*) FROM source WHERE name = ? AND instance_id = ?", SourceName(fakeOwn), c.ID) != 1 {
		t.Fatal("the source is the instance's")
	}
	chats, err := p.Chats(c)
	if err != nil || len(chats) != 2 {
		t.Fatalf("chats %v %v", chats, err)
	}

	// sending into the group: a mention as Signal's apps write it, a reply quoting its message, a file
	var conv plugins.Conversation
	db.Row(c.Store().Read(), "SELECT id, key FROM conversation WHERE key = ?", []any{group}, &conv.ID, &conv.Key)
	annaAddr := num("SELECT id FROM address WHERE value = '+306900000001'")
	replyID := num("SELECT id FROM message WHERE key = ?", bob+":3000")
	got, err := p.Send(context.Background(), c, conv, "hi @Anna Rita 😀!",
		&plugins.Reply{ID: replyID, Key: bob + ":3000"},
		[]plugins.Mention{{Start: 3, Length: 10, AddressID: annaAddr}},
		&plugins.File{Data: []byte("bytes"), Filename: "a.txt", MimeType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	sent := lines(t, filepath.Join(StoreDir(c), "sent.jsonl"))
	req := sent[0]
	if req["text"] != "hi \uFFFC 😀!" {
		t.Fatalf("text %q", req["text"])
	}
	ms := req["mentions"].([]any)[0].(map[string]any)
	if ms["start"].(float64) != 3 || ms["length"].(float64) != 1 || ms["aci"] != anna {
		t.Fatalf("mention %v", ms)
	}
	q := req["quote"].(map[string]any)
	if q["author"] != bob || q["ts"].(float64) != 3000 {
		t.Fatalf("quote %v", q)
	}
	if req["chat"].(map[string]any)["kind"] != "group" {
		t.Fatal("to the group")
	}
	att := req["attachments"].([]any)[0].(map[string]any)
	if att["bytes"] != "bytes" || att["filename"] != "a.txt" || att["content_type"] != "text/plain" {
		t.Fatalf("file %v", att)
	}
	key := got.(M)["id"].(string)
	if num("SELECT count(*) FROM message WHERE key = ? AND outgoing", key) != 1 {
		t.Fatalf("what was sent is in the archive (%s)", key)
	}
	if left, _ := filepath.Glob(filepath.Join(CacheDir(c), "send-*")); len(left) != 0 {
		t.Fatal("the file to send is left behind")
	}

	// live: connected, then read receipts through it; it ends with its context
	writeScript(t, c, scriptEvents("none.jpg"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Live(ctx, c) }()
	deadline := time.Now().Add(20 * time.Second)
	for num("SELECT count(*) FROM call") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("live did not import: %v", c.LastLines(30))
		}
		time.Sleep(50 * time.Millisecond)
	}
	annaConv := plugins.Conversation{Key: anna}
	db.Row(c.Store().Read(), "SELECT id FROM conversation WHERE key = ?", []any{anna}, &annaConv.ID)
	marked, err := p.MarkRead(context.Background(), c, annaConv, 4000)
	if err != nil || marked != 1 { // 1000 was read on the phone already; 3700 is not
		t.Fatalf("marked %d %v", marked, err)
	}
	read := lines(t, filepath.Join(StoreDir(c), "read.jsonl"))
	if len(read) != 1 {
		t.Fatalf("read %v", read)
	}
	if v := num("SELECT max(value) FROM state_report WHERE field = 'read_until'"); v != 3700 {
		t.Fatalf("read_until %d", v)
	}
	facts = p.InfoFacts(c)
	if facts[0].Value != "connected to Signal" {
		t.Fatalf("facts %v", facts)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("live did not end")
	}
}

func TestNoHelper(t *testing.T) {
	c, _, _ := newPlugin(t, M{"helper": filepath.Join(t.TempDir(), "nothing")})
	if ok, _ := (Plugin{}).Check(c); ok {
		t.Fatal("ready without a helper")
	}
	if err := (Plugin{}).RunImport(c); err == nil {
		t.Fatal("ran without a helper")
	}
}
