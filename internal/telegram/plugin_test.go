package telegram

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/tg"

	"everysaid/internal/archive"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

func video(id int, peer tg.PeerClass, docID int64, mime string, attrs ...tg.DocumentAttributeClass) *tg.Message {
	m := text(id, peer, 0, 1600000500+id, "", false)
	doc := &tg.Document{ID: docID, AccessHash: 1, FileReference: []byte("r"), MimeType: mime, Size: 11, DCID: 2, Attributes: attrs}
	media := &tg.MessageMediaDocument{}
	media.SetDocument(doc)
	m.SetMedia(media)
	return m
}

func TestMedia(t *testing.T) {
	freshCache(t)
	f := account()
	peer := &tg.PeerUser{UserID: 2}
	f.chats[0].messages = append(f.chats[0].messages,
		video(300, peer, 77, "video/mp4"),
		video(301, peer, 78, "application/x-tgsticker", &tg.DocumentAttributeSticker{Stickerset: &tg.InputStickerSetEmpty{}}),
		video(302, peer, 79, "audio/ogg", &tg.DocumentAttributeAudio{Voice: true}))
	f.files[77] = []byte("video bytes")
	f.files[79] = []byte("voice")
	var out bytes.Buffer
	if err := syncRun(newTestCtx(), f.conn(), en(&out)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := mediaRun(newTestCtx(), nil, true, nil, en(&out)); err != nil {
		t.Fatal(err)
	}
	if out.String() != "2 files, 0.00 GB, in 1 chats\n" {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	if err := mediaRun(newTestCtx(), f.conn(), false, map[int64]bool{2: true}, en(&out)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(MediaPath(), "2", "300.mp4"))
	if err != nil || string(got) != "video bytes" {
		t.Fatalf("%v %q", err, got)
	}
	if _, err := os.Stat(filepath.Join(MediaPath(), "2", "302.ogg")); err != nil {
		t.Fatal(err)
	}
	d, _ := db.ReadOnly(DBPath())
	defer d.Close()
	if files := db.Strs(d, "SELECT file FROM message WHERE file IS NOT NULL ORDER BY id"); !reflect.DeepEqual(files, []string{"2/300.mp4", "2/302.ogg"}) {
		t.Fatalf("%v", files)
	}
	if !strings.HasSuffix(out.String(), "2 downloaded into "+MediaPath()+"\n") {
		t.Fatalf("%q", out.String())
	}
	// none in other chats asked for
	out.Reset()
	mediaRun(newTestCtx(), nil, true, map[int64]bool{-10: true}, en(&out))
	if out.String() != "0 files, 0.00 GB, in 0 chats\n" {
		t.Fatalf("%q", out.String())
	}
}

func TestWantedAndFiles(t *testing.T) {
	for js, want := range map[string][2]any{
		`{"media":{"_":"MessageMediaPhoto","photo":{"_":"Photo","sizes":[{"_":"PhotoSize","size":10},{"_":"PhotoSizeProgressive","sizes":[5,900]}]}}}`: {true, int64(900)},
		`{"media":{"_":"MessageMediaPhoto","photo":null}}`: {false, int64(0)},
		`{"media":{"_":"MessageMediaDocument","document":{"mime_type":"image/webp","size":4,"attributes":[{"_":"DocumentAttributeSticker"}]}}}`: {false, int64(0)},
		`{"media":{"_":"MessageMediaDocument","document":{"mime_type":"video/mp4","size":4,"attributes":[{"_":"DocumentAttributeAnimated"}]}}}`: {true, int64(4)},
		`{"media":{"_":"MessageMediaDocument","document":{"mime_type":"application/pdf","size":4,"attributes":[]}}}`:                            {false, int64(0)},
		`{"media":null}`: {false, int64(0)},
	} {
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(js))
		dec.UseNumber()
		dec.Decode(&m)
		ok, n := wanted(m)
		if ok != want[0] || n != want[1] {
			t.Errorf("%s: %v %v", js, ok, n)
		}
	}
	doc := &tg.Document{MimeType: "application/x-unknown", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "a.b.tar"}}}
	if fileExt(nil, doc) != ".tar" || fileExt(&tg.Photo{}, nil) != ".jpg" || fileExt(nil, &tg.Document{MimeType: "image/png"}) != ".png" {
		t.Fatal("ext")
	}
	big := &tg.PhotoSize{Type: "y", Size: 900}
	if largest([]any{&tg.PhotoStrippedSize{Bytes: []byte{1, 2, 3}}, big, &tg.PhotoSize{Type: "x", Size: 10}, &tg.PhotoPathSize{}}) != big {
		t.Fatal("largest")
	}
	if j := strippedToJPEG([]byte{1, 9, 8, 7}); len(j) != 623+1+2 || j[164] != 9 || j[166] != 8 {
		t.Fatal("stripped")
	}
}

