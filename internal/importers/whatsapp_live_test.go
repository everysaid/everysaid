// The live WhatsApp connection's batches as the writer puts them into the archive: kinds, replies,
// places, reactions, edits, deletions, receipts, reads, calls, files, notices, polls; what names a
// message still to come waits for it; and what the connection keeps of chats and people (keys.db).
package importers

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

const (
	waPeer  = "15551234567@s.whatsapp.net"
	waGroup = "120363000000000001@g.us"
	waStore = "/the/store"
)

func waTS(minute int) string { return fmt.Sprintf("2026-10-06 10:%02d:00+03:00", minute) }

// waAt is a minute of the tests' day, Unix ms.
func waAt(minute int) int64 { return isoMS(waTS(minute)) }

// waKeys is a keys database with the live connection's tables, and whatsmeow's that it reads (as
// whatsmeow makes them).
func waKeys(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "keys.db"))
	must(t, err)
	t.Cleanup(func() { d.Close() })
	must(t, WhatsAppTables(d))
	db.Exec(d, "CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT UNIQUE NOT NULL)")
	db.Exec(d, `CREATE TABLE whatsmeow_contacts (our_jid TEXT, their_jid TEXT, first_name TEXT, full_name TEXT, push_name TEXT,
		business_name TEXT, PRIMARY KEY (our_jid, their_jid))`)
	db.Exec(d, `CREATE TABLE whatsmeow_chat_settings (our_jid TEXT, chat_jid TEXT, muted_until BIGINT NOT NULL DEFAULT 0,
		pinned BOOLEAN NOT NULL DEFAULT false, archived BOOLEAN NOT NULL DEFAULT false, PRIMARY KEY (our_jid, chat_jid))`)
	return d
}

// waWrite writes a batch as the plugin does (a writer, then Done), the source the instance's; it
// gives what changed on messages already there.
func waWrite(t *testing.T, a *archive.Archive, keys *sql.DB, b *WABatch) map[string]int {
	t.Helper()
	var updated map[string]int
	err := func() (err error) {
		defer archive.Recover(&err)
		w := NewWhatsAppWriter(a, keys, waStore)
		tieInstance(a, w.Source)
		w.Write(b)
		w.Done()
		updated = w.Updated
		return nil
	}()
	must(t, err)
	return updated
}

// tieInstance ties a source to an instance of the plugin, as the plugin's writer does (a chat's
// state is an instance's).
func tieInstance(a *archive.Archive, src int64) {
	if !a.Exists("SELECT 1 FROM plugin_instance WHERE plugin = 'whatsapp-bridge'") {
		a.Exec("INSERT INTO plugin_instance (plugin, kind, label, created_at) VALUES ('whatsapp-bridge', 'source', 'W', 0)")
	}
	a.Exec("UPDATE source SET instance_id = (SELECT id FROM plugin_instance WHERE plugin = 'whatsapp-bridge') WHERE id = ?", src)
}

