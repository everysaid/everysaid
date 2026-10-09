package importers

import (
	"os"
	"path/filepath"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// Telegram: whom a forwarded message came from (a known user by name, a hidden one by the name it
// left), the album of files sent together, and the pin as it is now (unpinned later: no longer).
func TestTelegramForwardAlbumPin(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "telegram.db")
	d := telegramDB(t, path)
	add := func(id int64, m M) {
		db.Exec(d, "INSERT INTO message (chat_id, id, date, json) VALUES (?, ?, ?, ?)", tgMaria, id, 1_790_000_100+id, js(m))
	}
	add(30, M{"_": "Message", "id": 30, "from_id": M{"user_id": tgMaria}, "message": "a", "grouped_id": 9001, "pinned": true,
		"fwd_from": M{"_": "MessageFwdHeader", "from_id": M{"user_id": tgBob}}})
	add(31, M{"_": "Message", "id": 31, "from_id": M{"user_id": tgMaria}, "message": "b", "grouped_id": 9001,
		"fwd_from": M{"_": "MessageFwdHeader", "from_name": "Hidden Person"}})
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path}))
	eq(t, "30", msgRow(a, "30", "forward_from, album, pinned"), []any{"Bob", "9001", int64(-1)})
	eq(t, "31", msgRow(a, "31", "forward_from, album, pinned"), []any{"Hidden Person", "9001", nil})

	db.Exec(d, "UPDATE message SET json = json_set(json, '$.pinned', json('false')) WHERE id = 30")
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path}))
	eq(t, "unpinned", msgRow(a, "30", "pinned")[0], nil)

	// a message imported before these were kept takes them
	a.Exec("UPDATE message SET forward_from = NULL, album = NULL WHERE key = '31'")
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path}))
	eq(t, "filled", msgRow(a, "31", "forward_from, album"), []any{"Hidden Person", "9001"})
}

// WhatsApp's bridge: a pin for a day lasts a day from its notice, an unpin ends a pin; the
// album and the channel a message was forwarded from, also for a message imported before.
func TestWhatsAppBridgePinsAlbum(t *testing.T) {
	a, _ := newArchive(t)
	path, d := bridgeDB(t, t.TempDir(), newBridgeSchema+`
		ALTER TABLE messages ADD COLUMN forward_from TEXT; ALTER TABLE messages ADD COLUMN album TEXT;
		CREATE TABLE chat_events (chat_jid TEXT, id TEXT, sender TEXT, is_from_me BOOLEAN, timestamp TIMESTAMP, code TEXT,
			args TEXT, target TEXT, PRIMARY KEY (chat_jid, id));`)
	waMessage(d, "M1", 1, "one", 0, M{"kind": "text"})
	waMessage(d, "M2", 2, "two", 1, M{"kind": "text", "album": "ALB", "forward_from": "News"})
	db.Exec(d, "INSERT INTO chat_events VALUES (?, 'E1', '', 1, ?, 'pin', '{\"seconds\":86400}', 'M1')", waPeer, waTS(4))
	db.Exec(d, "INSERT INTO chat_events VALUES (?, 'E2', '', 1, ?, 'pin', '{\"seconds\":null}', 'M2')", waPeer, waTS(5))
	db.Exec(d, "INSERT INTO chat_events VALUES (?, 'E3', '', 1, ?, 'unpin', '{}', 'M2')", waPeer, waTS(6))
	runBridge(t, a, path)
	pinAt := a.Int("SELECT ts FROM message WHERE key = 'E1'")
	eq(t, "pinned a day", msgRow(a, "M1", "pinned")[0], pinAt+86_400_000)
	eq(t, "unpinned", msgRow(a, "M2", "pinned")[0], nil)
	eq(t, "album", msgRow(a, "M2", "forward_from, album"), []any{"News", "ALB"})

	a.Exec("UPDATE message SET forward_from = NULL, album = NULL WHERE key = 'M2'")
	runBridge(t, a, path)
	eq(t, "filled", msgRow(a, "M2", "forward_from, album"), []any{"News", "ALB"})
}

// A file's name as it was sent, kept with the link; a file linked before names were kept takes it.
func TestLinkNamed(t *testing.T) {
	a, _ := newArchive(t)
	src := a.Source("test", "test", "", "")
	conv := a.Conversation("telegram", nil, "chat", "")
	mid := a.AddMessage(src, "1", archive.Message{Service: "telegram", ConversationID: conv, TS: 1, Outgoing: true, Kind: "file"})
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "x.pdf"), []byte("pdf"), 0o600))
	s := NewStore(a)
	s.Root = t.TempDir()
	s.Link("test", src, filepath.Join(dir, "x.pdf"), "x.pdf", mid)
	eq(t, "no name", a.Int("SELECT name IS NULL FROM attachment"), int64(1))
	s.LinkNamed("test", src, filepath.Join(dir, "x.pdf"), "x.pdf", mid, "Report.pdf")
	var name string
	a.Row("SELECT name FROM attachment", nil, &name)
	eq(t, "named", name, "Report.pdf")
	eq(t, "once", a.Int("SELECT count(*) FROM attachment"), int64(1))
}
