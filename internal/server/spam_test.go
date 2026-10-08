package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

// newSpammer adds to the archive someone unknown who wrote on Telegram and Viber; it returns their person.
func newSpammer(t *testing.T, c *client, n int) int64 {
	t.Helper()
	a, err := archive.Open(c.s.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	src := a.Source("test/spam", "", "test", "")
	tg := archive.H("id", fmt.Sprint(900000+n), "telegram")
	vb := archive.H("id", fmt.Sprintf("viber-%d", n), "viber")
	a.Alias(vb, tg)
	for i, h := range []archive.Handle{tg, vb} {
		conv := a.Conversation(h.Service, []archive.Handle{h}, h.Value, "")
		a.AddMessage(src, fmt.Sprintf("%d-%d", n, i), archive.Message{Service: h.Service, ConversationID: conv, TS: int64(1000 + i),
			SenderID: a.Address(h), Kind: "text", Text: "a prize for you", Key: fmt.Sprintf("spam-%d-%d", n, i)})
	}
	a.HandleName(tg, "telegram", fmt.Sprintf("Lisa %d", n), "profile", 0)
	a.Resolve()
	a.Commit()
	return a.Int("SELECT person_id FROM person_address WHERE address_id = ?", a.Address(tg))
}

// spamSource reports to Telegram and Viber (Viber refuses), and drops its copy of the chats.
type spamSource struct{}

var spamSeen struct {
	sync.Mutex
	reported, forgotten []string
}

func (spamSource) Info() *plugins.Info {
	return &plugins.Info{ID: "test-spam", Name: "Test spam", Kind: "source", Services: []string{"telegram", "viber"},
		CanReportSpam: true}
}

func (spamSource) ReportSpam(ctx context.Context, c *plugins.Context, conv plugins.Conversation) error {
	if conv.Service == "viber" {
		return errs.Plugin("Viber refuses", 0)
	}
	spamSeen.Lock()
	defer spamSeen.Unlock()
	spamSeen.reported = append(spamSeen.reported, conv.Service)
	return nil
}

func (spamSource) Forget(c *plugins.Context, conv plugins.Conversation) error {
	spamSeen.Lock()
	defer spamSeen.Unlock()
	spamSeen.forgotten = append(spamSeen.forgotten, conv.Service)
	return nil
}

// Someone removed as spam: the check says what goes and where it can be reported; the removal
// reports where it can (a refusal said, the rest done), each source drops its copy, and the chat
// leaves the app, to come back only when restored.
func TestRemoveAsSpam(t *testing.T) {
	c := newServer(t)
	c.login()
	plugins.Register(spamSource{})
	plugins.Create(c.s.Store, "test-spam", "Spam", M{})
	pid := newSpammer(t, c, 1)
	check := c.getJSON(fmt.Sprintf("/api/people/%d/spam", pid))
	must(t, fmt.Sprint(check["reportable"]) == "[telegram viber]" && num(check["messages"]) == 2 && check["name"] == "Lisa 1",
		"check: %v", check)

	r := c.post(fmt.Sprintf("/api/people/%d/spam", pid), M{"report": true})
	must(t, r.status == 200, "remove: %d %s", r.status, r.body)
	out := r.json()
	must(t, fmt.Sprint(out["reported"]) == "[telegram]" && num(out["messages"]) == num(check["messages"]), "removed: %v", out)
	failed, _ := out["failed"].([]any)
	must(t, len(failed) == 1 && strings.Contains(fmt.Sprint(failed[0]), "Viber refuses"), "failed: %v", out["failed"])
	spamSeen.Lock()
	must(t, fmt.Sprint(spamSeen.forgotten) == "[telegram viber]", "forgotten: %v", spamSeen.forgotten)
	spamSeen.Unlock()
	must(t, c.get(fmt.Sprintf("/api/chats/p%d/stream", pid)).status == 404 ||
		len(items(c.getJSON(fmt.Sprintf("/api/chats/p%d/stream", pid)))) == 0, "their chat is still there")

	list := c.getJSON("/api/spam")
	removed := list["removed"].([]any)
	must(t, len(removed) == 2 && removed[0].(map[string]any)["name"] == "Lisa 1", "removed: %v", removed)
	aid := num(removed[0].(map[string]any)["address_id"])
	must(t, c.post(fmt.Sprintf("/api/spam/%d/restore", aid), nil).status == 200, "restore")
	must(t, len(c.getJSON("/api/spam")["removed"].([]any)) == 1, "still listed")
	audit := fmt.Sprint(c.getJSON("/api/auth/account")["audit"])
	must(t, strings.Contains(audit, "removed as spam") && strings.Contains(audit, "restored from spam"), "audit: %s", audit)
}

// Someone the user named is not removed, and a removal without asking reports nothing.
func TestSpamOnlyForStrangers(t *testing.T) {
	c := newServer(t)
	c.login()
	named, stranger := newSpammer(t, c, 1), newSpammer(t, c, 2)
	must(t, c.do("PATCH", fmt.Sprintf("/api/people/%d", named), M{"name": "Known"}, H).status == 200, "name")
	r := c.post(fmt.Sprintf("/api/people/%d/spam", named), M{"report": false})
	must(t, r.status == 409 && r.code() == "spam.named", "named: %d %s", r.status, r.body)

	r = c.post(fmt.Sprintf("/api/people/%d/spam", stranger), M{"report": false})
	must(t, r.status == 200 && len(r.json()["reported"].([]any)) == 0, "without reporting: %d %s", r.status, r.body)
	must(t, c.post(fmt.Sprintf("/api/people/%d/not-spam", named), nil).status == 200, "not spam")
}
