// Mentions, receipts and how far chats were read,
// as the Telegram and Viber importers bring them, on small databases made here (shaped as
// telegram.db and the iPhone's viber.sqlite).
package importers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/telegramstore"
)

const (
	tgMe, tgMaria, tgBob, tgGroup = 999, 111, 222, -5
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

type M = map[string]any

func newArchive(t *testing.T) (*archive.Archive, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := archive.Open(path)
	must(t, err)
	t.Cleanup(func() { a.Close() })
	return a, path
}

func telegramDB(t *testing.T, path string) *sql.DB {
	d, err := telegramstore.Open(path)
	must(t, err)
	t.Cleanup(func() { d.Close() })
	for _, e := range []struct {
		id   int64
		user M
	}{
		{tgMe, M{"_": "User", "id": tgMe, "is_self": true, "phone": "15550000000"}},
		{tgMaria, M{"_": "User", "id": tgMaria, "first_name": "Maria", "username": "Maria_K", "phone": "15557770001"}},
		{tgBob, M{"_": "User", "id": tgBob, "first_name": "Bob"}},
	} {
		db.Exec(d, "INSERT INTO entity VALUES (?, ?)", e.id, js(e.user))
	}
	db.Exec(d, "INSERT INTO chat VALUES (?, 'group', 'Friends', 0, '{}', 1)", tgGroup)
	db.Exec(d, "INSERT INTO chat VALUES (?, 'user', 'Maria', 0, ?, 1)", tgMaria,
		js(M{"_": "User", "id": tgMaria, "username": "Maria_K", "phone": "15557770001"}))
	text := "hi 😀 @maria_k and Bob" // the emoji is two UTF-16 units: the offsets count them so
	msgs := []struct {
		chat, id int64
		m        M
	}{
		{tgGroup, 1, M{"_": "Message", "id": 1, "message": text, "from_id": M{"user_id": tgBob},
			"entities": []M{{"_": "MessageEntityMention", "offset": 6, "length": 8},
				{"_": "MessageEntityMentionName", "offset": 19, "length": 3, "user_id": tgBob},
				{"_": "MessageEntityBold", "offset": 0, "length": 2}}}},
		{tgGroup, 2, M{"_": "Message", "id": 2, "message": "from Maria", "from_id": M{"user_id": tgMaria}}},
		{tgGroup, 3, M{"_": "Message", "id": 3, "message": "anonymous admin", "from_id": M{"channel_id": 77}}},
		{tgMaria, 10, M{"_": "Message", "id": 10, "message": "mine 1", "out": true}},
		{tgMaria, 11, M{"_": "Message", "id": 11, "message": "theirs", "from_id": M{"user_id": tgMaria}}},
		{tgMaria, 12, M{"_": "Message", "id": 12, "message": "mine 2", "out": true}},
		{tgMaria, 13, M{"_": "Message", "id": 13, "message": "mine 3", "out": true}},
	}
	for _, m := range msgs {
		db.Exec(d, "INSERT INTO message (chat_id, id, date, json) VALUES (?, ?, ?, ?)", m.chat, m.id, 1_790_000_000+m.id, js(m.m))
	}
	return d
}

func p64(n int64) *int64 { return &n }

func pairs(rows []map[string]any, a, b string) [][2]any {
	var out [][2]any
	for _, r := range rows {
		out = append(out, [2]any{r[a], r[b]})
	}
	return out
}

func TestTelegramMentionsMembersAndReads(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "telegram.db")
	d := telegramDB(t, path)
	_, err := telegramstore.NoteRead(d, tgMaria, p64(11), p64(10), false)
	must(t, err)
	a.Exec("INSERT INTO plugin_instance (plugin, kind, label, created_at) VALUES ('telegram', 'source', 'T', 0)")
	iid := a.Int("SELECT max(id) FROM plugin_instance")
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path}))
	a.Exec("UPDATE source SET instance_id = ? WHERE name = 'telegram'", iid)
	must(t, Telegram(a, nil, TelegramOptions{DBPath: path})) // again, now with an instance: the reads are reported
	a.Commit()

	group := a.Int("SELECT c.id FROM conversation c JOIN service s ON s.id = c.service_id WHERE s.name = 'telegram' AND c.key = ?",
		fmt.Sprint(tgGroup))
	named := pairs(db.Maps(a.Tx(), "SELECT a.value, n.token FROM mention n JOIN address a ON a.id = n.address_id "+
		"JOIN message m ON m.id = n.message_id WHERE m.conversation_id = ? ORDER BY n.token", group), "value", "token")
	if want := [][2]any{{"+15557770001", "@maria_k"}, {fmt.Sprint(tgBob), "Bob"}}; !reflect.DeepEqual(named, want) {
		t.Fatalf("mentions %v", named)
	}
	members := db.Strs(a.Tx(), "SELECT a.value FROM conversation_member cm JOIN address a ON a.id = cm.address_id "+
		"WHERE cm.conversation_id = ? ORDER BY a.value", group)
	if want := []string{"+15557770001", fmt.Sprint(tgBob)}; !reflect.DeepEqual(members, want) { // whoever wrote there; not an anonymous admin
		t.Fatalf("members %v", members)
	}
	chat := a.Int("SELECT c.id FROM conversation c JOIN service s ON s.id = c.service_id WHERE s.name = 'telegram' AND c.key = ?",
		fmt.Sprint(tgMaria))
	got := pairs(db.Maps(a.Tx(), "SELECT m.key, r.read_at FROM receipt r JOIN message m ON m.id = r.message_id "+
		"WHERE m.conversation_id = ? ORDER BY m.key", chat), "key", "read_at")
	if want := [][2]any{{"10", int64(0)}}; !reflect.DeepEqual(got, want) { // read, when not known
		t.Fatalf("receipts %v", got)
	}
	if read := a.Int("SELECT value FROM state_report WHERE conversation_id = ? AND field = 'read_until'", chat); read != (1_790_000_000+11)*1000 {
		t.Fatalf("read_until %d", read)
	}

	// seen live: the other read up to 13 now; 10 keeps its "known, not when"
	_, err = telegramstore.NoteRead(d, tgMaria, nil, p64(13), true)
	must(t, err)
	must(t, TelegramReads(a, nil, path, map[int64]bool{tgMaria: true}))
	a.Commit()
	readAt := map[string]int64{}
	for _, r := range db.Maps(a.Tx(), "SELECT m.key, r.read_at FROM receipt r JOIN message m ON m.id = r.message_id "+
		"WHERE m.conversation_id = ?", chat) {
		readAt[r["key"].(string)] = r["read_at"].(int64)
	}
	if _, theirs := readAt["11"]; readAt["10"] != 0 || readAt["12"] <= 0 || readAt["12"] != readAt["13"] || theirs {
		t.Fatalf("receipts after %v", readAt)
	}
}

