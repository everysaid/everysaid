// The connection to Telegram and what the Python asked of Telethon: TelegramClient with its
// session and flood_sleep_threshold, iter_dialogs, iter_messages(min_id, reverse=True),
// get_messages(limit=0, filter) counts, get_messages(ids), get_input_entity from what was seen.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/db"
	"everysaid/internal/errs"
)

// floodWait sleeps through a FLOOD_WAIT up to threshold and tries again, as Telethon's
// flood_sleep_threshold does; a longer one is the error. A call made with withThreshold uses its
// own (the sync's, on the live connection).
func floodWait(threshold time.Duration) telegram.Middleware {
	return telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			limit := threshold
			if v, ok := ctx.Value(thresholdKey{}).(time.Duration); ok {
				limit = v
			}
			for {
				err := next.Invoke(ctx, input, output)
				d, ok := tgerr.AsFloodWait(err)
				if !ok || d > limit {
					return err
				}
				select {
				case <-time.After(d + time.Second):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	})
}

type thresholdKey struct{}

// withThreshold is ctx with the longest FLOOD_WAIT its calls sleep through.
func withThreshold(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, thresholdKey{}, d)
}

// gate keeps a process to one client with the account's key: two at once (the live connection and
// an import's sync, or a send while neither runs) would be two sessions of one key, each saving
// the session over the other's. A client open offers its connection to the others, which use it
// instead of opening their own; only the live connection, which needs its own updates, and a login
// wait for the open one to end. Across processes the lock file (lock.go) does the same.
type gate struct {
	mu      sync.Mutex
	busy    bool          // a client is open
	shared  *conn         // its connection, while others may use it
	users   int           // how many are using it
	changed chan struct{} // closed at every change of the three
}

var one = &gate{changed: make(chan struct{})}

func (g *gate) notify() { // with mu held
	close(g.changed)
	g.changed = make(chan struct{})
}

