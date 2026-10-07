package iphone

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSignalRemovesDecryptedData: Ctrl-C while a backup is open (its Manifest.db decrypted into
// the system's temporary folder) and a file is half decrypted removes both before the program
// ends; a Go program stopped by a signal runs no defers.
func TestSignalRemovesDecryptedData(t *testing.T) {
	isolate(t)
	bk := makeBackup(t, t.TempDir(), []testFile{{domain: "D", path: "a", data: []byte("abc")}})
	b, err := Open(bk, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	half := filepath.Join(t.TempDir(), "tmp123")
	os.WriteFile(half, []byte("half"), 0o600)
	hold(half)

	exited := make(chan int, 1)
	exit = func(code int) { exited <- code }
	defer func() { exit = os.Exit }()
	stop := onSignal()
	defer stop()
	p, _ := os.FindProcess(os.Getpid())
	if err := p.Signal(os.Interrupt); err != nil {
		t.Skip("no interrupt to send here:", err)
	}
	select {
	case code := <-exited:
		if code != 130 {
			t.Errorf("exit code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the signal was not handled")
	}
	for _, path := range []string{b.tmp, half} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", path, err)
		}
	}
}

// TestTemporaryReleased: what is held for a moment is let go once it is kept or removed.
func TestTemporaryReleased(t *testing.T) {
	isolate(t)
	bk := makeBackup(t, t.TempDir(), []testFile{{domain: "D", path: "a", data: []byte("abc")}})
	b, err := Open(bk, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ExtractFile("a", "", filepath.Join(t.TempDir(), "a"), nil); err != nil {
		t.Fatal(err)
	}
	temporary.Lock()
	held := len(temporary.paths)
	_, manifest := temporary.paths[b.tmp]
	temporary.Unlock()
	if held != 1 || !manifest {
		t.Errorf("held %d, the manifest's folder %v", held, manifest)
	}
	b.Close()
	if len(temporary.paths) != 0 {
		t.Errorf("still held: %v", temporary.paths)
	}
}
