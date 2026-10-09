// Package archive is the archive database: its schema and the helpers the importers share.
//
// One row per message whatever its source; `message_origin` records which source row it came from
// (so that a later import knows what it has). Our ids are our own; `message.key` is the service's
// own id where it has one (Viber token, WhatsApp stanza id, iMessage guid), unique per service, or
// per conversation for services whose ids are only unique within a chat (`service.key_scope`;
// `message.key_scope` is then the conversation). Messages without a key carry a `fingerprint`
// (time, direction, kind and text), the way to tell a message seen before in sources without ids.
// What a message carries beyond its text (the message it answers, reactions, edits, deletions,
// forwarding, a star, a place) is in its own columns and in `reaction`, read by package extras;
// the source rows themselves are not kept: they are in the sources.
//
// People: an `address` is one handle (a phone number or email, shared by every service, or an id,
// username or name within one service); a `person` has one or more addresses, and may point to a
// contact elsewhere. The owner's own handles are `account` rows, seeded from config [owner] numbers.
//
// Sources belong to a `device`, whose period of use decides which copy of a record found on two
// devices is kept; each source names the folder its media paths are relative to.
//
// Search: `message_fts` (by words) and `message_tri` (by trigrams, for parts of words) hold each
// message's text folded (text.Fold), written by AddMessage, not by a trigger.
//
// The schema has a version (`PRAGMA user_version`), 1 until the first release: until then it
// changes in place, without migrations.
//
// Errors: the importers' work is a long run of statements, as in a script; a failing statement
// panics with an *Error, which Run (and the importers' callers) turn back into an error.
package archive

import (
	"context"
	"crypto/sha1"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/text"
)

//go:embed schema.sql
var Schema string

const (
	AppleEpoch    = 978307200
	SchemaVersion = 1 // until the first release: the schema changes in place (no migrations yet)
)

var (
	Services = []string{"sms", "mms", "imessage", "rcs", "viber", "whatsapp", "phone", "facetime", "telegram",
		"messenger", "signal"}
	KeyPerConversation = []string{"telegram"} // message ids unique only within a chat
	// phone, email, sender (an SMS sender name), uri: the same for every service; id, username,
	// name: within one service.
	AddressKinds = []string{"phone", "email", "sender", "id", "username", "name", "uri"}
	SharedKinds  = map[string]bool{"phone": true, "email": true, "sender": true, "uri": true}
	MessageKinds = []string{"text", "image", "video", "voice", "file", "sticker", "location", "contact",
		"call", "system", "reaction"}
	// What message.subtype, call.detail and call_member.outcome may say.
	vocabulary = map[string][]string{
		"message.subtype": {"link", "gif", "video note", "deleted", "notice", "call", "invalid",
			"group event", "poll", "pin", "location", "tapback"},
		"call.detail":         {"missed", "unanswered", "rejected", "blocked", "busy", "failed"},
		"call_member.outcome": {"joined", "missed", "unanswered", "rejected", "busy", "failed"},
	}
)

// VocabularyOf is what a field may say.
func VocabularyOf(field string) []string { return vocabulary[field] }

// Label is one the app brings, as the archive starts with them.
type Label struct {
	Kind, Key, Meaning string
	Sensitive          int
}

var Labels = []Label{
	{"tone", "friendly", "friendly and casual: friends, company, jokes", 0},
	{"tone", "family", "family matters between relatives", 0},
	{"tone", "personal", "close and caring, personal matters, but not romantic", 0},
	{"tone", "romantic", "flirting, a love affair or a partner, and explicit sexual talk", 1},
	{"tone", "professional", "work: colleagues, partners, projects", 0},
	{"tone", "transactional", "arranging a purchase or a service: shops, craftsmen, doctors, bookings", 0},
	{"tone", "formal", "polite and distant", 0},
	{"tone", "conflict", "quarrels, complaints, anger", 1},
	{"tone", "automated", "mass or automatic messages: adverts, notifications, bots", 0},
	{"relation", "friend", "a friend", 0},
	{"relation", "relative", "a relative", 0},
	{"relation", "partner", "a partner or lover, now or once", 0},
	{"relation", "colleague", "a colleague or business partner", 0},
	{"relation", "client", "a client or a supplier", 0},
	{"relation", "acquaintance", "an acquaintance", 0},
	{"relation", "service", "a company, a service or a bot", 0},
}

// Error is a failed statement, carried by a panic out of the importers' work.
type Error = db.Error

// Recover turns an archive panic back into an error: `defer archive.Recover(&err)`.
func Recover(err *error) {
	if r := recover(); r != nil {
		if e, ok := r.(error); ok {
			*err = e
			return
		}
		panic(r)
	}
}

// DB is the archive's path.
func DB() string { return filepath.Join(config.Data, "archive.db") }

// IphoneData is where the iPhone's decrypted databases and new media are (made again by each sync).
func IphoneData() string { return filepath.Join(config.Cache, "iphone") }

// MediaRoot holds media/<ab>/<sha256><ext>: in the data folder (some exist nowhere else).
func MediaRoot() string { return config.MediaStore }

// Iphone and Android are the devices' names source names start with ('iphone/sms').
func Iphone() string  { return config.String("iphone", "device", "iphone") }
func Android() string { return config.String("android", "device", "android") }

