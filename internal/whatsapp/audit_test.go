package whatsapp

// What the audit of the move into the program found, each kind with its check: the connection's end
// losing nothing, what whatsmeow leaves undone (logged out, banned, failed), channels and status kept
// out, edits only by their authors, a person's two chats (number and LID), downloads, and the store
// folder kept private. Never connected: the client is made on a device store in a temporary
// folder, and writes into an archive of its own.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/importers"
)

// probe reads an archive of the tests.
type probe struct {
	t    *testing.T
	path string
}

func (p probe) read(fn func(d *sql.DB)) {
	d, err := db.ReadOnly(p.path)
	if err != nil {
		p.t.Fatal(err)
	}
	defer d.Close()
	fn(d)
}

// Int is the first column of a query's first row.
func (p probe) Int(query string, args ...any) (n int) {
	p.read(func(d *sql.DB) { n = int(db.Int(d, query, args...)) })
	return n
}

// Str is the first column of a query's first row, as text.
func (p probe) Str(query string, args ...any) (s string) {
	p.read(func(d *sql.DB) { s = db.Str(d, query, args...) })
	return s
}

// testArchive is an archive of its own, the bridge's way into it (as the plugin's writeTo, without
// its instance), and what reads it.
func testArchive(t *testing.T, dir string) (Writer, probe) {
	path := filepath.Join(t.TempDir(), "archive.db")
	write := func(fn func(w *importers.WhatsAppWriter) error) (err error) {
		keys, err := Devices(context.Background())
		if err != nil {
			return err
		}
		a, err := archive.Open(path)
		if err != nil {
			return err
		}
		defer a.Close()
		defer archive.Recover(&err)
		w := importers.NewWhatsAppWriter(a, keys, key(dir))
		if err := fn(w); err != nil {
			return err
		}
		w.Done()
		return nil
	}
	if err := write(func(*importers.WhatsAppWriter) error { return nil }); err != nil { // made
		t.Fatal(err)
	}
	return write, probe{t, path}
}

// offline is a bridge on a temporary store folder with a client that never connects, writing into
// an archive of its own (in reads it).
func offline(t *testing.T) (b *Bridge, client *whatsmeow.Client, store *Store, in probe) {
	dir := t.TempDir()
	write, in := testArchive(t, dir)
	c, err := OpenDevices(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	device := c.NewDevice() // as linked: the account 15550009999, this device 7
	device.ID = &types.JID{User: "15550009999", Device: 7, Server: types.DefaultUserServer}
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64),
		AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := c.PutDevice(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	client = whatsmeow.NewClient(device, nil)
	store, err = OpenStore(dir, write)
	if err != nil {
		t.Fatal(err)
	}
	b = New(dir, Options{}, Hooks{Write: write})
	b.store, b.client = store, client
	b.sender = &Sender{client: client, store: store, logger: b.log, enabled: true, limits: SendLimits{6, 60, 300, 3, 5}}
	return b, client, store, in
}

func text(chat, sender types.JID, id, body string, fromMe bool) *events.Message {
	return &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: sender,
		IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer}, ID: id, Timestamp: time.Now()},
		Message: &waE2E.Message{Conversation: proto.String(body)}}
}

