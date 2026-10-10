package importers

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
)

func writeAt(t *testing.T, path, data string, at int64) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte(data), 0o600))
	must(t, os.Chtimes(path, time.Unix(at, 0), time.Unix(at, 0)))
}

func shaOf(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// The iPhone's WhatsApp photos: a person's two, the whole picture preferred to its thumbnail, a
// group's, and one of a stranger left out; kept in the extract, stored once, as JPEGs.
func TestPhotosWhatsAppIphone(t *testing.T) {
	t.Cleanup(config.Load)
	t.Setenv("EVERYSAID_CACHE", t.TempDir())
	t.Setenv("EVERYSAID_DATA", t.TempDir())
	config.Load()
	a, _ := newArchive(t)
	maria := address("+15557770001")
	a.Address(maria)
	group := a.Conversation("whatsapp", []archive.Handle{maria}, "15557770001-1600000000@g.us", "Friends")
	dir := filepath.Join(archive.IphoneData(), "whatsapp-photos")
	jpeg := "\xff\xd8\xff\xe0"
	writeAt(t, filepath.Join(dir, "15557770001-1700000000.thumb"), jpeg+"old", 1)
	writeAt(t, filepath.Join(dir, "15557770001-1710000000.thumb"), jpeg+"new thumb", 1)
	writeAt(t, filepath.Join(dir, "15557770001-1710000000.jpg"), jpeg+"new", 1)
	writeAt(t, filepath.Join(dir, "15557770001-1600000000-1720000000.thumb"), jpeg+"group", 1)
	writeAt(t, filepath.Join(dir, "15559999999-1700000000.thumb"), jpeg+"stranger", 1)
	writeAt(t, filepath.Join(dir, "Photo.jpg"), jpeg+"other", 1)
	must(t, Media(a, nil, PhotosWhatsAppIphone))
	eq(t, "person", photoRows(a, "handle_photo"),
		shaOf(jpeg+"old")+" 1700000000-1700000000 -|"+shaOf(jpeg+"new")+" 1710000000-1710000000 -")
	eq(t, "group", photoRows(a, "chat_photo"), shaOf(jpeg+"group")+" 1720000000-1720000000 -")
	eq(t, "group's conversation", a.Int("SELECT conversation_id FROM chat_photo"), group)
	eq(t, "stored", db.Strs(a.Tx(), "SELECT mime || ' ' || substr(path, -4) FROM media"),
		[]string{"image/jpeg .jpg", "image/jpeg .jpg", "image/jpeg .jpg"})
	if _, err := os.Stat(filepath.Join(dir, "15557770001-1700000000.thumb")); err != nil {
		t.Fatal("the extract's file was taken: ", err)
	}
	must(t, Media(a, nil, PhotosWhatsAppIphone))
	eq(t, "again", a.Int("SELECT count(*) FROM handle_photo"), int64(2))
}

// The iPhone's Viber: a member's photo before and now (named by its id, or its MD5), a group's.
func TestPhotosViberIphone(t *testing.T) {
	t.Cleanup(config.Load)
	t.Setenv("EVERYSAID_CACHE", t.TempDir())
	t.Setenv("EVERYSAID_DATA", t.TempDir())
	config.Load()
	a, _ := newArchive(t)
	maria := address("+15557770001")
	a.Address(maria)
	group := a.Conversation("viber", []archive.Handle{maria}, "group:42", "Friends")
	must(t, os.MkdirAll(archive.IphoneData(), 0o700))
	d, err := db.Open(ViberIphoneDB())
	must(t, err)
	db.Exec(d, "CREATE TABLE ZMEMBER (Z_PK INTEGER, ZMEMBERID TEXT, ZICONID TEXT, ZPREVICONID TEXT)")
	db.Exec(d, "CREATE TABLE ZPHONENUMBER (ZMEMBER INTEGER, ZCANONIZEDPHONENUM TEXT, ZPHONE TEXT)")
	db.Exec(d, "CREATE TABLE ZCONVERSATION (Z_PK INTEGER, ZGROUPID INTEGER, ZICONID TEXT)")
	db.Exec(d, "INSERT INTO ZMEMBER VALUES (1, 'mid-maria', '0-04-05-new', '0-04-05-old'), (2, 'mid-bob', '0-04-05-bob', NULL)")
	db.Exec(d, "INSERT INTO ZPHONENUMBER VALUES (1, '+15557770001', NULL), (2, '+15557770002', NULL)")
	db.Exec(d, "INSERT INTO ZCONVERSATION VALUES (1, 42, '0-04-05-group'), (2, NULL, NULL)")
	d.Close()
	must(t, os.Chtimes(ViberIphoneDB(), time.Unix(1_800_000_000, 0), time.Unix(1_800_000_000, 0)))
	dir := filepath.Join(archive.IphoneData(), "viber-photos")
	md5Old := md5.Sum([]byte("0-04-05-old"))
	writeAt(t, filepath.Join(dir, hex.EncodeToString(md5Old[:])+".jpg"), "old", 1_700_000_000)
	writeAt(t, filepath.Join(dir, "0-04-05-new"), "new", 1_750_000_000)
	writeAt(t, filepath.Join(dir, "0-04-05-bob"), "bob", 1_750_000_000) // bob: not in the archive
	writeAt(t, filepath.Join(dir, "0-04-05-group"), "group", 1_760_000_000)
	must(t, Media(a, nil, PhotosViberIphone))
	eq(t, "member", photoRows(a, "handle_photo"),
		shaOf("old")+" 1700000000-1700000000 -|"+shaOf("new")+" 1750000000-1800000000 -")
	eq(t, "group", photoRows(a, "chat_photo"), shaOf("group")+" 1760000000-1800000000 -")
	eq(t, "group's conversation", a.Int("SELECT conversation_id FROM chat_photo"), group)
}

// Viber Desktop: the Avatars named by a contact's DownloadID and a group's IconID.
func TestViberDesktopPhotos(t *testing.T) {
	t.Cleanup(config.Load)
	t.Setenv("EVERYSAID_DATA", t.TempDir())
	config.Load()
	a, _ := newArchive(t)
	maria := address("+15557770001")
	a.Address(maria)
	group := a.Conversation("viber", []archive.Handle{maria}, "group:7701", "Friends")
	path := filepath.Join(t.TempDir(), "viber.db")
	f := newDesktop(t, path)
	db.Exec(f.d, "UPDATE Contact SET DownloadID = '0-04-05-maria' WHERE ContactID = 2")
	db.Exec(f.d, "UPDATE ChatInfo SET IconID = '0-04-05-friends' WHERE ChatID = 2")
	f.d.Close()
	must(t, os.Chtimes(path, time.Unix(1_800_000_000, 0), time.Unix(1_800_000_000, 0)))
	avatars := filepath.Join(t.TempDir(), "15550000000", "Avatars")
	writeAt(t, filepath.Join(avatars, "0-04-05-maria.jpg"), "maria", 1_750_000_000)
	writeAt(t, filepath.Join(avatars, "0-04-05-friends.jpg"), "friends", 1_760_000_000)
	must(t, Media(a, nil, ViberDesktopPhotos(path, avatars)))
	eq(t, "member", photoRows(a, "handle_photo"), shaOf("maria")+" 1750000000-1800000000 -")
	eq(t, "group", photoRows(a, "chat_photo"), shaOf("friends")+" 1760000000-1800000000 -")
	eq(t, "group's conversation", a.Int("SELECT conversation_id FROM chat_photo"), group)
	if _, err := os.Stat(filepath.Join(avatars, "0-04-05-maria.jpg")); err != nil {
		t.Fatal("Viber's own file was taken: ", err)
	}
}
