package core_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/core"
	"everysaid/internal/db"
)

// A person's photo by the order of names (Telegram's profile before WhatsApp's), a missing file or a
// removal giving way to the next, the user's pins; a group's photo; the history and the choices.
func TestPhotos(t *testing.T) {
	s := store(t)
	media := func(tx *sql.Tx, name string) string {
		sha := fmt.Sprintf("%064x", len(name)*7919+int(name[0]))
		path := "media/" + sha[:2] + "/" + sha + ".jpg"
		file := filepath.Join(archive.MediaRoot(), path)
		os.MkdirAll(filepath.Dir(file), 0o700)
		os.WriteFile(file, []byte(name), 0o600)
		db.Exec(tx, "INSERT INTO media VALUES (?, ?, 'image/jpeg', ?)", sha, len(name), path)
		return sha
	}
	var pid, phone, tgID, conv int64
	var wa, tg, old, grp string
	svc := func(name string) string { return "(SELECT id FROM service WHERE name = '" + name + "')" }
	write(t, s, func(tx *sql.Tx) {
		pid = db.LastID(tx, "INSERT INTO person DEFAULT VALUES")
		phone = db.LastID(tx, "INSERT INTO address (kind_id, value) VALUES ((SELECT id FROM address_kind WHERE name = 'phone'), '+15559990000')")
		tgID = db.LastID(tx, "INSERT INTO address (kind_id, value, service_id) VALUES ((SELECT id FROM address_kind WHERE name = 'id'), '777', "+svc("telegram")+")")
		for _, aid := range []int64{phone, tgID} {
			db.Exec(tx, "INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", aid, pid)
		}
		wa, tg, old, grp = media(tx, "whatsapp photo"), media(tx, "telegram"), media(tx, "old telegram photo"), media(tx, "g")
		db.Exec(tx, "INSERT INTO handle_photo (address_id, service_id, sha256, first_seen, last_seen) VALUES "+
			"(?, "+svc("whatsapp")+", ?, 300, 300), (?, "+svc("telegram")+", ?, 100, 150), (?, "+svc("telegram")+", ?, 200, 250)",
			phone, wa, tgID, old, tgID, tg)
		conv = db.LastID(tx, "INSERT INTO conversation (service_id, key, title, is_group) VALUES ("+svc("telegram")+", '-55', 'Photo group', 1)")
		db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, kind_id, text) VALUES ("+svc("telegram")+
			", ?, 1000, 1, (SELECT id FROM message_kind WHERE name = 'text'), 'hi')", conv)
		db.Exec(tx, "INSERT INTO chat_photo (conversation_id, service_id, sha256, first_seen, last_seen) VALUES (?, "+svc("telegram")+", ?, 10, 10)", conv, grp)
	})
	shown := func() string {
		if ph := core.PeopleOf(s).Photo(pid); ph != nil {
			return ph.Sha
		}
		return ""
	}
	if got := shown(); got != tg {
		t.Fatalf("by the order: %s, want Telegram's", got)
	}
	// its file gone: the one before it of that handle
	tgFile := filepath.Join(archive.MediaRoot(), "media", tg[:2], tg+".jpg")
	os.Rename(tgFile, tgFile+".away")
	write(t, s, func(tx *sql.Tx) {}) // the archive changed: chosen again
	if got := shown(); got != old {
		t.Fatalf("Telegram's file gone: %s, want its older one", got)
	}
	os.Rename(tgFile+".away", tgFile)
	none := core.Opt[string]{}
	for _, c := range []struct{ pin, want string }{{"whatsapp", wa}, {fmt.Sprintf("address:%d", phone), wa}, {"none", ""},
		{"signal", tg}, {"", tg}} {
		pin := core.To(c.pin)
		if c.pin == "" {
			pin = core.Clear[string]()
		}
		if err := core.SetPerson(s, pid, none, none, none, pin); err != nil {
			t.Fatal(err)
		}
		if got := shown(); got != c.want {
			t.Fatalf("pinned %q: %s, want %s", c.pin, got, c.want)
		}
	}
	if err := core.SetPerson(s, pid, none, none, none, core.To("nowhere")); err == nil {
		t.Fatal("a source that is none taken")
	}
	// Telegram says there is none now: WhatsApp's
	write(t, s, func(tx *sql.Tx) {
		db.Exec(tx, "INSERT INTO handle_photo (address_id, service_id, sha256, first_seen, last_seen) VALUES (?, "+svc("telegram")+", NULL, 400, 400)", tgID)
	})
	if got := shown(); got != wa {
		t.Fatalf("Telegram's removed: %s, want WhatsApp's", got)
	}
	ppl := core.PeopleOf(s)
	if h := ppl.PhotoHistory(pid); len(h) != 3 || h[0]["sha"] != wa || h[1]["current"] != false {
		t.Fatalf("history: %v", h)
	}
	if c := ppl.PhotoChoices(pid); len(c) != 1 || c[0]["pin"] != "whatsapp" {
		t.Fatalf("choices: %v", c)
	}
	person := core.Person(s, pid)
	if person["avatar"] != wa[:12] || person["photo_from"] != "whatsapp" {
		t.Fatalf("person: %v %v", person["avatar"], person["photo_from"])
	}
	// the group's
	chat := core.Index(s).ConvChat[conv]
	if got := core.GetChat(s, chat)["avatar"]; got != grp[:12] {
		t.Fatalf("group: %v", got)
	}
}
