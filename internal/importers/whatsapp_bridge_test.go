// Ports tests/test_whatsapp_bridge.py (its importer side): the WhatsApp bridge's databases as the
// importers read them: a bridge of this version (kinds, replies, places, reactions, edits,
// deletions, calls, the files it downloaded, its state), and one from before.
package importers

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

const (
	waPeer  = "15551234567@s.whatsapp.net"
	waGroup = "120363000000000001@g.us"
)

const oldBridgeSchema = `
CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
CREATE TABLE messages (id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP, is_from_me BOOLEAN,
    media_type TEXT, filename TEXT, url TEXT, media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB,
    file_length INTEGER, PRIMARY KEY (id, chat_jid));
`

const newBridgeSchema = oldBridgeSchema + `
ALTER TABLE messages ADD COLUMN kind TEXT; ALTER TABLE messages ADD COLUMN subtype TEXT;
ALTER TABLE messages ADD COLUMN reply_to TEXT; ALTER TABLE messages ADD COLUMN reply_text TEXT;
ALTER TABLE messages ADD COLUMN forwarded BOOLEAN; ALTER TABLE messages ADD COLUMN edited BOOLEAN;
ALTER TABLE messages ADD COLUMN deleted BOOLEAN; ALTER TABLE messages ADD COLUMN lat REAL;
ALTER TABLE messages ADD COLUMN lon REAL; ALTER TABLE messages ADD COLUMN place TEXT;
CREATE TABLE reactions (chat_jid TEXT, message_id TEXT, sender TEXT, is_from_me BOOLEAN, emoji TEXT, timestamp TIMESTAMP,
    PRIMARY KEY (chat_jid, message_id, sender));
CREATE TABLE calls (id TEXT PRIMARY KEY, source TEXT, chat_jid TEXT, creator TEXT, is_from_me BOOLEAN, is_group BOOLEAN,
    video BOOLEAN, timestamp TIMESTAMP, outcome TEXT, duration INTEGER, accepted_at TIMESTAMP, ended_at TIMESTAMP,
    end_reason TEXT);
CREATE TABLE call_participants (call_id TEXT, jid TEXT, outcome TEXT, PRIMARY KEY (call_id, jid));
CREATE TABLE bridge_state (key TEXT PRIMARY KEY, value TEXT, at TIMESTAMP);
ALTER TABLE messages ADD COLUMN direct_path TEXT; ALTER TABLE messages ADD COLUMN media_path TEXT;
ALTER TABLE messages ADD COLUMN media_error TEXT; ALTER TABLE messages ADD COLUMN mentions TEXT;
ALTER TABLE messages ADD COLUMN read_at TIMESTAMP;
CREATE TABLE receipts (chat_jid TEXT, message_id TEXT, jid TEXT, type TEXT, timestamp TIMESTAMP,
    PRIMARY KEY (chat_jid, message_id, jid, type));
CREATE TABLE group_info (jid TEXT PRIMARY KEY, name TEXT, addressing TEXT, member BOOLEAN, updated_at TIMESTAMP);
CREATE TABLE group_members (group_jid TEXT, jid TEXT, phone TEXT, lid TEXT, is_admin BOOLEAN, is_super_admin BOOLEAN,
    PRIMARY KEY (group_jid, jid));
`

func waTS(minute int) string { return fmt.Sprintf("2026-10-06 10:%02d:00+03:00", minute) }

func bridgeDB(t *testing.T, dir, schema string) (string, *sql.DB) {
	path := filepath.Join(dir, "messages.db")
	d, err := db.Open(path)
	must(t, err)
	d.SetMaxOpenConns(1)
	t.Cleanup(func() { d.Close() })
	db.Exec(d, schema)
	db.Exec(d, "INSERT INTO chats VALUES (?, 'Peer', ?)", waPeer, waTS(30))
	return path, d
}

