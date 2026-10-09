package whatsapp

// The plugin's part of the WhatsApp connection (what the plugin reads of the store and asks of the
// bridge); the importers' part is tested in internal/importers. Everything is made in
// temporary folders: never a real store or archive, and no connection to WhatsApp.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

const peer = "15551234567@s.whatsapp.net"

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "everysaid-whatsapp-test")
	for k, v := range map[string]string{"EVERYSAID_DATA": "data", "EVERYSAID_CACHE": "cache",
		"EVERYSAID_CONFIG": "config", "EVERYSAID_STATE": "state"} {
		os.Setenv(k, filepath.Join(dir, v))
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test-whatsapp")
	config.Load()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// sendInConfig writes config.toml with [whatsapp] send as given.
func sendInConfig(t *testing.T, on bool) {
	os.MkdirAll(config.Config, 0o700)
	text := "[whatsapp]\nsend = false\n"
	if on {
		text = "[whatsapp]\nsend = true\n"
	}
	if err := os.WriteFile(config.ConfigFile, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	config.Load()
}

type host struct {
	store  *core.Store
	mu     sync.Mutex
	alerts [][2]string
	events []M
}

func (h *host) Store() *core.Store { return h.store }
func (h *host) Emit(e M)           { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() }
func (h *host) Alert(title, body string) {
	h.mu.Lock()
	h.alerts = append(h.alerts, [2]string{title, body})
	h.mu.Unlock()
}

type fixture struct {
	h   *host
	iid int64
	dir string // the store folder
	ms  *MessageStore
	a   *archive.Archive
}

func (f *fixture) ctx() *plugins.Context {
	inst := plugins.GetInstance(f.h.store, f.iid)
	return plugins.NewContext(f.h, *inst)
}

func ts(minute int) string { return fmt.Sprintf("2026-10-06 10:%02d:00+03:00", minute) }

// bridgeInstance is an archive with an instance of the plugin on a store folder whose bridge
// allowed sending, and config.toml allowing it.
func bridgeInstance(t *testing.T) *fixture {
	sendInConfig(t, true)
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	store, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	wa := filepath.Join(dir, "store")
	ms, err := OpenMessages(wa)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ms.Close() })
	ms.db.Exec("INSERT INTO chats VALUES (?, 'Peer', ?)", peer, ts(30))
	ms.db.Exec("INSERT INTO bridge_state VALUES ('send_enabled', '1', ?)", ts(0))
	iid, err := plugins.Create(store, "whatsapp-bridge", "WhatsApp", M{"store": wa, "send": true})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{h: &host{store: store}, iid: iid, dir: wa, ms: ms, a: a}
}

func TestThePluginTurnsSendingOffWhenTheBridgeBlocksIt(t *testing.T) {
	f := bridgeInstance(t)
	p := Plugin{}
	if !plugins.Sending(p, f.ctx()) {
		t.Fatalf("not sending: %q", p.NotSending(f.ctx()))
	}
	f.ms.db.Exec("INSERT INTO bridge_state VALUES ('send_blocked', 'temporary ban: sent to too many people', ?)", ts(1))
	c := f.ctx()
	if plugins.Sending(p, c) {
		t.Fatal("sending while blocked")
	}
	if ok, why := p.Check(c); !ok || why != "sending blocked by the bridge: temporary ban: sent to too many people" {
		t.Fatalf("check: %v %q", ok, why)
	}
	watchState(c)
	if want := [][2]string{{"WhatsApp warned the account", "temporary ban: sent to too many people"}}; !reflect.DeepEqual(f.h.alerts, want) {
		t.Fatalf("alerts %v", f.h.alerts)
	}
	c = f.ctx()
	if c.Settings["send"] != false { // off in the app too, until the user says
		t.Fatalf("send setting %v", c.Settings["send"])
	}
	watchState(c)
	if len(f.h.alerts) != 1 { // told once
		t.Fatalf("alerts %v", f.h.alerts)
	}
	f.ms.db.Exec("UPDATE bridge_state SET value = '' WHERE key = 'send_blocked'") // cleared at the bridge
	watchState(f.ctx())
	if plugins.Sending(p, f.ctx()) { // still off: turning it on is the user's
		t.Fatal("sending again by itself")
	}
}

