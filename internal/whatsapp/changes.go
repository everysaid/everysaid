package whatsapp

// Not a port: the bridge had no way to react, edit or delete; this came with the app's actions on a
// message.

// The user's reaction to a message (put, changed, taken back), the edit of one of the user's own
// messages and its deletion for everyone, under the gates of sending (send.go): sending allowed, no
// block, a human pace. WhatsApp does not give a device back what it sent itself, so each is
// written into the archive as it would have been had it come from the phone (processMessage).
// A deletion for the user alone (any message) is no message but a change of the account's app
// state, which the phone and the other devices follow; written as a deletion, as the archive keeps
// both alike.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"everysaid/internal/importers"
)

// How long after sending WhatsApp lets a message be edited, deleted for everyone, as the apps offer
// it (whatsmeow's EditWindow, 20 minutes, is the server's leeway beyond the apps' 15).
const (
	editWindow   = 15 * time.Minute
	deleteWindow = 48 * time.Hour
)

// ChangeRequest is what is done to a message already in a chat.
type ChangeRequest struct {
	Kind      string // react, edit, delete, delete_me
	Recipient string // the chat, as for sending: a number's digits, or a jid
	ID        string // the message's id
	// the message as the archive has it (Known), for one the bridge's record of the last week does
	// not name: whether it is the account's own, and if not who wrote it (a jid or a number); its
	// kind (text, image, video, document, ...) and whom it names (jids), for an edit
	Known       bool
	FromMe      bool
	Sender      string
	MessageKind string
	Mentions    []string
	Emoji       string // "" takes the account's reaction back
	Text        string // an edit's new text (a caption, for a file)
	TS          int64  // when it was sent (Unix ms), as the archive has it: for a deletion for the user alone
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

// target is the message's chat, who wrote it and whether it is the account's own: as the bridge's
// record of the last week has it, or as the request says (known false where neither has it).
func (s *Sender) target(req ChangeRequest) (chat, author types.JID, fromMe, known bool, err error) {
	chats, err := s.jidsOf(req.Recipient)
	if err != nil {
		return chat, author, false, false, err
	}
	for _, c := range chats {
		var sender sql.NullString
		var mine sql.NullBool
		if s.store.db.QueryRow("SELECT sender, from_me FROM whatsapp_recent WHERE store = ? AND id = ? AND chat_jid = ?",
			s.store.key, req.ID, c.String()).Scan(&sender, &mine) != nil {
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
		chat, fromMe, known = chats[0], req.FromMe, req.Known
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

// edited is the new content of a message of a kind: its text, or its caption, keeping the people it
// names whose @ is still in the text.
func edited(kind string, mentions []string, text string) (*waE2E.Message, error) {
	var named []string
	for _, jid := range mentions {
		if j, err := types.ParseJID(jid); jid != "" && err == nil && containsToken(text, "@"+j.User) {
			named = append(named, jid)
		}
	}
	var ci *waE2E.ContextInfo
	if len(named) > 0 {
		ci = &waE2E.ContextInfo{MentionedJID: named}
	}
	switch kind {
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
	if req.Kind == "delete_me" {
		return s.deleteForMe(chat, author, fromMe, known, req)
	}
	if req.Kind == "edit" || req.Kind == "delete" {
		// what neither the bridge nor the archive has cannot be told whose it is
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
		content, err := edited(req.MessageKind, req.Mentions, text)
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
	s.store.sent(chat.String(), resp.ID, "")
	own := *s.client.Store.ID
	evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: own,
		IsFromMe: true, IsGroup: chat.Server == types.GroupServer}, ID: resp.ID, Timestamp: resp.Timestamp}, RawMessage: msg}
	b := &importers.WABatch{}
	processMessage(s.client, s.store, b, evt.UnwrapRaw(), "", s.logger)
	if !s.store.Write(b, s.logger) {
		s.logger.Warnf("Sent, but not written into the archive")
	}
	return http.StatusOK, SendResponse{Success: true, Message: "sent", ID: resp.ID, ChatJID: chat.String(), Timestamp: resp.Timestamp}
}

// deleteForMe deletes a message for the account alone, as the phone's "Delete for me" does: in the
// app state, which the account's devices follow (its files too). A group's message of another names
// its author; any other, none ("0").
func (s *Sender) deleteForMe(chat, author types.JID, fromMe, known bool, req ChangeRequest) (int, SendResponse) {
	at := time.UnixMilli(req.TS)
	var stored int64
	if s.store.db.QueryRow("SELECT at FROM whatsapp_recent WHERE store = ? AND id = ? AND chat_jid = ?", s.store.key, req.ID,
		chat.String()).Scan(&stored) == nil {
		at = time.UnixMilli(stored)
	}
	own, participant := "0", "0"
	if fromMe {
		own = "1"
	} else if chat.Server == types.GroupServer {
		participant = author.String()
	}
	patch := appstate.PatchInfo{Type: appstate.WAPatchRegularHigh, Mutations: []appstate.MutationInfo{{
		Index:   []string{appstate.IndexDeleteMessageForMe, chat.String(), req.ID, own, participant},
		Version: 3,
		Value: &waSyncAction.SyncActionValue{DeleteMessageForMeAction: &waSyncAction.DeleteMessageForMeAction{
			DeleteMedia: proto.Bool(true), MessageTimestamp: proto.Int64(at.Unix())}},
	}}}
	var err error
	if s.patch != nil {
		err = s.patch(patch)
	} else {
		err = s.client.SendAppState(context.Background(), patch)
	}
	if err != nil {
		return http.StatusInternalServerError, SendResponse{Message: fmt.Sprintf("deleting failed: %v", err)}
	}
	b := &importers.WABatch{}
	deletedForMe(b, []string{chat.String()}, req.ID, ownJID(s.client), at)
	s.store.Write(b, s.logger)
	return http.StatusOK, SendResponse{Success: true, Message: "deleted", ChatJID: chat.String()}
}
