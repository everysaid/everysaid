package importers

import (
	"encoding/json"
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

// A group's members are those Telegram gave (chat_member) and whoever wrote there: a member who never
// wrote, is one; the bot, a deleted account and the owner are not. TelegramMembers (the live
// connection's way) brings a later list's newcomers; one who left stays a member.
func TestTelegramMembers(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "telegram.db")
	d := telegramDB(t, path)
	const carol, bot, gone, dan = 333, 444, 555, 666
	for _, e := range []M{{"_": "User", "id": carol, "first_name": "Carol", "username": "carol_c"},
		{"_": "User", "id": bot, "first_name": "Helper", "bot": true}, {"_": "User", "id": gone, "deleted": true},
		{"_": "User", "id": dan, "first_name": "Dan"}} {
		db.Exec(d, "INSERT INTO entity VALUES (?, ?)", e["id"], js(e))
	}
	for _, u := range []int64{tgMe, tgMaria, carol, bot, gone} {
		db.Exec(d, "INSERT INTO chat_member VALUES (?, ?)", tgGroup, u)
	}
	members := func() []string {
		return db.Strs(a.Tx(), "SELECT ad.value FROM conversation_member cm JOIN conversation c ON c.id = cm.conversation_id "+
			"JOIN address ad ON ad.id = cm.address_id WHERE c.key = ? ORDER BY ad.value", fmt.Sprint(tgGroup))
	}
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path}))
	// two members wrote (one is also listed); one is listed only
	eq(t, "members", members(), []string{"+15557770001", "222", "333"})
	eq(t, "Carol's username", a.Int("SELECT count(*) FROM address WHERE value = 'carol_c'"), int64(1))

	// a later list: one came, one left
	must(t, telegramstore.NoteMembers(d, tgGroup, []int64{tgMe, tgMaria, dan}, true, 3))
	must(t, TelegramMembers(a, nil, path, map[int64]bool{tgGroup: true}))
	eq(t, "later", members(), []string{"+15557770001", "222", "333", "666"})
	var name string
	a.Row("SELECT hn.name FROM handle_name hn JOIN address ad ON ad.id = hn.address_id WHERE ad.value = '666'", nil, &name)
	eq(t, "Dan's name", name, "Dan")
}

// Telegram's service messages and polls say what they are as notices: who added whom, who left, a
// new title, a pin (of the message it answers), a group call, a timer, and a poll with its results.
func TestTelegramNotices(t *testing.T) {
	t.Cleanup(config.Load)
	t.Setenv("EVERYSAID_CACHE", t.TempDir())
	config.Load()
	a, _ := newArchive(t)
	d := telegramDB(t, telegramstore.DB())
	add := func(id int64, m M) {
		db.Exec(d, "INSERT INTO message (chat_id, id, date, json) VALUES (?, ?, ?, ?)", tgGroup, id, 1_790_000_100+id, js(m))
	}
	svc := func(id, from int64, action M) {
		add(id, M{"_": "MessageService", "id": id, "from_id": M{"user_id": from}, "action": action})
	}
	svc(30, tgBob, M{"_": "MessageActionChatAddUser", "users": []int64{tgMaria}})
	svc(31, tgMaria, M{"_": "MessageActionChatDeleteUser", "user_id": tgMaria})
	svc(32, tgBob, M{"_": "MessageActionChatEditTitle", "title": "Friends!"})
	add(33, M{"_": "MessageService", "id": 33, "out": true, "action": M{"_": "MessageActionPinMessage"},
		"reply_to": M{"reply_to_msg_id": 2}})
	svc(34, tgBob, M{"_": "MessageActionGroupCall", "call": M{"id": 5}})
	svc(35, tgBob, M{"_": "MessageActionSetMessagesTTL", "period": 86400})
	svc(36, tgBob, M{"_": "MessageActionContactSignUp"})
	add(37, M{"_": "Message", "id": 37, "from_id": M{"user_id": tgBob}, "media": M{"_": "MessageMediaPoll",
		"poll":    M{"question": M{"text": "When?"}, "answers": []M{{"text": "Mon"}, {"text": "Tue"}}, "closed": true},
		"results": M{"total_voters": 3, "results": []M{{"voters": 1}, {"voters": 2}}}}})
	must(t, Telegram(a, nil, TelegramOptions{}))

	notice := func(key string) (string, map[string]any) {
		var code, args string
		a.Row("SELECT n.code, n.args FROM notice n JOIN message m ON m.id = n.message_id WHERE m.key = ?", []any{key}, &code, &args)
		var v map[string]any
		json.Unmarshal([]byte(args), &v)
		return code, v
	}
	action := func(key string) map[string]any {
		_, v := notice(key)
		acts, _ := v["actions"].([]any)
		if len(acts) != 1 {
			t.Fatalf("%s: %v", key, v)
		}
		return acts[0].(map[string]any)
	}
	eq(t, "added", action("30")["type"], "added")
	eq(t, "left", action("31")["type"], "left")
	eq(t, "title", action("32")["title"], "Friends!")
	code, v := notice("33")
	eq(t, "pin", []any{code, v["by"]}, []any{"pin", map[string]any{"self": true}})
	eq(t, "pinned", a.Int("SELECT count(*) FROM message p JOIN message m ON m.id = p.reply_to WHERE p.key = '33' AND m.key = '2'"), int64(1))
	code, _ = notice("34")
	eq(t, "group call", code, "group_call")
	code, v = notice("35")
	eq(t, "timer", []any{code, v["seconds"]}, []any{"timer", float64(86400)})
	code, _ = notice("36")
	eq(t, "signed up", code, "signed_up")
	code, v = notice("37")
	opts, _ := v["options"].([]any)
	eq(t, "poll", []any{code, len(opts), opts[1].(map[string]any)["votes"], v["ended"], v["voters"]},
		[]any{"poll", 2, float64(2), true, float64(3)})
}

