package demo

import (
	"os"
	"testing"

	"everysaid/internal/db"
)

// A journal of the old archive (its process still there, or dead without closing it) is not replayed onto the new archive.
func TestRebuildOverAStaleJournal(t *testing.T) {
	_, path := Build(t)
	// a process that had the old archive open is still there (or died with its journal left):
	// its journal is not checkpointed
	w, err := db.Open(path, "journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	if _, err := w.Exec("PRAGMA wal_autocheckpoint = 0"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec("UPDATE message SET text = 'changed' WHERE id <= 2000"); err != nil {
		t.Fatal(err)
	}
	null, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	stdout := os.Stdout
	os.Stdout = null
	_, err = BuildArchive(7)
	os.Stdout = stdout
	null.Close()
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.ReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var ok string
	if err := d.QueryRow("PRAGMA integrity_check").Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity: %q %v", ok, err)
	}
	if n := db.Int(d, "SELECT count(*) FROM message"); n != 10582 {
		t.Errorf("%d messages", n)
	}
	if n := db.Int(d, "SELECT count(*) FROM message WHERE text = 'changed'"); n != 0 {
		t.Errorf("%d messages from the old journal", n)
	}
}