// waMsg is a message of the peer's chat at a minute ("" sender: the owner's).
func waMsg(id string, minute int, text string, fromMe bool, kind string) WAMessage {
	m := WAMessage{Chat: waPeer, ID: id, Sender: waPeer, FromMe: fromMe, At: waAt(minute), Kind: kind, Text: text}
	if fromMe {
		m.Sender = "15550000000@s.whatsapp.net"
	}
	return m
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

func TestLiveKindsRepliesPlacesAndReactions(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	lat, lon := 37.97, 23.73
	m2 := waMsg("M2", 2, "", false, "location")
	m2.Lat, m2.Lon, m2.Place = &lat, &lon, "Syntagma"
	m5 := waMsg("M5", 5, "yes", true, "text")
	m5.ReplyTo, m5.ReplyText, m5.Forwarded = "M1", "coming tonight?", true
	m6 := waMsg("M6", 6, "", false, "video")
	m6.Subtype, m6.MediaType = "gif", "video"
	m7 := waMsg("M7", 7, "look", false, "")
	m7.MediaType = "image" // what a message with no kind says: its file's
	waWrite(t, a, keys, &WABatch{Chats: map[string]string{waPeer: "Peer"}, Messages: []WAMessage{
		waMsg("M1", 1, "coming tonight?", false, "text"), m2, waMsg("M3", 3, "Nikos, +30 690 000 0000", false, "contact"),
		waMsg("M4", 4, "When?\n• Mon\n• Tue", false, "poll"), m5, m6, m7},
		Reactions: []WAReaction{{Chat: waPeer, Target: "M1", Sender: waPeer, Emoji: "👍", At: waAt(7)},
			{Chat: waPeer, Target: "M1", Sender: "15550000000@s.whatsapp.net", FromMe: true, Emoji: "❤️", At: waAt(8)}}})

	var kinds []string
	for _, k := range []string{"M1", "M2", "M3", "M4", "M5", "M6", "M7"} {
		kinds = append(kinds, kindOf(a, k))
	}
	eq(t, "kinds", kinds, []string{"text", "location", "contact", "text", "text", "video", "image"})
	eq(t, "place", msgRow(a, "M2", "lat, lon, place"), []any{37.97, 23.73, "Syntagma"})
	eq(t, "contact", msgRow(a, "M3", "text"), []any{"📇 Nikos, +30 690 000 0000"})
	eq(t, "poll", msgRow(a, "M4", "subtype, subtype_code"), []any{"poll", "whatsmeow:poll"})
	m1 := msgRow(a, "M1", "id")[0]
	eq(t, "reply", msgRow(a, "M5", "reply_to, reply_text, forwarded"), []any{m1, nil, int64(1)}) // quoted text dropped once linked
	eq(t, "gif", msgRow(a, "M6", "subtype, subtype_code"), []any{"gif", "whatsmeow:gif"})
	eq(t, "reactions", reactionsOf(a, "M1"), [][2]any{{"👍", nil}, {"❤️", int64(1)}})
	eq(t, "the chat", a.Int("SELECT count(*) FROM conversation WHERE key = '+15551234567'"), int64(1))
}

func TestLiveChangesToMessagesAlreadyThere(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	waWrite(t, a, keys, &WABatch{Messages: []WAMessage{waMsg("M1", 1, "see you at 8", false, "text"),
		waMsg("M2", 2, "oops", false, "text")},
		Reactions: []WAReaction{{Chat: waPeer, Target: "M1", Sender: waPeer, Emoji: "👍", At: waAt(3)},
			{Chat: waPeer, Target: "M1", Sender: "15550000000@s.whatsapp.net", FromMe: true, Emoji: "❤️", At: waAt(3)}}})
	// seen again (history after live): nothing changes
	eq(t, "nothing new", waWrite(t, a, keys, &WABatch{Messages: []WAMessage{waMsg("M1", 1, "see you at 8", false, "text")}}),
		map[string]int{})

	got := waWrite(t, a, keys, &WABatch{
		Changes: []WAChange{{Chat: waPeer, Target: "M1", Change: "edit", Sender: waPeer, Text: "see you at 9", At: waAt(4)},
			{Chat: waPeer, Target: "M2", Change: "delete", Sender: waPeer, At: waAt(4)}},
		Reactions: []WAReaction{{Chat: waPeer, Target: "M1", Sender: waPeer, Emoji: "😂", At: waAt(5)}, // changed
			{Chat: waPeer, Target: "M1", Sender: "15550000000@s.whatsapp.net", FromMe: true, Emoji: "", At: waAt(5)}}}) // taken back
	eq(t, "changes", got, map[string]int{"text": 1, "edited": 1, "deleted": 1, "reactions": 2})
	eq(t, "M1", msgRow(a, "M1", "text, edited"), []any{"see you at 9", int64(1)}) // the new text, as WhatsApp shows it
	eq(t, "M2", msgRow(a, "M2", "text, deleted"), []any{"oops", int64(1)})        // kept, marked
	eq(t, "found by the new", searchKeys(a, "9"), []string{"M1"})
	eq(t, "reactions", reactionsOf(a, "M1"), [][2]any{{"😂", nil}})
	// the phone's history, older than what came live, does not undo it
	waWrite(t, a, keys, &WABatch{Reactions: []WAReaction{{Chat: waPeer, Target: "M1", Sender: waPeer, Emoji: "👍", At: waAt(3),
		History: true}}})
	eq(t, "history after live", reactionsOf(a, "M1"), [][2]any{{"😂", nil}})
}

// What names a message the archive does not have yet waits for it in keys.db: a reaction, an edit,
// a receipt, the owner's read; when the message comes (the phone's history), each is applied, and
// what waited goes.
func TestWhatNamesAMessageStillToComeWaitsForIt(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	waWrite(t, a, keys, &WABatch{
		Reactions: []WAReaction{{Chat: waPeer, Target: "LATER", Sender: waPeer, Emoji: "🎉", At: waAt(5)}},
		Changes:   []WAChange{{Chat: waPeer, Target: "LATER", Change: "edit", Sender: "15550000000@s.whatsapp.net", FromMe: true, Text: "fixed", At: waAt(6)}},
		Receipts:  []WAReceipt{{Chat: waPeer, Target: "LATER", Who: waPeer, Kind: "read", At: waAt(7)}},
		Reads:     []WARead{{Chat: waPeer, IDs: []string{"THEIRS"}, At: waAt(8)}}})
	eq(t, "waiting", db.Int(keys, "SELECT count(*) FROM whatsapp_pending"), int64(4))
	waWrite(t, a, keys, &WABatch{Messages: []WAMessage{waMsg("LATER", 1, "fixd", true, "text"), waMsg("THEIRS", 2, "ok", false, "text")}})
	eq(t, "edited", msgRow(a, "LATER", "text, edited"), []any{"fixed", int64(1)})
	eq(t, "reaction", reactionsOf(a, "LATER"), [][2]any{{"🎉", nil}})
	eq(t, "receipt", a.Int("SELECT r.read_at FROM receipt r JOIN message m ON m.id = r.message_id WHERE m.key = 'LATER'"), waAt(7))
	var value int64
	a.Row("SELECT s.value FROM state_report s JOIN conversation c ON c.id = s.conversation_id "+
		"WHERE c.key = '+15551234567' AND s.field = 'read_until'", nil, &value)
	eq(t, "read up to", value, waAt(2))
	eq(t, "none waits", db.Int(keys, "SELECT count(*) FROM whatsapp_pending"), int64(0))
}

func TestLiveCalls(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	now := time.Now().UnixMilli()
	before := a.Int("SELECT count(*) FROM call")
	waWrite(t, a, keys, &WABatch{
		CallLogs: []WACallLog{{ID: "L1", Chat: waPeer, FromMe: true, Video: true, At: waAt(1), Outcome: "CONNECTED", Duration: 125},
			{ID: "L2", Chat: waPeer, At: waAt(10), Outcome: "MISSED"}},
		CallSignals: []WACallSignal{
			{ID: "E1", Kind: "offer", Chat: waPeer, Creator: waPeer, At: waAt(20)},
			{ID: "E1", Kind: "accept", At: waAt(20)},
			{ID: "E1", Kind: "terminate", At: waAt(20) + 90_000, Reason: "terminate"},
			{ID: "E2", Kind: "offer", Chat: waPeer, Creator: waPeer, At: waAt(40)},
			{ID: "E2", Kind: "reject", At: waAt(40)},
			{ID: "E3", Kind: "offer", Chat: waPeer, Creator: waPeer, At: now},      // still ringing
			{ID: "E5", Kind: "offer", Chat: waPeer, Creator: waPeer, At: waAt(50)}, // its end never seen, long ago: missed
			{ID: "E4", Kind: "offer", Chat: waPeer, Creator: waPeer, At: waAt(10)}, // the same call as L2
			{ID: "E4", Kind: "terminate", At: waAt(10), Reason: "timeout"}}})
	err := func() (err error) {
		defer archive.Recover(&err)
		NewWhatsAppWriter(a, keys, waStore).settleCalls("") // the import's, later: E5 an hour on
		a.Commit()
		return nil
	}()
	must(t, err)
	var got [][]any
	for _, r := range db.Maps(a.Tx(), "SELECT outgoing, answered, duration, detail, video FROM call ORDER BY ts") {
		got = append(got, []any{r["outgoing"], r["answered"], r["duration"], r["detail"], r["video"]})
	}
	eq(t, "count", a.Int("SELECT count(*) FROM call"), before+5)
	eq(t, "calls", got, [][]any{{int64(1), int64(1), int64(125), nil, int64(1)}, {int64(0), int64(0), int64(0), "missed", int64(0)},
		{int64(0), int64(1), int64(90), nil, int64(0)}, {int64(0), int64(0), int64(0), "rejected", int64(0)},
		{int64(0), int64(0), int64(0), "missed", int64(0)}})
	eq(t, "still ringing", db.Strs(keys, "SELECT id FROM whatsapp_call"), []string{"E3"})
	waWrite(t, a, keys, &WABatch{CallLogs: []WACallLog{{ID: "L1", Chat: waPeer, FromMe: true, Video: true, At: waAt(1),
		Outcome: "CONNECTED", Duration: 125}}}) // again: nothing new
	eq(t, "count again", a.Int("SELECT count(*) FROM call"), before+5)
}

func TestTheFilesDownloadedGoToTheirMessages(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	dir := t.TempDir()
	rel := "media/" + waPeer + "/PIC.jpg"
	must(t, os.MkdirAll(filepath.Join(dir, "media", waPeer), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, rel), []byte("a picture"), 0o600))
	pic := waMsg("PIC", 1, "look", false, "image")
	pic.MediaType, pic.File = "image", &WAFile{MediaType: "image", URL: "https://mmg.whatsapp.net/x", MediaKey: []byte{1}}
	later := waMsg("LATER", 2, "", false, "image")
	later.MediaType, later.File = "image", &WAFile{MediaType: "image", URL: "https://mmg.whatsapp.net/y", MediaKey: []byte{2}}
	once := waMsg("ONCE", 3, "", false, "image")
	once.MediaType, once.Subtype, once.File = "image", "view_once", &WAFile{MediaType: "image", URL: "https://mmg.whatsapp.net/z"}
	waWrite(t, a, keys, &WABatch{Messages: []WAMessage{pic, later, once}})
	eq(t, "to download", db.Strs(keys, "SELECT id FROM whatsapp_media ORDER BY id"), []string{"LATER", "PIC"}) // view once: left alone
	db.Exec(keys, "UPDATE whatsapp_media SET path = ? WHERE id = 'PIC'", rel)                                  // downloaded

	root := archive.MediaRoot()
	take := func() int {
		n := 0
		err := func() (err error) {
			defer archive.Recover(&err)
			w := NewWhatsAppWriter(a, keys, waStore)
			n = w.Files(dir)
			return nil
		}()
		must(t, err)
		return n
	}
	eq(t, "taken", take(), 1)
	eq(t, "again: nothing", take(), 0)
	rows := db.Maps(a.Tx(), "SELECT m.key, md.path FROM attachment t JOIN message m ON m.id = t.message_id "+
		"JOIN media md ON md.sha256 = t.sha256 JOIN source s ON s.id = t.source_id WHERE s.name = 'whatsapp-bridge'")
	if len(rows) != 1 || rows[0]["key"] != "PIC" {
		t.Fatalf("rows %v", rows)
	}
	b, err := os.ReadFile(filepath.Join(root, rows[0]["path"].(string)))
	must(t, err)
	eq(t, "file", string(b), "a picture")
	// taken into the store: the folder keeps nothing of it, nor keys.db
	if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
		t.Fatalf("still in the folder: %v", err)
	}
	eq(t, "still to download", db.Strs(keys, "SELECT id FROM whatsapp_media"), []string{"LATER"})
	// seen again with its file had: not to download again
	waWrite(t, a, keys, &WABatch{Messages: []WAMessage{pic}})
	eq(t, "had", db.Strs(keys, "SELECT id FROM whatsapp_media"), []string{"LATER"})
}

