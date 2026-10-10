package meta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "everysaid-meta-test")
	for k, v := range map[string]string{"EVERYSAID_DATA": "data", "EVERYSAID_CACHE": "cache",
		"EVERYSAID_CONFIG": "config", "EVERYSAID_STATE": "state"} {
		os.Setenv(k, filepath.Join(dir, v))
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test-meta")
	config.Load()
	historyPause = 10 * time.Millisecond
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type host struct {
	store  *core.Store
	mu     sync.Mutex
	events []M
}

func (h *host) Store() *core.Store   { return h.store }
func (h *host) Emit(e M)             { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() }
func (h *host) Alert(string, string) {}

type instance struct {
	h    *host
	iid  int64
	path string
}

func newInstance(t *testing.T, plugin string, settings M) *instance {
	path := filepath.Join(t.TempDir(), "archive.db")
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
	iid, err := plugins.Create(store, plugin, plugin, settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { forgetSession(iid) })
	return &instance{h: &host{store: store}, iid: iid, path: path}
}

func (in *instance) ctx() *plugins.Context {
	return plugins.NewContext(in.h, *plugins.GetInstance(in.h.store, in.iid))
}

func (in *instance) q() db.Querier { return in.h.store.Read() }

// eventually waits for a condition of the archive.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("not so: %s", what)
}

const (
	me   = int64(100)
	john = int64(200)
	ann  = int64(300)
	grp  = int64(9000)
)

func loggedInSession(t *testing.T, iid int64) {
	t.Helper()
	ck, _ := json.Marshal(map[string]string{"c_user": "100", "xs": "secret", "datr": "d"})
	if err := saveSession(iid, &session{Platform: "facebook", UserID: "100", Name: "Me Owner", Cookies: ck}); err != nil {
		t.Fatal(err)
	}
}

// initialTable is the page's first table: a chat with John (a message with a picture, a reaction
// on it), a group, John's name.
func initialTable(fileURL string) *table.LSTable {
	return &table.LSTable{
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: john, Name: "John Doe"}},
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
			{ThreadKey: john, ThreadType: table.ONE_TO_ONE, MuteExpireTimeMs: -1, LastReadWatermarkTimestampMs: 1600000001000},
			{ThreadKey: grp, ThreadType: table.GROUP_THREAD, ThreadName: "Friends"},
		},
		LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{
			{ThreadKey: grp, ContactId: me}, {ThreadKey: grp, ContactId: john}, {ThreadKey: grp, ContactId: ann}},
		LSInsertNewMessageRange: []*table.LSInsertNewMessageRange{{ThreadKey: john, MinTimestampMs: 1600000000000,
			MinMessageId: "mid.1", MaxTimestampMs: 1600000001000, HasMoreBefore: true}},
		LSUpsertMessage: []*table.LSUpsertMessage{
			{ThreadKey: john, MessageId: "mid.1", SenderId: john, TimestampMs: 1600000000000, Text: "look"},
			{ThreadKey: john, MessageId: "mid.2", SenderId: me, TimestampMs: 1600000001000, Text: "nice", ReplySourceId: "mid.1"},
		},
		LSInsertBlobAttachment: []*table.LSInsertBlobAttachment{{MessageId: "mid.1", ThreadKey: john,
			AttachmentType: table.AttachmentTypeImage, PreviewUrl: fileURL, PreviewUrlMimeType: "image/jpeg", AttachmentFbid: "a1"}},
		LSUpsertReaction: []*table.LSUpsertReaction{{ThreadKey: john, MessageId: "mid.1", ActorId: me, Reaction: "❤"}},
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: grp, MessageId: "mid.g1", SenderId: ann, TimestampMs: 1600000002000,
			Text: "hi @John", MentionIds: "200", MentionOffsets: "3", MentionLengths: "5", MentionTypes: "p"}},
	}
}

