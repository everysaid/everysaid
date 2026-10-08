package viber

// The client of Everysaid's Viber bridge (bridges/viber): a library loaded into the running Viber
// Desktop, answering one-line commands on a Unix socket (its README has the protocol).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultSocket is where the bridge listens unless told otherwise (VIBER_BRIDGE_SOCK).
func DefaultSocket() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "viber-bridge.sock")
	}
	return "/tmp/viber-bridge.sock"
}

// ErrNotRunning: nothing answers on the socket (Viber Desktop not running, or without the bridge).
var ErrNotRunning = errors.New("viber bridge not running")

// BridgeError is the bridge's refusal ("error <what>").
type BridgeError struct{ What string }

func (e *BridgeError) Error() string { return "viber bridge: " + e.What }

// call sends one command and reads the answer to its end (the bridge closes the connection).
func call(ctx context.Context, sock, line string, timeout time.Duration) (string, error) {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return "", ErrNotRunning
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(conn, 64<<20))
	if err != nil {
		return "", err
	}
	out := string(b)
	if strings.HasPrefix(out, "error ") {
		return "", &BridgeError{strings.TrimSpace(strings.TrimPrefix(out, "error "))}
	}
	return out, nil
}

// acting: one action at a time per bridge. Viber runs the bridge's commands on its main thread, but
// compose lets Qt's events run while it opens the chat and types, and a command waiting is one of
// them: it would run inside the compose (another compose typing into the same input, a snapshot
// taking the time the chat has to open).
var acting sync.Map

// act is a command whose answer is "ok".
func act(ctx context.Context, sock, line string) error {
	l, _ := acting.LoadOrStore(sock, &sync.Mutex{})
	l.(*sync.Mutex).Lock()
	defer l.(*sync.Mutex).Unlock()
	out, err := call(ctx, sock, line, 30*time.Second)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(out, "ok") {
		return &BridgeError{strings.TrimSpace(out)}
	}
	return nil
}

// escapeLine writes text on one line as the bridge reads it back (\n, \t, \\).
func escapeLine(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\t", `\t`, "\r", "").Replace(s)
}

// unescapeLine reads back a column the bridge wrote on one line.
func unescapeLine(s string) string {
	return strings.NewReplacer(`\\`, `\`, `\n`, "\n", `\t`, "\t").Replace(s)
}

// event is a row of Viber's Events (with its message's body and info) as the bridge streams it.
type event struct {
	ID, Chat   int64
	Outgoing   bool
	Type       int
	TS         int64 // Unix ms
	Token      string
	Body, Info string
}

// parseEvent reads a line of `events` or `subscribe`: EventID, ChatID, ContactID, Direction, Type,
// TimeStamp, Token, IsRead, Body, Info.
func parseEvent(line string) (event, bool) {
	f := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
	if len(f) != 10 {
		return event{}, false
	}
	id, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return event{}, false
	}
	chat, _ := strconv.ParseInt(f[1], 10, 64)
	typ, _ := strconv.Atoi(f[4])
	ts, _ := strconv.ParseInt(f[5], 10, 64)
	return event{ID: id, Chat: chat, Outgoing: f[3] == "1", Type: typ, TS: ts, Token: f[6],
		Body: unescapeLine(f[8]), Info: unescapeLine(f[9])}, true
}

// part is a piece of what compose types: text, or a mention of a Viber contact.
type part struct {
	Text    *string `json:"text,omitempty"`
	Mention int64   `json:"mention,omitempty"`
}

type composition struct {
	Chat  int64  `json:"chat"`
	Reply int64  `json:"reply,omitempty"`
	Edit  int64  `json:"edit,omitempty"`
	Parts []part `json:"parts"`
}

func compose(ctx context.Context, sock string, c composition) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return act(ctx, sock, "compose "+string(b))
}

// subscribe calls each with the events Viber adds (a new message, a reaction, an edit), until ctx
// ends or the bridge goes away; ready, once the bridge has said it is subscribed.
func subscribe(ctx context.Context, sock string, ready func(), each func(event)) error {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return ErrNotRunning
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	if _, err := io.WriteString(conn, "subscribe\n"); err != nil {
		return err
	}
	r := bufio.NewReaderSize(conn, 1<<20)
	first := true
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("the Viber bridge closed the connection: %w", err)
		}
		if first {
			first = false
			if strings.TrimSpace(line) != "subscribed" {
				return &BridgeError{strings.TrimSpace(line)}
			}
			ready()
			continue
		}
		if e, ok := parseEvent(line); ok {
			each(e)
		}
	}
}
