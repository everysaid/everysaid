package meta

// Sending, reacting, editing, unsending and read receipts, through the live session: a thread on
// Meta's servers through Lightspeed tasks, an end-to-end encrypted chat through whatsmeow (as
// mautrix-meta's pkg/connector/handlematrix.go and pkg/msgconv do). What went is written into the
// archive at once (an encrypted chat does not echo the owner's own messages).

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif" // the sizes of pictures sent into encrypted chats
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

// connectWait is how long an action waits for the connection to come up.
const connectWait = 30 * time.Second

// errNotConnected: no live connection (the action waits in the outbox).
func errNotConnected(service string) error {
	if service == "instagram" {
		return errs.Plugin("not connected to Instagram", 503)
	}
	return errs.Plugin("not connected to Messenger", 503)
}

// session is the instance's live session, connected; nil and the error when it is not.
func liveFor(ctx context.Context, c *plugins.Context, service string) (*liveSession, error) {
	deadline := time.Now().Add(connectWait)
	for {
		if s := running(c.ID); s != nil && s.connected.Load() {
			return s, nil
		}
		if time.Now().After(deadline) {
			return nil, errNotConnected(service)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// encrypted says whether a thread is an end-to-end encrypted chat.
func (s *liveSession) encrypted(key string) bool {
	return isEncrypted(s.thread(key).Type)
}

func (s *liveSession) group(key string) bool {
	return s.thread(key).Type == int64(table.ENCRYPTED_OVER_WA_GROUP)
}

// fbThread is a thread's Facebook key (an encrypted group's own key maps back to it).
func (s *liveSession) fbThread(key string) int64 {
	if t := s.thread(key); t.FBKey != "" {
		return fbid(t.FBKey)
	}
	return fbid(key)
}

// senderOf is the id of who sent a message of the archive (the owner's own: theirs).
func (s *liveSession) senderOf(msg plugins.Ref) string {
	if msg.Outgoing {
		return s.self
	}
	return db.Str(s.c.Store().Read(), "SELECT a.value FROM message m JOIN address a ON a.id = m.sender_id WHERE m.id = ?", msg.ID)
}

// waKeyOf is a message's key as whatsmeow names it.
func (s *liveSession) waKeyOf(conv plugins.Conversation, msg plugins.Ref) *waCommon.MessageKey {
	k := &waCommon.MessageKey{RemoteJID: proto.String(waJID(conv.Key, s.group(conv.Key)).String()), ID: proto.String(msg.Key)}
	if msg.Outgoing {
		k.FromMe = proto.Bool(true)
	} else if s.group(conv.Key) {
		k.Participant = proto.String(types.NewJID(s.senderOf(msg), types.MessengerServer).String())
	}
	return k
}

// mentionData is Meta's form of the people a text names (offsets in UTF-16 units).
func (s *liveSession) mentionData(text string, mentions []plugins.Mention) *socket.MentionData {
	if len(mentions) == 0 {
		return nil
	}
	runes := []rune(text)
	var ids, offs, lens, typs []string
	for _, m := range mentions {
		if m.Start < 0 || m.Start+m.Length > len(runes) {
			continue
		}
		id := db.Str(s.c.Store().Read(), "SELECT value FROM address WHERE id = ?", m.AddressID)
		if fbid(id) == 0 {
			continue
		}
		off := len(utf16.Encode(runes[:m.Start]))
		ln := len(utf16.Encode(runes[m.Start : m.Start+m.Length]))
		ids, offs, lens, typs = append(ids, id), append(offs, strconv.Itoa(off)), append(lens, strconv.Itoa(ln)), append(typs, "p")
	}
	if len(ids) == 0 {
		return nil
	}
	return &socket.MentionData{MentionIDs: strings.Join(ids, ","), MentionOffsets: strings.Join(offs, ","),
		MentionLengths: strings.Join(lens, ","), MentionTypes: strings.Join(typs, ",")}
}

// fileKind is the kind of a file sent, by its type.
func fileKind(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "voice"
	}
	return "file"
}

// send sends a message (a file with its text as caption) and writes it into the archive.
func send(ctx context.Context, c *plugins.Context, service string, conv plugins.Conversation, text string,
	reply *plugins.Reply, mentions []plugins.Mention, file *plugins.File) (any, error) {
	s, err := liveFor(ctx, c, service)
	if err != nil {
		return nil, err
	}
	if conv.Key == "" {
		return nil, errs.Plugin("This chat has no thread on the service", 409)
	}
	mime := ""
	if file != nil {
		mime = file.MimeType
		if mime == "" {
			mime = http.DetectContentType(file.Data)
		}
	}
	var id string
	var ts int64
	gif := false
	switch {
	case s.ig != nil:
		var ids []string
		ids, ts, err = s.igSend(ctx, conv.Key, text, reply, mentions, file, mime)
		if err == nil && len(ids) > 1 { // the file, then its text as a message of its own
			id = ids[0]
			sent := plugins.Sent{Keys: ids}
			ms := []importers.MetaMessage{{Thread: conv.Key, ID: ids[0], TS: ts, Outgoing: true, Kind: fileKind(mime)},
				{Thread: conv.Key, ID: ids[1], TS: ts + 1, Outgoing: true, Text: text, Kind: "text"}}
			if reply != nil {
				ms[0].ReplyTo = reply.Key
			}
			ms[0].Files = sentFile(c, conv.Key, id, file, mime)
			return sent, s.write(func(w *importers.MetaWriter, a *archive.Archive) {
				for _, m := range ms {
					w.Add(m)
				}
			}, true)
		}
		if len(ids) > 0 {
			id = ids[0]
		}
	case s.encrypted(conv.Key):
		id, ts, err = s.sendWA(ctx, conv, text, reply, file, mime)
		gif = file != nil && (file.Animated && fileKind(mime) == "video" || mime == "image/gif") // played as one
	default: // a GIF goes as the video it is
		id, ts, err = s.sendFB(ctx, conv, text, reply, mentions, file, mime)
	}
	if err != nil {
		return nil, err
	}
	m := importers.MetaMessage{Thread: conv.Key, ID: id, TS: ts, Outgoing: true, Text: text, Kind: "text"}
	if reply != nil {
		m.ReplyTo = reply.Key
	}
	if file != nil {
		m.Kind = fileKind(mime)
		m.Files = sentFile(c, conv.Key, id, file, mime)
	}
	if gif {
		m.Subtype = "gif"
	}
	err = s.write(func(w *importers.MetaWriter, a *archive.Archive) {
		w.Add(m)
	}, len(m.Files) > 0)
	return plugins.Sent{Keys: []string{id}}, err
}

// sentFile is a file sent, written into the download folder for the store to take.
func sentFile(c *plugins.Context, thread, id string, file *plugins.File, mime string) []importers.MetaFile {
	f := pendingFile{Thread: thread, Message: id, Name: file.Filename, Mime: mime}
	path := filepath.Join(mediaDir(c.ID), fileName(f))
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil || os.WriteFile(path, file.Data, 0o600) != nil {
		return nil
	}
	return []importers.MetaFile{{Path: path, Name: file.Filename, Rel: filepath.Base(path)}}
}

// write writes into the archive, the store taking the files it links (Move).
func (s *liveSession) write(fn func(w *importers.MetaWriter, a *archive.Archive), files bool) error {
	_, _, err := sourcekit.RunImporters(s.c, []sourcekit.Step{{Label: liveLabel(s.service), Run: func(a *archive.Archive, _ func(string)) error {
		w := writer(a, s.c, s.service)
		if files {
			w.Store = importers.NewStore(a)
			w.Store.Move = true
		}
		fn(w, a)
		a.Resolve()
		a.Commit()
		if w.Store != nil {
			w.Store.Release()
		}
		return nil
	}}})
	return err
}

// sendFB sends into a thread on Meta's servers; the message's id and time.
func (s *liveSession) sendFB(ctx context.Context, conv plugins.Conversation, text string, reply *plugins.Reply,
	mentions []plugins.Mention, file *plugins.File, mime string) (string, int64, error) {
	thread := s.fbThread(conv.Key)
	otid := methods.GenerateEpochID()
	task := &socket.SendMessageTask{ThreadId: thread, Otid: otid, Source: table.MESSENGER_INBOX_IN_THREAD,
		InitiatingSource: table.FACEBOOK_INBOX, SendType: table.TEXT, SyncGroup: 1, Text: text,
		MentionData: s.mentionData(text, mentions)}
	if reply != nil && reply.Key != "" {
		task.ReplyMetaData = &socket.ReplyMetaData{ReplyMessageId: reply.Key, ReplySourceType: 1, ReplyType: 0}
	}
	if file != nil {
		fid, err := s.cli.Upload(ctx, thread, file.Filename, mime, file.Data)
		if err != nil {
			return "", 0, fmt.Errorf("upload: %w", err)
		}
		task.SendType, task.AttachmentFBIds = table.MEDIA, []int64{fid}
	}
	if err := s.cli.WaitUntilCanSendMessages(ctx, 15*time.Second); err != nil {
		return "", 0, errNotConnected(s.service)
	}
	resp, err := s.cli.ExecuteTasks(ctx, task)
	if err != nil {
		return "", 0, err
	}
	want := strconv.FormatInt(otid, 10)
	var id string
	if resp != nil {
		for _, r := range resp.LSReplaceOptimsiticMessage {
			if r.OfflineThreadingId == want {
				id = r.MessageId
			}
		}
		if id == "" {
			for _, f := range resp.LSMarkOptimisticMessageFailed {
				return "", 0, errs.Plugin("Meta refused the message: "+f.Message, 409)
			}
			for _, f := range resp.LSHandleFailedTask {
				return "", 0, errs.Plugin("Meta refused the message: "+f.Message, 409)
			}
		}
	}
	if id == "" {
		return "", 0, errs.Plugin("Meta did not say the message's id", 502)
	}
	ts, err := methods.ParseMessageID(id)
	if err != nil || ts < time.Now().Add(-time.Hour).UnixMilli() || ts > time.Now().Add(time.Hour).UnixMilli() {
		ts = time.Now().UnixMilli()
	}
	return id, ts, nil
}

// sendWA sends into an encrypted chat; the message's id and time.
func (s *liveSession) sendWA(ctx context.Context, conv plugins.Conversation, text string, reply *plugins.Reply,
	file *plugins.File, mime string) (string, int64, error) {
	e := s.waClient()
	if e == nil {
		return "", 0, errNotConnected(s.service)
	}
	content := &waConsumerApplication.ConsumerApplication_Content{}
	if file == nil {
		content.Content = &waConsumerApplication.ConsumerApplication_Content_MessageText{
			MessageText: &waCommon.MessageText{Text: proto.String(text)}}
	} else {
		c, err := s.waMedia(ctx, e, file, mime, text)
		if err != nil {
			return "", 0, err
		}
		content.Content = c
	}
	var meta waMsgApplication.MessageApplication_Metadata
	if reply != nil && reply.Key != "" {
		meta.QuotedMessage = &waMsgApplication.MessageApplication_Metadata_QuotedMessage{StanzaID: proto.String(reply.Key),
			Participant: proto.String(types.NewJID(s.senderOf(*reply), types.MessengerServer).String())}
	}
	id := strconv.FormatInt(methods.GenerateEpochID(), 10)
	msg := &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: content}}}
	resp, err := e.SendFBMessage(ctx, waJID(conv.Key, s.group(conv.Key)), msg, &meta, whatsmeow.SendRequestExtra{ID: id})
	if err != nil {
		return "", 0, err
	}
	return id, resp.Timestamp.UnixMilli(), nil
}