// Handle is one handle as the importers make it: kind, value, and the service for the kinds
// that live within one.
type Handle struct{ Kind, Value, Service string }

// H is a handle of a shared kind (phone, email, sender, uri) or, with a service, of one within it.
func H(kind, value string, service ...string) Handle {
	h := Handle{Kind: kind, Value: value}
	if len(service) > 0 {
		h.Service = service[0]
	}
	return h
}

func (h Handle) norm() Handle {
	if SharedKinds[h.Kind] {
		h.Service = ""
	}
	return h
}

// Names maps the names of a lookup table to ids; a name not there yet is added.
type Names struct {
	a     *Archive
	table string
	ids   map[string]int64
}

func (n *Names) ID(name string) int64 {
	if id, ok := n.ids[name]; ok {
		return id
	}
	n.a.Exec("INSERT OR IGNORE INTO "+n.table+" (name) VALUES (?)", name)
	id := n.a.Int("SELECT id FROM "+n.table+" WHERE name = ?", name)
	n.ids[name] = id
	return id
}

// Version is the schema version (PRAGMA user_version): 0 for an empty database.
func Version(d interface {
	QueryRow(string, ...any) *sql.Row
}) int {
	var v int
	d.QueryRow("PRAGMA user_version").Scan(&v)
	return v
}

type pendingTapback struct {
	service          int64
	key, emoji, code string
	sender           any
	outgoing         int
}

// Archive is the archive as the importers write it: one connection, statements inside a
// transaction that opens by itself on the first change and ends at Commit.
type Archive struct {
	Path  string
	DB    *sql.DB // for reading what is committed; the importer's statements go through Exec and the rest
	conn  *sql.Conn
	inTx  bool
	stmts map[string]*sql.Stmt

	Service, AddressKind, MessageKind *Names
	keyScoped                         map[int64]bool
	addresses                         map[Handle]int64
	conversations                     map[string]int64
	devices                           map[string][2]sql.NullInt64
	pendingEdits                      [][2]any
	pendingTapbacks                   []pendingTapback
	pendingFiles                      []string // media files to delete once committed (PurgeSpam's)
}

// Open opens (making it if new) the archive at path.
func Open(path string) (a *Archive, err error) {
	defer Recover(&err)
	if path == "" {
		path = DB()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	d, err := db.Open(path, "journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(2) // the importer's own connection, and one for reading what is committed
	if v := Version(d); v != 0 && v != SchemaVersion {
		d.Close()
		return nil, fmt.Errorf("%s has an unknown schema (v%d, known: v%d)", path, v, SchemaVersion)
	}
	if _, err := d.Exec(Schema); err != nil {
		d.Close()
		return nil, err
	}
	a = &Archive{Path: path, DB: d, stmts: map[string]*sql.Stmt{}, addresses: map[Handle]int64{}, conversations: map[string]int64{}}
	a.seed()
	a.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion))
	a.Service = a.names("service")
	a.AddressKind = a.names("address_kind")
	a.MessageKind = a.names("message_kind")
	a.keyScoped = map[int64]bool{}
	for _, id := range a.Ints("SELECT id FROM service WHERE key_scope = 'conversation'") {
		a.keyScoped[id] = true
	}
	for _, number := range config.OwnNumbers {
		k, v := Address(number, config.Region)
		a.Account(H(k, v), "", "")
	}
	a.Commit()
	return a, nil
}

func (a *Archive) names(table string) *Names {
	n := &Names{a: a, table: table, ids: map[string]int64{}}
	rows := a.Query("SELECT name, id FROM " + table)
	defer rows.Close()
	for rows.Next() {
		var name string
		var id int64
		rows.Scan(&name, &id)
		n.ids[name] = id
	}
	return n
}

// seed puts the lookup tables' rows, and the labels once (what the user removed does not come back).
func (a *Archive) seed() {
	for _, t := range []struct {
		table string
		names []string
	}{{"service", Services}, {"address_kind", AddressKinds}, {"message_kind", MessageKinds}} {
		for _, n := range t.names {
			a.Exec("INSERT OR IGNORE INTO "+t.table+" (name) VALUES (?)", n)
		}
	}
	for _, n := range KeyPerConversation {
		a.Exec("UPDATE service SET key_scope = 'conversation' WHERE name = ?", n)
	}
	for _, f := range []string{"message.subtype", "call.detail", "call_member.outcome"} {
		for _, n := range vocabulary[f] {
			a.Exec("INSERT OR IGNORE INTO vocabulary VALUES (?, ?)", f, n)
		}
	}
	if n, _ := a.Exec("INSERT OR IGNORE INTO setting VALUES ('labels_seeded', 'true')").RowsAffected(); n > 0 {
		for i, l := range Labels {
			a.Exec("INSERT OR IGNORE INTO label (kind, key, sensitive, position) VALUES (?, ?, ?, ?)", l.Kind, l.Key, l.Sensitive, i)
		}
	}
}

// --- statements ----------------------------------------------------------------------------------

