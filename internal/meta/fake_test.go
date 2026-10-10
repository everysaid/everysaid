package meta

// A fake of messagix's client, in this process: it gives the tables a test makes, and records the
// tasks it is given. Nothing reaches Meta.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

type fakeClient struct {
	mu        sync.Mutex
	ck        *cookies.Cookies
	user      *table.LSVerifyContactRowExists
	initial   *table.LSTable
	loadErr   error
	handler   messagix.EventHandler
	tasks     []socket.Task
	uploads   []string
	connected bool
	state     json.RawMessage
	// reply is what a task is answered with (nil: an empty table)
	reply func(t socket.Task) *table.LSTable
}

func (f *fakeClient) LoadMessagesPage(ctx context.Context) (types.UserInfo, *table.LSTable, error) {
	if f.loadErr != nil {
		return nil, nil, f.loadErr
	}
	return f.user, f.initial, nil
}

func (f *fakeClient) Connect(ctx context.Context) error {
	f.mu.Lock()
	f.connected = true
	h := f.handler
	f.mu.Unlock()
	if h != nil {
		go h(ctx, &messagix.ConnectedEvent{})
	}
	return nil
}

func (f *fakeClient) Disconnect() {
	f.mu.Lock()
	f.connected = false
	f.mu.Unlock()
}

func (f *fakeClient) SetEventHandler(h messagix.EventHandler) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
}

// event is a table arriving, as from Meta.
func (f *fakeClient) event(tbl *table.LSTable) {
	f.mu.Lock()
	h := f.handler
	f.mu.Unlock()
	h(context.Background(), tbl)
}

func (f *fakeClient) ExecuteTasks(ctx context.Context, tasks ...socket.Task) (*table.LSTable, error) {
	f.mu.Lock()
	f.tasks = append(f.tasks, tasks...)
	reply := f.reply
	f.mu.Unlock()
	if reply != nil && len(tasks) > 0 {
		return reply(tasks[0]), nil
	}
	return &table.LSTable{}, nil
}

func (f *fakeClient) taskList() []socket.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]socket.Task(nil), f.tasks...)
}

func (f *fakeClient) WaitUntilCanSendMessages(ctx context.Context, timeout time.Duration) error {
	return nil
}
func (f *fakeClient) DumpState() (json.RawMessage, error) { return json.RawMessage(`{"fake":1}`), nil }
func (f *fakeClient) LoadState(state json.RawMessage) error {
	f.state = state
	return errors.New("the fake loads the page")
}
func (f *fakeClient) GetCookies() *cookies.Cookies             { return f.ck }
func (f *fakeClient) PostHandlePublishResponse(*table.LSTable) {}
func (f *fakeClient) GetCursor(int64) string                   { return "cursor" }
func (f *fakeClient) FetchMoreThreads(ctx context.Context, syncGroup int64) (*socket.KeyStoreData, *table.LSTable, error) {
	return nil, nil, nil
}
func (f *fakeClient) Upload(ctx context.Context, thread int64, name, mime string, data []byte) (int64, error) {
	f.mu.Lock()
	f.uploads = append(f.uploads, name)
	f.mu.Unlock()
	return 777, nil
}
func (f *fakeClient) E2EE(ctx context.Context, dev *store.Device, fbid int64, register bool) (*whatsmeow.Client, error) {
	return nil, errors.New("the fake has no encrypted chats")
}

// useFake puts a fake in the place of messagix for one test.
func useFake(f *fakeClient) func() {
	old := newClient
	newClient = func(c *cookies.Cookies, log zerolog.Logger) client {
		f.ck = c
		return f
	}
	return func() { newClient = old }
}

// replyOptimistic answers a message sent with the id Meta gives it.
func replyOptimistic(id string) func(t socket.Task) *table.LSTable {
	return func(t socket.Task) *table.LSTable {
		if s, ok := t.(*socket.SendMessageTask); ok {
			return &table.LSTable{LSReplaceOptimsiticMessage: []*table.LSReplaceOptimsiticMessage{
				{OfflineThreadingId: strconv.FormatInt(s.Otid, 10), MessageId: id}}}
		}
		return &table.LSTable{}
	}
}
