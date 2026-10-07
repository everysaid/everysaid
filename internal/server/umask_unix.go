//go:build unix

package server

import "syscall"

// umask: what the server writes (logs, thumbnails, the database) is the user's alone.
func umask() { syscall.Umask(0o077) }
