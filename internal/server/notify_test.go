package server

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
)

// pushRecorder is a push service that takes every message and only counts them.
type pushRecorder struct {
	mu sync.Mutex
	n  int
}

func (p *pushRecorder) Do(r *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.n++
	p.mu.Unlock()
	return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}

func (p *pushRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

// New messages in an archived chat are shown as they come, but the chat stays archived and sends no
// notification; the other chat's do, unless already read (on another device: a phone's backup).
func TestAnArchivedChatStaysArchivedAndSaysNothingOfNewMessages(t *testing.T) {
	defer config.DeleteSecret("vapid-private")
	c := newServer(t)
	c.login()
	rec := &pushRecorder{}
	c.s.Push.httpClient = rec
	pushEndpointOK = func(context.Context, string) bool { return true }
	t.Cleanup(func() { pushEndpointOK = pushEndpoint })
	browser, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := M{"endpoint": "https://push.invalid/1", "keys": M{"p256dh": b64.EncodeToString(browser.PublicKey().Bytes()),
		"auth": b64.EncodeToString(randomBytes(16))}}
	must(t, c.post("/api/push/subscribe", sub).status == 200, "subscribe")

	s := c.s.Host.Store()
	q := s.Read()
	byChat := map[string]int64{} // two chats, one conversation of each
	var order []string
	for _, conv := range db.Ints(q, "SELECT id FROM conversation c WHERE NOT is_group AND EXISTS "+
		"(SELECT 1 FROM message m WHERE m.conversation_id = c.id AND NOT m.outgoing) ORDER BY id") {
		cid := core.ChatOfConversation(s, conv)
		if _, ok := byChat[cid]; cid != "" && !ok {
			byChat[cid] = conv
			order = append(order, cid)
		}
	}
	must(t, len(order) >= 2, "two chats with incoming messages: %d", len(order))
	archived, other := order[0], order[1]
	must(t, core.SetChatState(s, archived, false, map[string]core.StateValue{"archived": true}) == nil, "archive")
	first := db.Int(q, "SELECT max(id) FROM message")
	s.MustWrite(func(tx *sql.Tx) {
		for _, cid := range []string{archived, other} {
			db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) "+
				"SELECT service_id, conversation_id, ?, 0, sender_id, kind_id, 'hi' FROM message "+
				"WHERE conversation_id = ? AND NOT outgoing LIMIT 1", time.Now().UnixMilli(), byChat[cid])
		}
	})
	last := db.Int(s.Read(), "SELECT max(id) FROM message")

	ch := c.s.Host.Listen()
	defer c.s.Host.Unlisten(ch)
	c.s.Host.Emit(M{"type": "new", "messages": []int64{first, last}})
	var event M
	select {
	case event = <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	chats := event["chats"].(map[string]int64)
	must(t, len(chats) == 2 && chats[archived] == 1 && chats[other] == 1, "both shown as they come: %v", chats)
	must(t, core.States(s)[archived].Archived, "still archived")
	notes, _ := event["notify"].([]map[string]any)
	must(t, len(notes) == 1 && notes[0]["chat"] == other, "the event carries the other chat's notification: %v", event["notify"])
	deadline := time.Now().Add(5 * time.Second)
	for rec.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // a second one, were it sent, would be here by now
	must(t, rec.count() == 1, "only the other chat notifies: %d sent", rec.count())

	// one that comes late (a phone's backup), already read there: shown, but no notification
	early := time.Now().Add(-time.Hour).UnixMilli()
	must(t, core.SetChatState(s, other, false, map[string]core.StateValue{"read_until": float64(time.Now().UnixMilli())}) == nil, "read")
	before := db.Int(s.Read(), "SELECT max(id) FROM message")
	s.MustWrite(func(tx *sql.Tx) {
		db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) "+
			"SELECT service_id, conversation_id, ?, 0, sender_id, kind_id, 'read already' FROM message "+
			"WHERE conversation_id = ? AND NOT outgoing LIMIT 1", early, byChat[other])
	})
	c.s.Host.Emit(M{"type": "new", "messages": []int64{before, db.Int(s.Read(), "SELECT max(id) FROM message")}})
	select {
	case event = <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	must(t, event["chats"].(map[string]int64)[other] == 1, "shown: %v", event["chats"])
	must(t, event["notify"] == nil, "no notification of what was read: %v", event["notify"])
	time.Sleep(200 * time.Millisecond)
	must(t, rec.count() == 1, "no push of what was read: %d sent", rec.count())
}

