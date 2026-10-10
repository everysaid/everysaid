package signal

// New (the Python never had Signal). What Signal said, kept as the client said it until the archive
// has it: keys.db's tables of the prefix signal_ (beside signalmeow's own, signalmeow_*, the device's
// keys), each row of a plugin instance, the way the Telegram live connection keeps its store there;
// the importer (import.go) reads it into the archive. Facts only (a message, an edit, a deletion, a
// reaction, a receipt, a read, a call): what they mean together is the importer's. A message stays
// only until the archive has it and it is fetchDays old (its files may still be fetched again until
// then); the people and groups stay, as the ids the archive's handles are made from.

import (
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"everysaid/internal/db"
	"everysaid/internal/keys"
)

const storeSchema = `
CREATE TABLE IF NOT EXISTS signal_account (     -- aci, pni, phone, device_id
    instance INTEGER NOT NULL,
    key TEXT NOT NULL,
    value TEXT,
    PRIMARY KEY (instance, key)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_contact (
    instance INTEGER NOT NULL,
    aci TEXT NOT NULL,
    phone TEXT,                         -- E.164, where Signal shows it
    name TEXT,                          -- as the phone's address book has them
    profile_name TEXT,                  -- as they named themselves
    seen_at INTEGER NOT NULL,           -- Unix s
    PRIMARY KEY (instance, aci)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_pni (        -- someone's PNI (the id of their number), as Signal told it
    instance INTEGER NOT NULL,
    pni TEXT NOT NULL,                  -- "PNI:<uuid>"
    phone TEXT,                         -- E.164: the phone said it, writing to them
    aci TEXT,                           -- their ACI: they signed with the PNI, writing from it
    seen_at INTEGER NOT NULL,
    PRIMARY KEY (instance, pni)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_group (
    instance INTEGER NOT NULL,
    id TEXT NOT NULL,                   -- the group's id (base64), not its master key
    title TEXT,
    description TEXT,
    revision INTEGER,
    seen_at INTEGER NOT NULL,
    PRIMARY KEY (instance, id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_group_member (
    instance INTEGER NOT NULL,
    group_id TEXT NOT NULL,
    aci TEXT NOT NULL,
    PRIMARY KEY (instance, group_id, aci)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_message (
    instance INTEGER NOT NULL,
    author TEXT NOT NULL,               -- who wrote it (the account's own ACI for the owner's)
    ts INTEGER NOT NULL,                -- when it was sent, Unix ms: with the author, Signal's name for it
    chat_kind TEXT NOT NULL,            -- contact, group
    chat TEXT NOT NULL,                 -- the other person's ACI, or the group's id
    outgoing INTEGER NOT NULL,
    server_ts INTEGER,
    json TEXT NOT NULL,                 -- the client's event, whole
    stored_at INTEGER NOT NULL,         -- when it came, Unix ms (the history transferred at link time comes old)
    PRIMARY KEY (instance, author, ts)
);                                      -- (not WITHOUT ROWID: a message's JSON is larger than such a table keeps in a page)
CREATE INDEX IF NOT EXISTS signal_message_chat ON signal_message (instance, chat, ts);
CREATE TABLE IF NOT EXISTS signal_edit (
    instance INTEGER NOT NULL,
    author TEXT NOT NULL,
    target_ts INTEGER NOT NULL,         -- the message edited (its first sent time)
    ts INTEGER NOT NULL,                -- the edit's own
    json TEXT NOT NULL,
    PRIMARY KEY (instance, author, target_ts, ts)
);
CREATE TABLE IF NOT EXISTS signal_deletion (   -- deleted for everyone by its author (or a group's admin)
    instance INTEGER NOT NULL,
    author TEXT NOT NULL,
    ts INTEGER NOT NULL,
    at INTEGER NOT NULL,
    PRIMARY KEY (instance, author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_reaction (   -- the latest of each person on each message
    instance INTEGER NOT NULL,
    target_author TEXT NOT NULL,
    target_ts INTEGER NOT NULL,
    sender TEXT NOT NULL,
    emoji TEXT,                         -- NULL: taken back
    ts INTEGER NOT NULL,
    PRIMARY KEY (instance, target_author, target_ts, sender)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_receipt (    -- the others' receipts of the owner's messages
    instance INTEGER NOT NULL,
    ts INTEGER NOT NULL,                -- the owner's message
    aci TEXT NOT NULL,
    kind TEXT NOT NULL,                 -- delivery, read, viewed
    at INTEGER NOT NULL,                -- Unix ms
    PRIMARY KEY (instance, ts, aci, kind)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_read (       -- the others' messages the owner read, on any device
    instance INTEGER NOT NULL,
    author TEXT NOT NULL,
    ts INTEGER NOT NULL,
    at INTEGER NOT NULL,                -- Unix ms
    PRIMARY KEY (instance, author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_late_file (  -- messages whose files came after the message (fetched again)
    instance INTEGER NOT NULL,
    author TEXT NOT NULL,
    ts INTEGER NOT NULL,
    PRIMARY KEY (instance, author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_fetch_try (  -- files that failed, asked again this many times
    instance INTEGER NOT NULL,
    author TEXT NOT NULL,
    ts INTEGER NOT NULL,
    tries INTEGER NOT NULL,
    PRIMARY KEY (instance, author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_fetch_at (   -- when the files of a message were last asked again, Unix s
    instance INTEGER NOT NULL,
    author TEXT NOT NULL,
    ts INTEGER NOT NULL,
    at INTEGER NOT NULL,
    PRIMARY KEY (instance, author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_call (
    instance INTEGER NOT NULL,
    id TEXT NOT NULL,                   -- Signal's call id
    chat_kind TEXT,
    chat TEXT,
    ts INTEGER NOT NULL,                -- when it began, Unix ms
    outgoing INTEGER,
    video INTEGER NOT NULL DEFAULT 0,
    accepted INTEGER,                   -- NULL: not known (yet)
    result TEXT,                        -- the phone's word: accepted, not_accepted
    hangup TEXT,                        -- how it ended: normal, declined, busy, ...
    ended_at INTEGER,                   -- Unix ms
    PRIMARY KEY (instance, id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_poll_vote (  -- each voter's newest vote in a poll
    instance INTEGER NOT NULL,
    target_author TEXT NOT NULL,
    target_ts INTEGER NOT NULL,
    voter TEXT NOT NULL,
    options TEXT NOT NULL,              -- JSON: the options' places
    vote_count INTEGER NOT NULL,        -- the voter's own count: a higher one replaces
    PRIMARY KEY (instance, target_author, target_ts, voter)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_unreadable ( -- messages that could not be decrypted (the sender was asked again)
    instance INTEGER NOT NULL,
    author TEXT NOT NULL,
    ts INTEGER NOT NULL,                -- the message's sent time
    chat_kind TEXT NOT NULL,
    chat TEXT NOT NULL,
    at INTEGER NOT NULL,                -- when it failed, Unix s
    PRIMARY KEY (instance, author, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS signal_chat_state ( -- a chat's state on the phone (Signal's storage service, the transfer at link time)
    instance INTEGER NOT NULL,
    chat TEXT NOT NULL,                 -- the person's ACI (or PNI), or the group's id
    field TEXT NOT NULL,                -- archived, pinned (0/1), muted (until, Unix ms; -1 for ever; 0 not)
    value INTEGER NOT NULL,
    at INTEGER NOT NULL,                -- when Signal said so, Unix ms
    PRIMARY KEY (instance, chat, field)
) WITHOUT ROWID;
`

