package telegram

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/tg"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/importers"
)

// The photos as the users and chats carry them: one not had downloaded, one seen again moving its
// sighting, a removal recorded, a broadcast channel's left alone; then into the archive, the files
// taken.
func TestPhotosFromEntities(t *testing.T) {
	store, err := openStore(DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	defer db.Exec(store, "DELETE FROM telegram_photo; DELETE FROM telegram_chat WHERE id IN (501, -502, -1000000000503)")
	user := &tg.User{ID: 501, AccessHash: 9, Phone: "15557770001", Photo: &tg.UserProfilePhoto{PhotoID: 70}}
	user.SetFlags()
	group := &tg.Chat{ID: 502, Title: "Friends", Photo: &tg.ChatPhoto{PhotoID: 80}}
	channel := &tg.Channel{ID: 503, AccessHash: 1, Broadcast: true, Title: "News", Photo: &tg.ChatPhoto{PhotoID: 90}}
	channel.SetFlags()
	for _, r := range []struct {
		id   int64
		kind string
		js   string
	}{{501, "user", Dump(user)}, {-502, "group", Dump(group)}, {-1000000000503, "channel", Dump(channel)}} {
		db.Exec(store, "INSERT OR REPLACE INTO telegram_chat (id, kind, title, archived, json) VALUES (?, ?, 'x', 0, ?)", r.id, r.kind, r.js)
	}
	// a group's member the account has no chat with: not asked for
	stranger := &tg.User{ID: 504, AccessHash: 3, Photo: &tg.UserProfilePhoto{PhotoID: 60}}
	stranger.SetFlags()
	db.Exec(store, "INSERT OR REPLACE INTO telegram_entity VALUES (504, ?)", Dump(stranger))
	defer db.Exec(store, "DELETE FROM telegram_entity WHERE id = 504; DELETE FROM telegram_photo_fail")
	// a download that failed lately: not again yet
	db.Exec(store, "INSERT INTO telegram_photo_fail VALUES (-502, 80, 900)")
	if todo, _, _ := scanPhotos(store, 1000); len(todo) != 1 || todo[0].peer != 501 {
		t.Fatalf("to download: %+v", todo)
	}
	db.Exec(store, "UPDATE telegram_photo_fail SET at = ?", 1000-int64(photoRetry.Seconds()))
	todo, changed, err := scanPhotos(store, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(todo) != 2 || changed != 0 {
		t.Fatalf("to download: %+v (%d changed)", todo, changed)
	}
	for _, w := range todo {
		if err := keepPhoto(store, w, []byte{0xff, 0xd8, 0xff, byte(w.photoID)}, 1000); err != nil {
			t.Fatal(err)
		}
	}
	if todo, _, _ = scanPhotos(store, 1000+int64(photoEvery.Seconds())); len(todo) != 0 {
		t.Fatalf("again: %+v", todo)
	}
	if n := db.Int(store, "SELECT count(*) FROM telegram_photo WHERE last_seen > first_seen"); n != 2 {
		t.Fatalf("sightings moved: %d", n)
	}
	group.Photo = &tg.ChatPhotoEmpty{}
	db.Exec(store, "UPDATE telegram_chat SET json = ? WHERE id = -502", Dump(group))
	if _, changed, _ = scanPhotos(store, 5_000_000); changed != 2 { // the removal, and the user's seen again
		t.Fatalf("changed %d", changed)
	}

	a, err := archive.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Address(archive.H("phone", "+15557770001"))
	a.Conversation("telegram", nil, "-502", "Friends")
	a.Commit()
	if err := importers.Media(a, nil, importers.PhotosTelegram); err != nil {
		t.Fatal(err)
	}
	if got := a.Int("SELECT count(*) FROM handle_photo WHERE sha256 IS NOT NULL AND last_seen = 5000000"); got != 1 {
		t.Fatalf("the user's photo: %d", got)
	}
	if got := a.Int("SELECT count(*) FROM chat_photo WHERE removed_at = 5000000") + a.Int("SELECT count(*) FROM chat_photo WHERE sha256 IS NULL"); got != 2 {
		t.Fatalf("the group's photo and its removal: %d", got)
	}
	if _, err := os.Stat(filepath.Join(MediaPath(), "photos", "501", "70.jpg")); !os.IsNotExist(err) {
		t.Fatalf("the downloaded file is still there: %v", err)
	}
	if err := importers.Media(a, nil, importers.PhotosTelegram); err != nil {
		t.Fatal(err)
	}
	if got := a.Int("SELECT count(*) FROM handle_photo") + a.Int("SELECT count(*) FROM chat_photo"); got != 3 {
		t.Fatalf("again: %d rows", got)
	}
}
