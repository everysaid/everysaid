package whatsapp

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// A message that could not be read leaves a trace until it comes; a view-once one is said as such;
// one WhatsApp hides from linked devices is not kept.
func TestUndecryptable(t *testing.T) {
	b, client, store := offline(t)
	person := types.NewJID("15551234567", types.DefaultUserServer)
	info := types.MessageInfo{MessageSource: types.MessageSource{Chat: person, Sender: person}, ID: "U1", Timestamp: time.Now()}
	b.handle(client, store, &events.UndecryptableMessage{Info: info})
	info2 := info
	info2.ID = "V1"
	b.handle(client, store, &events.UndecryptableMessage{Info: info2, IsUnavailable: true, UnavailableType: events.UnavailableTypeViewOnce})
	info3 := info
	info3.ID = "H1"
	b.handle(client, store, &events.UndecryptableMessage{Info: info3, DecryptFailMode: events.DecryptFailHide})
	if n := count(t, store, "SELECT count(*) FROM chat_events WHERE code IN ('unreadable', 'view_once')"); n != 2 {
		t.Fatalf("%d traces", n)
	}
	b.handle(client, store, text(person, person, "U1", "here after all", false))
	if count(t, store, "SELECT count(*) FROM chat_events WHERE code = 'unreadable'") != 0 ||
		count(t, store, "SELECT count(*) FROM messages WHERE id = 'U1'") != 1 {
		t.Fatal("the message came, the trace stayed")
	}
}

// A history notification is kept until its history is stored (not lost if the download fails, or
// the bridge ends before).
func TestHistoryKept(t *testing.T) {
	b, client, store := offline(t)
	person := types.NewJID("15551234567", types.DefaultUserServer)
	n := &waE2E.HistorySyncNotification{DirectPath: proto.String("/v/t62/h1"), FileEncSHA256: []byte{1}}
	b.handle(client, store, &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{
		Chat: person, Sender: person, IsFromMe: true}, ID: "N1"},
		Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION.Enum(), HistorySyncNotification: n}}})
	var raw []byte
	if err := store.db.QueryRow("SELECT notification FROM history_pending WHERE direct_path = '/v/t62/h1'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	back := &waE2E.HistorySyncNotification{}
	if proto.Unmarshal(raw, back) != nil || back.GetDirectPath() != "/v/t62/h1" {
		t.Fatal("not kept whole")
	}
	if historyRetry(0) != time.Minute || historyRetry(30) != 24*time.Hour {
		t.Fatal("retry")
	}
}

// Kinds mautrix-whatsapp says as text are kept as text; one not kept is said as unsupported.
func TestMoreKinds(t *testing.T) {
	b, client, store := offline(t)
	person := types.NewJID("15551234567", types.DefaultUserServer)
	msg := func(id string, m *waE2E.Message) {
		b.handle(client, store, &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{
			Chat: person, Sender: person}, ID: id, Timestamp: time.Now()}, Message: m})
	}
	msg("E", &waE2E.Message{EventMessage: &waE2E.EventMessage{Name: proto.String("Dinner"), Description: proto.String("at 8")}})
	msg("G", &waE2E.Message{GroupInviteMessage: &waE2E.GroupInviteMessage{GroupName: proto.String("Club"), Caption: proto.String("join")}})
	msg("P5", &waE2E.Message{PollCreationMessageV5: &waE2E.PollCreationMessage{Name: proto.String("When?"),
		Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Mon")}}}})
	msg("S", &waE2E.Message{SpoilerMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{Conversation: proto.String("hidden")}}})
	msg("X", &waE2E.Message{PlaceholderMessage: &waE2E.PlaceholderMessage{}})
	got := map[string]string{}
	rows, _ := store.db.Query("SELECT id, kind || ':' || content FROM messages")
	for rows.Next() {
		var id, v string
		rows.Scan(&id, &v)
		got[id] = v
	}
	rows.Close()
	want := map[string]string{"E": "text:Dinner\nat 8", "G": "text:Club\njoin", "P5": "poll:When?\n• Mon", "S": "text:hidden"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if count(t, store, "SELECT count(*) FROM chat_events WHERE id = 'X' AND code = 'unsupported'") != 1 {
		t.Fatal("not said as unsupported")
	}
}

// The phone's answer that it no longer has a file is recorded as the file gone.
func TestMediaRetryNotOnPhone(t *testing.T) {
	_, _, store := offline(t)
	person := types.NewJID("15551234567", types.DefaultUserServer)
	store.db.Exec("INSERT INTO chats (jid, name) VALUES (?, 'P')", person.String())
	if _, err := store.db.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, media_key, media_error)
		VALUES ('IMG', ?, ?, '', ?, 0, 'image', x'01', 'asked again')`, person.String(), person.String(), time.Now()); err != nil {
		t.Fatal(err)
	}
	mediaRetried(store, &events.MediaRetry{MessageID: "IMG", ChatID: person, Error: &events.MediaRetryError{Code: 2}},
		func(string, string) { t.Fatal("queued") }, nil)
	var failed string
	store.db.QueryRow("SELECT media_error FROM messages WHERE id = 'IMG'").Scan(&failed)
	if failed == "" || failed == askedAgain {
		t.Fatalf("%q", failed)
	}
}

// The history's entries with no message: a missed call kept, a deletion applied; a poll's votes kept.
func TestHistoryStubsAndVotes(t *testing.T) {
	b, client, store := offline(t)
	person := types.NewJID("15551234567", types.DefaultUserServer)
	b.handle(client, store, text(person, person, "GONE", "soon deleted", false))
	now := uint64(time.Now().Unix())
	key := func(id string) *waCommon.MessageKey {
		return &waCommon.MessageKey{RemoteJID: proto.String(person.String()), ID: proto.String(id)}
	}
	msgs := []*waHistorySync.HistorySyncMsg{
		{Message: &waWeb.WebMessageInfo{Key: key("CALL"), MessageTimestamp: proto.Uint64(now),
			MessageStubType: waWeb.WebMessageInfo_CALL_MISSED_VIDEO.Enum()}},
		{Message: &waWeb.WebMessageInfo{Key: key("GONE"), MessageTimestamp: proto.Uint64(now),
			MessageStubType: waWeb.WebMessageInfo_REVOKE.Enum()}},
		{Message: &waWeb.WebMessageInfo{Key: key("POLL"), MessageTimestamp: proto.Uint64(now),
			Message: &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{Name: proto.String("When?"),
				Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Mon")}}}},
			PollUpdates: []*waWeb.PollUpdate{{PollUpdateMessageKey: key("VOTE"), SenderTimestampMS: proto.Int64(1),
				Vote: &waE2E.PollVoteMessage{SelectedOptions: [][]byte{{0xab}}}}}}},
	}
	b.handle(client, store, &events.HistorySync{Notification: &waE2E.HistorySyncNotification{},
		Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{ID: proto.String(person.String()), Messages: msgs}}}})
	if count(t, store, "SELECT count(*) FROM calls WHERE id = 'CALL' AND outcome = 'MISSED' AND video") != 1 {
		t.Fatal("missed call")
	}
	if count(t, store, "SELECT count(*) FROM messages WHERE id = 'GONE' AND deleted") != 1 {
		t.Fatal("deletion")
	}
	if count(t, store, "SELECT count(*) FROM poll_votes WHERE poll_id = 'POLL' AND options = '[\"ab\"]'") != 1 {
		t.Fatal("vote")
	}
}
