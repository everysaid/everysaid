package iphone

// New (the Python removed its decrypted Manifest.db as the interpreter unwound after Ctrl-C): a
// signal ends a Go program without running its defers, so what a run decrypts for a moment is
// kept here, and the command lines remove it before they go.

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"
)

// temporary is the decrypted data a run holds for a moment: the folder of each open backup's
// Manifest.db, and each file being decrypted.
var temporary = struct {
	sync.Mutex
	paths map[string]bool
}{paths: map[string]bool{}}

func hold(path string) {
	temporary.Lock()
	temporary.paths[path] = true
	temporary.Unlock()
}

func release(path string) {
	temporary.Lock()
	delete(temporary.paths, path)
	temporary.Unlock()
}

// RemoveTemporary removes the decrypted data of the runs still going: for a program stopped by a
// signal while one runs (the server, as it shuts down).
func RemoveTemporary() {
	temporary.Lock()
	defer temporary.Unlock()
	for path := range temporary.paths {
		os.RemoveAll(path)
	}
	clear(temporary.paths)
}

var exit = os.Exit

// onSignal: until stop, an interrupt or a termination removes the decrypted data, gives the
// terminal back as it was (its echo, off while the password is asked), then ends the program as
// the signal would have (128 + its number).
func onSignal() (stop func()) {
	fd := int(os.Stdin.Fd())
	state, _ := term.GetState(fd)
	got := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(got, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case s := <-got:
			RemoveTemporary()
			if state != nil {
				term.Restore(fd, state)
			}
			code := 130
			if s == syscall.SIGTERM {
				code = 143
			}
			exit(code)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(got)
		close(done)
	}
}
