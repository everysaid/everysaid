package importers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/telegramstore"
)

// Telegram's kinds, calls and files: a poll, a place, a round video, a missed call (a call of its
// own too), and a downloaded picture linked to its message.
func TestTelegramKindsCallsAndFiles(t *testing.T) {
	t.Cleanup(config.Load) // after the folder is put back
	t.Setenv("EVERYSAID_CACHE", t.TempDir())
	config.Load()
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

// TestTelegramChangesToMessagesThere: the live connection writes an edited message (and its
// reactions as they are now) over its row in telegram.db and imports it at once; a message already
// in the archive takes the new text (found by search under it, no longer under the old), marked
// edited, and its reactions follow what Telegram says now, also once they are taken back.
func TestTelegramChangesToMessagesThere(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "telegram.db")
	d := telegramDB(t, path)
	set := func(m M) { db.Exec(d, "UPDATE message SET json = ? WHERE chat_id = ? AND id = 11", js(m), tgMaria) }
	only := map[[2]int64]bool{{tgMaria, 11}: true}
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path}))
	reactions := func() []string {
		return db.Strs(a.Tx(), "SELECT coalesce(r.emoji, '') || ' ' || coalesce(ad.value, '') || ' ' || coalesce(r.outgoing, 0) "+
			"FROM reaction r JOIN message m ON m.id = r.message_id LEFT JOIN address ad ON ad.id = r.address_id "+
			"WHERE m.key = '11' ORDER BY 1")
	}
	edited := func() int64 { return a.Int("SELECT edited FROM message WHERE key = '11'") }

	set(M{"_": "Message", "id": 11, "message": "corrected", "from_id": M{"user_id": tgMaria}, "edit_date": 1_790_000_500,
		"reactions": M{"_": "MessageReactions", "results": []M{{"reaction": M{"_": "ReactionEmoji", "emoticon": "👍"}, "count": 1,
			"chosen_order": 0}}, "recent_reactions": []M{{"peer_id": M{"user_id": tgMe}, "reaction": M{"_": "ReactionEmoji",
			"emoticon": "👍"}, "my": true}}}})
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path, Only: only}))
	eq(t, "edited", edited(), int64(1))
	eq(t, "new text", msgRow(a, "11", "text")[0], "corrected")
	eq(t, "found by the new", searchKeys(a, "corrected"), []string{"11"})
	eq(t, "not by the old", len(searchKeys(a, "theirs")), 0)
	eq(t, "reactions", reactions(), []string{"👍  1"})
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path})) // again: nothing changes
	eq(t, "reactions again", reactions(), []string{"👍  1"})

	set(M{"_": "Message", "id": 11, "message": "corrected", "from_id": M{"user_id": tgMaria}, "edit_date": 1_790_000_500,
		"reactions": M{"_": "MessageReactions", "results": []M{}}})
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path, Only: only}))
	eq(t, "taken back", len(reactions()), 0)

	// an edit Telegram hides (a bot's buttons changed, a link preview fetched) is not the sender's edit
	db.Exec(d, "UPDATE message SET json = ? WHERE chat_id = ? AND id = 10", js(M{"_": "Message", "id": 10, "message": "mine 1",
		"out": true, "edit_date": 1_790_000_600, "edit_hide": true}), tgMaria)
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path}))
	eq(t, "hidden edit", a.Int("SELECT edited FROM message WHERE key = '10' AND outgoing"), int64(0))
}

// searchKeys are the keys of the messages the search tables find for a word, by words and by
// trigrams (each must give the same, for a word long enough to have trigrams).
func searchKeys(a *archive.Archive, word string) []string {
	words := db.Strs(a.Tx(), "SELECT m.key FROM message m WHERE m.id IN (SELECT rowid FROM message_fts "+
		"WHERE message_fts MATCH ?) ORDER BY m.key", word)
	tri := db.Strs(a.Tx(), "SELECT m.key FROM message m WHERE m.id IN (SELECT rowid FROM message_tri "+
		"WHERE message_tri MATCH ?) ORDER BY m.key", word)
	if len([]rune(word)) >= 3 && strings.Join(words, ",") != strings.Join(tri, ",") { // trigrams: 3 or more
		return append(words, "≠ trigrams: "+strings.Join(tri, ","))
	}
	return words
}
