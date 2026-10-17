package archive_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// A long import lets the others write while it goes on: between two messages, once it has kept the
// archive for BreatheAfter, it commits and runs its OnBreathe; the others' writes go through then,
// not after its end, and its own go on after them.
func TestALongImportLetsOthersWrite(t *testing.T) {
	defer func(d time.Duration) { archive.BreatheAfter = d }(archive.BreatheAfter)
	archive.BreatheAfter = 20 * time.Millisecond
	path := filepath.Join(t.TempDir(), "a.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	other, err := db.Open(path, "busy_timeout(2000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	breaths := 0
	a.OnBreathe(func() { breaths++ }, nil)
	src := a.Source("test", "/x", "", "")
	conv := a.Conversation("sms", []archive.Handle{archive.H("phone", "+15550001111")}, "", "")
	sender := a.Address(archive.H("phone", "+15550001111"))
	wrote := make(chan time.Time, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		if _, err := other.Exec("INSERT INTO setting VALUES ('other', 'x')"); err != nil {
			t.Error(err)
		}
		wrote <- time.Now()
	}()
	err = func() (err error) {
		defer archive.Recover(&err)
		for i := range 60 {
			a.AddMessage(src, fmt.Sprint(i), archive.Message{Service: "sms", ConversationID: conv, TS: int64(i), SenderID: sender,
				Kind: "text", Text: fmt.Sprint(i)})
			time.Sleep(5 * time.Millisecond)
		}
		a.Commit()
		return nil
	}()
	ended := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	if at := <-wrote; !at.Before(ended) {
		t.Fatal("the other's write waited for the import's end")
	}
	if breaths == 0 {
		t.Fatal("never breathed")
	}
	if n := db.Int(other, "SELECT count(*) FROM message"); n != 60 {
		t.Fatalf("%d messages", n)
	}
}

// While the import reads rows of the archive it does not breathe: their snapshot would make its next
// write fail once another committed (SQLITE_BUSY_SNAPSHOT).
func TestNoBreathWhileReading(t *testing.T) {
	defer func(d time.Duration) { archive.BreatheAfter = d }(archive.BreatheAfter)
	archive.BreatheAfter = 0
	a, err := archive.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	breaths := 0
	a.OnBreathe(func() { breaths++ }, nil)
	src := a.Source("test", "/x", "", "")
	conv := a.Conversation("sms", []archive.Handle{archive.H("phone", "+15550001111")}, "", "")
	for i := range 3 {
		a.AddMessage(src, fmt.Sprint(i), archive.Message{Service: "sms", ConversationID: conv, TS: int64(i), Outgoing: true, Kind: "text"})
	}
	a.Commit()
	breaths, n := 0, 0
	a.Each("SELECT id FROM message WHERE id <= 3", nil, func(scan func(...any)) {
		n++
		a.AddMessage(src, fmt.Sprint("copy", n), archive.Message{Service: "sms", ConversationID: conv, TS: 9, Outgoing: true, Kind: "text"})
	})
	if breaths != 0 {
		t.Fatalf("breathed %d times while reading", breaths)
	}
	a.AddMessage(src, "after", archive.Message{Service: "sms", ConversationID: conv, TS: 10, Outgoing: true, Kind: "text"})
	if breaths != 1 {
		t.Fatalf("after reading: %d breaths", breaths)
	}
}
