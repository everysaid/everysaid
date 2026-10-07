package signal

// New (the Python never had Signal). `<cache>/signal/<instance>/signal.db`: what the helper said,
// kept as it said it, the way the Telegram live connection keeps telegram.db and the WhatsApp bridge
// messages.db; the importer (import.go) reads it into the archive. Facts only (a message, an edit,
// a deletion, a reaction, a receipt, a read, a call): what they mean together is the importer's.
// It can be made again from the helper's own store (the `history` command).

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"everysaid/internal/db"
)

const storeSchema = `
CREATE TABLE IF NOT EXISTS account (key TEXT PRIMARY KEY, value TEXT);   -- aci, pni, phone, device_id
CREATE TABLE IF NOT EXISTS contact (
    aci TEXT PRIMARY KEY,
    phone TEXT,                         -- E.164, where Signal shows it
    name TEXT,                          -- as the phone's address book has them
    profile_name TEXT,                  -- as they named themselves
    seen_at INTEGER NOT NULL            -- Unix s
);
CREATE TABLE IF NOT EXISTS pni (       -- someone's PNI (the id of their number), as Signal told it
    pni TEXT PRIMARY KEY,               -- "PNI:<uuid>"
    phone TEXT,                         -- E.164: the phone said it, writing to them
    aci TEXT,                           -- their ACI: they signed with the PNI, writing from it
    seen_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS grp (
    id TEXT PRIMARY KEY,                -- the group's id (base64), not its master key
    title TEXT,
    description TEXT,
    revision INTEGER,
    seen_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS group_member (
    group_id TEXT NOT NULL,
    aci TEXT NOT NULL,
    PRIMARY KEY (group_id, aci)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS message (
    author TEXT NOT NULL,               -- who wrote it (the account's own ACI for the owner's)
    ts INTEGER NOT NULL,                -- when it was sent, Unix ms: with the author, Signal's name for it
    chat_kind TEXT NOT NULL,            -- contact, group
    chat TEXT NOT NULL,                 -- the other person's ACI, or the group's id
    outgoing INTEGER NOT NULL,
    server_ts INTEGER,
    json TEXT NOT NULL,                 -- the helper's event, whole
    PRIMARY KEY (author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS edit (
    author TEXT NOT NULL,
    target_ts INTEGER NOT NULL,         -- the message edited (its first sent time)
    ts INTEGER NOT NULL,                -- the edit's own
    json TEXT NOT NULL,
    PRIMARY KEY (author, target_ts, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS deletion (   -- deleted for everyone by its author (or a group's admin)
    author TEXT NOT NULL,
    ts INTEGER NOT NULL,
    at INTEGER NOT NULL,
    PRIMARY KEY (author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS reaction (   -- the latest of each person on each message
    target_author TEXT NOT NULL,
    target_ts INTEGER NOT NULL,
    sender TEXT NOT NULL,
    emoji TEXT,                         -- NULL: taken back
    ts INTEGER NOT NULL,
    PRIMARY KEY (target_author, target_ts, sender)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS receipt (    -- the others' receipts of the owner's messages
    ts INTEGER NOT NULL,                -- the owner's message
    aci TEXT NOT NULL,
    kind TEXT NOT NULL,                 -- delivery, read, viewed
    at INTEGER NOT NULL,                -- Unix ms
    PRIMARY KEY (ts, aci, kind)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS read (       -- the others' messages the owner read, on any device
    author TEXT NOT NULL,
    ts INTEGER NOT NULL,
    at INTEGER NOT NULL,                -- Unix ms
    PRIMARY KEY (author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS late_file (  -- messages whose files came after the message (fetched again)
    author TEXT NOT NULL,
    ts INTEGER NOT NULL,
    PRIMARY KEY (author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS fetch_try (  -- files that failed, asked again this many times
    author TEXT NOT NULL,
    ts INTEGER NOT NULL,
    tries INTEGER NOT NULL,
    PRIMARY KEY (author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS call (
    id TEXT PRIMARY KEY,                -- Signal's call id
    chat_kind TEXT,
    chat TEXT,
    ts INTEGER NOT NULL,                -- when it began, Unix ms
    outgoing INTEGER,
    video INTEGER NOT NULL DEFAULT 0,
    accepted INTEGER,                   -- NULL: not known (yet)
    result TEXT,                        -- the phone's word: accepted, not_accepted
    hangup TEXT,                        -- how it ended: normal, declined, busy, ...
    ended_at INTEGER                    -- Unix ms
);
`

// Store is signal.db, written by the plugin as the helper speaks.
type Store struct {
	DB   *sql.DB
	Path string
}

