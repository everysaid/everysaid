//go:build signal

package signal

// The conversion of what Signal sends into the client's events, on contents made by hand (as the
// earlier helper's convert.rs tests did); it needs libsignal (a group's id), so the tag `signal`.

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/backuppb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
)

const me = fakeOwn

func testConv() conv { return conv{own: me, groupID: groupIDOf} }

func metaFrom(sender string) meta {
	return meta{sender: sender, destination: me, device: 1, ts: 1000, serverTS: 1001}
}

// js is an event as the store reads it.
func js(t *testing.T, x converted) map[string]any {
	t.Helper()
	b, err := json.Marshal(x.ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	m["event"] = x.name
	return m
}

func one(t *testing.T, m meta, content *signalpb.Content) map[string]any {
	t.Helper()
	out := testConv().convert(m, content)
	if len(out) != 1 {
		t.Fatalf("%d events, not one: %+v", len(out), out)
	}
	return js(t, out[0])
}

func data(dm *signalpb.DataMessage) *signalpb.Content {
	return &signalpb.Content{Content: &signalpb.Content_DataMessage{DataMessage: dm}}
}

func syncOf(sm *signalpb.SyncMessage) *signalpb.Content {
	return &signalpb.Content{Content: &signalpb.Content_SyncMessage{SyncMessage: sm}}
}

func TestTextFromAPerson(t *testing.T) {
	e := one(t, metaFrom(anna), data(&signalpb.DataMessage{Body: proto.String("hello"), Timestamp: proto.Uint64(1000)}))
	if e["event"] != "message" || e["text"] != "hello" || e["sender"] != anna || e["outgoing"] != false ||
		e["ts"].(float64) != 1000 || e["server_ts"].(float64) != 1001 {
		t.Fatalf("%v", e)
	}
	if c := e["chat"].(map[string]any); c["kind"] != "contact" || c["id"] != anna {
		t.Fatalf("chat %v", c)
	}
}

func TestGroupMessageWithMentionQuoteAndAttachment(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 7
	}
	dm := &signalpb.DataMessage{
		Body: proto.String("hi ￼"), Timestamp: proto.Uint64(2000),
		GroupV2:    &signalpb.GroupContextV2{MasterKey: key, Revision: proto.Uint32(3)},
		BodyRanges: []*signalpb.BodyRange{{Start: proto.Uint32(3), Length: proto.Uint32(1), AssociatedValue: &signalpb.BodyRange_MentionAci{MentionAci: bob}}},
		Quote:      &signalpb.DataMessage_Quote{Id: proto.Uint64(1500), AuthorAci: proto.String(me), Text: proto.String("q")},
		Attachments: []*signalpb.AttachmentPointer{{ContentType: proto.String("image/jpeg"), Size: proto.Uint32(10), Flags: proto.Uint32(1),
			AttachmentIdentifier: &signalpb.AttachmentPointer_CdnKey{CdnKey: "abc"}, Key: []byte{1, 2}}},
	}
	m := metaFrom(anna)
	m.ts = 2000
	out := testConv().convert(m, data(dm))
	if len(out) != 1 || len(out[0].atts) != 1 || string(out[0].group) != string(key) {
		t.Fatalf("%+v", out)
	}
	e := js(t, out[0])
	chat := e["chat"].(map[string]any)
	if chat["kind"] != "group" || chat["id"] != groupIDOf(key) || chat["id"] == base64Std(key) {
		t.Fatalf("the group's id, not its master key: %v", chat)
	}
	ms := e["mentions"].([]any)[0].(map[string]any)
	if ms["start"].(float64) != 3 || ms["aci"] != bob {
		t.Fatalf("mention %v", ms)
	}
	if q := e["quote"].(map[string]any); q["ts"].(float64) != 1500 || q["author"] != me || q["text"] != "q" {
		t.Fatalf("quote %v", q)
	}
	a := e["attachments"].([]any)[0].(map[string]any)
	if a["voice"] != true || a["file"] != nil || e["group_revision"].(float64) != 3 {
		t.Fatalf("attachment %v", e)
	}
	// the file's pointer, kept to fetch it again
	if p := pointerOf(a["pointer"].(string)); p == nil || p.GetCdnKey() != "abc" || string(p.GetKey()) != "\x01\x02" {
		t.Fatalf("pointer %v", a["pointer"])
	}
	// an envelope of another time than the message says is not taken
	if out := testConv().convert(metaFrom(anna), data(dm)); len(out) != 0 {
		t.Fatal("a timestamp mismatch taken")
	}
}