// Store is the instance's part of keys.db's signal_ tables, written as the client speaks.
type Store struct {
	DB       *sql.DB
	Path     string // the keys database's
	Instance int64
}

// OpenStore opens the store of a plugin instance in the keys database at path ("" for keys.db), its
// tables made when new: a pool of its own, closed with the store. Its transactions begin as
// writers'.
func OpenStore(path string, instance int64) (*Store, error) {
	if path == "" {
		path = keys.Path()
	}
	d, err := keys.Own(path, true)
	if err != nil {
		return nil, err
	}
	if _, err := d.Exec(storeSchema); err != nil {
		d.Close()
		return nil, err
	}
	return &Store{DB: d, Path: path, Instance: instance}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// Account records the account's ids, as the client says them.
func (s *Store) Account(st Status) {
	for k, v := range map[string]string{"aci": st.ACI, "pni": st.PNI, "phone": st.Phone} {
		if v != "" {
			db.Exec(s.DB, "INSERT OR REPLACE INTO signal_account VALUES (?, ?, ?)", s.Instance, k, v)
		}
	}
}

// Own is the account's ACI ("" before it was linked).
func (s *Store) Own() string {
	return db.Str(s.DB, "SELECT value FROM signal_account WHERE instance = ? AND key = 'aci'", s.Instance)
}

// Counts are how many contacts and groups the store has.
func (s *Store) Counts() (contacts, groups int64, err error) {
	defer db.Recover(&err)
	db.Row(s.DB, "SELECT (SELECT count(*) FROM signal_contact WHERE instance = ?1), "+
		"(SELECT count(*) FROM signal_group WHERE instance = ?1)", []any{s.Instance}, &contacts, &groups)
	return contacts, groups, nil
}

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
	TS          int64          `json:"ts"`
	Author      *string        `json:"author"`
	Text        *string        `json:"text"`
	Attachments []attachmentEv `json:"attachments"`
}