// begin opens a transaction on the archive's one connection, if none is open (as Python's sqlite3
// does before a change), and gives that connection with its statements kept prepared: an importer
// runs the same few statements millions of times. IMMEDIATE: one that began with a read and then
// writes would fail (SQLITE_BUSY_SNAPSHOT, which busy_timeout does not wait out) when another
// connection (the server's, another plugin's) committed in between.
func (a *Archive) begin() db.Querier {
	if a.conn == nil {
		c, err := a.DB.Conn(context.Background())
		if err != nil {
			panic(&Error{Query: "connect", Err: err})
		}
		a.conn = c
	}
	if !a.inTx {
		if _, err := a.conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
			panic(&Error{Query: "BEGIN IMMEDIATE", Err: err})
		}
		a.inTx = true
	}
	return stmtConn{a}
}

func (a *Archive) q() db.Querier { return a.begin() }

type stmtConn struct{ a *Archive }

func (c stmtConn) stmt(q string) *sql.Stmt {
	st, ok := c.a.stmts[q]
	if !ok {
		var err error
		if st, err = c.a.conn.PrepareContext(context.Background(), q); err != nil {
			panic(&Error{Query: q, Err: err})
		}
		c.a.stmts[q] = st
	}
	return st
}

func (c stmtConn) Exec(q string, args ...any) (sql.Result, error) { return c.stmt(q).Exec(args...) }
func (c stmtConn) Query(q string, args ...any) (*sql.Rows, error) { return c.stmt(q).Query(args...) }
func (c stmtConn) QueryRow(q string, args ...any) *sql.Row        { return c.stmt(q).QueryRow(args...) }

// Exec runs a change (inside the open transaction).
func (a *Archive) Exec(q string, args ...any) sql.Result { return db.Exec(a.q(), q, args...) }

// Query runs a question (inside the open transaction, which sees its own changes).
func (a *Archive) Query(q string, args ...any) *db.Rows { return db.Query(a.q(), q, args...) }

// Each calls fn for each row of a question.
func (a *Archive) Each(q string, args []any, fn func(scan func(dest ...any))) {
	db.Each(a.q(), q, args, fn)
}

// Row is one row's values into dest; false when there is none.
func (a *Archive) Row(q string, args []any, dest ...any) bool { return db.Row(a.q(), q, args, dest...) }

// Int is the first column of the first row, or 0 when there is none (or it is NULL).
func (a *Archive) Int(q string, args ...any) int64 { return db.Int(a.q(), q, args...) }

// IntOK is the first column of the first row, and whether there was a row with a value.
func (a *Archive) IntOK(q string, args ...any) (int64, bool) { return db.IntOK(a.q(), q, args...) }

// Ints is the first column of every row.
func (a *Archive) Ints(q string, args ...any) []int64 { return db.Ints(a.q(), q, args...) }

// Exists says whether the question has a row.
func (a *Archive) Exists(q string, args ...any) bool { return db.Exists(a.q(), q, args...) }

// Tx is the open transaction (opened if none is), for the db helpers.
func (a *Archive) Tx() db.Querier { return a.begin() }

// Commit ends the open transaction, if any.
func (a *Archive) Commit() {
	if a.inTx {
		_, err := a.conn.ExecContext(context.Background(), "COMMIT")
		a.inTx = false
		if err != nil {
			panic(&Error{Query: "COMMIT", Err: err})
		}
		RemoveMediaFiles(a.pendingFiles)
		a.pendingFiles = nil
	}
}

// Rollback drops the open transaction's changes.
func (a *Archive) Rollback() {
	if a.inTx {
		a.conn.ExecContext(context.Background(), "ROLLBACK")
		a.inTx = false
		a.pendingFiles = nil
	}
}

func (a *Archive) Close() error {
	a.Rollback()
	for _, st := range a.stmts {
		st.Close()
	}
	if a.conn != nil {
		a.conn.Close()
	}
	return a.DB.Close()
}

// --- sources and devices -------------------------------------------------------------------------

// Source is the source's id; device: the device it was read from (registered when new).
func (a *Archive) Source(name, path, device, mediaRoot string) int64 {
	var deviceID any
	if device != "" {
		deviceID = a.Device(device, "")
	}
	var root any
	if mediaRoot != "" {
		root = Contract(mediaRoot)
	}
	a.Exec("INSERT INTO source (name, path, device_id, media_root) VALUES (?, ?, ?, ?) "+
		"ON CONFLICT (name) DO UPDATE SET path = excluded.path, "+
		"device_id = coalesce(source.device_id, excluded.device_id), "+
		"media_root = coalesce(excluded.media_root, source.media_root)", name, path, deviceID, root)
	return a.Int("SELECT id FROM source WHERE name = ?", name)
}

func (a *Archive) Device(name, kind string) int64 {
	a.Exec("INSERT OR IGNORE INTO device (name, kind) VALUES (?, ?)", name, nullStr(kind))
	a.devices = nil
	return a.Int("SELECT id FROM device WHERE name = ?", name)
}

