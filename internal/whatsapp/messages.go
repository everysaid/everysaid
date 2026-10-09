package whatsapp

// Ports bridges/whatsapp/messages.go (and extractMediaInfo of main.go).

// What the bridge keeps of each message beyond text and media: its kind, the message it answers,
// forwarding, a shared place, contact or poll; and, in tables of their own, reactions, calls and
// the edits and deletions of earlier messages.

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
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
	Poll                                 string // a poll's question, options, multiple (JSON)
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
		"poll":        "TEXT",      // a poll's question, options and whether several may be chosen (JSON)
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
		CREATE TABLE IF NOT EXISTS poll_votes (   -- each voter's newest vote in a poll
			chat_jid TEXT,
			poll_id TEXT,
			voter TEXT,
			options TEXT,             -- JSON: the SHA-256 (hex) of each option chosen, as WhatsApp names them
			timestamp TIMESTAMP,
			PRIMARY KEY (chat_jid, poll_id, voter)
		);
		CREATE TABLE IF NOT EXISTS chat_events (  -- a pin, a timer set: what a notice says (docs/design.md, "Notices")
			chat_jid TEXT,
			id TEXT,                  -- the message that said it
			sender TEXT,
			is_from_me BOOLEAN,
			timestamp TIMESTAMP,
			code TEXT,                -- pin, unpin, timer
			args TEXT,                -- JSON
			target TEXT,              -- the message it is about, if any
			PRIMARY KEY (chat_jid, id)
		);
	`)
	return err
}

// storeChatEvent keeps a pin or a timer set, as a notice says it.
func (store *MessageStore) storeChatEvent(evt *events.Message, code string, args map[string]any, target string) error {
	js, _ := json.Marshal(args)
	_, err := store.db.Exec(`INSERT OR IGNORE INTO chat_events VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, evt.Info.Chat.String(),
		evt.Info.ID, evt.Info.Sender.ToNonAD().String(), evt.Info.IsFromMe, evt.Info.Timestamp, code, string(js), target)
	return err
}