type attachmentEv struct {
	ContentType *string `json:"content_type"`
	Filename    *string `json:"filename"`
	Size        *int64  `json:"size"`
	Voice       bool    `json:"voice"`
	Gif         bool    `json:"gif"`
	Sticker     bool    `json:"sticker"`
	Caption     *string `json:"caption"`
	Emoji       *string `json:"emoji"` // a sticker's
	File        *string `json:"file"`
	Error       string  `json:"error"`
	// where Signal keeps the file and its key (Signal's AttachmentPointer, base64): what fetches it
	// again, and keeps it in an edit of the message
	Pointer string `json:"pointer"`
	// the hash of the file's content, where that is its check (the transfer archive's files)
	Hash string `json:"hash"`
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
	Multiple bool     `json:"multiple"`
}

// groupChangesEv is a group's change: who made it, and its actions ({"type", ...}, people by id).
type groupChangesEv struct {
	Editor  string           `json:"editor"`
	Actions []map[string]any `json:"actions"`
}

// pinEv is a pin or an unpin: the message, and for how long (nil: for good).
type pinEv struct {
	TargetAuthor *string `json:"target_author"`
	TargetTS     int64   `json:"target_ts"`
	Seconds      *int64  `json:"seconds"`
}

type ref struct {
	Author string `json:"author"`
	TS     int64  `json:"ts"`
}

