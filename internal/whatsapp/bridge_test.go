package whatsapp

// Ports bridges/whatsapp/bridge_test.go. Tests run on an in-memory database or in temporary
// folders: never on a real store.

import (
	"bytes"
	"database/sql"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// oldStore is a store as the bridge made it before this version.
func oldStore(t *testing.T) *MessageStore {
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
		CREATE TABLE messages (id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP,
			is_from_me BOOLEAN, media_type TEXT, filename TEXT, url TEXT, media_key BLOB, file_sha256 BLOB,
			file_enc_sha256 BLOB, file_length INTEGER, PRIMARY KEY (id, chat_jid), FOREIGN KEY (chat_jid) REFERENCES chats(jid));
		INSERT INTO chats VALUES ('1@s.whatsapp.net', 'A', '2026-10-01 10:00:00+03:00');
		INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename)
			VALUES ('OLD', '1@s.whatsapp.net', '1', 'hello', '2026-10-01 10:00:00+03:00', 0, 'image', 'image_1.jpg');
	`)
	if err != nil {
		t.Fatal(err)
	}
	s := &MessageStore{db: db, dir: t.TempDir()}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateStatus(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateGroups(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateReceipts(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrateKeepsRowsAndIsRepeatable(t *testing.T) {
	s := oldStore(t)
	if err := s.migrate(); err != nil {
		t.Fatal("second migrate:", err)
	}
	var content string
	var kind sql.NullString
	if err := s.db.QueryRow("SELECT content, kind FROM messages WHERE id = 'OLD'").Scan(&content, &kind); err != nil {
		t.Fatal(err)
	}
	if content != "hello" || kind.Valid {
		t.Fatalf("old row changed: %q %v", content, kind)
	}
}

func TestStoreAgainKeepsFilenameAndEditedText(t *testing.T) {
	s := oldStore(t)
	ts := time.Now()
	m := StoredMessage{ID: "OLD", ChatJID: "1@s.whatsapp.net", Sender: "1@s.whatsapp.net", Content: "hello",
		Timestamp: ts, MediaType: "image", Filename: "image_2.jpg", Kind: "image"}
	s.db.Exec("UPDATE messages SET content = 'hello (edited)', edited = 1 WHERE id = 'OLD'")
	if err := s.storeMessage(m); err != nil {
		t.Fatal(err)
	}
	var content, filename, kind string
	s.db.QueryRow("SELECT content, filename, kind FROM messages WHERE id = 'OLD'").Scan(&content, &filename, &kind)
	if content != "hello (edited)" || filename != "image_1.jpg" || kind != "image" {
		t.Fatalf("got %q %q %q", content, filename, kind)
	}
}

func TestReactionKeepsTheLatest(t *testing.T) {
	s := oldStore(t)
	t0 := time.Now()
	s.storeReaction("c", "m", "p", false, "👍", t0)
	s.storeReaction("c", "m", "p", false, "❤️", t0.Add(time.Second))
	s.storeReaction("c", "m", "p", false, "😂", t0.Add(-time.Second)) // older: ignored
	var emoji string
	s.db.QueryRow("SELECT emoji FROM reactions WHERE chat_jid = 'c' AND message_id = 'm' AND sender = 'p'").Scan(&emoji)
	if emoji != "❤️" {
		t.Fatalf("got %q", emoji)
	}
	s.storeReaction("c", "m", "p", false, "", t0.Add(2*time.Second)) // taken back
	s.db.QueryRow("SELECT emoji FROM reactions WHERE chat_jid = 'c' AND message_id = 'm' AND sender = 'p'").Scan(&emoji)
	if emoji != "" {
		t.Fatalf("taken back: got %q", emoji)
	}
}

func TestChatTimeKeepsTheNewest(t *testing.T) {
	s := oldStore(t)
	older, _ := time.Parse(time.RFC3339, "2026-09-01T10:00:00+03:00")
	s.storeChatTime("1@s.whatsapp.net", "A", older)
	var last time.Time
	s.db.QueryRow("SELECT last_message_time FROM chats WHERE jid = '1@s.whatsapp.net'").Scan(&last)
	if last.Month() != time.October {
		t.Fatalf("went back to %v", last)
	}
}

func TestDescribe(t *testing.T) {
	lat, lon := 37.9, 23.7
	cases := []struct {
		name          string
		msg           *waE2E.Message
		kind, subtype string
		content       string
		ok            bool
	}{
		{"text", &waE2E.Message{Conversation: proto.String("hi")}, "text", "", "hi", true},
		{"link", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("see https://x.org"), MatchedText: proto.String("https://x.org")}}, "text", "link", "see https://x.org", true},
		{"caption", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("look")}}, "image", "", "look", true},
		{"gif", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{GifPlayback: proto.Bool(true)}}, "video", "gif", "", true},
		{"video note", &waE2E.Message{PtvMessage: &waE2E.VideoMessage{}}, "video", "video_note", "", true},
		{"voice", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}}, "voice", "", "", true},
		{"sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, "sticker", "", "", true},
		{"location", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: &lat, DegreesLongitude: &lon, Name: proto.String("Syntagma")}}, "location", "", "", true},
		{"contact", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Nikos"), Vcard: proto.String("BEGIN:VCARD\nTEL;type=CELL:+30 690 000 0000\nEND:VCARD")}}, "contact", "", "Nikos, +30 690 000 0000", true},
		{"poll", &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{Name: proto.String("When?"), Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Mon")}, {OptionName: proto.String("Tue")}}}}, "poll", "", "When?\n• Mon\n• Tue", true},
		{"reaction", &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")}}, "", "", "", false},
	}
	for _, c := range cases {
		m := StoredMessage{}
		ok := describe(&m, c.msg, nil)
		if ok != c.ok || m.Kind != c.kind || m.Subtype != c.subtype || m.Content != c.content {
			t.Errorf("%s: got ok=%v kind=%q subtype=%q content=%q", c.name, ok, m.Kind, m.Subtype, m.Content)
		}
	}
	m := StoredMessage{}
	describe(&m, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("yes"),
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("Q1"), QuotedMessage: &waE2E.Message{Conversation: proto.String("coming?")}, IsForwarded: proto.Bool(true)}}}, nil)
	if m.ReplyTo != "Q1" || m.ReplyText != "coming?" || !m.Forwarded {
		t.Errorf("reply: %+v", m)
	}
}

func TestBlockKeepsTheFirstReason(t *testing.T) {
	s := oldStore(t)
	s.block("temporary ban: x")
	s.block("logged out: y")
	if v, _ := s.state("send_blocked"); v != "temporary ban: x" {
		t.Fatalf("got %q", v)
	}
}

func TestSendRefusesWhenOffOrBlocked(t *testing.T) {
	s := oldStore(t)
	off := &Sender{store: s, enabled: false, limits: SendLimits{6, 60, 300, 3}}
	if code, _ := off.send(SendRequest{Recipient: "1", Message: "hi"}); code != 403 {
		t.Fatalf("off: %d", code)
	}
	s.block("temporary ban: x")
	on := &Sender{store: s, enabled: true, limits: SendLimits{6, 60, 300, 3}}
	if code, _ := on.send(SendRequest{Recipient: "1", Message: "hi"}); code != 423 {
		t.Fatalf("blocked: %d", code)
	}
}

func TestLimitsCountRecentSends(t *testing.T) {
	s := oldStore(t)
	now := time.Now().Unix()
	for _, ago := range []int64{10, 30, 50, 3000, 90000} { // three in the last minute, four in the hour, four in a day
		s.db.Exec("INSERT INTO sent (at, chat_jid, id, text_hash) VALUES (?, 'c', 'x', 'h')", now-ago)
	}
	got := (&Sender{store: s}).counts()
	if got["minute"] != 3 || got["hour"] != 4 || got["day"] != 4 {
		t.Fatalf("got %v", got)
	}
}

func TestQuoteNamesWhoWroteTheAnsweredMessage(t *testing.T) {
	s := oldStore(t)
	sender := &Sender{store: s}
	group := types.NewJID("123", types.GroupServer)
	s.db.Exec("INSERT INTO chats VALUES (?, 'G', '2026-10-01 10:00:00+03:00')", group.String())
	s.storeMessage(StoredMessage{ID: "Q", ChatJID: group.String(), Sender: "2@s.whatsapp.net", Content: "",
		Timestamp: time.Now(), Kind: "image", MediaType: "image"})

	var stored StoredMessage
	ci, err := sender.quote(group, SendRequest{ReplyTo: "Q"}, &stored)
	if err != nil || ci.GetParticipant() != "2@s.whatsapp.net" || ci.GetStanzaID() != "Q" || ci.GetQuotedMessage().GetImageMessage() == nil {
		t.Fatalf("a message the bridge has: %v %v", ci, err)
	}
	// one from before the bridge: who wrote it comes with the request
	ci, err = sender.quote(group, SendRequest{ReplyTo: "OLDER", ReplySender: "+306900000000", ReplyText: "hi"}, &stored)
	if err != nil || ci.GetParticipant() != "306900000000@s.whatsapp.net" || ci.GetQuotedMessage().GetConversation() != "hi" {
		t.Fatalf("a message from before: %v %v", ci, err)
	}
	if stored.ReplyTo != "OLDER" || stored.ReplyText != "hi" {
		t.Fatalf("the answer keeps what it answers: %+v", stored)
	}
	// in a group, without it, nobody can tell
	if _, err := sender.quote(group, SendRequest{ReplyTo: "OLDER"}, &stored); err == nil {
		t.Fatal("a group's unknown message was quoted without its sender")
	}
	// in a person's chat it is theirs
	person := types.NewJID("1", types.DefaultUserServer)
	if ci, _ := sender.quote(person, SendRequest{ReplyTo: "OLDER"}, &stored); ci.GetParticipant() != person.String() {
		t.Fatalf("a person's chat: %v", ci)
	}
	if ci, _ := sender.quote(person, SendRequest{}, &stored); ci != nil {
		t.Fatal("nothing answered, yet a quote")
	}
}

func TestFileKindAndThumbnail(t *testing.T) {
	for _, c := range []struct {
		mime    string
		caption bool
		want    string
	}{{"image/jpeg", true, "image"}, {"image/png", false, "image"}, {"image/webp", false, "document"},
		{"video/mp4", true, "video"}, {"audio/ogg", false, "audio"}, {"audio/ogg", true, "document"},
		{"application/pdf", false, "document"}} {
		if got := fileKind(c.mime, c.caption); got != c.want {
			t.Errorf("%s caption=%v: %s, want %s", c.mime, c.caption, got, c.want)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 400, 200)))
	thumb, w, h := thumbnail(buf.Bytes())
	if w != 400 || h != 200 || len(thumb) == 0 {
		t.Fatalf("thumbnail: %d bytes, %dx%d", len(thumb), w, h)
	}
	small, _, _ := image.DecodeConfig(bytes.NewReader(thumb))
	if small.Width != 96 || small.Height != 48 {
		t.Fatalf("thumbnail size %dx%d", small.Width, small.Height)
	}
	if thumb, _, _ := thumbnail([]byte("not a picture")); thumb != nil {
		t.Fatal("a thumbnail of what is not a picture")
	}
}

func TestMediaFileNamesAreSafeAndPerMessage(t *testing.T) {
	if got := mediaFile("image", "image_1.jpg", "1@s.whatsapp.net", "ABC"); got != "media/1@s.whatsapp.net/ABC.jpg" {
		t.Fatal(got)
	}
	if got := mediaFile("document", "Report.PDF", "12:3@lid", "a/b"); got != "media/12_3@lid/a_b.pdf" {
		t.Fatal(got)
	}
}

func TestDownloadAnswersFromWhatIsThere(t *testing.T) {
	s := oldStore(t)
	if _, err := s.download(nil, "OLD", "1@s.whatsapp.net"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("no keys yet: %v", err)
	}
	rel := mediaFile("image", "", "1@s.whatsapp.net", "OLD")
	if err := s.writeMedia(rel, []byte("x")); err != nil {
		t.Fatal(err)
	}
	s.db.Exec("UPDATE messages SET media_path = ? WHERE id = 'OLD'", rel)
	if path, err := s.download(nil, "OLD", "1@s.whatsapp.net"); err != nil || !strings.HasSuffix(path, rel) {
		t.Fatalf("a file already there: %q %v", path, err)
	}
}

func groupStore(t *testing.T, addressing types.AddressingMode) (*MessageStore, types.JID) {
	s := oldStore(t)
	group := types.NewJID("123", types.GroupServer)
	pn, lid := types.NewJID("306912345678", types.DefaultUserServer), types.NewJID("98765", types.HiddenUserServer)
	primary := pn
	if addressing == types.AddressingModeLID {
		primary = lid
	}
	err := s.storeGroup(&types.GroupInfo{JID: group, GroupName: types.GroupName{Name: "G"}, AddressingMode: addressing,
		Participants: []types.GroupParticipant{{JID: primary, PhoneNumber: pn, LID: lid, IsAdmin: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return s, group
}

func TestGroupMembersAndHowTheGroupNamesThem(t *testing.T) {
	s, group := groupStore(t, types.AddressingModeLID)
	for _, who := range []string{"306912345678@s.whatsapp.net", "98765@lid"} {
		if got := s.mentionJID(group, who); got != "98765@lid" {
			t.Errorf("%s in a LID group: %q", who, got)
		}
	}
	if got := s.mentionJID(group, "1@s.whatsapp.net"); got != "" {
		t.Errorf("not a member: %q", got)
	}
	// read again: the members are replaced, not added to
	s.storeGroup(&types.GroupInfo{JID: group, AddressingMode: types.AddressingModeLID})
	if got := s.mentionJID(group, "98765@lid"); got != "" {
		t.Errorf("left, still a member: %q", got)
	}
	s, group = groupStore(t, types.AddressingModePN)
	if got := s.mentionJID(group, "98765@lid"); got != "306912345678@s.whatsapp.net" {
		t.Errorf("a number group: %q", got)
	}
}

func TestMentionsAreWrittenAsTheGroupNamesThem(t *testing.T) {
	s, group := groupStore(t, types.AddressingModeLID)
	sender := &Sender{store: s}
	text, jids, err := sender.mentions(group, "hi @306912345678, and @3069123456789", []string{"+306912345678"})
	if err != nil || text != "hi @98765, and @3069123456789" || len(jids) != 1 || jids[0] != "98765@lid" {
		t.Fatalf("%q %v %v", text, jids, err)
	}
	// named twice, given twice: both places rewritten, one jid
	text, jids, err = sender.mentions(group, "@306912345678 hi @306912345678", []string{"306912345678", "306912345678"})
	if err != nil || text != "@98765 hi @98765" || len(jids) != 1 {
		t.Fatalf("twice: %q %v %v", text, jids, err)
	}
	if _, _, err := sender.mentions(group, "hi", []string{"306912345678"}); err == nil {
		t.Fatal("a mention not in the text")
	}
	if _, _, err := sender.mentions(group, "hi @1", []string{"1"}); err == nil {
		t.Fatal("a mention of someone not in the group")
	}
	if _, _, err := sender.mentions(types.NewJID("1", types.DefaultUserServer), "hi @1", []string{"1"}); err == nil {
		t.Fatal("a mention in a person's chat")
	}
	if text, jids, err := sender.mentions(group, "plain", nil); text != "plain" || jids != nil || err != nil {
		t.Fatal("no mentions changed the text")
	}
}

func TestIncomingMentionsAreKept(t *testing.T) {
	s := oldStore(t)
	m := StoredMessage{ID: "M", ChatJID: "1@s.whatsapp.net", Timestamp: time.Now()}
	describe(&m, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("@98765 look"),
		ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{"98765@lid"}}}}, nil)
	if err := s.storeMessage(m); err != nil {
		t.Fatal(err)
	}
	var mentions string
	s.db.QueryRow("SELECT mentions FROM messages WHERE id = 'M'").Scan(&mentions)
	if mentions != "98765@lid" {
		t.Fatalf("mentions %q", mentions)
	}
}

func TestReceiptsWhoGotAndReadAndWhatIRead(t *testing.T) {
	s := oldStore(t)
	group := types.NewJID("123", types.GroupServer)
	reader := types.JID{User: "306912345678", Server: types.DefaultUserServer, Device: 3}
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	receipt := func(kind types.ReceiptType, when time.Time, ids ...string) *events.Receipt {
		return &events.Receipt{MessageSource: types.MessageSource{Chat: group, Sender: reader}, MessageIDs: ids, Timestamp: when, Type: kind}
	}
	handleReceipt(s, receipt(types.ReceiptTypeDelivered, at, "A", "B"), nil)
	handleReceipt(s, receipt(types.ReceiptTypeRead, at.Add(time.Minute), "A"), nil)
	handleReceipt(s, receipt(types.ReceiptTypeRead, at.Add(time.Hour), "A"), nil) // again: the first time stays
	handleReceipt(s, receipt(types.ReceiptTypeSender, at, "A"), nil)              // to my own devices: not a receipt
	var n int
	s.db.QueryRow("SELECT count(*) FROM receipts").Scan(&n)
	if n != 3 {
		t.Fatalf("%d receipts", n)
	}
	var who string
	var when time.Time
	s.db.QueryRow("SELECT jid, timestamp FROM receipts WHERE message_id = 'A' AND type = 'read'").Scan(&who, &when)
	if who != "306912345678@s.whatsapp.net" || !when.Equal(at.Add(time.Minute)) {
		t.Fatalf("read by %s at %v", who, when)
	}
	// read on the phone: the message from others is marked, once
	handleReceipt(s, receipt(types.ReceiptTypeReadSelf, at, "OLD"), nil)
	handleReceipt(s, receipt(types.ReceiptTypeReadSelf, at.Add(time.Hour), "OLD"), nil)
	var read time.Time
	s.db.QueryRow("SELECT read_at FROM messages WHERE id = 'OLD'").Scan(&read)
	if !read.Equal(at) {
		t.Fatalf("read at %v", read)
	}
}

func TestHistoryReceipts(t *testing.T) {
	s := oldStore(t)
	group := types.NewJID("123", types.GroupServer)
	historyReceipts(s, group, &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String("G"), FromMe: proto.Bool(true)},
		UserReceipt: []*waWeb.UserReceipt{{UserJID: proto.String("1@s.whatsapp.net"), ReceiptTimestamp: proto.Int64(100),
			ReadTimestamp: proto.Int64(200)}}})
	person := types.NewJID("2", types.DefaultUserServer)
	status := waWeb.WebMessageInfo_READ
	historyReceipts(s, person, &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String("P"), FromMe: proto.Bool(true)},
		Status: &status})
	historyReceipts(s, person, &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String("THEIRS")}, Status: &status})
	got := map[string]bool{}
	rows, _ := s.db.Query("SELECT message_id || ' ' || jid || ' ' || type FROM receipts")
	for rows.Next() {
		var r string
		rows.Scan(&r)
		got[r] = true
	}
	rows.Close()
	want := []string{"G 1@s.whatsapp.net delivered", "G 1@s.whatsapp.net read", "P 2@s.whatsapp.net read", "P 2@s.whatsapp.net delivered"}
	if len(got) != len(want) {
		t.Fatalf("receipts %v", got)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing %s in %v", w, got)
		}
	}
}

func TestUnreadIsRecentMessagesFromOthersBySender(t *testing.T) {
	s := oldStore(t)
	sender := &Sender{store: s}
	group := types.NewJID("123", types.GroupServer)
	s.db.Exec("INSERT INTO chats VALUES (?, 'G', ?)", group.String(), time.Now())
	now := time.Now()
	for _, m := range []StoredMessage{
		{ID: "1", Sender: "a@s.whatsapp.net", Timestamp: now.Add(-time.Hour)},
		{ID: "2", Sender: "b@s.whatsapp.net", Timestamp: now.Add(-time.Minute)},
		{ID: "3", Sender: "a@s.whatsapp.net", Timestamp: now.Add(-30 * time.Minute)},
		{ID: "MINE", IsFromMe: true, Timestamp: now.Add(-time.Minute)},
		{ID: "OLDER", Sender: "a@s.whatsapp.net", Timestamp: now.Add(-8 * 24 * time.Hour)},
		{ID: "LATER", Sender: "a@s.whatsapp.net", Timestamp: now.Add(time.Hour)},
		{ID: "READ", Sender: "b@s.whatsapp.net", Timestamp: now.Add(-time.Minute)},
	} {
		m.ChatJID, m.Kind = group.String(), "text"
		s.storeMessage(m)
	}
	s.markedRead([]string{"READ"}, now)
	got, err := sender.unread(group, now)
	if err != nil || len(got) != 2 || strings.Join(got["a@s.whatsapp.net"], ",") != "1,3" || strings.Join(got["b@s.whatsapp.net"], ",") != "2" {
		t.Fatalf("%v %v", got, err)
	}
}
