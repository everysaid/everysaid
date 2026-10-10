package db_test

import (
	"os"
	"strings"
	"testing"

	"everysaid/internal/db"
)

// A large write leaves no WAL of its size behind: Checkpoint empties it, and journal_size_limit
// cuts it back after the checkpoints that follow.
func TestWALEmptied(t *testing.T) {
	path := t.TempDir() + "/x.db"
	d, err := db.Open(path, "journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetMaxOpenConns(1)
	var limit int64
	d.QueryRow("PRAGMA journal_size_limit").Scan(&limit)
	if limit != db.JournalLimit {
		t.Fatalf("journal_size_limit %d, want %d", limit, db.JournalLimit)
	}
	db.Exec(d, "CREATE TABLE t (x TEXT)")
	tx, _ := d.Begin()
	big := strings.Repeat("x", 4000)
	for i := 0; i < 3000; i++ {
		db.Exec(tx, "INSERT INTO t VALUES (?)", big)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path + "-wal"); err != nil || st.Size() < 10<<20 {
		t.Fatalf("expected a large WAL before the checkpoint: %v %v", st, err)
	}
	if err := db.Checkpoint(d); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path + "-wal"); err != nil || st.Size() != 0 {
		t.Fatalf("WAL after the checkpoint: %v %v", st, err)
	}
}
