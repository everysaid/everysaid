//go:build windows

package telegram

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockedByte is the part of the file locked: past what is written in it (the pid), which Windows
// would otherwise refuse to let the others read.
const lockedByte = 1 << 30

// tryLock takes the lock on f without waiting; errHeld when another holds it. The system lets it go
// when the process ends, however it ends.
func tryLock(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockedByte}
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return errHeld
	}
	return err
}

func unlock(f *os.File) {
	windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{Offset: lockedByte})
}
