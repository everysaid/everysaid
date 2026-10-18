package whatsapp

// Tests run in temporary folders, keys.db among them: never on a real store.

import (
	"bytes"
	"errors"
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
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"everysaid/internal/importers"
)

// testStore is a store folder's part of keys.db, writing nowhere.
func testStore(t *testing.T) *Store {
	s, err := OpenStore(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// count is the first column of a query on keys.db.
func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestChatTimeKeepsTheNewest(t *testing.T) {
	s := testStore(t)
	newer := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	s.storeChat("1@s.whatsapp.net", "A", newer)
	s.storeChat("1@s.whatsapp.net", "", newer.Add(-30*24*time.Hour)) // history, older: the name kept too
	var last int64
	var name string
	s.db.QueryRow("SELECT last_at, name FROM whatsapp_chat WHERE store = ? AND jid = '1@s.whatsapp.net'", s.key).Scan(&last, &name)
	if last != newer.UnixMilli() || name != "A" {
		t.Fatalf("went back to %v %q", time.UnixMilli(last), name)
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
	// a picture of an album, forwarded from a channel
	m = StoredMessage{}
	describe(&m, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{URL: proto.String("https://mmg.whatsapp.net/v/x"),
		MediaKey: []byte("k"), ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true),
			ForwardedNewsletterMessageInfo: &waE2E.ContextInfo_ForwardedNewsletterMessageInfo{NewsletterName: proto.String("News")}}},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageAssociation: &waE2E.MessageAssociation{
			AssociationType: waE2E.MessageAssociation_MEDIA_ALBUM.Enum(), ParentMessageKey: &waCommon.MessageKey{ID: proto.String("ALB")}}}}, nil)
	if m.Kind != "image" || m.ForwardFrom != "News" || m.Album != "ALB" {
		t.Errorf("album: %+v", m)
	}
	// as the archive's writer takes it: its file, what downloads it
	w := m.wa()
	if w.File == nil || w.File.URL != "https://mmg.whatsapp.net/v/x" || string(w.File.MediaKey) != "k" || w.Album != "ALB" {
		t.Errorf("for the archive: %+v %+v", w, w.File)
	}
}

func TestBlockKeepsTheFirstReason(t *testing.T) {
	s := testStore(t)
	s.block("temporary ban: x")
	s.block("logged out: y")
	if v, _ := s.state("send_blocked"); v != "temporary ban: x" {
		t.Fatalf("got %q", v)
	}
}

func TestSendRefusesWhenOffOrBlocked(t *testing.T) {
	s := testStore(t)
	off := &Sender{store: s, enabled: false, limits: SendLimits{6, 60, 300, 3, 5}}
	if code, _ := off.send(SendRequest{Recipient: "1", Message: "hi"}); code != 403 {
		t.Fatalf("off: %d", code)
	}
	s.block("temporary ban: x")
	on := &Sender{store: s, enabled: true, limits: SendLimits{6, 60, 300, 3, 5}}
	if code, _ := on.send(SendRequest{Recipient: "1", Message: "hi"}); code != 423 {
		t.Fatalf("blocked: %d", code)
	}
}

func TestLimitsCountRecentSends(t *testing.T) {
	s := testStore(t)
	now := time.Now().Unix()
	for _, ago := range []int64{10, 30, 50, 3000, 90000} { // three in the last minute, four in the hour, four in a day
		s.exec("INSERT INTO whatsapp_sent (store, at, chat_jid, id, text_hash) VALUES (?, ?, 'c', 'x', 'h')", s.key, now-ago)
	}
	got := (&Sender{store: s}).counts()
	if got["minute"] != 3 || got["hour"] != 4 || got["day"] != 4 {
		t.Fatalf("got %v", got)
	}
}

