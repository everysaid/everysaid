package importers

// WhatsApp as the live connection (internal/whatsapp) brings it: each event, or each history the
// phone sends, is a WABatch that a WhatsAppWriter writes into the archive at once, as the Meta
// sources do; nothing of it is kept anywhere else. What a batch names that the archive does not have
// yet (a reaction, an edit, a receipt or a read of a message still to come, from the phone's history
// or the iPhone's backup) waits in keys.db (whatsapp_pending) until it does.
//
// keys.db also holds what the connection itself needs to go on (WhatsAppSchema): no message's
// content, only what WhatsApp names things by. Each row is a store folder's (`store`: its absolute
// path), as whatsapp_device says which device each folder linked.

import (
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// WhatsAppSchema is the live connection's tables in keys.db. Times are Unix milliseconds where they
// are a message's (as the archive's), Unix seconds where they are the connection's own.
const WhatsAppSchema = `
CREATE TABLE IF NOT EXISTS whatsapp_state (       -- the connection: connection, send_enabled, send_blocked, ban_until
    store TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT,
    at INTEGER,                                    -- Unix s
    PRIMARY KEY (store, key)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS whatsapp_event (       -- its last events (the card shows them)
    store TEXT NOT NULL,
    at INTEGER NOT NULL,                           -- Unix ms
    event TEXT NOT NULL,
    code TEXT,
    detail TEXT
);
CREATE INDEX IF NOT EXISTS whatsapp_event_store ON whatsapp_event (store);
CREATE TABLE IF NOT EXISTS whatsapp_sent (        -- what was sent from here, for the limits of pace (a day)
    store TEXT NOT NULL,
    at INTEGER NOT NULL,                           -- Unix s
    chat_jid TEXT,
    id TEXT,
    text_hash TEXT
);
CREATE INDEX IF NOT EXISTS whatsapp_sent_at ON whatsapp_sent (store, at);
CREATE TABLE IF NOT EXISTS whatsapp_started (     -- the chats the owner wrote in first, from here
    store TEXT NOT NULL,
    chat_jid TEXT NOT NULL,
    at INTEGER NOT NULL,                           -- Unix s
    PRIMARY KEY (store, chat_jid)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS whatsapp_history (     -- history the phone sent, not written yet
    store TEXT NOT NULL,
    direct_path TEXT NOT NULL,
    notification BLOB NOT NULL,                    -- waE2E.HistorySyncNotification
    tries INTEGER NOT NULL DEFAULT 0,
    next_at INTEGER NOT NULL DEFAULT 0,            -- Unix s: not tried before
    UNIQUE (store, direct_path)
);
CREATE TABLE IF NOT EXISTS whatsapp_chat (        -- the chats: their names, and whether someone wrote there
    store TEXT NOT NULL,
    jid TEXT NOT NULL,
    name TEXT,
    last_at INTEGER,                               -- its newest message, Unix ms
    incoming INTEGER NOT NULL DEFAULT 0,           -- the other side wrote there (the connection sends only there)
    PRIMARY KEY (store, jid)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS whatsapp_group (       -- the groups: how they name their members
    store TEXT NOT NULL,
    jid TEXT NOT NULL,
    name TEXT,
    addressing TEXT,                               -- 'pn' or 'lid'
    member INTEGER NOT NULL DEFAULT 1,             -- the account is in it now
    updated_at INTEGER,                            -- Unix s
    PRIMARY KEY (store, jid)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS whatsapp_group_member (
    store TEXT NOT NULL,
    group_jid TEXT NOT NULL,
    jid TEXT NOT NULL,                             -- as the group names them (its addressing)
    phone TEXT,                                    -- their number's jid, where known
    lid TEXT,                                      -- their LID, where known
    is_admin INTEGER NOT NULL DEFAULT 0,
    is_super_admin INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (store, group_jid, jid)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS whatsapp_blocked (     -- the people the account blocked, as WhatsApp last said
    store TEXT NOT NULL,
    jid TEXT NOT NULL,
    PRIMARY KEY (store, jid)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS whatsapp_photo (       -- the profile photos seen of each jid, a row for each
    store TEXT NOT NULL,
    jid TEXT NOT NULL,
    picture_id TEXT NOT NULL,                      -- WhatsApp's id of the photo; '': none
    path TEXT,                                     -- its file, relative to the store folder (gone once the archive has it)
    sha256 TEXT,
    first_seen INTEGER NOT NULL,                   -- Unix s
    last_seen INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS whatsapp_photo_jid ON whatsapp_photo (store, jid, first_seen);
CREATE TABLE IF NOT EXISTS whatsapp_photo_check ( -- when each jid's photo was last asked for
    store TEXT NOT NULL,
    jid TEXT NOT NULL,
    checked_at INTEGER,                            -- Unix s; NULL: never
    due INTEGER NOT NULL DEFAULT 0,                -- WhatsApp said it changed: asked first
    PRIMARY KEY (store, jid)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS whatsapp_recent (      -- the messages of the last week by their ids: where each is, who wrote it
    store TEXT NOT NULL,
    chat_jid TEXT NOT NULL,
    id TEXT NOT NULL,
    sender TEXT,                                   -- a jid
    from_me INTEGER NOT NULL,
    at INTEGER NOT NULL,                           -- Unix ms
    read_at INTEGER,                               -- the account read it (another's), on any device: Unix ms
    PRIMARY KEY (store, chat_jid, id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS whatsapp_recent_id ON whatsapp_recent (store, id);
CREATE TABLE IF NOT EXISTS whatsapp_media (       -- a message's file (or its link preview's picture) not in the archive yet
    store TEXT NOT NULL,
    chat_jid TEXT NOT NULL,
    id TEXT NOT NULL,
    at INTEGER NOT NULL,                           -- the message's time, Unix ms
    sender TEXT,
    from_me INTEGER NOT NULL DEFAULT 0,
    media_type TEXT,                               -- image, video, audio, document, sticker; NULL: only a preview's picture
    filename TEXT,                                 -- a document's name as it was sent
    url TEXT,
    direct_path TEXT,
    media_key BLOB,
    file_sha256 BLOB,
    file_enc_sha256 BLOB,
    file_length INTEGER,
    path TEXT,                                     -- downloaded, relative to the store folder: for the archive to take
    error TEXT,                                    -- why it cannot be had ('asked again': the phone was asked for it)
    preview_url TEXT,                              -- the link of the preview whose picture is to come
    preview_direct_path TEXT,
    preview_sha256 BLOB,
    preview_enc_sha256 BLOB,
    preview_media_key BLOB,
    PRIMARY KEY (store, chat_jid, id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS whatsapp_pending (     -- what names a message the archive does not have yet
    store TEXT NOT NULL,
    chat_jid TEXT NOT NULL,
    target TEXT NOT NULL,                          -- the message's id
    kind TEXT NOT NULL,                            -- react, edit, delete, delete_me, delivered, read, played, self_read
    sender TEXT NOT NULL,                          -- who did it (a jid; '' for the account's own read)
    from_me INTEGER NOT NULL DEFAULT 0,
    data TEXT,                                     -- JSON: a reaction's emoji, an edit's text and mentions
    at INTEGER NOT NULL,                           -- Unix ms (0: not known)
    PRIMARY KEY (store, chat_jid, target, kind, sender)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS whatsapp_pending_target ON whatsapp_pending (store, target);
CREATE TABLE IF NOT EXISTS whatsapp_vote (        -- each voter's newest vote in a poll
    store TEXT NOT NULL,
    chat_jid TEXT NOT NULL,
    poll_id TEXT NOT NULL,
    voter TEXT NOT NULL,
    options TEXT NOT NULL,                         -- JSON: the SHA-256 (hex) of each option chosen, as WhatsApp names them
    at INTEGER NOT NULL,                           -- Unix ms
    PRIMARY KEY (store, chat_jid, poll_id, voter)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS whatsapp_call (        -- a call as its signalling goes, until it ends
    store TEXT NOT NULL,
    id TEXT NOT NULL,
    chat_jid TEXT,
    creator TEXT,
    from_me INTEGER NOT NULL DEFAULT 0,
    is_group INTEGER NOT NULL DEFAULT 0,
    video INTEGER NOT NULL DEFAULT 0,
    at INTEGER NOT NULL,                           -- Unix ms
    accepted_at INTEGER,
    ended_at INTEGER,
    end_reason TEXT,
    PRIMARY KEY (store, id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS whatsapp_unreadable (  -- a message this device could not read (the phone is asked for it)
    store TEXT NOT NULL,
    chat_jid TEXT NOT NULL,
    id TEXT NOT NULL,
    sender TEXT,
    from_me INTEGER NOT NULL DEFAULT 0,
    at INTEGER NOT NULL,                           -- Unix ms
    PRIMARY KEY (store, id)
) WITHOUT ROWID;
`

// WhatsAppTables makes the live connection's tables in a keys database.
func WhatsAppTables(d *sql.DB) error {
	_, err := d.Exec(WhatsAppSchema)
	return err
}

// WhatsAppSource is the archive's source of the live connection's messages, and of its calls.
const (
	WhatsAppSource      = "whatsapp-bridge"
	whatsappCallLogs    = "whatsapp-bridge/calls"
	whatsappCallSignals = "whatsapp-bridge/call-events"
)

// WAMessage is a message as the connection reads it. Its people are jids; Kind is WhatsApp's
// (text, image, video, audio, voice, document, sticker, location, contact, poll), Subtype too (gif,
// video_note, link, live_location, view_once).
type WAMessage struct {
	Chat, ID, Sender   string
	FromMe             bool
	At                 int64 // Unix ms
	Kind, Subtype      string
	Text               string
	MediaType          string // the file's kind, "" for none
	ReplyTo, ReplyText string
	Mentions           []string
	Forwarded          bool
	ForwardFrom, Album string
	Lat, Lon           *float64
	Place              string
	Poll               string // a poll's question, options and whether several may be chosen (JSON)
	Preview            *archive.Preview
	File               *WAFile        // its file, where it has one
	PreviewFile        *WAPreviewFile // where its preview's high-quality picture is, where it has one
}

// WAFile is a message's file as WhatsApp's servers keep it (encrypted), what downloads it; Path:
// already in the store folder (relative to it), as for a file sent from here.
type WAFile struct {
	MediaType, Filename, URL, DirectPath string
	MediaKey, FileSHA256, FileEncSHA256  []byte
	FileLength                           uint64
	Path                                 string
}

// WAPreviewFile is where a link preview's high-quality picture is on WhatsApp's servers.
type WAPreviewFile struct {
	URL, DirectPath        string
	SHA256, EncSHA256, Key []byte
}

// WAReaction is someone's reaction to a message (Emoji "" once taken back). History: from the
// phone's history, older than what came live: it does not replace a reaction there is.
type WAReaction struct {
	Chat, Target, Sender string
	FromMe               bool
	Emoji                string
	At                   int64
	History              bool
}

// WAChange is an edit (Text, Mentions), a deletion for everyone or one for the user alone
// (delete_me) of an earlier message, by Sender. A person's chat may be kept under their number and
// their LID: Alt are the chat's other jids, SenderAlt the sender's.
type WAChange struct {
	Chat, Target, Change, Sender string
	FromMe                       bool
	Text                         string
	Mentions                     []string
	At                           int64
	Alt, SenderAlt               []string
}

// WAReceipt says that Who got (delivered), read or played one of the account's messages; At 0:
// only that it was (from history).
type WAReceipt struct {
	Chat, Target, Who, Kind string
	At                      int64
}

// WARead: the account read messages of a chat, on any device, at At.
type WARead struct {
	Chat string
	IDs  []string
	At   int64
}

// WAVote is a voter's newest vote in a poll: the SHA-256 (hex) of each option chosen.
type WAVote struct {
	Chat, Poll, Voter string
	Options           []string
	At                int64
}

// WANotice is what a chat's event says (docs/design.md, "Notices"): a pin, a timer set, a message
// of a kind not kept, one that could not be read. Target: the message it is about.
type WANotice struct {
	Chat, ID, Sender string
	FromMe           bool
	At               int64
	Code             string
	Args             map[string]any
	Target           string
}

// WAGroupEvent is what changed in a group, its actions as notices say them (JSON), people by jid.
// OncePerDay: WhatsApp may say it again (a joining, on connecting): kept once a day.
type WAGroupEvent struct {
	Group, Sender string
	At            int64
	Actions       string
	OncePerDay    bool
}

// WACallLog is the call-log message every device gets after a call.
type WACallLog struct {
	ID, Chat             string
	FromMe, Group, Video bool
	At                   int64
	Outcome              string      // CONNECTED, MISSED, FAILED, REJECTED, ACCEPTED_ELSEWHERE, ...
	Duration             int64       // seconds
	Participants         [][2]string // a group call's: jid, outcome
}

// WACallSignal is a call's signalling: an offer (Creator, FromMe, Group, Video), its acceptance,
// rejection or end (Reason).
type WACallSignal struct {
	ID, Kind, Chat, Creator string // Kind: offer, accept, reject, terminate
	FromMe, Group, Video    bool
	At                      int64
	Reason                  string
}

// WABatch is what one event (or one history) brings, written in this order.
type WABatch struct {
	Chats       map[string]string // chat jid -> its name, where known
	Messages    []WAMessage
	Notices     []WANotice
	GroupEvents []WAGroupEvent
	Reactions   []WAReaction
	Changes     []WAChange
	Receipts    []WAReceipt
	Reads       []WARead
	Votes       []WAVote
	CallLogs    []WACallLog
	CallSignals []WACallSignal
}

// Empty says whether the batch has nothing to write.
func (b *WABatch) Empty() bool {
	return b == nil || len(b.Messages)+len(b.Notices)+len(b.GroupEvents)+len(b.Reactions)+len(b.Changes)+len(b.Receipts)+
		len(b.Reads)+len(b.Votes)+len(b.CallLogs)+len(b.CallSignals) == 0
}

// WhatsAppWriter writes a store folder's batches into the archive.
type WhatsAppWriter struct {
	A       *archive.Archive
	Keys    *sql.DB // keys.db
	Store   string  // the store folder, as keys.db names it
	Source  int64
	Updated map[string]int // what changed on messages already there: counts
	Added   int            // messages added
	person  *waPeople
	own     map[archive.Handle]bool
	convs   map[string]int64
	names   map[string]string
	calls   *CallSet
	done    [][]any // whatsapp_pending rows applied: deleted once the archive has them (Done)
}

// NewWhatsAppWriter is a writer of the store folder's batches into the archive.
func NewWhatsAppWriter(a *archive.Archive, keys *sql.DB, store string) *WhatsAppWriter {
	w := &WhatsAppWriter{A: a, Keys: keys, Store: store, Updated: map[string]int{}, convs: map[string]int64{},
		names: map[string]string{}, own: a.Own()}
	w.Source = a.Source(WhatsAppSource, store, WhatsAppSource, "")
	w.person = &waPeople{lidPhone: map[string]string{}, lids: map[string]bool{}, names: map[waName]string{}, keys: keys}
	a.Imported(w.Source)
	return w
}

// Write writes a batch.
func (w *WhatsAppWriter) Write(b *WABatch) {
	for jid, name := range b.Chats {
		w.names[jid] = name
	}
	var keys []string
	files := false
	for _, m := range b.Messages {
		if _, added := w.Message(m); added {
			keys = append(keys, m.ID)
		}
		files = files || m.File != nil && m.File.Path != ""
	}
	if files { // sent from here: in the store folder already
		w.Files(w.Store)
	}
	for _, n := range b.Notices {
		w.Notice(n)
	}
	for _, g := range b.GroupEvents {
		w.GroupEvent(g)
	}
	for _, r := range b.Reactions {
		w.React(r)
	}
	for _, c := range b.Changes {
		w.Change(c)
	}
	for _, r := range b.Receipts {
		w.Receipt(r)
	}
	for _, r := range b.Reads {
		w.Read(r)
	}
	for _, v := range b.Votes {
		w.Vote(v)
	}
	for _, c := range b.CallLogs {
		w.CallLog(c)
	}
	for _, c := range b.CallSignals {
		w.CallSignal(c)
	}
	if len(keys) > 0 { // what waited for these (none given would be all that waits)
		w.Pending(keys)
	}
	if len(b.Notices) > 0 || len(b.Messages) > 0 {
		for c, n := range ApplyPins(w.A, "whatsapp") {
			w.Updated[c] += n
		}
	}
}

// Done ends the writing: the archive's links settled and committed, then what waited in keys.db
// for it and is there now let go. It says whether messages already shown changed.
func (w *WhatsAppWriter) Done() bool {
	if w.Added > 0 || w.calls != nil { // the links of what is new (a receipt, a reaction, a read has none)
		w.A.Resolve()
	}
	w.A.Commit()
	for _, k := range w.done {
		db.Exec(w.Keys, "DELETE FROM whatsapp_pending WHERE store = ? AND chat_jid = ? AND target = ? AND kind = ? AND sender = ?", k...)
	}
	w.done = nil
	if len(w.Updated) > 0 {
		w.A.Changed = true
	}
	return len(w.Updated) > 0
}

// conv is the conversation of a chat; 0 for a channel's.
func (w *WhatsAppWriter) conv(jid string) int64 {
	if c, ok := w.convs[jid]; ok {
		return c
	}
	var title any
	if name, ok := w.names[jid]; ok {
		title = name
	} else if strings.HasSuffix(jid, "@g.us") {
		var name sql.NullString
		db.Row(w.Keys, "SELECT name FROM whatsapp_chat WHERE store = ? AND jid = ?", []any{w.Store, jid}, &name)
		if name.Valid {
			title = name.String
		}
	}
	c := waConversation(w.A, w.person, jid, title, nil)
	w.convs[jid] = c
	return c
}

// message is a message of the archive by its id (0 for none).
func (w *WhatsAppWriter) message(id string) int64 {
	if id == "" {
		return 0
	}
	mid, ok := w.A.MessageByKey("whatsapp", id, 0)
	if !ok {
		return 0
	}
	return mid
}

// kindOf is the archive's kind of a message.
func (m WAMessage) kindOf() string {
	if m.Kind != "" {
		if k := bridgeKinds[m.Kind]; k != "" {
			return k
		}
		return "file"
	}
	if k, ok := bridgeMediaKinds[m.MediaType]; ok {
		return k
	}
	return "file"
}

// extras is what the archive keeps of a message beyond its text.
func (m WAMessage) extras() *archive.Extras {
	out := &archive.Extras{}
	if m.Subtype != "" {
		if s, ok := bridgeSubtypes[m.Subtype]; ok {
			out.Subtype = s
		}
		out.SubtypeCode = "whatsmeow:" + m.Subtype
	}
	if m.Kind == "poll" {
		out.Subtype, out.SubtypeCode = "poll", "whatsmeow:poll"
	}
	if m.ReplyTo != "" {
		out.ReplyKey, out.ReplyText = m.ReplyTo, m.ReplyText
	}
	out.Forwarded, out.ForwardFrom, out.Album = m.Forwarded, m.ForwardFrom, m.Album
	if m.Lat != nil && m.Lon != nil {
		out.Lat, out.Lon, out.Place = m.Lat, m.Lon, m.Place
	}
	out.Text = contactLines(m.Kind, m.Text)
	out.Preview = m.Preview
	return out
}

// contactLines is a shared contact's text as the archive shows it, a 📇 on each line ("" for
// another kind).
func contactLines(kind, text string) string {
	if kind != "contact" || text == "" {
		return ""
	}
	var lines []string
	for _, l := range splitLines(text) {
		lines = append(lines, "📇 "+l)
	}
	return strings.Join(lines, "\n")
}

// sender is the archive's address of a jid that is not the owner's (0 for the owner or none).
func (w *WhatsAppWriter) sender(jid string) int64 {
	if p, ok := w.person.of(jid); ok && !w.own[p] {
		return w.A.Address(p)
	}
	return 0
}

// Message writes a message; one the archive has (from here before, or from the iPhone: the same
// id) gets what it lacked: its preview, where it was forwarded from, its album, its poll's votes,
// whom it names. It gives the message's id (0 for a channel's) and whether it is new.
func (w *WhatsAppWriter) Message(m WAMessage) (int64, bool) {
	a := w.A
	conv := w.conv(m.Chat)
	if conv == 0 {
		return 0, false
	}
	rowKey := m.Chat + "/" + m.ID
	// it came after all: the trace of it not read goes (by its id alone: the phone may send it again
	// under the chat's other name, its number or its LID)
	db.Exec(w.Keys, "DELETE FROM whatsapp_unreadable WHERE store = ? AND id = ?", w.Store, m.ID)
	if mid := w.message(m.ID); mid != 0 {
		if !a.HasOrigin(w.Source, rowKey, "") {
			a.Exec("INSERT OR IGNORE INTO message_origin (source_id, row_key, message_id) VALUES (?, ?, ?)", w.Source, rowKey, mid)
		}
		// its picture kept stays: the high-quality one, downloaded after the small one came
		if m.Preview != nil && setPreview(a, mid, m.Preview, true) {
			w.Updated["preview"]++
		}
		for _, c := range ApplyMeta(a, mid, Meta{ForwardFrom: m.ForwardFrom, Album: m.Album}) {
			w.Updated[c]++
		}
		w.mentions(mid, m.Mentions)
		if m.Poll != "" {
			w.poll(mid, m.Chat, m.ID, m.Poll)
		}
		if m.File != nil && !a.Exists("SELECT 1 FROM attachment WHERE message_id = ?", mid) {
			w.wantFile(m, false) // a file it has not got (yet)
		}
		return mid, false
	}
	// the row key was another message's, gone from the archive since (as spam): now it is this one's
	a.Exec("DELETE FROM message_origin WHERE source_id = ? AND row_key = ?", w.Source, rowKey)
	var senderID int64
	if !m.FromMe {
		s := m.Sender
		if s == "" {
			s = m.Chat
		}
		if p, ok := w.person.of(s); ok {
			senderID = a.Address(p)
		}
	}
	mid := a.AddMessage(w.Source, rowKey, archive.Message{Service: "whatsapp", ConversationID: conv, TS: m.At,
		Outgoing: m.FromMe, SenderID: senderID, Kind: m.kindOf(), Text: m.Text, Key: m.ID, Extras: m.extras()})
	w.Added++
	w.mentions(mid, m.Mentions)
	if m.Poll != "" {
		w.poll(mid, m.Chat, m.ID, m.Poll)
	}
	if m.File != nil || m.PreviewFile != nil {
		w.wantFile(m, true)
	}
	return mid, true
}

// wantFile records a message's file to download (a view-once message's is left alone, as the
// sender meant it), and, of a new message, its preview's high-quality picture. A new place for a
// file that could not be had is worth trying again.
func (w *WhatsAppWriter) wantFile(m WAMessage, isNew bool) {
	f, p := m.File, m.PreviewFile
	if m.Subtype == "view_once" {
		f = nil
	}
	if !isNew {
		p = nil
	}
	if f == nil && p == nil {
		return
	}
	if f == nil {
		f = &WAFile{}
	}
	if p == nil {
		p = &WAPreviewFile{}
	}
	name := ""
	if f.MediaType == "document" {
		name = f.Filename
	}
	db.Exec(w.Keys, `INSERT INTO whatsapp_media (store, chat_jid, id, at, sender, from_me, media_type, filename, url, direct_path,
			media_key, file_sha256, file_enc_sha256, file_length, path, preview_url, preview_direct_path, preview_sha256,
			preview_enc_sha256, preview_media_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (store, chat_jid, id) DO UPDATE SET
			media_type = coalesce(excluded.media_type, whatsapp_media.media_type),
			filename = coalesce(whatsapp_media.filename, excluded.filename),
			url = coalesce(excluded.url, whatsapp_media.url),
			direct_path = coalesce(excluded.direct_path, whatsapp_media.direct_path),
			media_key = coalesce(excluded.media_key, whatsapp_media.media_key),
			file_sha256 = coalesce(excluded.file_sha256, whatsapp_media.file_sha256),
			file_enc_sha256 = coalesce(excluded.file_enc_sha256, whatsapp_media.file_enc_sha256),
			file_length = coalesce(excluded.file_length, whatsapp_media.file_length),
			path = coalesce(excluded.path, whatsapp_media.path),
			error = CASE WHEN excluded.direct_path IS NOT whatsapp_media.direct_path AND excluded.direct_path IS NOT NULL
				THEN NULL ELSE whatsapp_media.error END,
			preview_url = coalesce(excluded.preview_url, whatsapp_media.preview_url),
			preview_direct_path = coalesce(excluded.preview_direct_path, whatsapp_media.preview_direct_path),
			preview_sha256 = coalesce(excluded.preview_sha256, whatsapp_media.preview_sha256),
			preview_enc_sha256 = coalesce(excluded.preview_enc_sha256, whatsapp_media.preview_enc_sha256),
			preview_media_key = coalesce(excluded.preview_media_key, whatsapp_media.preview_media_key)`,
		w.Store, m.Chat, m.ID, m.At, db.NullStr(m.Sender), archive.B2I(m.FromMe), db.NullStr(f.MediaType), db.NullStr(name),
		db.NullStr(f.URL), db.NullStr(f.DirectPath), blobOrNil(f.MediaKey), blobOrNil(f.FileSHA256), blobOrNil(f.FileEncSHA256),
		nullLength(f.FileLength), db.NullStr(f.Path), db.NullStr(p.URL), db.NullStr(p.DirectPath), blobOrNil(p.SHA256),
		blobOrNil(p.EncSHA256), blobOrNil(p.Key))
}

func blobOrNil(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func nullLength(n uint64) any {
	if n == 0 {
		return nil
	}
	return int64(n)
}

// MessageOf is the archive's message of a chat's message (0 where it has none yet).
func (w *WhatsAppWriter) MessageOf(chat, id string) int64 {
	if mid, ok := w.A.IntOK("SELECT message_id FROM message_origin WHERE source_id = ? AND row_key = ?", w.Source, chat+"/"+id); ok {
		return mid
	}
	return w.message(id)
}

// mentions: whom a message names with @.
func (w *WhatsAppWriter) mentions(mid int64, jids []string) {
	for _, jid := range jids {
		if p, ok := w.person.of(jid); ok && mid != 0 {
			w.A.Exec("INSERT OR IGNORE INTO mention VALUES (?, ?, ?)", mid, w.A.Address(p), "@"+strings.SplitN(jid, "@", 2)[0])
		}
	}
}

// poll gives a poll its notice: each option's votes as they are now (WhatsApp names a chosen
// option by the SHA-256 of its text).
func (w *WhatsAppWriter) poll(mid int64, chat, id, poll string) {
	var p struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
		Multiple bool     `json:"multiple"`
	}
	if json.Unmarshal([]byte(poll), &p) != nil {
		return
	}
	var votes [][]string
	db.Each(w.Keys, "SELECT options FROM whatsapp_vote WHERE store = ? AND chat_jid = ? AND poll_id = ? ORDER BY voter",
		[]any{w.Store, chat, id}, func(scan func(...any)) {
			var js string
			scan(&js)
			var chosen []string
			if json.Unmarshal([]byte(js), &chosen) == nil {
				votes = append(votes, chosen)
			}
		})
	counts := map[string]int64{}
	voters := 0
	for _, v := range votes {
		if len(v) > 0 {
			voters++
		}
		for _, h := range v {
			counts[h]++
		}
	}
	options := []any{}
	for _, o := range p.Options {
		sum := sha256.Sum256([]byte(o))
		opt := pollOption(o, counts[hex.EncodeToString(sum[:])])
		if len(votes) == 0 { // no vote seen (made before linking, or unreadable): not known
			opt["votes"] = nil
		}
		options = append(options, opt)
	}
	n := &archive.Notice{Code: "poll", Args: map[string]any{"question": p.Question, "options": options,
		"multiple": p.Multiple, "voters": voters}}
	if setNoticeIfChanged(w.A, mid, n) {
		w.Updated["poll"]++
	}
}

// Vote keeps a voter's newest vote and counts the poll's votes again, where the archive has it.
func (w *WhatsAppWriter) Vote(v WAVote) {
	js, _ := json.Marshal(v.Options)
	if v.Options == nil {
		js = []byte("[]")
	}
	db.Exec(w.Keys, `INSERT INTO whatsapp_vote VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (store, chat_jid, poll_id, voter)
		DO UPDATE SET options = excluded.options, at = excluded.at WHERE excluded.at >= whatsapp_vote.at`,
		w.Store, v.Chat, v.Poll, v.Voter, string(js), v.At)
	mid := w.message(v.Poll)
	if mid == 0 {
		return
	}
	var poll sql.NullString
	if w.A.Row("SELECT n.args FROM notice n WHERE n.message_id = ? AND n.code = 'poll'", []any{mid}, &poll) && poll.Valid {
		var args struct {
			Question string           `json:"question"`
			Options  []map[string]any `json:"options"`
			Multiple bool             `json:"multiple"`
		}
		if json.Unmarshal([]byte(poll.String), &args) == nil {
			names := []string{}
			for _, o := range args.Options {
				t, _ := o["text"].(string)
				names = append(names, t)
			}
			js, _ := json.Marshal(map[string]any{"question": args.Question, "options": names, "multiple": args.Multiple})
			w.poll(mid, v.Chat, v.Poll, string(js))
		}
	}
}

// pend keeps what names a message the archive does not have yet; the newest of each kind by
// each person stays.
func (w *WhatsAppWriter) pend(chat, target, kind, sender string, fromMe bool, data any, at int64) {
	var js any
	if data != nil {
		b, _ := json.Marshal(data)
		js = string(b)
	}
	db.Exec(w.Keys, `INSERT INTO whatsapp_pending VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (store, chat_jid, target, kind, sender) DO UPDATE SET from_me = excluded.from_me, data = excluded.data,
		at = excluded.at WHERE excluded.at >= whatsapp_pending.at`, w.Store, chat, target, kind, sender, archive.B2I(fromMe), js, at)
}

// React brings a message's reactions to what someone's says now (one per person: changed, added,
// or removed once taken back).
func (w *WhatsAppWriter) React(r WAReaction) {
	mid := w.message(r.Target)
	if mid == 0 {
		w.pend(r.Chat, r.Target, "react", r.Sender, r.FromMe, map[string]any{"emoji": r.Emoji, "history": r.History}, r.At)
		return
	}
	a := w.A
	mine := r.FromMe
	var who int64
	if !mine {
		p, ok := w.person.of(r.Sender)
		mine = ok && w.own[p]
		if !mine && ok {
			who = a.Address(p)
		}
	}
	if !mine && who == 0 {
		return
	}
	var oldID int64
	var oldEmoji sql.NullString
	var found bool
	if mine {
		found = a.Row("SELECT rowid, emoji FROM reaction WHERE message_id = ? AND outgoing = 1", []any{mid}, &oldID, &oldEmoji)
	} else {
		found = a.Row("SELECT rowid, emoji FROM reaction WHERE message_id = ? AND address_id = ?", []any{mid, who}, &oldID, &oldEmoji)
	}
	switch {
	case found && r.History: // what came live is newer
	case r.Emoji == "":
		if found {
			a.Exec("DELETE FROM reaction WHERE rowid = ?", oldID)
			w.Updated["reactions"]++
		}
	case !found:
		var o any
		if mine {
			o = 1
		}
		a.Exec("INSERT INTO reaction (message_id, emoji, count, address_id, outgoing) VALUES (?, ?, 1, ?, ?)",
			mid, r.Emoji, archive.NullID(who), o)
		w.Updated["reactions"]++
	case !oldEmoji.Valid || oldEmoji.String != r.Emoji:
		a.Exec("UPDATE reaction SET emoji = ?, code = NULL WHERE rowid = ?", r.Emoji, oldID)
		w.Updated["reactions"]++
	}
}

// Change applies an edit or a deletion where the one who made it wrote the message (or, a
// deletion in a group, an admin: any member's is taken, as WhatsApp's apps do); a deletion for the
// user alone, of any message of the chat.
func (w *WhatsAppWriter) Change(c WAChange) {
	a := w.A
	mid := w.message(c.Target)
	if mid == 0 {
		data := map[string]any{"alt": c.Alt, "sender_alt": c.SenderAlt}
		if c.Change == "edit" {
			data["text"], data["mentions"] = c.Text, c.Mentions
		}
		w.pend(c.Chat, c.Target, c.Change, c.Sender, c.FromMe, data, c.At)
		return
	}
	var outgoing bool
	var sender sql.NullInt64
	var conv int64
	a.Row("SELECT outgoing, sender_id, conversation_id FROM message WHERE id = ?", []any{mid}, &outgoing, &sender, &conv)
	inChat := false
	for _, chat := range append([]string{c.Chat}, c.Alt...) {
		found, _ := a.FindConversation("whatsapp", w.person.conversationKey(chat))
		inChat = inChat || found != 0 && found == conv
	}
	if !inChat {
		return // a message of another chat (ids are not unique across chats)
	}
	apply := func(ch Change) {
		for _, k := range ApplyChange(a, mid, ch) {
			w.Updated[k]++
		}
	}
	if c.Change == "delete_me" {
		apply(Change{Deleted: true})
		return
	}
	deletion := c.Change == "delete"
	group := strings.HasSuffix(c.Chat, "@g.us")
	author := outgoing && c.FromMe
	if !outgoing && !c.FromMe {
		for _, jid := range append([]string{c.Sender}, c.SenderAlt...) {
			if h, ok := w.person.of(jid); ok && a.Known(h) { // (asked of the archive, not made in it)
				author = author || sender.Valid && a.Address(h) == sender.Int64
			}
		}
		author = author || (!group && !sender.Valid)
	}
	if !author && !(deletion && group) {
		return
	}
	if deletion {
		apply(Change{Deleted: true})
		return
	}
	t := c.Text
	apply(Change{Text: &t, Edited: true})
	w.mentions(mid, c.Mentions)
}

// Receipt: who got, read and played the owner's messages, and when (the first time each).
func (w *WhatsAppWriter) Receipt(r WAReceipt) {
	if r.Kind != "delivered" && r.Kind != "read" && r.Kind != "played" {
		return
	}
	mid := w.message(r.Target)
	if mid == 0 {
		w.pend(r.Chat, r.Target, r.Kind, r.Who, false, nil, r.At)
		return
	}
	p, ok := w.person.of(r.Who)
	if !ok || w.own[p] {
		return
	}
	a := w.A
	aid := a.Address(p)
	a.Exec("INSERT OR IGNORE INTO receipt (message_id, address_id) SELECT id, ? FROM message WHERE id = ? AND outgoing", aid, mid)
	a.Exec(fmt.Sprintf("UPDATE receipt SET %[1]s_at = ? WHERE message_id = ? AND address_id = ? "+
		"AND (coalesce(%[1]s_at, 0) = 0 OR (%[1]s_at > ? AND ? > 0))", r.Kind), r.At, mid, aid, r.At, r.At)
}

// Read: the owner read a chat up to its newest of these messages, as any of their devices said.
func (w *WhatsAppWriter) Read(r WARead) {
	// by the messages' own chats: the receipt's may be the other jid (a LID for a number) of theirs
	newest := map[int64]int64{}
	var convs []int64
	for _, id := range r.IDs {
		mid := w.message(id)
		if mid == 0 { // not here yet: when it comes
			w.pend(r.Chat, id, "self_read", "", false, nil, r.At)
			continue
		}
		var conv, ts int64
		var outgoing bool
		w.A.Row("SELECT conversation_id, ts, outgoing FROM message WHERE id = ?", []any{mid}, &conv, &ts, &outgoing)
		if outgoing {
			continue
		}
		if _, seen := newest[conv]; !seen {
			convs = append(convs, conv)
		}
		newest[conv] = max(newest[conv], ts)
	}
	for _, conv := range convs {
		w.A.ReportState(w.Source, conv, "read_until", newest[conv], r.At, r.At)
	}
}

// Notice writes a chat's event as a notice of its own.
func (w *WhatsAppWriter) Notice(n WANotice) {
	conv := w.conv(n.Chat)
	if conv == 0 {
		return
	}
	args := map[string]any{}
	for k, v := range n.Args {
		args[k] = v
	}
	var senderID int64
	args["by"] = map[string]any{"self": true}
	if !n.FromMe {
		args["by"] = nil
		if h, ok := w.person.of(n.Sender); ok {
			args["by"] = NoticePerson(w.A, h, w.own)
			if !w.own[h] {
				senderID = w.A.Address(h)
			}
		}
	}
	x := &archive.Extras{Notice: &archive.Notice{Code: n.Code, Args: args}}
	if n.Code == "pin" || n.Code == "unpin" {
		x.Subtype, x.SubtypeCode = "pin", "whatsmeow:"+n.Code
	} else {
		x.Subtype, x.SubtypeCode = "notice", "whatsmeow:"+n.Code
	}
	x.ReplyKey = n.Target
	rowKey := n.Chat + "/" + n.ID
	if w.message(n.ID) != 0 || w.A.HasOrigin(w.Source, rowKey, "") {
		return
	}
	w.A.AddMessage(w.Source, rowKey, archive.Message{Service: "whatsapp", ConversationID: conv, TS: n.At, Outgoing: n.FromMe,
		SenderID: senderID, Kind: "system", Key: n.ID, Extras: x})
	w.Added++
}

// GroupEvent writes a group's change as a notice; the iPhone's copy of the same change (its own
// row, a few seconds apart) is kept instead, this one recorded as had by it.
func (w *WhatsAppWriter) GroupEvent(g WAGroupEvent) {
	a := w.A
	conv := w.conv(g.Group)
	if conv == 0 {
		return
	}
	var acts []map[string]any
	if json.Unmarshal([]byte(g.Actions), &acts) != nil || len(acts) == 0 {
		return
	}
	sum := sha1.Sum([]byte(g.Actions))
	key := fmt.Sprintf("group_event:%s:%d:%x", g.Group, g.At, sum[:6])
	if g.OncePerDay && a.Exists("SELECT 1 FROM message_origin o JOIN message m ON m.id = o.message_id "+
		"WHERE o.source_id = ? AND o.row_key LIKE ? ESCAPE '\\' AND m.ts > ?", w.Source,
		likeEscape("group_event:"+g.Group+":")+"%"+fmt.Sprintf(":%x", sum[:6]), time.Now().Add(-24*time.Hour).UnixMilli()) {
		return
	}
	if a.HasOrigin(w.Source, key, "") {
		return
	}
	who := func(jid string) map[string]any {
		if h, ok := w.person.of(jid); ok {
			return NoticePerson(a, h, w.own)
		}
		return nil
	}
	for _, x := range acts {
		if j, ok := x["who"].(string); ok {
			x["who"] = who(j)
		}
	}
	by := who(g.Sender)
	outgoing := by != nil && by["self"] == true
	var senderID int64
	if h, ok := w.person.of(g.Sender); ok && !outgoing {
		senderID = a.Address(h)
	}
	if mid, ok := a.IntOK("SELECT id FROM message WHERE conversation_id = ? AND subtype = 'group event' "+
		"AND subtype_code = 'whatsapp:6' AND abs(ts - ?) <= 5000", conv, g.At); ok {
		a.Exec("INSERT OR IGNORE INTO message_origin (source_id, row_key, message_id) VALUES (?, ?, ?)", w.Source, key, mid)
		return
	}
	a.AddMessage(w.Source, key, archive.Message{Service: "whatsapp", ConversationID: conv, TS: g.At, Outgoing: outgoing,
		SenderID: senderID, Kind: "system", Key: key, Extras: &archive.Extras{Subtype: "group event",
			SubtypeCode: "whatsmeow:group_event", Notice: groupNotice(by, acts...)}})
	w.Added++
}

// likeEscape keeps a text literal in a LIKE pattern (ESCAPE '\').
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

// callSet is the calls of the archive this writer adds to.
func (w *WhatsAppWriter) callSet() *CallSet {
	if w.calls == nil {
		w.calls = NewCallSet(w.A)
	}
	return w.calls
}

// callPeer is the address of the other side of a person's call (0 for a group's, or none).
func (w *WhatsAppWriter) callPeer(jid string) int64 {
	if jid == "" {
		return 0
	}
	return w.sender(jid)
}

// CallLog writes a call as its call-log message says it, with a group call's members.
func (w *WhatsAppWriter) CallLog(c WACallLog) {
	a := w.A
	src := a.Source(whatsappCallLogs, w.Store, WhatsAppSource, "")
	a.Imported(src)
	var conv, peer int64
	if c.Group {
		conv, _ = a.FindConversation("whatsapp", c.Chat)
	} else {
		peer = w.callPeer(c.Chat)
	}
	detail := bridgeOutcomes[c.Outcome]
	if c.Outcome == "MISSED" && c.FromMe {
		detail = "unanswered"
	}
	code := ""
	if detail != "" {
		code = "whatsmeow:" + c.Outcome
	}
	callID := w.callSet().add(src, []string{c.ID}, archive.Call{Service: "whatsapp", AddressID: peer, TS: c.At,
		Outgoing: c.FromMe, Answered: c.Outcome == "CONNECTED", Duration: c.Duration, Detail: detail,
		DetailCode: code, Video: c.Video, ConversationID: conv})
	if callID == 0 || !c.Group {
		return
	}
	for _, p := range c.Participants {
		said := p[1]
		member := bridgeOutcomes[said]
		if said == "CONNECTED" {
			member = "joined"
		}
		var code any
		if said != "" {
			code = "whatsmeow:" + said
		}
		a.Exec("INSERT OR IGNORE INTO call_member VALUES (?, ?, ?, ?)", callID, archive.NullID(w.callPeer(p[0])),
			archive.NullStr(member), code)
	}
}

// CallSignal follows a call's signalling (kept in keys.db while it goes on) and writes the call
// once it ended.
func (w *WhatsAppWriter) CallSignal(s WACallSignal) {
	switch s.Kind {
	case "offer":
		db.Exec(w.Keys, `INSERT OR IGNORE INTO whatsapp_call (store, id, chat_jid, creator, from_me, is_group, video, at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, w.Store, s.ID, s.Chat, s.Creator, archive.B2I(s.FromMe), archive.B2I(s.Group),
			archive.B2I(s.Video), s.At)
	case "accept":
		db.Exec(w.Keys, "UPDATE whatsapp_call SET accepted_at = coalesce(accepted_at, ?) WHERE store = ? AND id = ?",
			s.At, w.Store, s.ID)
	case "reject":
		db.Exec(w.Keys, "UPDATE whatsapp_call SET ended_at = coalesce(ended_at, ?), end_reason = coalesce(end_reason, 'reject') "+
			"WHERE store = ? AND id = ?", s.At, w.Store, s.ID)
	case "terminate":
		db.Exec(w.Keys, "UPDATE whatsapp_call SET ended_at = coalesce(ended_at, ?), end_reason = coalesce(end_reason, ?) "+
			"WHERE store = ? AND id = ?", s.At, s.Reason, w.Store, s.ID)
	}
	w.settleCalls(s.ID)
}

// settleCalls writes the call whose signalling ended; id "": all those that ended, and those whose
// end was never seen (the connection was down then), an hour on, as they stood with no end; one
// answered waits for its end, however long (its end, when seen, says how long).
func (w *WhatsAppWriter) settleCalls(id string) {
	a := w.A
	type call struct {
		id, chat, creator, reason string
		fromMe, group, video      bool
		at                        int64
		accepted, ended           sql.NullInt64
	}
	var calls []call
	query := "SELECT id, coalesce(chat_jid, ''), coalesce(creator, ''), from_me, is_group, video, at, accepted_at, ended_at, " +
		"coalesce(end_reason, '') FROM whatsapp_call WHERE store = ? AND (ended_at IS NOT NULL OR (accepted_at IS NULL AND at < ?))"
	args := []any{w.Store, time.Now().Add(-time.Hour).UnixMilli()}
	if id != "" {
		query = "SELECT id, coalesce(chat_jid, ''), coalesce(creator, ''), from_me, is_group, video, at, accepted_at, ended_at, " +
			"coalesce(end_reason, '') FROM whatsapp_call WHERE store = ? AND ended_at IS NOT NULL AND id = ?"
		args = []any{w.Store, id}
	}
	db.Each(w.Keys, query, args, func(scan func(...any)) {
		var c call
		scan(&c.id, &c.chat, &c.creator, &c.fromMe, &c.group, &c.video, &c.at, &c.accepted, &c.ended, &c.reason)
		calls = append(calls, c)
	})
	if len(calls) == 0 {
		return
	}
	src := a.Source(whatsappCallSignals, w.Store, WhatsAppSource, "")
	a.Imported(src)
	for _, c := range calls {
		var conv, peer int64
		if c.group {
			conv, _ = a.FindConversation("whatsapp", c.chat)
		} else {
			peer = w.callPeer(c.chat)
		}
		wasAccepted := c.accepted.Valid && c.accepted.Int64 != 0
		ended, reason := c.ended.Int64, c.reason
		if !c.ended.Valid {
			ended, reason = c.accepted.Int64, "unended"
		}
		detail := ""
		if !wasAccepted {
			switch {
			case reason == "reject":
				detail = "rejected"
			case c.fromMe:
				detail = "unanswered"
			default:
				detail = "missed"
			}
		}
		code := ""
		if detail != "" && reason != "" {
			code = "whatsmeow:" + reason
		}
		var duration int64
		if wasAccepted {
			duration = max(0, floorDiv(ended-c.accepted.Int64, 1000))
		}
		if peer == 0 && !c.group {
			peer = w.callPeer(c.creator)
		}
		w.callSet().add(src, []string{c.id}, archive.Call{Service: "whatsapp", AddressID: peer, TS: c.at, Outgoing: c.fromMe,
			Answered: wasAccepted, Duration: duration, Key: c.id, Detail: detail, DetailCode: code, Video: c.video,
			ConversationID: conv})
		db.Exec(w.Keys, "DELETE FROM whatsapp_call WHERE store = ? AND id = ?", w.Store, c.id)
	}
}

// Pending applies what waited for messages the archive has now: of these ids, or (none given)
// all that wait.
func (w *WhatsAppWriter) Pending(ids []string) {
	type row struct {
		chat, target, kind, sender string
		fromMe                     bool
		data                       sql.NullString
		at                         int64
	}
	var rows []row
	scan := func(scan func(...any)) {
		var r row
		scan(&r.chat, &r.target, &r.kind, &r.sender, &r.fromMe, &r.data, &r.at)
		rows = append(rows, r)
	}
	const cols = "SELECT chat_jid, target, kind, sender, from_me, data, at FROM whatsapp_pending WHERE store = ?"
	if ids == nil {
		db.Each(w.Keys, cols+" ORDER BY at", []any{w.Store}, scan)
	}
	for _, id := range ids {
		db.Each(w.Keys, cols+" AND target = ? ORDER BY at", []any{w.Store, id}, scan)
	}
	for _, r := range rows {
		if w.message(r.target) == 0 {
			continue
		}
		var d struct {
			Emoji     string   `json:"emoji"`
			History   bool     `json:"history"`
			Text      string   `json:"text"`
			Mentions  []string `json:"mentions"`
			Alt       []string `json:"alt"`
			SenderAlt []string `json:"sender_alt"`
		}
		json.Unmarshal([]byte(r.data.String), &d)
		switch r.kind {
		case "react":
			w.React(WAReaction{Chat: r.chat, Target: r.target, Sender: r.sender, FromMe: r.fromMe, Emoji: d.Emoji, At: r.at,
				History: d.History})
		case "edit", "delete", "delete_me":
			w.Change(WAChange{Chat: r.chat, Target: r.target, Change: r.kind, Sender: r.sender, FromMe: r.fromMe, Text: d.Text,
				Mentions: d.Mentions, At: r.at, Alt: d.Alt, SenderAlt: d.SenderAlt})
		case "delivered", "read", "played":
			w.Receipt(WAReceipt{Chat: r.chat, Target: r.target, Who: r.sender, Kind: r.kind, At: r.at})
		case "self_read":
			w.Read(WARead{Chat: r.chat, IDs: []string{r.target}, At: r.at})
		}
		w.done = append(w.done, []any{w.Store, r.chat, r.target, r.kind, r.sender})
	}
}

// Unreadable writes, as notices, the messages this device could not read that the phone has not
// sent since, a day on (it may still send them: a day, as for Signal's).
func (w *WhatsAppWriter) Unreadable() {
	type trace struct {
		chat, id, sender string
		fromMe           bool
		at               int64
	}
	var due []trace
	db.Each(w.Keys, "SELECT chat_jid, id, coalesce(sender, ''), from_me, at FROM whatsapp_unreadable WHERE store = ? AND at < ?",
		[]any{w.Store, time.Now().Add(-24 * time.Hour).UnixMilli()}, func(scan func(...any)) {
			var t trace
			scan(&t.chat, &t.id, &t.sender, &t.fromMe, &t.at)
			due = append(due, t)
		})
	for _, t := range due {
		if w.message(t.id) == 0 {
			w.Notice(WANotice{Chat: t.chat, ID: "undecryptable:" + t.id, Sender: t.sender, FromMe: t.fromMe, At: t.at,
				Code: "unreadable", Args: map[string]any{}})
		}
		db.Exec(w.Keys, "DELETE FROM whatsapp_unreadable WHERE store = ? AND id = ?", w.Store, t.id)
	}
}

// PreviewImage gives a message's link preview its high-quality picture, downloaded after
// the message was written.
func (w *WhatsAppWriter) PreviewImage(mid int64, link string, img []byte) bool {
	p := archive.Preview{URL: link, Image: img}
	var u, site, title, desc *string
	if w.A.Row("SELECT url, site, title, description FROM link_preview WHERE message_id = ?", []any{mid},
		&u, &site, &title, &desc) {
		p.URL, p.Site, p.Title, p.Description = deref(u), deref(site), deref(title), deref(desc)
	}
	return w.A.SetPreview(mid, &p)
}
