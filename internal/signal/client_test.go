//go:build signal

package signal

// signalmeow's store over keys.db (ncruces's SQLite, encrypted: WAL, foreign keys, $N parameters),
// the client without a device, and the transfer archive taken into the store and the archive. None
// of it touches the network.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/backuppb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/store"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// testDevice is a device of the account fakeOwn in signalmeow's tables, as a link would leave it.
func testDevice(t *testing.T, container *store.Container) *store.Device {
	t.Helper()
	ctx := context.Background()
	aciKeys, err := libsignalgo.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pniKeys, _ := libsignalgo.GenerateIdentityKeyPair()
	data := &store.DeviceData{ACIIdentityKeyPair: aciKeys, PNIIdentityKeyPair: pniKeys, ACIRegistrationID: 7, PNIRegistrationID: 8,
		ACI: uuid.MustParse(fakeOwn), PNI: uuid.MustParse("44444444-4444-4444-4444-444444444444"), DeviceID: 2,
		Number: fakePhone, Password: "secret"}
	if err := container.PutDevice(ctx, data); err != nil {
		t.Fatal(err)
	}
	dev, err := container.DeviceByACI(ctx, data.ACI)
	if err != nil || dev == nil {
		t.Fatal("the device", err)
	}
	return dev
}

func TestSignalmeowOnKeysDB(t *testing.T) {
	folders(t)
	ctx := context.Background()
	pool, container, err := openDevices(ctx, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var mode string
	pool.QueryRow("PRAGMA journal_mode").Scan(&mode)
	var fk int
	pool.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	if mode != "wal" || fk != 1 {
		t.Fatalf("journal %s, foreign keys %d", mode, fk)
	}
	dev := testDevice(t, container)
	if !dev.IsDeviceLoggedIn() || dev.Number != fakePhone || dev.DeviceID != 2 {
		t.Fatalf("%+v", dev.DeviceData)
	}
	// a recipient by ACI and PNI ($1, $2... as SQLite's ?NNN), a group's key
	anna, pni := uuid.MustParse(anna), uuid.MustParse("55555555-5555-5555-5555-555555555555")
	if _, err := dev.RecipientStore.LoadAndUpdateRecipient(ctx, anna, pni, func(r *types.Recipient) (bool, error) {
		r.E164, r.ContactName = "+306900000001", "Anna Rita"
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	list, err := dev.RecipientStore.LoadAllContacts(ctx)
	if err != nil || len(list) != 1 || list[0].ACI != anna || list[0].ContactName != "Anna Rita" {
		t.Fatalf("contacts %+v %v", list, err)
	}
	if r, err := dev.RecipientStore.LoadRecipientByE164(ctx, "+306900000001"); err != nil || r == nil || r.ACI != anna || r.PNI != pni {
		t.Fatalf("by number %+v %v", r, err)
	}
	if err := dev.GroupStore.StoreMasterKey(ctx, "Z2lk", "a2V5"); err != nil {
		t.Fatal(err)
	}
	if k, err := dev.GroupStore.MasterKeyFromGroupIdentifier(ctx, "Z2lk"); err != nil || k != "a2V5" {
		t.Fatalf("group key %q %v", k, err)
	}
	// the device removed: its rows go with it (foreign keys, cascading)
	if err := container.DeleteDevice(ctx, &dev.DeviceData); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"signalmeow_recipients", "signalmeow_groups", "signalmeow_identity_keys"} {
		if n := db.Int(pool, "SELECT count(*) FROM "+table+" WHERE account_id = ?", fakeOwn); n != 0 {
			t.Fatalf("%s: %d rows left", table, n)
		}
	}
}

// A client without a device: not linked, says so; its store and device can be forgotten.
func TestClientNotLinked(t *testing.T) {
	folders(t)
	tr, err := startClient(clientConfig{Instance: 1, Media: t.TempDir(), Handle: func(string, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	raw, err := tr.Call(context.Background(), "status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := decodeStatus(raw); st.Linked || !st.Open {
		t.Fatalf("%+v", st)
	}
	for _, cmd := range []string{"receive", "send", "sync", "discover", "mark_read", "fetch"} {
		var ce *ClientError
		if _, err := tr.Call(context.Background(), cmd, map[string]any{}); err == nil || !asClientError(err, &ce) || ce.Code != "not_linked" {
			t.Fatalf("%s: %v", cmd, err)
		}
	}
	if err := dropDevice(1); err != nil {
		t.Fatal(err)
	}
}

func asClientError(err error, ce **ClientError) bool {
	e, ok := err.(*ClientError)
	*ce = e
	return ok
}

// The history the phone transfers: from signalmeow's backup tables into the store (each event kept
// before the next), the tables cleared, then into the archive as any messages.
func TestTransferIntoTheArchive(t *testing.T) {
	dir := folders(t)
	path := newArchive(t, dir)
	ctx := context.Background()
	s, err := OpenStore("", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Account(Status{Linked: true, ACI: fakeOwn, Phone: fakePhone})
	tr, err := startClient(clientConfig{Instance: 1, Media: filepath.Join(dir, "media"), Store: s, Handle: func(name string, raw []byte) error {
		_, err := s.Apply(raw)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	c := tr.(*client)
	dev := testDevice(t, c.container)
	cli := signalmeow.NewClient(dev, zerolog.Nop(), c.handle)
	c.cli = cli
	annaID := uuid.MustParse(anna)
	b := dev.BackupStore
	for _, r := range []*backuppb.Recipient{
		{Id: 1, Destination: &backuppb.Recipient_Self{Self: &backuppb.Self{}}},
		{Id: 2, Destination: &backuppb.Recipient_Contact{Contact: &backuppb.Contact{Aci: annaID[:], E164: ptrU64(306900000001),
			SystemGivenName: "Anna", SystemFamilyName: "Rita"}}},
	} {
		if err := b.AddBackupRecipient(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.AddBackupChat(ctx, &backuppb.Chat{Id: 10, RecipientId: 2, Archived: true}); err != nil {
		t.Fatal(err)
	}
	items := []*backuppb.ChatItem{
		{ChatId: 10, AuthorId: 2, DateSent: 1000, Item: &backuppb.ChatItem_StandardMessage{StandardMessage: &backuppb.StandardMessage{
			Text: &backuppb.Text{Body: "before the link"}}},
			DirectionalDetails: &backuppb.ChatItem_Incoming{Incoming: &backuppb.ChatItem_IncomingMessageDetails{DateReceived: 1100, Read: true}}},
		{ChatId: 10, AuthorId: 1, DateSent: 2000, Item: &backuppb.ChatItem_StandardMessage{StandardMessage: &backuppb.StandardMessage{
			Text: &backuppb.Text{Body: "an answer"}}},
			DirectionalDetails: &backuppb.ChatItem_Outgoing{Outgoing: &backuppb.ChatItem_OutgoingMessageDetails{}}},
	}
	for _, it := range items {
		if err := b.AddBackupChatItem(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.RecalculateChatCounts(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := c.applyBackup(cli)
	if err != nil || n != 2 {
		t.Fatalf("%d messages, %v", n, err)
	}
	if left := db.Int(c.pool, "SELECT count(*) FROM signalmeow_backup_message") + db.Int(c.pool, "SELECT count(*) FROM signalmeow_backup_recipient"); left != 0 {
		t.Fatalf("%d rows of the archive left", left)
	}
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if got, err := Import(a, s, filepath.Join(dir, "media"), 0, nil); err != nil || got.Messages != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	texts := db.Strs(a.DB, "SELECT text FROM message ORDER BY ts")
	if strings.Join(texts, "|") != "before the link|an answer" {
		t.Fatalf("%v", texts)
	}
	// Anna by her number, with the address book's name
	if db.Int(a.DB, "SELECT count(*) FROM address WHERE value = '+306900000001'") != 1 ||
		db.Str(a.DB, "SELECT name FROM handle_name WHERE kind = 'book'") != "Anna Rita" {
		t.Fatal("the person")
	}
	var st []map[string]any
	db.Each(s.DB, "SELECT chat, field, value FROM signal_chat_state", nil, func(scan func(...any)) {
		var chat, field string
		var v int64
		scan(&chat, &field, &v)
		st = append(st, map[string]any{"chat": chat, field: v})
	})
	if b, _ := json.Marshal(st); !strings.Contains(string(b), `"archived":1`) {
		t.Fatalf("chat state %s", b)
	}
}

func ptrU64(v uint64) *uint64 { return &v }

// What Signal sends is acknowledged only once the store kept it: a handler's failure is signalmeow's
// false (the envelope comes again).
func TestKeptBeforeAcknowledged(t *testing.T) {
	folders(t)
	s, err := OpenStore("", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fail := true
	tr, err := startClient(clientConfig{Instance: 1, Media: t.TempDir(), Store: s, Handle: func(name string, raw []byte) error {
		if fail {
			return errors.New("the disk is full")
		}
		_, err := s.Apply(raw)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	c := tr.(*client)
	c.cli = signalmeow.NewClient(testDevice(t, c.container), zerolog.Nop(), c.handle)
	sender, _ := libsignalgo.ServiceIDFromString(anna)
	own, _ := libsignalgo.ServiceIDFromString(fakeOwn)
	ts := uint64(time.Now().UnixMilli())
	evt := &events.Content{Envelope: &signalpb.Envelope{ClientTimestamp: &ts, ServerTimestamp: &ts},
		Content: data(&signalpb.DataMessage{Body: proto.String("hello"), Timestamp: &ts}), Sender: sender, SenderDevice: 1, Destination: own}
	if c.handle(evt) {
		t.Fatal("acknowledged though not kept")
	}
	fail = false
	if !c.handle(evt) {
		t.Fatal("kept, not acknowledged")
	}
	if e, ok := s.Message(anna, int64(ts)); !ok || deref(e.Text) != "hello" {
		t.Fatal("not in the store")
	}
}
