package whatsapp

// The plugin's part of the WhatsApp connection (what the plugin reads of the store and asks of the
// bridge, and how the bridge writes into the archive); the writer itself is tested in
// internal/importers. Everything is made in temporary folders: never a real store, keys.db or
// archive, and no connection to WhatsApp.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"

	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/errs"
	"everysaid/internal/importers"
	"everysaid/internal/keys"
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

// linkDevice gives a store folder a linked device in keys.db (the account 15550009999, device 7).
func linkDevice(t *testing.T, dir string) {
	ctx := context.Background()
	c, err := OpenDevices(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	device := c.NewDevice()
	device.ID = &types.JID{User: "15550009999", Device: 7, Server: types.DefaultUserServer}
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64),
		AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := c.PutDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	d, _ := keys.Open()
	if err := recordDevice(d, dir, *device.ID); err != nil {
		t.Fatal(err)
	}
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
	ms  *Store
	a   *archive.Archive
}

// put writes a message into the archive as the bridge does when it arrives.
func (f *fixture) put(t *testing.T, m StoredMessage) {
	t.Helper()
	b := &importers.WABatch{}
	keep(f.ms, b, m, f.ms.chatNameOf(m.ChatJID))
	if !f.ms.Write(b, waLog.Noop) {
		t.Fatal("not written")
	}
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
	iid, err := plugins.Create(store, "whatsapp-bridge", "WhatsApp", M{"store": wa, "send": true})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{h: &host{store: store}, iid: iid, dir: wa, a: a}
	if f.ms, err = OpenStore(wa, writeTo(f.ctx())); err != nil {
		t.Fatal(err)
	}
	f.ms.storeChat(peer, "Peer", time.Now())
	f.ms.setState("send_enabled", "1")
	return f
}

func TestThePluginTurnsSendingOffWhenTheBridgeBlocksIt(t *testing.T) {
	f := bridgeInstance(t)
	p := Plugin{}
	if !plugins.Sending(p, f.ctx()) {
		t.Fatalf("not sending: %q", p.NotSending(f.ctx()))
	}
	f.ms.setState("send_blocked", "temporary ban: sent to too many people")
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
	f.ms.setState("send_blocked", "") // cleared at the bridge
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
	linkDevice(t, f.dir)
	f.ms.setState("connection", "connected")
	f.ms.exec("INSERT INTO whatsapp_sent (store, at, chat_jid, id, text_hash) VALUES (?, ?, 'c', 'x', 'h')", f.ms.key, time.Now().Unix()-60)
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
		{Recipient: "15551234567", Message: "yes", ReplyTo: "THEIRS", ReplySender: "+15551234567", ReplyText: "a question", ReplyKind: "text"},
		{Recipient: "15551234567", Message: "yes", ReplyTo: "MINE", ReplySender: "me", ReplyText: "an aside", ReplyKind: "text"},
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
	// nothing turns it on again by itself
	watchState(f.ctx())
	if plugins.Sending(p, f.ctx()) {
		t.Fatal("sending again by itself")
	}
	// the user's "allow sending again": the block cleared and the source's sending on, kept
	if err := p.Action(f.ctx(), "unblock"); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.ms.state("send_blocked"); v != "" {
		t.Fatalf("still blocked: %q", v)
	}
	if f.ctx().Settings["send"] != true { // read again from the archive
		t.Fatalf("sending still off: %v", f.ctx().Settings["send"])
	}
}

