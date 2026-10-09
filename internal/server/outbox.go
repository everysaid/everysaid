// The outbox: a message that could not go now for a passing reason (the service's connection down,
// a limit of pace, no answer in time) is kept and sent again by itself, in the order written, until
// it goes or a day has passed. Each message has the app's own id: asked again with it (the answer
// lost on the way), it is not sent twice; and before each new try the chat is looked at, in case it
// went after all (a source that said it failed, though it sent).
//
// It is kept in a database of its own beside the archive (<archive>-outbox.db): what waits to be
// sent, not the archive's history.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
)

const outboxSchema = `
CREATE TABLE IF NOT EXISTS outbox (
    id TEXT PRIMARY KEY,            -- the app's own id of the message
    chat TEXT NOT NULL,
    request TEXT NOT NULL,          -- JSON: text, conversation_id, service, reply_to, mentions, file_name, file_type
    file BLOB,
    created_at INTEGER NOT NULL,    -- Unix ms
    state TEXT NOT NULL,            -- queued, sending, sent, failed
    attempts INTEGER NOT NULL DEFAULT 0,
    next_at INTEGER NOT NULL DEFAULT 0,
    error TEXT,                     -- JSON: the last failure (code, status, text, params)
    result TEXT                     -- JSON: what the sending said, once sent
);
CREATE INDEX IF NOT EXISTS outbox_chat ON outbox (chat, state);
`

// how long a message is tried for, and the pauses between tries (doubling up to the last)
const (
	outboxFor  = 24 * time.Hour
	firstPause = 15 * time.Second
	lastPause  = 10 * time.Minute
)

// queued is what a message's sending said when it was kept: why it could not go now (nil: others of
// its chat wait before it).
type queued struct {
	ID    string
	Cause error
}

func (q *queued) Error() string {
	if q.Cause == nil {
		return "queued"
	}
	return "queued: " + q.Cause.Error()
}

// outboxRequest is a SendRequest as kept (the file apart).
type outboxRequest struct {
	Text           string            `json:"text"`
	ConversationID int64             `json:"conversation_id,omitempty"`
	Service        string            `json:"service,omitempty"`
	ReplyTo        int64             `json:"reply_to,omitempty"`
	Mentions       []plugins.Mention `json:"mentions,omitempty"`
	FileName       string            `json:"file_name,omitempty"`
	FileType       string            `json:"file_type,omitempty"`
}

// outboxDB is the outbox's database, opened at its first use.
func (h *Host) outboxDB() *sql.DB {
	h.outboxOnce.Do(func() {
		p := h.store.Path
		path := strings.TrimSuffix(p, filepath.Ext(p)) + "-outbox.db"
		d, err := db.Open(path, "journal_mode(WAL)")
		if err == nil {
			_, err = d.Exec(outboxSchema)
		}
		if err != nil {
			panic(err)
		}
		d.SetMaxOpenConns(1)
		h.outbox = d
	})
	return h.outbox
}

// transient says whether a failure to send may pass: the service's connection down (503), a limit
// (429), no answer (502, 504), or a failure the source did not word (a timeout, the network: not a
// refusal). Refusals (not a member, cannot answer, blocked) are not tried again.
func transient(err error) bool {
	var ue *errs.UserError
	if errors.As(err, &ue) {
		switch ue.Status {
		case 429, 502, 503, 504:
			return true
		}
		return false
	}
	return err != nil
}

// errJSON is a failure as kept, to be said later in the user's words.
func errJSON(err error) string {
	ue := &errs.UserError{Code: "plugin", Status: 500, Text: err.Error()}
	errors.As(err, &ue)
	b, _ := json.Marshal(map[string]any{"code": ue.Code, "status": ue.Status, "text": ue.Text, "params": ue.Params})
	return string(b)
}

func errOf(s string) *errs.UserError {
	var e struct {
		Code   string         `json:"code"`
		Status int            `json:"status"`
		Text   string         `json:"text"`
		Params map[string]any `json:"params"`
	}
	if json.Unmarshal([]byte(s), &e) != nil || e.Code == "" {
		return nil
	}
	return &errs.UserError{Code: e.Code, Status: e.Status, Text: e.Text, Params: e.Params}
}

