// The connection to Telegram and what the Python asked of Telethon: TelegramClient with its
// session and flood_sleep_threshold, iter_dialogs, iter_messages(min_id, reverse=True),
// get_messages(limit=0, filter) counts, get_messages(ids), get_input_entity from what was seen.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/errs"
)

// floodWait sleeps through a FLOOD_WAIT up to threshold and tries again, as Telethon's
// flood_sleep_threshold does; a longer one is the error.
func floodWait(threshold time.Duration) telegram.Middleware {
	return telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			for {
				err := next.Invoke(ctx, input, output)
				d, ok := tgerr.AsFloodWait(err)
				if !ok || d > threshold {
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

// noCredentials: no API id and hash saved.
const noCredentials = "no credentials: run with --save-credentials first"

// newClient is a gotd client with the saved credentials and session. handler: updates (nil: none).
func newClient(threshold time.Duration, store *keyringSession, handler telegram.UpdateHandler) (*telegram.Client, error) {
	id, hash, ok := credentials()
	if !ok {
		return nil, errors.New(noCredentials)
	}
	opts := telegram.Options{SessionStorage: store, Middlewares: []telegram.Middleware{floodWait(threshold)},
		UpdateHandler: handler, NoUpdates: handler == nil, Device: device()}
	return telegram.NewClient(id, hash, opts), nil
}

// conn is the API of a connected client and the users and chats it has seen (Telethon's entity
// cache: a StringSession keeps no access hashes, so they come from the dialogs read each time).
type conn struct {
	api *tg.Client

	mu       sync.Mutex
	entities map[int64]any // marked id -> *tg.User, *tg.Chat, *tg.Channel, ...
}

func newConn(api *tg.Client) *conn { return &conn{api: api, entities: map[int64]any{}} }

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
	for {
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