func TestLiveMentionsAndTheGroupsMembers(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	db.Exec(keys, "INSERT INTO whatsapp_group_member VALUES (?, ?, '98765@lid', '15557654321@s.whatsapp.net', '98765@lid', 0, 0)",
		waStore, waGroup)
	db.Exec(keys, "INSERT INTO whatsapp_group_member VALUES (?, ?, '15551234567@s.whatsapp.net', '15551234567@s.whatsapp.net', '', 1, 0)",
		waStore, waGroup)
	hey := WAMessage{Chat: waGroup, ID: "HEY", Sender: "98765@lid", At: waAt(1), Kind: "text", Text: "@98765 @15551234567 look",
		Mentions: []string{"98765@lid", "15551234567@s.whatsapp.net"}}
	waWrite(t, a, keys, &WABatch{Chats: map[string]string{waGroup: "Group"}, Messages: []WAMessage{hey}})
	waWrite(t, a, keys, &WABatch{Messages: []WAMessage{hey}}) // again: nothing twice
	state(t, a, keys)
	named := db.Strs(a.Tx(), "SELECT a.value FROM mention n JOIN address a ON a.id = n.address_id JOIN message m "+
		"ON m.id = n.message_id WHERE m.key = 'HEY' ORDER BY a.value")
	eq(t, "named", named, []string{"+15551234567", "+15557654321"}) // a LID as the number the group gives for it
	members := db.Strs(a.Tx(), "SELECT a.value FROM conversation_member cm JOIN conversation c ON c.id = cm.conversation_id "+
		"JOIN address a ON a.id = cm.address_id WHERE c.key = ? ORDER BY a.value", waGroup)
	eq(t, "members", members, []string{"+15551234567", "+15557654321"})
	eq(t, "sender", msgRow(a, "HEY", "sender_id")[0], a.Int("SELECT id FROM address WHERE value = '+15557654321'"))
	eq(t, "title", a.Int("SELECT count(*) FROM conversation WHERE key = ? AND title = 'Group'", waGroup), int64(1))
}