// SendKept sends as Send does, keeping the message in the outbox where it cannot go now for a passing
// reason (a *queued error then) or where the chat has others waiting before it. id: the app's own id
// of the message ("" for none: not kept); one already sent with it is not sent again.
func (h *Host) SendKept(ctx context.Context, chatID, id string, r SendRequest) (M, error) {
	if id == "" {
		return h.Send(ctx, chatID, r)
	}
	o := h.outboxDB()
	var state string
	var result sql.NullString
	if db.Row(o, "SELECT state, result FROM outbox WHERE id = ?", []any{id}, &state, &result) {
		switch state {
		case "sent":
			var out M
			json.Unmarshal([]byte(result.String), &out)
			return out, nil
		case "queued", "sending":
			return nil, &queued{ID: id}
		}
		db.Exec(o, "DELETE FROM outbox WHERE id = ? AND state = 'failed'", id) // tried again by the user
	}
	kept := outboxRequest{Text: r.Text, ConversationID: r.ConversationID, Service: r.Service, ReplyTo: r.ReplyTo, Mentions: r.Mentions}
	var file []byte
	if r.File != nil {
		kept.FileName, kept.FileType, file = r.File.Filename, r.File.MimeType, r.File.Data
	}
	req, _ := json.Marshal(kept)
	now := time.Now().UnixMilli()
	// behind others of the chat still waiting: after them, in order
	if db.Exists(o, "SELECT 1 FROM outbox WHERE chat = ? AND state IN ('queued', 'sending')", chatID) {
		db.Exec(o, "INSERT INTO outbox (id, chat, request, file, created_at, state) VALUES (?, ?, ?, ?, ?, 'queued')",
			id, chatID, string(req), file, now)
		h.outboxChanged(chatID)
		return nil, &queued{ID: id}
	}
	db.Exec(o, "INSERT INTO outbox (id, chat, request, file, created_at, state, attempts) VALUES (?, ?, ?, ?, ?, 'sending', 1)",
		id, chatID, string(req), file, now)
	out, err := h.Send(ctx, chatID, r)
	if err != nil && h.arrived(chatID, kept, now) { // said failed, though it went (as Viber Desktop may)
		out, err = M{"service": r.Service, "conversation_id": 0, "messages": []M{}, "result": nil}, nil
	}
	switch {
	case err == nil:
		b, _ := json.Marshal(out)
		db.Exec(o, "UPDATE outbox SET state = 'sent', file = NULL, result = ? WHERE id = ?", string(b), id)
		return out, nil
	case transient(err):
		db.Exec(o, "UPDATE outbox SET state = 'queued', next_at = ?, error = ? WHERE id = ?",
			time.Now().Add(firstPause).UnixMilli(), errJSON(err), id)
		h.outboxChanged(chatID)
		return nil, &queued{ID: id, Cause: err}
	default:
		db.Exec(o, "DELETE FROM outbox WHERE id = ?", id) // refused: said now, kept by the app if it wants
		return nil, err
	}
}

// outboxChanged tells the apps a chat's outbox changed, and wakes the outbox.
func (h *Host) outboxChanged(chatID string) {
	h.Emit(M{"type": "outbox", "chat": chatID})
	h.wakeOutbox()
}

// wakeOutbox has the outbox look at what waits now (a message kept, a source connected).
func (h *Host) wakeOutbox() {
	select {
	case h.outboxWake <- struct{}{}:
	default:
	}
}

// Kept is one message of a chat's outbox, as the app shows it.
func (h *Host) Kept(chatID, lang string) []M {
	out := []M{}
	db.Each(h.outboxDB(), "SELECT id, request, created_at, state, attempts, next_at, error FROM outbox "+
		"WHERE chat = ? AND state != 'sent' ORDER BY created_at", []any{chatID}, func(scan func(...any)) {
		var id, req, state string
		var created, attempts, next int64
		var e sql.NullString
		scan(&id, &req, &created, &state, &attempts, &next, &e)
		var r outboxRequest
		json.Unmarshal([]byte(req), &r)
		m := M{"id": id, "text": r.Text, "service": r.Service, "reply_to": r.ReplyTo, "file_name": r.FileName,
			"created_at": created, "state": state, "attempts": attempts, "next_at": next, "error": nil}
		if ue := errOf(e.String); ue != nil {
			m["error"] = userErrorDetail(ue, lang)
		}
		out = append(out, m)
	})
	return out
}

// Retry puts a kept message back to be sent now (one that failed, or waits for its next try).
func (h *Host) Retry(id string) error {
	o := h.outboxDB()
	var chat string
	if !db.Row(o, "SELECT chat FROM outbox WHERE id = ? AND state IN ('queued', 'failed')", []any{id}, &chat) {
		return core.ErrNotFound
	}
	// a day from now again, for one the user tries again
	db.Exec(o, "UPDATE outbox SET state = 'queued', next_at = 0, created_at = CASE WHEN state = 'failed' THEN ? ELSE created_at END WHERE id = ?",
		time.Now().UnixMilli(), id)
	h.outboxChanged(chat)
	return nil
}

// Discard takes a kept message out of the outbox (not while it is being sent).
func (h *Host) Discard(id string) error {
	o := h.outboxDB()
	var chat string
	if !db.Row(o, "SELECT chat FROM outbox WHERE id = ? AND state IN ('queued', 'failed')", []any{id}, &chat) {
		return core.ErrNotFound
	}
	db.Exec(o, "DELETE FROM outbox WHERE id = ?", id)
	h.outboxChanged(chat)
	return nil
}

