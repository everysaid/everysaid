package archive_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"everysaid/internal/archive"
)

// A handle's photos: a row per photo, last_seen moving while it stays, a removal marked on the
// photo and followed by a row without one, sightings out of order put beside their time, the same
// sighting twice changing nothing.
func TestPhotoHistory(t *testing.T) {
	a, err := archive.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, sha := range []string{"aa", "bb", "cc"} {
		a.Exec("INSERT INTO media VALUES (?, 1, 'image/jpeg', ?)", sha, "media/"+sha)
	}
	h := archive.H("phone", "+306900000001")
	if a.HandlePhoto(h, "whatsapp", "aa", 100) {
		t.Fatal("a handle the archive does not have was recorded")
	}
	a.Address(h)
	a.HandlePhoto(h, "whatsapp", "", 50) // none, and never a photo before: nothing
	a.HandlePhoto(h, "whatsapp", "aa", 100)
	a.HandlePhoto(h, "whatsapp", "aa", 200)
	a.HandlePhoto(h, "whatsapp", "bb", 300)
	a.HandlePhoto(h, "whatsapp", "", 400)
	a.HandlePhoto(h, "whatsapp", "", 450)
	a.HandlePhoto(h, "whatsapp", "cc", 500)
	a.HandlePhoto(h, "whatsapp", "aa", 80)  // an older sighting of the first
	a.HandlePhoto(h, "whatsapp", "cc", 20)  // a history's photo before all of them
	a.HandlePhoto(h, "whatsapp", "bb", 300) // again
	a.HandlePhoto(h, "telegram", "cc", 300) // another service's, apart
	got := rows(a, "handle_photo")
	want := "whatsapp cc 20-20 -|whatsapp aa 80-200 -|whatsapp bb 300-300 400|whatsapp - 400-450 -|whatsapp cc 500-500 -|telegram cc 300-300 -"
	if got != want {
		t.Fatalf("handle_photo:\n got %s\nwant %s", got, want)
	}

	// another photo seen within one's sightings: that one is split around it
	a.Exec("DELETE FROM handle_photo")
	a.HandlePhoto(h, "whatsapp", "aa", 100)
	a.HandlePhoto(h, "whatsapp", "aa", 200)
	a.HandlePhoto(h, "whatsapp", "bb", 150)
	a.HandlePhoto(h, "whatsapp", "", 170)
	a.HandlePhoto(h, "whatsapp", "bb", 150) // again
	if got, want := rows(a, "handle_photo"), "whatsapp aa 100-100 -|whatsapp bb 150-150 170|whatsapp - 170-170 -|whatsapp aa 200-200 -"; got != want {
		t.Fatalf("split:\n got %s\nwant %s", got, want)
	}

	conv := a.Conversation("telegram", nil, "-100", "Group")
	a.ChatPhoto(conv, "telegram", "aa", 10)
	a.ChatPhoto(conv, "telegram", "", 20)
	a.ChatPhoto(conv, "telegram", "aa", 30) // put back: a new row
	if got, want := rows(a, "chat_photo"), "telegram aa 10-10 20|telegram - 20-20 -|telegram aa 30-30 -"; got != want {
		t.Fatalf("chat_photo:\n got %s\nwant %s", got, want)
	}
	a.Commit()
}

func rows(a *archive.Archive, table string) string {
	var out []string
	a.Each("SELECT s.name, coalesce(p.sha256, '-'), p.first_seen, p.last_seen, coalesce(p.removed_at, '-') FROM "+table+
		" p JOIN service s ON s.id = p.service_id ORDER BY s.name DESC, p.first_seen", nil, func(scan func(...any)) {
		var svc, sha, removed string
		var first, last int64
		scan(&svc, &sha, &first, &last, &removed)
		out = append(out, fmt.Sprintf("%s %s %d-%d %s", svc, sha, first, last, removed))
	})
	return strings.Join(out, "|")
}

// A handle removed as spam takes its photos and its chat's with it, their files too where nothing
// else has them.
func TestPurgeSpamTakesPhotos(t *testing.T) {
	a, err := archive.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, sha := range []string{"aa", "bb", "kept"} {
		a.Exec("INSERT INTO media VALUES (?, 1, 'image/jpeg', ?)", sha, "media/"+sha)
	}
	h, other := archive.H("phone", "+306900000001"), archive.H("phone", "+306900000002")
	conv := a.Conversation("whatsapp", []archive.Handle{h}, "", "")
	a.Address(other)
	a.HandlePhoto(h, "whatsapp", "aa", 100)
	a.ChatPhoto(conv, "whatsapp", "bb", 100)
	a.HandlePhoto(other, "whatsapp", "kept", 100)
	a.Exec("INSERT INTO spam VALUES (?, 'removed', NULL, 1)", a.Address(h))
	p := archive.PurgeSpam(a.Tx(), nil)
	a.Commit()
	if got := strings.Join(p.Files, ","); got != "media/aa,media/bb" && got != "media/bb,media/aa" {
		t.Fatalf("files: %s", got)
	}
	if n := a.Int("SELECT count(*) FROM media"); n != 1 {
		t.Fatalf("%d media left", n)
	}
	if got := rows(a, "handle_photo") + "/" + rows(a, "chat_photo"); got != "whatsapp kept 100-100 -/" {
		t.Fatalf("photos left: %s", got)
	}
}
