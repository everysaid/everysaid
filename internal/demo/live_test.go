package demo

import (
	"context"
	"sync"
	"testing"
	"time"

	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

type host struct {
	store  *core.Store
	mu     sync.Mutex
	events []core.M
	lock   sync.Mutex
}

func (h *host) Store() *core.Store       { return h.store }
func (h *host) Alert(title, body string) {}
func (h *host) ImportLock() sync.Locker  { return &h.lock }
func (h *host) Emit(e core.M) {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
}

func demoHost(t *testing.T) *host {
	_, path := Build(t)
	s, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return &host{store: s}
}

func TestMessageAndReceipt(t *testing.T) {
	h := demoHost(t)
	r := h.store.Read()
	conv := db.Int(r, "SELECT c.id FROM conversation c JOIN service s ON s.id = c.service_id "+
		"WHERE s.name = 'whatsapp' AND c.is_group ORDER BY c.id LIMIT 1")
	member := db.Int(r, "SELECT address_id FROM conversation_member WHERE conversation_id = ? ORDER BY address_id LIMIT 1", conv)
	before := db.Int(r, "SELECT max(id) FROM message")
	var replyKey string
	db.Row(r, "SELECT key FROM message WHERE conversation_id = ? ORDER BY id LIMIT 1", []any{conv}, &replyKey)
	text := "Γεια @Κάποιος, δες"
	mid, err := Message(h, conv, text, true, replyKey, []plugins.Mention{{Start: 5, Length: 8, AddressID: member}},
		&plugins.File{Data: []byte("not really a picture"), Filename: "Photo.JPG", MimeType: "image/jpeg"})
	if err != nil {
		t.Fatal(err)
	}
	if mid != before+1 {
		t.Fatalf("id %d, want %d", mid, before+1)
	}
	var kind, key, token, rel string
	var outgoing int
	var sender any
	db.Row(r, "SELECT k.name, m.key, m.outgoing, m.sender_id FROM message m JOIN message_kind k ON k.id = m.kind_id WHERE m.id = ?",
		[]any{mid}, &kind, &key, &outgoing, &sender)
	if kind != "image" || outgoing != 1 || sender != nil || len(key) < len("demo-live-") || key[:10] != "demo-live-" {
		t.Errorf("message: %s %s %d %v", kind, key, outgoing, sender)
	}
	db.Row(r, "SELECT token FROM mention WHERE message_id = ?", []any{mid}, &token)
	if token != "@Κάποιος" {
		t.Errorf("mention %q (characters, not bytes)", token)
	}
	db.Row(r, "SELECT source_path FROM attachment WHERE message_id = ?", []any{mid}, &rel)
	if len(rel) < 5 || rel[len(rel)-4:] != ".jpg" {
		t.Errorf("attachment %q", rel)
	}
	if !db.Exists(r, "SELECT 1 FROM message WHERE id = ? AND reply_to IS NOT NULL", mid) {
		t.Error("the reply is not linked to its message")
	}
	if len(h.events) != 1 || h.events[0]["type"] != "new" {
		t.Fatalf("events %v", h.events)
	}

	if err := Receipt(h, mid, "read"); err != nil {
		t.Fatal(err)
	}
	members := db.Int(r, "SELECT count(*) FROM conversation_member WHERE conversation_id = ? AND "+
		"address_id NOT IN (SELECT address_id FROM account)", conv)
	if n := db.Int(r, "SELECT count(*) FROM receipt WHERE message_id = ? AND read_at IS NOT NULL", mid); n != members || n == 0 {
		t.Errorf("%d read, want %d", n, members)
	}
	if err := Receipt(h, mid, "x; DROP TABLE receipt"); err == nil {
		t.Error("a receipt field is only delivered or read")
	}
}

func TestSenderSendsAndHearsBack(t *testing.T) {
	h := demoHost(t)
	r := h.store.Read()
	var conv plugins.Conversation
	db.Row(r, "SELECT c.id, s.name FROM conversation c JOIN service s ON s.id = c.service_id "+
		"WHERE s.name = 'telegram' AND NOT c.is_group ORDER BY c.id LIMIT 1", nil, &conv.ID, &conv.Service)
	c := plugins.NewContext(h, plugins.Instance{ID: 1, Plugin: "demo-sender", Kind: "source"})
	out, err := Sender{}.Send(context.Background(), c, conv, "hello", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mid := out.(plugins.Sent).IDs[0]
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		answered := db.Exists(r, "SELECT 1 FROM message WHERE conversation_id = ? AND text = '↩ hello' AND NOT outgoing", conv.ID)
		read := db.Exists(r, "SELECT 1 FROM receipt WHERE message_id = ? AND read_at IS NOT NULL", mid)
		if answered && read {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no answer, or no read receipt, within 10 s")
}

func TestSplitext(t *testing.T) {
	for in, want := range map[string]string{"a.JPG": ".JPG", "x/b.tar.gz": ".gz", ".bashrc": "", "..x": "", "noext": "",
		"dir.d/file": "", "": ""} {
		if got := splitext(in); got != want {
			t.Errorf("splitext(%q) = %q, want %q", in, got, want)
		}
	}
}
