package whatsapp

// Not a port: the bridge had no way to react, edit or delete; this came with the app's actions on a
// message.

// The user's reaction to a message (put, changed, taken back), the edit of one of the user's own
// messages and its deletion for everyone, under the gates of sending (send.go): sending allowed, no
// block, a human pace. WhatsApp does not give a device back what it sent itself, so each is stored
// as it would have been had it come from the phone (processMessage), and the import brings it.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// How long after sending WhatsApp lets a message be edited, deleted for everyone, as the apps offer
// it (whatsmeow's EditWindow, 20 minutes, is the server's leeway beyond the apps' 15).
const (
	editWindow   = 15 * time.Minute
	deleteWindow = 48 * time.Hour
)

// ChangeRequest is what is done to a message already in a chat.
type ChangeRequest struct {
	Kind      string // react, edit, delete
	Recipient string // the chat, as for sending: a number's digits, or a jid
	ID        string // the message's id
	// for a message the bridge does not have (from before it was linked): whether it is the
	// account's own, and if not who wrote it (a jid or a number)
	FromMe bool
	Sender string
	Emoji  string // "" takes the account's reaction back
	Text   string // an edit's new text (a caption, for a file)
}

// Change does it (an HTTP status and the answer, as Send gives them).
func (b *Bridge) Change(req ChangeRequest) (int, SendResponse) {
	_, _, sender := b.parts()
	if sender == nil || !b.open() {
		return 503, SendResponse{Message: ErrNotConnected.Error()}
	}
	defer b.handling.RUnlock()
	return sender.change(req)
}

// connected: linked (the account's jid names what is stored) and connected, or a test's stand-in.
func (s *Sender) connected() bool {
	return s.client != nil && s.client.Store.ID != nil && (s.transmit != nil || (s.client.IsConnected() && s.client.IsLoggedIn()))
}

// target is the message's chat, who wrote it and whether it is the account's own: as the bridge
// keeps it, or as the request says for one it does not have (known false).
func (s *Sender) target(req ChangeRequest) (chat, author types.JID, fromMe, known bool, err error) {
	chats, err := s.jidsOf(req.Recipient)
	if err != nil {
		return chat, author, false, false, err
	}
	for _, c := range chats {
		var sender sql.NullString
		var mine sql.NullBool
		if s.store.db.QueryRow("SELECT sender, is_from_me FROM messages WHERE id = ? AND chat_jid = ?", req.ID, c.String()).
			Scan(&sender, &mine) != nil {
			continue
		}
		chat, fromMe, known = c, mine.Bool, true
		if !fromMe && sender.String != "" {
			if author, err = types.ParseJID(sender.String); err != nil {
				return chat, author, false, false, fmt.Errorf("not a jid: %s", sender.String)
			}
		}
		break
	}
	if !known {
		chat, fromMe = chats[0], req.FromMe
		if !fromMe && req.Sender != "" {
			author = types.NewJID(strings.TrimPrefix(req.Sender, "+"), types.DefaultUserServer)
			if strings.Contains(req.Sender, "@") {
				if author, err = types.ParseJID(req.Sender); err != nil {
					return chat, author, false, false, fmt.Errorf("not a jid: %s", req.Sender)
				}
			}
		}
	}
	switch {
	case fromMe:
		author = types.EmptyJID
	case author.IsEmpty() && chat.Server == types.GroupServer:
		return chat, author, false, false, fmt.Errorf("who wrote the message is not known")
	case author.IsEmpty():
		author = chat // a person's chat: theirs
	}
	return chat, author.ToNonAD(), fromMe, known, nil
}

// edited is the new content of a message: its text, or its caption, keeping the people it names
// whose @ is still in the text.
func (s *Sender) edited(chat types.JID, id, text string) (*waE2E.Message, error) {
	var kind, mentions sql.NullString
	s.store.db.QueryRow("SELECT kind, mentions FROM messages WHERE id = ? AND chat_jid = ?", id, chat.String()).Scan(&kind, &mentions)
	var named []string
	for _, jid := range strings.Split(mentions.String, ",") {
		if j, err := types.ParseJID(jid); jid != "" && err == nil && containsToken(text, "@"+j.User) {
			named = append(named, jid)
		}
	}
	var ci *waE2E.ContextInfo
	if len(named) > 0 {
		ci = &waE2E.ContextInfo{MentionedJID: named}
	}
	switch kind.String {
	case "text", "":
		if ci == nil {
			return &waE2E.Message{Conversation: proto.String(text)}, nil
		}
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text), ContextInfo: ci}}, nil
	case "image":
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String(text), ContextInfo: ci}}, nil
	case "video":
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String(text), ContextInfo: ci}}, nil
	case "document":
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: proto.String(text), ContextInfo: ci}}, nil
	}
	return nil, fmt.Errorf("a message of this kind has no text to edit")
}