// take is the connection open now (share: if it may be used), or else the right to open one; done
// gives back either. When another process has the session: errHeld, or, given waiting (called
// once), it waits for it to end.
func (g *gate) take(ctx context.Context, share bool, waiting func()) (cn *conn, done func(), err error) {
	for {
		g.mu.Lock()
		if share && g.shared != nil {
			cn := g.shared
			g.users++
			g.mu.Unlock()
			return cn, func() {
				g.mu.Lock()
				g.users--
				g.notify()
				g.mu.Unlock()
			}, nil
		}
		var retry <-chan time.Time
		if !g.busy {
			f, err := lockSession()
			if err == nil {
				g.busy = true
				g.mu.Unlock()
				return nil, func() {
					g.mu.Lock()
					unlockSession(f)
					g.busy = false
					g.notify()
					g.mu.Unlock()
				}, nil
			}
			if !errors.Is(err, errHeld) || waiting == nil {
				g.mu.Unlock()
				return nil, nil, err
			}
			waiting()
			waiting = func() {}
			retry = time.After(5 * time.Second)
		}
		ch := g.changed
		g.mu.Unlock()
		select {
		case <-ch:
		case <-retry:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// borrow is the connection open now, if there is one, and its giving back.
func (g *gate) borrow() (*conn, func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.shared == nil {
		return nil, nil
	}
	g.users++
	return g.shared, func() {
		g.mu.Lock()
		g.users--
		g.notify()
		g.mu.Unlock()
	}
}

// share offers cn to the others until stop, which waits (a minute at most) for those using it.
func (g *gate) share(cn *conn) (stop func()) {
	g.mu.Lock()
	g.shared = cn
	g.notify()
	g.mu.Unlock()
	return func() {
		deadline := time.After(time.Minute)
		g.mu.Lock()
		g.shared = nil
		g.notify()
		for g.users > 0 {
			ch := g.changed
			g.mu.Unlock()
			select {
			case <-ch:
			case <-deadline:
				return
			}
			g.mu.Lock()
		}
		g.mu.Unlock()
	}
}

// noCredentials: no API id and hash saved.
const noCredentials = "no credentials: run with --save-credentials first"

// newClient is a gotd client with the saved credentials and session. handler: updates (nil: none).
// tweak: more options (the live connection's).
func newClient(threshold time.Duration, store *keyringSession, handler telegram.UpdateHandler, tweak ...func(*telegram.Options)) (*telegram.Client, error) {
	id, hash, ok := credentials()
	if !ok {
		return nil, errors.New(noCredentials)
	}
	opts := telegram.Options{SessionStorage: store, Middlewares: []telegram.Middleware{floodWait(threshold)},
		UpdateHandler: handler, NoUpdates: handler == nil, Device: device()}
	for _, f := range tweak {
		f(&opts)
	}
	return telegram.NewClient(id, hash, opts), nil
}

// conn is the API of a connected client and the users and chats it has seen (Telethon's entity
// cache: a StringSession keeps no access hashes, so they come from the dialogs read each time).
type conn struct {
	api *tg.Client

	mu       sync.Mutex
	entities map[int64]any  // marked id -> *tg.User, *tg.Chat, *tg.Channel, ...
	unknown  map[int64]bool // ids the dialogs were read again for, in vain: not again
}

func newConn(api *tg.Client) *conn {
	return &conn{api: api, entities: map[int64]any{}, unknown: map[int64]bool{}}
}

// seen keeps the users and chats of an answer; it returns them by marked id (those of this answer).
func (c *conn) seen(users []tg.UserClass, chats []tg.ChatClass) map[int64]any {
	out := map[int64]any{}
	for _, u := range users {
		if _, empty := u.(*tg.UserEmpty); !empty {
			out[EntityID(u)] = u
		}
	}
	for _, ch := range chats {
		if _, empty := ch.(*tg.ChatEmpty); !empty {
			out[EntityID(ch)] = ch
		}
	}
	c.mu.Lock()
	for id, e := range out {
		if _, ok := InputPeer(e); ok { // a "min" one does not replace what can be addressed
			c.entities[id] = e
		} else if _, had := c.entities[id]; !had {
			c.entities[id] = e
		}
	}
	c.mu.Unlock()
	return out
}

// entity is a user or chat seen, by marked id.
func (c *conn) entity(id int64) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entities[id]
}

// inputPeer is get_input_entity of a marked id: from what was seen.
func (c *conn) inputPeer(id int64) (tg.InputPeerClass, error) {
	if p, ok := InputPeer(c.entity(id)); ok {
		return p, nil
	}
	return nil, fmt.Errorf("could not find the input entity for %d", id)
}

// peer is inputPeer, else as telegram.db keeps the chat (a chat left or deleted is no longer among
// the dialogs), else reading the dialogs again for a chat not met yet (new since they were read, or a
// connection shared before it read them): once for each id, as reading them all is many requests.
func (c *conn) peer(ctx context.Context, id int64) (tg.InputPeerClass, error) {
	if p, err := c.inputPeer(id); err == nil {
		return p, nil
	}
	if p, ok := storedPeer(id); ok {
		return p, nil
	}
	c.mu.Lock()
	tried := c.unknown[id]
	c.mu.Unlock()
	if !tried {
		if _, err := c.dialogs(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.unknown[id] = true
		c.mu.Unlock()
	}
	return c.inputPeer(id)
}

// storedPeer is a chat or user as telegram.db keeps it, as an input peer.
func storedPeer(id int64) (p tg.InputPeerClass, ok bool) {
	if _, err := os.Stat(DBPath()); err != nil {
		return nil, false // no store yet
	}
	store, err := db.ReadOnly(DBPath())
	if err != nil {
		return nil, false
	}
	defer func() { // a store that cannot be read: not known
		if recover() != nil {
			p, ok = nil, false
		}
	}()
	defer store.Close()
	for _, q := range []string{"SELECT json FROM chat WHERE id = ?", "SELECT json FROM entity WHERE id = ?"} {
		raw := db.Str(store, q, id)
		if raw == "" {
			continue
		}
		if o, err := Load([]byte(raw)); err == nil && EntityID(o) == id {
			if p, ok := InputPeer(o); ok {
				return p, true
			}
		}
	}
	return nil, false
}

// --- dialogs ---------------------------------------------------------------------------------------

// dialog is Telethon's custom.Dialog, the parts used.
type dialog struct {
	ID       int64
	Entity   any
	Name     string
	Archived bool
	Pinned   bool
	Dialog   *tg.Dialog
	Message  tg.MessageClass // the last one, nil if none
	Date     int             // its date, 0 if none
}

const maxChunk = 100

// historyPause is the wait between a long history's pages (none in tests).
var historyPause = time.Second

type dialogKey struct {
	channel int64
	id      int
}

func messageKey(peer tg.PeerClass, id int) dialogKey {
	if ch, ok := peer.(*tg.PeerChannel); ok {
		return dialogKey{ch.ChannelID, id}
	}
	return dialogKey{0, id}
}

func messageDate(m tg.MessageClass) int {
	switch m := m.(type) {
	case *tg.Message:
		return m.Date
	case *tg.MessageService:
		return m.Date
	}
	return 0
}

func messagePeer(m tg.MessageClass) tg.PeerClass {
	switch m := m.(type) {
	case *tg.Message:
		return m.PeerID
	case *tg.MessageService:
		return m.PeerID
	case *tg.MessageEmpty:
		if p, ok := m.GetPeerID(); ok {
			return p
		}
	}
	return nil
}

// dialogs is iter_dialogs(): every dialog, archived ones too, in Telegram's order.
func (c *conn) dialogs(ctx context.Context) ([]dialog, error) {
	req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: maxChunk}
	seenIDs := map[int64]bool{}
	var out []dialog
	for {
		r, err := c.api.MessagesGetDialogs(ctx, req)
		if err != nil {
			return out, err
		}
		var (
			dialogs  []tg.DialogClass
			messages []tg.MessageClass
			users    []tg.UserClass
			chats    []tg.ChatClass
			slice    bool
		)
		switch r := r.(type) {
		case *tg.MessagesDialogs:
			dialogs, messages, users, chats = r.Dialogs, r.Messages, r.Users, r.Chats
		case *tg.MessagesDialogsSlice:
			dialogs, messages, users, chats, slice = r.Dialogs, r.Messages, r.Users, r.Chats, true
		default:
			return out, nil
		}
		entities := c.seen(users, chats)
		byKey := map[dialogKey]tg.MessageClass{}
		for _, m := range messages {
			if p := messagePeer(m); p != nil {
				byKey[messageKey(p, m.GetID())] = m
			}
		}
		var buffer []dialog
		for _, dc := range dialogs {
			d, ok := dc.(*tg.Dialog)
			if !ok {
				continue // a folder
			}
			pid := PeerID(d.Peer)
			if seenIDs[pid] {
				continue
			}
			seenIDs[pid] = true
			e, ok := entities[pid]
			if !ok {
				continue // a UserEmpty, rarely
			}
			m := byKey[messageKey(d.Peer, d.TopMessage)]
			_, archived := d.GetFolderID()
			buffer = append(buffer, dialog{ID: EntityID(e), Entity: e, Name: DisplayName(e), Archived: archived,
				Pinned: d.Pinned, Dialog: d, Message: m, Date: messageDate(m)})
		}
		out = append(out, buffer...)
		if len(buffer) == 0 || len(dialogs) < req.Limit || !slice {
			return out, nil
		}
		var last tg.MessageClass
		for i := len(dialogs) - 1; i >= 0 && last == nil; i-- {
			if d, ok := dialogs[i].(*tg.Dialog); ok {
				last = byKey[messageKey(d.Peer, d.TopMessage)]
			}
		}
		req.ExcludePinned = true
		req.OffsetID, req.OffsetDate = 0, 0
		if last != nil {
			req.OffsetID, req.OffsetDate = last.GetID(), messageDate(last)
		}
		p, err := dialogPeer(buffer[len(buffer)-1])
		if err != nil {
			p = &tg.InputPeerEmpty{}
		}
		req.OffsetPeer = p
	}
}

// --- messages --------------------------------------------------------------------------------------

// senderID is Telethon's Message.sender_id: from_id, else the chat for a channel's post or an
// incoming private message.
func senderID(m tg.MessageClass) int64 {
	var from, peer tg.PeerClass
	var post, out bool
	switch m := m.(type) {
	case *tg.Message:
		from, _ = m.GetFromID()
		peer, post, out = m.PeerID, m.Post, m.Out
	case *tg.MessageService:
		from, _ = m.GetFromID()
		peer, post, out = m.PeerID, m.Post, m.Out
	default:
		return 0
	}
	if from != nil {
		return PeerID(from)
	}
	if _, user := peer.(*tg.PeerUser); peer != nil && (post || (!out && user)) {
		return PeerID(peer)
	}
	return 0
}

// sent is a message with its sender's entity, if the answer had it (Telethon's m.sender).
type sent struct {
	Message tg.MessageClass
	Sender  any
}

func messagesOf(r tg.MessagesMessagesClass) (msgs []tg.MessageClass, users []tg.UserClass, chats []tg.ChatClass, count int, slice bool) {
	switch r := r.(type) {
	case *tg.MessagesMessages:
		return r.Messages, r.Users, r.Chats, len(r.Messages), false
	case *tg.MessagesMessagesSlice:
		return r.Messages, r.Users, r.Chats, r.Count, true
	case *tg.MessagesChannelMessages:
		return r.Messages, r.Users, r.Chats, r.Count, true
	case *tg.MessagesMessagesNotModified:
		return nil, nil, nil, r.Count, false
	}
	return nil, nil, nil, 0, false
}

// history is iter_messages(peer, min_id=minID, reverse=True): the messages after minID, oldest
// first, given to fn a chunk at a time.
func (c *conn) history(ctx context.Context, peer tg.InputPeerClass, minID int, fn func([]sent) error) error {
	offset := minID
	if offset > 0 {
		offset++
	} else {
		offset = 1
	}
	req := &tg.MessagesGetHistoryRequest{Peer: peer, Limit: maxChunk, OffsetID: offset, AddOffset: -maxChunk}
	last := 0
	for page := 0; ; page++ {
		if page >= 30 { // a long history: a second between pages, as Telethon (fewer flood waits)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(historyPause):
			}
		}
		r, err := c.api.MessagesGetHistory(ctx, req)
		if err != nil {
			return err
		}
		msgs, users, chats, _, slice := messagesOf(r)
		entities := c.seen(users, chats)
		var buffer []sent
		stop := false
		for i := len(msgs) - 1; i >= 0; i-- {
			m := msgs[i]
			if _, empty := m.(*tg.MessageEmpty); empty {
				continue
			}
			if m.GetID() <= last || m.GetID() >= math.MaxInt32 {
				stop = true
				break
			}
			last = m.GetID()
			buffer = append(buffer, sent{m, entities[senderID(m)]})
		}
		if len(buffer) > 0 {
			if err := fn(buffer); err != nil {
				return err
			}
		}
		if stop || !slice || len(msgs) == 0 || len(buffer) == 0 {
			return nil
		}
		lastMsg := buffer[len(buffer)-1].Message
		req.OffsetID = lastMsg.GetID() + 1
		req.OffsetDate = messageDate(lastMsg)
	}
}

// first is get_messages(peer, limit=1, reverse=True): the oldest message, nil if none.
func (c *conn) first(ctx context.Context, peer tg.InputPeerClass) (tg.MessageClass, error) {
	r, err := c.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 1, OffsetID: 1, AddOffset: -1})
	if err != nil {
		return nil, err
	}
	msgs, users, chats, _, _ := messagesOf(r)
	c.seen(users, chats)
	for i := len(msgs) - 1; i >= 0; i-- {
		if _, empty := msgs[i].(*tg.MessageEmpty); !empty {
			return msgs[i], nil
		}
	}
	return nil, nil
}

