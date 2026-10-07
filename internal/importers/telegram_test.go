package importers

import (
	"os"
	"path/filepath"
	"testing"

	"everysaid/internal/db"
	"everysaid/internal/telegramstore"
)

// Telegram's kinds, calls and files: a poll, a place, a round video, a missed call (a call of its
// own too), and a downloaded picture linked to its message.
func TestTelegramKindsCallsAndFiles(t *testing.T) {
	a, _ := newArchive(t)
	d := telegramDB(t, telegramstore.DB())
	add := func(id int64, m M) {
		db.Exec(d, "INSERT INTO message (chat_id, id, date, json) VALUES (?, ?, ?, ?)", tgMaria, id, 1_790_000_100+id, js(m))
	}
	add(20, M{"_": "Message", "id": 20, "from_id": M{"user_id": tgMaria}, "media": M{"_": "MessageMediaPoll",
		"poll": M{"question": M{"_": "TextWithEntities", "text": "When?"}, "answers": []M{{"text": "Mon"}, {"text": M{"text": "Tue"}}}}}})
	add(21, M{"_": "Message", "id": 21, "out": true, "media": M{"_": "MessageMediaVenue", "geo": M{"lat": 37.97, "long": 23.73},
		"title": "Syntagma", "address": ""}})
	add(22, M{"_": "Message", "id": 22, "from_id": M{"user_id": tgMaria}, "media": M{"_": "MessageMediaDocument",
		"document": M{"mime_type": "video/mp4", "attributes": []M{{"_": "DocumentAttributeVideo", "round_message": true}}}}})
	add(23, M{"_": "MessageService", "id": 23, "out": true, "action": M{"_": "MessageActionPhoneCall", "call_id": 777,
		"reason": M{"_": "PhoneCallDiscardReasonMissed"}, "video": false}})
	add(24, M{"_": "Message", "id": 24, "out": true, "message": "look", "media": M{"_": "MessageMediaPhoto"}})
	db.Exec(d, "UPDATE message SET file = 'maria/24.jpg' WHERE id = 24")
	must(t, os.MkdirAll(filepath.Join(telegramstore.Media(), "maria"), 0o700))
	must(t, os.WriteFile(filepath.Join(telegramstore.Media(), "maria", "24.jpg"), []byte("jpeg"), 0o600))

	must(t, Telegram(a, nil, TelegramOptions{}))
	eq(t, "poll", msgRow(a, "20", "text, subtype"), []any{"When?\n- Mon\n- Tue", "poll"})
	eq(t, "place", msgRow(a, "21", "lat, lon, place"), []any{37.97, 23.73, "Syntagma"})
	eq(t, "video note", []any{kindOf(a, "22"), msgRow(a, "22", "subtype")[0]}, []any{"video", "video note"})
	var detail, key string
	var answered int64
	a.Row("SELECT detail, key, answered FROM call", nil, &detail, &key, &answered)
	eq(t, "call", []any{detail, key, answered}, []any{"unanswered", "777", int64(0)})

	s := NewStore(a)
	s.Root = t.TempDir()
	TelegramMedia(a, s)
	a.Commit()
	eq(t, "linked", a.Int("SELECT count(*) FROM attachment t JOIN message m ON m.id = t.message_id WHERE m.key = '24'"), int64(1))
	eq(t, "type", msgType(a), "image/jpeg")
}

func msgType(a interface {
	Row(string, []any, ...any) bool
}) string {
	var mime string
	a.Row("SELECT mime FROM media", nil, &mime)
	return mime
}