func TestReactionDeleteAndEdit(t *testing.T) {
	e := one(t, metaFrom(anna), data(&signalpb.DataMessage{Reaction: &signalpb.DataMessage_Reaction{Emoji: proto.String("👍"),
		Remove: proto.Bool(false), TargetAuthorAci: proto.String(me), TargetSentTimestamp: proto.Uint64(900)}}))
	if e["event"] != "reaction" || e["emoji"] != "👍" || e["target_ts"].(float64) != 900 || e["target_author"] != me {
		t.Fatalf("%v", e)
	}
	e = one(t, metaFrom(anna), data(&signalpb.DataMessage{Delete: &signalpb.DataMessage_Delete{TargetSentTimestamp: proto.Uint64(800)}}))
	if e["event"] != "delete" || e["target_ts"].(float64) != 800 || e["target_author"] != anna {
		t.Fatalf("%v", e)
	}
	edit := &signalpb.Content{Content: &signalpb.Content_EditMessage{EditMessage: &signalpb.EditMessage{TargetSentTimestamp: proto.Uint64(700),
		DataMessage: &signalpb.DataMessage{Body: proto.String("fixed"), Timestamp: proto.Uint64(1100)}}}}
	m := metaFrom(anna)
	m.ts = 1100
	e = one(t, m, edit)
	if e["event"] != "edit" || e["target_ts"].(float64) != 700 || e["text"] != "fixed" || e["ts"].(float64) != 1100 {
		t.Fatalf("%v", e)
	}
	if out := testConv().convert(metaFrom(anna), edit); len(out) != 0 {
		t.Fatal("an edit of another time taken")
	}
}

func TestSentFromThePhone(t *testing.T) {
	sent := &signalpb.SyncMessage_Sent{DestinationServiceId: proto.String(anna), Timestamp: proto.Uint64(3000),
		Message: &signalpb.DataMessage{Body: proto.String("from my phone"), Timestamp: proto.Uint64(3000)}}
	e := one(t, metaFrom(me), syncOf(&signalpb.SyncMessage{Content: &signalpb.SyncMessage_Sent_{Sent: sent}}))
	if e["outgoing"] != true || e["sender"] != me || e["chat"].(map[string]any)["id"] != anna || e["ts"].(float64) != 3000 {
		t.Fatalf("%v", e)
	}
	// a sync message from someone else is a forgery
	if out := testConv().convert(metaFrom(bob), syncOf(&signalpb.SyncMessage{Content: &signalpb.SyncMessage_Sent_{Sent: sent}})); len(out) != 0 {
		t.Fatal("a forged sync message taken")
	}
}

func TestSentFromThePhoneToANumber(t *testing.T) {
	// someone the phone knows only by number: the destination is their PNI, the number beside it
	const pni = "PNI:44444444-4444-4444-4444-444444444444"
	sid, _ := libsignalgo.ServiceIDFromString(pni)
	sent := &signalpb.SyncMessage_Sent{DestinationServiceIdBinary: sid.Bytes(), DestinationE164: proto.String("+306900000002"),
		Timestamp: proto.Uint64(3000), Message: &signalpb.DataMessage{Body: proto.String("to a number"), Timestamp: proto.Uint64(3000)}}
	out := testConv().convert(metaFrom(me), syncOf(&signalpb.SyncMessage{Content: &signalpb.SyncMessage_Sent_{Sent: sent}}))
	if len(out) != 2 {
		t.Fatalf("%+v", out)
	}
	if e := js(t, out[0]); e["event"] != "ids" || e["aci"] != nil || e["pni"] != pni || e["phone"] != "+306900000002" {
		t.Fatalf("ids %v", e)
	}
	if e := js(t, out[1]); e["text"] != "to a number" || e["chat"].(map[string]any)["id"] != pni {
		t.Fatalf("message %v", e)
	}
	// to an ACI, its number too
	sent = &signalpb.SyncMessage_Sent{DestinationServiceId: proto.String(anna), DestinationE164: proto.String("+306900000001"),
		Message: &signalpb.DataMessage{Body: proto.String("hi"), Timestamp: proto.Uint64(3100)}}
	out = testConv().convert(metaFrom(me), syncOf(&signalpb.SyncMessage{Content: &signalpb.SyncMessage_Sent_{Sent: sent}}))
	if e := js(t, out[0]); e["event"] != "ids" || e["aci"] != anna || e["phone"] != "+306900000001" {
		t.Fatalf("ids %v", e)
	}
}