func waMessage(d *sql.DB, id string, minute int, content string, fromMe int, cols M) {
	sender := waPeer
	if fromMe != 0 {
		sender = ""
	}
	row := M{"id": id, "chat_jid": waPeer, "sender": sender, "content": content, "timestamp": waTS(minute),
		"is_from_me": fromMe, "media_type": ""}
	for k, v := range cols {
		row[k] = v
	}
	var names []string
	for k := range row {
		names = append(names, k)
	}
	sort.Strings(names)
	var vals []any
	for _, k := range names {
		vals = append(vals, row[k])
	}
	db.Exec(d, "INSERT INTO messages ("+strings.Join(names, ", ")+") VALUES ("+db.Marks(len(names))+")", vals...)
}

func runBridge(t *testing.T, a *archive.Archive, path string) map[string]int {
	t.Helper()
	got, err := WhatsApp(a, nil, WhatsAppOptions{NoIphone: true, NoContacts: true, BridgeDB: path, NoStore: true})
	must(t, err)
	return got
}

func msgRow(a *archive.Archive, key, cols string) []any {
	m := db.Maps(a.Tx(), "SELECT "+cols+" FROM message WHERE key = ?", key)
	if len(m) == 0 {
		return nil
	}
	var out []any
	for _, c := range strings.Split(cols, ", ") {
		out = append(out, m[0][c])
	}
	return out
}

func reactionsOf(a *archive.Archive, key string) [][2]any {
	var out [][2]any
	for _, r := range db.Maps(a.Tx(), "SELECT r.emoji, r.outgoing FROM reaction r JOIN message m ON m.id = r.message_id "+
		"WHERE m.key = ? ORDER BY coalesce(r.outgoing, 0), r.emoji", key) {
		out = append(out, [2]any{r["emoji"], r["outgoing"]})
	}
	return out
}

func kindOf(a *archive.Archive, key string) string {
	var k string
	a.Row("SELECT k.name FROM message m JOIN message_kind k ON k.id = m.kind_id WHERE m.key = ?", []any{key}, &k)
	return k
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %#v, want %#v", what, got, want)
	}
}

func TestBridgeKindsRepliesPlacesAndReactions(t *testing.T) {
	a, _ := newArchive(t)
	path, d := bridgeDB(t, t.TempDir(), newBridgeSchema)
	waMessage(d, "M1", 1, "coming tonight?", 0, M{"kind": "text"})
	waMessage(d, "M2", 2, "", 0, M{"kind": "location", "lat": 37.97, "lon": 23.73, "place": "Syntagma"})
	waMessage(d, "M3", 3, "Nikos, +30 690 000 0000", 0, M{"kind": "contact"})
	waMessage(d, "M4", 4, "When?\n• Mon\n• Tue", 0, M{"kind": "poll"})
	waMessage(d, "M5", 5, "yes", 1, M{"kind": "text", "reply_to": "M1", "reply_text": "coming tonight?", "forwarded": 1})
	waMessage(d, "M6", 6, "", 0, M{"kind": "video", "subtype": "gif", "media_type": "video"})
	db.Exec(d, "INSERT INTO reactions VALUES (?, 'M1', ?, 0, '👍', ?)", waPeer, waPeer, waTS(7))
	db.Exec(d, "INSERT INTO reactions VALUES (?, 'M1', 'me@s.whatsapp.net', 1, '❤️', ?)", waPeer, waTS(8))
	runBridge(t, a, path)

	var kinds []string
	for _, k := range []string{"M1", "M2", "M3", "M4", "M5", "M6"} {
		kinds = append(kinds, kindOf(a, k))
	}
	eq(t, "kinds", kinds, []string{"text", "location", "contact", "text", "text", "video"})
	eq(t, "place", msgRow(a, "M2", "lat, lon, place"), []any{37.97, 23.73, "Syntagma"})
	eq(t, "contact", msgRow(a, "M3", "text"), []any{"📇 Nikos, +30 690 000 0000"})
	eq(t, "poll", msgRow(a, "M4", "subtype, subtype_code"), []any{"poll", "whatsmeow:poll"})
	m1 := msgRow(a, "M1", "id")[0]
	eq(t, "reply", msgRow(a, "M5", "reply_to, reply_text, forwarded"), []any{m1, nil, int64(1)}) // quoted text dropped once linked
	eq(t, "gif", msgRow(a, "M6", "subtype, subtype_code"), []any{"gif", "whatsmeow:gif"})
	eq(t, "reactions", reactionsOf(a, "M1"), [][2]any{{"👍", nil}, {"❤️", int64(1)}})
}