// waMedia uploads a file to an encrypted chat and wraps it as its kind of message.
func (s *liveSession) waMedia(ctx context.Context, e *whatsmeow.Client, file *plugins.File, mime, caption string) (
	waConsumerApplication.ConsumerApplication_Content_Content, error) {
	kind := fileKind(mime)
	mt := map[string]whatsmeow.MediaType{"image": whatsmeow.MediaImage, "video": whatsmeow.MediaVideo,
		"voice": whatsmeow.MediaAudio, "file": whatsmeow.MediaDocument}[kind]
	if mime == "image/gif" { // Messenger takes a GIF as a video message
		kind, mt = "video", whatsmeow.MediaVideo
	}
	up, err := e.Upload(ctx, file.Data, mt)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	var w, h int
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(file.Data)); err == nil {
		w, h = cfg.Width, cfg.Height
	}
	tw, th := w, h
	if tw == 0 || th == 0 {
		tw, th = 400, 400
	} else if tw > 400 || th > 400 { // the thumbnail's size, which Messenger's apps need
		if tw > th {
			tw, th = 400, th*400/tw
		} else {
			tw, th = tw*400/th, 400
		}
	}
	tr := &waMediaTransport.WAMediaTransport{
		Integral: &waMediaTransport.WAMediaTransport_Integral{FileSHA256: up.FileSHA256, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, DirectPath: &up.DirectPath, MediaKeyTimestamp: proto.Int64(time.Now().Unix())},
		Ancillary: &waMediaTransport.WAMediaTransport_Ancillary{FileLength: proto.Uint64(uint64(len(file.Data))),
			Mimetype: &mime, ObjectID: &up.ObjectID,
			Thumbnail: &waMediaTransport.WAMediaTransport_Ancillary_Thumbnail{ThumbnailWidth: proto.Uint32(uint32(tw)),
				ThumbnailHeight: proto.Uint32(uint32(th))}},
	}
	text := &waCommon.MessageText{Text: proto.String(caption)}
	switch kind {
	case "image":
		m := &waConsumerApplication.ConsumerApplication_ImageMessage{Caption: text}
		err = m.Set(&waMediaTransport.ImageTransport{Integral: &waMediaTransport.ImageTransport_Integral{Transport: tr},
			Ancillary: &waMediaTransport.ImageTransport_Ancillary{Width: proto.Uint32(uint32(w)), Height: proto.Uint32(uint32(h))}})
		return &waConsumerApplication.ConsumerApplication_Content_ImageMessage{ImageMessage: m}, err
	case "video":
		m := &waConsumerApplication.ConsumerApplication_VideoMessage{Caption: text}
		anc := &waMediaTransport.VideoTransport_Ancillary{Width: proto.Uint32(uint32(w)), Height: proto.Uint32(uint32(h))}
		if mime == "image/gif" || file.Animated {
			anc.GifPlayback = proto.Bool(true)
		}
		err = m.Set(&waMediaTransport.VideoTransport{Integral: &waMediaTransport.VideoTransport_Integral{Transport: tr}, Ancillary: anc})
		return &waConsumerApplication.ConsumerApplication_Content_VideoMessage{VideoMessage: m}, err
	case "voice":
		m := &waConsumerApplication.ConsumerApplication_AudioMessage{PTT: proto.Bool(false)}
		err = m.Set(&waMediaTransport.AudioTransport{Integral: &waMediaTransport.AudioTransport_Integral{Transport: tr},
			Ancillary: &waMediaTransport.AudioTransport_Ancillary{}})
		return &waConsumerApplication.ConsumerApplication_Content_AudioMessage{AudioMessage: m}, err
	default:
		name := file.Filename
		m := &waConsumerApplication.ConsumerApplication_DocumentMessage{FileName: &name}
		err = m.Set(&waMediaTransport.DocumentTransport{Integral: &waMediaTransport.DocumentTransport_Integral{Transport: tr},
			Ancillary: &waMediaTransport.DocumentTransport_Ancillary{}})
		return &waConsumerApplication.ConsumerApplication_Content_DocumentMessage{DocumentMessage: m}, err
	}
}