// endpointRecorder is a push service that keeps which endpoints it was sent to.
type endpointRecorder struct {
	mu   sync.Mutex
	sent []string
}

func (p *endpointRecorder) Do(r *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.sent = append(p.sent, r.URL.String())
	p.mu.Unlock()
	return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}

func (p *endpointRecorder) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.sent
	p.sent = nil
	return out
}

// A device can leave out services (their own apps notify there already): it gets nothing of them,
// the other devices still do; nothing at all of a service the user hid. Only the user's own device's
// choice is read or changed.
func TestNotificationsByServicePerDevice(t *testing.T) {
	defer config.DeleteSecret("vapid-private")
	c := newServer(t)
	c.login()
	rec := &endpointRecorder{}
	c.s.Push.httpClient = rec
	pushEndpointOK = func(context.Context, string) bool { return true }
	t.Cleanup(func() { pushEndpointOK = pushEndpoint })
	for _, e := range []string{"https://push.invalid/phone", "https://push.invalid/desk"} {
		browser, _ := ecdh.P256().GenerateKey(rand.Reader)
		must(t, c.post("/api/push/subscribe", M{"endpoint": e, "keys": M{"p256dh": b64.EncodeToString(browser.PublicKey().Bytes()),
			"auth": b64.EncodeToString(randomBytes(16))}}).status == 200, "subscribe")
	}
	s := c.s.Host.Store()
	var conv int64
	var service string
	db.Row(s.Read(), "SELECT m.conversation_id, sv.name FROM message m JOIN service sv ON sv.id = m.service_id "+
		"JOIN conversation c ON c.id = m.conversation_id WHERE NOT m.outgoing AND NOT c.is_group ORDER BY m.id LIMIT 1", nil, &conv, &service)
	cid := core.ChatOfConversation(s, conv)
	must(t, cid != "" && !core.States(s)[cid].Muted && !core.States(s)[cid].Archived, "a chat that notifies: %s", cid)
	arrive := func() []string {
		before := db.Int(s.Read(), "SELECT max(id) FROM message")
		s.MustWrite(func(tx *sql.Tx) {
			db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) "+
				"SELECT service_id, conversation_id, ?, 0, sender_id, kind_id, 'hi' FROM message "+
				"WHERE conversation_id = ? AND NOT outgoing LIMIT 1", time.Now().UnixMilli(), conv)
		})
		ch := c.s.Host.Listen()
		defer c.s.Host.Unlisten(ch)
		c.s.Host.Emit(M{"type": "new", "messages": []int64{before, db.Int(s.Read(), "SELECT max(id) FROM message")}})
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		time.Sleep(300 * time.Millisecond)
		got := rec.take()
		sort.Strings(got)
		return got
	}
	must(t, len(arrive()) == 2, "both devices")

	must(t, c.do("PUT", "/api/push/services", M{"endpoint": "https://push.invalid/phone", "off": []string{service}}, H).status == 200, "leave out")
	must(t, c.getJSON("/api/push/services?endpoint=https://push.invalid/phone")["off"].([]any)[0] == service, "kept")
	got := arrive()
	must(t, len(got) == 1 && strings.HasSuffix(got[0], "/desk"), "the other device only: %v", got)
	must(t, c.get("/api/push/services?endpoint=https://push.invalid/someone-else").status == 404, "not a device of the user's")
	must(t, c.do("PUT", "/api/push/services", M{"endpoint": "https://push.invalid/someone-else", "off": []string{}}, H).status == 404,
		"not a device of the user's")

	must(t, c.do("PUT", "/api/settings", M{"hidden_services": []string{service}}, H).status == 200, "hide")
	must(t, len(arrive()) == 0, "nothing of a hidden service")
}