// OpenStore opens (making it if new) signal.db at path.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	d, err := db.Open(path, "journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	if _, err := d.Exec(storeSchema); err != nil {
		d.Close()
		return nil, err
	}
	return &Store{DB: d, Path: path}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// Account records the account's ids, as the helper says them.
func (s *Store) Account(st Status) {
	for k, v := range map[string]string{"aci": st.ACI, "pni": st.PNI, "phone": st.Phone} {
		if v != "" {
			db.Exec(s.DB, "INSERT OR REPLACE INTO account VALUES (?, ?)", k, v)
		}
	}
}

// Own is the account's ACI ("" before it was linked).
func (s *Store) Own() string { return db.Str(s.DB, "SELECT value FROM account WHERE key = 'aci'") }

type chatRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type mentionEv struct {
	Start  int    `json:"start"`
	Length int    `json:"length"`
	ACI    string `json:"aci"`
}

type quoteEv struct {
	TS     int64   `json:"ts"`
	Author *string `json:"author"`
	Text   *string `json:"text"`
}

type attachmentEv struct {
	ContentType *string `json:"content_type"`
	Filename    *string `json:"filename"`
	Size        *int64  `json:"size"`
	Voice       bool    `json:"voice"`
	Gif         bool    `json:"gif"`
	Sticker     bool    `json:"sticker"`
	Caption     *string `json:"caption"`
	File        *string `json:"file"`
	Error       string  `json:"error"`
}

type contactEv struct {
	ACI         string  `json:"aci"`
	Phone       *string `json:"phone"`
	Name        *string `json:"name"`
	ProfileName *string `json:"profile_name"`
}

type sharedContact struct {
	Name         *string  `json:"name"`
	Phones       []string `json:"phones"`
	Emails       []string `json:"emails"`
	Organization *string  `json:"organization"`
}

type pollEv struct {
	Question *string  `json:"question"`
	Options  []string `json:"options"`
}

type ref struct {
	Author string `json:"author"`
	TS     int64  `json:"ts"`
}

// event is any of the helper's events, the fields each has.
type event struct {
	Event    string   `json:"event"`
	Chat     *chatRef `json:"chat"`
	Sender   string   `json:"sender"`
	Outgoing bool     `json:"outgoing"`
	TS       int64    `json:"ts"`
	ServerTS int64    `json:"server_ts"`

	// a message (or an edit's new content)
	Text          *string         `json:"text"`
	Mentions      []mentionEv     `json:"mentions"`
	Quote         *quoteEv        `json:"quote"`
	Attachments   []attachmentEv  `json:"attachments"`
	Contacts      []sharedContact `json:"contacts"`
	Previews      []any           `json:"previews"`
	Poll          *pollEv         `json:"poll"`
	GroupChange   bool            `json:"group_change"`
	GroupCall     bool            `json:"group_call"`
	ExpireUpdate  bool            `json:"expire_timer_update"`
	ExpireTimer   int64           `json:"expire_timer"`
	Forwarded     bool            `json:"forwarded"`
	ViewOnce      bool            `json:"view_once"`
	PinRaw        json.RawMessage `json:"pin"`
	UnpinRaw      json.RawMessage `json:"unpin"`
	Payment       bool            `json:"payment"`
	Gift          bool            `json:"gift"`
	StoryReply    bool            `json:"story_reply"`
	GroupRevision *int64          `json:"group_revision"`

	// an edit, a deletion, a reaction
	TargetAuthor *string `json:"target_author"`
	TargetTS     *int64  `json:"target_ts"`
	Emoji        *string `json:"emoji"`
	Remove       bool    `json:"remove"`

	// a receipt, the owner's reading
	Kind       string  `json:"kind"`
	Timestamps []int64 `json:"timestamps"`
	Messages   []ref   `json:"messages"`

	// a call (from a call message, or the phone's call log); a group's id
	Source    string  `json:"source"`
	ID        *string `json:"id"`
	Action    string  `json:"action"`
	Hangup    string  `json:"hangup"`
	Type      string  `json:"type"`
	Direction string  `json:"direction"`
	Result    string  `json:"result"`
	Video     bool    `json:"video"`

	// one person's ids (and a person seen, a contact)
	ACI   string  `json:"aci"`
	PNI   string  `json:"pni"`
	Phone *string `json:"phone"`

	// contacts, a group
	ContactList []contactEv `json:"-"`
	Title       *string     `json:"title"`
	Description *string     `json:"description"`
	Revision    *int64      `json:"revision"`
	Members     []string    `json:"members"`

	// the receive loop's end
	Error *string `json:"error"`
}

