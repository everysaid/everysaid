package importers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/telegramstore"
)

// photoRows are a table's photos, "<sha or none> <first>-<last> <removed or ->" by time.
func photoRows(a *archive.Archive, table string) string {
	var got []string
	a.Each("SELECT coalesce(sha256, 'none'), first_seen, last_seen, coalesce(removed_at, '-') FROM "+table+
		" ORDER BY first_seen, id", nil, func(scan func(...any)) {
		var sha, removed string
		var first, last int64
		scan(&sha, &first, &last, &removed)
		got = append(got, fmt.Sprintf("%s %d-%d %s", sha, first, last, removed))
	})
	return strings.Join(got, "|")
}

// A group's photo as its notices tell: a new one with its file (the message's), then removed.
func TestTelegramGroupPhotos(t *testing.T) {
	t.Cleanup(config.Load)
	t.Setenv("EVERYSAID_CACHE", t.TempDir())
	t.Setenv("EVERYSAID_DATA", t.TempDir())
	config.Load()
	a, _ := newArchive(t)
	d := telegramDB(t, telegramstore.DB())
	add := func(id int64, action M) {
		db.Exec(d, "INSERT INTO telegram_message (chat_id, id, date, json) VALUES (?, ?, ?, ?)", tgGroup, id, 1_790_000_000+id,
			js(M{"_": "MessageService", "id": id, "from_id": M{"user_id": tgBob}, "action": action}))
	}
	add(50, M{"_": "MessageActionChatEditPhoto", "photo": M{"_": "Photo", "id": 9}})
	add(51, M{"_": "MessageActionChatDeletePhoto"})
	chat := fmt.Sprint(tgGroup)
	must(t, os.MkdirAll(filepath.Join(telegramstore.Media(), chat), 0o700))
	must(t, os.WriteFile(filepath.Join(telegramstore.Media(), chat, "50.jpg"), []byte("\xff\xd8\xff\xe0jpeg"), 0o600))
	must(t, Telegram(a, nil, TelegramOptions{}))
	must(t, Media(a, nil, TelegramMedia))
	sha := db.Str(a.Tx(), "SELECT sha256 FROM attachment")
	want := sha + " 1790000050-1790000050 1790000051|none 1790000051-1790000051 -"
	eq(t, "chat photos", photoRows(a, "chat_photo"), want)
	must(t, Media(a, nil, TelegramMedia)) // again: nothing new
	eq(t, "once", photoRows(a, "chat_photo"), want)
}
