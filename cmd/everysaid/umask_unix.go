//go:build !windows

package main

import "syscall"

// umask keeps every file Everysaid makes to its user (the archive, the media, the logs).
func umask() { syscall.Umask(0o077) }
