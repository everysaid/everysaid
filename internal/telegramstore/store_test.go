// Ports test_a_read_seen_late_is_known_but_not_when of tests/test_marks.py.
package telegramstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"everysaid/internal/db"
)

func p(n int64) *int64 { return &n }

// A read seen late: the other read further while nothing was connected: not the time last seen
// live (which would be earlier than some of those messages), but known, not when.
func TestAReadSeenLateIsKnownButNotWhen(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const maria = 111
	if moved, err := NoteRead(d, maria, nil, p(10), true); err != nil || !moved {
		t.Fatal("first read not moved", err)
	}
	if db.Int(d, "SELECT outbox_at FROM chat_read") == 0 {
		t.Fatal("outbox_at not set")
	}
	if moved, _ := NoteRead(d, maria, nil, p(10), false); moved { // the same: nothing moved
		t.Fatal("moved again")
	}
	if db.Int(d, "SELECT outbox_at FROM chat_read") == 0 {
		t.Fatal("outbox_at lost")
	}
	if moved, _ := NoteRead(d, maria, nil, p(20), false); !moved {
		t.Fatal("not moved to 20")
	}
	var outbox int64
	var at sql.NullInt64
	db.Row(d, "SELECT outbox, outbox_at FROM chat_read", nil, &outbox, &at)
	if outbox != 20 || at.Valid {
		t.Fatalf("got %d %v", outbox, at)
	}
}
