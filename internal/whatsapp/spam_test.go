package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

// A chat removed as spam leaves what the bridge keeps, under its number and its LID (the device's
// store maps one to the other), with its downloaded files; the others stay.
func TestForgetDropsTheChat(t *testing.T) {
	f := bridgeInstance(t)
	lid := "99887766554433@lid"
	devices, err := Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(devices, "INSERT OR REPLACE INTO whatsmeow_lid_map (lid, pn) VALUES ('99887766554433', '15551234567')")
	file := filepath.Join("media", "x", "M1.jpg")
	os.MkdirAll(filepath.Join(f.dir, "media", "x"), 0o700)
	os.WriteFile(filepath.Join(f.dir, file), []byte("jpg"), 0o600)
	f.ms.storeChat(lid, "Peer", time.Now())
	f.ms.storeChat("15550000001@s.whatsapp.net", "Other", time.Now())
	for _, r := range [][3]string{{"M1", peer, file}, {"M2", lid, ""}, {"M3", "15550000001@s.whatsapp.net", ""}} {
		f.ms.remember(StoredMessage{ID: r[0], ChatJID: r[1], Sender: r[1], Timestamp: time.Now()})
		f.ms.exec("INSERT INTO whatsapp_media (store, chat_jid, id, at, media_type, path) VALUES (?, ?, ?, 0, 'image', ?)",
			f.ms.key, r[1], r[0], db.NullStr(r[2]))
	}
	if err := (Plugin{}).Forget(f.ctx(), plugins.Conversation{Key: "+15551234567", Service: "whatsapp"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SELECT id FROM whatsapp_recent WHERE store = ? ORDER BY id", "SELECT id FROM whatsapp_media WHERE store = ? ORDER BY id"} {
		if got := db.Strs(f.ms.db, q, f.ms.key); len(got) != 1 || got[0] != "M3" {
			t.Fatalf("left: %v", got)
		}
	}
	if got := db.Strs(f.ms.db, "SELECT jid FROM whatsapp_chat WHERE store = ? ORDER BY jid", f.ms.key); len(got) != 1 {
		t.Fatalf("chats left: %v", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, file)); !os.IsNotExist(err) {
		t.Fatal("its file is still there")
	}
}
