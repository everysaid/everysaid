package importers

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"howett.net/plist"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// richLink is a URL balloon's payload_data as the iPhone writes it: an NSKeyedArchiver RichLink whose
// LPLinkMetadata has the link, title, summary, site, and its icon and image as payload files 0 and 1.
func richLink(t *testing.T, withImage bool) []byte {
	url := func(s string, cls int) map[string]any {
		return map[string]any{"NS.base": plist.UID(0), "NS.relative": s, "$class": plist.UID(cls)}
	}
	objects := []any{"$null",
		map[string]any{"richLinkIsPlaceholder": false, "richLinkMetadata": plist.UID(2), "$class": plist.UID(11)}, // 1
		map[string]any{"originalURL": plist.UID(3), "URL": plist.UID(4), "title": plist.UID(6), "summary": plist.UID(7),
			"siteName": plist.UID(8), "icon": plist.UID(9), "$class": plist.UID(12), "version": uint64(1)}, // 2
		url("https://example.org/a", 5),                                               // 3
		url("https://www.example.org/a?resolved=1", 5),                                // 4
		map[string]any{"$classname": "NSURL", "$classes": []any{"NSURL", "NSObject"}}, // 5
		"A page",       // 6
		"About it",     // 7
		"Example Site", // 8
		map[string]any{"richLinkImageAttachmentSubstituteIndex": uint64(0), "MIMEType": plist.UID(13), "$class": plist.UID(10)}, // 9
		map[string]any{"$classname": "RichLinkImageAttachmentSubstitute", "$classes": []any{"RichLinkImageAttachmentSubstitute", "NSObject"}},
		map[string]any{"$classname": "RichLink", "$classes": []any{"RichLink", "NSObject"}},
		map[string]any{"$classname": "LPLinkMetadata", "$classes": []any{"LPLinkMetadata", "NSObject"}},
		"image/png", // 13
	}
	if withImage {
		objects = append(objects, map[string]any{"richLinkImageAttachmentSubstituteIndex": uint64(1), "MIMEType": plist.UID(13),
			"$class": plist.UID(10)})
		objects[2].(map[string]any)["image"] = plist.UID(len(objects) - 1)
	}
	b, err := plist.Marshal(map[string]any{"$version": uint64(100000), "$archiver": "NSKeyedArchiver",
		"$top": map[string]any{"root": plist.UID(1)}, "$objects": objects}, plist.BinaryFormat)
	must(t, err)
	return b
}

func TestIMessagePreviewPayload(t *testing.T) {
	l, ok := imessagePreview(richLink(t, true))
	want := archive.Preview{URL: "https://example.org/a", Site: "Example Site", Title: "A page", Description: "About it"}
	if !ok || l.index != 1 || l.preview.URL != want.URL || l.preview.Site != want.Site || l.preview.Title != want.Title ||
		l.preview.Description != want.Description {
		t.Fatalf("%+v %v", l, ok)
	}
	if l, ok := imessagePreview(richLink(t, false)); !ok || l.index != -1 {
		t.Fatalf("without an image: %+v", l)
	}
	for _, bad := range [][]byte{nil, []byte("not a plist")} {
		if _, ok := imessagePreview(bad); ok {
			t.Fatalf("%q read", bad)
		}
	}
}

