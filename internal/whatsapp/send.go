package whatsapp

// Ports bridges/whatsapp/send.go (the REST handler /api/send is now Bridge.Send).

// Sending text and files, as an answer to a message or not; off unless config.toml allows it
// ([whatsapp] send = true, what the bridge's -send was), and kept to what a person does by hand: only into chats where the other side has written
// before, at a human pace, never the same text or file to many chats, and never while a block is
// set (status.go).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type SendLimits struct {
	PerMinute int `json:"per_minute"`
	PerHour   int `json:"per_hour"`
	PerDay    int `json:"per_day"`
	SameText  int `json:"same_text_chats"` // the same text into at most this many chats an hour
}

// Short texts ("ok", an emoji) go to many chats in normal use; only longer ones count as the same text.
const sameTextMinLength = 20

type Sender struct {
	client  *whatsmeow.Client
	store   *MessageStore
	logger  waLog.Logger
	enabled bool
	limits  SendLimits
	mu      sync.Mutex // checks and sending one at a time, so limits cannot be raced past
}

type SendRequest struct {
	Recipient string `json:"recipient"` // a phone number's digits, or a jid
	Message   string `json:"message"`   // the text, or a file's caption
	ReplyTo   string `json:"reply_to"`  // the id of the message answered, in that chat
	// for a message answered that the bridge does not have (from before it was linked)
	ReplySender string `json:"reply_sender"` // "me", or who wrote it: a jid or a number
	ReplyText   string `json:"reply_text"`
	// people of a group the text names: each a jid or a number, written in the text as @<its user
	// part> (@306912345678); the bridge writes them as the group names its members
	Mentions []string `json:"mentions"`
	Media    []byte   `json:"media"` // a file to send, base64 in the JSON
	Filename string   `json:"filename"`
	MimeType string   `json:"mime_type"`
}

