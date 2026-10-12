// The previews of WhatsApp's links, from the iPhone (its data items, the pictures iphone-sync took
// out) and from the bridge (its preview columns, the high-quality picture coming later).
package importers

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	must(t, jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil))
	return b.Bytes()
}

// previewOf is a message's preview: url, title, description, and its picture's width (0: none).
func previewOf(a *archive.Archive, key string) []any {
	m := db.Maps(a.Tx(), "SELECT p.url, p.title, p.description, coalesce(i.width, 0) AS w FROM link_preview p "+
		"JOIN message m ON m.id = p.message_id LEFT JOIN link_preview_image i ON i.sha256 = p.image WHERE m.key = ?", key)
	if len(m) == 0 {
		return nil
	}
	return []any{m[0]["url"], m[0]["title"], m[0]["description"], m[0]["w"]}
}

func TestWhatsAppIphonePreviews(t *testing.T) {
	a, _ := newArchive(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "whatsapp.sqlite")
	d := sqliteAt(t, path, `
CREATE TABLE ZWACHATSESSION (Z_PK INTEGER PRIMARY KEY, ZCONTACTJID TEXT, ZPARTNERNAME TEXT, ZSESSIONTYPE INTEGER, ZARCHIVED INTEGER);
CREATE TABLE ZWAGROUPMEMBER (Z_PK INTEGER PRIMARY KEY, ZCHATSESSION INTEGER, ZMEMBERJID TEXT);
CREATE TABLE ZWAMEDIAITEM (Z_PK INTEGER PRIMARY KEY, ZMETADATA BLOB, ZLATITUDE REAL, ZLONGITUDE REAL, ZTITLE TEXT,
    ZVCARDNAME TEXT, ZVCARDSTRING TEXT, ZMEDIAURL TEXT, ZXMPPTHUMBPATH TEXT);
CREATE TABLE ZWAMESSAGEINFO (Z_PK INTEGER PRIMARY KEY, ZRECEIPTINFO BLOB);
CREATE TABLE ZWAPROFILEPUSHNAME (ZJID TEXT, ZPUSHNAME TEXT);
CREATE TABLE ZWACHATPUSHCONFIG (ZJID TEXT, ZMUTEDUNTIL REAL);
CREATE TABLE ZWAMESSAGE (Z_PK INTEGER PRIMARY KEY, ZCHATSESSION INTEGER, ZMESSAGEDATE REAL, ZISFROMME INTEGER,
    ZMESSAGETYPE INTEGER, ZMEDIAITEM INTEGER, ZFROMJID TEXT, ZGROUPMEMBER INTEGER, ZMESSAGEINFO INTEGER, ZTEXT TEXT,
    ZSTANZAID TEXT, ZSTARRED INTEGER, ZGROUPEVENTTYPE INTEGER);
CREATE TABLE ZWAMESSAGEDATAITEM (Z_PK INTEGER PRIMARY KEY, ZMESSAGE INTEGER, ZINDEX INTEGER, ZCONTENT1 TEXT,
    ZMATCHEDTEXT TEXT, ZTITLE TEXT, ZSUMMARY TEXT, ZTHUMBNAILPATH TEXT);`)
	db.Exec(d, "INSERT INTO ZWACHATSESSION VALUES (1, ?, 'Peer', 0, 0)", waPeer)
	at := float64(pairTS/1000 - archive.AppleEpoch)
	// a link with its whole preview (and a second link, not shown); a link the app found no
	// preview for (the link in the summary); a link message whose preview has only a title
	db.Exec(d, "INSERT INTO ZWAMEDIAITEM (Z_PK, ZTITLE, ZMEDIAURL, ZXMPPTHUMBPATH) VALUES "+
		"(1, 'A page', 'https://example.org/a', 'Media/x/a.thumb'), (3, 'Only a title', 'https://example.org/c', NULL)")
	db.Exec(d, "INSERT INTO ZWAMESSAGE (Z_PK, ZCHATSESSION, ZMESSAGEDATE, ZISFROMME, ZMESSAGETYPE, ZMEDIAITEM, ZFROMJID, ZTEXT, "+
		"ZSTANZAID) VALUES (1, 1, ?, 0, 7, 1, ?, 'see https://example.org/a and b.org', 'W1'), "+
		"(2, 1, ?, 1, 0, NULL, NULL, 'b.org', 'W2'), (3, 1, ?, 0, 7, 3, ?, 'https://example.org/c', 'W3')",
		at, waPeer, at+1, at+2, waPeer)
	db.Exec(d, "INSERT INTO ZWAMESSAGEDATAITEM VALUES "+
		"(1, 1, 0, 'https://example.org/a', 'https://example.org/a', 'A page', 'About it', 'Media/x/a.thumb'), "+
		"(2, 1, 1, 'http://b.org', 'b.org', 'Other', 'Not this one', NULL), "+
		"(3, 2, 0, 'http://b.org', 'b.org', NULL, 'b.org', NULL), "+
		"(4, 3, 0, 'https://example.org/c', 'https://example.org/c', 'Only a title', 'https://example.org/c', NULL)")
	must(t, os.MkdirAll(filepath.Join(dir, WhatsAppPreviewFolder, "x"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, WhatsAppPreviewFolder, "x", "a.thumb"), testJPEG(t, 30, 20), 0o600))

	run := func() map[string]int {
		got, err := WhatsApp(a, nil, WhatsAppOptions{IphoneDB: path, NoContacts: true, NoBridge: true, NoStore: true})
		must(t, err)
		return got
	}
	run()
	eq(t, "W1", previewOf(a, "W1"), []any{"https://example.org/a", "A page", "About it", int64(30)})
	eq(t, "W2", previewOf(a, "W2"), []any(nil))
	eq(t, "W3", previewOf(a, "W3"), []any{"https://example.org/c", "Only a title", nil, int64(0)})
	eq(t, "the same again", run()["preview"], 0)

	// messages imported before there were previews get theirs
	a.Exec("DELETE FROM link_preview")
	a.Exec("DELETE FROM link_preview_image")
	eq(t, "backfilled", run()["preview"], 2)
	eq(t, "W1 again", previewOf(a, "W1"), []any{"https://example.org/a", "A page", "About it", int64(30)})
	eq(t, "W2 not on the list", WhatsAppPreviewThumbs(path), map[string]bool{"x/a.thumb": true})
}

