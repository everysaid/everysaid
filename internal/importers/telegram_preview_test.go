package importers

import (
	"bytes"
	"image"
	"image/jpeg"
	"strings"
	"testing"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/telegramstore"
)

// Telegram's link previews: the words of media.webpage, the picture the live connection downloaded
// (of the preview's photo as it is now); none for a preview not made yet. A picture downloaded later,
// or a message imported before there were previews, is brought on a later import (counted as a
// change); once the store lets the message go, its picture goes too (the row stays: looked at).
func TestTelegramPreviews(t *testing.T) {
	t.Cleanup(config.Load)
	t.Setenv("EVERYSAID_CACHE", t.TempDir())
	t.Setenv("EVERYSAID_DATA", t.TempDir())
	config.Load()
	a, _ := newArchive(t)
	d := telegramDB(t, telegramstore.DB())
	var pic bytes.Buffer
	must(t, jpeg.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 40, 20)), nil))
	const photo = 5_000_000_000_000_000_001
	page := func(photoID int64) M {
		w := M{"_": "WebPage", "url": "https://example.com/a", "display_url": "example.com/a", "site_name": "Example",
			"title": "A title", "description": "Some words", "photo": nil}
		if photoID != 0 {
			w["photo"] = M{"_": "Photo", "id": photoID, "sizes": []M{}}
		}
		return M{"_": "MessageMediaWebPage", "webpage": w}
	}
	add := func(id int64, media M) {
		db.Exec(d, "INSERT INTO telegram_message (chat_id, id, date, json) VALUES (?, ?, ?, ?)", tgMaria, id, 1_790_000_100+id,
			js(M{"_": "Message", "id": id, "message": "see https://example.com/a", "from_id": M{"user_id": tgMaria}, "media": media}))
	}
	add(40, page(photo))
	add(41, page(0))
	add(42, M{"_": "MessageMediaWebPage", "webpage": M{"_": "WebPagePending", "id": 1, "date": 1}})
	add(43, page(photo+1))
	db.Exec(d, "INSERT INTO telegram_webpage_photo (chat_id, id, photo_id, data) VALUES (?, 40, ?, ?)", tgMaria, photo, pic.Bytes())
	// a picture of the photo the preview had before an edit: not this one's
	db.Exec(d, "INSERT INTO telegram_webpage_photo (chat_id, id, photo_id, data) VALUES (?, 43, ?, ?)", tgMaria, photo, pic.Bytes())

	must(t, Telegram(a, nil, TelegramOptions{}))
	previews := func() string {
		return strings.Join(db.Strs(a.Tx(), "SELECT m.key || ' ' || p.url || ' ' || p.site || ' ' || p.title || ' ' || p.description || ' ' || "+
			"(p.image IS NOT NULL) FROM link_preview p JOIN message m ON m.id = p.message_id ORDER BY m.key"), "\n")
	}
	eq(t, "previews", previews(), "40 https://example.com/a Example A title Some words 1\n"+
		"41 https://example.com/a Example A title Some words 0\n43 https://example.com/a Example A title Some words 0")
	eq(t, "still a link", msgRow(a, "40", "subtype")[0], "link")
	eq(t, "not a file", a.Int("SELECT count(*) FROM attachment"), int64(0))

	// its picture downloaded since; one imported before there were previews
	db.Exec(d, "UPDATE telegram_webpage_photo SET photo_id = ? WHERE id = 43", photo+1)
	a.Exec("DELETE FROM link_preview WHERE message_id = (SELECT id FROM message WHERE key = '41')")
	var lines []string
	must(t, Telegram(a, func(s string) { lines = append(lines, s) }, TelegramOptions{}))
	eq(t, "brought", strings.Contains(strings.Join(lines, "\n"), "preview 2"), true)
	eq(t, "pictures", a.Int("SELECT count(*) FROM link_preview WHERE image IS NOT NULL"), int64(2))
	lines = nil
	must(t, Telegram(a, func(s string) { lines = append(lines, s) }, TelegramOptions{}))
	eq(t, "again: nothing", strings.Contains(strings.Join(lines, "\n"), "preview"), false)

	// out of the store: its picture too, in the archive now
	must(t, TelegramPrune(a))
	eq(t, "let go", db.Int(d, "SELECT count(*) FROM telegram_webpage_photo WHERE data IS NOT NULL"), int64(0))
	eq(t, "looked at", db.Int(d, "SELECT count(*) FROM telegram_webpage_photo"), int64(2))
	eq(t, "kept", a.Int("SELECT count(*) FROM link_preview WHERE image IS NOT NULL"), int64(2))
}
