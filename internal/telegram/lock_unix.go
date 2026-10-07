//go:build !windows

package telegram

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes the lock on f without waiting; errHeld when another holds it. The system lets it go
// when the process ends, however it ends.
func tryLock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errHeld
	}
	return err
}

func unlock(f *os.File) { unix.Flock(int(f.Fd()), unix.LOCK_UN) }