type SendResponse struct {
	Success   bool      `json:"success"`
	Message   string    `json:"message"`
	ID        string    `json:"id,omitempty"`
	ChatJID   string    `json:"chat_jid,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

func (s *Sender) count(since time.Duration) int {
	var n int
	s.store.db.QueryRow("SELECT count(*) FROM sent WHERE at > ?", time.Now().Add(-since).Unix()).Scan(&n)
	return n
}

func (s *Sender) counts() map[string]int {
	return map[string]int{"minute": s.count(time.Minute), "hour": s.count(time.Hour), "day": s.count(24 * time.Hour)}
}

// chatFor finds the existing chat a recipient means where the other side has written: a number
// may be kept under its LID.
func (s *Sender) chatFor(recipient string) (types.JID, error) {
	var candidates []types.JID
	if strings.Contains(recipient, "@") {
		j, err := types.ParseJID(recipient)
		if err != nil {
			return types.JID{}, fmt.Errorf("not a jid: %v", err)
		}
		candidates = append(candidates, j.ToNonAD())
	} else {
		candidates = append(candidates, types.NewJID(strings.TrimPrefix(recipient, "+"), types.DefaultUserServer))
	}
	if c := candidates[0]; c.Server == types.DefaultUserServer {
		if lid, err := s.client.Store.LIDs.GetLIDForPN(context.Background(), c); err == nil && !lid.IsEmpty() {
			candidates = append(candidates, lid)
		}
	} else if c.Server == types.HiddenUserServer {
		if pn, err := s.client.Store.LIDs.GetPNForLID(context.Background(), c); err == nil && !pn.IsEmpty() {
			candidates = append(candidates, pn)
		}
	}
	for _, c := range candidates {
		switch c.Server {
		case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
		default:
			return types.JID{}, fmt.Errorf("not a person's or a group's chat: %s", c)
		}
		var n int
		s.store.db.QueryRow("SELECT count(*) FROM messages WHERE chat_jid = ? AND NOT is_from_me", c.String()).Scan(&n)
		if n > 0 {
			return c, nil
		}
	}
	return types.JID{}, fmt.Errorf("no chat with %s where they have written: the bridge sends only there", recipient)
}

// send returns an HTTP status and the answer.
func (s *Sender) send(req SendRequest) (int, SendResponse) {
	fail := func(code int, format string, args ...interface{}) (int, SendResponse) {
		return code, SendResponse{Success: false, Message: fmt.Sprintf(format, args...)}
	}
	if !s.enabled {
		return fail(http.StatusForbidden, "sending is off: [whatsapp] send = true in config.toml turns it on")
	}
	text := strings.TrimSpace(req.Message)
	if (text == "" && len(req.Media) == 0) || req.Recipient == "" {
		return fail(http.StatusBadRequest, "recipient and a message or a file are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if blocked, _ := s.store.state("send_blocked"); blocked != "" {
		return fail(http.StatusLocked, "sending is blocked (%s): allow it again once it is safe", blocked)
	}
	if !s.client.IsConnected() || !s.client.IsLoggedIn() {
		return fail(http.StatusServiceUnavailable, "not connected to WhatsApp")
	}
	chat, err := s.chatFor(req.Recipient)
	if err != nil {
		return fail(http.StatusForbidden, "%v", err)
	}
	text, mentioned, err := s.mentions(chat, text, req.Mentions)
	if err != nil {
		return fail(http.StatusBadRequest, "%v", err)
	}
	for _, l := range []struct {
		n    int
		over time.Duration
		word string
	}{{s.limits.PerMinute, time.Minute, "minute"}, {s.limits.PerHour, time.Hour, "hour"}, {s.limits.PerDay, 24 * time.Hour, "day"}} {
		if s.count(l.over) >= l.n {
			return fail(http.StatusTooManyRequests, "limit reached: %d messages a %s", l.n, l.word)
		}
	}
	media := sha256.Sum256(req.Media)
	sum := sha256.Sum256([]byte(text + "\x00" + hex.EncodeToString(media[:])))
	hash := hex.EncodeToString(sum[:])
	if len(req.Media) > 0 || utf8.RuneCountInString(text) >= sameTextMinLength {
		var others int
		s.store.db.QueryRow("SELECT count(DISTINCT chat_jid) FROM sent WHERE text_hash = ? AND chat_jid != ? AND at > ?",
			hash, chat.String(), time.Now().Add(-time.Hour).Unix()).Scan(&others)
		if others >= s.limits.SameText {
			return fail(http.StatusTooManyRequests, "the same text or file went to %d other chats this hour", others)
		}
	}

	stored := StoredMessage{ChatJID: chat.String(), Sender: ownJID(s.client), Content: text, IsFromMe: true, Kind: "text"}
	quote, err := s.quote(chat, req, &stored)
	if err != nil {
		return fail(http.StatusBadRequest, "%v", err)
	}
	if len(mentioned) > 0 {
		if quote == nil {
			quote = &waE2E.ContextInfo{}
		}
		quote.MentionedJID = mentioned
	}
	var msg *waE2E.Message
	switch {
	case len(req.Media) > 0:
		if msg, err = s.mediaMessage(req, text, quote, &stored); err != nil {
			return fail(http.StatusInternalServerError, "uploading the file failed: %v", err)
		}
	case quote != nil:
		msg = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text), ContextInfo: quote}}
	default:
		msg = &waE2E.Message{Conversation: proto.String(text)}
	}
	resp, err := s.client.SendMessage(context.Background(), chat, msg)
	if err != nil {
		return fail(http.StatusInternalServerError, "sending failed: %v", err)
	}
	s.store.db.Exec("INSERT INTO sent (at, chat_jid, id, text_hash) VALUES (?, ?, ?, ?)", time.Now().Unix(), chat.String(), resp.ID, hash)
	// The bridge does not get its own messages back: store it as WhatsApp would show it.
	var name string
	s.store.db.QueryRow("SELECT name FROM chats WHERE jid = ?", chat.String()).Scan(&name)
	s.store.storeChatTime(chat.String(), name, resp.Timestamp)
	stored.ID, stored.Timestamp, stored.Mentions = resp.ID, resp.Timestamp, mentioned
	if err := s.store.storeMessage(stored); err != nil {
		s.logger.Warnf("Sent, but failed to store: %v", err)
	} else if len(req.Media) > 0 {
		rel := mediaFile(stored.MediaType, stored.Filename, stored.ChatJID, stored.ID)
		if err := s.store.writeMedia(rel, req.Media); err == nil {
			s.store.db.Exec("UPDATE messages SET media_path = ? WHERE id = ? AND chat_jid = ?", rel, stored.ID, stored.ChatJID)
		}
	}
	return http.StatusOK, SendResponse{Success: true, Message: "sent", ID: resp.ID, ChatJID: chat.String(), Timestamp: resp.Timestamp}
}

// mentions checks that each person named is in the group, and writes each @ in the text as the
// group names them (a number in one group, a LID in another).
func (s *Sender) mentions(chat types.JID, text string, who []string) (string, []string, error) {
	if len(who) == 0 {
		return text, nil, nil
	}
	if chat.Server != types.GroupServer {
		return "", nil, fmt.Errorf("mentions are for groups")
	}
	var jids []string
	done := map[string]bool{} // each person once: their every place in the text is rewritten at once
	for _, w := range who {
		given := types.NewJID(strings.TrimPrefix(w, "+"), types.DefaultUserServer)
		if strings.Contains(w, "@") {
			j, err := types.ParseJID(w)
			if err != nil {
				return "", nil, fmt.Errorf("not a jid: %s", w)
			}
			given = j.ToNonAD()
		}
		jid := s.store.mentionJID(chat, given.String())
		if jid == "" {
			return "", nil, fmt.Errorf("%s is not in this group", w)
		}
		if done[jid] {
			continue
		}
		done[jid] = true
		token := "@" + given.User
		if !containsToken(text, token) {
			return "", nil, fmt.Errorf("the text does not name %s", token)
		}
		named, _ := types.ParseJID(jid)
		text = replaceToken(text, token, "@"+named.User)
		jids = append(jids, jid)
	}
	return text, jids, nil
}

// A token ends where its digits do: @3069 is not in @30691.
func tokenAt(text, token string, i int) bool {
	end := i + len(token)
	return strings.HasPrefix(text[i:], token) && (end == len(text) || text[end] < '0' || text[end] > '9')
}

func containsToken(text, token string) bool {
	for i := strings.Index(text, token); i >= 0; {
		if tokenAt(text, token, i) {
			return true
		}
		next := strings.Index(text[i+1:], token)
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return false
}

func replaceToken(text, token, with string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		if tokenAt(text, token, i) {
			out.WriteString(with)
			i += len(token)
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

// quote is the answered message as WhatsApp shows it above the answer: who wrote it and its text
// (or its kind, for a file without one); nil when nothing is answered.
func (s *Sender) quote(chat types.JID, req SendRequest, stored *StoredMessage) (*waE2E.ContextInfo, error) {
	if req.ReplyTo == "" {
		return nil, nil
	}
	var sender, content, kind, filename sql.NullString
	var fromMe sql.NullBool
	err := s.store.db.QueryRow("SELECT sender, is_from_me, content, kind, filename FROM messages WHERE id = ? AND chat_jid = ?",
		req.ReplyTo, chat.String()).Scan(&sender, &fromMe, &content, &kind, &filename)
	var participant string
	switch {
	case err == nil && fromMe.Bool, err != nil && req.ReplySender == "me":
		participant = ownJID(s.client)
	case err == nil && sender.String != "":
		participant = sender.String
	case err == nil:
		participant = chat.String()
	case req.ReplySender != "":
		j, perr := types.ParseJID(req.ReplySender)
		if !strings.Contains(req.ReplySender, "@") {
			j, perr = types.NewJID(strings.TrimPrefix(req.ReplySender, "+"), types.DefaultUserServer), nil
		}
		if perr != nil {
			return nil, fmt.Errorf("reply_sender is not a jid: %v", perr)
		}
		participant = j.ToNonAD().String()
	case chat.Server != types.GroupServer:
		participant = chat.String() // a person's chat: theirs
	default:
		return nil, fmt.Errorf("who wrote the message answered is not known: give reply_sender")
	}
	text := content.String
	if err != nil {
		text, kind.String = req.ReplyText, "text"
	}
	stored.ReplyTo, stored.ReplyText = req.ReplyTo, text
	return &waE2E.ContextInfo{StanzaID: proto.String(req.ReplyTo), Participant: proto.String(participant),
		QuotedMessage: quotedMessage(kind.String, text, filename.String)}, nil
}

func quotedMessage(kind, text, filename string) *waE2E.Message {
	switch kind {
	case "image":
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String(text)}}
	case "video":
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String(text)}}
	case "voice", "audio":
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(kind == "voice")}}
	case "document":
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String(filename), Caption: proto.String(text)}}
	case "sticker":
		return &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}
	}
	return &waE2E.Message{Conversation: proto.String(text)}
}

// fileKind is how a file goes: as a picture, a video or a sound where WhatsApp shows those (a
// sound has no caption: with one it goes as a document), else as a document.
func fileKind(mimeType string, hasCaption bool) string {
	switch {
	case mimeType == "image/jpeg", mimeType == "image/png":
		return "image"
	case mimeType == "video/mp4", mimeType == "video/3gpp":
		return "video"
	case !hasCaption && (mimeType == "audio/ogg" || mimeType == "audio/mpeg" || mimeType == "audio/mp4" || mimeType == "audio/aac"):
		return "audio"
	}
	return "document"
}

// thumbnail is a small JPEG of a picture, which WhatsApp shows until the picture is downloaded.
func thumbnail(data []byte) (thumb []byte, width, height int) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0
	}
	b := img.Bounds()
	width, height = b.Dx(), b.Dy()
	scale := 96.0 / float64(max(width, height))
	if scale > 1 {
		scale = 1
	}
	tw, th := max(1, int(float64(width)*scale)), max(1, int(float64(height)*scale))
	small := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			small.Set(x, y, img.At(b.Min.X+int(float64(x)/scale), b.Min.Y+int(float64(y)/scale)))
		}
	}
	var out bytes.Buffer
	if jpeg.Encode(&out, small, &jpeg.Options{Quality: 60}) != nil {
		return nil, width, height
	}
	return out.Bytes(), width, height
}

// mediaMessage uploads the file (encrypted, as WhatsApp does) and makes the message that carries it.
func (s *Sender) mediaMessage(req SendRequest, caption string, quote *waE2E.ContextInfo, stored *StoredMessage) (*waE2E.Message, error) {
	mimeType := strings.TrimSpace(strings.SplitN(req.MimeType, ";", 2)[0])
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(req.Filename)))
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(req.Media)
	}
	mimeType = strings.SplitN(mimeType, ";", 2)[0]
	kind := fileKind(mimeType, caption != "")
	up, err := s.client.Upload(context.Background(), req.Media, mediaTypes[kind])
	if err != nil {
		return nil, err
	}
	filename := filepath.Base(req.Filename)
	if filename == "." || filename == "/" {
		filename = ""
	}
	*stored = StoredMessage{ChatJID: stored.ChatJID, Sender: stored.Sender, Content: caption, IsFromMe: true,
		ReplyTo: stored.ReplyTo, ReplyText: stored.ReplyText, Kind: kind, MediaType: kind, Filename: filename,
		URL: up.URL, DirectPath: up.DirectPath, MediaKey: up.MediaKey, FileSHA256: up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256, FileLength: up.FileLength}
	var text *string
	if caption != "" {
		text = proto.String(caption)
	}
	switch kind {
	case "image":
		thumb, w, h := thumbnail(req.Media)
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: text, Mimetype: proto.String(mimeType),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			JPEGThumbnail: thumb, Width: proto.Uint32(uint32(w)), Height: proto.Uint32(uint32(h)), ContextInfo: quote}}, nil
	case "video":
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: text, Mimetype: proto.String(mimeType),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			ContextInfo: quote}}, nil
	case "audio":
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String(mimeType),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			ContextInfo: quote}}, nil
	}
	if filename == "" {
		filename = "file"
	}
	stored.Filename = filename
	return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: text, Mimetype: proto.String(mimeType),
		FileName: proto.String(filename), Title: proto.String(filename),
		URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		ContextInfo: quote}}, nil
}