// --- the plugin on an archive ----------------------------------------------------------------------

type host struct {
	store  *core.Store
	mu     sync.Mutex
	events []M
}

func (h *host) Store() *core.Store   { return h.store }
func (h *host) Emit(e M)             { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() }
func (h *host) Alert(string, string) {}

// instance is an archive with a Telegram instance; the importers' calls are recorded.
type instance struct {
	h       *host
	iid     int64
	path    string
	keys    []map[[2]int64]bool
	reads   []map[int64]bool
	imports int
}

func newInstance(t *testing.T, settings M) *instance {
	freshCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	store, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	iid, err := plugins.Create(store, "telegram", "Telegram", settings)
	if err != nil {
		t.Fatal(err)
	}
	in := &instance{h: &host{store: store}, iid: iid, path: path}
	oldT, oldR := importTelegram, importReads
	t.Cleanup(func() { importTelegram, importReads = oldT, oldR })
	importTelegram = func(a *archive.Archive, out func(string), only map[[2]int64]bool, skip map[int64]bool) error {
		in.keys = append(in.keys, only)
		in.imports++
		return nil
	}
	importReads = func(a *archive.Archive, chats map[int64]bool) error {
		in.reads = append(in.reads, chats)
		return nil
	}
	return in
}

func (in *instance) ctx() *plugins.Context {
	return plugins.NewContext(in.h, *plugins.GetInstance(in.h.store, in.iid))
}

func TestStoreMessages(t *testing.T) {
	in := newInstance(t, M{"skip_chats": []any{-10}})
	c := in.ctx()
	bob := user(2, "Bob", 22)
	n, err := storeMessages(c, bob, []sent{{text(5, &tg.PeerUser{UserID: 2}, 0, 1600000000, "hi", false), bob}})
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if !reflect.DeepEqual(in.keys, []map[[2]int64]bool{{{2, 5}: true}}) {
		t.Fatalf("%v", in.keys)
	}
	// a chat the user left out: kept in telegram.db, not imported
	group := &tg.Chat{ID: 10, Title: "Friends", Photo: &tg.ChatPhotoEmpty{}}
	if n, _ := storeMessages(c, group, []sent{{text(1, &tg.PeerChat{ChatID: 10}, 2, 1600000000, "g", false), nil}}); n != 0 || in.imports != 1 {
		t.Fatal("skip_chats")
	}
	// channels and bots stay out
	ch := &tg.Channel{ID: 20, Title: "News", Photo: &tg.ChatPhotoEmpty{}}
	ch.SetBroadcast(true)
	if n, _ := storeMessages(c, ch, []sent{{text(1, &tg.PeerChannel{ChannelID: 20}, 0, 1600000000, "n", false), nil}}); n != 0 {
		t.Fatal("channel")
	}
	d, _ := db.ReadOnly(DBPath())
	defer d.Close()
	if ids := db.Ints(d, "SELECT id FROM chat ORDER BY id"); !reflect.DeepEqual(ids, []int64{-10, 2}) {
		t.Fatalf("%v", ids)
	}
	// reads: only chats telegram.db knows, only when they move on
	if err := noteReads(c, []readItem{{2, intp(5), nil}, {99, intp(1), nil}}, false); err != nil {
		t.Fatal(err)
	}
	if err := noteReads(c, []readItem{{2, intp(4), nil}}, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in.reads, []map[int64]bool{{2: true}}) {
		t.Fatalf("%v", in.reads)
	}
	var outboxAt *int64
	noteReads(c, []readItem{{2, nil, intp(5)}}, true)
	db.Row(d, "SELECT outbox_at FROM chat_read WHERE chat_id = 2", nil, &outboxAt)
	if outboxAt == nil {
		t.Fatal("seen now: its time")
	}
}

