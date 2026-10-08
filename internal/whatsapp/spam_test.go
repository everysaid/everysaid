package whatsapp

import (
	"os"
	"path/filepath"
	"testing"

	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

// A chat removed as spam leaves messages.db under its number and its LID (the device's store maps
// one to the other), with its downloaded files; the others stay.
func TestForgetDropsTheChat(t *testing.T) {
	f := bridgeInstance(t)
	lid := "99887766554433@lid"
	devices, err := db.Open(filepath.Join(f.dir, "whatsapp.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(devices, "CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT UNIQUE NOT NULL)")
	db.Exec(devices, "INSERT INTO whatsmeow_lid_map VALUES ('99887766554433', '15551234567')")
	devices.Close()
	file := filepath.Join("media", "x", "M1.jpg")
	os.MkdirAll(filepath.Join(f.dir, "media", "x"), 0o700)
	os.WriteFile(filepath.Join(f.dir, file), []byte("jpg"), 0o600)
	db.Exec(f.ms.db, "INSERT INTO chats VALUES (?, 'Peer', ?), ('15550000001@s.whatsapp.net', 'Other', ?)", lid, ts(30), ts(30))
	for _, r := range [][3]string{{"M1", peer, file}, {"M2", lid, ""}, {"M3", "15550000001@s.whatsapp.net", ""}} {
		db.Exec(f.ms.db, "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_path) VALUES (?, ?, ?, 'win', ?, 0, ?)",
			r[0], r[1], r[1], ts(1), r[2])
	}
	if err := (Plugin{}).Forget(f.ctx(), plugins.Conversation{Key: "+15551234567", Service: "whatsapp"}); err != nil {
		t.Fatal(err)
	}
	if got := db.Strs(f.ms.db, "SELECT id FROM messages ORDER BY id"); len(got) != 1 || got[0] != "M3" {
		t.Fatalf("left: %v", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, file)); !os.IsNotExist(err) {
		t.Fatal("its file is still there")
	}
}
