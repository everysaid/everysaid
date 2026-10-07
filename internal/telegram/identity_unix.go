//go:build !windows

package telegram

import "golang.org/x/sys/unix"

// uname is Python's platform.uname() machine and release.
func uname() (string, string) {
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return "", ""
	}
	return unix.ByteSliceToString(u.Machine[:]), unix.ByteSliceToString(u.Release[:])
}
