// The server's log (everysaid/server/__init__.py, log_config): warnings and errors on the console and
// in <state>/logs/server.log, kept to five files of 5 MB.
package server

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"everysaid/internal/config"
)

// rotating is a log file that moves aside when it grows past max: server.log.1 … .4, the oldest gone.
type rotating struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func openRotating(path string, max int64, keep int) (*rotating, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	r := &rotating{path: path, max: max, keep: keep}
	return r, r.open()
}

func (r *rotating) open() error {
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotating) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return len(b), nil
	}
	if r.size+int64(len(b)) > r.max && r.size > 0 {
		r.f.Close()
		for i := r.keep - 1; i >= 1; i-- {
			os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
		}
		os.Rename(r.path, r.path+".1")
		if err := r.open(); err != nil {
			r.f = nil
			return len(b), nil
		}
	}
	n, err := r.f.Write(b)
	r.size += int64(n)
	return n, err
}

func (r *rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	return r.f.Close()
}

// defaultLogger writes warnings and errors to the console and to <state>/logs/server.log.
func defaultLogger() (*slog.Logger, func()) {
	var w io.Writer = os.Stderr
	closer := func() {}
	if f, err := openRotating(filepath.Join(config.Logs, "server.log"), 5_000_000, 4); err == nil {
		w = io.MultiWriter(os.Stderr, f)
		closer = func() { f.Close() }
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelWarn})), closer
}