// state runs the import of what the connection keeps (the plugin's, from time to time).
func state(t *testing.T, a *archive.Archive, keys *sql.DB) map[string]int {
	t.Helper()
	w := NewWhatsAppWriter(a, keys, waStore)
	tieInstance(a, w.Source)
	got, err := WhatsAppState(w, "")
	must(t, err)
	return got
}

func TestLiveReceiptsAndWhatTheOwnerRead(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	waWrite(t, a, keys, &WABatch{Messages: []WAMessage{waMsg("MINE", 1, "seen?", true, "text"), waMsg("THEIRS", 2, "yes", false, "text")},
		Receipts: []WAReceipt{{Chat: waPeer, Target: "MINE", Who: waPeer, Kind: "delivered", At: waAt(1)},
			{Chat: waPeer, Target: "MINE", Who: waPeer, Kind: "read", At: waAt(3)},
			{Chat: waPeer, Target: "THEIRS", Who: waPeer, Kind: "read", At: waAt(4)}}, // not the owner's message: no receipt
		Reads: []WARead{{Chat: waPeer, IDs: []string{"THEIRS"}, At: waAt(5)}}})
	// the first time stays
	waWrite(t, a, keys, &WABatch{Receipts: []WAReceipt{{Chat: waPeer, Target: "MINE", Who: waPeer, Kind: "read", At: waAt(9)}}})
	var rows [][]any
	for _, r := range db.Maps(a.Tx(), "SELECT m.key, a.value, r.delivered_at, r.read_at, r.played_at FROM receipt r "+
		"JOIN message m ON m.id = r.message_id JOIN address a ON a.id = r.address_id") {
		rows = append(rows, []any{r["key"], r["value"], r["delivered_at"], r["read_at"], r["played_at"]})
	}
	eq(t, "receipts", rows, [][]any{{"MINE", "+15551234567", waAt(1), waAt(3), nil}})
	var value, changed int64
	a.Row("SELECT s.value, s.changed_at FROM state_report s JOIN conversation c ON c.id = s.conversation_id "+
		"WHERE c.key = '+15551234567' AND s.field = 'read_until'", nil, &value, &changed)
	eq(t, "read_until", []int64{value, changed}, []int64{waAt(2), waAt(5)})
}