func TestBridgePreviews(t *testing.T) {
	a, _ := newArchive(t)
	path, d := bridgeDB(t, t.TempDir(), newBridgeSchema+`
ALTER TABLE messages ADD COLUMN preview_url TEXT; ALTER TABLE messages ADD COLUMN preview_title TEXT;
ALTER TABLE messages ADD COLUMN preview_description TEXT; ALTER TABLE messages ADD COLUMN preview_thumb BLOB;
ALTER TABLE messages ADD COLUMN preview_image BLOB;`)
	waMessage(d, "L1", 1, "see https://x.org", 0, M{"kind": "text", "subtype": "link", "preview_url": "https://x.org",
		"preview_title": "X", "preview_description": "All about x", "preview_thumb": testJPEG(t, 10, 10)})
	waMessage(d, "L2", 2, "x.org", 1, M{"kind": "text", "subtype": "link"})
	runBridge(t, a, path)
	eq(t, "L1", previewOf(a, "L1"), []any{"https://x.org", "X", "All about x", int64(10)})
	eq(t, "L2", previewOf(a, "L2"), []any(nil))
	eq(t, "kind", kindOf(a, "L1"), "text")

	// the high-quality picture, downloaded after the import, takes the small one's place
	db.Exec(d, "UPDATE messages SET preview_image = ? WHERE id = 'L1'", testJPEG(t, 40, 20))
	eq(t, "updated", runBridge(t, a, path)["preview"], 1)
	eq(t, "L1 large", previewOf(a, "L1"), []any{"https://x.org", "X", "All about x", int64(40)})
	eq(t, "pictures", a.Int("SELECT count(*) FROM link_preview_image"), int64(1))
	eq(t, "the same again", runBridge(t, a, path)["preview"], 0)
}
