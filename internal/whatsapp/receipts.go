package whatsapp

// Ports bridges/whatsapp/receipts.go (the REST handler /api/read is now Bridge.MarkRead).

// Receipts: who got and who read (or played) the account's messages, and when, as WhatsApp tells
// every device of the account (receipts); when the account itself read a message, on any device
// (messages.read_at). The bridge sends read receipts of its own only when asked (Sender.markRead):
// the app does so only where the user turned it on.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// How far back a read receipt goes: what is older stays as it was (read on the phone, or not).
const readBack = 7 * 24 * time.Hour

func (store *MessageStore) migrateReceipts() error {
	_, err := store.db.Exec(`
		CREATE TABLE IF NOT EXISTS receipts (
			chat_jid TEXT,
			message_id TEXT,          -- one of the account's messages
			jid TEXT,                 -- who got it, read it or played it
			type TEXT,                -- delivered, read, played
			timestamp TIMESTAMP,      -- when (NULL: only that it was, from history)
			PRIMARY KEY (chat_jid, message_id, jid, type)
		);
	`)
	return err
}

func (store *MessageStore) storeReceipt(chatJID, messageID, jid, kind string, t interface{}) {
	store.db.Exec(`INSERT INTO receipts (chat_jid, message_id, jid, type, timestamp) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT DO UPDATE SET timestamp = coalesce(receipts.timestamp, excluded.timestamp)`,
		chatJID, messageID, jid, kind, t)
}

// markedRead records that the account read these messages (on any device); the first time stays.
func (store *MessageStore) markedRead(ids []string, at time.Time) {
	for _, id := range ids {
		store.db.Exec("UPDATE messages SET read_at = ? WHERE id = ? AND NOT is_from_me AND read_at IS NULL", at, id)
	}
}

func handleReceipt(store *MessageStore, v *events.Receipt, logger waLog.Logger) {
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
		store.markedRead(v.MessageIDs, v.Timestamp)
		return
	default:
		return
	}
	who := v.Sender.ToNonAD().String()
	for _, id := range v.MessageIDs {
		store.storeReceipt(v.Chat.String(), id, who, kind, v.Timestamp)
	}
}

// historyReceipts stores what a history-sync message says of who got and read it: per person in a
// group (with times), only its state in a person's chat.
func historyReceipts(store *MessageStore, chat types.JID, webMsg *waWeb.WebMessageInfo) {
	if !webMsg.GetKey().GetFromMe() {
		return
	}
	id := webMsg.GetKey().GetID()
	unix := func(s int64) interface{} {
		if s <= 0 {
			return nil
		}
		return time.Unix(s, 0)
	}
	for _, r := range webMsg.GetUserReceipt() {
		who := r.GetUserJID()
		if j, err := types.ParseJID(who); err == nil {
			who = j.ToNonAD().String()
		}
		for kind, s := range map[string]int64{"delivered": r.GetReceiptTimestamp(), "read": r.GetReadTimestamp(), "played": r.GetPlayedTimestamp()} {
			if s > 0 {
				store.storeReceipt(chat.String(), id, who, kind, unix(s))
			}
		}
	}
	if chat.Server == types.GroupServer || len(webMsg.GetUserReceipt()) > 0 {
		return
	}
	switch webMsg.GetStatus() {
	case waWeb.WebMessageInfo_PLAYED:
		store.storeReceipt(chat.String(), id, chat.String(), "played", nil)
		fallthrough
	case waWeb.WebMessageInfo_READ:
		store.storeReceipt(chat.String(), id, chat.String(), "read", nil)
		fallthrough
	case waWeb.WebMessageInfo_DELIVERY_ACK:
		store.storeReceipt(chat.String(), id, chat.String(), "delivered", nil)
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
	for _, b := range batches {
		if err := s.client.MarkRead(context.Background(), b.ids, now, b.chat, b.from, kind); err != nil {
			return fail(http.StatusInternalServerError, "marking read failed: %v", err)
		}
		s.store.markedRead(b.ids, now)
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
func (s *Sender) unread(chat types.JID, until time.Time) (map[string][]string, error) {
	rows, err := s.store.db.Query(`SELECT id, coalesce(sender, '') FROM messages WHERE chat_jid = ? AND NOT is_from_me
		AND read_at IS NULL AND timestamp <= ? AND timestamp > ? ORDER BY timestamp`,
		chat.String(), until, time.Now().Add(-readBack))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, sender string
		if rows.Scan(&id, &sender) == nil {
			out[sender] = append(out[sender], id)
		}
	}
	return out, nil
}
