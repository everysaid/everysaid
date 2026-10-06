package main

// What the bridge keeps of each message beyond text and media: its kind, the message it answers,
// forwarding, a shared place, contact or poll; and, in tables of their own, reactions, calls and
// the edits and deletions of earlier messages.

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// StoredMessage is one row of the messages table.
type StoredMessage struct {
	ID, ChatJID, Sender, Content         string
	Timestamp                            time.Time
	IsFromMe                             bool
	MediaType, Filename, URL, DirectPath string
	MediaKey, FileSHA256, FileEncSHA256  []byte
	FileLength                           uint64
	Kind, Subtype, ReplyTo, ReplyText    string
	Mentions                             []string // the jids the text names with @<user>
	Forwarded                            bool
	Lat, Lon                             *float64
	Place                                string
}

// migrate adds what this version keeps to a store made by an older one. Nothing is removed.
func (store *MessageStore) migrate() error {
	columns := map[string]string{
		"kind":        "TEXT",    // text, image, video, audio, voice, document, sticker, location, contact, poll
		"subtype":     "TEXT",    // gif, video_note, link, live_location, view_once
		"reply_to":    "TEXT",    // the id of the message this one answers
		"reply_text":  "TEXT",    // the text it quotes
		"forwarded":   "BOOLEAN", // forwarded by the sender
		"edited":      "BOOLEAN", // edited later by its sender: content is the last version
		"deleted":     "BOOLEAN", // deleted later by its sender (or a group admin): content stays
		"lat":         "REAL",
		"lon":         "REAL",
		"place":       "TEXT",
		"direct_path": "TEXT",      // where the encrypted file is on WhatsApp's servers
		"media_path":  "TEXT",      // the downloaded file, relative to the store (media.go)
		"media_error": "TEXT",      // why it could not be downloaded
		"mentions":    "TEXT",      // the jids the text names with @<user>, comma-separated
		"read_at":     "TIMESTAMP", // when the account read it (a message from others), on any device
	}
	have := map[string]bool{}
	rows, err := store.db.Query("PRAGMA table_info(messages)")
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	for name, typ := range columns {
		if !have[name] {
			if _, err := store.db.Exec(fmt.Sprintf("ALTER TABLE messages ADD COLUMN %s %s", name, typ)); err != nil {
				return err
			}
		}
	}
	_, err = store.db.Exec(`
		CREATE TABLE IF NOT EXISTS reactions (
			chat_jid TEXT,
			message_id TEXT,          -- the message reacted to
			sender TEXT,              -- who reacted (a jid)
			is_from_me BOOLEAN,
			emoji TEXT,               -- '' once taken back
			timestamp TIMESTAMP,
			PRIMARY KEY (chat_jid, message_id, sender)
		);
		CREATE TABLE IF NOT EXISTS calls (
			id TEXT PRIMARY KEY,      -- the call id (from call events) or the call-log message id
			source TEXT,              -- 'log': the call-log message every device gets after a call; 'event': the call signalling
			chat_jid TEXT,            -- the person's or the group's chat
			creator TEXT,             -- who started it (events)
			is_from_me BOOLEAN,
			is_group BOOLEAN,
			video BOOLEAN,
			timestamp TIMESTAMP,
			outcome TEXT,             -- log: CONNECTED, MISSED, FAILED, REJECTED, ACCEPTED_ELSEWHERE, ...
			duration INTEGER,         -- seconds (log)
			accepted_at TIMESTAMP,    -- events
			ended_at TIMESTAMP,       -- events
			end_reason TEXT           -- events: the terminate reason, or 'reject'
		);
		CREATE TABLE IF NOT EXISTS call_participants (
			call_id TEXT,
			jid TEXT,
			outcome TEXT,
			PRIMARY KEY (call_id, jid)
		);
	`)
	return err
}