// Keeper is, of devices that hold copies of one record, the one whose copy is kept: the one in use
// at ts (Unix ms), else the most recent one (it carries the history copied from phone to phone),
// else the first named.
func (a *Archive) Keeper(devices []string, ts int64) string {
	if a.devices == nil {
		a.devices = map[string][2]sql.NullInt64{}
		rows := a.Query("SELECT name, used_from, used_until FROM device")
		for rows.Next() {
			var n string
			var f, u sql.NullInt64
			rows.Scan(&n, &f, &u)
			a.devices[n] = [2]sql.NullInt64{f, u}
		}
		rows.Close()
	}
	for _, d := range devices {
		p := a.devices[d]
		start, until := p[0], p[1]
		if (start.Valid || until.Valid) && start.Int64 <= ts && (!until.Valid || ts < until.Int64) {
			return d
		}
	}
	best, found := "", false
	var bestStart int64
	for _, d := range devices {
		if s := a.devices[d][0]; s.Valid && (!found || s.Int64 > bestStart || (s.Int64 == bestStart && d > best)) {
			best, bestStart, found = d, s.Int64, true
		}
	}
	if found {
		return best
	}
	return devices[0]
}

func (a *Archive) Imported(sourceID int64) {
	a.Exec("UPDATE source SET imported_at = ? WHERE id = ?", time.Now().Unix(), sourceID)
}

// HasOrigin says whether the source's row is in the archive (table: message_origin or call_origin).
func (a *Archive) HasOrigin(sourceID int64, rowKey string, table string) bool {
	if table == "" {
		table = "message_origin"
	}
	return a.Exists("SELECT 1 FROM "+table+" WHERE source_id = ? AND row_key = ?", sourceID, rowKey)
}

// Origin is a source row: the source's name and the row's key there.
type Origin struct{ Source, RowKey string }

// RecordPairs: a record found on both phones is kept once; its other copy is recorded as a second
// origin of the same row, so later imports know it without reading the other phone again.
// sources: source name -> id.
func (a *Archive) RecordPairs(sources map[string]int64, pairs [][2]Origin, table string) {
	if table == "" {
		table = "message_origin"
	}
	for _, p := range pairs {
		kept, other := p[0], p[1]
		var sid int64
		var rk string
		var rid int64
		if a.Row("SELECT source_id, row_key, "+strings.TrimSuffix(table, "_origin")+"_id FROM "+table+" WHERE source_id = ? AND row_key = ?",
			[]any{sources[kept.Source], kept.RowKey}, &sid, &rk, &rid) {
			a.Exec("INSERT OR IGNORE INTO "+table+" VALUES (?, ?, ?)", sources[other.Source], other.RowKey, rid)
		}
	}
}

// --- people --------------------------------------------------------------------------------------

func (a *Archive) serviceID(service string) any {
	if service == "" {
		return nil
	}
	return a.Service.ID(service)
}

// Address is the id of a handle; the service only for the kinds that live within one (id,
// username, name). A new handle is a new person, until the owner merges people.
func (a *Archive) Address(h Handle) int64 {
	h = h.norm()
	if !SharedKinds[h.Kind] && h.Service == "" {
		panic(&Error{Query: "address", Err: fmt.Errorf("an address of kind %s needs its service", h.Kind)})
	}
	if id, ok := a.addresses[h]; ok {
		return id
	}
	kid, sid := a.AddressKind.ID(h.Kind), a.serviceID(h.Service)
	n, _ := a.Exec("INSERT OR IGNORE INTO address (kind_id, value, service_id) VALUES (?, ?, ?)", kid, h.Value, sid).RowsAffected()
	aid := a.Int("SELECT id FROM address WHERE kind_id = ? AND value = ? AND service_id IS ?", kid, h.Value, sid)
	if n > 0 {
		pid, _ := a.Exec("INSERT INTO person DEFAULT VALUES").LastInsertId()
		a.Exec("INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", aid, pid)
	}
	a.addresses[h] = aid
	return aid
}

// Known says whether the archive has this handle.
func (a *Archive) Known(h Handle) bool {
	h = h.norm()
	if _, ok := a.addresses[h]; ok {
		return true
	}
	return a.Exists("SELECT 1 FROM address WHERE kind_id = ? AND value = ? AND service_id IS ?",
		a.AddressKind.ID(h.Kind), h.Value, a.serviceID(h.Service))
}

// Alias records another handle of the person who has `of`, such as a username or the name a
// service shows: new, it joins that person; already someone's, it is left as it is (merging people
// is the owner's).
func (a *Archive) Alias(h, of Handle) {
	person := a.Int("SELECT person_id FROM person_address WHERE address_id = ?", a.Address(of))
	h = h.norm()
	if _, ok := a.addresses[h]; ok {
		return
	}
	kid, sid := a.AddressKind.ID(h.Kind), a.serviceID(h.Service)
	n, _ := a.Exec("INSERT OR IGNORE INTO address (kind_id, value, service_id) VALUES (?, ?, ?)", kid, h.Value, sid).RowsAffected()
	aid := a.Int("SELECT id FROM address WHERE kind_id = ? AND value = ? AND service_id IS ?", kid, h.Value, sid)
	if n > 0 {
		a.Exec("INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", aid, person)
	}
	a.addresses[h] = aid
}

// NameLike says whether a name has a letter in it (else it is a number or a symbol).
func NameLike(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || (unicode.IsNumber(r) && !unicode.IsDigit(r)) {
			return true
		}
	}
	return false
}

