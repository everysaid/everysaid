// The records two phones both hold (SMS and calls copied from phone to phone), on small databases
// made here (shaped as the iPhone's sms.db and CallHistory.storedata, and an Android export).
package importers

import (
	"database/sql"
	"path/filepath"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

const pairTS = 1_700_000_000_000 // Unix ms of the record both phones hold

func sqliteAt(t *testing.T, path string, schema string) *sql.DB {
	t.Helper()
	d, err := db.Open(path)
	must(t, err)
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return d
}

// iphoneSMSDB is an sms.db with one SMS received at pairTS.
func iphoneSMSDB(t *testing.T, dir string) string {
	path := filepath.Join(dir, "sms.db")
	d := sqliteAt(t, path, `
CREATE TABLE handle (ROWID INTEGER PRIMARY KEY, id TEXT);
CREATE TABLE chat (ROWID INTEGER PRIMARY KEY, guid TEXT, style INTEGER, display_name TEXT);
CREATE TABLE chat_message_join (chat_id INTEGER, message_id INTEGER);
CREATE TABLE chat_handle_join (chat_id INTEGER, handle_id INTEGER);
CREATE TABLE attachment (ROWID INTEGER PRIMARY KEY, mime_type TEXT);
CREATE TABLE message_attachment_join (message_id INTEGER, attachment_id INTEGER);
CREATE TABLE message (ROWID INTEGER PRIMARY KEY, guid TEXT, date INTEGER, is_from_me INTEGER, service TEXT,
    text TEXT, attributedBody BLOB, handle_id INTEGER, cache_has_attachments INTEGER,
    associated_message_type INTEGER, associated_message_guid TEXT, associated_message_emoji TEXT, item_type INTEGER);`)
	db.Exec(d, "INSERT INTO handle VALUES (1, '+15557770001')")
	db.Exec(d, "INSERT INTO message VALUES (1, 'G1', ?, 0, 'SMS', 'see you at five', NULL, 1, 0, 0, NULL, NULL, 0)",
		(pairTS/1000-archive.AppleEpoch)*1_000_000_000)
	return path
}

// iphoneCallsDB is a CallHistory.storedata with one call received at pairTS.
func iphoneCallsDB(t *testing.T, dir string) string {
	path := filepath.Join(dir, "CallHistory.storedata")
	d := sqliteAt(t, path, `
CREATE TABLE ZHANDLE (Z_PK INTEGER PRIMARY KEY, ZVALUE TEXT);
CREATE TABLE Z_2REMOTEPARTICIPANTHANDLES (Z_2REMOTEPARTICIPANTCALLS INTEGER, Z_4REMOTEPARTICIPANTHANDLES INTEGER);
CREATE TABLE ZCALLRECORD (Z_PK INTEGER PRIMARY KEY, ZUNIQUE_ID TEXT, ZADDRESS TEXT, ZDATE REAL, ZDURATION REAL,
    ZORIGINATED INTEGER, ZANSWERED INTEGER, ZSERVICE_PROVIDER TEXT, ZCALLTYPE INTEGER);`)
	db.Exec(d, "INSERT INTO ZCALLRECORD VALUES (1, 'C1', '+15557770001', ?, 42, 0, 1, 'com.apple.Telephony', 1)",
		float64(pairTS/1000-archive.AppleEpoch))
	return path
}

// androidExport is an export with the same SMS and call, a second off.
func androidExport(t *testing.T, dir string) archive.AndroidExport {
	path := filepath.Join(dir, "android.db")
	d := sqliteAt(t, path, `
CREATE TABLE sms (_id TEXT, type TEXT, address TEXT, date TEXT, body TEXT);
CREATE TABLE mms (_id TEXT, msg_box TEXT, date TEXT);
CREATE TABLE mms_part (_id TEXT, mid TEXT, seq TEXT, ct TEXT, text TEXT, _data TEXT);
CREATE TABLE mms_addr (_id TEXT, msg_id TEXT, address TEXT, type TEXT);
CREATE TABLE calls (_id TEXT, type TEXT, number TEXT, date TEXT, duration TEXT);`)
	db.Exec(d, "INSERT INTO sms VALUES ('7', '1', '+15557770001', ?, 'see you at five')", pairTS+1000)
	db.Exec(d, "INSERT INTO calls VALUES ('9', '1', '+15557770001', ?, '42')", pairTS+1000)
	return archive.AndroidExport{Device: "acme", DB: path, Folder: dir}
}

// TestPairAcrossImports: a record both phones hold, imported once from one phone and again when
// the other phone's copy turns up, stays one record, the copy that came in first; the copy found
// later is recorded as its second origin. The keeper rule (the phone in use at the time) decides
// only between copies that come in together: before, the new phone's copy was added as a second
// record beside the one already there, which neither import knew of.
func TestPairAcrossImports(t *testing.T) {
	for _, first := range []string{"iphone", "android"} {
		t.Run(first+" first", func(t *testing.T) {
			a, _ := newArchive(t)
			dir := t.TempDir()
			smsDB, callsDB, export := iphoneSMSDB(t, dir), iphoneCallsDB(t, dir), androidExport(t, dir)
			// the other phone was the one in use then: its copy would be the one kept, were both there
			inUse, later := "acme", archive.Iphone()
			if first == "android" {
				inUse, later = later, inUse
			}
			a.Device(inUse, "")
			a.Device(later, "")
			a.Exec("UPDATE device SET used_from = ?, used_until = ? WHERE name = ?", pairTS-1000, pairTS+1_000_000, inUse)
			a.Exec("UPDATE device SET used_from = ? WHERE name = ?", pairTS+1_000_000, later)
			a.Device(inUse, "") // forget what Keeper read
			none := filepath.Join(dir, "missing.db")

			sms1, calls1 := SMSOptions{IphoneDB: smsDB, Exports: []archive.AndroidExport{}},
				CallsOptions{IphoneDB: callsDB, Exports: []archive.AndroidExport{}}
			if first == "android" {
				sms1 = SMSOptions{IphoneDB: none, Exports: []archive.AndroidExport{export}}
				calls1 = CallsOptions{IphoneDB: none, Exports: []archive.AndroidExport{export}}
			}
			must(t, SMS(a, nil, sms1))
			must(t, Calls(a, nil, calls1))
			both := func() {
				must(t, SMS(a, nil, SMSOptions{IphoneDB: smsDB, Exports: []archive.AndroidExport{export}}))
				must(t, Calls(a, nil, CallsOptions{IphoneDB: callsDB, Exports: []archive.AndroidExport{export}}))
			}
			both()
			both() // and again: nothing changes
			if n := a.Int("SELECT count(*) FROM message"); n != 1 {
				t.Errorf("messages: %d, want 1", n)
			}
			if n := a.Int("SELECT count(*) FROM call"); n != 1 {
				t.Errorf("calls: %d, want 1", n)
			}
			if n := a.Int("SELECT count(DISTINCT message_id) FROM message_origin"); n != 1 {
				t.Errorf("messages the origins name: %d, want 1", n)
			}
			if n := a.Int("SELECT count(*) FROM message_origin"); n != 2 {
				t.Errorf("message origins: %d, want 2 (both phones' rows)", n)
			}
			if n := a.Int("SELECT count(*) FROM call_origin"); n != 2 {
				t.Errorf("call origins: %d, want 2 (both phones' rows)", n)
			}
		})
	}
	t.Run("together", func(t *testing.T) { // the keeper rule, unchanged
		a, _ := newArchive(t)
		dir := t.TempDir()
		smsDB, export := iphoneSMSDB(t, dir), androidExport(t, dir)
		a.Device("acme", "")
		a.Exec("UPDATE device SET used_from = ?, used_until = ? WHERE name = 'acme'", pairTS-1000, pairTS+1_000_000)
		a.Device("acme", "")
		must(t, SMS(a, nil, SMSOptions{IphoneDB: smsDB, Exports: []archive.AndroidExport{export}}))
		if ts := a.Int("SELECT ts FROM message"); ts != pairTS+1000 {
			t.Errorf("kept the copy of %d, want the Android one's (%d)", ts, pairTS+1000)
		}
		if n := a.Int("SELECT count(*) FROM message"); n != 1 {
			t.Errorf("messages: %d, want 1", n)
		}
	})
}

// TestIMessageRepliesEditsUnsendsAndTapbacks: what an iMessage carries beyond its text, as the
// Messages app shows it: an inline reply (thread_originator_guid) answers its message, an edited
// message is marked edited, one unsent is marked deleted, and a tapback taken back (its 3000s
// code) leaves the message it was on, while another stays.
func TestIMessageRepliesEditsUnsendsAndTapbacks(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "sms.db")
	d := sqliteAt(t, path, `
CREATE TABLE handle (ROWID INTEGER PRIMARY KEY, id TEXT);
CREATE TABLE chat (ROWID INTEGER PRIMARY KEY, guid TEXT, style INTEGER, display_name TEXT);
CREATE TABLE chat_message_join (chat_id INTEGER, message_id INTEGER);
CREATE TABLE chat_handle_join (chat_id INTEGER, handle_id INTEGER);
CREATE TABLE attachment (ROWID INTEGER PRIMARY KEY, mime_type TEXT);
CREATE TABLE message_attachment_join (message_id INTEGER, attachment_id INTEGER);
CREATE TABLE message (ROWID INTEGER PRIMARY KEY, guid TEXT, date INTEGER, is_from_me INTEGER, service TEXT,
    text TEXT, attributedBody BLOB, handle_id INTEGER, cache_has_attachments INTEGER,
    associated_message_type INTEGER, associated_message_guid TEXT, associated_message_emoji TEXT, item_type INTEGER,
    thread_originator_guid TEXT, date_edited INTEGER, date_retracted INTEGER);`)
	db.Exec(d, "INSERT INTO handle VALUES (1, '+15557770001')")
	at := func(n int64) int64 { return (pairTS/1000 - archive.AppleEpoch + n) * 1_000_000_000 }
	for _, r := range [][]any{
		{1, "A", at(1), 0, "a question", 0, nil, nil, nil, 0, 0},
		{2, "B", at(2), 1, "the answer", 0, nil, nil, "A", 0, 0},
		{3, "C", at(3), 0, "fixed typo", 0, nil, nil, nil, at(4), 0},
		{4, "D", at(5), 1, nil, 0, nil, nil, nil, at(6), at(6)},
		{5, "E", at(7), 0, "Loved “the answer”", 2000, "p:0/B", nil, nil, 0, 0},
		{6, "F", at(8), 0, "Liked “the answer”", 2001, "p:0/B", nil, nil, 0, 0},
		{7, "G", at(9), 0, "Removed a like from “the answer”", 3001, "p:0/B", nil, nil, 0, 0},
	} {
		db.Exec(d, "INSERT INTO message VALUES (?, ?, ?, ?, 'iMessage', ?, NULL, 1, 0, ?, ?, ?, 0, ?, ?, ?)", r...)
	}
	opt := SMSOptions{IphoneDB: path, Exports: []archive.AndroidExport{}}
	must(t, SMS(a, nil, opt))
	must(t, SMS(a, nil, opt)) // again: nothing changes
	eq(t, "reply", a.Int("SELECT reply_to FROM message WHERE key = 'B'"), a.Int("SELECT id FROM message WHERE key = 'A'"))
	eq(t, "edited", a.Int("SELECT edited FROM message WHERE key = 'C'"), int64(1))
	eq(t, "unsent", a.Int("SELECT deleted FROM message WHERE key = 'D'"), int64(1))
	eq(t, "not edited", a.Int("SELECT edited + deleted FROM message WHERE key = 'A'"), int64(0))
	eq(t, "tapbacks", db.Strs(a.Tx(), "SELECT r.emoji FROM reaction r JOIN message m ON m.id = r.message_id "+
		"WHERE m.key = 'B' ORDER BY r.emoji"), []string{"❤️"})
}