func cdn(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Fetch-Dest") == "" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte("jpeg bytes"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLiveMessenger(t *testing.T) {
	in := newInstance(t, "messenger", M{"history": true})
	loggedInSession(t, in.iid)
	srv := cdn(t)
	f := &fakeClient{user: &table.LSVerifyContactRowExists{ContactId: me, Name: "Me Owner"}, initial: initialTable(srv.URL + "/pic.jpg"),
		reply: replyOptimistic("mid.sent")}
	defer useFake(f)()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Plugin{"messenger"}.Live(ctx, in.ctx()) }()
	q := in.q()
	eventually(t, "the messages", func() bool { return db.Int(q, "SELECT count(*) FROM message") == 3 })
	// the chats and their people
	if title := db.Str(q, "SELECT title FROM conversation WHERE key = ?", "9000"); title != "Friends" {
		t.Errorf("the group: %q", title)
	}
	if n := db.Int(q, "SELECT count(*) FROM conversation_member cm JOIN conversation c ON c.id = cm.conversation_id WHERE c.key = '9000'"); n != 2 {
		t.Errorf("the group's members: %d", n)
	}
	if name := db.Str(q, "SELECT name FROM handle_name hn JOIN address a ON a.id = hn.address_id WHERE a.value = '200'"); name != "John Doe" {
		t.Errorf("John's name: %q", name)
	}
	if n := db.Int(q, "SELECT count(*) FROM account x JOIN address a ON a.id = x.address_id WHERE a.value = '100'"); n != 1 {
		t.Errorf("the owner's account: %d", n)
	}
	if n := db.Int(q, "SELECT count(*) FROM message WHERE key = 'mid.2' AND outgoing AND reply_to IS NOT NULL"); n != 1 {
		t.Errorf("the owner's reply: %d", n)
	}
	if n := db.Int(q, "SELECT count(*) FROM reaction WHERE emoji = '❤' AND outgoing = 1"); n != 1 {
		t.Errorf("the owner's reaction: %d", n)
	}
	if n := db.Int(q, "SELECT count(*) FROM mention m JOIN address a ON a.id = m.address_id WHERE a.value = '200' AND m.token = '@John'"); n != 1 {
		t.Errorf("the mention: %d", n)
	}
	if v := db.Int(q, "SELECT value FROM state_report WHERE field = 'muted'"); v != -1 {
		t.Errorf("muted: %d", v)
	}
	// the picture: downloaded, in the store, gone from the download folder
	eventually(t, "the picture", func() bool { return db.Int(q, "SELECT count(*) FROM attachment") == 1 })
	if entries, _ := os.ReadDir(mediaDir(in.iid)); len(entries) != 0 {
		t.Errorf("left in the download folder: %d", len(entries))
	}
	if files, _ := pendingFiles(in.iid, ""); len(files) != 0 {
		t.Errorf("still to download: %d", len(files))
	}
	// the earlier history of John's chat asked for
	eventually(t, "the history asked for", func() bool {
		for _, task := range f.taskList() {
			if fm, ok := task.(*socket.FetchMessagesTask); ok && fm.ThreadKey == john && fm.ReferenceMessageId == "mid.1" {
				return true
			}
		}
		return false
	})
	// what changes: an edit, John's reaction, an unsend, John read the owner's message
	f.event(&table.LSTable{
		LSEditMessage:       []*table.LSEditMessage{{MessageID: "mid.1", Text: "look!", EditCount: 1}},
		LSUpsertReaction:    []*table.LSUpsertReaction{{ThreadKey: john, MessageId: "mid.2", ActorId: john, Reaction: "😆"}},
		LSDeleteMessage:     []*table.LSDeleteMessage{{ThreadKey: grp, MessageId: "mid.g1"}},
		LSUpdateReadReceipt: []*table.LSUpdateReadReceipt{{ThreadKey: john, ContactId: john, ReadWatermarkTimestampMs: 1600000001000, ReadActionTimestampMs: 1600000005000}},
	})
	eventually(t, "the edit", func() bool { return db.Str(q, "SELECT text FROM message WHERE key = 'mid.1' AND edited") == "look!" })
	eventually(t, "the unsend", func() bool { return db.Int(q, "SELECT deleted FROM message WHERE key = 'mid.g1'") == 1 })
	eventually(t, "the receipt", func() bool {
		return db.Int(q, "SELECT read_at FROM receipt r JOIN message m ON m.id = r.message_id WHERE m.key = 'mid.2'") == 1600000005000
	})
	if n := db.Int(q, "SELECT count(*) FROM reaction r JOIN message m ON m.id = r.message_id WHERE m.key = 'mid.2' AND r.emoji = '😆'"); n != 1 {
		t.Errorf("John's reaction: %d", n)
	}
	// sending, with a file; a reaction, an edit, an unsend
	c := in.ctx()
	conv := plugins.Conversation{ID: db.Int(q, "SELECT id FROM conversation WHERE key = '200'"), Key: "200", Service: "messenger"}
	got, err := Plugin{"messenger"}.Send(ctx, c, conv, "a file", &plugins.Reply{Key: "mid.1"}, nil,
		&plugins.File{Data: []byte("%PDF"), Filename: "doc.pdf", MimeType: "application/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, plugins.Sent{Keys: []string{"mid.sent"}}) {
		t.Fatalf("sent: %v", got)
	}
	if n := db.Int(q, "SELECT count(*) FROM message m JOIN attachment a ON a.message_id = m.id WHERE m.key = 'mid.sent' AND m.outgoing"); n != 1 {
		t.Errorf("the message sent: %d", n)
	}
	var sent *socket.SendMessageTask
	for _, task := range f.taskList() {
		if s, ok := task.(*socket.SendMessageTask); ok {
			sent = s
		}
	}
	if sent == nil || sent.ThreadId != john || sent.Text != "a file" || sent.ReplyMetaData == nil ||
		sent.ReplyMetaData.ReplyMessageId != "mid.1" || !reflect.DeepEqual(sent.AttachmentFBIds, []int64{777}) {
		t.Fatalf("the task: %+v", sent)
	}
	ref := plugins.Ref{ID: db.Int(q, "SELECT id FROM message WHERE key = 'mid.sent'"), Key: "mid.sent", Outgoing: true}
	if err := (Plugin{"messenger"}).React(ctx, c, conv, ref, "👍"); err != nil {
		t.Fatal(err)
	}
	if err := (Plugin{"messenger"}).Edit(ctx, c, conv, ref, "a file!"); err != nil {
		t.Fatal(err)
	}
	if err := (Plugin{"messenger"}).Delete(ctx, c, conv, ref); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, task := range f.taskList() {
		switch x := task.(type) {
		case *socket.SendReactionTask:
			kinds = append(kinds, "react:"+x.Reaction)
		case *socket.EditMessageTask:
			kinds = append(kinds, "edit:"+x.Text)
		case *socket.DeleteMessageTask:
			kinds = append(kinds, "delete:"+x.MessageId)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"react:👍", "edit:a file!", "delete:mid.sent"}) {
		t.Errorf("tasks: %v", kinds)
	}
	if db.Int(q, "SELECT deleted FROM message WHERE key = 'mid.sent'") != 1 ||
		db.Str(q, "SELECT text FROM message WHERE key = 'mid.sent'") != "a file!" {
		t.Errorf("the changes not in the archive")
	}
	// read receipts only where the user allows
	if n, _ := (Plugin{"messenger"}).MarkRead(ctx, c, conv, 1600000002000); n != 0 {
		t.Errorf("marked read without the setting: %d", n)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// the session kept: the cookies, messagix's state
	sess, _ := loadSession(in.iid)
	if sess == nil || string(sess.State) != `{"fake":1}` || sess.Name != "Me Owner" {
		t.Errorf("the session: %+v", sess)
	}
}

func TestEncryptedMessagesIntoTheArchive(t *testing.T) {
	in := newInstance(t, "messenger", nil)
	loggedInSession(t, in.iid)
	s := &liveSession{c: in.ctx(), service: "messenger", self: "100", selfName: "Me Owner", names: map[string]importers.MetaUser{},
		threads: map[string]threadInfo{}, wake: make(chan struct{}, 1), history: make(chan rangeOp, 1),
		tr: newTranslator("100"), cli: &fakeClient{}, sess: &session{}}
	chat := types.NewJID("200", types.MessengerServer)
	at := time.UnixMilli(1600000000000)
	text := func(id, body string, quote string) *events.FBMessage {
		evt := &events.FBMessage{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat,
			Sender: types.NewJID("200", types.MessengerServer)}, ID: id, Timestamp: at},
			Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
				Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
					Content: &waConsumerApplication.ConsumerApplication_Content_MessageText{MessageText: &waCommon.MessageText{Text: proto.String(body)}}}}}},
			FBApplication: &waMsgApplication.MessageApplication{}}
		if quote != "" {
			evt.FBApplication.Metadata = &waMsgApplication.MessageApplication_Metadata{
				QuotedMessage: &waMsgApplication.MessageApplication_Metadata_QuotedMessage{StanzaID: proto.String(quote)}}
		}
		return evt
	}
	ctx := context.Background()
	for _, evt := range []any{text("W1", "secret hello", ""), text("W2", "and more", "W1")} {
		if err := s.handle(ctx, item{wa: evt}); err != nil {
			t.Fatal(err)
		}
	}
	q := in.q()
	if n := db.Int(q, "SELECT count(*) FROM message m JOIN conversation c ON c.id = m.conversation_id WHERE c.key = '200'"); n != 2 {
		t.Fatalf("messages: %d", n)
	}
	if n := db.Int(q, "SELECT count(*) FROM message WHERE key = 'W2' AND reply_to IS NOT NULL"); n != 1 {
		t.Errorf("the reply: %d", n)
	}
	if !s.encrypted("200") {
		t.Errorf("the chat is not known as encrypted")
	}
	// a reaction and a revoke from John
	react := &events.FBMessage{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat,
		Sender: types.NewJID("200", types.MessengerServer)}, ID: "W3", Timestamp: at},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content_ReactionMessage{ReactionMessage: &waConsumerApplication.ConsumerApplication_ReactionMessage{
					Key: &waCommon.MessageKey{ID: proto.String("W1")}, Text: proto.String("😮")}}}}}}}
	revoke := &events.FBMessage{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat,
		Sender: types.NewJID("200", types.MessengerServer)}, ID: "W4", Timestamp: at},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_ApplicationData{ApplicationData: &waConsumerApplication.ConsumerApplication_ApplicationData{
				ApplicationContent: &waConsumerApplication.ConsumerApplication_ApplicationData_Revoke{Revoke: &waConsumerApplication.ConsumerApplication_RevokeMessage{
					Key: &waCommon.MessageKey{ID: proto.String("W2")}}}}}}}}
	for _, evt := range []any{react, revoke} {
		if err := s.handle(ctx, item{wa: evt}); err != nil {
			t.Fatal(err)
		}
	}
	if n := db.Int(q, "SELECT count(*) FROM reaction r JOIN message m ON m.id = r.message_id WHERE m.key = 'W1' AND r.emoji = '😮'"); n != 1 {
		t.Errorf("the reaction: %d", n)
	}
	if db.Int(q, "SELECT deleted FROM message WHERE key = 'W2'") != 1 || db.Int(q, "SELECT count(*) FROM message") != 2 {
		t.Errorf("the revoke")
	}
}