func TestQuoteNamesWhoWroteTheAnsweredMessage(t *testing.T) {
	s := testStore(t)
	sender := &Sender{store: s}
	group := types.NewJID("123", types.GroupServer)
	s.remember(StoredMessage{ID: "Q", ChatJID: group.String(), Sender: "2@lid", Timestamp: time.Now(), Kind: "image"})

	var stored StoredMessage
	ci, err := sender.quote(group, SendRequest{ReplyTo: "Q", ReplySender: "+302", ReplyKind: "image"}, &stored)
	if err != nil || ci.GetParticipant() != "2@lid" || ci.GetStanzaID() != "Q" || ci.GetQuotedMessage().GetImageMessage() == nil {
		t.Fatalf("a message of the last week: named as it came: %v %v", ci, err)
	}
	// an older one: who wrote it comes with the request, as the archive has it
	ci, err = sender.quote(group, SendRequest{ReplyTo: "OLDER", ReplySender: "+306900000000", ReplyText: "hi"}, &stored)
	if err != nil || ci.GetParticipant() != "306900000000@s.whatsapp.net" || ci.GetQuotedMessage().GetConversation() != "hi" {
		t.Fatalf("an older message: %v %v", ci, err)
	}
	if stored.ReplyTo != "OLDER" || stored.ReplyText != "hi" {
		t.Fatalf("the answer keeps what it answers: %+v", stored)
	}
	ci, _ = sender.quote(group, SendRequest{ReplyTo: "DOC", ReplySender: "+302", ReplyKind: "document", ReplyFilename: "a.pdf"}, &stored)
	if ci.GetQuotedMessage().GetDocumentMessage().GetFileName() != "a.pdf" {
		t.Fatalf("a document: %v", ci)
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
	if localFile("/s", "../x") != "" || localFile("/s", "") != "" || localFile("/s", "media/a.jpg") != "/s/media/a.jpg" {
		t.Fatal("a file outside the store folder")
	}
}

// A file with what downloads it incomplete is given up; one downloaded already goes into the
// archive, not downloaded again.
func TestDownloadAnswersFromWhatIsThere(t *testing.T) {
	s := testStore(t)
	s.exec("INSERT INTO whatsapp_media (store, chat_jid, id, at, media_type) VALUES (?, '1@s.whatsapp.net', 'OLD', 0, 'image')", s.key)
	if err := s.download(nil, "OLD", "1@s.whatsapp.net"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("no keys yet: %v", err)
	}
	if count(t, s, "SELECT count(*) FROM whatsapp_media WHERE error IS NOT NULL") != 1 {
		t.Fatal("not recorded")
	}
	rel := mediaFile("image", "", "1@s.whatsapp.net", "OLD")
	if err := s.writeMedia(rel, []byte("x")); err != nil {
		t.Fatal(err)
	}
	s.exec("UPDATE whatsapp_media SET path = ? WHERE id = 'OLD'", rel)
	taken := 0
	s.write = func(fn func(w *importers.WhatsAppWriter) error) error { taken++; return nil }
	if err := s.download(nil, "OLD", "1@s.whatsapp.net"); err != nil || taken != 1 {
		t.Fatalf("a file already there: %v, taken %d", err, taken)
	}
	if err := s.download(nil, "NONE", "1@s.whatsapp.net"); err == nil {
		t.Fatal("a file of no message")
	}
}

func groupStore(t *testing.T, addressing types.AddressingMode) (*Store, types.JID) {
	s := testStore(t)
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
	m := StoredMessage{ID: "M", ChatJID: "1@s.whatsapp.net", Timestamp: time.Now()}
	describe(&m, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("@98765 look"),
		ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{"98765@lid"}}}}, nil)
	if got := m.wa().Mentions; len(got) != 1 || got[0] != "98765@lid" {
		t.Fatalf("mentions %q", got)
	}
}

func TestReceiptsWhoGotAndReadAndWhatIRead(t *testing.T) {
	s := testStore(t)
	group := types.NewJID("123", types.GroupServer)
	reader := types.JID{User: "306912345678", Server: types.DefaultUserServer, Device: 3}
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	receipt := func(kind types.ReceiptType, when time.Time, ids ...string) *events.Receipt {
		return &events.Receipt{MessageSource: types.MessageSource{Chat: group, Sender: reader}, MessageIDs: ids, Timestamp: when, Type: kind}
	}
	b := &importers.WABatch{}
	handleReceipt(s, b, receipt(types.ReceiptTypeDelivered, at, "A", "B"))
	handleReceipt(s, b, receipt(types.ReceiptTypeRead, at.Add(time.Minute), "A"))
	handleReceipt(s, b, receipt(types.ReceiptTypeSender, at, "A")) // to my own devices: not a receipt
	if len(b.Receipts) != 3 || b.Receipts[2] != (importers.WAReceipt{Chat: group.String(), Target: "A",
		Who: "306912345678@s.whatsapp.net", Kind: "read", At: at.Add(time.Minute).UnixMilli()}) {
		t.Fatalf("receipts %+v", b.Receipts)
	}
	// read on the phone: the message from others is marked, once
	s.remember(StoredMessage{ID: "OLD", ChatJID: group.String(), Sender: "1@s.whatsapp.net", Timestamp: at.Add(-time.Hour)})
	handleReceipt(s, b, receipt(types.ReceiptTypeReadSelf, at, "OLD"))
	handleReceipt(s, b, receipt(types.ReceiptTypeReadSelf, at.Add(time.Hour), "OLD"))
	if count(t, s, "SELECT read_at FROM whatsapp_recent WHERE id = 'OLD'") != int(at.UnixMilli()) {
		t.Fatal("not marked read, or not the first time")
	}
	if len(b.Reads) != 2 || b.Reads[0].Chat != group.String() || b.Reads[0].IDs[0] != "OLD" || b.Reads[0].At != at.UnixMilli() {
		t.Fatalf("reads %+v", b.Reads)
	}
}

