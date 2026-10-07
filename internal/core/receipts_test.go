package core_test

import (
	"database/sql"
	"testing"

	"everysaid/internal/core"
	"everysaid/internal/db"
)

// In a group, "all" is whoever got the user's messages there about then: one who left, or came
// later, is not waited for; a person by two addresses counts once.
func TestAllWhoGotItAreThoseTheServiceNamedThen(t *testing.T) {
	s := store(t)
	r := s.Read()
	var conv, mid, ts int64
	if !db.Row(r, "SELECT m.conversation_id, m.id, m.ts FROM message m JOIN conversation c ON c.id = m.conversation_id "+
		"JOIN receipt x ON x.message_id = m.id WHERE c.is_group AND m.outgoing "+
		"GROUP BY m.id HAVING count(*) > 2 ORDER BY m.ts DESC LIMIT 1", nil, &conv, &mid, &ts) {
		t.Fatal("the demo has no group message with more than two receipts")
	}
	members := db.Ints(r, "SELECT address_id FROM conversation_member WHERE conversation_id = ? "+
		"AND address_id NOT IN (SELECT address_id FROM account)", conv)
	gone := members[0]
	write(t, s, func(tx *sql.Tx) { // one never got anything there: as one who left
		db.Exec(tx, "DELETE FROM receipt WHERE address_id = ? AND message_id IN "+
			"(SELECT id FROM message WHERE conversation_id = ?)", gone, conv)
		db.Exec(tx, "UPDATE receipt SET read_at = coalesce(read_at, ?) WHERE message_id = ?", ts+1000, mid)
	})
	m := core.Hydrate(s, []core.Item{{Type: "m", ID: mid, TS: ts}}, nil)[0]
	rc := m["receipts"].(core.M)
	if rc["to"] != len(members)-1 || rc["read"] != rc["to"] {
		t.Fatalf("receipts %v, %d members besides the user", rc, len(members))
	}
	who := core.Receipts(s, mid)
	if len(who) != len(members)-1 {
		t.Fatalf("%d who got it, %d members besides the user", len(who), len(members))
	}
	for _, x := range who {
		if x["read_at"] == nil {
			t.Fatalf("not read: %v", x)
		}
	}
}