func TestLoginWithCookies(t *testing.T) {
	in := newInstance(t, "messenger", nil)
	f := &fakeClient{user: &table.LSVerifyContactRowExists{ContactId: me, Name: "Me Owner"}}
	defer useFake(f)()
	c := in.ctx()
	if _, err := (Plugin{"messenger"}).login(context.Background(), c, "c_user=100; xs=1%3Aabc"); err == nil || !strings.Contains(err.Error(), "Missing cookies") {
		t.Fatalf("missing datr: %v", err)
	}
	sess, err := (Plugin{"messenger"}).login(context.Background(), c, `curl 'https://www.facebook.com/' \
  -H 'accept: */*' \
  -b 'datr=D; c_user=100; xs=1%3Aabc; fr=x'`)
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserID != "100" || sess.Name != "Me Owner" {
		t.Errorf("%+v", sess)
	}
	if ok, _ := (Plugin{"messenger"}).Check(c); !ok {
		t.Error("not ready once logged in")
	}
	// the cookies of another account
	f.user = &table.LSVerifyContactRowExists{ContactId: 999, Name: "Other"}
	if _, err := (Plugin{"messenger"}).login(context.Background(), c, `{"c_user":"999","xs":"a","datr":"b"}`); err == nil {
		t.Error("another account's cookies taken")
	}
	if err := (Plugin{"messenger"}).Action(c, "logout"); err != nil {
		t.Fatal(err)
	}
	if s, _ := loadSession(in.iid); s != nil {
		t.Error("the session stayed after logging out")
	}
}