func TestSendingNeedsConfigToAllowIt(t *testing.T) {
	f := bridgeInstance(t)
	sendInConfig(t, false)
	defer sendInConfig(t, true)
	if why := (Plugin{}).NotSending(f.ctx()); why != offInConfig {
		t.Fatalf("got %q", why)
	}
}

func TestItsCardSaysWhatTheStoreSays(t *testing.T) {
	f := bridgeInstance(t)
	p := Plugin{}
	facts := p.InfoFacts(f.ctx())
	if facts[0].Value != "not linked yet (Link a device)" || !strings.HasPrefix(facts[1].Value, "on, 0 of 1000 today") ||
		facts[2].Value != "15 a minute, 300 an hour, 1000 a day; the same text into 3 chats an hour; 5 new chats a day" {
		t.Fatalf("no device: %v", facts)
	}
	// a device linked, the connection last said connected: not so now (it does not run)
	d, _ := sql.Open("sqlite", dsn(filepath.Join(f.dir, "whatsapp.db")))
	d.Exec("CREATE TABLE whatsmeow_device (jid TEXT); INSERT INTO whatsmeow_device VALUES ('1@s.whatsapp.net')")
	d.Close()
	f.ms.setState("connection", "connected")
	f.ms.db.Exec("INSERT INTO sent (at, chat_jid, id, text_hash) VALUES (?, 'c', 'x', 'h')", time.Now().Unix()-60)
	facts = p.InfoFacts(f.ctx())
	if want := []plugins.Fact{{Label: "Connection", Value: "not connected to WhatsApp"}, {Label: "Sending", Value: "on, 1 of 1000 today"}}; !reflect.DeepEqual(facts[:2], want) {
		t.Fatalf("got %v", facts)
	}
	f.ms.setState("connection", "logged_out")
	f.ms.setState("send_blocked", "logged out: 401")
	facts = p.InfoFacts(f.ctx())
	if want := []plugins.Fact{{Label: "Connection", Value: "logged out of WhatsApp"}, {Label: "Sending", Value: "blocked by the bridge: logged out: 401"}}; !reflect.DeepEqual(facts[:2], want) {
		t.Fatalf("got %v", facts)
	}
}