// storeChatTime records a chat, keeping its newest message time (history arrives newest first).
func (store *MessageStore) storeChatTime(jid, name string, t time.Time) error {
	var old sql.NullTime
	err := store.db.QueryRow("SELECT last_message_time FROM chats WHERE jid = ?", jid).Scan(&old)
	if err == nil && old.Valid && old.Time.After(t) {
		t = old.Time
	}
	return store.StoreChat(jid, name, t)
}

// storeMessage writes a message; a message seen again (history after live) keeps its file name,
// and its text once edited.
func (store *MessageStore) storeMessage(m StoredMessage) error {
	_, err := store.db.Exec(`
		INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url,
			media_key, file_sha256, file_enc_sha256, file_length, kind, subtype, reply_to, reply_text, forwarded,
			lat, lon, place, direct_path, mentions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id, chat_jid) DO UPDATE SET
			sender = excluded.sender,
			content = CASE WHEN messages.edited THEN messages.content ELSE excluded.content END,
			timestamp = excluded.timestamp, is_from_me = excluded.is_from_me, media_type = excluded.media_type,
			filename = coalesce(nullif(messages.filename, ''), excluded.filename),
			url = excluded.url, media_key = excluded.media_key, file_sha256 = excluded.file_sha256,
			file_enc_sha256 = excluded.file_enc_sha256, file_length = excluded.file_length,
			kind = excluded.kind, subtype = excluded.subtype, reply_to = excluded.reply_to,
			reply_text = excluded.reply_text, forwarded = excluded.forwarded,
			lat = excluded.lat, lon = excluded.lon, place = excluded.place,
			direct_path = coalesce(excluded.direct_path, messages.direct_path),
			mentions = CASE WHEN messages.edited THEN messages.mentions ELSE excluded.mentions END`,
		m.ID, m.ChatJID, m.Sender, m.Content, m.Timestamp, m.IsFromMe, m.MediaType, m.Filename, m.URL,
		m.MediaKey, m.FileSHA256, m.FileEncSHA256, m.FileLength, m.Kind, m.Subtype, m.ReplyTo, m.ReplyText,
		m.Forwarded, m.Lat, m.Lon, m.Place, nullable(m.DirectPath), nullable(strings.Join(m.Mentions, ",")))
	return err
}

// storeReaction keeps each person's latest reaction to a message (an empty emoji once taken back).
func (store *MessageStore) storeReaction(chatJID, messageID, sender string, fromMe bool, emoji string, t time.Time) error {
	_, err := store.db.Exec(`
		INSERT INTO reactions (chat_jid, message_id, sender, is_from_me, emoji, timestamp) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (chat_jid, message_id, sender) DO UPDATE SET emoji = excluded.emoji, timestamp = excluded.timestamp
		WHERE excluded.timestamp >= reactions.timestamp`,
		chatJID, messageID, sender, fromMe, emoji, t)
	return err
}

func messageText(msg *waE2E.Message) string {
	switch {
	case msg == nil:
		return ""
	case msg.GetConversation() != "":
		return msg.GetConversation()
	case msg.GetExtendedTextMessage() != nil:
		return msg.GetExtendedTextMessage().GetText()
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetCaption()
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetCaption()
	case msg.GetPtvMessage() != nil:
		return msg.GetPtvMessage().GetCaption()
	case msg.GetDocumentMessage() != nil:
		return msg.GetDocumentMessage().GetCaption()
	}
	return ""
}

func contextInfo(msg *waE2E.Message) *waE2E.ContextInfo {
	for _, c := range []interface{ GetContextInfo() *waE2E.ContextInfo }{
		msg.GetExtendedTextMessage(), msg.GetImageMessage(), msg.GetVideoMessage(), msg.GetPtvMessage(),
		msg.GetAudioMessage(), msg.GetDocumentMessage(), msg.GetStickerMessage(), msg.GetLocationMessage(),
		msg.GetLiveLocationMessage(), msg.GetContactMessage(), msg.GetContactsArrayMessage(),
		msg.GetPollCreationMessage(), msg.GetPollCreationMessageV2(), msg.GetPollCreationMessageV3(),
	} {
		if ci := c.GetContextInfo(); ci != nil {
			return ci
		}
	}
	return nil
}