// HandleName records a name `service` shows for a handle the archive has: kind "book" (its copy of
// the user's address book), "chat" (a chat's name) or "profile" (chosen by them). Not for the
// user's own handles, nor a "name" without a letter (a number). seenAt: Unix seconds, 0 for now.
func (a *Archive) HandleName(h Handle, service, name, kind string, seenAt int64) {
	name = strings.TrimSpace(name)
	if !NameLike(name) || !a.Known(h) {
		return
	}
	aid := a.Address(h)
	if a.Exists("SELECT 1 FROM account WHERE address_id = ?", aid) {
		return
	}
	sid := a.Service.ID(service)
	t := seenAt
	if t == 0 {
		t = time.Now().Unix()
	}
	a.Exec("INSERT INTO handle_name (address_id, service_id, kind, name, first_seen, last_seen) "+
		"VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO UPDATE SET "+
		"first_seen = min(first_seen, excluded.first_seen), last_seen = max(last_seen, excluded.last_seen)",
		aid, sid, kind, name, t, t)
	// the current one: the latest seen of this handle, service and kind
	a.Exec("UPDATE handle_name SET current = (name = (SELECT name FROM handle_name WHERE address_id = ? "+
		"AND service_id = ? AND kind = ? ORDER BY last_seen DESC LIMIT 1)) "+
		"WHERE address_id = ? AND service_id = ? AND kind = ?", aid, sid, kind, aid, sid, kind)
}

// FindConversation is the id of a conversation of a service by its key, if the archive has it.
func (a *Archive) FindConversation(service, key string) (int64, bool) {
	return a.IntOK("SELECT id FROM conversation WHERE service_id = ? AND key = ?", a.Service.ID(service), key)
}

// ReportState records what a source says about a conversation: archived, muted (until, Unix ms;
// -1 for ever), pinned, read_until. observedAt: when its data was so (ms); changedAt: when it became
// so, if the service says (0: the first time it was seen so).
func (a *Archive) ReportState(sourceID, conversationID int64, field string, value, observedAt, changedAt int64) {
	if conversationID == 0 {
		return
	}
	iid, ok := a.IntOK("SELECT instance_id FROM source WHERE id = ?", sourceID)
	if !ok {
		return
	}
	var oldValue, oldObserved int64
	if a.Row("SELECT value, observed_at FROM state_report WHERE conversation_id = ? AND instance_id = ? AND field = ?",
		[]any{conversationID, iid, field}, &oldValue, &oldObserved) {
		if oldObserved > observedAt {
			return // older news than what is there
		}
		if oldValue == value {
			a.Exec("UPDATE state_report SET observed_at = ? WHERE conversation_id = ? AND instance_id = ? AND field = ?",
				observedAt, conversationID, iid, field)
			return
		}
	}
	if changedAt == 0 {
		changedAt = observedAt
	}
	a.Exec("INSERT OR REPLACE INTO state_report VALUES (?, ?, ?, ?, ?, ?)", conversationID, iid, field, value, observedAt, changedAt)
}

// Account records one of the owner's own handles.
func (a *Archive) Account(h Handle, service, label string) {
	aid := a.Address(h)
	a.Exec("INSERT OR IGNORE INTO account (address_id, service_id, label) VALUES (?, ?, ?)", aid, a.serviceID(service), nullStr(label))
}

// Own is the owner's handles, the way the importers make them.
func (a *Archive) Own() map[Handle]bool {
	out := map[Handle]bool{}
	rows := a.Query("SELECT k.name, a.value, s.name FROM account x JOIN address a ON a.id = x.address_id " +
		"JOIN address_kind k ON k.id = a.kind_id LEFT JOIN service s ON s.id = a.service_id")
	defer rows.Close()
	for rows.Next() {
		var k, v string
		var s sql.NullString
		rows.Scan(&k, &v, &s)
		out[Handle{k, v, s.String}] = true
	}
	return out
}

// Conversation is the id of a conversation, made when new. key: "" for the sorted member values.
func (a *Archive) Conversation(service string, members []Handle, key, title string) int64 {
	sid := a.Service.ID(service)
	if key == "" {
		vals := make([]string, len(members))
		for i, m := range members {
			vals[i] = m.Value
		}
		sort.Strings(vals)
		key = strings.Join(vals, ",")
	}
	ck := fmt.Sprint(sid, "\x00", key)
	if id, ok := a.conversations[ck]; ok {
		return id
	}
	group := 0
	if len(members) > 1 {
		group = 1
	}
	a.Exec("INSERT OR IGNORE INTO conversation (service_id, key, title, is_group) VALUES (?, ?, ?, ?)", sid, key, nullStr(title), group)
	cid := a.Int("SELECT id FROM conversation WHERE service_id = ? AND key = ?", sid, key)
	for _, m := range members {
		a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", cid, a.Address(m))
	}
	a.conversations[ck] = cid
	return cid
}

// MessageByKey is the id of the message with this key, within the conversation where the service
// needs it.
func (a *Archive) MessageByKey(service, key string, conversationID int64) (int64, bool) {
	sid := a.Service.ID(service)
	var scope any
	if a.keyScoped[sid] {
		scope = conversationID
	}
	return a.IntOK("SELECT id FROM message WHERE service_id = ? AND key = ? AND key_scope IS ?", sid, key, scope)
}

