package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"

	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

// Forwarded is what became of a message forwarded to one chat: forwarded by its service's own
// forward, sent there as a message of its own (another service), kept to go when it can, or not.
type Forwarded struct {
	Chat      string          `json:"chat"`
	Service   string          `json:"service,omitempty"`
	Forwarded bool            `json:"forwarded"`
	Queued    bool            `json:"queued,omitempty"`
	Error     *errs.UserError `json:"-"`
}

// ForwardTarget is a chat to forward to, through a service ("": the message's own where the chat has
// it, else the way the chat is written to).
type ForwardTarget struct {
	Chat, Service string
}

// Forward sends a message on to chats. Where a chat has a conversation of the message's own service
// (asked for, or none asked) and a source there forwards, it goes as that service forwards (marked
// forwarded); elsewhere as a message of its own, its text and files, through the service asked for or
// the way the chat is written to (kept to go later where it cannot now, as anything sent).
func (h *Host) Forward(ctx context.Context, messageID int64, targets []ForwardTarget) (out []Forwarded, err error) {
	defer db.Recover(&err)
	q := h.store.Read()
	var conv, ts int64
	var outgoing, deleted bool
	var key, convKey, text sql.NullString
	var svc string
	if !db.Row(q, "SELECT m.conversation_id, m.ts, m.outgoing, m.deleted, m.key, c.key, m.text, s.name FROM message m "+
		"JOIN conversation c ON c.id = m.conversation_id JOIN service s ON s.id = c.service_id WHERE m.id = ?",
		[]any{messageID}, &conv, &ts, &outgoing, &deleted, &key, &convKey, &text, &svc) {
		return nil, core.ErrNotFound
	}
	if deleted {
		return nil, errs.New("message.deleted", 409, nil)
	}
	var files []fileOf
	db.Each(q, "SELECT a.sha256, coalesce(a.name, ''), coalesce(m.mime, '') FROM attachment a JOIN media m ON m.sha256 = a.sha256 "+
		"WHERE a.message_id = ? ORDER BY a.id", []any{messageID}, func(scan func(...any)) {
		var f fileOf
		scan(&f.sha, &f.name, &f.mime)
		files = append(files, f)
	})
	if text.String == "" && len(files) == 0 {
		return nil, errs.New("message.cannot_forward", 409, nil)
	}

	// by the service's own forward: the chats with a conversation of it, through one source
	var fw plugins.Forwarder
	var fwIID int64
	if key.String != "" {
		for _, s := range h.Senders() {
			if f, ok := s.p.(plugins.Forwarder); ok && contains(s.p.Info().Services, svc) {
				fw, fwIID = f, s.iid
				break
			}
		}
	}
	var native []plugins.Conversation
	var nativeChats []int
	for _, tg := range targets {
		r := Forwarded{Chat: tg.Chat}
		c := core.ChatOf(h.store, tg.Chat)
		if c == nil {
			r.Error = errs.New("not_found", 404, nil)
			out = append(out, r)
			continue
		}
		if fw != nil && (tg.Service == "" || tg.Service == svc) {
			if to, ok := h.conversationIn(c.Conversations, svc); ok {
				native = append(native, to)
				nativeChats = append(nativeChats, len(out))
				r.Service, r.Forwarded = svc, true
				out = append(out, r)
				continue
			}
		}
		h.forwardCopy(ctx, &r, tg.Service, text.String, files)
		out = append(out, r)
	}
	if len(native) > 0 {
		pc, err := h.Ctx(fwIID)
		if err == nil {
			from := plugins.Conversation{ID: conv, Key: convKey.String, Service: svc}
			err = fw.Forward(ctx, pc, from, []plugins.Ref{{ID: messageID, Key: key.String, Outgoing: outgoing, TS: ts}}, native)
		}
		if err != nil {
			for _, i := range nativeChats {
				out[i].Forwarded, out[i].Error = false, userError(err)
			}
		}
	}
	h.Emit(M{"type": "changed"})
	return out, nil
}

// fileOf is a file of a message: its media, its name, its type.
type fileOf struct{ sha, name, mime string }

// conversationIn is the conversation of the service among these (the one last active).
func (h *Host) conversationIn(convs []int64, svc string) (plugins.Conversation, bool) {
	q := h.store.Read()
	var best plugins.Conversation
	var bestTS int64 = -1
	for _, id := range convs {
		var key sql.NullString
		var s string
		if !db.Row(q, "SELECT c.key, s.name FROM conversation c JOIN service s ON s.id = c.service_id WHERE c.id = ?", []any{id}, &key, &s) ||
			s != svc || key.String == "" {
			continue
		}
		if last := db.Int(q, "SELECT coalesce(max(ts), 0) FROM message WHERE conversation_id = ?", id); last > bestTS {
			best, bestTS = plugins.Conversation{ID: id, Key: key.String, Service: s}, last
		}
	}
	return best, bestTS >= 0
}

// forwardCopy sends the message to a chat as one of its own, through the service ("" the chat's
// way): the text with the first file (its caption), each other file after it.
func (h *Host) forwardCopy(ctx context.Context, r *Forwarded, service, text string, files []fileOf) {
	send := func(text string, f *plugins.File) bool {
		res, err := h.SendKept(ctx, r.Chat, newOutboxID(), SendRequest{Text: text, Service: service, File: f})
		var qd *queued
		switch {
		case errors.As(err, &qd):
			r.Queued = true
		case err != nil:
			r.Error = userError(err)
			return false
		}
		if s, _ := res["service"].(string); s != "" {
			r.Service = s
		}
		return true
	}
	if len(files) == 0 {
		send(text, nil)
		return
	}
	for i, f := range files {
		data, err := h.fileData(f.sha)
		if err != nil {
			r.Error = errs.New("file_gone", 404, nil)
			return
		}
		caption := ""
		if i == 0 {
			caption = text
		}
		if !send(caption, &plugins.File{Data: data, Filename: f.name, MimeType: f.mime}) {
			return
		}
	}
}

// fileData is a file of the archive: kept here, or from the library that holds it.
func (h *Host) fileData(sha string) ([]byte, error) {
	if p := h.LocalFile(sha); p != "" {
		return os.ReadFile(p)
	}
	got := h.Fetch(sha, "original")
	if got == nil {
		return nil, os.ErrNotExist
	}
	if got.Path != "" {
		return os.ReadFile(got.Path)
	}
	return got.Data, nil
}

// userError is a failure as the user is told it (a plugin's own words where it did not word it).
func userError(err error) *errs.UserError {
	ue := &errs.UserError{Code: "plugin", Status: 500, Text: err.Error()}
	errors.As(err, &ue)
	return ue
}

// newOutboxID is an id for a message the server sends of itself, kept in the outbox as the app's are.
func newOutboxID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
