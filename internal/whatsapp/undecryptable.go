package whatsapp

import (
	"encoding/json"

	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// handleUndecryptable keeps a trace of a message this device could not read, as WhatsApp's own
// apps show "waiting for this message": the phone is asked to send it again (whatsmeow's retry, and
// AutomaticMessageRerequestFromPhone), and when it comes the trace goes (processMessage); a view-once
// message, which the phone never gives a linked device, is said as one. Kept as a chat event of its
// own id (undecryptable:<id>), so that the message, when it comes, is not taken for it.
func handleUndecryptable(store *MessageStore, evt *events.UndecryptableMessage, logger waLog.Logger) {
	if evt.DecryptFailMode == events.DecryptFailHide || isChannel(evt.Info.Chat) {
		return // what WhatsApp itself would not show (a reaction, a poll's vote, …)
	}
	code := "unreadable"
	if evt.UnavailableType == events.UnavailableTypeViewOnce {
		code = "view_once"
	}
	js, _ := json.Marshal(map[string]any{})
	if _, err := store.db.Exec(`INSERT OR IGNORE INTO chat_events VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, evt.Info.Chat.String(),
		"undecryptable:"+evt.Info.ID, evt.Info.Sender.ToNonAD().String(), evt.Info.IsFromMe, evt.Info.Timestamp, code,
		string(js), ""); err != nil {
		logger.Warnf("Failed to keep a message that could not be read: %v", err)
	}
}

// readAfterAll drops the trace of a message that could not be read, once it came.
func (store *MessageStore) readAfterAll(chat, id string) {
	store.db.Exec("DELETE FROM chat_events WHERE chat_jid = ? AND id = ? AND code = 'unreadable'", chat, "undecryptable:"+id)
}
