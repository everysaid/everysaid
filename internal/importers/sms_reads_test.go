package importers

import (
	"path/filepath"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// How far the owner read each conversation on the iPhone, and which of the owner's iMessages the
// other got and read, from sms.db.
func TestSMSReads(t *testing.T) {
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
    is_read INTEGER, date_read INTEGER, is_delivered INTEGER, date_delivered INTEGER);`)
	apple := func(unixMS int64) int64 { return (unixMS/1000 - archive.AppleEpoch) * 1_000_000_000 }
	t0 := int64(1_790_000_000_000)
	db.Exec(d, "INSERT INTO handle VALUES (1, '+15557770001'), (2, '+15557770002')")
	db.Exec(d, "INSERT INTO chat VALUES (1, 'iMessage;-;+15557770002', 45, NULL)")
	db.Exec(d, "INSERT INTO chat_handle_join VALUES (1, 2)")
	db.Exec(d, "INSERT INTO chat_message_join VALUES (1, 3), (1, 4)")
	msg := "INSERT INTO message VALUES (?, ?, ?, ?, ?, ?, NULL, ?, 0, 0, NULL, NULL, 0, ?, ?, ?, ?)"
	db.Exec(d, msg, 1, "G1", apple(t0), 0, "SMS", "read", 1, 1, apple(t0+60_000), 0, 0)
	db.Exec(d, msg, 2, "G2", apple(t0+120_000), 0, "SMS", "not yet", 1, 0, 0, 0, 0)
	db.Exec(d, msg, 3, "G3", apple(t0+180_000), 1, "iMessage", "mine", 0, 1, apple(t0+300_000), 1, apple(t0+200_000))
	db.Exec(d, msg, 4, "G4", apple(t0+400_000), 0, "iMessage", "theirs", 2, 1, apple(t0+500_000), 0, 0)
	a.Exec("INSERT INTO plugin_instance (plugin, kind, label, created_at) VALUES ('iphone-backup', 'source', 'P', 0)")
	a.Exec("INSERT INTO source (name, path, instance_id) VALUES (?, ?, ?)", archive.Iphone()+"/sms", path,
		a.Int("SELECT max(id) FROM plugin_instance"))

	must(t, SMS(a, nil, SMSOptions{IphoneDB: path, Exports: []archive.AndroidExport{}}))
	readUntil := func(key string) []any {
		var v, changed int64
		a.Row("SELECT r.value, r.changed_at FROM state_report r JOIN message m ON m.conversation_id = r.conversation_id "+
			"JOIN message_origin o ON o.message_id = m.id WHERE o.row_key = ? AND r.field = 'read_until'", []any{key}, &v, &changed)
		return []any{v, changed}
	}
	eq(t, "SMS: read up to the first, read a minute later", readUntil("G1"), []any{t0, t0 + 60_000})
	eq(t, "iMessage: read up to theirs", readUntil("G4"), []any{t0 + 400_000, t0 + 500_000})
	var delivered, read int64
	a.Row("SELECT delivered_at, read_at FROM receipt r JOIN message m ON m.id = r.message_id WHERE m.key = 'G3'", nil,
		&delivered, &read)
	eq(t, "mine got and read", []any{delivered, read}, []any{t0 + 200_000, t0 + 300_000})
	eq(t, "no receipts of SMS", a.Int("SELECT count(*) FROM receipt"), int64(1))
}
