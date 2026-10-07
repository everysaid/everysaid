// The session's lock across processes: the server's live connection and `everysaid telegram-sync`
// run by hand would otherwise be two clients of one key (the gate in client.go keeps one process
// to one). A lock the system holds (flock, LockFileEx) rather than a file's existence: it goes
// with the process that held it, so a crash leaves nothing stale.
package telegram

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// errHeld: another process has the session open.
var errHeld = errors.New("held by another process")

// heldElsewhere is what the user is told when another process has the session open.
const heldElsewhere = "Telegram is connected by another Everysaid (the server, or telegram-sync run by hand): " +
	"stop it, or wait until it finishes"

// LockPath is the lock file, beside the store.
func LockPath() string { return filepath.Join(Folder(), "telegram.lock") }

// lockSession takes the lock for this process, writing its pid in the file (for whoever looks);
// errHeld when another process has it.
func lockSession() (*os.File, error) {
	if err := os.MkdirAll(Folder(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(LockPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := tryLock(f); err != nil {
		f.Close()
		return nil, err
	}
	f.Truncate(0)
	f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return f, nil
}

func unlockSession(f *os.File) {
	unlock(f)
	f.Close()
}
