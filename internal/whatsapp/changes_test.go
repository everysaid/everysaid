package whatsapp

// Reactions, edits and deletions from the app: what goes to WhatsApp (a stand-in here: nothing is
// ever sent), and that the archive shows it at once.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/errs"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
)

// linked is the instance's bridge running as if connected, linked as the account 15550009999,
// with a stand-in for WhatsApp that keeps what it is given.
func linked(t *testing.T, f *fixture) (*Bridge, *[]*waE2E.Message) {
	c, err := OpenDevices(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
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
	f.put(t, StoredMessage{ID: "THEIRS", ChatJID: peer, Sender: peer, Content: "lunch?", Timestamp: now, Kind: "text"})
	f.put(t, StoredMessage{ID: "MINE", ChatJID: peer, Sender: "15550009999@s.whatsapp.net", Content: "at 1",
		Timestamp: now.Add(time.Second), IsFromMe: true, Kind: "text"})
	p := Plugin{}
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
	if n := count(t, f.ms, "SELECT count(*) FROM whatsapp_sent WHERE store = ?", f.ms.key); n != 5 {
		t.Fatalf("sent %d", n)
	}
}

// A reaction to someone's message in a group names who wrote it; an older one takes its author from
// the archive; what is not the account's own is neither edited nor deleted.
func TestAChangeNamesTheRightMessage(t *testing.T) {
	f := bridgeInstance(t)
	b, sent := linked(t, f)
	group, nikos := "120363000000000001@g.us", "15557654321@s.whatsapp.net"
	f.ms.storeChat(group, "Friends", time.Now())
	f.put(t, StoredMessage{ID: "G1", ChatJID: group, Sender: nikos, Content: "hi all", Timestamp: time.Now(), Kind: "text"})
	f.put(t, StoredMessage{ID: "PIC", ChatJID: group, Sender: "15550009999@s.whatsapp.net", Content: "us",
		Timestamp: time.Now(), IsFromMe: true, Kind: "image", Mentions: []string{nikos, "15550000001@s.whatsapp.net"}})

	if code, a := b.Change(ChangeRequest{Kind: "react", Recipient: group, ID: "G1", Emoji: "😂"}); code != 200 {
		t.Fatalf("%d %s", code, a.Message)
	}
	k := (*sent)[0].GetReactionMessage().GetKey()
	if k.GetParticipant() != nikos || k.GetFromMe() || k.GetRemoteJID() != group {
		t.Fatalf("key %v", k)
	}
	if g1, _, _, _ := archived(t, f, "G1"); myReaction(f, g1) != "😂" {
		t.Fatalf("in the archive %q", myReaction(f, g1))
	}
	// an older one: the author as the archive has them, or refused
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
	// a caption is edited as a caption (the kind as the archive has it); the people still named stay named
	if code, a := b.Change(ChangeRequest{Kind: "edit", Recipient: group, ID: "PIC", Text: "us and @15557654321",
		MessageKind: "image", Mentions: []string{nikos, "15550000001@s.whatsapp.net"}}); code != 200 {
		t.Fatalf("%d %s", code, a.Message)
	}
	img := (*sent)[2].GetEditedMessage().GetMessage().GetProtocolMessage().GetEditedMessage().GetImageMessage()
	if img.GetCaption() != "us and @15557654321" || strings.Join(img.GetContextInfo().GetMentionedJID(), ",") != nikos {
		t.Fatalf("caption %v", img)
	}
	if _, text, edited, _ := archived(t, f, "PIC"); text != "us and @15557654321" || !edited {
		t.Fatalf("in the archive %q %v", text, edited)
	}
}

// The plugin gives the bridge what the archive has of a message: its kind, whom it names (by the
// jid its text names them by), who wrote it.
func TestAChangeTakesTheMessageFromTheArchive(t *testing.T) {
	if got := mentionedJID("@15557654321", "+15557654321"); got != "15557654321@s.whatsapp.net" {
		t.Fatal(got)
	}
	if got := mentionedJID("@98765", "+15557654321"); got != "98765@lid" { // named by their LID, known by their number
		t.Fatal(got)
	}
	if got := mentionedJID("@98765", "98765@lid"); got != "98765@lid" {
		t.Fatal(got)
	}
	if waKind("file") != "document" || waKind("image") != "image" {
		t.Fatal("kinds")
	}
}

// The account's reaction, once it came from the phone under its LID, is the one changed too: the
// account has one reaction on a message.
func TestTheAccountsReactionIsOne(t *testing.T) {
	f := bridgeInstance(t)
	b, _ := linked(t, f)
	f.put(t, StoredMessage{ID: "M", ChatJID: peer, Sender: peer, Content: "x", Timestamp: time.Now(), Kind: "text"})
	if !f.ms.Write(&importers.WABatch{Reactions: []importers.WAReaction{{Chat: peer, Target: "M", Sender: "99990000@lid",
		FromMe: true, Emoji: "👍", At: time.Now().Add(-time.Hour).UnixMilli()}}}, waLog.Noop) {
		t.Fatal("not written")
	}
	m, _, _, _ := archived(t, f, "M")
	if myReaction(f, m) != "👍" {
		t.Fatal("the phone's reaction")
	}
	if code, a := b.Change(ChangeRequest{Kind: "react", Recipient: peer, ID: "M", Emoji: ""}); code != 200 {
		t.Fatalf("%d %s", code, a.Message)
	}
	if got := myReaction(f, m); got != "" {
		t.Fatalf("still %q", got)
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

// A deletion for the user alone goes to the account's app state, no message to anyone: the right
// index (a group's message of another names its author), the archive's copy marked deleted; one the
// archive does not have waits for it; one deleted so on the phone too.
func TestDeleteForMe(t *testing.T) {
	f := bridgeInstance(t)
	b, sent := linked(t, f)
	var patches []appstate.PatchInfo
	b.sender.patch = func(p appstate.PatchInfo) error {
		patches = append(patches, p)
		return nil
	}
	group, nikos := "120363000000000001@g.us", "15557654321@s.whatsapp.net"
	old := time.Now().Add(-72 * time.Hour) // long past deleting for everyone
	f.ms.storeChat(group, "Friends", old)
	f.put(t, StoredMessage{ID: "G1", ChatJID: group, Sender: nikos, Content: "hi all", Timestamp: old, Kind: "text"})
	f.put(t, StoredMessage{ID: "MINE", ChatJID: peer, Sender: "15550009999@s.whatsapp.net", Content: "car papers",
		Timestamp: old, IsFromMe: true, Kind: "image"})
	f.put(t, StoredMessage{ID: "P2", ChatJID: peer, Sender: peer, Content: "from the phone", Timestamp: old, Kind: "text"})

	for _, c := range []struct {
		req   ChangeRequest
		index string
	}{
		{ChangeRequest{Kind: "delete_me", Recipient: group, ID: "G1"}, "deleteMessageForMe," + group + ",G1,0," + nikos},
		{ChangeRequest{Kind: "delete_me", Recipient: peer, ID: "MINE"}, "deleteMessageForMe," + peer + ",MINE,1,0"},
		{ChangeRequest{Kind: "delete_me", Recipient: peer, ID: "OLD", Sender: "+15551234567", TS: old.UnixMilli()},
			"deleteMessageForMe," + peer + ",OLD,0,0"},
	} {
		if code, a := b.Change(c.req); code != 200 {
			t.Fatalf("%s: %d %s", c.req.ID, code, a.Message)
		}
		m := patches[len(patches)-1].Mutations[0]
		if got := strings.Join(m.Index, ","); got != c.index || m.Version != 3 ||
			m.Value.GetDeleteMessageForMeAction().GetMessageTimestamp() != old.Unix() {
			t.Fatalf("%s: index %s, version %d, %v", c.req.ID, got, m.Version, m.Value)
		}
	}
	if len(*sent) != 0 || count(t, f.ms, "SELECT count(*) FROM whatsapp_sent WHERE store = ?", f.ms.key) != 0 {
		t.Fatal("a message went")
	}
	if n := count(t, f.ms, "SELECT count(*) FROM whatsapp_pending WHERE store = ? AND target = 'OLD' AND kind = 'delete_me'", f.ms.key); n != 1 {
		t.Fatalf("waiting %d", n)
	}
	// deleted for the user on the phone
	b.handle(b.client, f.ms, &events.DeleteForMe{ChatJID: types.NewJID("15551234567", types.DefaultUserServer), MessageID: "P2", Timestamp: time.Now()})
	for _, k := range []string{"G1", "MINE", "P2"} {
		if _, text, _, deleted := archived(t, f, k); !deleted || text == "" {
			t.Fatalf("%s in the archive: %q %v", k, text, deleted)
		}
	}
}