func viberDB(t *testing.T, path string) {
	d, err := db.Open(path)
	must(t, err)
	defer d.Close()
	db.Exec(d, `
    CREATE TABLE ZMEMBER (Z_PK INTEGER PRIMARY KEY, ZMEMBERID TEXT);
    CREATE TABLE ZPHONENUMBER (ZMEMBER INTEGER, ZCANONIZEDPHONENUM TEXT, ZPHONE TEXT);
    CREATE TABLE Z_5PHONENUMINDEXES (Z_5CONVERSATIONS INTEGER, Z_10PHONENUMINDEXES INTEGER);
    CREATE TABLE ZCONVERSATION (Z_PK INTEGER PRIMARY KEY, ZGROUPID INTEGER, ZNAME TEXT, ZSUBTYPE INTEGER,
        ZLASTREADTOKEN INTEGER, ZSEENSTATUSLASTTOKEN INTEGER);
    CREATE TABLE ZATTACHMENT (Z_PK INTEGER PRIMARY KEY, ZTYPE TEXT);
    CREATE TABLE ZVIBERLOCATION (Z_PK INTEGER PRIMARY KEY, ZLATITUDE REAL, ZLONGITUDE REAL, ZADDRESS TEXT);
    CREATE TABLE ZVIBERMESSAGE (Z_PK INTEGER PRIMARY KEY, ZSTATE TEXT, ZSYSTEMTYPE TEXT, ZATTACHMENT INTEGER,
        ZCONVERSATION INTEGER, ZDATE REAL, ZTOKEN INTEGER, ZPHONENUMINDEX INTEGER, ZTEXT TEXT, ZMETADATA TEXT,
        ZCLIENTMETADATA TEXT, ZCALLTYPE INTEGER, ZLIKESCOUNT INTEGER, ZLOCATION INTEGER, ZCALLSCOUNT INTEGER,
        ZFORWARDTYPE INTEGER, ZLIKESTYPE INTEGER);
    INSERT INTO ZMEMBER VALUES (1, 'mid-anna'), (2, 'mid-nick');
    INSERT INTO ZPHONENUMBER VALUES (1, '+15558880001', NULL), (2, '+15558880002', NULL);
    INSERT INTO Z_5PHONENUMINDEXES VALUES (10, 1), (20, 1), (20, 2);
    INSERT INTO ZCONVERSATION VALUES (10, NULL, NULL, 0, 1002, 1003), (20, 4242, 'Team', 0, NULL, NULL);`)
	date := 1_790_000_000 - archive.AppleEpoch
	mention := js(M{"textMetaInfo": []M{{"type": 0, "memberId": "mid-nick", "start": 4, "end": 11}}})
	rows := []struct {
		token  int64
		state  string
		conv   int64
		sender any
		text   string
		md     any
	}{
		{1001, "delivered", 10, nil, "one", nil}, {1002, "received", 10, 1, "two", nil},
		{1003, "delivered", 10, nil, "three", nil}, {1004, "delivered", 10, nil, "four", nil},
		{2001, "received", 20, 1, "hey \u202a@Nick\u202c, look", mention},
	}
	for i, r := range rows {
		db.Exec(d, "INSERT INTO ZVIBERMESSAGE (Z_PK, ZSTATE, ZSYSTEMTYPE, ZCONVERSATION, ZDATE, ZTOKEN, ZPHONENUMINDEX, "+
			"ZTEXT, ZMETADATA, ZLIKESCOUNT) VALUES (?, ?, '', ?, ?, ?, ?, ?, ?, 0)",
			i+1, r.state, r.conv, date+i, r.token, r.sender, r.text, r.md)
	}
}

