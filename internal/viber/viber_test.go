package viber

// The source against a bridge of its own (a Unix socket answering as bridges/viber does): what it
// brings from Viber Desktop's database, and the exact commands each action gives the bridge.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "everysaid-viber-test")
	for k, v := range map[string]string{"EVERYSAID_DATA": "data", "EVERYSAID_CACHE": "cache",
		"EVERYSAID_CONFIG": "config", "EVERYSAID_STATE": "state"} {
		os.Setenv(k, filepath.Join(dir, v))
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test-viber")
	config.Load()
	settleAfter, fileWait, liveWait, checkFor = 0, 0, 20*time.Millisecond, 0
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type host struct {
	store  *core.Store
	mu     sync.Mutex
	events []M
	alerts []string
}

func (h *host) Store() *core.Store { return h.store }
func (h *host) Emit(e M)           { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() }
func (h *host) Alert(title, body string) {
	h.mu.Lock()
	h.alerts = append(h.alerts, title+": "+body)
	h.mu.Unlock()
}

// said is the alerts so far, taken.
func (h *host) said() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.alerts
	h.alerts = nil
	return out
}

// Viber Desktop's tables, as much of them as the importer reads.
const schema = `
CREATE TABLE Contact (ContactID integer primary key, Name TEXT, Number TEXT unique, MID TEXT unique);
CREATE TABLE ChatInfo (ChatID integer primary key, Name varchar(200), Token varchar(50) unique, Flags integer default 0,
  TimeStamp longint not null default 0, PGType integer);
CREATE TABLE ChatRelation (ChatID integer NOT NULL, ContactID integer NOT NULL, primary key (ChatID, ContactID));
CREATE TABLE Events (EventID integer primary key, TimeStamp longint not null, Direction unsigned integer not null,
  Type smallint not null, ContactLongitude signed long default 0, ContactLatitude signed long default 0, ChatID integer,
  ContactID integer, Token unsigned long not null);
CREATE TABLE Messages (EventID integer primary key, Type unsigned integer not null, Status integer not null default 0,
  Subject varchar(500), Body varchar(5000), Flag unsigned integer default 0, PayloadPath varchar(1000),
  ThumbnailPath varchar(100), StickerID unsigned long default 0, PttID varchar(100), Duration signed default 0,
  PGIsLiked integer, Info varchar(7000), AdminsReactions varchar(255), MembersReactions varchar(255), SelfReaction varchar(10));
CREATE TABLE LikeRelation (MessageToken INTEGER NOT NULL, LikeEventID INTEGER NOT NULL, primary key(MessageToken, LikeEventID));
INSERT INTO Contact VALUES (1, NULL, '+15550000000', 'mid-me'), (2, 'Maria', '+15557770001', 'mid-maria'),
  (3, 'Bob', '+15557770002', 'mid-bob');
INSERT INTO ChatInfo VALUES (1, NULL, '5501', 524292, 0, 255), (2, 'Friends', '7701', 0, 0, 255), (4, NULL, NULL, 0, 0, 255);
INSERT INTO ChatRelation VALUES (1, 1), (2, 1), (2, 2), (2, 3), (4, 1), (4, 2);
INSERT INTO Events VALUES (10, 1791000001000, 0, 0, 0, 0, 2, 2, 9001), (11, 1791000002000, 1, 0, 0, 0, 2, 1, 9002),
  (12, 1791000003000, 0, 0, 0, 0, 4, 2, 9003), (13, 1791000004000, 1, 0, 0, 0, 1, 1, 9004);
INSERT INTO Messages (EventID, Type, Body, Info) VALUES (10, 1, 'hello all', '{}'), (11, 1, 'mine', '{}'),
  (12, 1, 'just us', '{}'), (13, 1, 'a note', '{}');
`