// message puts a message into the archive (as the importer would have), returning its id.
func message(t *testing.T, f *fixture, key, text string, outgoing bool) int64 {
	a := f.a
	conv := a.Conversation("whatsapp", []archive.Handle{archive.H("phone", "+15551234567")}, "+15551234567", "")
	var sender any
	if !outgoing {
		sender = a.Address(archive.H("phone", "+15551234567"))
	}
	id := a.Exec("INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text, key) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		a.Service.ID("whatsapp"), conv, int64(1_790_000_000_000), archive.B2I(outgoing), sender, a.MessageKind.ID("text"), text, key)
	a.Commit()
	n, _ := id.LastInsertId()
	return n
}

func TestAnAnswerTellsTheBridgeWhatItAnswers(t *testing.T) {
	f := bridgeInstance(t)
	theirs := message(t, f, "THEIRS", "a question", false)
	mine := message(t, f, "MINE", "an aside", true)
	conv := plugins.Conversation{ID: 1, Key: "+15551234567", Service: "whatsapp"}
	var got []SendRequest
	for _, r := range []*plugins.Reply{{ID: theirs, Key: "THEIRS"}, {ID: mine, Key: "MINE"}} {
		req, err := request(f.ctx(), conv, "yes", r, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, req)
	}
	plain, _ := request(f.ctx(), conv, "plain", nil, nil, nil)
	file, _ := request(f.ctx(), conv, "", nil, nil, &plugins.File{Data: []byte("\x89PNG"), Filename: "a.png", MimeType: "image/png"})
	got = append(got, plain, file)
	want := []SendRequest{
		{Recipient: "15551234567", Message: "yes", ReplyTo: "THEIRS", ReplySender: "+15551234567", ReplyText: "a question"},
		{Recipient: "15551234567", Message: "yes", ReplyTo: "MINE", ReplySender: "me", ReplyText: "an aside"},
		{Recipient: "15551234567", Message: "plain"},
		{Recipient: "15551234567", Message: "", Media: []byte("\x89PNG"), Filename: "a.png", MimeType: "image/png"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestAMentionIsWrittenAsWhatsAppHasIt(t *testing.T) {
	f := bridgeInstance(t)
	maria := f.a.Address(archive.H("phone", "+15557654321"))
	nikos := f.a.Address(archive.H("id", "98765@lid", "whatsapp"))
	f.a.Commit()
	text, who, err := mentions(f.ctx(), "🙂 @Maria and @Nikos, hi", []plugins.Mention{
		{Start: 2, Length: 6, AddressID: maria}, {Start: 13, Length: 6, AddressID: nikos}})
	if err != nil || text != "🙂 @15557654321 and @98765, hi" || !reflect.DeepEqual(who, []string{"15557654321", "98765@lid"}) {
		t.Fatalf("%q %v %v", text, who, err)
	}
	if !plugins.Manifest(Plugin{}, "en")["can_mention"].(bool) {
		t.Fatal("cannot mention")
	}
	// the same person named twice: both places written, the person given once
	text, who, _ = mentions(f.ctx(), "@Maria @Maria", []plugins.Mention{
		{Start: 0, Length: 6, AddressID: maria}, {Start: 7, Length: 6, AddressID: maria}})
	if text != "@15557654321 @15557654321" || !reflect.DeepEqual(who, []string{"15557654321"}) {
		t.Fatalf("twice: %q %v", text, who)
	}
	if _, _, err := mentions(f.ctx(), "@X", []plugins.Mention{{Start: 0, Length: 2, AddressID: 999999}}); err == nil {
		t.Fatal("an unknown person mentioned")
	}
}

// fakeRunning is a bridge as Run leaves it once connected, but with no client (never connected).
func fakeRunning(t *testing.T, f *fixture, enabled bool) *Bridge {
	b := New(f.dir, Options{Send: enabled, Limits: SendLimits{6, 60, 300, 3, 5}}, Hooks{})
	b.store, b.sender = f.ms, &Sender{store: f.ms, enabled: enabled, limits: b.opts.Limits}
	runningMu.Lock()
	running[key(f.dir)] = b
	runningMu.Unlock()
	t.Cleanup(func() {
		runningMu.Lock()
		delete(running, key(f.dir))
		runningMu.Unlock()
	})
	return b
}

func TestSendingSaysWhyNot(t *testing.T) {
	f := bridgeInstance(t)
	conv := plugins.Conversation{ID: 1, Key: "+15551234567", Service: "whatsapp"}
	p := Plugin{}
	var ue *errs.UserError
	if _, err := p.Send(context.Background(), f.ctx(), conv, "hi", nil, nil, nil); !errors.As(err, &ue) || ue.Text != "not connected to WhatsApp" {
		t.Fatalf("no bridge running: %v", err)
	}
	fakeRunning(t, f, true)
	if _, err := p.Send(context.Background(), f.ctx(), conv, "hi", nil, nil, nil); !errors.As(err, &ue) || ue.Text != "not connected to WhatsApp" {
		t.Fatalf("not connected: %v", err)
	}
	// a block: refused, and the instance's sending goes off
	f.ms.block("temporary ban: x")
	if _, err := p.Send(context.Background(), f.ctx(), conv, "hi", nil, nil, nil); !errors.As(err, &ue) || !strings.HasPrefix(ue.Text, "sending is blocked (temporary ban: x)") {
		t.Fatalf("blocked: %v", err)
	}
	if f.ctx().Settings["send"] != false || len(f.h.alerts) != 1 {
		t.Fatalf("sending still on: %v %v", f.ctx().Settings["send"], f.h.alerts)
	}
	if _, err := p.Send(context.Background(), f.ctx(), conv, "hi", nil, nil, nil); !errors.As(err, &ue) || ue.Text != "Sending is off in this source's settings" {
		t.Fatalf("off: %v", err)
	}
	// clearing the block is the user's action; the source's own setting stays off
	if err := p.Action(f.ctx(), "unblock"); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.ms.state("send_blocked"); v != "" {
		t.Fatalf("still blocked: %q", v)
	}
	if plugins.Sending(p, f.ctx()) {
		t.Fatal("sending again by itself")
	}
}

func TestReadReceiptsOnlyWhereTheUserTurnedThemOn(t *testing.T) {
	f := bridgeInstance(t)
	fakeRunning(t, f, true)
	p := Plugin{}
	conv := plugins.Conversation{ID: 1, Key: "+15551234567", Service: "whatsapp"}
	if n, err := p.MarkRead(context.Background(), f.ctx(), conv, 1_790_000_000_500); n != 0 || err != nil { // off by default
		t.Fatal(n, err)
	}
	on := true
	plugins.Update(f.h.store, f.iid, nil, M{"read_receipts": on}, nil, false)
	// on, but not connected: nothing marked, nothing failed
	if n, err := p.MarkRead(context.Background(), f.ctx(), conv, 1_790_000_000_500); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if floorDiv(1_790_000_000_500, 1000) != 1_790_000_000 || floorDiv(-1500, 1000) != -2 {
		t.Fatal("floorDiv")
	}
	if !plugins.Manifest(p, "en")["can_mark_read"].(bool) {
		t.Fatal("cannot mark read")
	}
}

func TestLinkingNeedsTheLiveConnection(t *testing.T) {
	f := bridgeInstance(t)
	var ue *errs.UserError
	if err := (Plugin{}).Action(f.ctx(), "link"); !errors.As(err, &ue) {
		t.Fatalf("got %v", err)
	}
	idle := (Plugin{}).IdleActions(f.ctx())
	if !reflect.DeepEqual(idle, []string{"unblock"}) { // no device: linking is to do; no block
		t.Fatalf("idle %v", idle)
	}
}

func TestQRIsDrawnAsText(t *testing.T) {
	drawn := DrawQR("2@abc,def,ghi")
	if strings.Count(drawn, "\n") < 10 || strings.ContainsRune(drawn, '\x1b') {
		t.Fatalf("drawn %q", drawn)
	}
}

func TestTheManifest(t *testing.T) {
	m := plugins.Manifest(Plugin{}, "en")
	if m["id"] != "whatsapp-bridge" || m["has_chats"] != false || m["live_default"] != true {
		t.Fatalf("%v", m)
	}
	for _, s := range m["settings"].([]M) {
		if s["key"] == "api" {
			t.Fatal("the REST API is gone")
		}
	}
	// a stored value of the bridge's REST API is kept, unused
	f := bridgeInstance(t)
	plugins.Update(f.h.store, f.iid, nil, M{"api": "http://127.0.0.1:9"}, nil, false)
	if f.ctx().Str("api") != "http://127.0.0.1:9" {
		t.Fatal("api lost")
	}
}

// A store made by the standalone bridge (mattn/go-sqlite3, cgo) opens here unchanged. Give one,
// made in a temporary folder by the bridge's code path, in EVERYSAID_TEST_BRIDGE_STORE; it is
// copied first, never written.
func TestAStoreOfTheBridgeOpens(t *testing.T) {
	from := os.Getenv("EVERYSAID_TEST_BRIDGE_STORE")
	if from == "" {
		t.Skip("EVERYSAID_TEST_BRIDGE_STORE not given")
	}
	dir := t.TempDir()
	for _, name := range []string{"whatsapp.db", "messages.db"} {
		b, err := os.ReadFile(filepath.Join(from, name))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, name), b, 0o600)
	}
	if !HasDevice(dir) {
		t.Fatal("no device seen")
	}
	c, err := OpenDevices(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := c.GetFirstDevice(context.Background())
	if err != nil || dev.ID == nil || dev.ID.User != "15550001111" || dev.ID.Device != 7 || dev.PushName != "Test" {
		t.Fatalf("device %+v %v", dev, err)
	}
	t.Logf("noise key %x, registration %d", dev.NoiseKey.Pub[:], dev.RegistrationID)
	c.Close()
	ms, err := OpenMessages(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	var at time.Time
	if err := ms.db.QueryRow("SELECT timestamp FROM messages WHERE id = 'M'").Scan(&at); err != nil || at.Nanosecond() != 123456789 {
		t.Fatalf("old time %v %v", at, err)
	}
	ms.storeMessage(StoredMessage{ID: "N", ChatJID: "1@s.whatsapp.net", Timestamp: at, Kind: "text"})
	var a, b string
	ms.db.QueryRow("SELECT CAST(timestamp AS TEXT) FROM messages WHERE id = 'M'").Scan(&a)
	ms.db.QueryRow("SELECT CAST(timestamp AS TEXT) FROM messages WHERE id = 'N'").Scan(&b)
	if a != b {
		t.Fatalf("written %q, the bridge wrote %q", b, a)
	}
}

// A store without a linked device: Run makes it, waits to be linked without connecting, and ends
// with its context.
func TestRunWaitsToBeLinked(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	b := New(dir, Options{}, Hooks{})
	go func() { done <- b.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for Running(dir) == nil || !func() bool { _, s, _ := b.parts(); return s != nil }() {
		if time.Now().After(deadline) {
			t.Fatal("not running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := New(dir, Options{}, Hooks{}).Run(ctx); err != ErrRunning {
		t.Fatalf("a second connection on one store: %v", err)
	}
	if b.Linked() || HasDevice(dir) {
		t.Fatal("linked by itself")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if Running(dir) != nil {
		t.Fatal("still running")
	}
	for _, name := range []string{"whatsapp.db", "messages.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

// An import brings the store's messages, ties their source to the instance, says what is new; again,
// nothing.
func TestTheImportBringsTheStoresMessages(t *testing.T) {
	f := bridgeInstance(t)
	f.ms.storeMessage(StoredMessage{ID: "HELLO", ChatJID: peer, Sender: peer, Content: "hello",
		Timestamp: time.Date(2026, 10, 6, 10, 1, 0, 0, time.UTC), Kind: "text"})
	if err := (Plugin{}).RunImport(f.ctx()); err != nil {
		t.Fatal(err)
	}
	var n, tied int64
	db := f.h.store.Read()
	db.QueryRow("SELECT count(*) FROM message WHERE key = 'HELLO'").Scan(&n)
	db.QueryRow("SELECT count(*) FROM source WHERE instance_id = ?", f.iid).Scan(&tied)
	if n != 1 || tied == 0 {
		t.Fatalf("messages %d, sources of the instance %d", n, tied)
	}
	news := 0
	for _, e := range f.h.events {
		if e["type"] == "new" {
			news++
		}
	}
	if err := (Plugin{}).RunImport(f.ctx()); err != nil {
		t.Fatal(err)
	}
	again := 0
	for _, e := range f.h.events {
		if e["type"] == "new" {
			again++
		}
	}
	if news != 1 || again != 1 {
		t.Fatalf("new events %d, then %d", news, again)
	}
}

// A first message (a conversation not in the archive yet) goes only to someone in the owner's contacts.
func TestFirstMessageOnlyToContacts(t *testing.T) {
	f := bridgeInstance(t)
	fakeRunning(t, f, true)
	conv := plugins.Conversation{Key: "+15559990000", Service: "whatsapp"}
	var ue *errs.UserError
	if _, err := (Plugin{}).Send(context.Background(), f.ctx(), conv, "hi", nil, nil, nil); !errors.As(err, &ue) || ue.Text != notAContact || ue.Status != 409 {
		t.Fatalf("not a contact: %v", err)
	}
	a, err := archive.Open(f.ctx().Store().Path)
	if err != nil {
		t.Fatal(err)
	}
	aid := a.Address(archive.H("phone", "+15559990000"))
	a.Exec("UPDATE person SET contact_uid = 'uid-1' WHERE id = (SELECT person_id FROM person_address WHERE address_id = ?)", aid)
	a.Commit()
	a.Close()
	if _, err := (Plugin{}).Send(context.Background(), f.ctx(), conv, "hi", nil, nil, nil); !errors.As(err, &ue) || ue.Text != "not connected to WhatsApp" {
		t.Fatalf("a contact: on to the bridge: %v", err)
	}
}