func TestBridgeChangesToMessagesAlreadyThere(t *testing.T) {
	a, _ := newArchive(t)
	path, d := bridgeDB(t, t.TempDir(), newBridgeSchema)
	waMessage(d, "M1", 1, "see you at 8", 0, M{"kind": "text"})
	waMessage(d, "M2", 2, "oops", 0, M{"kind": "text"})
	db.Exec(d, "INSERT INTO reactions VALUES (?, 'M1', ?, 0, '👍', ?)", waPeer, waPeer, waTS(3))
	db.Exec(d, "INSERT INTO reactions VALUES (?, 'M1', 'me@s.whatsapp.net', 1, '❤️', ?)", waPeer, waTS(3))
	runBridge(t, a, path)
	eq(t, "nothing new", runBridge(t, a, path), map[string]int{}) // nothing new: nothing changed

	db.Exec(d, "UPDATE messages SET content = 'see you at 9', edited = 1 WHERE id = 'M1'")
	db.Exec(d, "UPDATE messages SET deleted = 1 WHERE id = 'M2'")
	db.Exec(d, "UPDATE reactions SET emoji = '😂' WHERE NOT is_from_me") // changed
	db.Exec(d, "UPDATE reactions SET emoji = '' WHERE is_from_me")      // taken back
	eq(t, "changes", runBridge(t, a, path), map[string]int{"text": 1, "edited": 1, "deleted": 1, "reactions": 2})
	eq(t, "M1", msgRow(a, "M1", "text, edited"), []any{"see you at 9", int64(1)}) // the new text, as WhatsApp shows it
	eq(t, "M2", msgRow(a, "M2", "text, deleted"), []any{"oops", int64(1)})        // kept, marked
	eq(t, "found by the new", searchKeys(a, "9"), []string{"M1"})
	eq(t, "unchanged again", runBridge(t, a, path), map[string]int{})
	eq(t, "reactions", reactionsOf(a, "M1"), [][2]any{{"😂", nil}})
	eq(t, "again", runBridge(t, a, path), map[string]int{})
}

func TestABridgeFromBeforeImportsAsItDid(t *testing.T) {
	a, _ := newArchive(t)
	path, d := bridgeDB(t, t.TempDir(), oldBridgeSchema)
	waMessage(d, "O1", 1, "hello", 0, nil)
	waMessage(d, "O2", 2, "look", 0, M{"media_type": "image"})
	eq(t, "changes", runBridge(t, a, path), map[string]int{})
	eq(t, "O1", msgRow(a, "O1", "text"), []any{"hello"})
	eq(t, "O2", kindOf(a, "O2"), "image")
	must(t, BridgeCalls(a, NewCallSet(a), path, "")) // no calls table: nothing, no error
}