func TestReadOnThePhoneAndReceipts(t *testing.T) {
	sm := &signalpb.SyncMessage{Read: []*signalpb.SyncMessage_Read{{SenderAci: proto.String(anna), Timestamp: proto.Uint64(500)}}}
	e := one(t, metaFrom(me), syncOf(sm))
	if e["event"] != "read" || e["messages"].([]any)[0].(map[string]any)["ts"].(float64) != 500 {
		t.Fatalf("%v", e)
	}
	rm := &signalpb.Content{Content: &signalpb.Content_ReceiptMessage{ReceiptMessage: &signalpb.ReceiptMessage{
		Type: signalpb.ReceiptMessage_READ.Enum(), Timestamp: []uint64{1, 2}}}}
	e = one(t, metaFrom(anna), rm)
	if e["event"] != "receipt" || e["kind"] != "read" || len(e["timestamps"].([]any)) != 2 {
		t.Fatalf("%v", e)
	}
}

func TestCalls(t *testing.T) {
	call := func(cm *signalpb.CallMessage) *signalpb.Content {
		return &signalpb.Content{Content: &signalpb.Content_CallMessage{CallMessage: cm}}
	}
	e := one(t, metaFrom(anna), call(&signalpb.CallMessage{Offer: &signalpb.CallMessage_Offer{Id: proto.Uint64(42),
		Type: signalpb.CallMessage_Offer_OFFER_VIDEO_CALL.Enum()}}))
	if e["action"] != "offer" || e["id"] != "42" || e["video"] != true {
		t.Fatalf("%v", e)
	}
	for cm, want := range map[*signalpb.CallMessage]string{
		{Answer: &signalpb.CallMessage_Answer{Id: proto.Uint64(42)}}: "answer",
		{Busy: &signalpb.CallMessage_Busy{Id: proto.Uint64(42)}}:     "busy",
	} {
		if e := one(t, metaFrom(anna), call(cm)); e["action"] != want || e["id"] != "42" {
			t.Fatalf("%s: %v", want, e)
		}
	}
	e = one(t, metaFrom(anna), call(&signalpb.CallMessage{Hangup: &signalpb.CallMessage_Hangup{Id: proto.Uint64(42),
		Type: signalpb.CallMessage_Hangup_HANGUP_DECLINED.Enum()}}))
	if e["action"] != "hangup" || e["hangup"] != "declined" {
		t.Fatalf("%v", e)
	}
	// the phone's call log
	u := uuid.MustParse(anna)
	ev := &signalpb.SyncMessage_CallEvent{ConversationId: u[:], CallId: proto.Uint64(42), Timestamp: proto.Uint64(1234),
		Type: signalpb.SyncMessage_CallEvent_AUDIO_CALL.Enum(), Direction: signalpb.SyncMessage_CallEvent_INCOMING.Enum(),
		Event: signalpb.SyncMessage_CallEvent_ACCEPTED.Enum()}
	e = one(t, metaFrom(me), syncOf(&signalpb.SyncMessage{Content: &signalpb.SyncMessage_CallEvent_{CallEvent: ev}}))
	if e["source"] != "sync" || e["result"] != "accepted" || e["direction"] != "incoming" || e["ts"].(float64) != 1234 ||
		e["chat"].(map[string]any)["id"] != anna {
		t.Fatalf("%v", e)
	}
}

func TestNothingToKeep(t *testing.T) {
	pk := data(&signalpb.DataMessage{ProfileKey: make([]byte, 32), Flags: proto.Uint32(4)})
	typing := &signalpb.Content{Content: &signalpb.Content_TypingMessage{TypingMessage: &signalpb.TypingMessage{}}}
	for _, c := range []*signalpb.Content{pk, typing} {
		if out := testConv().convert(metaFrom(anna), c); len(out) != 0 {
			t.Fatalf("%+v", out)
		}
	}
}