// bridge answers as bridges/viber does: ping, snapshot (the database above), check (version, and
// what is missing), and "ok" to the actions, each kept as said; fail, where set, is said in place of
// "ok". What is sent is written as Viber does, its event streamed to the subscribers before the "ok"
// (unless quiet).
type bridge struct {
	sock, data string
	mu         sync.Mutex
	said       []string
	fail       string
	quiet      bool
	version    string
	missing    []string
	l          net.Listener
	conns      []net.Conn
	subs       []net.Conn
}

func newBridge(t *testing.T) *bridge {
	dir, err := os.MkdirTemp("", "vb") // short: a socket's path has a limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	b := &bridge{sock: filepath.Join(dir, "s"), data: filepath.Join(dir, "viber.db"), version: "27.3.0.2"}
	d, err := db.Open(b.data)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(d, schema)
	d.Close()
	b.start(t)
	t.Cleanup(b.stop)
	return b
}

// start listens (again); stop goes away, as Viber quitting does (its subscribers' connections end).
func (b *bridge) start(t *testing.T) {
	l, err := net.Listen("unix", b.sock)
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.l, b.conns = l, nil
	b.mu.Unlock()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			b.mu.Lock()
			b.conns = append(b.conns, c)
			b.mu.Unlock()
			go b.serve(c)
		}
	}()
}

func (b *bridge) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.l != nil {
		b.l.Close()
	}
	for _, c := range b.conns {
		c.Close()
	}
}