func TestViberMentionsSeenAndRead(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "viber.sqlite")
	viberDB(t, path)
	a.Exec("INSERT INTO plugin_instance (plugin, kind, label, created_at) VALUES ('iphone-backup', 'source', 'P', 0)")
	iid := a.Int("SELECT max(id) FROM plugin_instance")
	a.Exec("INSERT INTO source (name, path, instance_id) VALUES (?, ?, ?)", archive.Iphone()+"/viber", path, iid)
	must(t, Viber(a, nil, ViberOptions{IphoneDB: path, NoDesktop: true}))
	named := pairs(db.Maps(a.Tx(), "SELECT a.value, n.token FROM mention n JOIN address a ON a.id = n.address_id "+
		"JOIN message m ON m.id = n.message_id WHERE m.key = '2001'"), "value", "token")
	if want := [][2]any{{"+15558880002", "\u202a@Nick\u202c"}}; !reflect.DeepEqual(named, want) {
		t.Fatalf("mentions %q", named)
	}
	var seen [][4]any
	for _, r := range db.Maps(a.Tx(), "SELECT m.key, a.value, r.read_at, r.delivered_at FROM receipt r JOIN message m ON m.id = r.message_id "+
		"JOIN address a ON a.id = r.address_id WHERE m.key LIKE '100%' ORDER BY m.key") {
		seen = append(seen, [4]any{r["key"], r["value"], r["read_at"], r["delivered_at"]})
	}
	want := [][4]any{{"1001", "+15558880001", int64(0), nil}, {"1003", "+15558880001", int64(0), nil}} // not 1004, not theirs
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("seen %v", seen)
	}
	conv := a.Int("SELECT conversation_id FROM message WHERE key = '1001'")
	if read := a.Int("SELECT value FROM state_report WHERE conversation_id = ? AND field = 'read_until'", conv); read != (1_790_000_000+1)*1000 {
		t.Fatalf("read_until %d", read)
	}
}