// whatsmeow acknowledges a history-sync notification to the phone at once and downloads the history
// later, apart from the connection: the end waits for it (and for what is being handled), and
// nothing is handled or sent after.
func TestTheEndLosesNoHistoryAlreadyAcknowledged(t *testing.T) {
	b, client, store, in := offline(t)
	defer func(w time.Duration) { historyWait = w }(historyWait)
	historyWait = 5 * time.Second
	person := types.NewJID("15551234567", types.DefaultUserServer)
	notification := &waE2E.HistorySyncNotification{DirectPath: proto.String("/v/t62/history")}
	b.handle(client, store, &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{
		Chat: person, Sender: person, IsFromMe: true}, ID: "N"},
		Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION.Enum(), HistorySyncNotification: notification}}})

	ended := make(chan struct{})
	go func() { b.end(client); close(ended) }()
	select {
	case <-ended:
		t.Fatal("ended with the history still to come")
	case <-time.After(300 * time.Millisecond):
	}
	b.handle(client, store, &events.HistorySync{Notification: notification, Data: &waHistorySync.HistorySync{
		Conversations: []*waHistorySync.Conversation{{ID: proto.String(person.String()), Messages: []*waHistorySync.HistorySyncMsg{{
			Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String(person.String()), ID: proto.String("OLD")},
				MessageTimestamp: proto.Uint64(uint64(time.Now().Unix())), Message: &waE2E.Message{Conversation: proto.String("hi")}}}}}}}})
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("did not end once the history came")
	}
	if in.Int("SELECT count(*) FROM message WHERE key = 'OLD'") != 1 {
		t.Fatal("the history was lost")
	}
	b.handle(client, store, text(person, person, "LATE", "late", false))
	if in.Int("SELECT count(*) FROM message WHERE key = 'LATE'") != 0 {
		t.Fatal("handled after the end")
	}
	if code, _ := b.Send(SendRequest{Recipient: "15551234567", Message: "hi"}); code != 503 {
		t.Fatalf("sent after the end: %d", code)
	}
}

// Where whatsmeow does not reconnect by itself, the connection acts as WhatsApp Desktop: logged out
// it starts again (to be linked anew), banned it waits for the ban's end, another failure is tried
// again later; replaced or too old it stays down; what it does once up is done once.
func TestTheConnectionStaysAsWhatsAppDesktopWould(t *testing.T) {
	stay := func(evts ...any) (error, int, bool) {
		b := New(t.TempDir(), Options{}, Hooks{})
		for _, e := range evts {
			b.said <- e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		ready := 0
		err := b.stay(ctx, func() { ready++ })
		return err, ready, ctx.Err() == nil // ended by itself
	}
	if err, ready, ended := stay(&events.Connected{}, &events.Disconnected{}, &events.Connected{}); err != nil || ready != 1 || ended {
		t.Fatalf("connected, again: %v %d %v", err, ready, ended)
	}
	for _, e := range []any{&events.StreamReplaced{}, &events.ClientOutdated{}, &events.Disconnected{}} {
		if err, _, ended := stay(e); err != nil || ended {
			t.Fatalf("%T: %v %v", e, err, ended)
		}
	}
	if err, _, ended := stay(&events.LoggedOut{}); err != nil || !ended {
		t.Fatalf("logged out: %v %v", err, ended)
	}
	if err, _, ended := stay(&events.ConnectFailure{Reason: events.ConnectFailureBadUserAgent}); err == nil || !ended {
		t.Fatalf("a connect failure: %v %v", err, ended)
	}
	start := time.Now()
	if err, _, ended := stay(&events.TemporaryBan{Expire: 100 * time.Millisecond}); err != nil || !ended || time.Since(start) < 100*time.Millisecond {
		t.Fatalf("banned: %v %v after %v", err, ended, time.Since(start))
	}
	if _, _, ended := stay(&events.TemporaryBan{Expire: time.Hour}); ended {
		t.Fatal("connecting again during a ban")
	}
}

// Channels and status (the owner's rule: never in the archive) are not kept, live or from history,
// nor their receipts, and their files are not downloaded.
func TestChannelsAndStatusAreNotKept(t *testing.T) {
	b, client, store, in := offline(t)
	person := types.NewJID("15551234567", types.DefaultUserServer)
	channel := types.NewJID("120363000000000000", types.NewsletterServer)
	picture := func(chat, sender types.JID, id string) *events.Message {
		e := text(chat, sender, id, "", false)
		e.Message = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{URL: proto.String("https://mmg.whatsapp.net/v/x"),
			MediaKey: []byte("k"), FileEncSHA256: []byte("e")}}
		return e
	}
	for _, e := range []*events.Message{text(types.StatusBroadcastJID, person, "S", "status", false),
		text(channel, channel, "C", "news", false), text(person, person, "P", "hi", false),
		picture(types.StatusBroadcastJID, person, "SP"), picture(channel, channel, "CP")} {
		b.handle(client, store, e)
	}
	b.handle(client, store, &events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{
		ID: proto.String(types.StatusBroadcastJID.String()), Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
			Key:              &waCommon.MessageKey{RemoteJID: proto.String(types.StatusBroadcastJID.String()), ID: proto.String("HS"), Participant: proto.String(person.String())},
			MessageTimestamp: proto.Uint64(uint64(time.Now().Unix())), Message: &waE2E.Message{Conversation: proto.String("status")}}}}}}}})
	b.handle(client, store, &events.Receipt{MessageSource: types.MessageSource{Chat: types.StatusBroadcastJID, Sender: person},
		MessageIDs: []string{"MINE"}, Timestamp: time.Now(), Type: types.ReceiptTypeRead})
	if n := in.Int("SELECT count(*) FROM message"); n != 1 {
		t.Fatalf("%d messages kept, only the person's should be", n)
	}
	if n := in.Int("SELECT count(*) FROM conversation") + in.Int("SELECT count(*) FROM receipt"); n != 1 {
		t.Fatalf("%d chats and receipts, only the person's chat should be", n)
	}
	if n := count(t, store, "SELECT count(*) FROM whatsapp_chat WHERE store = ?", store.key); n != 1 {
		t.Fatalf("%d chats kept by the bridge", n)
	}
	for _, chat := range []string{types.StatusBroadcastJID.String(), channel.String()} {
		d := &Downloads{store: store, jobs: make(chan mediaJob, 10), done: make(chan struct{})}
		if d.wanted(mediaJob{chat[:2] + "P", chat}) || count(t, store, "SELECT count(*) FROM whatsapp_media WHERE chat_jid = ?", chat) != 0 {
			t.Fatalf("a file of %s wanted", chat)
		}
	}
	d := startDownloads(client, store, nil)
	d.stop()
	if len(d.jobs) != 0 {
		t.Fatalf("%d files of channels queued at the start", len(d.jobs))
	}
}

