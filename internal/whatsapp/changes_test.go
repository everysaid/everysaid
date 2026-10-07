package whatsapp

// Reactions, edits and deletions from the app: what goes to WhatsApp (a stand-in here: nothing is
// ever sent), what the bridge stores of it, and that the import then shows it in the archive.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

// linked is the instance's bridge running as if connected, linked as the account 15550009999,
// with a stand-in for WhatsApp that keeps what it is given.
func linked(t *testing.T, f *fixture) (*Bridge, *[]*waE2E.Message) {
	c, err := OpenDevices(context.Background(), f.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	device := c.NewDevice()
	device.ID = &types.JID{User: "15550009999", Device: 7, Server: types.DefaultUserServer}
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64),
		AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := c.PutDevice(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	client := whatsmeow.NewClient(device, nil)
	b := fakeRunning(t, f, true)
	var sent []*waE2E.Message
	b.client = client
	b.sender.client, b.sender.logger = client, b.log
	b.sender.transmit = func(chat types.JID, msg *waE2E.Message) (whatsmeow.SendResponse, error) {
		sent = append(sent, msg)
		return whatsmeow.SendResponse{ID: fmt.Sprintf("OUT%d", len(sent)), Timestamp: time.Now()}, nil
	}
	return b, &sent
}

// archived is a message of the archive by its key: its id, text, edited and deleted.
func archived(t *testing.T, f *fixture, key string) (id int64, text string, edited, deleted bool) {
	t.Helper()
	if err := f.h.store.Read().QueryRow("SELECT id, coalesce(text, ''), coalesce(edited, 0), coalesce(deleted, 0) FROM message WHERE key = ?", key).
		Scan(&id, &text, &edited, &deleted); err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return
}

// myReaction is the user's reaction on a message in the archive ("" for none).
func myReaction(f *fixture, id int64) string {
	var emoji string
	f.h.store.Read().QueryRow("SELECT emoji FROM reaction WHERE message_id = ? AND outgoing = 1", id).Scan(&emoji)
	return emoji
}

func TestReactEditAndDeleteReachTheArchive(t *testing.T) {
	f := bridgeInstance(t)
	_, sent := linked(t, f)
	now := time.Now().Add(-time.Minute)
	f.ms.storeMessage(StoredMessage{ID: "THEIRS", ChatJID: peer, Sender: peer, Content: "lunch?", Timestamp: now, Kind: "text"})
	f.ms.storeMessage(StoredMessage{ID: "MINE", ChatJID: peer, Sender: "15550009999@s.whatsapp.net", Content: "at 1",
		Timestamp: now.Add(time.Second), IsFromMe: true, Kind: "text"})
	p := Plugin{}
	if err := p.RunImport(f.ctx()); err != nil {
		t.Fatal(err)
	}
	theirs, _, _, _ := archived(t, f, "THEIRS")
	mine, _, _, _ := archived(t, f, "MINE")
	var convID int64
	var convKey string
	f.h.store.Read().QueryRow("SELECT c.id, c.key FROM conversation c JOIN message m ON m.conversation_id = c.id WHERE m.id = ?", theirs).
		Scan(&convID, &convKey)
	conv := plugins.Conversation{ID: convID, Key: convKey, Service: "whatsapp"}
	ctx := context.Background()

	for _, emoji := range []string{"👍", "❤️", ""} { // put, changed, taken back
		if err := p.React(ctx, f.ctx(), conv, plugins.Ref{ID: theirs, Key: "THEIRS"}, emoji); err != nil {
			t.Fatal(err)
		}
		if got := myReaction(f, theirs); got != emoji {
			t.Fatalf("reaction %q in the archive, want %q", got, emoji)
		}
	}
	r := (*sent)[0].GetReactionMessage()
	if r.GetText() != "👍" || r.GetKey().GetID() != "THEIRS" || r.GetKey().GetFromMe() || r.GetKey().GetRemoteJID() != peer {
		t.Fatalf("reaction sent %v", r)
	}

	if err := p.Edit(ctx, f.ctx(), conv, plugins.Ref{ID: mine, Key: "MINE", Outgoing: true}, " at 2 "); err != nil {
		t.Fatal(err)
	}
	if _, text, edited, _ := archived(t, f, "MINE"); text != "at 2" || !edited {
		t.Fatalf("edit in the archive: %q %v", text, edited)
	}
	e := (*sent)[3].GetEditedMessage().GetMessage().GetProtocolMessage()
	if e.GetType() != waE2E.ProtocolMessage_MESSAGE_EDIT || e.GetKey().GetID() != "MINE" || e.GetEditedMessage().GetConversation() != "at 2" {
		t.Fatalf("edit sent %v", e)
	}

	if err := p.Delete(ctx, f.ctx(), conv, plugins.Ref{ID: mine, Key: "MINE", Outgoing: true}); err != nil {
		t.Fatal(err)
	}
	if _, text, _, deleted := archived(t, f, "MINE"); !deleted || text != "at 2" {
		t.Fatalf("deletion in the archive: %q %v", text, deleted)
	}
	d := (*sent)[4].GetProtocolMessage()
	if d.GetType() != waE2E.ProtocolMessage_REVOKE || d.GetKey().GetID() != "MINE" || !d.GetKey().GetFromMe() {
		t.Fatalf("deletion sent %v", d)
	}
	// each went to WhatsApp as a message, and counts for the pace
	if n := count(t, f.ms, "SELECT count(*) FROM sent"); n != 5 {
		t.Fatalf("sent %d", n)
	}
}

// A reaction to someone's message in a group names who wrote it; one the bridge does not have takes
// its author from the archive; what is not the account's own is neither edited nor deleted.
func TestAChangeNamesTheRightMessage(t *testing.T) {
	f := bridgeInstance(t)
	b, sent := linked(t, f)
	group, nikos := "120363000000000001@g.us", "15557654321@s.whatsapp.net"
	f.ms.storeChatTime(group, "Friends", time.Now())
	f.ms.storeMessage(StoredMessage{ID: "G1", ChatJID: group, Sender: nikos, Content: "hi all", Timestamp: time.Now(), Kind: "text"})
	f.ms.storeMessage(StoredMessage{ID: "PIC", ChatJID: group, Sender: "15550009999@s.whatsapp.net", Content: "us",
		Timestamp: time.Now(), IsFromMe: true, Kind: "image", Mentions: []string{nikos, "15550000001@s.whatsapp.net"}})

	if code, a := b.Change(ChangeRequest{Kind: "react", Recipient: group, ID: "G1", Emoji: "😂"}); code != 200 {
		t.Fatalf("%d %s", code, a.Message)
	}
	k := (*sent)[0].GetReactionMessage().GetKey()
	if k.GetParticipant() != nikos || k.GetFromMe() || k.GetRemoteJID() != group {
		t.Fatalf("key %v", k)
	}
	if n := count(t, f.ms, "SELECT count(*) FROM reactions WHERE message_id = 'G1' AND is_from_me AND emoji = '😂'"); n != 1 {
		t.Fatalf("stored %d", n)
	}
	// from before the bridge was linked: the author as the archive has them, or refused
	if code, a := b.Change(ChangeRequest{Kind: "react", Recipient: group, ID: "OLD", Sender: "+15557654321", Emoji: "👍"}); code != 200 {
		t.Fatalf("%d %s", code, a.Message)
	}
	if p := (*sent)[1].GetReactionMessage().GetKey().GetParticipant(); p != nikos {
		t.Fatalf("participant %q", p)
	}
	if code, _ := b.Change(ChangeRequest{Kind: "react", Recipient: group, ID: "OLD", Emoji: "👍"}); code != 400 {
		t.Fatalf("no author: %d", code)
	}
	if code, _ := b.Change(ChangeRequest{Kind: "edit", Recipient: group, ID: "G1", Text: "x"}); code != 403 {
		t.Fatalf("someone else's edited: %d", code)
	}
	if code, _ := b.Change(ChangeRequest{Kind: "delete", Recipient: group, ID: "OLD", FromMe: true}); code != 404 {
		t.Fatalf("an unknown one deleted: %d", code)
	}
	// a caption is edited as a caption; the people still named stay named
	if code, a := b.Change(ChangeRequest{Kind: "edit", Recipient: group, ID: "PIC", Text: "us and @15557654321"}); code != 200 {
		t.Fatalf("%d %s", code, a.Message)
	}
	img := (*sent)[2].GetEditedMessage().GetMessage().GetProtocolMessage().GetEditedMessage().GetImageMessage()
	if img.GetCaption() != "us and @15557654321" || strings.Join(img.GetContextInfo().GetMentionedJID(), ",") != nikos {
		t.Fatalf("caption %v", img)
	}
	var content, mentions string
	f.ms.db.QueryRow("SELECT content, mentions FROM messages WHERE id = 'PIC'").Scan(&content, &mentions)
	if content != "us and @15557654321" || mentions != nikos {
		t.Fatalf("stored %q %q", content, mentions)
	}
}

// The account's reaction, once stored from the phone under its LID, is the one changed too: the
// import would otherwise read two of them.
func TestTheAccountsReactionIsOne(t *testing.T) {
	f := bridgeInstance(t)
	b, _ := linked(t, f)
	f.ms.storeMessage(StoredMessage{ID: "M", ChatJID: peer, Sender: peer, Content: "x", Timestamp: time.Now(), Kind: "text"})
	f.ms.storeReaction(peer, "M", "99990000@lid", true, "👍", time.Now().Add(-time.Hour))
	if code, a := b.Change(ChangeRequest{Kind: "react", Recipient: peer, ID: "M", Emoji: ""}); code != 200 {
		t.Fatalf("%d %s", code, a.Message)
	}
	if n := count(t, f.ms, "SELECT count(*) FROM reactions WHERE message_id = 'M' AND is_from_me AND emoji != ''"); n != 0 {
		t.Fatalf("still %d", n)
	}
}

func TestChangesKeepTheGatesOfSending(t *testing.T) {
	f := bridgeInstance(t)
	p := Plugin{}
	conv := plugins.Conversation{ID: 1, Key: "+15551234567", Service: "whatsapp"}
	ref := plugins.Ref{ID: message(t, f, "THEIRS", "hi", false), Key: "THEIRS"}
	var ue *errs.UserError
	if err := p.React(context.Background(), f.ctx(), conv, ref, "👍"); !errors.As(err, &ue) || ue.Text != "not connected to WhatsApp" || ue.Status != 503 {
		t.Fatalf("no bridge running: %v", err)
	}
	b := fakeRunning(t, f, true) // running, never connected
	if err := p.React(context.Background(), f.ctx(), conv, ref, "👍"); !errors.As(err, &ue) || ue.Text != "not connected to WhatsApp" {
		t.Fatalf("not connected: %v", err)
	}
	b.sender.enabled = false
	if code, _ := b.Change(ChangeRequest{Kind: "react", Recipient: "15551234567", ID: "THEIRS"}); code != 403 {
		t.Fatalf("off in config: %d", code)
	}
	b.sender.enabled = true
	f.ms.block("temporary ban: x")
	if err := p.Delete(context.Background(), f.ctx(), conv, plugins.Ref{ID: ref.ID, Key: "THEIRS", Outgoing: true}); !errors.As(err, &ue) ||
		!strings.HasPrefix(ue.Text, "sending is blocked (temporary ban: x)") {
		t.Fatalf("blocked: %v", err)
	}
	if err := p.Edit(context.Background(), f.ctx(), conv, ref, "x"); !errors.As(err, &ue) || ue.Text != "Sending is off in this source's settings" {
		t.Fatalf("off: %v", err)
	}
	m := plugins.Manifest(p, "en")
	if m["can_react"] != true || m["free_reactions"] != true || m["can_edit"] != true || m["can_delete"] != true ||
		strings.Join(m["reactions"].([]string), "") != "👍❤️😂😮😢🙏" {
		t.Fatalf("%v", m)
	}
	if i := p.Info(); i.EditWindow != 15*time.Minute || i.DeleteWindow != 48*time.Hour {
		t.Fatalf("windows %v %v", i.EditWindow, i.DeleteWindow)
	}
}