func TestStatusAndChannelsAreNotChats(t *testing.T) {
	for _, jid := range []string{"306900000001@status", "10000000000001@lid.status", "status@broadcast", "120363000000000001@newsletter"} {
		if waConversation(nil, nil, jid, "x", nil) != 0 {
			t.Fatalf("%s is a chat", jid)
		}
	}
}

// A group's changes are notices in the group: who was added and by whom, a new name; once only,
// however many times they come; a joining said again on connecting, once a day.
func TestLiveGroupEvents(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	change := WAGroupEvent{Group: waGroup, At: waAt(5), Sender: waPeer,
		Actions: `[{"type":"added","who":"15559990000@s.whatsapp.net"},{"type":"title","title":"Friends!"}]`}
	waWrite(t, a, keys, &WABatch{Chats: map[string]string{waGroup: "Friends"}, GroupEvents: []WAGroupEvent{change}})
	waWrite(t, a, keys, &WABatch{GroupEvents: []WAGroupEvent{change}})
	var code, args string
	a.Row("SELECT n.code, n.args FROM notice n JOIN message m ON m.id = n.message_id", nil, &code, &args)
	var v map[string]any
	must(t, json.Unmarshal([]byte(args), &v))
	acts, _ := v["actions"].([]any)
	eq(t, "notice", []any{code, len(acts), v["by"] != nil}, []any{"group", 2, true})
	eq(t, "who", acts[0].(map[string]any)["who"] != nil, true)
	eq(t, "once", a.Int("SELECT count(*) FROM notice"), int64(1))
	now := time.Now().UnixMilli()
	join := WAGroupEvent{Group: waGroup, At: now, Actions: `[{"type":"joined","who":"15550000000@s.whatsapp.net"}]`, OncePerDay: true}
	waWrite(t, a, keys, &WABatch{GroupEvents: []WAGroupEvent{join}})
	join.At = now + 60_000
	waWrite(t, a, keys, &WABatch{GroupEvents: []WAGroupEvent{join}})
	eq(t, "joined once a day", a.Int("SELECT count(*) FROM notice"), int64(2))
}