// react puts the owner's reaction on a message ("" takes it back).
func react(ctx context.Context, c *plugins.Context, service string, conv plugins.Conversation, msg plugins.Ref, emoji string) error {
	s, err := liveFor(ctx, c, service)
	if err != nil {
		return err
	}
	if s.ig != nil {
		if err := s.igReact(ctx, conv, msg, emoji); err != nil {
			return err
		}
	} else if s.encrypted(conv.Key) {
		e := s.waClient()
		if e == nil {
			return errNotConnected(service)
		}
		m := &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content_ReactionMessage{
					ReactionMessage: &waConsumerApplication.ConsumerApplication_ReactionMessage{Key: s.waKeyOf(conv, msg),
						Text: proto.String(emoji), SenderTimestampMS: proto.Int64(time.Now().UnixMilli())}}}}}}
		if _, err := e.SendFBMessage(ctx, waJID(conv.Key, s.group(conv.Key)), m, nil); err != nil {
			return err
		}
	} else if _, err := s.cli.ExecuteTasks(ctx, &socket.SendReactionTask{ThreadKey: s.fbThread(conv.Key),
		TimestampMs: time.Now().UnixMilli(), MessageID: msg.Key, Reaction: emoji, ActorID: fbid(s.self), SyncGroup: 1,
		SendAttribution: table.MESSENGER_INBOX_IN_THREAD}); err != nil {
		return err
	}
	return s.write(func(w *importers.MetaWriter, a *archive.Archive) {
		w.React(conv.Key, msg.Key, s.selfUser(), true, emoji)
	}, false)
}