func vcardNumber(vcard string) string {
	for _, line := range strings.Split(vcard, "\n") {
		if strings.HasPrefix(strings.ToUpper(line), "TEL") {
			if i := strings.LastIndex(line, ":"); i >= 0 {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return ""
}

func contactText(c *waE2E.ContactMessage) string {
	parts := []string{}
	if c.GetDisplayName() != "" {
		parts = append(parts, c.GetDisplayName())
	}
	if n := vcardNumber(c.GetVcard()); n != "" {
		parts = append(parts, n)
	}
	return strings.Join(parts, ", ")
}

func pollText(name string, options []*waE2E.PollCreationMessage_Option) string {
	lines := []string{name}
	for _, o := range options {
		lines = append(lines, "• "+o.GetOptionName())
	}
	return strings.Join(lines, "\n")
}

// describe fills in what a message is; false for what is not a message of its own (reactions,
// edits, deletions, call logs, protocol and key messages).
func describe(m *StoredMessage, msg *waE2E.Message, evt *events.Message) bool {
	m.Content = messageText(msg)
	m.MediaType, m.Filename, m.URL, m.MediaKey, m.FileSHA256, m.FileEncSHA256, m.FileLength = extractMediaInfo(msg)
	for _, f := range []interface{ GetDirectPath() string }{msg.GetImageMessage(), msg.GetVideoMessage(),
		msg.GetPtvMessage(), msg.GetAudioMessage(), msg.GetDocumentMessage(), msg.GetStickerMessage()} {
		if p := f.GetDirectPath(); p != "" {
			m.DirectPath = p
		}
	}
	switch {
	case msg.GetConversation() != "":
		m.Kind = "text"
	case msg.GetExtendedTextMessage() != nil:
		m.Kind = "text"
		if msg.GetExtendedTextMessage().GetMatchedText() != "" {
			m.Subtype = "link"
		}
	case msg.GetImageMessage() != nil:
		m.Kind = "image"
	case msg.GetVideoMessage() != nil:
		m.Kind = "video"
		if msg.GetVideoMessage().GetGifPlayback() {
			m.Subtype = "gif"
		}
	case msg.GetPtvMessage() != nil:
		m.Kind, m.Subtype = "video", "video_note"
	case msg.GetAudioMessage() != nil:
		m.Kind = "audio"
		if msg.GetAudioMessage().GetPTT() {
			m.Kind = "voice"
		}
	case msg.GetDocumentMessage() != nil:
		m.Kind = "document"
	case msg.GetStickerMessage() != nil:
		m.Kind = "sticker"
	case msg.GetLocationMessage() != nil:
		l := msg.GetLocationMessage()
		lat, lon := l.GetDegreesLatitude(), l.GetDegreesLongitude()
		m.Kind, m.Lat, m.Lon = "location", &lat, &lon
		m.Place = strings.TrimSpace(strings.Join([]string{l.GetName(), l.GetAddress()}, "\n"))
		m.Content = l.GetComment()
	case msg.GetLiveLocationMessage() != nil:
		l := msg.GetLiveLocationMessage()
		lat, lon := l.GetDegreesLatitude(), l.GetDegreesLongitude()
		m.Kind, m.Subtype, m.Lat, m.Lon, m.Content = "location", "live_location", &lat, &lon, l.GetCaption()
	case msg.GetContactMessage() != nil:
		m.Kind, m.Content = "contact", contactText(msg.GetContactMessage())
	case msg.GetContactsArrayMessage() != nil:
		texts := []string{}
		for _, c := range msg.GetContactsArrayMessage().GetContacts() {
			texts = append(texts, contactText(c))
		}
		m.Kind, m.Content = "contact", strings.Join(texts, "\n")
	case msg.GetPollCreationMessage() != nil:
		p := msg.GetPollCreationMessage()
		m.Kind, m.Content = "poll", pollText(p.GetName(), p.GetOptions())
	case msg.GetPollCreationMessageV2() != nil:
		p := msg.GetPollCreationMessageV2()
		m.Kind, m.Content = "poll", pollText(p.GetName(), p.GetOptions())
	case msg.GetPollCreationMessageV3() != nil:
		p := msg.GetPollCreationMessageV3()
		m.Kind, m.Content = "poll", pollText(p.GetName(), p.GetOptions())
	default:
		return false
	}
	if m.Kind == "sticker" {
		m.MediaType = "sticker"
	}
	if evt != nil && evt.IsViewOnce && m.Subtype == "" {
		m.Subtype = "view_once"
	}
	if ci := contextInfo(msg); ci != nil {
		m.ReplyTo = ci.GetStanzaID()
		if m.ReplyTo != "" {
			m.ReplyText = messageText(ci.GetQuotedMessage())
		}
		m.Forwarded = ci.GetIsForwarded()
		m.Mentions = ci.GetMentionedJID()
	}
	return true
}

func nullable(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ownJID is the account's own jid, as reactions and calls name it.
func ownJID(client *whatsmeow.Client) string {
	if client.Store.ID == nil {
		return ""
	}
	return client.Store.ID.ToNonAD().String()
}

// processMessage stores a message, or applies what it does to an earlier one (an edit, a
// deletion, a reaction), or records the call it logs. name: the chat's name where already known.
func processMessage(client *whatsmeow.Client, store *MessageStore, evt *events.Message, name string, logger waLog.Logger) {
	msg := evt.Message
	if msg == nil {
		return
	}
	chatJID := evt.Info.Chat.String()
	sender := evt.Info.Sender.ToNonAD().String()

	if pm := msg.GetProtocolMessage(); pm != nil {
		target := pm.GetKey().GetID()
		switch pm.GetType() {
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			edited := pm.GetEditedMessage()
			var mentions []string
			if ci := contextInfo(edited); ci != nil {
				mentions = ci.GetMentionedJID()
			}
			if _, err := store.db.Exec("UPDATE messages SET content = ?, mentions = ?, edited = 1 WHERE id = ? AND chat_jid = ?",
				messageText(edited), nullable(strings.Join(mentions, ",")), target, chatJID); err != nil {
				logger.Warnf("Failed to store edit: %v", err)
			}
		case waE2E.ProtocolMessage_REVOKE:
			if _, err := store.db.Exec("UPDATE messages SET deleted = 1 WHERE id = ? AND chat_jid = ?",
				target, chatJID); err != nil {
				logger.Warnf("Failed to store deletion: %v", err)
			}
		}
		return
	}
	if rm := msg.GetReactionMessage(); rm != nil {
		t := evt.Info.Timestamp
		if ms := rm.GetSenderTimestampMS(); ms > 0 {
			t = time.UnixMilli(ms)
		}
		if err := store.storeReaction(chatJID, rm.GetKey().GetID(), sender, evt.Info.IsFromMe, rm.GetText(), t); err != nil {
			logger.Warnf("Failed to store reaction: %v", err)
		}
		return
	}
	if cl := msg.GetCallLogMesssage(); cl != nil {
		storeCallLog(store, evt, cl, logger)
		return
	}

	m := StoredMessage{ID: evt.Info.ID, ChatJID: chatJID, Sender: sender, Timestamp: evt.Info.Timestamp, IsFromMe: evt.Info.IsFromMe}
	if !describe(&m, msg, evt) {
		return
	}
	if name == "" {
		name = GetChatName(client, store, evt.Info.Chat, chatJID, nil, evt.Info.Sender.User, logger)
	}
	if err := store.storeChatTime(chatJID, name, evt.Info.Timestamp); err != nil {
		logger.Warnf("Failed to store chat: %v", err)
	}
	if err := store.storeMessage(m); err != nil {
		logger.Warnf("Failed to store message: %v", err)
		return
	}
	direction := "←"
	if m.IsFromMe {
		direction = "→"
	}
	fmt.Printf("[%s] %s %s: [%s]\n", m.Timestamp.Format("2006-01-02 15:04:05"), direction, evt.Info.Sender.User, m.Kind)
}

// historyReactions stores the reactions a history-sync message carries.
func historyReactions(client *whatsmeow.Client, store *MessageStore, chatJID types.JID, webMsg *waWeb.WebMessageInfo, logger waLog.Logger) {
	for _, r := range webMsg.GetReactions() {
		key := r.GetKey()
		sender := key.GetParticipant()
		switch {
		case key.GetFromMe():
			sender = ownJID(client)
		case sender == "":
			sender = chatJID.String()
		}
		if j, err := types.ParseJID(sender); err == nil {
			sender = j.ToNonAD().String()
		}
		t := time.UnixMilli(r.GetSenderTimestampMS())
		if err := store.storeReaction(chatJID.String(), webMsg.GetKey().GetID(), sender, key.GetFromMe(), r.GetText(), t); err != nil {
			logger.Warnf("Failed to store history reaction: %v", err)
		}
	}
}

func storeCallLog(store *MessageStore, evt *events.Message, cl *waE2E.CallLogMessage, logger waLog.Logger) {
	id := evt.Info.ID
	_, err := store.db.Exec(`
		INSERT OR REPLACE INTO calls (id, source, chat_jid, is_from_me, is_group, video, timestamp, outcome, duration)
		VALUES (?, 'log', ?, ?, ?, ?, ?, ?, ?)`,
		id, evt.Info.Chat.String(), evt.Info.IsFromMe, evt.Info.IsGroup, cl.GetIsVideo(), evt.Info.Timestamp,
		cl.GetCallOutcome().String(), cl.GetDurationSecs())
	if err != nil {
		logger.Warnf("Failed to store call log: %v", err)
		return
	}
	for _, p := range cl.GetParticipants() {
		store.db.Exec("INSERT OR REPLACE INTO call_participants (call_id, jid, outcome) VALUES (?, ?, ?)",
			id, p.GetJID(), p.GetCallOutcome().String())
	}
}

// handleCallEvent records the call signalling the bridge sees: offers (one-to-one, and notices of
// group calls), acceptance, rejection and the end.
func handleCallEvent(client *whatsmeow.Client, store *MessageStore, evt interface{}, logger waLog.Logger) {
	offer := func(meta types.BasicCallMeta, video bool) {
		chat, isGroup := meta.From.ToNonAD(), !meta.GroupJID.IsEmpty()
		if isGroup {
			chat = meta.GroupJID
		}
		creator := meta.CallCreator.ToNonAD().String()
		_, err := store.db.Exec(`
			INSERT OR IGNORE INTO calls (id, source, chat_jid, creator, is_from_me, is_group, video, timestamp)
			VALUES (?, 'event', ?, ?, ?, ?, ?, ?)`,
			meta.CallID, chat.String(), creator, creator == ownJID(client), isGroup, video, meta.Timestamp)
		if err != nil {
			logger.Warnf("Failed to store call: %v", err)
		}
	}
	switch v := evt.(type) {
	case *events.CallOffer:
		video := false
		if v.Data != nil {
			if o, ok := v.Data.GetOptionalChildByTag("offer"); ok {
				_, video = o.GetOptionalChildByTag("video")
			}
		}
		offer(v.BasicCallMeta, video)
	case *events.CallOfferNotice:
		offer(v.BasicCallMeta, v.Media == "video")
	case *events.CallAccept:
		store.db.Exec("UPDATE calls SET accepted_at = coalesce(accepted_at, ?) WHERE id = ?", v.Timestamp, v.CallID)
	case *events.CallReject:
		store.db.Exec("UPDATE calls SET ended_at = coalesce(ended_at, ?), end_reason = coalesce(end_reason, 'reject') WHERE id = ?",
			v.Timestamp, v.CallID)
	case *events.CallTerminate:
		store.db.Exec("UPDATE calls SET ended_at = coalesce(ended_at, ?), end_reason = coalesce(end_reason, ?) WHERE id = ?",
			v.Timestamp, v.Reason, v.CallID)
	}
}
