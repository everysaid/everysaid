package phones

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type logged struct {
	mu       sync.Mutex
	lines    []string
	progress []string
}

func (l *logged) terminal() *Terminal {
	return NewTerminal(func(line string, redrawn bool) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if redrawn {
			line = "~" + line
		}
		l.lines = append(l.lines, line)
	}, func(line string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.progress = append(l.progress, line)
	})
}

func TestTerminalLines(t *testing.T) {
	var l logged
	term := l.terminal()
	// lines in pieces, a CRLF, a blank line, a bar drawn three times then ended
	for _, chunk := range []string{"Backup: start", "ing\nsecond\r\n\n", "[=   ] 10%\r[==  ] 50%", "\r[====] 100%", "\nafter\n", "last"} {
		term.Write([]byte(chunk))
	}
	term.Close()
	want := []string{"Backup: starting", "second", "~[====] 100%", "after", "last"}
	if strings.Join(l.lines, "|") != strings.Join(want, "|") {
		t.Errorf("lines %q", l.lines)
	}
	if len(l.progress) == 0 || l.progress[0] != "[==  ] 50%" {
		t.Errorf("progress %q", l.progress)
	}
}

func TestTerminalLastState(t *testing.T) {
	var l logged
	term := l.terminal()
	term.Write([]byte("a\r"))
	term.Write([]byte("b\r")) // too soon after a: drawn once 0.2 s have passed
	time.Sleep(400 * time.Millisecond)
	l.mu.Lock()
	got := strings.Join(l.progress, "|")
	l.mu.Unlock()
	if got != "a|b" {
		t.Errorf("progress %q", got)
	}
	term.Close()
}

func TestTerminalInvalidUTF8(t *testing.T) {
	var l logged
	term := l.terminal()
	term.Write([]byte("x\xff\xfey\n"))
	if len(l.lines) != 1 || l.lines[0] != "x��y" {
		t.Errorf("%q", l.lines)
	}
}

func TestArgs(t *testing.T) {
	ap := NewArgs("prog", "What it does.")
	out := ap.String("-o,--out", "OUT", "def", "where")
	flag := ap.Bool("--flag", "a switch")
	only := ap.List("--only", "NAME", "some")
	depth := ap.Int("--depth", "N", 3, "how deep")
	pos := ap.Pos("domain", "", "a pattern", false)
	opt := ap.Pos("path", "%", "another", true)
	if err := ap.Parse([]string{"%viber%", "-oX", "--only", "a", "b", "--flag", "--depth=-2"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if *out != "X" || !*flag || strings.Join(*only, ",") != "a,b" || *depth != -2 || *pos != "%viber%" || *opt != "%" {
		t.Errorf("%q %v %q %d %q %q", *out, *flag, *only, *depth, *pos, *opt)
	}

	ap = NewArgs("prog", "What it does.")
	ap.String("-o,--out", "OUT", "", "where")
	ap.Pos("domain", "", "a pattern", false)
	var f *Failure
	err := ap.Parse([]string{"--out"}, &bytes.Buffer{})
	if f, _ = err.(*Failure); f == nil || f.Code != 2 || !strings.Contains(f.Text, "expected one argument") {
		t.Errorf("--out alone: %v", err)
	}
	err = ap.Parse(nil, &bytes.Buffer{})
	if f, _ = err.(*Failure); f == nil || !strings.Contains(f.Text, "the following arguments are required: domain") {
		t.Errorf("no domain: %v", err)
	}
	var help bytes.Buffer
	err = ap.Parse([]string{"-h"}, &help)
	if f, _ = err.(*Failure); f == nil || f.Code != 0 || !strings.Contains(help.String(), "usage: prog") {
		t.Errorf("help: %v %s", err, help.String())
	}
}

func TestToolMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Tool("adb")
	f, _ := err.(*Failure)
	if f == nil || f.Error() != "adb (Android platform-tools) is needed and was not found in PATH." {
		t.Errorf("%v", err)
	}
	if f.In("el") != "Χρειάζεται το adb (Android platform-tools) και δεν βρέθηκε στο PATH." {
		t.Errorf("%q", f.In("el"))
	}
}