// A pin (of the message it is about), a timer set, and a poll with each option's votes (WhatsApp
// names a chosen option by the SHA-256 of its text); a view-once message, and those that could not
// be read, said a day on.
func TestLivePinsTimersAndPolls(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	poll := waMsg("P1", 2, "When?\n• Mon\n• Tue", false, "poll")
	poll.Poll = `{"question":"When?","options":["Mon","Tue"],"multiple":false}`
	tue := sha256.Sum256([]byte("Tue"))
	waWrite(t, a, keys, &WABatch{
		Votes: []WAVote{{Chat: waPeer, Poll: "P1", Voter: waPeer, Options: []string{hex.EncodeToString(tue[:])}, At: waAt(3)}}})
	waWrite(t, a, keys, &WABatch{Messages: []WAMessage{waMsg("M1", 1, "pin me", false, "text"), poll},
		Notices: []WANotice{{Chat: waPeer, ID: "E1", FromMe: true, At: waAt(4), Code: "pin", Args: map[string]any{"seconds": 86400}, Target: "M1"},
			{Chat: waPeer, ID: "E2", Sender: waPeer, At: waAt(5), Code: "timer", Args: map[string]any{"seconds": 604800}},
			{Chat: waPeer, ID: "undecryptable:V", Sender: waPeer, At: waAt(6), Code: "view_once", Args: map[string]any{}}}})
	// one that could not be read long ago, one just now (it may still come), one that came after all
	for _, u := range []struct {
		id string
		at int64
	}{{"U", waAt(7)}, {"N", time.Now().UnixMilli()}, {"CAME", waAt(8)}} {
		db.Exec(keys, "INSERT INTO whatsapp_unreadable VALUES (?, ?, ?, ?, 0, ?)", waStore, waPeer, u.id, waPeer, u.at)
	}
	waWrite(t, a, keys, &WABatch{Messages: []WAMessage{waMsg("CAME", 8, "here", false, "text")}})
	state(t, a, keys)
	notice := func(key string) (string, map[string]any) {
		var code, args string
		a.Row("SELECT n.code, n.args FROM notice n JOIN message m ON m.id = n.message_id WHERE m.key = ?", []any{key}, &code, &args)
		var v map[string]any
		json.Unmarshal([]byte(args), &v)
		return code, v
	}
	code, v := notice("E1")
	eq(t, "pin", []any{code, v["seconds"], v["by"]}, []any{"pin", float64(86400), map[string]any{"self": true}})
	eq(t, "pinned", a.Int("SELECT count(*) FROM message p JOIN message m ON m.id = p.reply_to WHERE p.key = 'E1' AND m.key = 'M1'"), int64(1))
	code, v = notice("E2")
	eq(t, "timer", []any{code, v["seconds"]}, []any{"timer", float64(604800)})
	code, _ = notice("undecryptable:V")
	eq(t, "view once", code, "view_once")
	code, _ = notice("undecryptable:U")
	eq(t, "unreadable", code, "unreadable")
	code, _ = notice("undecryptable:N")
	eq(t, "not yet", code, "")
	code, _ = notice("undecryptable:CAME")
	eq(t, "came after all", code, "")
	eq(t, "still waits", db.Strs(keys, "SELECT id FROM whatsapp_unreadable"), []string{"N"})
	code, v = notice("P1")
	opts, _ := v["options"].([]any)
	eq(t, "poll", []any{code, opts[0].(map[string]any)["votes"], opts[1].(map[string]any)["votes"], v["voters"]},
		[]any{"poll", float64(0), float64(1), float64(1)})
	// another vote, once the poll is there: counted again
	mon := sha256.Sum256([]byte("Mon"))
	waWrite(t, a, keys, &WABatch{Votes: []WAVote{{Chat: waPeer, Poll: "P1", Voter: "15557654321@s.whatsapp.net",
		Options: []string{hex.EncodeToString(mon[:])}, At: waAt(9)}}})
	_, v = notice("P1")
	opts, _ = v["options"].([]any)
	eq(t, "votes", []any{opts[0].(map[string]any)["votes"], opts[1].(map[string]any)["votes"], v["voters"]},
		[]any{float64(1), float64(1), float64(2)})
}