func TestBridgeCalls(t *testing.T) {
	a, _ := newArchive(t)
	path, d := bridgeDB(t, t.TempDir(), newBridgeSchema)
	// id, source, chat, creator, from me, group, video, ts, outcome, duration, accepted, ended, reason
	calls := [][]any{
		{"L1", "log", waPeer, nil, 1, 0, 1, waTS(1), "CONNECTED", 125, nil, nil, nil},
		{"L2", "log", waPeer, nil, 0, 0, 0, waTS(10), "MISSED", 0, nil, nil, nil},
		{"E1", "event", waPeer, waPeer, 0, 0, 0, waTS(20), nil, nil, waTS(20), "2026-10-06 10:21:30+03:00", "terminate"},
		{"E2", "event", waPeer, waPeer, 0, 0, 0, waTS(40), nil, nil, nil, waTS(40), "reject"},
		{"E3", "event", waPeer, waPeer, 0, 0, 0, waTS(50), nil, nil, nil, nil, nil},            // still ringing
		{"E4", "event", waPeer, waPeer, 0, 0, 0, waTS(10), nil, nil, nil, waTS(10), "timeout"}, // the same call as L2
	}
	for _, c := range calls {
		db.Exec(d, "INSERT INTO calls VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", c...)
	}
	before := a.Int("SELECT count(*) FROM call")
	must(t, BridgeCalls(a, NewCallSet(a), path, ""))
	a.Commit()
	var got [][]any
	for _, r := range db.Maps(a.Tx(), "SELECT outgoing, answered, duration, detail, video FROM call ORDER BY ts") {
		got = append(got, []any{r["outgoing"], r["answered"], r["duration"], r["detail"], r["video"]})
	}
	eq(t, "count", a.Int("SELECT count(*) FROM call"), before+4)
	eq(t, "calls", got, [][]any{{int64(1), int64(1), int64(125), nil, int64(1)}, {int64(0), int64(0), int64(0), "missed", int64(0)},
		{int64(0), int64(1), int64(90), nil, int64(0)}, {int64(0), int64(0), int64(0), "rejected", int64(0)}})
	must(t, BridgeCalls(a, NewCallSet(a), path, "")) // again: nothing new
	eq(t, "count again", a.Int("SELECT count(*) FROM call"), before+4)
}

func TestTheFilesTheBridgeDownloadedGoToTheirMessages(t *testing.T) {
	a, _ := newArchive(t)
	dir := t.TempDir()
	path, d := bridgeDB(t, dir, newBridgeSchema)
	rel := "media/" + waPeer + "/PIC.jpg"
	must(t, os.MkdirAll(filepath.Join(dir, "media", waPeer), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, rel), []byte("a picture"), 0o600))
	waMessage(d, "PIC", 1, "look", 0, M{"kind": "image", "media_type": "image", "media_path": rel})
	waMessage(d, "LATER", 2, "", 0, M{"kind": "image", "media_type": "image"}) // not downloaded (yet)
	runBridge(t, a, path)
	root := filepath.Join(dir, "archive-media")
	step := func(a *archive.Archive, s *Store) {
		s.Root = root
		MediaWhatsAppBridge(path)(a, s)
	}
	must(t, Media(a, nil, step))
	must(t, Media(a, nil, step)) // again: nothing new
	rows := db.Maps(a.Tx(), "SELECT m.key, md.path FROM attachment t JOIN message m ON m.id = t.message_id "+
		"JOIN media md ON md.sha256 = t.sha256 JOIN source s ON s.id = t.source_id WHERE s.name = 'whatsapp-bridge'")
	if len(rows) != 1 || rows[0]["key"] != "PIC" {
		t.Fatalf("rows %v", rows)
	}
	b, err := os.ReadFile(filepath.Join(root, rows[0]["path"].(string)))
	must(t, err)
	eq(t, "file", string(b), "a picture")
}

func TestABridgeFromBeforeHasNoFilesToGive(t *testing.T) {
	a, _ := newArchive(t)
	path, d := bridgeDB(t, t.TempDir(), oldBridgeSchema)
	waMessage(d, "PIC", 1, "", 0, M{"media_type": "image"})
	must(t, Media(a, nil, MediaWhatsAppBridge(path)))
}