func TestParseCookies(t *testing.T) {
	want := map[string]string{"c_user": "100", "xs": "1%3Aabc", "datr": "D"}
	for _, text := range []string{
		`{"c_user": "100", "xs": "1%3Aabc", "datr": "D"}`,
		`[{"name": "c_user", "value": "100"}, {"name": "xs", "value": "1%3Aabc"}, {"name": "datr", "value": "D"}]`,
		`c_user=100; xs=1%3Aabc; datr=D`,
		`Cookie: c_user=100; xs=1%3Aabc; datr=D`,
		`curl 'https://www.messenger.com/' -H 'Cookie: c_user=100; xs=1%3Aabc; datr=D' -H 'accept: */*'`,
		`curl "https://www.facebook.com/" \  -b "c_user=100; xs=1%3Aabc; datr=D"`, // its line end gone, pasted into one line
	} {
		got, err := parseCookies(text)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v %v", text, got, err)
		}
	}
	if _, err := parseCookies("nothing here"); err == nil {
		t.Error("cookies from nothing")
	}
}

func TestTranslateKeepsFoldersAndSpamOut(t *testing.T) {
	tr := newTranslator("100")
	o := tr.translate(&table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
		{ThreadKey: 1, ThreadType: table.ONE_TO_ONE, FolderName: "spam"},
		{ThreadKey: 2, ThreadType: table.FOLDER},
		{ThreadKey: 3, ThreadType: table.ONE_TO_ONE, FolderName: "inbox"},
	}, LSVerifyHybridThreadExists: []*table.LSVerifyHybridThreadExists{{ThreadKey: 50, ThreadJID: 60, ThreadType: table.ENCRYPTED_OVER_WA_GROUP}},
		LSSyncUpdateThreadName: []*table.LSSyncUpdateThreadName{{ThreadKey: 50, ThreadName: "Renamed"}}})
	if len(o.threads) != 1 || o.threads[0].Key != "3" {
		t.Errorf("threads: %+v", o.threads)
	}
	o2 := tr.translate(&table.LSTable{LSMoveThreadToE2EECutoverFolder: []*table.LSMoveThreadToE2EECutoverFolder{{ThreadKey: 3}}})
	if len(o2.threads) != 1 || o2.threads[0].Type != table.ENCRYPTED_OVER_WA_ONE_TO_ONE {
		t.Errorf("moved to the encrypted chats: %+v", o2.threads)
	}
	if len(o.titles) != 1 || o.titles[0].Thread != "60" {
		t.Errorf("an encrypted group's Facebook key not mapped: %+v", o.titles)
	}
}