// The iPhone's group changes whose meaning is known: a new name (12), people added (50).
func TestIphoneGroupNotices(t *testing.T) {
	a, _ := newArchive(t)
	person := newWAPeople(nil, nil, nil)
	own := a.Own()
	n := iphoneGroupNotice(a, person, own, 12, "Friends!", nil)
	eq(t, "title", n.Args["actions"].([]any)[0].(map[string]any)["title"], "Friends!")
	n = iphoneGroupNotice(a, person, own, 50, "15551234567@s.whatsapp.net;15559990001@s.whatsapp.net,15559990002@s.whatsapp.net", nil)
	eq(t, "added", []any{len(n.Args["actions"].([]any)), n.Args["by"] != nil}, []any{2, true})
	eq(t, "unknown kinds stay", iphoneGroupNotice(a, person, own, 2, "15559990001@s.whatsapp.net", nil) == nil, true)
}

// An edit and a deletion are applied where the one who made them wrote the message; another's edit
// is not. A deletion for the user alone is of any message of the chat; in a group, any member's
// deletion is taken (an admin's).
func TestLiveChangesNeedTheirAuthor(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	g := WAMessage{Chat: waGroup, ID: "G1", Sender: "15557654321@s.whatsapp.net", At: waAt(5), Kind: "text", Text: "rude"}
	waWrite(t, a, keys, &WABatch{Messages: []WAMessage{waMsg("THEIRS", 1, "teh text", false, "text"), waMsg("MINE", 2, "mine", true, "text"),
		waMsg("GONE", 3, "oops", false, "text"), waMsg("HIDDEN", 4, "theirs, hidden", false, "text"), g}})
	waWrite(t, a, keys, &WABatch{Changes: []WAChange{
		{Chat: waPeer, Target: "THEIRS", Change: "edit", Sender: waPeer, Text: "the text", At: waAt(4)},
		{Chat: waPeer, Target: "MINE", Change: "edit", Sender: waPeer, Text: "not theirs to edit", At: waAt(5)},
		{Chat: waPeer, Target: "GONE", Change: "delete", Sender: waPeer, At: waAt(6)},
		{Chat: waPeer, Target: "HIDDEN", Change: "delete_me", Sender: "15550000000@s.whatsapp.net", FromMe: true, At: waAt(7)},
		{Chat: waGroup, Target: "G1", Change: "edit", Sender: "15559990000@s.whatsapp.net", Text: "nice", At: waAt(8)},
		{Chat: waGroup, Target: "G1", Change: "delete", Sender: "15559990000@s.whatsapp.net", At: waAt(8)},
		{Chat: "15559990000@s.whatsapp.net", Target: "THEIRS", Change: "delete", Sender: "15559990000@s.whatsapp.net", At: waAt(9)}}})
	eq(t, "edited", msgRow(a, "THEIRS", "text, edited, deleted"), []any{"the text", int64(1), int64(0)}) // another chat's id: not this
	eq(t, "not another's", msgRow(a, "MINE", "text, edited"), []any{"mine", int64(0)})
	eq(t, "deleted", msgRow(a, "GONE", "deleted")[0], int64(1))
	eq(t, "deleted for me", msgRow(a, "HIDDEN", "text, deleted"), []any{"theirs, hidden", int64(1)})
	eq(t, "an admin's deletion, not their edit", msgRow(a, "G1", "text, edited, deleted"), []any{"rude", int64(0), int64(1)})
}

