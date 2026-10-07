package signal

// New (the Python never had Signal). The helper process, bridges/signal (everysaid-signal, AGPL,
// on presage): found, started, spoken to in JSON lines on its stdin and stdout, stopped. Its answers
// carry the id of the request; its events (a link code, a message, the queue drained) come without
// one and are handed, in order, to the handler the helper was started with.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// HelperName is the helper's program.
const HelperName = "everysaid-signal"

// HelperError is the helper's refusal: its code (not_open, not_linked, already_linked, bad_request,
// unknown_group, locked, failed) and what it said.
type HelperError struct{ Code, Msg string }

func (e *HelperError) Error() string { return e.Msg }

// ErrStopped: the helper is gone (it ended, or was stopped).
var ErrStopped = errors.New("the Signal helper stopped")

// FindHelper is the helper's path: the setting where given, else next to this program, else on the
// PATH; "" when there is none.
func FindHelper(setting string) string {
	name := HelperName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if setting != "" {
		if st, err := os.Stat(setting); err == nil && !st.IsDir() {
			return setting
		}
		return ""
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

type answer struct {
	ID     *uint64         `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Code   string          `json:"code"`
	Error  string          `json:"error"`
}

// Helper is a running helper.
type Helper struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	wmu   sync.Mutex

	pmu     sync.Mutex
	next    uint64
	pending map[uint64]chan answer
	stopped bool

	// events not yet handled, handed one at a time to handle
	qmu     sync.Mutex
	queue   [][]byte
	wake    chan struct{}
	settled *sync.Cond // signalled when the queue is empty and nothing is being handled
	busy    bool
	handle  func(name string, raw []byte)

	done    chan struct{}
	errDone chan struct{} // its stderr read to the end
	logs    func(line string)
}

// StartHelper starts the helper at path. handle gets each event (its name and its line); logs, the
// helper's own lines (its stderr).
func StartHelper(path string, handle func(name string, raw []byte), logs func(line string)) (*Helper, error) {
	cmd := exec.Command(path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	h := &Helper{cmd: cmd, stdin: stdin, pending: map[uint64]chan answer{}, wake: make(chan struct{}, 1),
		handle: handle, done: make(chan struct{}), errDone: make(chan struct{}), logs: logs}
	h.settled = sync.NewCond(&h.qmu)
	go h.read(stdout)
	go h.dispatch()
	go func() {
		defer close(h.errDone)
		r := bufio.NewReader(stderr)
		for {
			line, err := r.ReadString('\n')
			if h.logs != nil && strings.TrimSpace(line) != "" {
				h.logs(strings.TrimRight(line, "\r\n"))
			}
			if err != nil {
				return
			}
		}
	}()
	return h, nil
}

func (h *Helper) read(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			h.line(line)
		}
		if err != nil {
			break
		}
	}
	<-h.errDone
	h.cmd.Wait()
	h.pmu.Lock()
	h.stopped = true
	for id, ch := range h.pending {
		close(ch)
		delete(h.pending, id)
	}
	h.pmu.Unlock()
	close(h.done)
}

func (h *Helper) line(line []byte) {
	var head struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		if h.logs != nil {
			h.logs("not JSON from the helper: " + strings.TrimSpace(string(line)))
		}
		return
	}
	if head.Event != "" { // an event's own "id" (a group's, a call's) is not a request's
		h.qmu.Lock()
		h.queue = append(h.queue, line)
		h.qmu.Unlock()
		select {
		case h.wake <- struct{}{}:
		default:
		}
		return
	}
	var a answer
	if err := json.Unmarshal(line, &a); err != nil || a.ID == nil {
		if h.logs != nil && a.Error != "" {
			h.logs("the helper: " + a.Error)
		}
		return
	}
	h.pmu.Lock()
	ch := h.pending[*a.ID]
	delete(h.pending, *a.ID)
	h.pmu.Unlock()
	if ch != nil {
		ch <- a
	}
}

func (h *Helper) dispatch() {
	for {
		h.qmu.Lock()
		if len(h.queue) == 0 {
			h.busy = false
			h.settled.Broadcast()
			h.qmu.Unlock()
			select {
			case <-h.wake:
			case <-h.done: // the helper ended: what it said before is in the queue already
				h.qmu.Lock()
				empty := len(h.queue) == 0
				h.qmu.Unlock()
				if empty {
					return
				}
			}
			continue
		}
		line := h.queue[0]
		h.queue = h.queue[1:]
		h.busy = true
		h.qmu.Unlock()
		var e struct {
			Event string `json:"event"`
		}
		json.Unmarshal(line, &e)
		if h.handle != nil {
			h.handle(e.Event, line)
		}
	}
}

// Settle waits until every event received so far has been handled.
func (h *Helper) Settle() {
	h.qmu.Lock()
	for len(h.queue) > 0 || h.busy {
		h.settled.Wait()
	}
	h.qmu.Unlock()
}

// Call sends a request and waits for its answer (or ctx, or the helper's end).
func (h *Helper) Call(ctx context.Context, cmd string, args map[string]any) (json.RawMessage, error) {
	h.pmu.Lock()
	if h.stopped {
		h.pmu.Unlock()
		return nil, ErrStopped
	}
	h.next++
	id := h.next
	ch := make(chan answer, 1)
	h.pending[id] = ch
	h.pmu.Unlock()
	req := map[string]any{}
	for k, v := range args {
		req[k] = v
	}
	req["id"], req["cmd"] = id, cmd
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	h.wmu.Lock()
	_, err = h.stdin.Write(append(b, '\n'))
	h.wmu.Unlock()
	if err != nil {
		h.forget(id)
		return nil, ErrStopped
	}
	select {
	case a, ok := <-ch:
		if !ok {
			return nil, ErrStopped
		}
		if !a.OK {
			return nil, &HelperError{Code: a.Code, Msg: a.Error}
		}
		return a.Result, nil
	case <-ctx.Done():
		h.forget(id)
		return nil, ctx.Err()
	}
}

func (h *Helper) forget(id uint64) {
	h.pmu.Lock()
	delete(h.pending, id)
	h.pmu.Unlock()
}

// Done is closed once the helper has ended.
func (h *Helper) Done() <-chan struct{} { return h.done }

// Close asks the helper to end (what it is doing ends first, for a while), then stops it.
func (h *Helper) Close() {
	select {
	case <-h.done:
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	h.Call(ctx, "quit", nil)
	cancel()
	h.stdin.Close()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		h.cmd.Process.Kill()
		<-h.done
	}
}

// Kill stops the helper at once.
func (h *Helper) Kill() {
	h.cmd.Process.Kill()
	<-h.done
}

// Status is what the helper says of its account.
type Status struct {
	Open       bool   `json:"open"`
	Linked     bool   `json:"linked"`
	Receiving  bool   `json:"receiving"`
	ACI        string `json:"aci"`
	PNI        string `json:"pni"`
	Phone      string `json:"phone"`
	DeviceID   *int   `json:"device_id"`
	DeviceName string `json:"device_name"`
}

func decodeStatus(raw json.RawMessage) (Status, error) {
	var s Status
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("the helper's status: %w", err)
	}
	return s, nil
}
