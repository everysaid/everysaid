//go:build unix

package demo

import "syscall"

// private makes what the process writes from now on its owner's alone, as `python -m everysaid`
// (which builds the demo and serves it) does.
func private() { syscall.Umask(0o077) }