// edit changes the text of the owner's message.
func edit(ctx context.Context, c *plugins.Context, service string, conv plugins.Conversation, msg plugins.Ref, text string) error {
	s, err := liveFor(ctx, c, service)
	if err != nil {
		return err
	}
	if s.ig != nil {
		if err := s.igEdit(ctx, conv, msg, text); err != nil {
			return err
		}
	} else if s.encrypted(conv.Key) {
		e := s.waClient()
		if e == nil {
			return errNotConnected(service)
		}
		m := &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content_EditMessage{
					EditMessage: &waConsumerApplication.ConsumerApplication_EditMessage{Key: s.waKeyOf(conv, msg),
						Message: &waCommon.MessageText{Text: proto.String(text)}, TimestampMS: proto.Int64(time.Now().UnixMilli())}}}}}}
		if _, err := e.SendFBMessage(ctx, waJID(conv.Key, s.group(conv.Key)), m, nil); err != nil {
			return err
		}
	} else {
		resp, err := s.cli.ExecuteTasks(ctx, &socket.EditMessageTask{MessageID: msg.Key, Text: text})
		if err != nil {
			return err
		}
		if resp != nil && len(resp.LSEditMessage) > 0 && resp.LSEditMessage[0].Text != text {
			return errs.Plugin("Meta did not take the edit", 409)
		}
	}
	return s.write(func(w *importers.MetaWriter, a *archive.Archive) { w.Edit(conv.Key, msg.Key, text) }, false)
}