func decodeEvent(raw []byte) (event, error) {
	var e event
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, err
	}
	switch e.Event {
	case "contacts":
		var c struct {
			Contacts []contactEv `json:"contacts"`
		}
		json.Unmarshal(raw, &c)
		e.ContactList, e.Contacts = c.Contacts, nil
	case "contact": // one person, seen in a message
		var c contactEv
		json.Unmarshal(raw, &c)
		e.ContactList = []contactEv{c}
	}
	return e, nil
}

// Apply keeps an event; it says whether it was one to keep (the archive has something new to read).
func (s *Store) Apply(raw []byte) (kept bool, err error) {
	defer db.Recover(&err)
	e, err := decodeEvent(raw)
	if err != nil {
		return false, err
	}
	now := time.Now().Unix()
	switch e.Event {
	case "message":
		if e.Chat == nil {
			return false, nil
		}
		// a copy seen again (history, a fetch again) may have its files now; what was kept of it
		// stays otherwise
		if hasFile(e) && db.Int(s.DB, "SELECT count(*) FROM message WHERE author = ? AND ts = ?", e.Sender, e.TS) > 0 {
			db.Exec(s.DB, "INSERT OR IGNORE INTO late_file VALUES (?, ?)", e.Sender, e.TS)
		}
		db.Exec(s.DB, "INSERT INTO message (author, ts, chat_kind, chat, outgoing, server_ts, json) VALUES (?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT (author, ts) DO UPDATE SET json = excluded.json", e.Sender, e.TS, e.Chat.Kind, e.Chat.ID,
			db.B(e.Outgoing), e.ServerTS, string(raw))
	case "edit":
		if e.TargetTS == nil {
			return false, nil
		}
		db.Exec(s.DB, "INSERT OR REPLACE INTO edit VALUES (?, ?, ?, ?)", e.Sender, *e.TargetTS, e.TS, string(raw))
	case "delete":
		if e.TargetTS == nil || e.TargetAuthor == nil {
			return false, nil
		}
		db.Exec(s.DB, "INSERT OR IGNORE INTO deletion VALUES (?, ?, ?)", *e.TargetAuthor, *e.TargetTS, e.TS)
	case "reaction":
		if e.TargetTS == nil || e.TargetAuthor == nil {
			return false, nil
		}
		var emoji any
		if !e.Remove && e.Emoji != nil {
			emoji = *e.Emoji
		}
		db.Exec(s.DB, "INSERT INTO reaction VALUES (?, ?, ?, ?, ?) ON CONFLICT DO UPDATE SET "+
			"emoji = excluded.emoji, ts = excluded.ts WHERE excluded.ts >= reaction.ts",
			*e.TargetAuthor, *e.TargetTS, e.Sender, emoji, e.TS)
	case "receipt":
		for _, ts := range e.Timestamps {
			db.Exec(s.DB, "INSERT OR IGNORE INTO receipt VALUES (?, ?, ?, ?)", ts, e.Sender, e.Kind, e.TS)
		}
	case "read":
		for _, m := range e.Messages {
			db.Exec(s.DB, "INSERT OR IGNORE INTO read VALUES (?, ?, ?)", m.Author, m.TS, e.TS)
		}
	case "call":
		return s.call(e), nil
	case "contacts", "contact":
		// the book's name and the number stay where presage no longer has them (it names those it
		// saw in a message after their profile, without their number)
		for _, c := range e.ContactList {
			if c.ACI == "" {
				continue
			}
			db.Exec(s.DB, "INSERT INTO contact VALUES (?, ?, ?, ?, ?) ON CONFLICT (aci) DO UPDATE SET "+
				"phone = coalesce(excluded.phone, contact.phone), name = coalesce(excluded.name, contact.name), "+
				"profile_name = coalesce(excluded.profile_name, contact.profile_name), seen_at = excluded.seen_at",
				c.ACI, ptr(c.Phone), ptr(c.Name), ptr(c.ProfileName), now)
		}
	case "ids":
		// a number written to, from the phone, is kept under its PNI: what ties it to the person
		phone := ptr(e.Phone)
		if deref(e.Phone) == "" {
			phone = nil
		}
		var aci any
		if e.ACI != "" {
			aci = e.ACI
		}
		switch {
		case e.PNI != "":
			db.Exec(s.DB, "INSERT INTO pni VALUES (?, ?, ?, ?) ON CONFLICT (pni) DO UPDATE SET "+
				"phone = coalesce(excluded.phone, pni.phone), aci = coalesce(excluded.aci, pni.aci), seen_at = excluded.seen_at",
				e.PNI, phone, aci, now)
		case e.ACI != "" && phone != nil:
			db.Exec(s.DB, "INSERT INTO contact (aci, phone, seen_at) VALUES (?, ?, ?) ON CONFLICT (aci) DO UPDATE SET "+
				"phone = excluded.phone, seen_at = excluded.seen_at", e.ACI, phone, now)
		default:
			return false, nil
		}
	case "group":
		if e.ID == nil {
			return false, nil
		}
		db.Exec(s.DB, "INSERT OR REPLACE INTO grp VALUES (?, ?, ?, ?, ?)", *e.ID, ptr(e.Title), ptr(e.Description),
			ptrI(e.Revision), now)
		db.Exec(s.DB, "DELETE FROM group_member WHERE group_id = ?", *e.ID)
		for _, m := range e.Members {
			db.Exec(s.DB, "INSERT OR IGNORE INTO group_member VALUES (?, ?)", *e.ID, m)
		}
	default:
		return false, nil
	}
	return true, nil
}