func TestSentHereReactionEditAndDelete(t *testing.T) {
	c := testConv()
	r := data(&signalpb.DataMessage{Timestamp: proto.Uint64(5000), Reaction: &signalpb.DataMessage_Reaction{Emoji: proto.String("❤️"),
		Remove: proto.Bool(false), TargetAuthorAci: proto.String(anna), TargetSentTimestamp: proto.Uint64(900)}})
	e := js(t, c.sentEvents(2, anna, r, 5000)[0])
	if e["event"] != "reaction" || e["chat"].(map[string]any)["id"] != anna || e["sender"] != me || e["outgoing"] != true {
		t.Fatalf("%v", e)
	}
	photo := &signalpb.AttachmentPointer{ContentType: proto.String("image/jpeg"), AttachmentIdentifier: &signalpb.AttachmentPointer_CdnKey{CdnKey: "k"}}
	stored := event{Attachments: []attachmentEv{{ContentType: proto.String("image/jpeg"), Pointer: pointerText(photo)}},
		Quote: &quoteEv{TS: 10, Author: proto.String(bob)}}
	dm := edited(storedMessage(stored), "new", 5200)
	if dm.GetBody() != "new" || dm.GetTimestamp() != 5200 || len(dm.Attachments) != 1 || dm.GetQuote().GetId() != 10 {
		t.Fatalf("%v", dm)
	}
	content := &signalpb.Content{Content: &signalpb.Content_EditMessage{EditMessage: &signalpb.EditMessage{
		TargetSentTimestamp: proto.Uint64(4000), DataMessage: dm}}}
	out := c.sentEvents(2, anna, content, 5200)
	if e := js(t, out[0]); e["event"] != "edit" || e["target_ts"].(float64) != 4000 || e["text"] != "new" || len(out[0].atts) != 1 {
		t.Fatalf("%v", e)
	}
}

func TestAdminDeletes(t *testing.T) {
	admins := []string{anna}
	if !adminDeleteAllowed(anna, bob, admins, true) || adminDeleteAllowed(bob, anna, admins, true) ||
		adminDeleteAllowed(bob, anna, nil, false) || !adminDeleteAllowed(bob, bob, nil, false) {
		t.Fatal("admin deletes")
	}
	u := uuid.MustParse(bob)
	e := one(t, metaFrom(anna), data(&signalpb.DataMessage{AdminDelete: &signalpb.DataMessage_AdminDelete{
		TargetAuthorAciBinary: u[:], TargetSentTimestamp: proto.Uint64(800)}}))
	if e["event"] != "delete" || e["target_author"] != bob || e["admin"] != true {
		t.Fatalf("%v", e)
	}
}

func TestNoticesAndVotes(t *testing.T) {
	e := one(t, metaFrom(anna), data(&signalpb.DataMessage{Flags: proto.Uint32(uint32(signalpb.DataMessage_EXPIRATION_TIMER_UPDATE)),
		Timestamp: proto.Uint64(1000)}))
	if e["expire_timer_update"] != true || e["expire_timer"].(float64) != 0 {
		t.Fatalf("a timer turned off: %v", e)
	}
	a := uuid.MustParse(anna)
	e = one(t, metaFrom(anna), data(&signalpb.DataMessage{Timestamp: proto.Uint64(1000), PinMessage: &signalpb.DataMessage_PinMessage{
		TargetAuthorAciBinary: a[:], TargetSentTimestamp: proto.Uint64(500),
		PinDuration: &signalpb.DataMessage_PinMessage_PinDurationSeconds{PinDurationSeconds: 86400}}}))
	if e["pin"].(map[string]any)["seconds"].(float64) != 86400 {
		t.Fatalf("a pin for a day: %v", e)
	}
	b := uuid.MustParse(bob)
	e = one(t, metaFrom(anna), data(&signalpb.DataMessage{Timestamp: proto.Uint64(1000), PollVote: &signalpb.DataMessage_PollVote{
		TargetAuthorAciBinary: b[:], TargetSentTimestamp: proto.Uint64(700), OptionIndexes: []uint32{1}, VoteCount: proto.Uint32(2)}}))
	if e["event"] != "poll_vote" || e["target_author"] != bob || e["vote_count"].(float64) != 2 {
		t.Fatalf("a vote: %v", e)
	}
	e = one(t, metaFrom(anna), data(&signalpb.DataMessage{Timestamp: proto.Uint64(1000), StoryContext: &signalpb.DataMessage_StoryContext{},
		Reaction: &signalpb.DataMessage_Reaction{Emoji: proto.String("🔥")}}))
	if e["event"] != "message" || e["story_reaction"] != "🔥" {
		t.Fatalf("a reaction to a story: %v", e)
	}
	e = one(t, metaFrom(anna), data(&signalpb.DataMessage{Timestamp: proto.Uint64(1000), RequiredProtocolVersion: proto.Uint32(99)}))
	if e["unsupported"] != true {
		t.Fatalf("made by a newer Signal: %v", e)
	}
}

