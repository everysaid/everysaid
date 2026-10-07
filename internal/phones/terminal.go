// Ports the reading of a script's output in everysaid/plugins/sources.py (run_script).
package phones

import (
	"bytes"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Terminal reads a tool's output as a terminal shows it: a line ends with \n; a \r draws the line
// again (a progress bar). The bar is one line that changes in place: drawn as it changes (at most
// every 0.2 s, and its last state always), and in the log as each drawing ends. It is an
// io.Writer; Close says what is left.
type Terminal struct {
	log      func(line string, redrawn bool)
	progress func(line string)

	mu      sync.Mutex
	pending []byte
	shown   string
	sent    string
	drawn   time.Time
	timer   *time.Timer
	closed  bool
}

// NewTerminal: log(line, redrawn) takes each line that ended (redrawn: a progress bar's line as it
// ended), progress(line) the bar as it is drawn again.
func NewTerminal(log func(line string, redrawn bool), progress func(line string)) *Terminal {
	return &Terminal{log: log, progress: progress}
}

const redraw = 200 * time.Millisecond

func (t *Terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = append(t.pending, p...)
	for {
		i := bytes.IndexByte(t.pending, '\n')
		if i < 0 {
			break
		}
		text := strings.TrimRight(decode(t.pending[:i]), "\r")
		t.pending = t.pending[i+1:]
		parts := strings.Split(text, "\r")
		line := parts[len(parts)-1]
		if line == "" {
			line = t.shown
		}
		line = rstrip(line)
		redrawn := t.shown != "" || strings.Contains(text, "\r")
		t.shown, t.sent = "", ""
		if strings.TrimSpace(line) != "" {
			t.log(line, redrawn)
		}
	}
	if bytes.IndexByte(t.pending, '\r') >= 0 {
		parts := bytes.Split(t.pending, []byte("\r"))
		for j := len(parts) - 1; j >= 0; j-- {
			if s := decode(parts[j]); strings.TrimSpace(s) != "" {
				t.shown = s
				break
			}
		}
		t.pending = append([]byte(nil), parts[len(parts)-1]...)
	}
	t.draw()
	return len(p), nil
}

// draw sends the bar when it changed and 0.2 s have passed; else once they have.
func (t *Terminal) draw() {
	if t.shown == "" || t.shown == t.sent || t.closed {
		return
	}
	if wait := redraw - time.Since(t.drawn); wait > 0 {
		if t.timer == nil {
			t.timer = time.AfterFunc(wait, func() {
				t.mu.Lock()
				defer t.mu.Unlock()
				t.timer = nil
				t.draw()
			})
		}
		return
	}
	t.progress(t.shown)
	t.sent, t.drawn = t.shown, time.Now()
}

// Close logs what is left of the output.
func (t *Terminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	if strings.TrimSpace(decode(t.pending)) != "" {
		parts := strings.Split(decode(t.pending), "\r")
		t.log(rstrip(parts[len(parts)-1]), false)
	}
	t.pending = nil
	return nil
}

// decode is bytes as text, each byte that is not UTF-8 as U+FFFD (Python's "replace").
func decode(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var s strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		s.WriteRune(r)
		b = b[n:]
	}
	return s.String()
}

func rstrip(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) }
