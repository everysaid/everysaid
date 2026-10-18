package whatsapp

import (
	"go.mau.fi/whatsmeow/types/events"

	"everysaid/internal/importers"
)

// handleUndecryptable keeps a trace of a message this device could not read, as WhatsApp's own
// apps show "waiting for this message": the phone is asked to send it again (whatsmeow's retry, and
// AutomaticMessageRerequestFromPhone), and when it comes the trace goes (importers.WhatsAppWriter);
// a day on, it is a notice of its own id (undecryptable:<id>), so that the message, should it still
// come, is not taken for it. A view-once message, which the phone never gives a linked device, is
// said as one at once.
func handleUndecryptable(store *Store, b *importers.WABatch, evt *events.UndecryptableMessage) {
	if evt.DecryptFailMode == events.DecryptFailHide || isChannel(evt.Info.Chat) {
		return // what WhatsApp itself would not show (a reaction, a poll's vote, …)
	}
	if evt.UnavailableType == events.UnavailableTypeViewOnce {
		b.Notices = append(b.Notices, importers.WANotice{Chat: evt.Info.Chat.String(), ID: "undecryptable:" + evt.Info.ID,
			Sender: evt.Info.Sender.ToNonAD().String(), FromMe: evt.Info.IsFromMe, At: evt.Info.Timestamp.UnixMilli(),
			Code: "view_once", Args: map[string]any{}})
		return
	}
	store.exec(`INSERT OR IGNORE INTO whatsapp_unreadable (store, chat_jid, id, sender, from_me, at) VALUES (?, ?, ?, ?, ?, ?)`,
		store.key, evt.Info.Chat.String(), evt.Info.ID, evt.Info.Sender.ToNonAD().String(), evt.Info.IsFromMe,
		evt.Info.Timestamp.UnixMilli())
}
