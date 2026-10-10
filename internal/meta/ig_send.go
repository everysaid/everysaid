package meta

// Sending into Instagram, reacting, editing, unsending and read receipts, through instameow (as
// mautrix-meta's pkg/igconnector/handlematrix.go and pkg/msgconv/igconv/from-matrix.go do).

import (
	"context"
	"fmt"
	"strconv"
	"time"
	"unicode/utf16"

	"go.mau.fi/util/jsontime"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"

	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

// igIDs are a thread's short and long ids, asked of Instagram where not known yet.
func (s *liveSession) igIDs(ctx context.Context, key string) (short, long string, err error) {
	if t := s.thread(key); t.IGID != "" && t.IGThread != "" {
		return t.IGID, t.IGThread, nil
	}
	ids, err := s.ig.FetchThreadID(ctx, fbid(key))
	if err != nil {
		return "", "", err
	}
	s.noteThread(key, threadInfo{IGID: ids.ShortID, IGThread: ids.LongID})
	return ids.ShortID, ids.LongID, nil
}

// igMentions are the people a text names, as Instagram takes them (offsets in UTF-16 units).
func (s *liveSession) igMentions(text string, mentions []plugins.Mention) ([]slidetypes.InputMention, []string) {
	runes := []rune(text)
	var out []slidetypes.InputMention
	var ids []string
	for _, m := range mentions {
		if m.Start < 0 || m.Start+m.Length > len(runes) {
			continue
		}
		id := fbid(db.Str(s.c.Store().Read(), "SELECT value FROM address WHERE id = ?", m.AddressID))
		if id == 0 {
			continue
		}
		out = append(out, slidetypes.InputMention{Offset: len(utf16.Encode(runes[:m.Start])),
			Length: len(utf16.Encode(runes[m.Start : m.Start+m.Length])), FBID: id})
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	return out, ids
}

// igSendText sends a text; the message's id and time.
func (s *liveSession) igSendText(ctx context.Context, key, text string, reply *plugins.Reply, mentions []plugins.Mention) (string, int64, error) {
	short, _, err := s.igIDs(ctx, key)
	attribution := "igd_web_chat_tab:in_thread"
	req := &slidetypes.SendTextRequest{OfflineThreadingID: strconv.FormatInt(methods.GenerateEpochID(), 10),
		Text: slidetypes.SensitiveString{Value: text}, SendAttribution: &attribution}
	req.Mentions, req.MentionedUserIDs = s.igMentions(text, mentions)
	if reply != nil && reply.Key != "" {
		req.ReplyToMessageID = &reply.Key
	}
	if err == nil {
		req.IGThreadIGID = &short
	} else {
		req.RecipientIGIDs = []string{key} // a chat with one person Instagram has no thread of yet
	}
	resp, err := s.ig.SendMessage(ctx, req)
	if err != nil {
		return "", 0, err
	}
	return resp.Message.ID, resp.Message.TimestampMS.UnixMilli(), nil
}

// igSend sends a message (a file, then its text as a message of its own); the ids and the time.
func (s *liveSession) igSend(ctx context.Context, key, text string, reply *plugins.Reply, mentions []plugins.Mention,
	file *plugins.File, mime string) ([]string, int64, error) {
	if file == nil {
		id, ts, err := s.igSendText(ctx, key, text, reply, mentions)
		return []string{id}, ts, err
	}
	short, _, err := s.igIDs(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	fid, err := s.ig.Upload(ctx, fbid(key), file.Filename, mime, file.Data)
	if err != nil {
		return nil, 0, fmt.Errorf("upload: %w", err)
	}
	req := &slidetypes.SendMediaRequest{AttachmentFBID: strconv.FormatInt(fid, 10), ThreadID: short,
		OfflineThreadingID: strconv.FormatInt(methods.GenerateEpochID(), 10)}
	if reply != nil && reply.Key != "" {
		req.ReplyToMessageID = &reply.Key
	}
	resp, err := s.ig.SendMedia(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	ids := []string{resp.Message.ID}
	ts := resp.Message.TimestampMS.UnixMilli()
	if text != "" {
		id, _, err := s.igSendText(ctx, key, text, nil, mentions)
		if err != nil {
			return ids, ts, err
		}
		ids = append(ids, id)
	}
	return ids, ts, nil
}

// igReact puts the owner's reaction on a message ("" takes back the one there is).
func (s *liveSession) igReact(ctx context.Context, conv plugins.Conversation, msg plugins.Ref, emoji string) error {
	short, _, err := s.igIDs(ctx, conv.Key)
	if err != nil {
		return err
	}
	in := slidetypes.ReactionInput{Emoji: emoji, MessageID: msg.Key, ReactionStatus: slidetypes.ReactionStatusCreated, ThreadID: short}
	if emoji == "" {
		in.Emoji = db.Str(s.c.Store().Read(), "SELECT coalesce(emoji, '') FROM reaction WHERE message_id = ? AND outgoing = 1", msg.ID)
		in.ReactionStatus = slidetypes.ReactionStatusDeleted
	}
	_, err = s.ig.SendReaction(ctx, &slidetypes.CreateReactionRequest{Input: in})
	return err
}

func (s *liveSession) igEdit(ctx context.Context, conv plugins.Conversation, msg plugins.Ref, text string) error {
	short, _, err := s.igIDs(ctx, conv.Key)
	if err != nil {
		return err
	}
	_, err = s.ig.EditMessage(ctx, &slidetypes.EditMessageRequest{ThreadID: short, Body: slidetypes.SensitiveString{Value: text},
		OfflineThreadingID: strconv.FormatInt(methods.GenerateEpochID(), 10), TargetMessageID: msg.Key})
	return err
}

func (s *liveSession) igUnsend(ctx context.Context, conv plugins.Conversation, msg plugins.Ref) error {
	_, long, err := s.igIDs(ctx, conv.Key)
	if err != nil {
		return err
	}
	_, err = s.ig.UnsendMessage(ctx, &slidetypes.UnsendMessageRequest{MessageID: msg.Key, SendData: slidetypes.SendData{ThreadID: long}})
	return err
}

// igMarkRead tells Instagram the owner read a chat up to its last message from the others until then.
func (s *liveSession) igMarkRead(ctx context.Context, conv plugins.Conversation, until int64) (int, error) {
	var key string
	var ts int64
	if !db.Row(s.c.Store().Read(), "SELECT key, ts FROM message WHERE conversation_id = ? AND NOT outgoing AND key IS NOT NULL "+
		"AND ts <= ? ORDER BY ts DESC LIMIT 1", []any{conv.ID, until}, &key, &ts) {
		return 0, nil
	}
	_, long, err := s.igIDs(ctx, conv.Key)
	if err != nil {
		return 0, err
	}
	empty := ""
	if _, err := s.ig.MarkRead(ctx, &slidetypes.MarkReadRequest{Metadata: slidetypes.MarkReadMetadata{IGThreadIGID: long},
		Data: slidetypes.MarkReadData{MessageID: key, ItemID: &empty}}); err != nil {
		return 0, err
	}
	if _, err := s.ig.MarkReadValidation(ctx, &slidetypes.MarkReadRequest{Metadata: slidetypes.MarkReadMetadata{IGThreadIGID: long},
		Data: slidetypes.MarkReadData{MessageID: key, MessageTimestampMS: jsontime.UnixMilliString{Time: time.UnixMilli(ts)}}}); err != nil {
		return 0, err
	}
	return 1, nil
}
