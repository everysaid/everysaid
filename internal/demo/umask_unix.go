//go:build unix

package demo

import "syscall"

// private makes what the process writes from now on its owner's alone (the demo builds an archive
// and serves it).
func private() { syscall.Umask(0o077) }
