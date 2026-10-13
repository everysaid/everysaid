package archive_test

import (
	"path/filepath"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// A transaction that began with a read still writes after another connection committed (the
// server's or another plugin's): it holds the write lock from its start, so it is not left with a
// stale snapshot that SQLite refuses to write from (SQLITE_BUSY_SNAPSHOT, 517, which no wait
// helps).
func TestWriteAfterAnotherCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	other, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	a.Int("SELECT count(*) FROM device")
	done := make(chan error, 1)
	go func() {
		_, err := other.Exec("INSERT INTO setting VALUES ('other', 'x')")
		done <- err
	}()
	select {
	case err := <-done: // committed while a's transaction is open: allowed only before the fix
		if err != nil {
			t.Fatal(err)
		}
		done <- nil
	case <-time.After(300 * time.Millisecond): // waiting for a's transaction, as it should
	}
	func() {
		defer archive.Recover(&err)
		a.Device("phone", "")
		a.Commit()
	}()
	if err != nil {
		t.Fatalf("write after another commit: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A new archive has the History of GIFs as a GIFs plugin, once: taken away, it is not put back.
func TestGIFHistorySeededOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	count := func() int64 {
		a, err := archive.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		defer a.Commit()
		return a.Int("SELECT count(*) FROM plugin_instance WHERE plugin = 'gif-history' AND kind = 'gifs'")
	}
	if n := count(); n != 1 {
		t.Fatalf("%d at first", n)
	}
	if n := count(); n != 1 {
		t.Fatalf("%d opened again", n)
	}
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Exec("DELETE FROM plugin_instance WHERE plugin = 'gif-history'")
	a.Commit()
	a.Close()
	if n := count(); n != 0 {
		t.Fatalf("%d after it was taken away", n)
	}
}