// call: a call message (offer, answer, busy, hangup) or the phone's record of the call.
func (s *Store) call(e event) bool {
	if e.ID == nil || *e.ID == "" {
		return false
	}
	id := *e.ID
	if e.Source == "sync" {
		var kind, chat any
		if e.Chat != nil {
			kind, chat = e.Chat.Kind, e.Chat.ID
		}
		var outgoing, accepted any
		switch e.Direction {
		case "incoming":
			outgoing = 0
		case "outgoing":
			outgoing = 1
		}
		switch e.Result {
		case "accepted":
			accepted = 1
		case "not_accepted":
			accepted = 0
		case "delete", "observed", "unknown": // the call log changed, not the call
			db.Exec(s.DB, "INSERT OR IGNORE INTO call (id, chat_kind, chat, ts, video) VALUES (?, ?, ?, ?, ?)",
				id, kind, chat, e.TS, db.B(e.Type == "video"))
			return true
		}
		db.Exec(s.DB, "INSERT INTO call (id, chat_kind, chat, ts, outgoing, video, accepted, result) VALUES (?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT (id) DO UPDATE SET chat_kind = coalesce(excluded.chat_kind, call.chat_kind), "+
			"chat = coalesce(excluded.chat, call.chat), ts = excluded.ts, outgoing = coalesce(excluded.outgoing, call.outgoing), "+
			"video = max(call.video, excluded.video), accepted = coalesce(excluded.accepted, call.accepted), result = excluded.result",
			id, kind, chat, e.TS, outgoing, db.B(e.Type == "video"), accepted, e.Result)
		return true
	}
	if e.Chat == nil {
		return false
	}
	switch e.Action {
	case "offer":
		db.Exec(s.DB, "INSERT INTO call (id, chat_kind, chat, ts, outgoing, video) VALUES (?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT (id) DO UPDATE SET video = max(call.video, excluded.video)",
			id, e.Chat.Kind, e.Chat.ID, e.TS, db.B(e.Outgoing), db.B(e.Video))
	case "answer": // they took the owner's call
		db.Exec(s.DB, "INSERT INTO call (id, chat_kind, chat, ts, outgoing, accepted) VALUES (?, ?, ?, ?, 1, 1) "+
			"ON CONFLICT (id) DO UPDATE SET accepted = 1", id, e.Chat.Kind, e.Chat.ID, e.TS)
	case "busy", "hangup":
		how := e.Hangup
		if e.Action == "busy" {
			how = "busy"
		}
		db.Exec(s.DB, "INSERT INTO call (id, chat_kind, chat, ts, hangup, ended_at) VALUES (?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT (id) DO UPDATE SET hangup = coalesce(call.hangup, excluded.hangup), "+
			"ended_at = max(coalesce(call.ended_at, 0), excluded.ended_at)", id, e.Chat.Kind, e.Chat.ID, e.TS, how, e.TS)
	default:
		return false
	}
	return true
}

func hasFile(e event) bool {
	for _, a := range e.Attachments {
		if deref(a.File) != "" {
			return true
		}
	}
	return false
}

// fetchTries is how many times the files of a message are asked again; fetchDays, for how long
// after it was sent (Signal's server keeps them for a while, then they are gone).
const (
	fetchTries = 3
	fetchDays  = 30
)