func (b *bridge) serve(c net.Conn) {
	defer c.Close()
	line, _ := bufio.NewReader(c).ReadString('\n')
	line = strings.TrimSuffix(line, "\n")
	cmd, rest, _ := strings.Cut(line, " ")
	switch cmd {
	case "ping":
		io.WriteString(c, "pong\n")
	case "subscribe":
		b.mu.Lock()
		b.subs = append(b.subs, c)
		io.WriteString(c, "subscribed\n")
		b.mu.Unlock()
		io.Copy(io.Discard, c) // open until either side ends it
		b.mu.Lock()
		for i, s := range b.subs {
			if s == c {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		return
	case "check":
		b.mu.Lock()
		out := "version " + b.version + "\n"
		for _, n := range []string{"read", "live", "send", "file", "compose", "react", "delete", "read-receipts"} {
			if slices.Contains(b.missing, n) {
				out += "missing " + n + " Something::gone/1\n"
			} else {
				out += "ok " + n + "\n"
			}
		}
		b.mu.Unlock()
		io.WriteString(c, out)
	case "snapshot":
		in, _ := os.ReadFile(b.data)
		os.WriteFile(rest, in, 0o600)
		io.WriteString(c, "ok\n")
	default:
		b.mu.Lock()
		b.said = append(b.said, line)
		fail := b.fail
		b.mu.Unlock()
		if fail != "" {
			io.WriteString(c, "error "+fail+"\n")
		} else {
			b.sent(cmd, rest)
			io.WriteString(c, "ok\n")
		}
	}
}

// sent writes the user's message an action sends (a text, a file, a composed text) into Viber's
// tables, and streams its event.
func (b *bridge) sent(cmd, rest string) {
	var chat int64
	var body, path string
	typ := 1
	switch cmd {
	case "send":
		id, text, _ := strings.Cut(rest, " ")
		chat, _ = strconv.ParseInt(id, 10, 64)
		body = unescapeLine(text)
	case "file":
		id, p, _ := strings.Cut(rest, " ")
		chat, _ = strconv.ParseInt(id, 10, 64)
		path, typ = p, 2
	case "compose":
		var cmp composition
		if json.Unmarshal([]byte(rest), &cmp) != nil || cmp.Edit != 0 {
			return
		}
		chat = cmp.Chat
		for _, p := range cmp.Parts {
			if p.Text != nil {
				body += *p.Text
			} else {
				body += "@\u2068someone\u2069" // as Viber writes a mention
			}
		}
	default:
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.quiet {
		return
	}
	d, err := db.Open(b.data)
	if err != nil {
		return
	}
	defer d.Close()
	id := db.Int(d, "SELECT max(EventID) + 1 FROM Events")
	ts := time.Now().UnixMilli()
	token := time.Now().UnixNano() // unique, as Viber's are
	db.Exec(d, "INSERT INTO Events (EventID, TimeStamp, Direction, Type, ChatID, ContactID, Token) VALUES (?, ?, 1, 0, ?, 1, ?)",
		id, ts, chat, token)
	db.Exec(d, "INSERT INTO Messages (EventID, Type, Status, Body, PayloadPath, Info) VALUES (?, ?, 130, ?, ?, '{}')",
		id, typ, body, path)
	for _, s := range b.subs {
		fmt.Fprintf(s, "%d\t%d\t1\t1\t0\t%d\t%d\t1\t%s\t{}\n", id, chat, ts, token, escapeLine(body))
	}
}

func (b *bridge) take() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.said
	b.said = nil
	return out
}

func instance(t *testing.T, settings M) *plugins.Context {
	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	s, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	iid, err := plugins.Create(s, "viber-desktop", "Viber", settings)
	if err != nil {
		t.Fatal(err)
	}
	return plugins.NewContext(&host{store: s}, *plugins.GetInstance(s, iid))
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: %#v, want %#v", what, got, want)
	}
}

func TestViberDesktop(t *testing.T) {
	b := newBridge(t)
	c := instance(t, M{"socket": b.sock, "send": true})
	if ok, why := (Plugin{}).Check(c); !ok {
		t.Fatal(why)
	}
	if err := (Plugin{}).RunImport(c); err != nil {
		t.Fatal(err)
	}
	q := c.Store().Read()
	eq(t, "the card's last run", db.Int(q, "SELECT count(*) FROM plugin_instance WHERE id = ? AND last_run > 0 AND last_status = 'ok'", c.ID), int64(1))
	eq(t, "brought", db.Strs(q, "SELECT text FROM message ORDER BY ts"), []string{"hello all", "mine", "just us", "a note"})
	conv := func(key string) plugins.Conversation {
		return plugins.Conversation{ID: db.Int(q, "SELECT id FROM conversation WHERE key = ?", key), Key: key, Service: "viber"}
	}
	group, maria, notes := conv("group:7701"), conv("+15557770001"), conv("group:5501")
	maria.ID = db.Int(q, "SELECT conversation_id FROM message WHERE text = 'just us'")
	ref := func(key string) plugins.Ref {
		return plugins.Ref{ID: db.Int(q, "SELECT id FROM message WHERE key = ?", key), Key: key}
	}
	bob := db.Int(q, "SELECT id FROM address WHERE value = '+15557770002'")
	ctx := t.Context()

	// what went, known by its event and returned once it is in the archive
	went := func(res any, conv plugins.Conversation, texts ...string) {
		t.Helper()
		s, ok := res.(plugins.Sent)
		if !ok || len(s.Keys) != len(texts) {
			t.Fatalf("sent: %#v", res)
		}
		for i, k := range s.Keys {
			eq(t, "in the archive", db.Strs(q, "SELECT text FROM message WHERE conversation_id = ? AND key = ? AND outgoing",
				conv.ID, k), []string{texts[i]})
		}
	}
	res, err := (Plugin{}).Send(ctx, c, group, "two\nlines", nil, nil, nil)
	must(t, err)
	went(res, group, "two\nlines")
	res, err = (Plugin{}).Send(ctx, c, maria, "hi", nil, nil, nil)
	must(t, err)
	went(res, maria, "hi")
	res, err = (Plugin{}).Send(ctx, c, notes, "to me", nil, nil, nil)
	must(t, err)
	went(res, notes, "to me")
	eq(t, "sent", b.take(), []string{`send 2 two\nlines`, "send 4 hi", "send 1 to me"})

	// two of the same text at once: each its own
	var wg sync.WaitGroup
	keys := make([][]string, 2)
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, err := (Plugin{}).Send(ctx, c, maria, "same", nil, nil, nil); err == nil {
				keys[i] = res.(plugins.Sent).Keys
			}
		}()
	}
	wg.Wait()
	if len(keys[0]) != 1 || len(keys[1]) != 1 || keys[0][0] == keys[1][0] {
		t.Fatalf("the same text twice: %v", keys)
	}
	b.take()

	// no event (an older bridge, Viber slow): sent all the same, what went not said
	b.quiet, sentWait = true, 50*time.Millisecond
	res, err = (Plugin{}).Send(ctx, c, maria, "unseen", nil, nil, nil)
	must(t, err)
	if _, ok := res.(plugins.Sent); ok {
		t.Fatalf("quiet: %#v", res)
	}
	b.quiet, sentWait = false, 5*time.Second
	b.take()

	// a reply naming a member: typed in, the mention picked as in the list
	res, err = (Plugin{}).Send(ctx, c, group, "@Bob look", &plugins.Reply{ID: ref("9001").ID, Key: "9001"},
		[]plugins.Mention{{Start: 0, Length: 4, AddressID: bob}}, nil)
	must(t, err)
	if s, ok := res.(plugins.Sent); !ok || len(s.Keys) != 1 {
		t.Fatalf("composed: %#v", res)
	}
	said := b.take()
	if len(said) != 1 || !strings.HasPrefix(said[0], "compose ") {
		t.Fatal(said)
	}
	var cmp map[string]any
	must(t, json.Unmarshal([]byte(strings.TrimPrefix(said[0], "compose ")), &cmp))
	eq(t, "composed", cmp, map[string]any{"chat": 2.0, "reply": 10.0,
		"parts": []any{map[string]any{"mention": 3.0}, map[string]any{"text": " look"}}})

	// a file, then its text as a message of its own
	res, err = (Plugin{}).Send(ctx, c, group, "a picture", nil, nil, &plugins.File{Data: []byte("x"), Filename: "p.png"})
	must(t, err)
	went(res, group, "", "a picture")
	said = b.take()
	if len(said) != 2 || !strings.HasPrefix(said[0], "file 2 ") || said[1] != "send 2 a picture" {
		t.Fatal(said)
	}
	if path := strings.TrimPrefix(said[0], "file 2 "); !strings.HasPrefix(path, filepath.Join(config.Cache, "viber")) {
		t.Fatal(path)
	}

	must(t, (Plugin{}).React(ctx, c, group, ref("9001"), "❤️"))
	must(t, (Plugin{}).React(ctx, c, group, ref("9001"), "❤")) // Telegram's spelling of it: still Viber's own
	must(t, (Plugin{}).React(ctx, c, group, ref("9001"), "🙏"))
	must(t, (Plugin{}).React(ctx, c, group, ref("9001"), ""))
	must(t, (Plugin{}).Edit(ctx, c, group, ref("9002"), "mine, fixed"))
	must(t, (Plugin{}).Delete(ctx, c, group, ref("9002")))
	n, err := (Plugin{}).MarkRead(ctx, c, group, 0)
	must(t, err)
	eq(t, "not without the user's yes", n, 0)
	eq(t, "marked deleted here", db.Int(q, "SELECT deleted FROM message WHERE key = '9002'"), int64(1))
	eq(t, "actions", b.take(), []string{"react 10 1", "react 10 1", "react 10 🙏", "unreact 10",
		`compose {"chat":2,"edit":11,"parts":[{"text":"mine, fixed"}]}`, "delete 11"})

	// what the bridge refuses, said
	b.fail = "send-disabled"
	var ue *errs.UserError
	if err := (Plugin{}).Delete(ctx, c, group, ref("9002")); !errors.As(err, &ue) || !strings.Contains(ue.Text, "VIBER_ALLOW_SEND") {
		t.Fatal(err)
	}
	b.fail = ""
	b.take()
	// a chat Viber Desktop does not have, a message it does not have
	if _, err := (Plugin{}).Send(ctx, c, plugins.Conversation{Key: "+15550009999", Service: "viber"}, "x", nil, nil, nil); err == nil {
		t.Fatal("an unknown chat")
	}
	if err := (Plugin{}).React(ctx, c, group, plugins.Ref{Key: "1234"}, "❤️"); err == nil {
		t.Fatal("an unknown message")
	}
	eq(t, "nothing went", b.take(), []string(nil))
}