func TestHistoryReceipts(t *testing.T) {
	b := &importers.WABatch{}
	group := types.NewJID("123", types.GroupServer)
	historyReceipts(b, group, &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String("G"), FromMe: proto.Bool(true)},
		UserReceipt: []*waWeb.UserReceipt{{UserJID: proto.String("1@s.whatsapp.net"), ReceiptTimestamp: proto.Int64(100),
			ReadTimestamp: proto.Int64(200)}}})
	person := types.NewJID("2", types.DefaultUserServer)
	status := waWeb.WebMessageInfo_READ
	historyReceipts(b, person, &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String("P"), FromMe: proto.Bool(true)},
		Status: &status})
	historyReceipts(b, person, &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String("THEIRS")}, Status: &status})
	got := map[string]bool{}
	for _, r := range b.Receipts {
		got[r.Target+" "+r.Who+" "+r.Kind] = true
	}
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
	s := testStore(t)
	sender := &Sender{store: s}
	group := types.NewJID("123", types.GroupServer)
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
		s.remember(m)
	}
	s.markedRead(&importers.WABatch{}, group.String(), []string{"READ"}, now)
	got, err := sender.unread(group, now)
	if err != nil || len(got) != 2 || strings.Join(got["a@s.whatsapp.net"], ",") != "1,3" || strings.Join(got["b@s.whatsapp.net"], ",") != "2" {
		t.Fatalf("%v %v", got, err)
	}
}

// A link's preview is kept as the sender's app made it: its small picture, and where its
// high-quality one is (to download).
func TestLinkPreviewKept(t *testing.T) {
	msg := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("see https://x.org/a"),
		MatchedText: proto.String("https://x.org/a"), Title: proto.String("A page"), Description: proto.String("About it"),
		JPEGThumbnail: []byte("small"), ThumbnailDirectPath: proto.String("/v/t62/x"), ThumbnailSHA256: []byte("sha"),
		ThumbnailEncSHA256: []byte("enc"), MediaKey: []byte("key")}}
	m := StoredMessage{ID: "L", ChatJID: "1@s.whatsapp.net", Sender: "1@s.whatsapp.net", Timestamp: time.Now()}
	if !describe(&m, msg, nil) || m.Subtype != "link" {
		t.Fatalf("described: %+v", m)
	}
	w := m.wa()
	if p := w.Preview; p == nil || p.URL != "https://x.org/a" || p.Title != "A page" || p.Description != "About it" || string(p.Image) != "small" {
		t.Errorf("preview %+v", w.Preview)
	}
	if f := w.PreviewFile; f == nil || f.DirectPath != "/v/t62/x" || string(f.Key) != "key" || string(f.EncSHA256) != "enc" {
		t.Errorf("its picture %+v", w.PreviewFile)
	}
	// a link without a preview: nothing
	m = StoredMessage{}
	describe(&m, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("x.org"),
		MatchedText: proto.String("x.org")}}, nil)
	if m.wa().Preview != nil || m.Subtype != "link" {
		t.Errorf("no preview: %+v", m.Preview)
	}
}

// What an event brings is written in one batch; one the archive could not take is not
// acknowledged (WhatsApp gives it again).
func TestABatchNotWrittenIsNotAcknowledged(t *testing.T) {
	s := testStore(t)
	if !s.Write(&importers.WABatch{}, waLog.Noop) {
		t.Fatal("nothing to write, yet not written")
	}
	b := &importers.WABatch{Messages: []importers.WAMessage{{Chat: "1@s.whatsapp.net", ID: "X"}}}
	if s.Write(b, waLog.Noop) {
		t.Fatal("written nowhere, yet acknowledged")
	}
	s.write = func(fn func(w *importers.WhatsAppWriter) error) error { return errors.New("the archive is busy") }
	if s.Write(b, waLog.Noop) {
		t.Fatal("not written, yet acknowledged")
	}
}