// A URL balloon's preview, its picture the payload file the sync copied: kept on a new message and
// brought to one the archive had without it; its payload files are never files of the message.
func TestIMessagePreviews(t *testing.T) {
	a, _ := newArchive(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "sms.db")
	d := sqliteAt(t, path, `
CREATE TABLE handle (ROWID INTEGER PRIMARY KEY, id TEXT);
CREATE TABLE chat (ROWID INTEGER PRIMARY KEY, guid TEXT, style INTEGER, display_name TEXT);
CREATE TABLE chat_message_join (chat_id INTEGER, message_id INTEGER);
CREATE TABLE chat_handle_join (chat_id INTEGER, handle_id INTEGER);
CREATE TABLE attachment (ROWID INTEGER PRIMARY KEY, filename TEXT, mime_type TEXT);
CREATE TABLE message_attachment_join (message_id INTEGER, attachment_id INTEGER);
CREATE TABLE message (ROWID INTEGER PRIMARY KEY, guid TEXT, date INTEGER, is_from_me INTEGER, service TEXT,
    text TEXT, attributedBody BLOB, handle_id INTEGER, cache_has_attachments INTEGER,
    associated_message_type INTEGER, associated_message_guid TEXT, item_type INTEGER,
    balloon_bundle_id TEXT, payload_data BLOB);`)
	db.Exec(d, "INSERT INTO handle VALUES (1, '+15557770001')")
	msg := "INSERT INTO message VALUES (?, ?, ?, 0, 'iMessage', ?, NULL, 1, 1, 0, NULL, 0, ?, ?)"
	db.Exec(d, msg, 1, "G1", int64(800_000_000)*1_000_000_000, "https://example.org/a", urlBalloon, richLink(t, true))
	db.Exec(d, msg, 2, "G2", int64(800_000_100)*1_000_000_000, "https://example.org/b", urlBalloon, richLink(t, false))
	files := []string{"ab/01/X/icon.pluginPayloadAttachment", "cd/02/Y/image.pluginPayloadAttachment",
		"ef/03/Z/icon2.pluginPayloadAttachment"}
	for i, f := range files {
		db.Exec(d, "INSERT INTO attachment VALUES (?, ?, NULL)", i+1, "~/Library/SMS/Attachments/"+f)
		db.Exec(d, "INSERT INTO message_attachment_join VALUES (?, ?)", 1+i/2, i+1)
	}
	var pic bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 6, 4))
	img.Set(2, 2, color.RGBA{10, 200, 10, 255})
	must(t, png.Encode(&pic, img))
	media := filepath.Join(dir, "sms-media")
	for _, f := range files[:2] {
		os.MkdirAll(filepath.Join(media, filepath.Dir(f)), 0o700)
		os.WriteFile(filepath.Join(media, f), pic.Bytes(), 0o600)
	}

	if got := IMessagePreviewFiles(d); !slices.Equal(got, []string{files[1]}) {
		t.Fatalf("the sync copies %v", got)
	}
	for _, f := range files {
		if SMSAttachment("~/Library/SMS/Attachments/"+f) != "" {
			t.Fatalf("%s a file of the message", f)
		}
	}
	must(t, SMS(a, nil, SMSOptions{IphoneDB: path, Exports: []archive.AndroidExport{}}))
	type card struct {
		URL, Site, Title, Desc string
		Image                  bool
	}
	cardOf := func(key string) card {
		var u, s, ti, de, im *string
		a.Row("SELECT p.url, p.site, p.title, p.description, p.image FROM link_preview p JOIN message m ON m.id = p.message_id "+
			"WHERE m.key = ?", []any{key}, &u, &s, &ti, &de, &im)
		v := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		return card{v(u), v(s), v(ti), v(de), im != nil}
	}
	eq(t, "with its picture", cardOf("G1"), card{"https://example.org/a", "Example Site", "A page", "About it", true})
	eq(t, "without", cardOf("G2"), card{"https://example.org/a", "Example Site", "A page", "About it", false})
	eq(t, "a text, not a file", db.Str(a.DB, "SELECT mk.name FROM message m JOIN message_kind mk ON mk.id = m.kind_id WHERE m.key = 'G1'"), "text")
	eq(t, "no files", a.Int("SELECT count(*) FROM attachment"), int64(0))

	// imported before previews were kept: brought in at the next import, the picture once
	a.Exec("DELETE FROM link_preview")
	a.Commit()
	var lines []string
	must(t, SMS(a, func(s string) { lines = append(lines, s) }, SMSOptions{IphoneDB: path, Exports: []archive.AndroidExport{}}))
	eq(t, "brought back", cardOf("G1").Image, true)
	eq(t, "said", slices.Contains(lines, "link previews on messages already here: 2"), true)
	eq(t, "one picture", a.Int("SELECT count(*) FROM link_preview_image"), int64(1))
	lines = nil
	must(t, SMS(a, func(s string) { lines = append(lines, s) }, SMSOptions{IphoneDB: path, Exports: []archive.AndroidExport{}}))
	eq(t, "nothing again", slices.ContainsFunc(lines, func(s string) bool { return bytes.Contains([]byte(s), []byte("previews")) }), false)
}
