package whatsapp

// Receipts: who got and read the messages, and marking chats read (Bridge.MarkRead).

// Receipts: who got and who read (or played) the account's messages, and when, as WhatsApp tells
// every device of the account; when the account itself read a message, on any device (the chat's
// read state in the archive, and whatsapp_recent.read_at for what is still to mark). The bridge
// sends read receipts of its own only when asked (Sender.markRead): the app does so only where the
// user turned it on.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"everysaid/internal/db"
	"everysaid/internal/importers"
)

// How far back a read receipt goes: what is older stays as it was (read on the phone, or not).
const readBack = 7 * 24 * time.Hour

// markedRead records that the account read these messages (on any device): in the archive, and
// in whatsapp_recent (the first time stays), by their ids, as the receipt's chat may be the other
// jid (a LID for a number) of the one they came in.
func (store *Store) markedRead(b *importers.WABatch, chat string, ids []string, at time.Time) {
	for _, id := range ids {
		store.exec("UPDATE whatsapp_recent SET read_at = ? WHERE store = ? AND id = ? AND NOT from_me AND read_at IS NULL",
			at.UnixMilli(), store.key, id)
	}
	b.Reads = append(b.Reads, importers.WARead{Chat: chat, IDs: ids, At: at.UnixMilli()})
}

func handleReceipt(store *Store, b *importers.WABatch, v *events.Receipt) {
	if isChannel(v.Chat) {
		return
	}
	var kind string
	switch v.Type {
	case types.ReceiptTypeDelivered:
		kind = "delivered"
	case types.ReceiptTypeRead:
		kind = "read"
	case types.ReceiptTypePlayed:
		kind = "played"
	case types.ReceiptTypeReadSelf, types.ReceiptTypePlayedSelf:
		store.markedRead(b, v.Chat.ToNonAD().String(), v.MessageIDs, v.Timestamp)
		return
	default:
		return
	}
	who := v.Sender.ToNonAD().String()
	for _, id := range v.MessageIDs {
		b.Receipts = append(b.Receipts, importers.WAReceipt{Chat: v.Chat.String(), Target: id, Who: who, Kind: kind,
			At: v.Timestamp.UnixMilli()})
	}
}

// historyReceipts adds what a history-sync message says of who got and read it: per person in a
// group (with times), only its state in a person's chat.
func historyReceipts(b *importers.WABatch, chat types.JID, webMsg *waWeb.WebMessageInfo) {
	if !webMsg.GetKey().GetFromMe() {
		return
	}
	id := webMsg.GetKey().GetID()
	add := func(who, kind string, s int64) {
		b.Receipts = append(b.Receipts, importers.WAReceipt{Chat: chat.String(), Target: id, Who: who, Kind: kind, At: s * 1000})
	}
	for _, r := range webMsg.GetUserReceipt() {
		who := r.GetUserJID()
		if j, err := types.ParseJID(who); err == nil {
			who = j.ToNonAD().String()
		}
		for _, k := range []struct {
			kind string
			at   int64
		}{{"delivered", r.GetReceiptTimestamp()}, {"read", r.GetReadTimestamp()}, {"played", r.GetPlayedTimestamp()}} {
			if k.at > 0 {
				add(who, k.kind, k.at)
			}
		}
	}
	if chat.Server == types.GroupServer || len(webMsg.GetUserReceipt()) > 0 {
		return
	}
	switch webMsg.GetStatus() {
	case waWeb.WebMessageInfo_PLAYED:
		add(chat.String(), "played", 0)
		fallthrough
	case waWeb.WebMessageInfo_READ:
		add(chat.String(), "read", 0)
		fallthrough
	case waWeb.WebMessageInfo_DELIVERY_ACK:
		add(chat.String(), "delivered", 0)
	}
}

type ReadRequest struct {
	Recipient string `json:"recipient"` // the chat: a number's digits or a jid
	Until     int64  `json:"until"`     // Unix seconds: messages up to then (0: all)
	Self      bool   `json:"self"`      // read only on the account's own devices: the others are not told
}

// markRead sends read receipts for the messages of a chat not read yet, up to a time and at most
// readBack old, as the phone does when a chat is opened; with Self, receipts only the account's own
// devices get (read-self, as WhatsApp sends when its read receipts are off).
func (s *Sender) markRead(req ReadRequest) (int, map[string]interface{}) {
	fail := func(code int, format string, args ...interface{}) (int, map[string]interface{}) {
		return code, map[string]interface{}{"success": false, "message": fmt.Sprintf(format, args...)}
	}
	if req.Recipient == "" {
		return fail(http.StatusBadRequest, "recipient is required")
	}
	if !s.client.IsConnected() || !s.client.IsLoggedIn() {
		return fail(http.StatusServiceUnavailable, "not connected to WhatsApp")
	}
	batches, err := s.toMark(req.Recipient, until(req.Until))
	if err != nil {
		return fail(http.StatusNotFound, "%v", err)
	}
	now, marked := time.Now(), 0
	kind := types.ReceiptTypeRead
	if req.Self {
		kind = types.ReceiptTypeReadSelf
	}
	read := &importers.WABatch{}
	defer func() { s.store.Write(read, s.logger) }()
	for _, b := range batches {
		if err := s.client.MarkRead(context.Background(), b.ids, now, b.chat, b.from, kind); err != nil {
			return fail(http.StatusInternalServerError, "marking read failed: %v", err)
		}
		s.store.markedRead(read, b.chat.ToNonAD().String(), b.ids, now)
		marked += len(b.ids)
	}
	return http.StatusOK, map[string]interface{}{"success": true, "marked": marked}
}

// until is a read request's time: now, or the one given if earlier.
func until(unix int64) time.Time {
	now := time.Now()
	if unix > 0 && time.Unix(unix, 0).Before(now) {
		return time.Unix(unix, 0)
	}
	return now
}

// readBatch is what one read receipt says: messages of a chat, from one sender in a group.
type readBatch struct {
	chat, from types.JID
	ids        []string
}

// toMark is what to mark read for a recipient: in each of their chats (a number's and a LID's),
// the messages not read yet up to a time.
func (s *Sender) toMark(recipient string, until time.Time) ([]readBatch, error) {
	chats, err := s.chatsFor(recipient)
	if err != nil {
		return nil, err
	}
	var out []readBatch
	for _, chat := range chats {
		bySender, err := s.unread(chat, until)
		if err != nil {
			return nil, err
		}
		for sender, ids := range bySender {
			var from types.JID // a person's chat: no sender
			if chat.Server == types.GroupServer {
				if from, err = types.ParseJID(sender); err != nil {
					continue
				}
			}
			out = append(out, readBatch{chat, from, ids})
		}
	}
	return out, nil
}

// unread is the chat's messages from others not read yet, up to a time and readBack old, by sender.
func (s *Sender) unread(chat types.JID, until time.Time) (out map[string][]string, err error) {
	defer db.Recover(&err)
	out = map[string][]string{}
	db.Each(s.store.db, `SELECT id, coalesce(sender, '') FROM whatsapp_recent WHERE store = ? AND chat_jid = ? AND NOT from_me
		AND read_at IS NULL AND at <= ? AND at > ? ORDER BY at`,
		[]any{s.store.key, chat.String(), until.UnixMilli(), time.Now().Add(-readBack).UnixMilli()}, func(scan func(...any)) {
			var id, sender string
			scan(&id, &sender)
			out[sender] = append(out[sender], id)
		})
	return out, nil
}