// FailedFiles are the messages (sent after since, Unix ms) with a file that failed to come and is
// still to be asked again; each is counted as asked once more.
func (s *Store) FailedFiles(since int64) []ref {
	var out []ref
	db.Each(s.DB, "SELECT m.author, m.ts, m.json FROM message m LEFT JOIN fetch_try f ON f.author = m.author AND f.ts = m.ts "+
		"WHERE m.ts >= ? AND m.json LIKE '%\"error\":%' AND coalesce(f.tries, 0) < ? ORDER BY m.ts",
		[]any{since, fetchTries}, func(scan func(...any)) {
			var r ref
			var js string
			scan(&r.Author, &r.TS, &js)
			var e event
			if json.Unmarshal([]byte(js), &e) != nil {
				return
			}
			for _, a := range e.Attachments {
				if a.Error != "" && deref(a.File) == "" {
					out = append(out, r)
					return
				}
			}
		})
	for _, r := range out {
		db.Exec(s.DB, "INSERT INTO fetch_try VALUES (?, ?, 1) ON CONFLICT DO UPDATE SET tries = tries + 1", r.Author, r.TS)
	}
	return out
}

// PNIs are the PNIs of the people signal.db has a chat with or was told of: chats with numbers the
// phone wrote to, which the helper's store does not list among its contacts.
func (s *Store) PNIs() []string {
	return append([]string{}, db.Strs(s.DB, "SELECT chat FROM message WHERE chat_kind = 'contact' AND chat LIKE 'PNI:%' UNION "+
		"SELECT pni FROM pni ORDER BY 1")...) // a list, never null
}

// Newest is the newest message's time (Unix ms), 0 for none.
func (s *Store) Newest() int64 { return db.Int(s.DB, "SELECT coalesce(max(ts), 0) FROM message") }

// Unread are the others' messages of a chat at or before until (Unix ms) the owner has not read.
func (s *Store) Unread(chat string, until int64) []ref {
	var out []ref
	db.Each(s.DB, "SELECT author, ts FROM message m WHERE chat = ? AND NOT outgoing AND ts <= ? "+
		"AND NOT EXISTS (SELECT 1 FROM read r WHERE r.author = m.author AND r.ts = m.ts) ORDER BY ts",
		[]any{chat, until}, func(scan func(...any)) {
			var r ref
			scan(&r.Author, &r.TS)
			out = append(out, r)
		})
	return out
}

// Read records messages the owner read (here), at Unix ms.
func (s *Store) Read(refs []ref, at int64) {
	for _, r := range refs {
		db.Exec(s.DB, "INSERT OR IGNORE INTO read VALUES (?, ?, ?)", r.Author, r.TS, at)
	}
}

// Revisions are the times a message has been known by: its own (ts) and its author's edits of it,
// oldest first. Signal's apps aim an edit (and a reaction, a deletion) at the newest; some at the
// first, so each edit is followed from any of them.
func (s *Store) Revisions(author string, ts int64) []int64 {
	next := map[int64][]int64{}
	db.Each(s.DB, "SELECT target_ts, ts FROM edit WHERE author = ? ORDER BY ts", []any{author}, func(scan func(...any)) {
		var target, t int64
		scan(&target, &t)
		next[target] = append(next[target], t)
	})
	out, seen := []int64{ts}, map[int64]bool{ts: true}
	for i := 0; i < len(out); i++ {
		for _, t := range next[out[i]] {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	slices.Sort(out)
	return out
}

// MyReaction is the owner's reaction on a message (any of its times) as signal.db last has it: its
// emoji ("" taken back), and whether there was one at all.
func (s *Store) MyReaction(own, author string, times []int64) (string, bool) {
	var emoji *string
	args := append([]any{author, own}, db.Args(times)...)
	if !db.Row(s.DB, "SELECT emoji FROM reaction WHERE target_author = ? AND sender = ? AND target_ts IN ("+
		db.Marks(len(times))+") ORDER BY ts DESC LIMIT 1", args, &emoji) {
		return "", false
	}
	return deref(emoji), true
}

// Message is a message's event as signal.db keeps it (false: not there).
func (s *Store) Message(author string, ts int64) (event, bool) {
	var js string
	var e event
	if !db.Row(s.DB, "SELECT json FROM message WHERE author = ? AND ts = ?", []any{author, ts}, &js) ||
		json.Unmarshal([]byte(js), &e) != nil {
		return e, false
	}
	return e, true
}

// Deleted: the message (any of its times) was deleted for everyone.
func (s *Store) Deleted(author string, times []int64) bool {
	return db.Int(s.DB, "SELECT count(*) FROM deletion WHERE author = ? AND ts IN ("+db.Marks(len(times))+")",
		append([]any{author}, db.Args(times)...)...) > 0
}

func ptr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func ptrI(i *int64) any {
	if i == nil {
		return nil
	}
	return *i
}