// An edit changes a message only when its author sent it (another member cannot rewrite what
// someone said); a deletion also when a group's admin did. One person's chat may be under their
// number and their LID: an edit arriving under the other one still finds its message.
func TestOnlyTheAuthorChangesAMessage(t *testing.T) {
	b, client, store, in := offline(t)
	group := types.NewJID("120363111111111111", types.GroupServer)
	alice := types.NewJID("15550000001", types.DefaultUserServer)
	bob := types.NewJID("15550000002", types.DefaultUserServer)
	edit := func(chat, sender types.JID, target, body string, fromMe bool) *events.Message {
		e := text(chat, sender, "E"+body, "", fromMe)
		e.Message = &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key: &waCommon.MessageKey{ID: proto.String(target)}, EditedMessage: &waE2E.Message{Conversation: proto.String(body)}}}
		return e
	}
	revoke := func(chat, sender types.JID, target string, fromMe bool) *events.Message {
		e := text(chat, sender, "R"+target, "", fromMe)
		e.Message = &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(),
			Key: &waCommon.MessageKey{ID: proto.String(target)}}}
		return e
	}
	content := func(id string) (body string, deleted bool) {
		return in.Str("SELECT text FROM message WHERE key = ?", id), in.Int("SELECT deleted FROM message WHERE key = ?", id) == 1
	}
	b.handle(client, store, text(group, alice, "G", "alice's", false))
	b.handle(client, store, edit(group, bob, "G", "forged", false))
	if body, _ := content("G"); body != "alice's" {
		t.Fatalf("another member rewrote it: %q", body)
	}
	b.handle(client, store, edit(group, alice, "G", "alice's, edited", false))
	if body, _ := content("G"); body != "alice's, edited" {
		t.Fatalf("its author's edit: %q", body)
	}
	b.handle(client, store, revoke(group, bob, "G", false)) // an admin may
	if _, deleted := content("G"); !deleted {
		t.Fatal("a group's deletion by another")
	}

	// a person's chat: my message is not theirs to change
	b.handle(client, store, text(alice, alice, "MINE", "mine", true))
	b.handle(client, store, edit(alice, alice, "MINE", "theirs", false))
	b.handle(client, store, revoke(alice, alice, "MINE", false))
	if body, deleted := content("MINE"); body != "mine" || deleted {
		t.Fatalf("my message changed by the other side: %q %v", body, deleted)
	}

	// kept under the number, edited under the LID
	lid := types.NewJID("99990000001", types.HiddenUserServer)
	b.handle(client, store, text(alice, alice, "PN", "before", false))
	e := edit(lid, lid, "PN", "after", false)
	e.Info.SenderAlt = alice
	b.handle(client, store, e)
	if body, _ := content("PN"); body != "after" {
		t.Fatalf("the edit under the LID: %q", body)
	}
}

