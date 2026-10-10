// Meta's export (Messenger, Instagram) on an invented export, written as Meta writes it.
package importers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moji is a text as Meta's JSON writes it: each UTF-8 byte a Latin-1 character.
func moji(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		b.WriteRune(rune(c))
	}
	return b.String()
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	b, err := json.Marshal(v)
	must(t, err)
	must(t, os.WriteFile(path, b, 0o600))
}

const (
	metaTS1 = int64(1600000000123)
	metaTS2 = int64(1600000060456)
	metaTS3 = int64(1600000120789)
)

// metaExport makes an export of two threads, one in two files, with a photo, a reaction, a call
// and a group; owner "Zoë Owner".
func metaExport(t *testing.T) string {
	dir := t.TempDir()
	act := filepath.Join(dir, "your_facebook_activity")
	writeJSON(t, filepath.Join(dir, "personal_information", "profile_information", "profile_information.json"),
		M{"profile_v2": M{"name": M{"full_name": moji("Zoë Owner")}}})
	thread := filepath.Join(act, "messages", "inbox", "johndoe_1234")
	must(t, os.MkdirAll(filepath.Join(thread, "photos"), 0o700))
	must(t, os.WriteFile(filepath.Join(thread, "photos", "pic_1.jpg"), []byte("a picture"), 0o600))
	parts := []M{{"name": moji("Zoë Owner")}, {"name": "John Doe"}}
	writeJSON(t, filepath.Join(thread, "message_1.json"), M{
		"participants": parts, "title": "John Doe", "thread_type": "Regular", "thread_path": "inbox/johndoe_1234",
		"messages": []M{
			{"sender_name": "John Doe", "timestamp_ms": metaTS3, "content": "", "call_duration": 65, "type": "Call"},
			{"sender_name": moji("Zoë Owner"), "timestamp_ms": metaTS2, "content": moji("café ❤️ 日本"),
				"reactions": []M{{"reaction": moji("❤"), "actor": "John Doe"}}},
		}})
	writeJSON(t, filepath.Join(thread, "message_2.json"), M{
		"participants": parts, "title": "John Doe", "thread_type": "Regular", "thread_path": "inbox/johndoe_1234",
		"messages": []M{
			{"sender_name": "John Doe", "timestamp_ms": metaTS1, "content": "look",
				"photos": []M{{"uri": "your_facebook_activity/messages/inbox/johndoe_1234/photos/pic_1.jpg"}}},
		}})
	group := filepath.Join(act, "messages", "inbox", "friends_5678")
	writeJSON(t, filepath.Join(group, "message_1.json"), M{
		"participants": []M{{"name": moji("Zoë Owner")}, {"name": "John Doe"}, {"name": "Ann Roe"}},
		"title":        "Friends", "thread_type": "RegularGroup", "thread_path": "inbox/friends_5678",
		"messages": []M{
			{"sender_name": "Ann Roe", "timestamp_ms": metaTS1, "content": "hello all",
				"share": M{"link": "https://example.com/x"}},
			{"sender_name": "Ann Roe", "timestamp_ms": metaTS1, "content": "and again"},
		}})
	return dir
}

func TestMetaTextReadsTheUTF8MetaWroteAsLatin1(t *testing.T) {
	for _, s := range []string{"plain", "café ❤️", "日本", ""} {
		if got := metaText(moji(s)); got != s {
			t.Errorf("metaText(moji(%q)) = %q", s, got)
		}
	}
	// a text that was not so stays as it is: Latin-1 that is not UTF-8, and text beyond Latin-1
	for _, s := range []string{"café", "日本"} {
		if got := metaText(s); got != s {
			t.Errorf("metaText(%q) = %q", s, got)
		}
	}
}

func TestMetaExportImportsThreadsFilesReactionsAndCalls(t *testing.T) {
	a, _ := newArchive(t)
	dir := metaExport(t)
	var lines []string
	must(t, MetaExport(a, func(s string) { lines = append(lines, s) }, MetaExportOptions{Dir: dir, Service: "messenger"}))
	if n := a.Int("SELECT count(*) FROM message"); n != 5 {
		t.Fatalf("messages: %d (%v)", n, lines)
	}
	var text string
	var outgoing bool
	a.Row("SELECT text, outgoing FROM message WHERE ts = ?", []any{metaTS2}, &text, &outgoing)
	if text != "café ❤️ 日本" || !outgoing {
		t.Errorf("the owner's message: %q outgoing %v", text, outgoing)
	}
	if e := a.Int("SELECT count(*) FROM reaction r JOIN message m ON m.id = r.message_id WHERE m.ts = ? AND r.emoji = '❤'", metaTS2); e != 1 {
		t.Errorf("reactions: %d", e)
	}
	var kind string
	a.Row("SELECT k.name FROM message m JOIN message_kind k ON k.id = m.kind_id WHERE m.ts = ? AND m.text = 'look'", []any{metaTS1}, &kind)
	if kind != "image" {
		t.Errorf("the photo's kind: %q", kind)
	}
	if n := a.Int("SELECT count(*) FROM attachment"); n != 1 {
		t.Errorf("files: %d", n)
	}
	var answered, duration int64
	if !a.Row("SELECT answered, duration FROM call", nil, &answered, &duration) || answered != 1 || duration != 65 {
		t.Errorf("the call: %d %d", answered, duration)
	}
	// the group: its title, its members (not the owner), the link
	var title string
	var group bool
	a.Row("SELECT title, is_group FROM conversation WHERE key = 'dyi:friends_5678'", nil, &title, &group)
	if title != "Friends" || !group {
		t.Errorf("the group: %q %v", title, group)
	}
	if n := a.Int("SELECT count(*) FROM conversation_member cm JOIN conversation c ON c.id = cm.conversation_id WHERE c.key = 'dyi:friends_5678'"); n != 2 {
		t.Errorf("the group's members: %d", n)
	}
	a.Row("SELECT text FROM message WHERE subtype = 'link'", nil, &text)
	if text != "hello all\nhttps://example.com/x" {
		t.Errorf("the link: %q", text)
	}
	// the owner's name is an account, not a person of their own in the chats
	if n := a.Int("SELECT count(*) FROM account x JOIN address ad ON ad.id = x.address_id WHERE ad.value = ?", "Zoë Owner"); n != 1 {
		t.Errorf("the owner's account: %d", n)
	}
	// again: nothing new
	must(t, MetaExport(a, nil, MetaExportOptions{Dir: dir, Service: "messenger"}))
	if n := a.Int("SELECT count(*) FROM message"); n != 5 {
		t.Errorf("after a second import: %d", n)
	}
}