// Viber counts where a mention is in UTF-16 units: an emoji before it is two.
func TestViberMentionAfterAnEmoji(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "viber.sqlite")
	viberDB(t, path)
	d, err := db.Open(path)
	must(t, err)
	db.Exec(d, "UPDATE ZVIBERMESSAGE SET ZTEXT = ?, ZMETADATA = ? WHERE ZTOKEN = 2001", "😀 \u202a@Nick\u202c hi",
		js(M{"textMetaInfo": []M{{"type": 0, "memberId": "mid-nick", "start": 3, "end": 10}, {"type": 0, "memberId": "mid-nick"}}}))
	d.Close()
	must(t, Viber(a, nil, ViberOptions{IphoneDB: path, NoDesktop: true}))
	var token string
	a.Row("SELECT n.token FROM mention n JOIN message m ON m.id = n.message_id WHERE m.key = '2001'", nil, &token)
	if token != "\u202a@Nick\u202c" {
		t.Fatalf("token %q", token)
	}
}

// TestViberEditEvents: on the iPhone an edit is a row of its own (systemInvalidMessage, its
// metadata naming the token edited) carrying the new text. As Viber shows it, the message edited
// takes the new text, marked edited, and the edit is no line of its own; also when the edit comes
// in a later import than the message.
func TestViberEditEvents(t *testing.T) {
	for _, later := range []bool{false, true} {
		a, _ := newArchive(t)
		path := filepath.Join(t.TempDir(), "viber.sqlite")
		viberDB(t, path)
		opt := ViberOptions{IphoneDB: path, NoDesktop: true}
		if later {
			must(t, Viber(a, nil, opt))
		}
		d, err := db.Open(path)
		must(t, err)
		db.Exec(d, "INSERT INTO ZVIBERMESSAGE (Z_PK, ZSTATE, ZSYSTEMTYPE, ZCONVERSATION, ZDATE, ZTOKEN, ZPHONENUMINDEX, "+
			"ZTEXT, ZMETADATA, ZLIKESCOUNT) VALUES (6, 'received', 'systemInvalidMessage', 10, ?, 1006, 1, 'two, fixed', ?, 0)",
			1_790_000_100-archive.AppleEpoch, js(M{"edit": M{"token": 1002}}))
		d.Close()
		must(t, Viber(a, nil, opt))
		must(t, Viber(a, nil, opt)) // again: nothing changes
		eq(t, "messages", a.Int("SELECT count(*) FROM message"), int64(5))
		eq(t, "edited", msgRow(a, "1002", "text, edited"), []any{"two, fixed", int64(1)})
		eq(t, "found by the new", searchKeys(a, "fixed"), []string{"1002"})
	}
}