// unsend deletes the owner's message for everyone.
func unsend(ctx context.Context, c *plugins.Context, service string, conv plugins.Conversation, msg plugins.Ref) error {
	s, err := liveFor(ctx, c, service)
	if err != nil {
		return err
	}
	if s.ig != nil {
		if err := s.igUnsend(ctx, conv, msg); err != nil {
			return err
		}
	} else if s.encrypted(conv.Key) {
		e := s.waClient()
		if e == nil {
			return errNotConnected(service)
		}
		m := &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_ApplicationData{
				ApplicationData: &waConsumerApplication.ConsumerApplication_ApplicationData{
					ApplicationContent: &waConsumerApplication.ConsumerApplication_ApplicationData_Revoke{
						Revoke: &waConsumerApplication.ConsumerApplication_RevokeMessage{Key: s.waKeyOf(conv, msg)}}}}}}
		if _, err := e.SendFBMessage(ctx, waJID(conv.Key, s.group(conv.Key)), m, nil); err != nil {
			return err
		}
	} else if _, err := s.cli.ExecuteTasks(ctx, &socket.DeleteMessageTask{MessageId: msg.Key}); err != nil {
		return err
	}
	return s.write(func(w *importers.MetaWriter, a *archive.Archive) { w.Delete(conv.Key, msg.Key) }, false)
}

// markRead tells Meta the owner read a chat up to a time; how many messages it marked.
func markRead(ctx context.Context, c *plugins.Context, service string, conv plugins.Conversation, until int64) (int, error) {
	s := running(c.ID)
	if s == nil || !s.connected.Load() {
		return 0, nil
	}
	if s.ig != nil {
		return s.igMarkRead(ctx, conv, until)
	}
	q := c.Store().Read()
	type in struct {
		key, sender string
	}
	var unread []in
	err := func() (err error) {
		defer db.Recover(&err)
		db.Each(q, "SELECT m.key, coalesce(a.value, '') FROM message m LEFT JOIN address a ON a.id = m.sender_id "+
			"WHERE m.conversation_id = ? AND NOT m.outgoing AND m.key IS NOT NULL AND m.ts <= ? ORDER BY m.ts DESC LIMIT 50",
			[]any{conv.ID, until}, func(scan func(...any)) {
				var x in
				scan(&x.key, &x.sender)
				unread = append(unread, x)
			})
		return nil
	}()
	if err != nil {
		return 0, err
	}
	if s.encrypted(conv.Key) {
		e := s.waClient()
		if e == nil {
			return 0, nil
		}
		by := map[string][]string{}
		for _, x := range unread {
			by[x.sender] = append(by[x.sender], x.key)
		}
		n := 0
		for sender, ids := range by {
			var who types.JID
			if s.group(conv.Key) {
				who = types.NewJID(sender, types.MessengerServer)
			}
			if err := e.MarkRead(ctx, ids, time.Now(), waJID(conv.Key, s.group(conv.Key)), who); err != nil {
				return n, err
			}
			n += len(ids)
		}
		return n, nil
	}
	if _, err := s.cli.ExecuteTasks(ctx, &socket.ThreadMarkReadTask{ThreadId: s.fbThread(conv.Key), LastReadWatermarkTs: until,
		SyncGroup: 1}); err != nil {
		return 0, err
	}
	return len(unread), nil
}

// fetchMedia brings a message's file now (one still to download): its path in the store.
func fetchMedia(ctx context.Context, c *plugins.Context, messageID int64) (string, error) {
	q := c.Store().Read()
	if p := importers.StoredFile(q, messageID); p != "" {
		return p, nil
	}
	s := running(c.ID)
	if s == nil {
		return "", nil
	}
	key := db.Str(q, "SELECT coalesce(key, '') FROM message WHERE id = ?", messageID)
	if key == "" || s.fetchFiles(ctx, key) == 0 {
		return "", nil
	}
	return importers.StoredFile(c.Store().Read(), messageID), nil
}
