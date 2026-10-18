package importers

import (
	"reflect"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

func blockedOf(a *archive.Archive, where string) []string {
	return db.Strs(a.Tx(), "SELECT a.value FROM blocked b JOIN address a ON a.id = b.address_id WHERE b.phone = ? ORDER BY a.value", where)
}

// The people blocked on WhatsApp, as the live connection last heard, reach the archive by their
// number; one unblocked since leaves it.
func TestLiveBlocklist(t *testing.T) {
	a, _ := newArchive(t)
	keys := waKeys(t)
	db.Exec(keys, "INSERT INTO whatsapp_blocked VALUES (?, ?), (?, '15559876543@s.whatsapp.net')", waStore, waPeer, waStore)
	db.Exec(keys, "INSERT INTO whatsapp_state VALUES (?, 'blocklist', '1', 0)", waStore)
	state(t, a, keys)
	if got := blockedOf(a, "whatsapp"); !reflect.DeepEqual(got, []string{"+15551234567", "+15559876543"}) {
		t.Fatalf("blocked: %v", got)
	}
	db.Exec(keys, "DELETE FROM whatsapp_blocked WHERE jid = ?", waPeer)
	state(t, a, keys)
	if got := blockedOf(a, "whatsapp"); !reflect.DeepEqual(got, []string{"+15559876543"}) {
		t.Fatalf("after unblocking: %v", got)
	}
}

// A connection that never had its blocklist from WhatsApp leaves what the archive has of it.
func TestLiveWithoutBlocklist(t *testing.T) {
	a, _ := newArchive(t)
	a.Blocked("whatsapp", archive.H("phone", "+15559876543"), true)
	state(t, a, waKeys(t))
	if got := blockedOf(a, "whatsapp"); len(got) != 1 {
		t.Fatalf("blocked: %v", got)
	}
}

// The numbers blocked on an Android phone, as its export lists them, with the number as the phone
// wrote it; an export without them (the phone refused them to the shell) leaves those recorded.
func TestAndroidBlocked(t *testing.T) {
	a, _ := newArchive(t)
	dir := t.TempDir()
	exportDB := dir + "/export.db"
	d, err := db.Open(exportDB)
	must(t, err)
	db.Exec(d, "CREATE TABLE calls (_id TEXT, number TEXT, date TEXT, duration TEXT, type TEXT)")
	db.Exec(d, "CREATE TABLE blocked (_id TEXT, original_number TEXT, e164_number TEXT)")
	db.Exec(d, "INSERT INTO blocked VALUES ('1', '(555) 987-6543', '+15559876543'), ('2', '+15551112222', NULL)")
	d.Close()
	none := dir + "/none.db"
	must(t, Calls(a, nil, CallsOptions{IphoneDB: none, Exports: []archive.AndroidExport{{Device: "pixel", DB: exportDB}}}))
	if got := blockedOf(a, "pixel"); !reflect.DeepEqual(got, []string{"+15551112222", "+15559876543"}) {
		t.Fatalf("blocked: %v", got)
	}
	if got := db.Str(a.Tx(), "SELECT original FROM blocked b JOIN address a ON a.id = b.address_id WHERE a.value = '+15559876543'"); got != "(555) 987-6543" {
		t.Fatalf("as the phone wrote it: %q", got)
	}

	d, _ = db.Open(exportDB)
	db.Exec(d, "DROP TABLE blocked")
	d.Close()
	must(t, Calls(a, nil, CallsOptions{IphoneDB: none, Exports: []archive.AndroidExport{{Device: "pixel", DB: exportDB}}}))
	if got := blockedOf(a, "pixel"); len(got) != 2 {
		t.Fatalf("an export without them: %v", got)
	}
}