// Read here, a chat is marked read on the account's devices whatever the setting (the others told
// only with read receipts on): through the live connection only.
func TestMarkingReadNeedsTheConnection(t *testing.T) {
	f := bridgeInstance(t)
	fakeRunning(t, f, true)
	p := Plugin{}
	conv := plugins.Conversation{ID: 1, Key: "+15551234567", Service: "whatsapp"}
	if n, err := p.MarkRead(context.Background(), f.ctx(), conv, 1_790_000_000_500); n != 0 || err != nil { // receipts off
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
	// the store folder holds no database: the bridge's tables are keys.db's
	if _, err := os.Stat(filepath.Join(dir, "messages.db")); !os.IsNotExist(err) {
		t.Fatalf("a database in the store folder: %v", err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the store folder: %v %v", st, err)
	}
}

// What arrives is written into the archive at once, its source tied to the instance, said as new;
// the same again, nothing.
func TestWhatArrivesIsWrittenAtOnce(t *testing.T) {
	f := bridgeInstance(t)
	hello := StoredMessage{ID: "HELLO", ChatJID: peer, Sender: peer, Content: "hello",
		Timestamp: time.Date(2026, 10, 6, 10, 1, 0, 0, time.UTC), Kind: "text"}
	f.put(t, hello)
	var n, tied int64
	db := f.h.store.Read()
	db.QueryRow("SELECT count(*) FROM message WHERE key = 'HELLO'").Scan(&n)
	db.QueryRow("SELECT count(*) FROM source WHERE instance_id = ?", f.iid).Scan(&tied)
	if n != 1 || tied == 0 {
		t.Fatalf("messages %d, sources of the instance %d", n, tied)
	}
	news := func() int {
		n := 0
		for _, e := range f.h.events {
			if e["type"] == "new" {
				n++
			}
		}
		return n
	}
	first := news()
	f.put(t, hello)
	if first != 1 || news() != 1 {
		t.Fatalf("new events %d, then %d", first, news())
	}
	if err := (Plugin{}).RunImport(f.ctx()); err != nil { // what it keeps of chats and people: nothing new
		t.Fatal(err)
	}
	if news() != 1 {
		t.Fatalf("the import said new: %d", news())
	}
}

// A first message (a conversation not in the archive yet) goes only to someone the owner knows: in
// their contacts, or in a group with them.
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

	// someone in a group with the owner (any service), not in the contacts: known too
	met := plugins.Conversation{Key: "+15559990001", Service: "whatsapp"}
	if _, err := (Plugin{}).Send(context.Background(), f.ctx(), met, "hi", nil, nil, nil); !errors.As(err, &ue) || ue.Text != notAContact {
		t.Fatalf("a stranger: %v", err)
	}
	a, err = archive.Open(f.ctx().Store().Path)
	if err != nil {
		t.Fatal(err)
	}
	g := a.Conversation("telegram", []archive.Handle{archive.H("phone", "+15559990001"), archive.H("phone", "+15559990002")}, "-100", "Club")
	a.Exec("UPDATE conversation SET is_group = 1 WHERE id = ?", g)
	a.Commit()
	a.Close()
	if _, err := (Plugin{}).Send(context.Background(), f.ctx(), met, "hi", nil, nil, nil); !errors.As(err, &ue) || ue.Text != "not connected to WhatsApp" {
		t.Fatalf("in a group with the owner: on to the bridge: %v", err)
	}
}

// keys.db keeps what the connection needs, only while it needs it: its record of a message a week
// (it sends, edits, deletes and marks read within that), sends a day; that someone wrote in a chat
// stays (it sends only there), and so does a file still to download.
func TestKeysKeepOnlyWhatTheConnectionNeeds(t *testing.T) {
	f := bridgeInstance(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	f.put(t, StoredMessage{ID: "OLD", ChatJID: peer, Sender: peer, Content: "old", Timestamp: old, Kind: "text"})
	f.put(t, StoredMessage{ID: "PIC", ChatJID: peer, Sender: peer, Timestamp: old, Kind: "image", MediaType: "image",
		URL: "https://mmg.whatsapp.net/v/p", MediaKey: []byte("k"), FileEncSHA256: []byte("e")})
	f.put(t, StoredMessage{ID: "NEW", ChatJID: peer, Sender: peer, Content: "new", Timestamp: time.Now(), Kind: "text"})
	f.ms.sent(peer, "S1", "h")
	f.ms.exec("UPDATE whatsapp_sent SET at = ? WHERE store = ?", time.Now().Add(-25*time.Hour).Unix(), f.ms.key)
	f.ms.sent(peer, "S2", "h")
	if err := (Plugin{}).RunImport(f.ctx()); err != nil {
		t.Fatal(err)
	}
	ids := func(query string) string {
		var out []string
		rows, _ := f.ms.db.Query(query, f.ms.key)
		defer rows.Close()
		for rows.Next() {
			var id string
			rows.Scan(&id)
			out = append(out, id)
		}
		return strings.Join(out, ",")
	}
	if got := ids("SELECT id FROM whatsapp_recent WHERE store = ? ORDER BY id"); got != "NEW" {
		t.Fatalf("the last week's: %s", got)
	}
	if got := ids("SELECT id FROM whatsapp_sent WHERE store = ? ORDER BY id"); got != "S2" {
		t.Fatalf("the last day's sends: %s", got)
	}
	if got := ids("SELECT id FROM whatsapp_media WHERE store = ? ORDER BY id"); got != "PIC" {
		t.Fatalf("still to download: %s", got)
	}
	if got := ids("SELECT jid FROM whatsapp_chat WHERE store = ? AND incoming"); got != peer { // what chatsFor goes by
		t.Fatalf("no longer sends there: %q", got)
	}
	// read on the phone, the message out of the record: the archive's copy marks the chat read
	b := &importers.WABatch{}
	f.ms.markedRead(b, peer, []string{"OLD"}, time.Now())
	if !f.ms.Write(b, waLog.Noop) {
		t.Fatal("not written")
	}
	var until int64
	f.h.store.Read().QueryRow("SELECT value FROM state_report WHERE field = 'read_until'").Scan(&until)
	if until != old.UnixMilli() {
		t.Fatalf("read until %d, want %d", until, old.UnixMilli())
	}
}

// A file still to download is on its way (also once the phone was asked again for it), not one given
// up on, nor an old one the connection no longer looks for; once downloaded it joins its message,
// which the open chats are told of.
func TestFilesPending(t *testing.T) {
	f := bridgeInstance(t)
	pic := func(id string, at time.Time) {
		f.put(t, StoredMessage{ID: id, ChatJID: peer, Sender: peer, Timestamp: at, Kind: "image", MediaType: "image",
			URL: "https://mmg.whatsapp.net/v/p", MediaKey: []byte("k"), FileEncSHA256: []byte("e")})
	}
	pic("PIC", time.Now())
	pic("OLDPIC", time.Now().Add(-30*24*time.Hour))
	f.put(t, StoredMessage{ID: "TXT", ChatJID: peer, Sender: peer, Content: "hi", Timestamp: time.Now(), Kind: "text"})
	mid := func(key string) int64 {
		var id int64
		f.h.store.Read().QueryRow("SELECT id FROM message WHERE key = ?", key).Scan(&id)
		return id
	}
	coming := func() map[int64]bool {
		return Plugin{}.FilesPending(context.Background(), f.ctx(), []int64{mid("PIC"), mid("OLDPIC"), mid("TXT")})
	}
	if got := coming(); !reflect.DeepEqual(got, map[int64]bool{mid("PIC"): true}) {
		t.Fatalf("pending %v", got)
	}
	f.ms.mediaError("PIC", peer, askedAgain)
	if !coming()[mid("PIC")] {
		t.Fatal("asked again of the phone: still coming")
	}
	f.ms.mediaError("PIC", peer, "the phone no longer has the file")
	if coming()[mid("PIC")] {
		t.Fatal("given up, still coming")
	}
	f.ms.exec("UPDATE whatsapp_media SET error = NULL WHERE store = ?", f.ms.key)
	// downloaded: it joins its message, the chats are told, and it is no longer on its way
	rel := mediaFile("image", "", peer, "PIC")
	if err := f.ms.writeMedia(rel, []byte("a picture")); err != nil {
		t.Fatal(err)
	}
	f.ms.exec("UPDATE whatsapp_media SET path = ? WHERE store = ? AND id = 'PIC'", filepath.ToSlash(rel), f.ms.key)
	f.h.events = nil
	if err := f.ms.takeFiles(); err != nil {
		t.Fatal(err)
	}
	var n int
	f.h.store.Read().QueryRow("SELECT count(*) FROM attachment WHERE message_id = ?", mid("PIC")).Scan(&n)
	if n != 1 || coming()[mid("PIC")] {
		t.Fatalf("attachments %d, still coming %v", n, coming())
	}
	changed := false
	for _, e := range f.h.events {
		changed = changed || e["type"] == "changed"
	}
	if !changed {
		t.Fatalf("not told: %v", f.h.events)
	}
	// files not downloaded: nothing is on its way
	pic("PIC2", time.Now())
	if !(Plugin{}).FilesPending(context.Background(), f.ctx(), []int64{mid("PIC2")})[mid("PIC2")] {
		t.Fatal("a second picture not on its way")
	}
	os.WriteFile(config.ConfigFile, []byte("[whatsapp]\nsend = true\ndownload = false\n"), 0o600)
	config.Load()
	defer sendInConfig(t, true)
	if got := (Plugin{}).FilesPending(context.Background(), f.ctx(), []int64{mid("PIC2")}); len(got) != 0 {
		t.Fatalf("downloads off: %v", got)
	}
}
