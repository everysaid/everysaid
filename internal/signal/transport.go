package signal

// How the plugin speaks to Signal: a client (client.go, built with the tag `signal`: signalmeow in
// this process) answering commands and saying events, in the words of the protocol Everysaid's
// earlier helper spoke over a pipe (bridges/signal/README.md, kept in docs/data-sources.md): a
// command with its fields, an answer as JSON; an event, a JSON object with its name in `event`.
// The events are handed, one at a time and in order, to the handler the client was made with; an
// event Signal waits for (a message, before its acknowledgement) is answered only once the handler
// kept it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// transport is a client of Signal for one plugin instance.
type transport interface {
	// Call sends a command and waits for its answer (or ctx, or the client's end).
	Call(ctx context.Context, cmd string, args map[string]any) (json.RawMessage, error)
	// Settle waits until every event said so far has been handled.
	Settle()
	// Done is closed once the client has ended.
	Done() <-chan struct{}
	// Close ends the client, after what it is doing (for a while).
	Close()
	// Kill ends the client at once.
	Kill()
}

// clientConfig is what a client is made with: the plugin instance, the folder its downloaded files go
// to, the store (the messages a fetch again or an edit reads), the handler of its events, and a sink
// for its log.
type clientConfig struct {
	Instance int64
	Media    string
	Store    *Store
	Handle   func(name string, raw []byte) error
	Logs     func(line string)
}

// newClient makes the instance's client (client.go; nobuild.go where Signal is not built in; the
// tests' fake).
var newClient func(cfg clientConfig) (transport, error) = startClient

// forgetDevice removes an instance's device (its keys) from keys.db, once it is unlinked.
var forgetDevice func(instance int64) error = dropDevice

// ClientError is the client's refusal: its code (not_open, not_linked, already_linked, bad_request,
// unknown_group, failed) and what it said.
type ClientError struct{ Code, Msg string }

func (e *ClientError) Error() string { return e.Msg }

func refuse(code, msg string) error { return &ClientError{Code: code, Msg: msg} }

// ErrStopped: the client is gone (it ended, or was stopped).
var ErrStopped = errors.New("the Signal client stopped")

// Status is what the client says of its account.
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
		return s, fmt.Errorf("the client's status: %w", err)
	}
	return s, nil
}

// queued is an event waiting for the handler; answer, where someone waits for its handling.
type queued struct {
	raw    []byte
	name   string
	answer chan error
}

// eventQueue hands a client's events to its handler, one at a time, in the order they were said.
type eventQueue struct {
	handle func(name string, raw []byte) error

	mu      sync.Mutex
	queue   []queued
	wake    chan struct{}
	settled *sync.Cond // signalled when the queue is empty and nothing is being handled
	busy    bool
	done    chan struct{}
	ended   bool
}

func newEventQueue(handle func(name string, raw []byte) error) *eventQueue {
	q := &eventQueue{handle: handle, wake: make(chan struct{}, 1), done: make(chan struct{})}
	q.settled = sync.NewCond(&q.mu)
	go q.dispatch()
	return q
}

// encode is an event's line: its fields, with its name in "event".
func encode(name string, fields map[string]any) []byte {
	m := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		m[k] = v
	}
	m["event"] = name
	b, err := json.Marshal(m)
	if err != nil {
		panic(err) // the client's own values: always JSON
	}
	return b
}

func (q *eventQueue) push(it queued) bool {
	q.mu.Lock()
	if q.ended {
		q.mu.Unlock()
		return false
	}
	q.queue = append(q.queue, it)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

// say queues an event, not waiting for it.
func (q *eventQueue) say(name string, fields map[string]any) {
	q.push(queued{raw: encode(name, fields), name: name})
}

// sayAndWait queues an event and waits for its handling: the handler's error, or ErrStopped where
// the client ended first.
func (q *eventQueue) sayAndWait(name string, fields map[string]any) error {
	it := queued{raw: encode(name, fields), name: name, answer: make(chan error, 1)}
	if !q.push(it) {
		return ErrStopped
	}
	select {
	case err := <-it.answer:
		return err
	case <-q.done:
		select {
		case err := <-it.answer:
			return err
		default:
			return ErrStopped
		}
	}
}

func (q *eventQueue) dispatch() {
	for {
		q.mu.Lock()
		if len(q.queue) == 0 {
			q.busy = false
			q.settled.Broadcast()
			ended := q.ended
			q.mu.Unlock()
			if ended {
				close(q.done)
				return
			}
			<-q.wake
			continue
		}
		it := q.queue[0]
		q.queue = q.queue[1:]
		q.busy = true
		q.mu.Unlock()
		var err error
		if q.handle != nil {
			err = q.handle(it.name, it.raw)
		}
		if it.answer != nil {
			it.answer <- err
		}
	}
}

// Settle waits until every event said so far has been handled.
func (q *eventQueue) Settle() {
	q.mu.Lock()
	for len(q.queue) > 0 || q.busy {
		q.settled.Wait()
	}
	q.mu.Unlock()
}

// end takes no more events; those queued are still handled, then done is closed.
func (q *eventQueue) end() {
	q.mu.Lock()
	q.ended = true
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