// event is any of the client's events, the fields each has.
type event struct {
	Event    string   `json:"event"`
	Chat     *chatRef `json:"chat"`
	Sender   string   `json:"sender"`
	Outgoing bool     `json:"outgoing"`
	TS       int64    `json:"ts"`
	ServerTS int64    `json:"server_ts"`

	// a message (or an edit's new content)
	Text          *string         `json:"text"`
	LongText      bool            `json:"long_text"` // the whole text of a long message (its body was the first 2 KiB)
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
	StoryReaction *string         `json:"story_reaction"` // a reaction to a story: the emoji
	Unsupported   bool            `json:"unsupported"`    // made by a newer version of Signal
	GroupChanges  *groupChangesEv `json:"group_changes"`
	PollEnd       *struct {
		TargetTS int64 `json:"target_ts"`
	} `json:"poll_end"`
	// a vote: the options chosen, the voter's count of votes (the newest wins)
	Options       []int64 `json:"options"`
	VoteCount     int64   `json:"vote_count"`
	GroupRevision *int64  `json:"group_revision"`

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

	// a chat's state on the phone: archived, pinned (0/1), muted (until, Unix ms; -1 for ever)
	States map[string]int64 `json:"state"`

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
	in := s.Instance
	// what could not be read and came again (a message, or a reaction, an edit, a call...: its sender
	// and sent time are the content's) is read
	if e.Event != "unreadable" && e.Sender != "" && e.TS != 0 {
		db.Exec(s.DB, "DELETE FROM signal_unreadable WHERE instance = ? AND author = ? AND ts = ?", in, e.Sender, e.TS)
	}
	switch e.Event {
	case "message":
		if e.Chat == nil {
			return false, nil
		}
		// a copy seen again (a fetch again) may have its files now; what was kept of it stays otherwise
		if (hasFile(e) || e.LongText) && db.Exists(s.DB, "SELECT 1 FROM signal_message WHERE instance = ? AND author = ? AND ts = ?",
			in, e.Sender, e.TS) {
			db.Exec(s.DB, "INSERT OR IGNORE INTO signal_late_file VALUES (?, ?, ?)", in, e.Sender, e.TS)
		}
		db.Exec(s.DB, "INSERT INTO signal_message (instance, author, ts, chat_kind, chat, outgoing, server_ts, json, stored_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (instance, author, ts) DO UPDATE SET json = excluded.json",
			in, e.Sender, e.TS, e.Chat.Kind, e.Chat.ID, db.B(e.Outgoing), e.ServerTS, string(raw), time.Now().UnixMilli())
	case "edit":
		if e.TargetTS == nil {
			return false, nil
		}
		db.Exec(s.DB, "INSERT OR REPLACE INTO signal_edit VALUES (?, ?, ?, ?, ?)", in, e.Sender, *e.TargetTS, e.TS, string(raw))
	case "delete":
		if e.TargetTS == nil || e.TargetAuthor == nil {
			return false, nil
		}
		db.Exec(s.DB, "INSERT OR IGNORE INTO signal_deletion VALUES (?, ?, ?, ?)", in, *e.TargetAuthor, *e.TargetTS, e.TS)
	case "reaction":
		if e.TargetTS == nil || e.TargetAuthor == nil {
			return false, nil
		}
		var emoji any
		if !e.Remove && e.Emoji != nil {
			emoji = *e.Emoji
		}
		db.Exec(s.DB, "INSERT INTO signal_reaction VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO UPDATE SET "+
			"emoji = excluded.emoji, ts = excluded.ts WHERE excluded.ts >= signal_reaction.ts",
			in, *e.TargetAuthor, *e.TargetTS, e.Sender, emoji, e.TS)
	case "receipt":
		for _, ts := range e.Timestamps {
			db.Exec(s.DB, "INSERT OR IGNORE INTO signal_receipt VALUES (?, ?, ?, ?, ?)", in, ts, e.Sender, e.Kind, e.TS)
		}
	case "read":
		for _, m := range e.Messages {
			db.Exec(s.DB, "INSERT OR IGNORE INTO signal_read VALUES (?, ?, ?, ?)", in, m.Author, m.TS, e.TS)
		}
	case "call":
		return s.call(e), nil
	case "poll_vote":
		if e.TargetTS == nil || e.TargetAuthor == nil {
			return false, nil
		}
		opts, _ := json.Marshal(e.Options)
		if e.Options == nil {
			opts = []byte("[]")
		}
		db.Exec(s.DB, "INSERT INTO signal_poll_vote VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO UPDATE SET options = excluded.options, "+
			"vote_count = excluded.vote_count WHERE excluded.vote_count >= signal_poll_vote.vote_count",
			in, *e.TargetAuthor, *e.TargetTS, e.Sender, string(opts), e.VoteCount)
	case "unreadable":
		if e.Chat == nil {
			return false, nil
		}
		db.Exec(s.DB, "INSERT OR IGNORE INTO signal_unreadable VALUES (?, ?, ?, ?, ?, ?)", in, e.Sender, e.TS, e.Chat.Kind, e.Chat.ID, now)
	case "contacts", "contact":
		// the book's name and the number stay where Signal no longer says them (a profile seen in a
		// message names someone without their number)
		for _, c := range e.ContactList {
			if c.ACI == "" {
				continue
			}
			db.Exec(s.DB, "INSERT INTO signal_contact VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (instance, aci) DO UPDATE SET "+
				"phone = coalesce(excluded.phone, signal_contact.phone), name = coalesce(excluded.name, signal_contact.name), "+
				"profile_name = coalesce(excluded.profile_name, signal_contact.profile_name), seen_at = excluded.seen_at",
				in, c.ACI, ptr(c.Phone), ptr(c.Name), ptr(c.ProfileName), now)
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
			db.Exec(s.DB, "INSERT INTO signal_pni VALUES (?, ?, ?, ?, ?) ON CONFLICT (instance, pni) DO UPDATE SET "+
				"phone = coalesce(excluded.phone, signal_pni.phone), aci = coalesce(excluded.aci, signal_pni.aci), seen_at = excluded.seen_at",
				in, e.PNI, phone, aci, now)
		case e.ACI != "" && phone != nil:
			db.Exec(s.DB, "INSERT INTO signal_contact (instance, aci, phone, seen_at) VALUES (?, ?, ?, ?) ON CONFLICT (instance, aci) "+
				"DO UPDATE SET phone = excluded.phone, seen_at = excluded.seen_at", in, e.ACI, phone, now)
		default:
			return false, nil
		}
	case "group":
		if e.ID == nil {
			return false, nil
		}
		db.Exec(s.DB, "INSERT OR REPLACE INTO signal_group VALUES (?, ?, ?, ?, ?, ?)", in, *e.ID, ptr(e.Title), ptr(e.Description),
			ptrI(e.Revision), now)
		db.Exec(s.DB, "DELETE FROM signal_group_member WHERE instance = ? AND group_id = ?", in, *e.ID)
		for _, m := range e.Members {
			db.Exec(s.DB, "INSERT OR IGNORE INTO signal_group_member VALUES (?, ?, ?)", in, *e.ID, m)
		}
	case "chat_state":
		if e.Chat == nil || len(e.States) == 0 {
			return false, nil
		}
		for _, f := range []string{"archived", "pinned", "muted"} {
			if v, ok := e.States[f]; ok {
				db.Exec(s.DB, "INSERT INTO signal_chat_state VALUES (?, ?, ?, ?, ?) ON CONFLICT DO UPDATE SET "+
					"value = excluded.value, at = excluded.at WHERE excluded.at >= signal_chat_state.at", in, e.Chat.ID, f, v, e.TS)
			}
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
	id, in := *e.ID, s.Instance
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
		case "delete": // an entry taken off the phone's call log: the call was as it was
			return false
		case "observed", "unknown": // the call log saw it, nothing of how it went: no call of its own (a
			// group call never joined would read as missed); a call known otherwise keeps its direction
			db.Exec(s.DB, "UPDATE signal_call SET outgoing = coalesce(outgoing, ?) WHERE instance = ? AND id = ?", outgoing, in, id)
			return true
		}
		db.Exec(s.DB, "INSERT INTO signal_call (instance, id, chat_kind, chat, ts, outgoing, video, accepted, result) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (instance, id) DO UPDATE SET "+
			"chat_kind = coalesce(excluded.chat_kind, signal_call.chat_kind), chat = coalesce(excluded.chat, signal_call.chat), "+
			"ts = excluded.ts, outgoing = coalesce(excluded.outgoing, signal_call.outgoing), "+
			"video = max(signal_call.video, excluded.video), accepted = coalesce(excluded.accepted, signal_call.accepted), "+
			"result = excluded.result", in, id, kind, chat, e.TS, outgoing, db.B(e.Type == "video"), accepted, e.Result)
		return true
	}
	if e.Chat == nil {
		return false
	}
	switch e.Action {
	case "offer":
		db.Exec(s.DB, "INSERT INTO signal_call (instance, id, chat_kind, chat, ts, outgoing, video) VALUES (?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT (instance, id) DO UPDATE SET video = max(signal_call.video, excluded.video)",
			in, id, e.Chat.Kind, e.Chat.ID, e.TS, db.B(e.Outgoing), db.B(e.Video))
	case "answer": // they took the owner's call
		db.Exec(s.DB, "INSERT INTO signal_call (instance, id, chat_kind, chat, ts, outgoing, accepted) VALUES (?, ?, ?, ?, ?, 1, 1) "+
			"ON CONFLICT (instance, id) DO UPDATE SET accepted = 1", in, id, e.Chat.Kind, e.Chat.ID, e.TS)
	case "busy", "hangup":
		how := e.Hangup
		if e.Action == "busy" {
			how = "busy"
		}
		db.Exec(s.DB, "INSERT INTO signal_call (instance, id, chat_kind, chat, ts, hangup, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT (instance, id) DO UPDATE SET hangup = coalesce(signal_call.hangup, excluded.hangup), "+
			"ended_at = max(coalesce(signal_call.ended_at, 0), excluded.ended_at)", in, id, e.Chat.Kind, e.Chat.ID, e.TS, how, e.TS)
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
// after it was sent (Signal's server keeps them for a while, then they are gone), and so how long a
// message the archive has stays in the store.
const (
	fetchTries = 3
	fetchDays  = 30
)

// keepFor is how long the store keeps what the archive has, from when it came: while its files may be asked
// again.
var keepFor = fetchDays * 24 * time.Hour

// FailedFiles are the messages (that came after since, Unix ms) with a file that failed to come and is
// still to be asked again; each is counted as asked once more.
func (s *Store) FailedFiles(since int64) (out []ref) {
	var err error
	defer func() {
		if err != nil { // the store closed meanwhile (its connection ended): none now
			out = nil
		}
	}()
	defer db.Recover(&err)
	in := s.Instance
	// the tries spaced out (at once, an hour on, six hours on), so that a fault of a while does not
	// use them all
	now := time.Now().Unix()
	db.Each(s.DB, "SELECT m.author, m.ts, m.json FROM signal_message m "+
		"LEFT JOIN signal_fetch_try f ON f.instance = m.instance AND f.author = m.author AND f.ts = m.ts "+
		"LEFT JOIN signal_fetch_at w ON w.instance = m.instance AND w.author = m.author AND w.ts = m.ts "+
		"WHERE m.instance = ? AND m.stored_at >= ? AND m.json LIKE '%\"error\":%' AND coalesce(f.tries, 0) < ? "+
		"AND coalesce(w.at, 0) <= ? - CASE coalesce(f.tries, 0) WHEN 0 THEN 0 WHEN 1 THEN 3600 ELSE 21600 END ORDER BY m.ts",
		[]any{in, since, fetchTries, now}, func(scan func(...any)) {
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
		db.Exec(s.DB, "INSERT INTO signal_fetch_try VALUES (?, ?, ?, 1) ON CONFLICT DO UPDATE SET tries = tries + 1", in, r.Author, r.TS)
		db.Exec(s.DB, "INSERT INTO signal_fetch_at VALUES (?, ?, ?, ?) ON CONFLICT DO UPDATE SET at = excluded.at", in, r.Author, r.TS, now)
	}
	return out
}

// Unread are the others' messages of a chat at or before until (Unix ms) the owner has not read.
func (s *Store) Unread(chat string, until int64) []ref {
	var out []ref
	db.Each(s.DB, "SELECT author, ts FROM signal_message m WHERE instance = ? AND chat = ? AND NOT outgoing AND ts <= ? "+
		"AND NOT EXISTS (SELECT 1 FROM signal_read r WHERE r.instance = m.instance AND r.author = m.author AND r.ts = m.ts) ORDER BY ts",
		[]any{s.Instance, chat, until}, func(scan func(...any)) {
			var r ref
			scan(&r.Author, &r.TS)
			out = append(out, r)
		})
	return out
}

// Read records messages the owner read (here), at Unix ms.
func (s *Store) Read(refs []ref, at int64) {
	for _, r := range refs {
		db.Exec(s.DB, "INSERT OR IGNORE INTO signal_read VALUES (?, ?, ?, ?)", s.Instance, r.Author, r.TS, at)
	}
}

// Revisions are the times a message has been known by: its own (ts) and its author's edits of it,
// oldest first. Signal's apps aim an edit (and a reaction, a deletion) at the newest; some at the
// first, so each edit is followed from any of them.
func (s *Store) Revisions(author string, ts int64) []int64 {
	next := map[int64][]int64{}
	db.Each(s.DB, "SELECT target_ts, ts FROM signal_edit WHERE instance = ? AND author = ? ORDER BY ts", []any{s.Instance, author},
		func(scan func(...any)) {
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

// MyReaction is the owner's reaction on a message (any of its times) as the store last has it: its
// emoji ("" taken back), and whether there was one at all.
func (s *Store) MyReaction(own, author string, times []int64) (string, bool) {
	var emoji *string
	args := append([]any{s.Instance, author, own}, db.Args(times)...)
	if !db.Row(s.DB, "SELECT emoji FROM signal_reaction WHERE instance = ? AND target_author = ? AND sender = ? AND target_ts IN ("+
		db.Marks(len(times))+") ORDER BY ts DESC LIMIT 1", args, &emoji) {
		return "", false
	}
	return deref(emoji), true
}

// Message is a message's event as the store keeps it (false: not there).
func (s *Store) Message(author string, ts int64) (event, bool) {
	var e event
	raw, ok := s.MessageJSON(author, ts)
	if !ok || json.Unmarshal(raw, &e) != nil {
		return e, false
	}
	return e, true
}

// MessageJSON is a message's event as the store keeps it, whole (false: not there).
func (s *Store) MessageJSON(author string, ts int64) ([]byte, bool) {
	var js string
	if !db.Row(s.DB, "SELECT json FROM signal_message WHERE instance = ? AND author = ? AND ts = ?",
		[]any{s.Instance, author, ts}, &js) {
		return nil, false
	}
	return []byte(js), true
}

// Deleted: the message (any of its times) was deleted for everyone.
func (s *Store) Deleted(author string, times []int64) bool {
	return db.Exists(s.DB, "SELECT 1 FROM signal_deletion WHERE instance = ? AND author = ? AND ts IN ("+db.Marks(len(times))+")",
		append([]any{s.Instance, author}, db.Args(times)...)...)
}

// Prune takes out of the store the messages the archive has (by key, author and sent time) that
// came keepFor ago (the time they came, not when they were sent: the history transferred at link
// time is old when it comes), with what was about them alone: their files' tries, a deletion, the
// votes of a poll, an edit's content (its times stay: what is aimed at an edit's time still finds
// its message). Then the reactions, receipts and reads as old whose message left too, the calls as
// old (but a chat's left out: skip), and the unreadable notes as old.
func (s *Store) Prune(imported []ref, skip map[string]bool) (err error) {
	defer db.Recover(&err)
	cutoff := time.Now().Add(-keepFor)
	ms, in := cutoff.UnixMilli(), s.Instance
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range imported {
		if n, _ := db.Exec(tx, "DELETE FROM signal_message WHERE instance = ? AND author = ? AND ts = ? AND stored_at < ?",
			in, r.Author, r.TS, ms).RowsAffected(); n == 0 {
			continue
		}
		for _, t := range []string{"signal_late_file", "signal_fetch_try", "signal_fetch_at", "signal_deletion"} {
			db.Exec(tx, "DELETE FROM "+t+" WHERE instance = ? AND author = ? AND ts = ?", in, r.Author, r.TS)
		}
		db.Exec(tx, "UPDATE signal_edit SET json = '' WHERE instance = ? AND author = ? AND target_ts = ?", in, r.Author, r.TS)
		db.Exec(tx, "DELETE FROM signal_poll_vote WHERE instance = ? AND target_author = ? AND target_ts = ?", in, r.Author, r.TS)
	}
	db.Exec(tx, "DELETE FROM signal_reaction WHERE instance = ? AND ts < ? AND NOT EXISTS (SELECT 1 FROM signal_message m "+
		"WHERE m.instance = signal_reaction.instance AND m.author = target_author AND m.ts = target_ts)", in, ms)
	db.Exec(tx, "DELETE FROM signal_receipt WHERE instance = ? AND at < ? AND NOT EXISTS (SELECT 1 FROM signal_message m "+
		"WHERE m.instance = signal_receipt.instance AND m.outgoing AND m.ts = signal_receipt.ts)", in, ms)
	db.Exec(tx, "DELETE FROM signal_read WHERE instance = ? AND at < ? AND NOT EXISTS (SELECT 1 FROM signal_message m "+
		"WHERE m.instance = signal_read.instance AND m.author = signal_read.author AND m.ts = signal_read.ts)", in, ms)
	var kept []string
	for chat := range skip {
		kept = append(kept, chat)
	}
	db.Exec(tx, "DELETE FROM signal_call WHERE instance = ? AND coalesce(ended_at, ts) < ? AND coalesce(chat, '') NOT IN ("+
		db.Marks(len(kept)+1)+")", append([]any{in, ms, "\x00"}, db.Args(kept)...)...) // (one never a chat: the list is never empty)
	db.Exec(tx, "DELETE FROM signal_unreadable WHERE instance = ? AND at < ?", in, cutoff.Unix())
	return tx.Commit()
}

// Forget removes everything the store has of its instance (the instance unlinked, its store imported).
func (s *Store) Forget() (err error) {
	defer db.Recover(&err)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"account", "contact", "pni", "group", "group_member", "message", "edit", "deletion", "reaction",
		"receipt", "read", "late_file", "fetch_try", "fetch_at", "call", "poll_vote", "unreadable", "chat_state"} {
		db.Exec(tx, "DELETE FROM signal_"+t+" WHERE instance = ?", s.Instance)
	}
	return tx.Commit()
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