func TestMentionsAndTheGroupsMembers(t *testing.T) {
	a, _ := newArchive(t)
	path, d := bridgeDB(t, t.TempDir(), newBridgeSchema)
	db.Exec(d, "INSERT INTO chats VALUES (?, 'Group', ?)", waGroup, waTS(30))
	db.Exec(d, "INSERT INTO group_members VALUES (?, '98765@lid', '15557654321@s.whatsapp.net', '98765@lid', 0, 0)", waGroup)
	db.Exec(d, "INSERT INTO group_members VALUES (?, '15551234567@s.whatsapp.net', '15551234567@s.whatsapp.net', '', 1, 0)", waGroup)
	waMessage(d, "HEY", 1, "@98765 @15551234567 look", 0, M{"chat_jid": waGroup, "sender": "98765@lid",
		"mentions": "98765@lid,15551234567@s.whatsapp.net"})
	runBridge(t, a, path)
	runBridge(t, a, path) // again: nothing twice
	named := db.Strs(a.Tx(), "SELECT a.value FROM mention n JOIN address a ON a.id = n.address_id JOIN message m "+
		"ON m.id = n.message_id WHERE m.key = 'HEY' ORDER BY a.value")
	eq(t, "named", named, []string{"+15551234567", "+15557654321"}) // a LID as the number the group gives for it
	members := db.Strs(a.Tx(), "SELECT a.value FROM conversation_member cm JOIN conversation c ON c.id = cm.conversation_id "+
		"JOIN address a ON a.id = cm.address_id WHERE c.key = ? ORDER BY a.value", waGroup)
	eq(t, "members", members, []string{"+15551234567", "+15557654321"})
}

func TestReceiptsAndWhatTheOwnerRead(t *testing.T) {
	a, _ := newArchive(t)
	path, d := bridgeDB(t, t.TempDir(), newBridgeSchema)
	waMessage(d, "MINE", 1, "seen?", 1, nil)
	waMessage(d, "THEIRS", 2, "yes", 0, nil)
	for _, r := range [][]any{{waPeer, "MINE", waPeer, "delivered", waTS(1)}, {waPeer, "MINE", waPeer, "read", waTS(3)},
		{waPeer, "THEIRS", waPeer, "read", waTS(4)}, // not the owner's message: no receipt
		{waPeer, "GONE", waPeer, "read", waTS(4)}} {
		db.Exec(d, "INSERT INTO receipts VALUES (?, ?, ?, ?, ?)", r...)
	}
	db.Exec(d, "UPDATE messages SET read_at = ? WHERE id = 'THEIRS'", waTS(5))
	a.Exec("INSERT INTO plugin_instance (plugin, kind, label, created_at) VALUES ('whatsapp-bridge', 'source', 'W', 0)")
	runBridge(t, a, path)
	// the source as the plugin's import leaves it: its instance's
	a.Exec("UPDATE source SET instance_id = (SELECT id FROM plugin_instance WHERE plugin = 'whatsapp-bridge') WHERE name = 'whatsapp-bridge'")
	runBridge(t, a, path)
	var rows [][]any
	for _, r := range db.Maps(a.Tx(), "SELECT m.key, a.value, r.delivered_at, r.read_at, r.played_at FROM receipt r "+
		"JOIN message m ON m.id = r.message_id JOIN address a ON a.id = r.address_id WHERE m.key IN ('MINE', 'THEIRS', 'GONE')") {
		rows = append(rows, []any{r["key"], r["value"], r["delivered_at"], r["read_at"], r["played_at"]})
	}
	eq(t, "receipts", rows, [][]any{{"MINE", "+15551234567", isoMS(waTS(1)), isoMS(waTS(3)), nil}})
	var value, changed int64
	a.Row("SELECT s.value, s.changed_at FROM state_report s JOIN conversation c ON c.id = s.conversation_id "+
		"WHERE c.key = '+15551234567' AND s.field = 'read_until'", nil, &value, &changed)
	eq(t, "read_until", []int64{value, changed}, []int64{isoMS(waTS(2)), isoMS(waTS(5))})
}

func TestStatusAndChannelsAreNotChats(t *testing.T) {
	for _, jid := range []string{"306900000001@status", "10000000000001@lid.status", "status@broadcast", "120363000000000001@newsletter"} {
		if waConversation(nil, nil, jid, "x", nil) != 0 {
			t.Fatalf("%s is a chat", jid)
		}
	}
}
