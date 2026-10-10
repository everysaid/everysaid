package meta

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/jsontime"

	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	tps "go.mau.fi/mautrix-meta/pkg/messagix/types"

	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

// fakeIG is a fake of instameow's client: an inbox the test makes, and the requests it is given.
type fakeIG struct {
	mu       sync.Mutex
	ck       *cookies.Cookies
	mailbox  *slidetypes.Mailbox
	threads  map[string]*slidetypes.ThreadInfo // by short id, for GetThread
	pages    map[string]*slidetypes.SlideMessages
	handler  func(context.Context, slidetypes.ClientEvent) error
	requests []any
	seq      int64
}

func (f *fakeIG) record(r any) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.mu.Unlock()
}

func (f *fakeIG) list() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]any(nil), f.requests...)
}

func (f *fakeIG) LoadIndex(ctx context.Context) (*tps.PolarisViewer, *slidetypes.Mailbox, error) {
	v := &tps.PolarisViewer{ID: "igme"}
	v.Data.FullName, v.Data.Username = "Me Owner", "me.owner"
	return v, f.mailbox, nil
}
func (f *fakeIG) GetOwnFBID() int64 { return me }
func (f *fakeIG) Connect(ctx context.Context) {
	f.event(ctx, &slidetypes.Connected{})
	<-ctx.Done()
}
func (f *fakeIG) event(ctx context.Context, e slidetypes.ClientEvent) error {
	f.mu.Lock()
	h := f.handler
	f.mu.Unlock()
	return h(ctx, e)
}
func (f *fakeIG) Disconnect() {}
func (f *fakeIG) SetEventHandler(h func(context.Context, slidetypes.ClientEvent) error) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
}
func (f *fakeIG) LoadState(json.RawMessage) error     { return nil }
func (f *fakeIG) DumpState() (json.RawMessage, error) { return json.RawMessage(`{"ig":1}`), nil }
func (f *fakeIG) SetSeqID(seq int64, ts time.Time)    { f.seq = seq }
func (f *fakeIG) GetCookies() *cookies.Cookies        { return f.ck }
func (f *fakeIG) FetchThreadID(context.Context, int64) (*instameow.ThreadIGIDs, error) {
	return &instameow.ThreadIGIDs{LongID: "long?", ShortID: "short?"}, nil
}
func (f *fakeIG) GetThread(ctx context.Context, req *slidetypes.GetThreadInfoRequest) (*slidetypes.ThreadInfoResponse, error) {
	f.record(req)
	return &slidetypes.ThreadInfoResponse{ThreadInfo: slidetypes.WrappedThreadInfo{AsIGDirectThread: f.threads[req.ThreadFBID]}}, nil
}
func (f *fakeIG) PaginateMessages(ctx context.Context, req *slidetypes.PaginateMessagesRequest) (*slidetypes.PaginateMessagesResponse, error) {
	f.record(req)
	out := &slidetypes.PaginateMessagesResponse{}
	cursor := ""
	if req.AfterCursor != nil {
		cursor = *req.AfterCursor
	}
	out.ThreadInfo.AsIGDirectThread = &slidetypes.MessagesOnlyThread{ID: req.ThreadID, Messages: f.pages[cursor]}
	return out, nil
}
func (f *fakeIG) PaginateMailbox(ctx context.Context, req *slidetypes.PaginateMailboxRequest) (*slidetypes.PaginateMailboxResponse, error) {
	f.record(req)
	return &slidetypes.PaginateMailboxResponse{Mailbox: &slidetypes.MailboxPage{}}, nil
}
func (f *fakeIG) SendMessage(ctx context.Context, req *slidetypes.SendTextRequest) (*slidetypes.SendTextResponse, error) {
	f.record(req)
	return &slidetypes.SendTextResponse{Message: slidetypes.SentMessage{ID: "ig.sent",
		TimestampMS: jsontime.UnixMilliString{Time: time.UnixMilli(1600000100000)}}}, nil
}
func (f *fakeIG) SendMedia(ctx context.Context, req *slidetypes.SendMediaRequest) (*slidetypes.SendMediaResponse, error) {
	f.record(req)
	return &slidetypes.SendMediaResponse{Message: slidetypes.SentMessage{ID: "ig.media",
		TimestampMS: jsontime.UnixMilliString{Time: time.UnixMilli(1600000200000)}}}, nil
}
func (f *fakeIG) EditMessage(ctx context.Context, req *slidetypes.EditMessageRequest) (*slidetypes.EditMessageResponse, error) {
	f.record(req)
	return &slidetypes.EditMessageResponse{}, nil
}
func (f *fakeIG) UnsendMessage(ctx context.Context, req *slidetypes.UnsendMessageRequest) (*slidetypes.UnsendMessageResponse, error) {
	f.record(req)
	return &slidetypes.UnsendMessageResponse{}, nil
}
func (f *fakeIG) SendReaction(ctx context.Context, req *slidetypes.CreateReactionRequest) (*slidetypes.SendReactionResponse, error) {
	f.record(req)
	return &slidetypes.SendReactionResponse{}, nil
}
func (f *fakeIG) MarkRead(ctx context.Context, req *slidetypes.MarkReadRequest) (*slidetypes.MarkReadResponse, error) {
	f.record(req)
	return &slidetypes.MarkReadResponse{}, nil
}
func (f *fakeIG) MarkReadValidation(ctx context.Context, req *slidetypes.MarkReadRequest) (*slidetypes.MarkReadValidationResponse, error) {
	return &slidetypes.MarkReadValidationResponse{}, nil
}
func (f *fakeIG) Upload(ctx context.Context, thread int64, name, mime string, data []byte) (int64, error) {
	return 555, nil
}

