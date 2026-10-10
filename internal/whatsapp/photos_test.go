package whatsapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/importers"
)

// fakePhotos answers GetProfilePictureInfo as WhatsApp would, from what each jid has now.
type fakePhotos struct {
	now   map[string]string // jid -> picture id; "" none; "hidden"
	asked []string
	busy  bool
}

func (f *fakePhotos) GetProfilePictureInfo(_ context.Context, jid types.JID, p *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error) {
	f.asked = append(f.asked, jid.String())
	switch id := f.now[jid.String()]; {
	case f.busy:
		return nil, whatsmeow.ErrIQRateOverLimit
	case id == "hidden":
		return nil, whatsmeow.ErrProfilePictureUnauthorized
	case id == "":
		return nil, whatsmeow.ErrProfilePictureNotSet
	case id == p.ExistingID:
		return nil, nil
	default:
		return &types.ProfilePictureInfo{ID: id, URL: "https://pps.example/" + id, Type: "image"}, nil
	}
}

func photoRows(t *testing.T, store *MessageStore) string {
	rows, err := store.db.Query("SELECT jid, picture_id, coalesce(path, '-'), first_seen = last_seen FROM profile_photo ORDER BY jid, first_seen, rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid, id, path string
		var once bool
		rows.Scan(&jid, &id, &path, &once)
		out = append(out, fmt.Sprint(jid, " ", id, " ", path, " ", once))
	}
	return strings.Join(out, "|")
}

// The photos asked for one at a time, each chat once until it is due again; a change WhatsApp tells
// of asked for first, a removal recorded at once; a hidden photo changes nothing; too many
// questions wait.
func TestPhotosAsked(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenMessages(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	photoFetch = func(_ context.Context, url string) ([]byte, error) { return []byte("jpeg of " + url), nil }
	defer func() { photoFetch = fetchPhoto }()
	maria, group, hidden := "15557770001@s.whatsapp.net", "15557770001-1600000000@g.us", "15557770003@s.whatsapp.net"
	for _, jid := range []string{maria, group, hidden, "status@broadcast", "123@newsletter"} {
		store.StoreChat(jid, "x", time.Now())
	}
	f := &fakePhotos{now: map[string]string{maria: "p1", group: "g1", hidden: "hidden"}}
	log := &logger{b: &Bridge{}}
	for range 5 {
		if jid := store.nextPhoto(time.Now()); jid != "" {
			askPhoto(context.Background(), f, store, jid, log)
		}
	}
	if got := strings.Join(f.asked, ","); got != group+","+maria+","+hidden {
		t.Fatalf("asked: %s", got)
	}
	want := group + " g1 photos/" + group + "/g1.jpg true|" + maria + " p1 photos/" + maria + "/p1.jpg true"
	if got := photoRows(t, store); got != want {
		t.Fatalf("photos:\n got %s\nwant %s", got, want)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "photos", maria, "p1.jpg")); err != nil || string(b) != "jpeg of https://pps.example/p1" {
		t.Fatalf("file %q %v", b, err)
	}

	// Maria's changed (WhatsApp says so), the group's removed
	f.now[maria] = "p2"
	var p *Photos
	p.changed(store, &events.Picture{JID: types.NewJID("15557770001", types.DefaultUserServer), PictureID: "p2"})
	p.changed(store, &events.Picture{JID: types.NewJID("15557770001-1600000000", types.GroupServer), Remove: true,
		Timestamp: time.Now().Add(time.Second)})
	if jid := store.nextPhoto(time.Now()); jid != maria {
		t.Fatalf("next: %q", jid)
	}
	askPhoto(context.Background(), f, store, maria, log)
	if jid := store.nextPhoto(time.Now()); jid != "" {
		t.Fatalf("next: %q", jid)
	}
	// later, due again: the same photo is a later sighting
	askPhoto(context.Background(), f, store, maria, log)
	want = group + " g1 photos/" + group + "/g1.jpg true|" + group + "  - true|" + maria + " p1 photos/" + maria + "/p1.jpg true|" +
		maria + " p2 photos/" + maria + "/p2.jpg true"
	if got := photoRows(t, store); got != want {
		t.Fatalf("photos:\n got %s\nwant %s", got, want)
	}
	f.busy = true
	if wait := askPhoto(context.Background(), f, store, maria, log); wait != photoBusy {
		t.Fatalf("too many questions: wait %v", wait)
	}

	// into the archive: the files taken, the photos at their times (a second apart here: made apart)
	store.db.Exec("UPDATE profile_photo SET first_seen = 1000 * rowid, last_seen = 1000 * rowid")
	t.Cleanup(config.Load)
	t.Setenv("EVERYSAID_DATA", t.TempDir())
	config.Load()
	a, err := archive.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := archive.H("phone", "+15557770001")
	a.Address(h)
	a.Conversation("whatsapp", []archive.Handle{h}, group, "Friends")
	a.Commit()
	step := importers.PhotosWhatsAppBridge(filepath.Join(dir, "messages.db"), nil)
	if err := importers.Media(a, nil, step); err != nil {
		t.Fatal(err)
	}
	if n := a.Int("SELECT count(*) FROM handle_photo WHERE sha256 IS NOT NULL"); n != 2 {
		t.Fatalf("%d photos of Maria", n)
	}
	if got := a.Int("SELECT count(*) FROM chat_photo WHERE sha256 IS NULL AND removed_at IS NULL") +
		10*a.Int("SELECT count(*) FROM chat_photo WHERE sha256 IS NOT NULL AND removed_at IS NOT NULL"); got != 11 {
		t.Fatalf("the group's photo and its removal: %d", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "photos", maria, "p1.jpg")); !os.IsNotExist(err) {
		t.Fatalf("the bridge's file is still there: %v", err)
	}
	if err := importers.Media(a, nil, step); err != nil { // again: nothing new, the files gone
		t.Fatal(err)
	}
	if n := a.Int("SELECT count(*) FROM handle_photo"); n != 2 {
		t.Fatalf("again: %d", n)
	}
}
