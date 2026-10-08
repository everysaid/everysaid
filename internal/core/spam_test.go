package core_test

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
)

// spammer is someone of the demo archive who may be removed as spam, with messages of their own and,
// where wanted, in a group.
func spammer(t *testing.T, s *core.Store, inGroup bool) (int64, core.M) {
	t.Helper()
	for _, pid := range core.PeopleOf(s).PeopleOrder {
		c, _, err := core.SpamCheck(s, pid)
		if err != nil || c["refused"] != "" || i64(c["messages"]) == 0 || (inGroup && i64(c["groups"]) == 0) {
			continue
		}
		return pid, c
	}
	t.Skip("no one in the demo archive to remove as spam")
	return 0, nil
}

func TestRemoveSpam(t *testing.T) {
	s := store(t)
	pid, check := spammer(t, s, false)
	q := s.Read()
	addrs := core.PeopleOf(s).Addresses(pid)
	_, convs, _ := core.SpamCheck(s, pid)
	inGroups := db.Int(q, "SELECT count(*) FROM message m JOIN conversation c ON c.id = m.conversation_id "+
		"WHERE c.is_group AND m.sender_id IN ("+db.Marks(len(addrs))+")", db.Args(addrs)...)
	purged, err := core.RemoveSpam(s, pid)
	if err != nil {
		t.Fatal(err)
	}
	if int64(purged.Messages) != i64(check["messages"]) || int64(purged.Calls) != i64(check["calls"]) ||
		int64(purged.Conversations) != i64(check["chats"]) {
		t.Fatalf("removed %+v, the check said %v", purged, check)
	}
	for _, c := range convs {
		if db.Exists(q, "SELECT 1 FROM conversation WHERE id = ?", c.ID) || db.Exists(q, "SELECT 1 FROM message WHERE conversation_id = ?", c.ID) {
			t.Fatal("their chat is still there", c)
		}
	}
	in := "(" + db.Marks(len(addrs)) + ")"
	if db.Exists(q, "SELECT 1 FROM handle_name WHERE address_id IN "+in, db.Args(addrs)...) ||
		db.Exists(q, "SELECT 1 FROM call WHERE conversation_id IS NULL AND address_id IN "+in, db.Args(addrs)...) {
		t.Fatal("their names or calls are still there")
	}
	if n := db.Int(q, "SELECT count(*) FROM message m JOIN conversation c ON c.id = m.conversation_id "+
		"WHERE c.is_group AND m.sender_id IN "+in, db.Args(addrs)...); n != inGroups {
		t.Fatalf("their messages in groups: %d, were %d", n, inGroups)
	}
	if ids(chats(s, func(o *core.ChatsOptions) { o.IncludeArchived = true }))[fmt.Sprintf("p%d", pid)] {
		t.Fatal("their chat is still listed")
	}
	removed := core.SpamRemoved(s)
	if len(removed) != len(addrs) || removed[0]["name"] != check["name"] {
		t.Fatalf("removed: %v", removed)
	}

	// an import brings their messages again from the sources: removed again, the chat not made anew
	a, err := archive.Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	src := a.Source("test/spam", "", "test", "")
	h := core.PeopleOf(s).Handles[pid][0]
	handle := archive.H(h.Kind, h.Value, h.Service)
	service := h.Service
	if service == "" {
		service = "sms"
	}
	conv := a.Conversation(service, []archive.Handle{handle}, "", "")
	a.AddMessage(src, "again", archive.Message{Service: service, ConversationID: conv, TS: 1, SenderID: a.Address(handle),
		Kind: "text", Text: "again"})
	a.HandleName(handle, "telegram", "Spam Name", "profile", 0)
	a.Resolve()
	a.Commit()
	a.Close()
	if db.Exists(q, "SELECT 1 FROM message WHERE text = 'again'") || db.Exists(q, "SELECT 1 FROM conversation WHERE id = ?", conv) ||
		db.Exists(q, "SELECT 1 FROM handle_name WHERE name = 'Spam Name'") {
		t.Fatal("the import brought them back")
	}

	// restored: the next import keeps what it brings
	if err := core.RestoreSpam(s, addrs[0]); err != nil {
		t.Fatal(err)
	}
	if err := core.RestoreSpam(s, addrs[0]); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("restored twice:", err)
	}
	a, _ = archive.Open(s.Path)
	src = a.Source("test/spam", "", "test", "")
	conv = a.Conversation(service, []archive.Handle{handle}, "", "")
	a.AddMessage(src, "back", archive.Message{Service: service, ConversationID: conv, TS: 2, SenderID: a.Address(handle),
		Kind: "text", Text: "back"})
	a.Resolve()
	a.Commit()
	a.Close()
	if len(addrs) == 1 && !db.Exists(q, "SELECT 1 FROM message WHERE text = 'back'") {
		t.Fatal("restored, but the import did not keep what it brought")
	}
}

func TestSpamRefusedForSomeoneKnown(t *testing.T) {
	s := store(t)
	pid, _ := spammer(t, s, false)
	if err := core.SetPerson(s, pid, core.To("Someone I know"), core.Opt[string]{}, core.Opt[string]{}); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := core.SpamCheck(s, pid); c["refused"] != "named" {
		t.Fatal(c["refused"])
	}
	_, err := core.RemoveSpam(s, pid)
	var ue *errs.UserError
	if !errors.As(err, &ue) || ue.Code != "spam.named" {
		t.Fatal(err)
	}
	if len(core.SpamRemoved(s)) != 0 {
		t.Fatal("removed all the same")
	}
}

func TestSpamSuggestions(t *testing.T) {
	s := store(t)
	pid, _ := spammer(t, s, false)
	aid := core.PeopleOf(s).Addresses(pid)[0]
	write(t, s, func(tx *sql.Tx) { db.Exec(tx, "INSERT INTO blocked (address_id, phone) VALUES (?, 'telegram')", aid) })
	got := core.SpamSuggestions(s)
	if len(got) != 1 || i64(got[0]["person_id"]) != pid || fmt.Sprint(got[0]["where"]) != "[telegram]" {
		t.Fatalf("suggestions: %v", got)
	}
	if err := core.NotSpam(s, pid); err != nil {
		t.Fatal(err)
	}
	if got := core.SpamSuggestions(s); len(got) != 0 {
		t.Fatalf("suggested after the user said no: %v", got)
	}
	if len(core.SpamRemoved(s)) != 0 {
		t.Fatal("kept counted as removed")
	}
}

func TestRemoveSpamKeepsWhatTheyWroteInGroups(t *testing.T) {
	s := store(t)
	pid, check := spammer(t, s, true)
	addrs := core.PeopleOf(s).Addresses(pid)
	in := "(" + db.Marks(len(addrs)) + ")"
	count := func() int64 {
		return db.Int(s.Read(), "SELECT count(*) FROM message m JOIN conversation c ON c.id = m.conversation_id "+
			"WHERE c.is_group AND m.sender_id IN "+in, db.Args(addrs)...)
	}
	before := count()
	if _, err := core.RemoveSpam(s, pid); err != nil {
		t.Fatal(err)
	}
	if after := count(); after != before || before == 0 {
		t.Fatalf("their messages in %d groups: %d, were %d", i64(check["groups"]), after, before)
	}
	if core.Person(s, pid) == nil {
		t.Fatal("the person who wrote in groups is gone")
	}
}