// count is get_messages(peer, limit=0, filter=…).total: how many messages, of a kind if filter.
func (c *conn) count(ctx context.Context, peer tg.InputPeerClass, filter tg.MessagesFilterClass) (int, error) {
	var r tg.MessagesMessagesClass
	var err error
	if filter == nil {
		r, err = c.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 1})
	} else {
		r, err = c.api.MessagesSearch(ctx, &tg.MessagesSearchRequest{Peer: peer, Filter: filter, Limit: 0})
	}
	if err != nil {
		return 0, err
	}
	_, _, _, n, _ := messagesOf(r)
	return n, nil
}

// byIDs is get_messages(peer, ids=…): the messages in that order, nil where one is gone.
func (c *conn) byIDs(ctx context.Context, peer tg.InputPeerClass, ids []int) ([]tg.MessageClass, error) {
	input := make([]tg.InputMessageClass, len(ids))
	for i, id := range ids {
		input[i] = &tg.InputMessageID{ID: id}
	}
	var r tg.MessagesMessagesClass
	var err error
	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		r, err = c.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}, ID: input})
	} else {
		r, err = c.api.MessagesGetMessages(ctx, input)
	}
	if err != nil {
		return nil, err
	}
	msgs, users, chats, _, _ := messagesOf(r)
	c.seen(users, chats)
	byID := map[int]tg.MessageClass{}
	for _, m := range msgs {
		if _, empty := m.(*tg.MessageEmpty); !empty {
			byID[m.GetID()] = m
		}
	}
	out := make([]tg.MessageClass, len(ids))
	for i, id := range ids {
		out[i] = byID[id]
	}
	return out, nil
}

// authorized says whether the session is signed in (Telethon's is_user_authorized).
func authorized(ctx context.Context, client *telegram.Client) (bool, error) {
	st, err := client.Auth().Status(ctx)
	if err != nil {
		return false, err
	}
	return st.Authorized, nil
}

// pluginErr is a plugin's failure the user is told about (English, translated on the way out).
func pluginErr(text string) error { return errs.Plugin(text, 0) }