// Read receipts go to every chat of the person read: their number's and their LID's (a chat moves
// to the LID at some point, its newer messages there).
func TestReadReceiptsReachTheNumbersAndTheLIDsChat(t *testing.T) {
	b, client, store, _ := offline(t)
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("99991234567", types.HiddenUserServer)
	if err := client.Store.LIDs.PutLIDMapping(context.Background(), lid, pn); err != nil {
		t.Fatal(err)
	}
	b.handle(client, store, text(pn, pn, "OLDER", "before", false))
	b.handle(client, store, text(lid, lid, "NEWER", "after", false))
	batches, err := b.sender.toMark("+15551234567", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range batches {
		for _, id := range m.ids {
			got[m.chat.String()+"/"+id] = true
		}
	}
	if len(got) != 2 || !got[pn.String()+"/OLDER"] || !got[lid.String()+"/NEWER"] {
		t.Fatalf("to mark: %v", got)
	}
}

// A file WhatsApp no longer has is recorded and not tried again; one not had for now (not connected,
// the network) is tried again at the next start, while WhatsApp still keeps it. Nothing half
// written stays.
func TestDownloadsRecordOnlyWhatIsGone(t *testing.T) {
	_, client, store, _ := offline(t)
	if !gone(fmt.Errorf("x: %w", whatsmeow.ErrMediaDownloadFailedWith410)) || !gone(whatsmeow.ErrInvalidMediaSHA256) ||
		!gone(errIncomplete) || gone(whatsmeow.ErrNotConnected) || gone(errors.New("dial tcp: i/o timeout")) {
		t.Fatal("gone")
	}
	store.exec(`INSERT INTO whatsapp_media (store, chat_jid, id, at, media_type, filename, direct_path, media_key, file_enc_sha256,
		file_sha256, file_length) VALUES (?, '1@s.whatsapp.net', 'F', 0, 'document', 'a.pdf', '/v/t62/x', 'k', 'e', 's', 3)`, store.key)
	if err := store.download(client, "F", "1@s.whatsapp.net"); err == nil {
		t.Fatal("downloaded while never connected")
	}
	if n := count(t, store, "SELECT count(*) FROM whatsapp_media WHERE id = 'F' AND error IS NULL"); n != 1 {
		t.Fatal("a passing failure recorded as if the file were gone: never tried again")
	}
	parts, _ := filepath.Glob(filepath.Join(store.dir, "media", "*", "*.part"))
	if len(parts) != 0 {
		t.Fatalf("left behind: %v", parts)
	}
}

// The store folder (the files on their way into the archive) is the user's alone, also one made
// readable by all before.
func TestTheStoreIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	dir := filepath.Join(t.TempDir(), "store")
	os.MkdirAll(dir, 0o755)
	if _, err := OpenStore(dir, nil); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("the store folder: %v %v", st.Mode().Perm(), err)
	}
}

// The owner's own chat (their notes) takes messages, though no one else ever writes there; another
// number's, where no one wrote, still does not.
func TestSendsToTheOwnersNotes(t *testing.T) {
	b, _, _, _ := offline(t)
	if chats, err := b.sender.chatsFor("+15550009999"); err != nil || len(chats) != 1 || chats[0].String() != "15550009999@s.whatsapp.net" {
		t.Fatalf("the owner's notes: %v %v", chats, err)
	}
	if _, err := b.sender.chatsFor("+15550001111"); err == nil {
		t.Fatal("a chat where no one wrote")
	}
}