// joinLines joins the parts that say something, a line each.
func joinLines(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

// pollJSON is a poll as the store keeps it.
func pollJSON(name string, options []*waE2E.PollCreationMessage_Option, selectable uint32) string {
	names := []string{}
	for _, o := range options {
		names = append(names, o.GetOptionName())
	}
	js, _ := json.Marshal(map[string]any{"question": name, "options": names, "multiple": selectable != 1})
	return string(js)
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
			-- a new place for the file: worth trying again
			media_error = CASE WHEN excluded.direct_path IS NOT messages.direct_path AND excluded.direct_path IS NOT NULL
				THEN NULL ELSE messages.media_error END,
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
		if d := msg.GetDocumentMessage(); m.Content == "" { // a file without words: its name, as WhatsApp shows it
			if n := cmp.Or(d.GetFileName(), d.GetTitle()); n != "" {
				m.Content = "📎 " + n
			}
		}
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
		m.Poll = pollJSON(p.GetName(), p.GetOptions(), p.GetSelectableOptionsCount())
	case msg.GetPollCreationMessageV2() != nil:
		p := msg.GetPollCreationMessageV2()
		m.Kind, m.Content = "poll", pollText(p.GetName(), p.GetOptions())
		m.Poll = pollJSON(p.GetName(), p.GetOptions(), p.GetSelectableOptionsCount())
	case msg.GetPollCreationMessageV3() != nil, msg.GetPollCreationMessageV5() != nil, msg.GetPollCreationMessageV6() != nil:
		p := cmp.Or(msg.GetPollCreationMessageV3(), msg.GetPollCreationMessageV5(), msg.GetPollCreationMessageV6())
		m.Kind, m.Content = "poll", pollText(p.GetName(), p.GetOptions())
		m.Poll = pollJSON(p.GetName(), p.GetOptions(), p.GetSelectableOptionsCount())
	// what other kinds say, as text (mautrix-whatsapp's way): an event, an invitation to a group,
	// a business's message with its buttons, and the answers to those
	case msg.GetEventMessage() != nil:
		e := msg.GetEventMessage()
		m.Kind, m.Content = "text", joinLines(e.GetName(), e.GetDescription(), e.GetLocation().GetName())
	case msg.GetGroupInviteMessage() != nil:
		g := msg.GetGroupInviteMessage()
		m.Kind, m.Content = "text", joinLines(g.GetGroupName(), g.GetCaption())
	case msg.GetScheduledCallCreationMessage() != nil:
		m.Kind, m.Content = "text", msg.GetScheduledCallCreationMessage().GetTitle()
	case msg.GetButtonsMessage() != nil:
		b := msg.GetButtonsMessage()
		lines := []string{b.GetContentText(), b.GetFooterText()}
		for _, x := range b.GetButtons() {
			lines = append(lines, "["+x.GetButtonText().GetDisplayText()+"]")
		}
		m.Kind, m.Content = "text", joinLines(lines...)
	case msg.GetListMessage() != nil:
		l := msg.GetListMessage()
		m.Kind, m.Content = "text", joinLines(l.GetTitle(), l.GetDescription(), l.GetFooterText())
	case msg.GetTemplateMessage() != nil:
		h := msg.GetTemplateMessage().GetHydratedTemplate()
		m.Kind, m.Content = "text", joinLines(h.GetHydratedTitleText(), h.GetHydratedContentText(), h.GetHydratedFooterText())
	case msg.GetInteractiveMessage() != nil:
		i := msg.GetInteractiveMessage()
		m.Kind, m.Content = "text", joinLines(i.GetHeader().GetTitle(), i.GetBody().GetText(), i.GetFooter().GetText())
	case msg.GetButtonsResponseMessage() != nil:
		m.Kind, m.Content = "text", msg.GetButtonsResponseMessage().GetSelectedDisplayText()
	case msg.GetListResponseMessage() != nil:
		m.Kind, m.Content = "text", msg.GetListResponseMessage().GetTitle()
	case msg.GetTemplateButtonReplyMessage() != nil:
		m.Kind, m.Content = "text", msg.GetTemplateButtonReplyMessage().GetSelectedDisplayText()
	case msg.GetInteractiveResponseMessage() != nil:
		m.Kind, m.Content = "text", msg.GetInteractiveResponseMessage().GetBody().GetText()
	default:
		// a newer kind wrapped for the clients that know it: what it wraps
		for _, w := range []*waE2E.FutureProofMessage{msg.GetGroupMentionedMessage(), msg.GetLottieStickerMessage(),
			msg.GetPollCreationMessageV4(), msg.GetQuestionMessage(), msg.GetSpoilerMessage(), msg.GetAudioStickerMessage()} {
			if inner := w.GetMessage(); inner != nil {
				return describe(m, inner, evt)
			}
		}
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

// extractMediaInfo is what a message's file is, and the keys to download it.
func extractMediaInfo(msg *waE2E.Message) (mediaType string, filename string, url string, mediaKey []byte, fileSHA256 []byte, fileEncSHA256 []byte, fileLength uint64) {
	if msg == nil {
		return "", "", "", nil, nil, nil, 0
	}
	if img := msg.GetImageMessage(); img != nil {
		return "image", "image_" + time.Now().Format("20060102_150405") + ".jpg",
			img.GetURL(), img.GetMediaKey(), img.GetFileSHA256(), img.GetFileEncSHA256(), img.GetFileLength()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return "video", "video_" + time.Now().Format("20060102_150405") + ".mp4",
			vid.GetURL(), vid.GetMediaKey(), vid.GetFileSHA256(), vid.GetFileEncSHA256(), vid.GetFileLength()
	}
	if ptv := msg.GetPtvMessage(); ptv != nil { // a round video note
		return "video", "video_" + time.Now().Format("20060102_150405") + ".mp4",
			ptv.GetURL(), ptv.GetMediaKey(), ptv.GetFileSHA256(), ptv.GetFileEncSHA256(), ptv.GetFileLength()
	}
	if st := msg.GetStickerMessage(); st != nil {
		return "sticker", "sticker_" + time.Now().Format("20060102_150405") + ".webp",
			st.GetURL(), st.GetMediaKey(), st.GetFileSHA256(), st.GetFileEncSHA256(), st.GetFileLength()
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		return "audio", "audio_" + time.Now().Format("20060102_150405") + ".ogg",
			aud.GetURL(), aud.GetMediaKey(), aud.GetFileSHA256(), aud.GetFileEncSHA256(), aud.GetFileLength()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		filename := doc.GetFileName()
		if filename == "" {
			filename = "document_" + time.Now().Format("20060102_150405")
		}
		return "document", filename,
			doc.GetURL(), doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
	}
	return "", "", "", nil, nil, nil, 0
}

func nullable(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// isChannel: channels (newsletters) and status (everyone's, status@broadcast) are never kept: not
// wanted in the archive, and their files would be downloaded for nothing. A broadcast list of the
// account's own is not one: what was sent through it is kept.
func isChannel(jid types.JID) bool {
	return jid.Server == types.NewsletterServer || jid == types.StatusBroadcastJID
}

// channelsSQL is isChannel for a chat_jid column.
const channelsSQL = "(chat_jid LIKE '%@newsletter' OR chat_jid = 'status@broadcast')"

// ownJID is the account's own jid, as reactions and calls name it.
func ownJID(client *whatsmeow.Client) string {
	if client.Store.ID == nil {
		return ""
	}
	return client.Store.ID.ToNonAD().String()
}

// alternates are a person's jids: the one given, the other one the message names (alt), and what
// whatsmeow knows of the number of a LID or the LID of a number. One person's chat may be kept
// under either.
func alternates(client *whatsmeow.Client, jid, alt types.JID) []string {
	out := []string{}
	add := func(j types.JID) {
		if s := jidString(j); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	add(jid)
	add(alt)
	if client != nil {
		ctx := context.Background()
		switch jid.Server {
		case types.DefaultUserServer:
			if lid, err := client.Store.LIDs.GetLIDForPN(ctx, jid.ToNonAD()); err == nil {
				add(lid)
			}
		case types.HiddenUserServer:
			if pn, err := client.Store.LIDs.GetPNForLID(ctx, jid.ToNonAD()); err == nil {
				add(pn)
			}
		}
	}
	return out
}

// changeable is the chat of the earlier message an edit or a deletion names, and whether whoever
// sent it may change it, as WhatsApp allows: an edit only its author; a deletion its author, or in
// a group an admin. Anything else (another member forging one) is ignored, as the apps do.
func changeable(client *whatsmeow.Client, store *MessageStore, evt *events.Message, id string, deletion bool) (string, bool) {
	info := evt.Info
	group := info.Chat.Server == types.GroupServer
	chats := []string{info.Chat.String()}
	if !group {
		alt := info.SenderAlt
		if info.IsFromMe {
			alt = info.RecipientAlt
		}
		chats = alternates(client, info.Chat, alt)
	}
	for _, chat := range chats {
		var sender sql.NullString
		var fromMe sql.NullBool
		if store.db.QueryRow("SELECT sender, is_from_me FROM messages WHERE id = ? AND chat_jid = ?", id, chat).Scan(&sender, &fromMe) != nil {
			continue
		}
		var author bool
		switch {
		case fromMe.Bool || info.IsFromMe:
			author = fromMe.Bool == info.IsFromMe
		case sender.String == "":
			author = !group // a person's chat has one other side
		default:
			author = slices.Contains(alternates(client, info.Sender, info.SenderAlt), sender.String)
		}
		return chat, author || (deletion && group)
	}
	return "", false
}

// processMessage stores a message, or applies what it does to an earlier one (an edit, a
// deletion, a reaction), or records the call it logs. name: the chat's name where already known.
// false: it could not be stored (not acknowledged, WhatsApp gives it again).
func processMessage(client *whatsmeow.Client, store *MessageStore, evt *events.Message, name string, logger waLog.Logger) bool {
	msg := evt.Message
	if msg == nil || isChannel(evt.Info.Chat) {
		return true
	}
	// a reaction or an edit sent encrypted with the message's secret (newer clients): what it says
	if er := msg.GetEncReactionMessage(); er != nil {
		r, err := client.DecryptReaction(context.Background(), evt)
		if err != nil {
			logger.Warnf("Failed to read an encrypted reaction: %v", err)
			return true
		}
		r.Key = er.GetTargetMessageKey()
		msg = &waE2E.Message{ReactionMessage: r}
	}
	if msg.GetSecretEncryptedMessage() != nil {
		inner, err := client.DecryptSecretEncryptedMessage(context.Background(), evt)
		if err != nil {
			logger.Warnf("Failed to read a secret-encrypted message: %v", err)
			return true
		}
		e := *evt
		e.RawMessage, e.Message = inner, nil
		evt = e.UnwrapRaw()
		msg = evt.Message
		if msg == nil {
			return true
		}
	}
	chatJID := evt.Info.Chat.String()
	sender := evt.Info.Sender.ToNonAD().String()

	if pm := msg.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			edited := pm.GetEditedMessage()
			var mentions []string
			if ci := contextInfo(edited); ci != nil {
				mentions = ci.GetMentionedJID()
			}
			if chat, ok := changeable(client, store, evt, pm.GetKey().GetID(), false); ok {
				if _, err := store.db.Exec("UPDATE messages SET content = ?, mentions = ?, edited = 1 WHERE id = ? AND chat_jid = ?",
					messageText(edited), nullable(strings.Join(mentions, ",")), pm.GetKey().GetID(), chat); err != nil {
					logger.Warnf("Failed to store edit: %v", err)
				}
			} else if chat == "" {
				store.pendChange(evt, pm.GetKey().GetID(), "edit", messageText(edited))
			}
		case waE2E.ProtocolMessage_EPHEMERAL_SETTING: // the timer of a chat (a group's comes as its change)
			if evt.Info.Chat.Server != types.GroupServer {
				if err := store.storeChatEvent(evt, "timer", map[string]any{"seconds": pm.GetEphemeralExpiration()}, ""); err != nil {
					logger.Warnf("Failed to store a timer: %v", err)
				}
			}
		case waE2E.ProtocolMessage_REVOKE:
			if chat, ok := changeable(client, store, evt, pm.GetKey().GetID(), true); ok {
				if _, err := store.db.Exec("UPDATE messages SET deleted = 1 WHERE id = ? AND chat_jid = ?",
					pm.GetKey().GetID(), chat); err != nil {
					logger.Warnf("Failed to store deletion: %v", err)
				}
			} else if chat == "" {
				store.pendChange(evt, pm.GetKey().GetID(), "delete", "")
			}
		}
		return true
	}
	if rm := msg.GetReactionMessage(); rm != nil {
		t := evt.Info.Timestamp
		if ms := rm.GetSenderTimestampMS(); ms > 0 {
			t = time.UnixMilli(ms)
		}
		if err := store.storeReaction(chatJID, rm.GetKey().GetID(), sender, evt.Info.IsFromMe, rm.GetText(), t); err != nil {
			logger.Warnf("Failed to store reaction: %v", err)
		}
		return true
	}
	if cl := msg.GetCallLogMesssage(); cl != nil {
		storeCallLog(store, evt, cl, logger)
		return true
	}
	if pin := msg.GetPinInChatMessage(); pin != nil {
		code, args := "pin", map[string]any{"seconds": nil}
		if pin.GetType() == waE2E.PinInChatMessage_UNPIN_FOR_ALL {
			code, args = "unpin", map[string]any{}
		} else if d := msg.GetMessageContextInfo().GetMessageAddOnDurationInSecs(); d > 0 {
			args["seconds"] = d
		}
		if err := store.storeChatEvent(evt, code, args, pin.GetKey().GetID()); err != nil {
			logger.Warnf("Failed to store a pin: %v", err)
		}
		return true
	}
	if pu := msg.GetPollUpdateMessage(); pu != nil {
		vote, err := client.DecryptPollVote(context.Background(), evt)
		if err != nil {
			logger.Warnf("Failed to read a poll vote: %v", err)
			return true
		}
		// one voter by one jid: their number's where the vote came from a LID and names it
		voter := sender
		if evt.Info.Sender.Server == types.HiddenUserServer && !evt.Info.SenderAlt.IsEmpty() {
			voter = evt.Info.SenderAlt.ToNonAD().String()
		}
		chosen := []string{}
		for _, h := range vote.GetSelectedOptions() {
			chosen = append(chosen, hex.EncodeToString(h))
		}
		js, _ := json.Marshal(chosen)
		if _, err := store.db.Exec(`INSERT INTO poll_votes VALUES (?, ?, ?, ?, ?) ON CONFLICT (chat_jid, poll_id, voter)
			DO UPDATE SET options = excluded.options, timestamp = excluded.timestamp WHERE excluded.timestamp >= poll_votes.timestamp`,
			chatJID, pu.GetPollCreationMessageKey().GetID(), voter, string(js), evt.Info.Timestamp); err != nil {
			logger.Warnf("Failed to store a poll vote: %v", err)
		}
		return true
	}

	m := StoredMessage{ID: evt.Info.ID, ChatJID: chatJID, Sender: sender, Timestamp: evt.Info.Timestamp, IsFromMe: evt.Info.IsFromMe}
	if !describe(&m, msg, evt) {
		// a message there is, of a kind not kept (a payment, a product, a sticker pack, one the phone
		// keeps from linked devices): said as such, not lost without a trace
		if msg.GetPlaceholderMessage() != nil || msg.GetSendPaymentMessage() != nil || msg.GetRequestPaymentMessage() != nil ||
			msg.GetProductMessage() != nil || msg.GetOrderMessage() != nil || msg.GetStickerPackMessage() != nil ||
			msg.GetMusicMessage() != nil || msg.GetHighlyStructuredMessage() != nil || msg.GetConditionalRevealMessage() != nil {
			if err := store.storeChatEvent(evt, "unsupported", map[string]any{}, ""); err != nil {
				logger.Warnf("Failed to store a message of a kind not kept: %v", err)
				return false
			}
		}
		return true
	}
	if name == "" {
		name = chatName(client, store, evt.Info.Chat, chatJID, nil, evt.Info.Sender.User, logger)
	}
	if err := store.storeChatTime(chatJID, name, evt.Info.Timestamp); err != nil {
		logger.Warnf("Failed to store chat: %v", err)
	}
	if err := store.storeMessage(m); err != nil {
		logger.Warnf("Failed to store message: %v", err)
		return false
	}
	store.readAfterAll(m.ChatJID, m.ID)
	if m.Poll != "" {
		store.db.Exec("UPDATE messages SET poll = ? WHERE id = ? AND chat_jid = ?", m.Poll, m.ID, m.ChatJID)
	}
	logger.Debugf("stored a message: %s", m.Kind) // its kind, never its text or who wrote it
	return true
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