// Viber's pins and polls say what they are as notices: a pin of the message it is about, an unpin,
// and a poll with its counts and how many voted (each vote a row of its own, not a message).
func TestViberNotices(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "viber.sqlite")
	viberDB(t, path)
	d, err := db.Open(path)
	must(t, err)
	date := 1_790_000_200 - archive.AppleEpoch
	ins := func(pk, token int64, state, system string, sender any, text string, md, cm any) {
		db.Exec(d, "INSERT INTO ZVIBERMESSAGE (Z_PK, ZSTATE, ZSYSTEMTYPE, ZCONVERSATION, ZDATE, ZTOKEN, ZPHONENUMINDEX, "+
			"ZTEXT, ZMETADATA, ZCLIENTMETADATA, ZLIKESCOUNT) VALUES (?, ?, ?, 20, ?, ?, ?, ?, ?, ?, 0)",
			pk, state, system, date+int(pk), token, sender, text, md, cm)
	}
	ins(10, 3001, "received", "systemPinnedMessageCreated", 1, "(paperclip) hey", js(M{"pin": M{"action": "create", "token": 2001}}), nil)
	ins(11, 3002, "delivered", "systemPinnedMessageDeleted", nil, "", js(M{"pin": M{"action": "delete", "token": 2001}}), nil)
	ins(12, 3003, "received", "poll", 1, "When?", js(M{"poll": M{"multiple": false}}),
		js(M{"Poll": []M{{"title": "Mon", "count": 1}, {"title": "Tue", "count": 1}}}))
	ins(13, 3004, "received", "pollMessageInvisible", 1, "Mon", js(M{"poll": M{"parentToken": 3003}}), nil)
	ins(14, 3005, "delivered", "pollMessageInvisible", nil, "Tue", js(M{"poll": M{"parentToken": 3003}}), nil)
	d.Close()
	must(t, Viber(a, nil, ViberOptions{IphoneDB: path, NoDesktop: true}))

	notice := func(key string) (string, map[string]any) {
		var code, args string
		a.Row("SELECT n.code, n.args FROM notice n JOIN message m ON m.id = n.message_id WHERE m.key = ?", []any{key}, &code, &args)
		var v map[string]any
		json.Unmarshal([]byte(args), &v)
		return code, v
	}
	code, v := notice("3001")
	eq(t, "pin", []any{code, v["by"] != nil}, []any{"pin", true})
	eq(t, "pinned", a.Int("SELECT count(*) FROM message p JOIN message m ON m.id = p.reply_to WHERE p.key = '3001' AND m.key = '2001'"), int64(1))
	code, v = notice("3002")
	eq(t, "unpin", []any{code, v["by"]}, []any{"unpin", map[string]any{"self": true}})
	code, v = notice("3003")
	opts, _ := v["options"].([]any)
	eq(t, "poll", []any{code, len(opts), v["voters"]}, []any{"poll", 2, float64(2)}) // single choice: one each
	eq(t, "no votes as messages", a.Int("SELECT count(*) FROM message WHERE key IN ('3004', '3005')"), int64(0))
}

// The iPhone's ZLIKE says who gave each reaction in a group: kept by person (the owner's as theirs),
// the rest of the counts no one's; a message imported before with bare counts takes them.
func TestViberReactionsByWhom(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "viber.sqlite")
	viberDB(t, path)
	d, err := db.Open(path)
	must(t, err)
	db.Exec(d, "INSERT INTO ZVIBERMESSAGE (Z_PK, ZSTATE, ZCONVERSATION, ZDATE, ZTOKEN, ZPHONENUMINDEX, ZTEXT, ZCLIENTMETADATA, ZLIKESCOUNT) "+
		"VALUES (30, 'received', 20, ?, 5001, 1, 'party', ?, 0)", 1_790_000_200-archive.AppleEpoch,
		js(M{"Reactions": M{"reactions": M{"1": 3}}}))
	d.Close()
	must(t, Viber(a, nil, ViberOptions{IphoneDB: path, NoDesktop: true}))
	eq(t, "counts only", desktopReactionsOf(a, "5001"), []string{"❤️  x3"})

	d, _ = db.Open(path)
	db.Exec(d, "CREATE TABLE ZLIKE (Z_PK INTEGER PRIMARY KEY, ZMESSAGETOKEN INTEGER, ZSENDER INTEGER, ZLIKEVALUE INTEGER, ZUNICODEREACTION VARCHAR)")
	db.Exec(d, "INSERT INTO ZLIKE VALUES (1, 5001, 2, 1, NULL), (2, 5001, NULL, 1, NULL), (3, 5001, 1, 0, NULL)")
	d.Close()
	must(t, Viber(a, nil, ViberOptions{IphoneDB: path, NoDesktop: true}))
	eq(t, "by whom", desktopReactionsOf(a, "5001"), []string{"❤️ ", "❤️ +15558880002", "❤️  me"})
}