func TestAttachmentNames(t *testing.T) {
	for _, c := range []struct {
		ct, name, want string
		i              int
	}{{"image/jpeg", "", "5-22222222-0.jpg", 0}, {"x/y", "Report.PDF", "5-22222222-1.pdf", 1}, {"", "../../etc", "5-22222222-2", 2}} {
		if got := attachmentName(5, anna, c.i, c.ct, c.name); got != c.want {
			t.Fatalf("%s, not %s", got, c.want)
		}
	}
	if got := attachmentName(5, "PNI:abc", 0, "", ""); got != "5-PNIabc-0" {
		t.Fatal(got)
	}
}

// The transfer archive's items: a message with its edit, reactions, receipt and the owner's reading;
// a call of the phone's call log; a deletion.
func TestTransferItems(t *testing.T) {
	u := uuid.MustParse(anna)
	a := &transferArchive{own: me, recipients: map[uint64]*backuppb.Recipient{
		1: {Id: 1, Destination: &backuppb.Recipient_Self{Self: &backuppb.Self{}}},
		2: {Id: 2, Destination: &backuppb.Recipient_Contact{Contact: &backuppb.Contact{Aci: u[:]}}},
	}}
	chat := contactChat(anna)
	text := func(s string) *backuppb.ChatItem_StandardMessage {
		return &backuppb.ChatItem_StandardMessage{StandardMessage: &backuppb.StandardMessage{Text: &backuppb.Text{Body: s},
			Reactions: []*backuppb.Reaction{{Emoji: "👍", AuthorId: 1, SentTimestamp: 1300}}}}
	}
	it := &backuppb.ChatItem{AuthorId: 2, DateSent: 1200, Item: text("hello!"),
		DirectionalDetails: &backuppb.ChatItem_Incoming{Incoming: &backuppb.ChatItem_IncomingMessageDetails{DateReceived: 1250, Read: true}},
		Revisions:          []*backuppb.ChatItem{{AuthorId: 2, DateSent: 1000, Item: text("helo")}}}
	var names []string
	evs := map[string]map[string]any{}
	for _, x := range a.item(chat, it) {
		names = append(names, x.name)
		evs[x.name] = js(t, x)
	}
	if len(names) != 4 || evs["message"]["text"] != "helo" || evs["message"]["ts"].(float64) != 1000 ||
		evs["edit"]["text"] != "hello!" || evs["edit"]["target_ts"].(float64) != 1000 || evs["edit"]["ts"].(float64) != 1200 ||
		evs["reaction"]["sender"] != me || evs["reaction"]["target_ts"].(float64) != 1000 || evs["read"] == nil {
		t.Fatalf("%v %v", names, evs)
	}
	callID := uint64(77)
	callItem := &backuppb.ChatItem{AuthorId: 2, DateSent: 2000, Item: &backuppb.ChatItem_UpdateMessage{UpdateMessage: &backuppb.ChatUpdateMessage{
		Update: &backuppb.ChatUpdateMessage_IndividualCall{IndividualCall: &backuppb.IndividualCall{CallId: &callID,
			Type: backuppb.IndividualCall_VIDEO_CALL, Direction: backuppb.IndividualCall_INCOMING, State: backuppb.IndividualCall_MISSED,
			StartedCallTimestamp: 2000}}}}}
	out := a.item(chat, callItem)
	if e := js(t, out[0]); len(out) != 1 || e["event"] != "call" || e["id"] != "77" || e["result"] != "not_accepted" || e["type"] != "video" {
		t.Fatalf("%v", out)
	}
	del := &backuppb.ChatItem{AuthorId: 1, DateSent: 3000, Item: &backuppb.ChatItem_RemoteDeletedMessage{RemoteDeletedMessage: &backuppb.RemoteDeletedMessage{}},
		DirectionalDetails: &backuppb.ChatItem_Outgoing{Outgoing: &backuppb.ChatItem_OutgoingMessageDetails{SendStatus: []*backuppb.SendStatus{
			{RecipientId: 2, Timestamp: 3100, DeliveryStatus: &backuppb.SendStatus_Read_{Read: &backuppb.SendStatus_Read{}}}}}}}
	names = nil
	for _, x := range a.item(chat, del) {
		names = append(names, x.name)
	}
	if len(names) != 3 || names[0] != "message" || names[1] != "delete" || names[2] != "receipt" {
		t.Fatalf("%v", names)
	}
}
