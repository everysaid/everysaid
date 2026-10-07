package viber

// The source against a bridge of its own (a Unix socket answering as bridges/viber does): what it
// brings from Viber Desktop's database, and the exact commands each action gives the bridge.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
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
	settleAfter, fileWait, liveWait = 0, 0, 20*time.Millisecond
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type host struct {
	store  *core.Store
	mu     sync.Mutex
	events []M
}

func (h *host) Store() *core.Store { return h.store }
func (h *host) Emit(e M)           { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() }
func (h *host) Alert(_, _ string)  {}

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

// bridge answers as bridges/viber does: ping, snapshot (the database above), and "ok" to the
// actions, each kept as said; fail, where set, is said in place of "ok".
type bridge struct {
	sock, data string
	mu         sync.Mutex
	said       []string
	fail       string
	l          net.Listener
	conns      []net.Conn
}

func newBridge(t *testing.T) *bridge {
	dir, err := os.MkdirTemp("", "vb") // short: a socket's path has a limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	b := &bridge{sock: filepath.Join(dir, "s"), data: filepath.Join(dir, "viber.db")}
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
		io.WriteString(c, "subscribed\n")
		io.Copy(io.Discard, c) // open until either side ends it
		return
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
			io.WriteString(c, "ok\n")
		}
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
	ref := func(key string) plugins.Ref {
		return plugins.Ref{ID: db.Int(q, "SELECT id FROM message WHERE key = ?", key), Key: key}
	}
	bob := db.Int(q, "SELECT id FROM address WHERE value = '+15557770002'")
	ctx := t.Context()

	_, err := (Plugin{}).Send(ctx, c, group, "two\nlines", nil, nil, nil)
	must(t, err)
	_, err = (Plugin{}).Send(ctx, c, maria, "hi", nil, nil, nil)
	must(t, err)
	_, err = (Plugin{}).Send(ctx, c, notes, "to me", nil, nil, nil)
	must(t, err)
	eq(t, "sent", b.take(), []string{`send 2 two\nlines`, "send 4 hi", "send 1 to me"})

	// a reply naming a member: typed in, the mention picked as in the list
	_, err = (Plugin{}).Send(ctx, c, group, "@Bob look", &plugins.Reply{ID: ref("9001").ID, Key: "9001"},
		[]plugins.Mention{{Start: 0, Length: 4, AddressID: bob}}, nil)
	must(t, err)
	said := b.take()
	if len(said) != 1 || !strings.HasPrefix(said[0], "compose ") {
		t.Fatal(said)
	}
	var cmp map[string]any
	must(t, json.Unmarshal([]byte(strings.TrimPrefix(said[0], "compose ")), &cmp))
	eq(t, "composed", cmp, map[string]any{"chat": 2.0, "reply": 10.0,
		"parts": []any{map[string]any{"mention": 3.0}, map[string]any{"text": " look"}}})

	// a file, then its text as a message of its own
	_, err = (Plugin{}).Send(ctx, c, group, "a picture", nil, nil, &plugins.File{Data: []byte("x"), Filename: "p.png"})
	must(t, err)
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