func useFakeIG(f *fakeIG) func() {
	old := newIGClient
	newIGClient = func(c *cookies.Cookies, log zerolog.Logger) igClient {
		f.ck = c
		return f
	}
	return func() { newIGClient = old }
}

func ms(t int64) jsontime.UnixMilliString { return jsontime.UnixMilliString{Time: time.UnixMilli(t)} }

func igText(id string, sender int64, at int64, text string) *slidetypes.Message {
	return &slidetypes.Message{ID: id, SenderFBID: sender, TimestampMS: ms(at), TextBody: text,
		Content: slidetypes.MessageContentWrapper{TypeName: "SlideMessageText", Content: &slidetypes.MessageContentText{TextBody: text}}}
}

func TestLiveInstagram(t *testing.T) {
	in := newInstance(t, "instagram", M{"history": true})
	ck, _ := json.Marshal(map[string]string{"sessionid": "s", "csrftoken": "c", "ds_user_id": "9"})
	if err := saveSession(in.iid, &session{Platform: "instagram", UserID: "100", Name: "Me Owner", Cookies: ck}); err != nil {
		t.Fatal(err)
	}
	srv := cdn(t)
	pic := igText("ig.2", john, 1600000001000, "")
	pic.Content = slidetypes.MessageContentWrapper{TypeName: "SlideMessageImageContent",
		Content: &slidetypes.MessageContentImage{Attachments: []*slidetypes.Attachment{{AttachmentCDNURL: srv.URL + "/p.jpg"}}}}
	pic.Reactions = []*slidetypes.Reaction{{Reaction: "❤", SenderFBID: me, LogMessageID: "L0"}}
	thread := &slidetypes.ThreadInfo{ID: "short1", ThreadID: "long1", ThreadKey: 4000,
		Users: []*slidetypes.User{{InteropMessagingUserFBID: john, FullName: "John Doe", Username: "john.doe"}},
		SlideMessages: &slidetypes.SlideMessages{Edges: []slidetypes.Node[*slidetypes.Message]{
			{Node: pic}, {Node: igText("ig.1", john, 1600000000000, "hello")}},
			PageInfo: slidetypes.PageInfo{EndCursor: "c1", HasNextPage: true}}}
	f := &fakeIG{mailbox: &slidetypes.Mailbox{UQSeqID: 50, ThreadsByFolder: slidetypes.Edged[slidetypes.Node[slidetypes.WrappedThreadInfo]]{
		Edges: []slidetypes.Node[slidetypes.WrappedThreadInfo]{{Node: slidetypes.WrappedThreadInfo{AsIGDirectThread: thread}}}}},
		pages: map[string]*slidetypes.SlideMessages{"": thread.SlideMessages, "c1": {Edges: []slidetypes.Node[*slidetypes.Message]{
			{Node: igText("ig.0", me, 1599999999000, "older")}}}},
		threads: map[string]*slidetypes.ThreadInfo{"short2": {ID: "short2", ThreadID: "long2", ThreadKey: 5000, IsGroup: true,
			ThreadTitle: "IG group", SlideMessages: &slidetypes.SlideMessages{}, Users: []*slidetypes.User{{InteropMessagingUserFBID: ann, FullName: "Ann Roe"}}}}}
	defer useFakeIG(f)()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Plugin{"instagram"}.Live(ctx, in.ctx()) }()
	q := in.q()
	eventually(t, "the inbox and its history", func() bool { return db.Int(q, "SELECT count(*) FROM message") == 3 })
	if n := db.Int(q, "SELECT count(*) FROM address a JOIN address_kind k ON k.id = a.kind_id WHERE k.name = 'username' AND a.value = 'john.doe'"); n != 1 {
		t.Errorf("John's user name: %d", n)
	}
	eventually(t, "the picture", func() bool { return db.Int(q, "SELECT count(*) FROM attachment") == 1 })
	if n := db.Int(q, "SELECT count(*) FROM reaction WHERE outgoing = 1"); n != 1 {
		t.Errorf("the owner's reaction: %d", n)
	}
	eventually(t, "connected", func() bool { s := running(in.iid); return s != nil && s.connected.Load() })
	ev := func(d *slidetypes.Delta) {
		if err := f.event(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	// what changes: a message, a reaction taken back by its id alone, an edit, an unsend, a receipt
	ev(&slidetypes.Delta{ThreadIGID: "short1", Data: &slidetypes.NewMessageEvent{Message: igText("ig.3", john, 1600000002000, "new")}})
	ev(&slidetypes.Delta{ThreadIGID: "short1", Data: &slidetypes.DeleteReactionEvent{MessageID: "x",
		Reaction: slidetypes.Reaction{LogMessageID: "L0"}}})
	ev(&slidetypes.Delta{ThreadIGID: "short1", Data: &slidetypes.EditMessageEvent{MessageID: "ig.1", TextBody: "hello!"}})
	ev(&slidetypes.Delta{ThreadIGID: "short1", Data: &slidetypes.DeleteMessageEvent{MessageID: "ig.3"}})
	ev(&slidetypes.Delta{ThreadIGID: "short1", Data: &slidetypes.ReadReceiptEvent{ReadReceipt: slidetypes.ReadReceipt{
		ParticipantFBID: john, WatermarkTimestampMS: ms(1600000000000)}}})
	// a thread not known yet: its information asked for
	ev(&slidetypes.Delta{ThreadIGID: "short2", Data: &slidetypes.NewMessageEvent{Message: igText("ig.g1", ann, 1600000003000, "hi group")}})
	if err := f.event(ctx, &slidetypes.SeqIDUpdate{SeqID: 77, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if db.Int(q, "SELECT count(*) FROM reaction WHERE outgoing = 1") != 0 {
		t.Error("the reaction taken back stayed")
	}
	if db.Str(q, "SELECT text FROM message WHERE key = 'ig.1' AND edited") != "hello!" || db.Int(q, "SELECT deleted FROM message WHERE key = 'ig.3'") != 1 {
		t.Error("the edit or the unsend")
	}
	if db.Int(q, "SELECT count(*) FROM receipt r JOIN message m ON m.id = r.message_id WHERE m.key = 'ig.0'") != 1 {
		t.Error("John's receipt of the owner's message")
	}
	if db.Str(q, "SELECT title FROM conversation WHERE key = '5000'") != "IG group" || db.Int(q, "SELECT count(*) FROM message WHERE key = 'ig.g1'") != 1 {
		t.Error("the thread not known before")
	}
	if sess, _ := loadSession(in.iid); sess.Seq != 77 {
		t.Errorf("the sequence number kept: %d", sess.Seq)
	}
	// sending, a reaction, an edit, an unsend
	c := in.ctx()
	conv := plugins.Conversation{ID: db.Int(q, "SELECT id FROM conversation WHERE key = '4000'"), Key: "4000", Service: "instagram"}
	got, err := Plugin{"instagram"}.Send(ctx, c, conv, "hey", &plugins.Reply{Key: "ig.1"}, nil, nil)
	if err != nil || !reflect.DeepEqual(got, plugins.Sent{Keys: []string{"ig.sent"}}) {
		t.Fatalf("sent: %v %v", got, err)
	}
	ref := plugins.Ref{ID: db.Int(q, "SELECT id FROM message WHERE key = 'ig.sent'"), Key: "ig.sent", Outgoing: true}
	for _, err := range []error{(Plugin{"instagram"}).React(ctx, c, conv, ref, "👍"), (Plugin{"instagram"}).React(ctx, c, conv, ref, ""),
		(Plugin{"instagram"}).Edit(ctx, c, conv, ref, "hey!"), (Plugin{"instagram"}).Delete(ctx, c, conv, ref)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	for _, r := range f.list() {
		switch x := r.(type) {
		case *slidetypes.SendTextRequest:
			if x.IGThreadIGID == nil || *x.IGThreadIGID != "short1" || x.ReplyToMessageID == nil || *x.ReplyToMessageID != "ig.1" {
				t.Errorf("the text sent: %+v", x)
			}
			seen = append(seen, "text")
		case *slidetypes.CreateReactionRequest:
			seen = append(seen, string(x.Input.ReactionStatus)+":"+x.Input.Emoji)
		case *slidetypes.EditMessageRequest:
			seen = append(seen, "edit:"+x.Body.Value)
		case *slidetypes.UnsendMessageRequest:
			seen = append(seen, "unsend:"+x.SendData.ThreadID)
		case *slidetypes.PaginateMessagesRequest:
			c := ""
			if x.AfterCursor != nil {
				c = *x.AfterCursor
			}
			seen = append(seen, "history:"+c)
		case *slidetypes.GetThreadInfoRequest:
			seen = append(seen, "thread:"+x.ThreadFBID)
		}
	}
	want := []string{"history:", "history:c1", "thread:short2", "text", "created:👍", "deleted:👍", "edit:hey!", "unsend:long1"}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("requests: %v", seen)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