func TestReportStates(t *testing.T) {
	in := newInstance(t, M{})
	c := in.ctx()
	a, _ := archive.Open(in.path)
	a.Conversation("telegram", []archive.Handle{archive.H("id", "2", "telegram")}, "2", "Bob")
	a.Commit()
	a.Close()
	f := account()
	ds, _ := f.conn().dialogs(newTestCtx())
	if err := reportStates(c, dialogStates(ds)); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	db.Each(in.h.store.Read(), "SELECT field, value FROM state_report", nil, func(scan func(...any)) {
		var k string
		var v int64
		scan(&k, &v)
		got[k] = v
	})
	if !reflect.DeepEqual(got, map[string]int64{"archived": 0, "pinned": 1, "muted": 2147483647000}) {
		t.Fatalf("%v", got)
	}
	var ns tg.PeerNotifySettings
	if untilMS(ns) != 0 {
		t.Fatal("not muted")
	}
}

func TestSendAndRead(t *testing.T) {
	in := newInstance(t, M{"read_receipts": true})
	c := in.ctx()
	a, _ := archive.Open(in.path)
	bobID := a.Address(archive.H("id", "2", "telegram"))
	conv := a.Conversation("telegram", []archive.Handle{archive.H("id", "2", "telegram")}, "2", "Bob")
	src := a.Source(Source, DBPath(), "telegram", MediaPath())
	a.AddMessage(src, "2/7", archive.Message{Service: "telegram", ConversationID: conv, TS: 1000, SenderID: bobID, Kind: "text", Text: "x", Key: "7"})
	a.AddMessage(src, "2/9", archive.Message{Service: "telegram", ConversationID: conv, TS: 3000, SenderID: bobID, Kind: "text", Text: "y", Key: "9"})
	a.Commit()
	a.Close()

	f := account()
	cn := f.conn()
	if _, err := cn.dialogs(newTestCtx()); err != nil {
		t.Fatal(err)
	}
	_, release, _ := one.take(newTestCtx(), false, nil)
	defer release()
	defer one.share(cn)()
	var p Plugin
	res, err := p.Send(newTestCtx(), c, plugins.Conversation{ID: conv, Key: "2", Service: "telegram"}, "🙂 @Bob, hi",
		&plugins.Reply{ID: 1, Key: "7"}, []plugins.Mention{{Start: 2, Length: 4, AddressID: bobID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.(M)["id"] != 1001 {
		t.Fatalf("%v", res)
	}
	req := f.requests[0].(*tg.MessagesSendMessageRequest)
	if req.Message != "🙂 Bob, hi" {
		t.Fatalf("%q", req.Message)
	}
	e := req.Entities[0].(*tg.InputMessageEntityMentionName)
	if e.Offset != 3 || e.Length != 3 || e.UserID.(*tg.InputUser).AccessHash != 22 {
		t.Fatalf("%+v", e) // 🙂 is two UTF-16 units, then a space
	}
	if r, ok := req.ReplyTo.(*tg.InputReplyToMessage); !ok || r.ReplyToMsgID != 7 {
		t.Fatal("reply")
	}
	d, _ := db.ReadOnly(DBPath())
	var js string
	db.Row(d, "SELECT json FROM message WHERE chat_id = 2 AND id = 1001", nil, &js)
	d.Close()
	var m map[string]any
	json.Unmarshal([]byte(js), &m)
	if m["out"] != true || m["reply_to"].(map[string]any)["reply_to_msg_id"] != 7.0 || m["message"] != "🙂 Bob, hi" {
		t.Fatalf("%s", js)
	}

	// read receipts up to the newest of theirs at or before the time
	n, err := p.MarkRead(newTestCtx(), c, plugins.Conversation{ID: conv, Key: "2"}, 2000)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if r := f.requests[1].(*tg.MessagesReadHistoryRequest); r.MaxID != 7 {
		t.Fatalf("%d", r.MaxID)
	}
	off := newInstance(t, M{})
	if n, _ := p.MarkRead(newTestCtx(), off.ctx(), plugins.Conversation{ID: conv, Key: "2"}, 2000); n != 0 {
		t.Fatal("receipts are off")
	}
}

func TestSyncLines(t *testing.T) {
	in := newInstance(t, M{})
	c := in.ctx()
	last := ""
	term := sourcekit.Terminal(c)
	w := lastLine{term, &last}
	w.Write([]byte("one\n\rBob: 1000\rBob: 2000"))
	w.Write([]byte("\ruser 2000 new  Bob\nlast"))
	term.Close()
	if got := c.LastLines(10); !reflect.DeepEqual(got, []string{"one", "last"}) || c.Bar != "user 2000 new  Bob" || last != "last" {
		t.Fatalf("%q %q %q", got, c.Bar, last)
	}
}

func TestManifest(t *testing.T) {
	m := plugins.Manifest(plugins.Get("telegram"), "el")
	if m["can_send"] != true || m["live_default"] != true || m["has_chats"] != true {
		t.Fatalf("%v", m)
	}
	if plugins.Services("en")["telegram"]["short"] != "Tg" {
		t.Fatal("looks")
	}
}

// The whole way: what the live connection writes, the importer reads into the archive.
func TestStoreThenImport(t *testing.T) {
	in := newInstance(t, M{})
	importTelegram, importReads = func(a *archive.Archive, out func(string), only map[[2]int64]bool, skip map[int64]bool) error {
		return importers.Telegram(a, out, importers.TelegramOptions{Only: only, Skip: skip})
	}, func(a *archive.Archive, chats map[int64]bool) error {
		return importers.TelegramReads(a, nil, "", chats)
	}
	c := in.ctx()
	self := user(1, "Me", 11)
	self.SetSelf(true)
	bob := user(2, "Bob", 22)
	bob.SetPhone("15551234567")
	m := text(5, &tg.PeerUser{UserID: 2}, 0, 1600000000, "hello", false)
	h := &tg.MessageReplyHeader{}
	h.SetReplyToMsgID(4)
	m.SetReplyTo(h)
	if _, err := storeMessages(c, self, []sent{{text(1, &tg.PeerUser{UserID: 1}, 1, 1599999999, "note", true), self}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeMessages(c, bob, []sent{{text(4, &tg.PeerUser{UserID: 2}, 0, 1599999999, "first", true), nil}, {m, bob}}); err != nil {
		t.Fatal(err)
	}
	var txt, sender string
	var reply *int64
	ok := db.Row(in.h.store.Read(), "SELECT m.text, a.value, m.reply_to FROM message m JOIN address a ON a.id = m.sender_id WHERE m.key = '5'",
		nil, &txt, &sender, &reply)
	if !ok || txt != "hello" || sender != "+15551234567" || reply == nil {
		t.Fatalf("%v %q %q %v", ok, txt, sender, reply)
	}
	if n := db.Int(in.h.store.Read(), "SELECT count(*) FROM message"); n != 3 {
		t.Fatalf("%d messages", n)
	}
}

// What Telegram pushes: a new message, and a service message (a call) that Telethon's NewMessage
// left out for good.
func TestLiveHandlers(t *testing.T) {
	in := newInstance(t, M{})
	c := in.ctx()
	cn := account().conn()
	d := tg.NewUpdateDispatcher()
	handlers(c, cn, d, func(int64) {})
	bob := user(2, "Bob", 22)
	call := &tg.MessageService{ID: 9, PeerID: &tg.PeerUser{UserID: 2}, Date: 1600000000,
		Action: &tg.MessageActionPhoneCall{CallID: 5}}
	call.SetFromID(&tg.PeerUser{UserID: 2})
	err := d.Handle(newTestCtx(), &tg.Updates{Users: []tg.UserClass{bob}, Updates: []tg.UpdateClass{
		&tg.UpdateNewMessage{Message: text(8, &tg.PeerUser{UserID: 2}, 0, 1600000000, "hi", false)},
		&tg.UpdateNewMessage{Message: call}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in.keys, []map[[2]int64]bool{{{2, 8}: true}, {{2, 9}: true}}) {
		t.Fatalf("%v", in.keys)
	}
	if got := c.LastLines(20); !strings.Contains(strings.Join(got, "\n"), "new message in Bob") {
		t.Fatalf("%q", got)
	}
}
