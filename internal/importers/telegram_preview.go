package importers

import (
	"database/sql"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// telegramPreview is a message's link preview as Telegram made it (media.webpage), its picture the
// one the live connection downloaded (telegram_webpage_photo) for the preview's photo as it is now;
// nil for none (no link, or a preview Telegram has not made yet: WebPagePending, WebPageEmpty).
func telegramPreview(d *sql.DB, chatID, id int64, m map[string]any) *archive.Preview {
	media := obj(m["media"])
	if pyStr(media["_"]) != "MessageMediaWebPage" {
		return nil
	}
	w := obj(media["webpage"])
	if pyStr(w["_"]) != "WebPage" {
		return nil
	}
	p := &archive.Preview{URL: strOrEmpty(w["url"]), Site: strOrEmpty(w["site_name"]), Title: strOrEmpty(w["title"]),
		Description: strOrEmpty(w["description"])}
	if photo := obj(w["photo"]); pyStr(photo["_"]) == "Photo" && truthy(photo["id"]) {
		var data []byte
		if db.Row(d, "SELECT data FROM telegram_webpage_photo WHERE chat_id = ? AND id = ? AND photo_id = ? AND data IS NOT NULL",
			[]any{chatID, id, toInt(photo["id"])}, &data) {
			p.Image = data
		}
	}
	return p
}