func TestViberDesktopGates(t *testing.T) {
	b := newBridge(t)
	c := instance(t, M{"socket": b.sock})
	eq(t, "sending off", (Plugin{}).NotSending(c), "Sending is off in this source's settings")
	if _, err := (Plugin{}).Send(t.Context(), c, plugins.Conversation{Key: "group:7701"}, "x", nil, nil, nil); err == nil {
		t.Fatal("sent with sending off")
	}
	if err := (Plugin{}).React(t.Context(), c, plugins.Conversation{Key: "group:7701"}, plugins.Ref{Key: "9001"}, "❤️"); err == nil {
		t.Fatal("reacted with sending off")
	}
	eq(t, "nothing went", b.take(), []string(nil))
	c = instance(t, M{"socket": filepath.Join(t.TempDir(), "none"), "send": true})
	if ok, why := (Plugin{}).Check(c); ok || why != notRunning {
		t.Fatal(why)
	}
	eq(t, "not running", (Plugin{}).NotSending(c), notRunning)
	if err := (Plugin{}).RunImport(c); err == nil {
		t.Fatal("imported without Viber")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Viber stopped and started again (restarted, updated): the live connection waits for it and goes on,
// bringing what came meanwhile, without failing.
func TestLiveWaitsForViber(t *testing.T) {
	b := newBridge(t)
	c := instance(t, M{"socket": b.sock, "interval": 3600})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- (Plugin{}).Live(ctx, c) }()
	count := func() int64 { return db.Int(c.Store().Read(), "SELECT count(*) FROM message") }
	until := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal(what)
			}
		}
	}
	until("the first import", func() bool { return count() == 4 })
	b.stop()
	os.Remove(b.sock)
	d, err := db.Open(b.data)
	must(t, err)
	db.Exec(d, "INSERT INTO Events VALUES (14, 1791000005000, 0, 0, 0, 0, 2, 3, 9005); "+
		"INSERT INTO Messages (EventID, Type, Body, Info) VALUES (14, 1, 'while it was away', '{}')")
	d.Close()
	time.Sleep(100 * time.Millisecond)
	b.start(t)
	until("what came while it was away", func() bool { return count() == 5 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// What this Viber Desktop can do, as the bridge's check says: the source ready or not, the facts,
// an action it can no longer do refused with what is missing.
func TestViberDesktopCheck(t *testing.T) {
	b := newBridge(t)
	c := instance(t, M{"socket": b.sock, "send": true, "read_receipts": true})
	ok, why := (Plugin{}).Check(c)
	eq(t, "all there", []any{ok, why}, []any{true, "ready"})
	eq(t, "facts", (Plugin{}).InfoFacts(c), []plugins.Fact{{Label: "Viber Desktop", Value: "running"},
		{Label: "Viber Desktop version", Value: "27.3.0.2"}})

	b.missing = []string{"react", "compose"}
	ok, why = (Plugin{}).Check(c)
	eq(t, "some missing", []any{ok, why}, []any{true, readyBut + ": replies and edits, reactions"})
	eq(t, "said", i18n.Tr(why, "el"), "Έτοιμο, αλλά αυτή η έκδοση του Viber Desktop δεν υποστηρίζει: απαντήσεις και διορθώσεις, αντιδράσεις")
	eq(t, "facts", (Plugin{}).InfoFacts(c)[2], plugins.Fact{Label: "Missing in this version", Value: "replies and edits, reactions"})
	must(t, (Plugin{}).RunImport(c))
	q := c.Store().Read()
	group := plugins.Conversation{ID: db.Int(q, "SELECT id FROM conversation WHERE key = 'group:7701'"), Key: "group:7701", Service: "viber"}
	ref := plugins.Ref{ID: db.Int(q, "SELECT id FROM message WHERE key = '9001'"), Key: "9001"}
	var ue *errs.UserError
	if err := (Plugin{}).React(t.Context(), c, group, ref, "❤️"); !errors.As(err, &ue) || ue.Text != cannot+": reactions" {
		t.Fatal(err)
	}
	if _, err := (Plugin{}).Send(t.Context(), c, group, "@Bob hi", nil, []plugins.Mention{{Start: 0, Length: 4, AddressID: 1}}, nil); !errors.As(err, &ue) || ue.Text != cannot+": replies and edits" {
		t.Fatal(err)
	}
	eq(t, "sending plain text still", (Plugin{}).NotSending(c), "")
	eq(t, "nothing reached the bridge", b.take(), []string(nil))

	b.missing = []string{"live", "send"}
	ok, why = (Plugin{}).Check(c)
	eq(t, "not ready without live", []any{ok, why}, []any{false, cannot + ": receiving live"})
	eq(t, "not sending", (Plugin{}).NotSending(c), cannot+": sending")
}

// An update of Viber Desktop is said once, with what it no longer has.
func TestViberDesktopUpdateIsSaid(t *testing.T) {
	b := newBridge(t)
	c := instance(t, M{"socket": b.sock})
	h := c.Host().(*host)
	noticeVersion(t.Context(), c)
	eq(t, "the first seen, all there", h.said(), []string(nil))
	eq(t, "kept", c.State["viber_version"], "27.3.0.2")
	noticeVersion(t.Context(), c)
	eq(t, "the same", h.said(), []string(nil))

	b.version = "27.4.0.1"
	noticeVersion(t.Context(), c)
	eq(t, "updated", h.said(), []string{"Viber Desktop: Viber Desktop 27.3.0.2 → 27.4.0.1: everything the bridge uses is there"})
	b.version, b.missing = "27.5.0.0", []string{"react"}
	noticeVersion(t.Context(), c)
	eq(t, "updated, something missing", h.said(), []string{"Viber Desktop: Viber Desktop 27.4.0.1 → 27.5.0.0, missing: reactions"})
	noticeVersion(t.Context(), c)
	eq(t, "said once", h.said(), []string(nil))
	eq(t, "kept", c.State["viber_missing"], "react")
}

// Live receiving that stopped (the checks every interval bring events the bridge did not tell of)
// is said once, until it works again.
func TestLiveReceivingThatStoppedIsSaid(t *testing.T) {
	b := newBridge(t)
	c := instance(t, M{"socket": b.sock, "interval": 1})
	h := c.Host().(*host)
	minEvery = 0
	t.Cleanup(func() { minEvery = 10 * time.Second })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- (Plugin{}).Live(ctx, c) }()
	d, err := db.Open(b.data)
	must(t, err)
	defer d.Close()
	id := int64(100)
	alerted := func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.alerts) > 0 }
	for end := time.Now().Add(15 * time.Second); !alerted() && time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		id++ // a message Viber has that the bridge did not stream
		db.Exec(d, "INSERT INTO Events VALUES (?, 1791000009000, 0, 0, 0, 0, 2, 2, ?)", id, 50000+id)
		db.Exec(d, "INSERT INTO Messages (EventID, Type, Body, Info) VALUES (?, 1, 'unseen', '{}')", id)
	}
	time.Sleep(2500 * time.Millisecond) // more checks: not said again
	cancel()
	must(t, <-done)
	eq(t, "said once", h.said(), []string{"Viber Desktop: Receiving live from Viber Desktop does not work: new messages come only with the check every 1 seconds"})
}
