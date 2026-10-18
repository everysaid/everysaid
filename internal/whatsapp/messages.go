package whatsapp

// The messages as they arrive: what each says, its file, its changes, as a batch for the archive
// (importers.WABatch): its kind, the message it answers, forwarding, a shared place, contact or
// poll, a link's preview; reactions, calls, votes and the edits and deletions of earlier messages.

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/archive"
	"everysaid/internal/importers"
)

// StoredMessage is what a message says, as the bridge reads it.
type StoredMessage struct {
	ID, ChatJID, Sender, Content         string
	Timestamp                            time.Time
	IsFromMe                             bool
	MediaType, Filename, URL, DirectPath string
	MediaKey, FileSHA256, FileEncSHA256  []byte
	FileLength                           uint64
	MediaPath                            string // the file already in the store folder (sent from here), relative to it
	Kind, Subtype, ReplyTo, ReplyText    string
	Mentions                             []string // the jids the text names with @<user>
	Forwarded                            bool
	ForwardFrom                          string // the channel a forwarded message came from
	Album                                string // the album's message id, for files sent together
	Lat, Lon                             *float64
	Place                                string
	Poll                                 string // a poll's question, options, multiple (JSON)
	Preview                              LinkPreview
}

// LinkPreview is a link's preview as the sender's app made it: the small picture the message
// carries, and where its high-quality one is on WhatsApp's servers (downloaded later, media.go).
type LinkPreview struct {
	URL, Title, Description string
	Thumb                   []byte // JPEGThumbnail
	DirectPath              string
	SHA256, EncSHA256, Key  []byte
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
		x := msg.GetExtendedTextMessage()
		if x.GetMatchedText() != "" {
			m.Subtype = "link"
		}
		if x.GetTitle() != "" || x.GetDescription() != "" || len(x.GetJPEGThumbnail()) > 0 || x.GetThumbnailDirectPath() != "" {
			m.Preview = LinkPreview{URL: x.GetMatchedText(), Title: x.GetTitle(), Description: x.GetDescription(),
				Thumb: x.GetJPEGThumbnail(), DirectPath: x.GetThumbnailDirectPath(), SHA256: x.GetThumbnailSHA256(),
				EncSHA256: x.GetThumbnailEncSHA256(), Key: x.GetMediaKey()}
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
		for _, w := range []*waE2E.FutureProofMessage{msg.GetAssociatedChildMessage(), msg.GetGroupMentionedMessage(), msg.GetLottieStickerMessage(),
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
		m.ForwardFrom = ci.GetForwardedNewsletterMessageInfo().GetNewsletterName()
		m.Mentions = ci.GetMentionedJID()
	}
	if as := msg.GetMessageContextInfo().GetMessageAssociation(); as.GetAssociationType() == waE2E.MessageAssociation_MEDIA_ALBUM {
		m.Album = as.GetParentMessageKey().GetID()
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

// others are a jid's other jids (alternates), without it.
func others(client *whatsmeow.Client, jid, alt types.JID) []string {
	all := alternates(client, jid, alt)
	if len(all) > 0 && all[0] == jidString(jid) {
		return all[1:]
	}
	return all
}

// wa is the message as the archive's writer takes it, its file what downloads it.
func (m StoredMessage) wa() importers.WAMessage {
	out := importers.WAMessage{Chat: m.ChatJID, ID: m.ID, Sender: m.Sender, FromMe: m.IsFromMe, At: m.Timestamp.UnixMilli(),
		Kind: m.Kind, Subtype: m.Subtype, Text: m.Content, MediaType: m.MediaType, ReplyTo: m.ReplyTo, ReplyText: m.ReplyText,
		Mentions: m.Mentions, Forwarded: m.Forwarded, ForwardFrom: m.ForwardFrom, Album: m.Album, Lat: m.Lat, Lon: m.Lon,
		Place: m.Place, Poll: m.Poll}
	if p := m.Preview; p.Title != "" || p.Description != "" || len(p.Thumb) > 0 {
		out.Preview = &archive.Preview{URL: p.URL, Title: p.Title, Description: p.Description, Image: p.Thumb}
	}
	if m.Preview.DirectPath != "" {
		out.PreviewFile = &importers.WAPreviewFile{URL: m.Preview.URL, DirectPath: m.Preview.DirectPath,
			SHA256: m.Preview.SHA256, EncSHA256: m.Preview.EncSHA256, Key: m.Preview.Key}
	}
	if m.MediaType != "" {
		out.File = &importers.WAFile{MediaType: m.MediaType, Filename: m.Filename, URL: m.URL, DirectPath: m.DirectPath,
			MediaKey: m.MediaKey, FileSHA256: m.FileSHA256, FileEncSHA256: m.FileEncSHA256, FileLength: m.FileLength,
			Path: m.MediaPath}
	}
	return out
}

// remember keeps where a message is and who wrote it, for a week (whatsapp_recent): what the
// bridge answers, edits, deletes or marks read is found there.
func (store *Store) remember(m StoredMessage) {
	store.exec(`INSERT INTO whatsapp_recent (store, chat_jid, id, sender, from_me, at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (store, chat_jid, id) DO UPDATE SET sender = excluded.sender, from_me = excluded.from_me, at = excluded.at`,
		store.key, m.ChatJID, m.ID, nullable(m.Sender), m.IsFromMe, m.Timestamp.UnixMilli())
}

// processMessage adds to the batch a message, or what it does to an earlier one (an edit, a
// deletion, a reaction, a vote), or the call it logs. name: the chat's name where already known.
func processMessage(client *whatsmeow.Client, store *Store, b *importers.WABatch, evt *events.Message, name string, logger waLog.Logger) {
	msg := evt.Message
	if msg == nil || isChannel(evt.Info.Chat) {
		return
	}
	// a reaction or an edit sent encrypted with the message's secret (newer clients): what it says
	if er := msg.GetEncReactionMessage(); er != nil {
		r, err := client.DecryptReaction(context.Background(), evt)
		if err != nil {
			logger.Warnf("Failed to read an encrypted reaction: %v", err)
			return
		}
		r.Key = er.GetTargetMessageKey()
		msg = &waE2E.Message{ReactionMessage: r}
	}
	if msg.GetSecretEncryptedMessage() != nil {
		inner, err := client.DecryptSecretEncryptedMessage(context.Background(), evt)
		if err != nil {
			logger.Warnf("Failed to read a secret-encrypted message: %v", err)
			return
		}
		e := *evt
		e.RawMessage, e.Message = inner, nil
		evt = e.UnwrapRaw()
		msg = evt.Message
		if msg == nil {
			return
		}
	}
	chatJID := evt.Info.Chat.String()
	sender := evt.Info.Sender.ToNonAD().String()
	at := evt.Info.Timestamp.UnixMilli()
	notice := func(code string, args map[string]any, target string) {
		b.Notices = append(b.Notices, importers.WANotice{Chat: chatJID, ID: evt.Info.ID, Sender: sender,
			FromMe: evt.Info.IsFromMe, At: at, Code: code, Args: args, Target: target})
	}

	if pm := msg.GetProtocolMessage(); pm != nil {
		// a person's chat, and who changed it, may be kept under their number or their LID
		var alt, senderAlt []string
		if info := evt.Info; info.Chat.Server != types.GroupServer {
			chatAlt := info.SenderAlt
			if info.IsFromMe {
				chatAlt = info.RecipientAlt
			}
			alt, senderAlt = others(client, info.Chat, chatAlt), others(client, info.Sender, info.SenderAlt)
		}
		switch pm.GetType() {
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			edited := pm.GetEditedMessage()
			var mentions []string
			if ci := contextInfo(edited); ci != nil {
				mentions = ci.GetMentionedJID()
			}
			b.Changes = append(b.Changes, importers.WAChange{Chat: chatJID, Target: pm.GetKey().GetID(), Change: "edit",
				Sender: sender, FromMe: evt.Info.IsFromMe, Text: messageText(edited), Mentions: mentions, At: at,
				Alt: alt, SenderAlt: senderAlt})
		case waE2E.ProtocolMessage_EPHEMERAL_SETTING: // the timer of a chat (a group's comes as its change)
			if evt.Info.Chat.Server != types.GroupServer {
				notice("timer", map[string]any{"seconds": pm.GetEphemeralExpiration()}, "")
			}
		case waE2E.ProtocolMessage_REVOKE:
			b.Changes = append(b.Changes, importers.WAChange{Chat: chatJID, Target: pm.GetKey().GetID(), Change: "delete",
				Sender: sender, FromMe: evt.Info.IsFromMe, At: at, Alt: alt, SenderAlt: senderAlt})
		}
		return
	}
	if rm := msg.GetReactionMessage(); rm != nil {
		t := at
		if ms := rm.GetSenderTimestampMS(); ms > 0 {
			t = ms
		}
		b.Reactions = append(b.Reactions, importers.WAReaction{Chat: chatJID, Target: rm.GetKey().GetID(), Sender: sender,
			FromMe: evt.Info.IsFromMe, Emoji: rm.GetText(), At: t})
		return
	}
	if cl := msg.GetCallLogMesssage(); cl != nil {
		c := importers.WACallLog{ID: evt.Info.ID, Chat: chatJID, FromMe: evt.Info.IsFromMe, Group: evt.Info.IsGroup,
			Video: cl.GetIsVideo(), At: at, Outcome: cl.GetCallOutcome().String(), Duration: cl.GetDurationSecs()}
		for _, p := range cl.GetParticipants() {
			c.Participants = append(c.Participants, [2]string{p.GetJID(), p.GetCallOutcome().String()})
		}
		b.CallLogs = append(b.CallLogs, c)
		return
	}
	if pin := msg.GetPinInChatMessage(); pin != nil {
		code, args := "pin", map[string]any{"seconds": nil}
		if pin.GetType() == waE2E.PinInChatMessage_UNPIN_FOR_ALL {
			code, args = "unpin", map[string]any{}
		} else if d := msg.GetMessageContextInfo().GetMessageAddOnDurationInSecs(); d > 0 {
			args["seconds"] = d
		}
		notice(code, args, pin.GetKey().GetID())
		return
	}
	if pu := msg.GetPollUpdateMessage(); pu != nil {
		vote, err := client.DecryptPollVote(context.Background(), evt)
		if err != nil {
			logger.Warnf("Failed to read a poll vote: %v", err)
			return
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
		b.Votes = append(b.Votes, importers.WAVote{Chat: chatJID, Poll: pu.GetPollCreationMessageKey().GetID(), Voter: voter,
			Options: chosen, At: at})
		return
	}

	m := StoredMessage{ID: evt.Info.ID, ChatJID: chatJID, Sender: sender, Timestamp: evt.Info.Timestamp, IsFromMe: evt.Info.IsFromMe}
	if !describe(&m, msg, evt) {
		// a message there is, of a kind not kept (a payment, a product, a sticker pack, one the phone
		// keeps from linked devices): said as such, not lost without a trace
		if msg.GetPlaceholderMessage() != nil || msg.GetSendPaymentMessage() != nil || msg.GetRequestPaymentMessage() != nil ||
			msg.GetProductMessage() != nil || msg.GetOrderMessage() != nil || msg.GetStickerPackMessage() != nil ||
			msg.GetMusicMessage() != nil || msg.GetHighlyStructuredMessage() != nil || msg.GetConditionalRevealMessage() != nil {
			notice("unsupported", map[string]any{}, "")
		}
		return
	}
	if name == "" {
		name = chatName(client, store, evt.Info.Chat, chatJID, nil, evt.Info.Sender.User, logger)
	}
	keep(store, b, m, name)
	logger.Debugf("a message: %s", m.Kind) // its kind, never its text or who wrote it
}

// keep adds a message to the batch, its chat named, and keeps what the bridge needs of it.
func keep(store *Store, b *importers.WABatch, m StoredMessage, name string) {
	if b.Chats == nil {
		b.Chats = map[string]string{}
	}
	b.Chats[m.ChatJID] = name
	store.storeChat(m.ChatJID, name, m.Timestamp)
	if !m.IsFromMe {
		store.incoming(m.ChatJID)
	}
	store.remember(m)
	b.Messages = append(b.Messages, m.wa())
}

// historyReactions adds the reactions a history-sync message carries.
func historyReactions(client *whatsmeow.Client, b *importers.WABatch, chatJID types.JID, webMsg *waWeb.WebMessageInfo) {
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
		b.Reactions = append(b.Reactions, importers.WAReaction{Chat: chatJID.String(), Target: webMsg.GetKey().GetID(),
			Sender: sender, FromMe: key.GetFromMe(), Emoji: r.GetText(), At: r.GetSenderTimestampMS(), History: true})
	}
}

// handleCallEvent adds the call signalling the bridge sees: offers (one-to-one, and notices of
// group calls), acceptance, rejection and the end.
func handleCallEvent(client *whatsmeow.Client, b *importers.WABatch, evt interface{}) {
	offer := func(meta types.BasicCallMeta, video bool) {
		chat, isGroup := meta.From.ToNonAD(), !meta.GroupJID.IsEmpty()
		if isGroup {
			chat = meta.GroupJID
		}
		creator := meta.CallCreator.ToNonAD().String()
		b.CallSignals = append(b.CallSignals, importers.WACallSignal{ID: meta.CallID, Kind: "offer", Chat: chat.String(),
			Creator: creator, FromMe: creator == ownJID(client), Group: isGroup, Video: video, At: meta.Timestamp.UnixMilli()})
	}
	signal := func(kind string, meta types.BasicCallMeta, reason string) {
		b.CallSignals = append(b.CallSignals, importers.WACallSignal{ID: meta.CallID, Kind: kind, At: meta.Timestamp.UnixMilli(),
			Reason: reason})
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
		signal("accept", v.BasicCallMeta, "")
	case *events.CallReject:
		signal("reject", v.BasicCallMeta, "")
	case *events.CallTerminate:
		signal("terminate", v.BasicCallMeta, v.Reason)
	}
}