// KeyScoped says whether the service's keys are unique only within a conversation.
func (a *Archive) KeyScoped(service string) bool { return a.keyScoped[a.Service.ID(service)] }

// Reaction is a reaction to a message: the emoji where known, the service's code ("viber:6");
// Who is a Handle, an address id (int64), "peer" for the other person of a one-to-one chat, a
// service's own id the importer maps (a jid string), or nil; Outgoing for the owner's own.
type Reaction struct {
	Emoji, Code string
	Count       int
	Who         any
	Outgoing    bool
}

// ReactsTo is a reaction sent as a message of its own (an iMessage tapback).
type ReactsTo struct{ Key, Emoji, Code string }

// Extras is what a message carries beyond its text (see package extras).
type Extras struct {
	Text                 string // the content of a shared contact or a poll, where the message has none
	Lat, Lon             *float64
	Place                string
	Subtype, SubtypeCode string
	ReplyKey, ReplyText  string
	Edited, Deleted      bool
	Forwarded, Starred   bool
	Reactions            []Reaction
	EditsKey             string
	ReactsTo             *ReactsTo
	Notice               *Notice
}

// Notice is what a notice says, its code and values (the `notice` table), for the interface to put
// in words; a person in Args is {"address": id}, the owner {"self": true}.
type Notice struct {
	Code string
	Args map[string]any
}