// What the connection keeps of chats and people comes in from time to time: the names WhatsApp
// shows, the chats' state, the people blocked (once WhatsApp said its list).
func TestLiveState(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	waWrite(t, a, keys, &WABatch{Messages: []WAMessage{waMsg("M1", 1, "hi", false, "text")}})
	db.Exec(keys, "INSERT INTO whatsmeow_contacts VALUES ('me', ?, 'Peter', 'Peter Pan', 'pp', '')", waPeer)
	db.Exec(keys, "INSERT INTO whatsmeow_chat_settings VALUES ('me', ?, -1, 1, 0)", waPeer)
	db.Exec(keys, "INSERT INTO whatsapp_blocked VALUES (?, '15559990000@s.whatsapp.net')", waStore)
	state(t, a, keys)
	eq(t, "not said yet: none blocked", a.Int("SELECT count(*) FROM blocked"), int64(0))
	db.Exec(keys, "INSERT INTO whatsapp_state VALUES (?, 'blocklist', '1', 0)", waStore)
	state(t, a, keys)
	eq(t, "blocked", db.Strs(a.Tx(), "SELECT a.value FROM blocked b JOIN address a ON a.id = b.address_id"), []string{"+15559990000"})
	reports := map[string]int64{}
	for _, r := range db.Maps(a.Tx(), "SELECT field, value FROM state_report") {
		reports[str(r["field"])] = toInt(r["value"])
	}
	eq(t, "state", reports, map[string]int64{"archived": 0, "pinned": 1, "muted": -1})
	eq(t, "names", db.Strs(a.Tx(), "SELECT name FROM handle_name ORDER BY name"), []string{"Peter Pan", "pp"})
}