func (s *Sender) change(req ChangeRequest) (int, SendResponse) {
	fail := func(code int, format string, args ...interface{}) (int, SendResponse) {
		return code, SendResponse{Success: false, Message: fmt.Sprintf(format, args...)}
	}
	if !s.enabled {
		return fail(http.StatusForbidden, "sending is off: [whatsapp] send = true in config.toml turns it on")
	}
	if req.Recipient == "" || req.ID == "" {
		return fail(http.StatusBadRequest, "recipient and a message id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if blocked, _ := s.store.state("send_blocked"); blocked != "" {
		return fail(http.StatusLocked, "sending is blocked (%s): allow it again once it is safe", blocked)
	}
	if !s.connected() {
		return fail(http.StatusServiceUnavailable, "not connected to WhatsApp")
	}
	chat, author, fromMe, known, err := s.target(req)
	if err != nil {
		return fail(http.StatusBadRequest, "%v", err)
	}
	if req.Kind == "edit" || req.Kind == "delete" {
		// what the bridge does not have it could not store as changed, nor tell whose it is
		if !known {
			return fail(http.StatusNotFound, "the bridge does not have this message")
		}
		if !fromMe {
			return fail(http.StatusForbidden, "only the account's own messages can be edited or deleted")
		}
	}
	if over := s.overLimit(); over != "" {
		return fail(http.StatusTooManyRequests, "%s", over)
	}
	var msg *waE2E.Message
	switch req.Kind {
	case "react":
		msg = s.client.BuildReaction(chat, author, req.ID, req.Emoji)
	case "edit":
		text := strings.TrimSpace(req.Text)
		if text == "" {
			return fail(http.StatusBadRequest, "the new text is empty")
		}
		content, err := s.edited(chat, req.ID, text)
		if err != nil {
			return fail(http.StatusBadRequest, "%v", err)
		}
		msg = s.client.BuildEdit(chat, req.ID, content)
	case "delete":
		msg = s.client.BuildRevoke(chat, types.EmptyJID, req.ID)
	default:
		return fail(http.StatusBadRequest, "unknown change: %s", req.Kind)
	}
	var resp whatsmeow.SendResponse
	if s.transmit != nil {
		resp, err = s.transmit(chat, msg)
	} else {
		resp, err = s.client.SendMessage(context.Background(), chat, msg)
	}
	if err != nil {
		return fail(http.StatusInternalServerError, "sending failed: %v", err)
	}
	// it is a message on the wire like any other: it counts for the pace, not as a text repeated
	s.store.db.Exec("INSERT INTO sent (at, chat_jid, id, text_hash) VALUES (?, ?, ?, NULL)", time.Now().Unix(), chat.String(), resp.ID)
	own := *s.client.Store.ID
	evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: own,
		IsFromMe: true, IsGroup: chat.Server == types.GroupServer}, ID: resp.ID, Timestamp: resp.Timestamp}, RawMessage: msg}
	processMessage(s.client, s.store, evt.UnwrapRaw(), "", s.logger)
	if req.Kind == "react" {
		// The account has one reaction on a message, whichever of its jids (number, LID) WhatsApp
		// named it by when it came from the phone: those rows say the same, or the import would
		// take whichever it read last.
		s.store.db.Exec("UPDATE reactions SET emoji = ?, timestamp = ? WHERE chat_jid = ? AND message_id = ? AND is_from_me AND sender != ?",
			req.Emoji, time.UnixMilli(msg.GetReactionMessage().GetSenderTimestampMS()), chat.String(), req.ID, own.ToNonAD().String())
	}
	return http.StatusOK, SendResponse{Success: true, Message: "sent", ID: resp.ID, ChatJID: chat.String(), Timestamp: resp.Timestamp}
}