// Message is a message to add.
type Message struct {
	Service        string
	ConversationID int64
	TS             int64
	Outgoing       bool
	SenderID       int64 // 0: none (outgoing)
	Kind           string
	Text           string // "" is NULL
	Key            string // "" is none
	Extras         *Extras
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// NullStr is "" as NULL; NullID is 0 as NULL.
func NullStr(s string) any { return nullStr(s) }
func NullID(id int64) any  { return nullID(id) }
func B2I(b bool) int       { return b2i(b) }

// AddMessage adds a message from a source row; it returns its id.
func (a *Archive) AddMessage(sourceID int64, rowKey string, m Message) int64 {
	x := m.Extras
	if x == nil {
		x = &Extras{}
	}
	sid := a.Service.ID(m.Service)
	txt := m.Text
	if x.Text != "" {
		txt = x.Text
	}
	var lat, lon, slat, slon any
	if m.Kind == "location" {
		lat, lon = fptr(x.Lat), fptr(x.Lon)
	} else { // on another kind: where the sender was
		slat, slon = fptr(x.Lat), fptr(x.Lon)
	}
	var key, scope, fp any
	if m.Key != "" {
		key = m.Key
		if a.keyScoped[sid] {
			scope = m.ConversationID
		}
	} else {
		fp = Fingerprint(m.TS, m.Outgoing, m.Kind, txt)
	}
	mid, _ := a.Exec(
		"INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text, key, "+
			"key_scope, fingerprint, subtype, subtype_code, reply_key, reply_text, edited, deleted, forwarded, "+
			"starred, lat, lon, place, sender_lat, sender_lon) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		sid, m.ConversationID, m.TS, b2i(m.Outgoing), nullID(m.SenderID), a.MessageKind.ID(m.Kind), nullStr(txt), key,
		scope, fp, nullStr(x.Subtype), nullStr(x.SubtypeCode), nullStr(x.ReplyKey), nullStr(x.ReplyText),
		b2i(x.Edited), b2i(x.Deleted), b2i(x.Forwarded), b2i(x.Starred),
		lat, lon, nullStr(x.Place), slat, slon).LastInsertId()
	if txt != "" {
		folded := text.Fold(txt)
		a.Exec("INSERT INTO message_fts (rowid, text) VALUES (?, ?)", mid, folded)
		a.Exec("INSERT INTO message_tri (rowid, text) VALUES (?, ?)", mid, folded)
	}
	a.Exec("INSERT INTO message_origin VALUES (?, ?, ?)", sourceID, rowKey, mid)
	if x.Notice != nil {
		a.SetNotice(mid, x.Notice)
	}
	a.AddReactions(mid, x.Reactions)
	if x.EditsKey != "" {
		a.pendingEdits = append(a.pendingEdits, [2]any{sid, x.EditsKey})
	}
	if x.ReactsTo != nil {
		a.pendingTapbacks = append(a.pendingTapbacks, pendingTapback{sid, x.ReactsTo.Key, x.ReactsTo.Emoji, x.ReactsTo.Code,
			nullID(m.SenderID), b2i(m.Outgoing)})
	}
	return mid
}

func fptr(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

// AddReactions adds reactions to a message.
func (a *Archive) AddReactions(messageID int64, reactions []Reaction) {
	for _, r := range reactions {
		var who any
		switch w := r.Who.(type) {
		case string:
			if w == "peer" { // the other person of a one-to-one chat
				members := a.Ints("SELECT cm.address_id FROM message m JOIN conversation c ON c.id = m.conversation_id "+
					"JOIN conversation_member cm ON cm.conversation_id = c.id WHERE m.id = ? AND NOT c.is_group", messageID)
				if len(members) == 1 {
					who = members[0]
				}
			}
		case Handle:
			who = a.Address(w)
		case int64:
			who = w
		case int:
			who = int64(w)
		}
		count := r.Count
		if count == 0 {
			count = 1
		}
		var out any
		if r.Outgoing {
			out = 1
		}
		a.Exec("INSERT INTO reaction (message_id, emoji, code, count, address_id, outgoing) VALUES (?, ?, ?, ?, ?, ?)",
			messageID, nullStr(r.Emoji), nullStr(r.Code), count, who, out)
	}
}

// InitArchived decides the app's own "archived", once, for the chats it sees for the first time: a
// person's chat archived if every one of their conversations a source reports on is archived there
// (one in view keeps them in view), a group if it is; a chat no source reports on, not archived.
// From then on it is the app's alone. Nothing is overwritten.
func (a *Archive) InitArchived() {
	own := map[int64]bool{}
	for _, id := range a.Ints("SELECT address_id FROM account") {
		own[id] = true
	}
	person := map[int64]int64{}
	rows := a.Query("SELECT address_id, person_id FROM person_address")
	for rows.Next() {
		var aid, pid int64
		rows.Scan(&aid, &pid)
		person[aid] = pid
	}
	rows.Close()
	me := map[int64]bool{}
	for aid := range own {
		if pid, ok := person[aid]; ok {
			me[pid] = true
		}
	}
	links := map[int64]int64{}
	rows = a.Query("SELECT conversation_id, into_id FROM group_link")
	for rows.Next() {
		var c, i int64
		rows.Scan(&c, &i)
		links[c] = i
	}
	rows.Close()
	newest := map[int64]int64{} // conversation -> archived, as its newest report says
	rows = a.Query("SELECT conversation_id, value FROM state_report WHERE field = 'archived' ORDER BY observed_at")
	for rows.Next() {
		var c, v int64
		rows.Scan(&c, &v)
		newest[c] = v
	}
	rows.Close()
	members := map[int64][]int64{}
	rows = a.Query("SELECT conversation_id, address_id FROM conversation_member")
	for rows.Next() {
		var c, aid int64
		rows.Scan(&c, &aid)
		members[c] = append(members[c], aid)
	}
	rows.Close()
	type conv struct {
		id    int64
		group bool
	}
	var convs []conv
	rows = a.Query("SELECT id, is_group FROM conversation")
	for rows.Next() {
		var c conv
		rows.Scan(&c.id, &c.group)
		convs = append(convs, c)
	}
	rows.Close()
	chats := map[string][]int64{}
	var order []string
	for _, c := range convs {
		others := map[int64]bool{}
		for _, aid := range members[c.id] {
			if own[aid] {
				continue
			}
			if pid, ok := person[aid]; ok && !me[pid] {
				others[pid] = true
			}
		}
		var chat string
		if !c.group && len(others) == 1 {
			for pid := range others {
				chat = fmt.Sprintf("p%d", pid)
			}
		} else {
			head := c.id
			if into, ok := links[c.id]; ok {
				head = into
			}
			chat = fmt.Sprintf("c%d", head)
		}
		if _, ok := chats[chat]; !ok {
			order = append(order, chat)
			chats[chat] = []int64{}
		}
		if v, ok := newest[c.id]; ok {
			chats[chat] = append(chats[chat], v)
		}
	}
	now := time.Now().UnixMilli()
	for _, chat := range order {
		vs := chats[chat]
		archived := len(vs) > 0
		for _, v := range vs {
			if v == 0 {
				archived = false
			}
		}
		a.Exec("INSERT OR IGNORE INTO chat_state (chat, field, value, set_at) VALUES (?, 'archived', ?, ?)", chat, b2i(archived), now)
	}
}

// Resolve, after an import: removes again what the sources brought of handles removed as spam
// (PurgeSpam); links replies to the messages they answer (keeping the quoted text only where that is
// not in the archive), marks the messages that edit events edited, and turns reactions sent as
// messages (tapbacks) into reactions on their message; and decides the app's own "archived" for
// chats new to it (InitArchived).
func (a *Archive) Resolve() {
	a.PurgeSpam()
	a.InitArchived()
	a.Exec("UPDATE message SET reply_to = (SELECT t.id FROM message t WHERE t.service_id = " +
		"message.service_id AND t.key = message.reply_key AND t.key_scope IS message.key_scope) " +
		"WHERE reply_key IS NOT NULL AND reply_to IS NULL")
	a.Exec("UPDATE message SET reply_text = NULL WHERE reply_to IS NOT NULL AND reply_text IS NOT NULL")
	for _, e := range a.pendingEdits {
		a.Exec("UPDATE message SET edited = 1 WHERE service_id = ? AND key = ?", e[0], e[1])
	}
	for _, t := range a.pendingTapbacks {
		if target, ok := a.IntOK("SELECT id FROM message WHERE service_id = ? AND key = ?", t.service, t.key); ok {
			a.Exec("INSERT INTO reaction (message_id, emoji, code, count, address_id, outgoing) VALUES (?, ?, ?, 1, ?, ?)",
				target, nullStr(t.emoji), nullStr(t.code), t.sender, t.outgoing)
		}
	}
	a.pendingEdits, a.pendingTapbacks = nil, nil
}

// PurgeSpam is PurgeSpam on what this import brought: its files are deleted once committed.
func (a *Archive) PurgeSpam() {
	p := PurgeSpam(a.q(), nil)
	if p.Conversations > 0 {
		a.conversations = map[string]int64{} // those cached may be gone
	}
	a.pendingFiles = append(a.pendingFiles, p.Files...)
}

// Call is a call to add.
type Call struct {
	Service        string
	AddressID      int64 // 0: a hidden number
	TS             int64
	Outgoing       bool
	Answered       bool
	Duration       int64
	Key            string
	Detail         string
	DetailCode     string
	Video          bool
	Attempts       int
	ConversationID int64
}

func (a *Archive) AddCall(sourceID int64, rowKey string, c Call) int64 {
	attempts := c.Attempts
	if attempts == 0 {
		attempts = 1
	}
	id, _ := a.Exec("INSERT INTO call (service_id, address_id, ts, outgoing, answered, duration, key, detail, detail_code, "+
		"video, attempts, conversation_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		a.Service.ID(c.Service), nullID(c.AddressID), c.TS, b2i(c.Outgoing), b2i(c.Answered), c.Duration, nullStr(c.Key),
		nullStr(c.Detail), nullStr(c.DetailCode), b2i(c.Video), attempts, nullID(c.ConversationID)).LastInsertId()
	a.Exec("INSERT INTO call_origin VALUES (?, ?, ?)", sourceID, rowKey, id)
	return id
}

// AndroidExport is one Android export: its device, database and folder.
type AndroidExport struct{ Device, DB, Folder string }

// AndroidExports are the Android exports there are: the earlier single one, then one per phone folder.
func AndroidExports(root string) []AndroidExport {
	if root == "" {
		root = config.AndroidExport
	}
	var out []AndroidExport
	legacy := filepath.Join(root, Android()+".db")
	if _, err := os.Stat(legacy); err == nil {
		out = append(out, AndroidExport{Android(), legacy, root})
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries { // ReadDir gives them sorted
		p := filepath.Join(root, e.Name(), "android.db")
		if _, err := os.Stat(p); err == nil {
			out = append(out, AndroidExport{e.Name(), p, filepath.Dir(p)})
		}
	}
	return out
}

// Fingerprint is what tells a message without a key from another in its conversation: time,
// direction, kind and text (not the sender, whose address may later be merged).
func Fingerprint(ts int64, outgoing bool, kind, txt string) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%d|%d|%s|%s", ts, b2i(outgoing), kind, txt)))
	return hex.EncodeToString(h[:])[:20]
}

// Contract is a path as stored: the cache and data folders written as {cache} and {data}.
func Contract(path string) string {
	for _, t := range [][2]string{{"{cache}", config.Cache}, {"{data}", config.Data}} {
		if path == t[1] || strings.HasPrefix(path, t[1]+string(os.PathSeparator)) {
			return t[0] + path[len(t[1]):]
		}
	}
	return path
}

// Expand is a stored path made whole again (see Contract).
func Expand(path string) string {
	for _, t := range [][2]string{{"{cache}", config.Cache}, {"{data}", config.Data}} {
		if strings.HasPrefix(path, t[0]) {
			return t[1] + path[len(t[0]):]
		}
	}
	return path
}

var uriRE = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:`)

// IsSpace is Python's str.isspace() for one character (Go's \s is ASCII only).
func IsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func isURI(s string) bool {
	m := uriRE.FindStringIndex(s)
	if m == nil {
		return false
	}
	for _, r := range s[m[1]:] {
		return !IsSpace(r)
	}
	return false
}

func stripJunk(s string) string {
	return strings.Map(func(r rune) rune {
		if IsSpace(r) || r == '-' || r == '(' || r == ')' || r == '.' {
			return -1
		}
		return r
	}, s)
}

// Address is (kind, normalised value) of a phone number, email, URI or alphanumeric sender.
func Address(raw, region string) (string, string) {
	s := strings.TrimSpace(raw)
	low := text.Lower(s)
	if strings.Contains(s, "@") && !strings.HasPrefix(low, "sip:") {
		return "email", low
	}
	if isURI(s) && !strings.HasPrefix(low, "tel:") {
		return "uri", low
	}
	digits := stripJunk(strings.TrimPrefix(s, "tel:"))
	if !allDigits(strings.TrimPrefix(digits, "+")) {
		return "sender", s
	}
	if strings.HasPrefix(digits, "00") {
		digits = "+" + digits[2:]
	}
	return "phone", Phone(digits, region)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// SetNotice writes (or replaces) what a message's notice says.
func (a *Archive) SetNotice(messageID int64, n *Notice) {
	a.Exec("INSERT INTO notice VALUES (?, ?, ?) ON CONFLICT (message_id) DO UPDATE SET code = excluded.code, args = excluded.args",
		messageID, n.Code, NoticeJSON(n))
}

// NoticeJSON is a notice's values as the archive keeps them.
func NoticeJSON(n *Notice) string {
	args := n.Args
	if args == nil {
		args = map[string]any{}
	}
	js, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return string(js)
}