// runOutbox sends what waits, as each one's time comes (and at once when woken: a message kept, a
// source connected); one at a time, each chat's in order.
func (h *Host) runOutbox(ctx context.Context) {
	defer h.wg.Done()
	o := h.outboxDB()
	db.Exec(o, "UPDATE outbox SET state = 'queued' WHERE state = 'sending'") // stopped while sending: again
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		h.sendKept(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-h.outboxWake:
		}
	}
}

// sendKept tries each message whose time has come; a chat whose earlier one cannot go yet waits.
func (h *Host) sendKept(ctx context.Context) {
	o := h.outboxDB()
	now := time.Now()
	db.Exec(o, "DELETE FROM outbox WHERE state = 'sent' AND created_at < ?", now.Add(-outboxFor).UnixMilli())
	type row struct {
		id, chat, req  string
		file           []byte
		created, tries int64
		next           int64
	}
	var rows []row
	db.Each(o, "SELECT id, chat, request, file, created_at, attempts, next_at FROM outbox WHERE state = 'queued' ORDER BY created_at",
		nil, func(scan func(...any)) {
			var r row
			scan(&r.id, &r.chat, &r.req, &r.file, &r.created, &r.tries, &r.next)
			rows = append(rows, r)
		})
	waiting := map[string]bool{}
	for _, r := range rows {
		if ctx.Err() != nil {
			return
		}
		if waiting[r.chat] {
			continue
		}
		var k outboxRequest
		json.Unmarshal([]byte(r.req), &k)
		if h.arrived(r.chat, k, r.created) { // it went after all
			db.Exec(o, "UPDATE outbox SET state = 'sent', file = NULL, result = '{}' WHERE id = ?", r.id)
			h.outboxChanged(r.chat)
			continue
		}
		if now.Sub(time.UnixMilli(r.created)) > outboxFor {
			db.Exec(o, "UPDATE outbox SET state = 'failed' WHERE id = ?", r.id)
			h.outboxChanged(r.chat)
			lang := h.store.Language()
			h.Alert(i18n.T("A message was not sent", lang, nil), i18n.T("It could not go for a day: it waits in its chat to be sent again or discarded", lang, nil))
			waiting[r.chat] = true
			continue
		}
		if r.next > now.UnixMilli() {
			waiting[r.chat] = true
			continue
		}
		req := SendRequest{Text: k.Text, ConversationID: k.ConversationID, Service: k.Service, ReplyTo: k.ReplyTo, Mentions: k.Mentions}
		if k.FileName != "" || len(r.file) > 0 {
			req.File = &plugins.File{Data: r.file, Filename: k.FileName, MimeType: k.FileType}
		}
		db.Exec(o, "UPDATE outbox SET state = 'sending', attempts = attempts + 1 WHERE id = ?", r.id)
		h.outboxChanged(r.chat)
		sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		out, err := h.Send(sctx, r.chat, req)
		cancel()
		switch {
		case err == nil:
			b, _ := json.Marshal(out)
			db.Exec(o, "UPDATE outbox SET state = 'sent', file = NULL, result = ? WHERE id = ?", string(b), r.id)
		case transient(err) && ctx.Err() == nil:
			pause := firstPause << min(r.tries, 10)
			if pause > lastPause {
				pause = lastPause
			}
			db.Exec(o, "UPDATE outbox SET state = 'queued', next_at = ?, error = ? WHERE id = ?",
				time.Now().Add(pause).UnixMilli(), errJSON(err), r.id)
			waiting[r.chat] = true
		case ctx.Err() != nil:
			db.Exec(o, "UPDATE outbox SET state = 'queued' WHERE id = ?", r.id)
			return
		default:
			db.Exec(o, "UPDATE outbox SET state = 'failed', error = ? WHERE id = ?", errJSON(err), r.id)
			waiting[r.chat] = true
		}
		h.outboxChanged(r.chat)
	}
}

// arrived says whether a kept message is in its chat already, sent by the user since it was written
// (its text, through its service if it had one).
func (h *Host) arrived(chatID string, k outboxRequest, created int64) bool {
	c := core.ChatOf(h.store, chatID)
	if c == nil || len(c.Conversations) == 0 || strings.TrimSpace(k.Text) == "" {
		return false
	}
	q := "SELECT 1 FROM message m JOIN conversation c ON c.id = m.conversation_id JOIN service s ON s.id = c.service_id " +
		"WHERE m.outgoing AND m.text = ? AND m.ts >= ? AND m.conversation_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(c.Conversations)), ",") + ")"
	args := []any{k.Text, created - 5000}
	for _, id := range c.Conversations {
		args = append(args, id)
	}
	if k.Service != "" {
		q += " AND s.name = ?"
		args = append(args, k.Service)
	}
	return db.Exists(h.store.Read(), q, args...)
}