func TestMetaExportKeepsOnceWhatTheLiveSourceBrought(t *testing.T) {
	a, _ := newArchive(t)
	w := NewMetaWriter(a, "messenger", "messenger-live", "", "")
	w.Self(MetaUser{ID: "100", Name: "Zoë Owner"})
	w.Conv(MetaThread{Key: "200", Members: []MetaUser{{ID: "200", Name: "John Doe"}}})
	if _, isNew := w.Add(MetaMessage{Thread: "200", ID: "mid.$abc", TS: metaTS2, Outgoing: true, Text: "café ❤️ 日本"}); !isNew {
		t.Fatal("the live message was not added")
	}
	a.Commit()
	must(t, MetaExport(a, nil, MetaExportOptions{Dir: metaExport(t), Service: "messenger"}))
	if n := a.Int("SELECT count(*) FROM message WHERE ts = ?", metaTS2); n != 1 {
		t.Errorf("the same message twice: %d", n)
	}
	if n := a.Int("SELECT count(*) FROM message_origin o JOIN message m ON m.id = o.message_id WHERE m.ts = ?", metaTS2); n != 2 {
		t.Errorf("its origins: %d", n)
	}
	// the export's John Doe is the live source's 200, by the name it showed
	var people int64
	people = a.Int("SELECT count(DISTINCT pa.person_id) FROM person_address pa JOIN address ad ON ad.id = pa.address_id " +
		"WHERE ad.value IN ('200', 'John Doe')")
	if people != 1 {
		t.Errorf("John Doe is %d people", people)
	}
}

func TestMetaExportWithoutAnOwnerAsks(t *testing.T) {
	a, _ := newArchive(t)
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "messages", "inbox", "x_1", "message_1.json"), M{
		"participants": []M{{"name": "A"}, {"name": "B"}}, "messages": []M{{"sender_name": "A", "timestamp_ms": 1, "content": "x"}}})
	if err := MetaExport(a, nil, MetaExportOptions{Dir: dir, Service: "messenger"}); err != ErrMetaOwner {
		t.Errorf("no owner: %v", err)
	}
	must(t, MetaExport(a, nil, MetaExportOptions{Dir: dir, Service: "messenger", Owner: "B"}))
	if n := a.Int("SELECT count(*) FROM message WHERE NOT outgoing"); n != 1 {
		t.Errorf("messages: %d", n)
	}
}

func TestMetaExportOfTheEncryptedChats(t *testing.T) {
	a, _ := newArchive(t)
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "messages", "media"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "messages", "media", "v.mp4"), []byte("video"), 0o600))
	writeJSON(t, filepath.Join(dir, "messages", "John Doe.json"), M{
		"participants": []string{"Me Owner", "John Doe"}, "threadName": "John Doe",
		"messages": []M{
			{"senderName": "John Doe", "timestamp": metaTS1, "text": "hi", "type": "text", "isUnsent": false,
				"media": []M{}, "reactions": []M{{"actor": "Me Owner", "reaction": "👍"}}},
			{"senderName": "Me Owner", "timestamp": metaTS2, "text": "", "type": "media", "isUnsent": false,
				"media": []M{{"uri": "messages/media/v.mp4"}}, "reactions": []M{}},
		}})
	must(t, MetaExport(a, nil, MetaExportOptions{Dir: dir, Service: "instagram", Owner: "Me Owner"}))
	if n := a.Int("SELECT count(*) FROM message m JOIN service s ON s.id = m.service_id WHERE s.name = 'instagram'"); n != 2 {
		t.Fatalf("messages: %d", n)
	}
	if n := a.Int("SELECT count(*) FROM reaction WHERE outgoing = 1 AND emoji = '👍'"); n != 1 {
		t.Errorf("the owner's reaction: %d", n)
	}
	var kind string
	a.Row("SELECT k.name FROM message m JOIN message_kind k ON k.id = m.kind_id WHERE m.ts = ?", []any{metaTS2}, &kind)
	if kind != "video" || a.Int("SELECT count(*) FROM attachment") != 1 {
		t.Errorf("the video: %q, %d files", kind, a.Int("SELECT count(*) FROM attachment"))
	}
}