// What the official apps show that was lost: a conference call (a call), a reply to another chat's
// message (what it quotes), a forum topic's message (no reply to the topic's start), dice and a to-do
// list as text, a bot's own service text, a story (and a reply to one), content of a newer Telegram,
// and an edit Telegram hides that still moves a live location.
func TestTelegramWhatTheAppsShow(t *testing.T) {
	t.Cleanup(config.Load)
	t.Setenv("EVERYSAID_CACHE", t.TempDir())
	config.Load()
	a, _ := newArchive(t)
	d := telegramDB(t, telegramstore.DB())
	add := func(id int64, m M) {
		m["id"] = id
		if m["from_id"] == nil && m["out"] == nil {
			m["from_id"] = M{"user_id": tgMaria}
		}
		db.Exec(d, "INSERT INTO message (chat_id, id, date, json) VALUES (?, ?, ?, ?)", tgMaria, id, 1_790_000_100+id, js(m))
	}
	add(40, M{"_": "MessageService", "action": M{"_": "MessageActionConferenceCall", "call_id": 9001, "missed": true}})
	add(41, M{"_": "Message", "message": "this", "reply_to": M{"reply_to_msg_id": 5, "reply_to_peer_id": M{"channel_id": 1},
		"quote_text": "the quote"}})
	add(42, M{"_": "Message", "message": "in a topic", "reply_to": M{"reply_to_msg_id": 2, "forum_topic": true}})
	add(43, M{"_": "Message", "media": M{"_": "MessageMediaDice", "emoticon": "🎲", "value": 4}})
	add(44, M{"_": "Message", "media": M{"_": "MessageMediaToDo", "todo": M{"title": M{"text": "Trip"},
		"list": []M{{"title": M{"text": "tickets"}}, {"title": M{"text": "hotel"}}}}}})
	add(45, M{"_": "MessageService", "action": M{"_": "MessageActionCustomAction", "message": "Score: 10"}})
	add(46, M{"_": "Message", "media": M{"_": "MessageMediaStory", "via_mention": true}})
	add(47, M{"_": "Message", "message": "nice", "reply_to": M{"_": "MessageReplyStoryHeader", "story_id": 3}})
	add(48, M{"_": "Message", "media": M{"_": "MessageMediaUnsupported"}})
	add(49, M{"_": "Message", "media": M{"_": "MessageMediaGeoLive", "geo": M{"lat": 1.0, "long": 2.0}, "period": 900}})
	must(t, Telegram(a, nil, TelegramOptions{}))

	var detail, key string
	a.Row("SELECT detail, key FROM call", nil, &detail, &key)
	eq(t, "conference call", []any{detail, key}, []any{"missed", "9001"})
	eq(t, "other chat's reply", msgRow(a, "41", "reply_text, reply_to"), []any{"the quote", nil})
	eq(t, "topic", msgRow(a, "42", "reply_to")[0], nil)
	eq(t, "dice", msgRow(a, "43", "text")[0], "🎲 4")
	eq(t, "to-do", msgRow(a, "44", "text")[0], "Trip\n- tickets\n- hotel")
	eq(t, "bot's text", msgRow(a, "45", "text")[0], "Score: 10")
	code := func(key string) string {
		return db.Str(a.Tx(), "SELECT coalesce(n.code, '') FROM message m LEFT JOIN notice n ON n.message_id = m.id WHERE m.key = ?", key)
	}
	eq(t, "notices", []string{code("46"), code("47"), code("48")}, []string{"story", "story_reply", "unsupported"})

	// the location moves (an edit Telegram hides): the new place, not marked edited
	db.Exec(d, "UPDATE message SET json = ? WHERE chat_id = ? AND id = 49", js(M{"_": "Message", "id": 49,
		"from_id": M{"user_id": tgMaria}, "media": M{"_": "MessageMediaGeoLive", "geo": M{"lat": 3.0, "long": 4.0}, "period": 900},
		"edit_date": 1_790_000_900, "edit_hide": true}), tgMaria)
	must(t, Telegram(a, nil, TelegramOptions{Only: map[[2]int64]bool{{tgMaria, 49}: true}}))
	eq(t, "moved", msgRow(a, "49", "lat, lon, edited"), []any{3.0, 4.0, int64(0)})

	// a conference call recorded while it went on: how it went once it ended
	add(50, M{"_": "MessageService", "action": M{"_": "MessageActionConferenceCall", "call_id": 9002}})
	must(t, Telegram(a, nil, TelegramOptions{}))
	db.Exec(d, "UPDATE message SET json = ? WHERE chat_id = ? AND id = 50", js(M{"_": "MessageService", "id": 50,
		"from_id": M{"user_id": tgMaria}, "action": M{"_": "MessageActionConferenceCall", "call_id": 9002, "duration": 120},
		"edit_date": 1_790_001_000}), tgMaria)
	must(t, Telegram(a, nil, TelegramOptions{Only: map[[2]int64]bool{{tgMaria, 50}: true}}))
	var answered, duration int64
	a.Row("SELECT answered, duration FROM call WHERE key = '9002'", nil, &answered, &duration)
	eq(t, "ended", []any{answered, duration}, []any{int64(1), int64(120)})
}
