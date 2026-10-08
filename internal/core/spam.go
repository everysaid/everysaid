// Spam: someone the user does not know who wrote or called to sell, cheat or annoy. The user removes
// them from the archive (archive.PurgeSpam: their own chats and calls, their names; what they wrote
// in groups stays), and nothing of theirs comes back with the next import. Only people the user has
// not named and no contact lists may be removed: one tap must not take someone they know. People
// blocked on a phone or a service are suggested for it, until the user says they are not spam.
package core

import (
	"database/sql"
	"sort"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/errs"
)

// SpamConversation is one of the conversations removing someone as spam takes.
type SpamConversation struct {
	ID           int64
	Service, Key string
}

// spamAllowed is why the person may not be removed as spam ("" when they may): "me", "named" (the
// user named them), "contact" (an address book lists them).
func spamAllowed(ppl *People, pid int64) string {
	switch {
	case ppl.Me[pid]:
		return "me"
	case ppl.Given[pid] != "":
		return "named"
	case ppl.Contact(pid) != nil:
		return "contact"
	}
	return ""
}

// SpamCheck is what removing the person as spam would remove: {person_id, name, refused (why not,
// "" when allowed), messages, calls, chats, files, groups (the group chats where what they wrote
// stays), services}; and those conversations.
func SpamCheck(s *Store, pid int64) (M, []SpamConversation, error) {
	ppl := PeopleOf(s)
	if _, ok := ppl.Handles[pid]; !ok {
		return nil, nil, ErrNotFound
	}
	q := s.Read()
	addrs := ppl.Addresses(pid)
	convIDs, calls := archive.SpamScope(q, addrs)
	var convs []SpamConversation
	services := []string{}
	seen := map[string]bool{}
	var messages, files int64
	for _, id := range convIDs {
		c := SpamConversation{ID: id}
		db.Row(q, "SELECT s.name, c.key FROM conversation c JOIN service s ON s.id = c.service_id WHERE c.id = ?",
			[]any{id}, &c.Service, &c.Key)
		convs = append(convs, c)
		if !seen[c.Service] {
			seen[c.Service] = true
			services = append(services, c.Service)
		}
		messages += db.Int(q, "SELECT count(*) FROM message WHERE conversation_id = ?", id)
		files += db.Int(q, "SELECT count(DISTINCT a.sha256) FROM attachment a JOIN message m ON m.id = a.message_id "+
			"WHERE m.conversation_id = ?", id)
	}
	sort.Strings(services)
	var groups int64
	if len(addrs) > 0 {
		groups = db.Int(q, "SELECT count(DISTINCT cm.conversation_id) FROM conversation_member cm JOIN conversation c "+
			"ON c.id = cm.conversation_id WHERE c.is_group AND cm.address_id IN ("+db.Marks(len(addrs))+")", db.Args(addrs)...)
	}
	return M{"person_id": pid, "name": ppl.Name(pid), "refused": spamAllowed(ppl, pid), "messages": messages,
		"calls": int64(len(calls)), "chats": int64(len(convs)), "files": files, "groups": groups,
		"services": services}, convs, nil
}

// RemoveSpam removes the person as spam: each of their handles is recorded so, and what belongs to
// them goes (archive.PurgeSpam), with the media files only they used.
func RemoveSpam(s *Store, pid int64) (archive.Purged, error) {
	var out archive.Purged
	ppl := PeopleOf(s)
	if _, ok := ppl.Handles[pid]; !ok {
		return out, ErrNotFound
	}
	if why := spamAllowed(ppl, pid); why != "" {
		return out, errs.New("spam."+why, 409, nil)
	}
	name := ppl.Name(pid)
	addrs := ppl.Addresses(pid)
	err := s.Write(func(tx *sql.Tx) error {
		now := time.Now().Unix()
		for _, aid := range addrs {
			db.Exec(tx, "INSERT INTO spam (address_id, decision, name, at) VALUES (?, 'removed', ?, ?) "+
				"ON CONFLICT DO UPDATE SET decision = 'removed', name = excluded.name, at = excluded.at", aid, name, now)
		}
		out = archive.PurgeSpam(tx, addrs)
		return nil
	})
	if err == nil {
		archive.RemoveMediaFiles(out.Files)
	}
	return out, err
}

// NotSpam: the user says the person is not spam; they are not suggested for removal again.
func NotSpam(s *Store, pid int64) error {
	ppl := PeopleOf(s)
	if _, ok := ppl.Handles[pid]; !ok {
		return ErrNotFound
	}
	return s.Write(func(tx *sql.Tx) error {
		now := time.Now().Unix()
		for _, aid := range ppl.Addresses(pid) {
			db.Exec(tx, "INSERT OR IGNORE INTO spam (address_id, decision, at) VALUES (?, 'kept', ?)", aid, now)
		}
		return nil
	})
}

// SpamRemoved is the handles removed as spam, newest first: [{address_id, label, kind, service, name, at}].
func SpamRemoved(s *Store) []M {
	out := []M{}
	db.Each(s.Read(), "SELECT x.address_id, k.name, a.value, s.name, x.name, x.at FROM spam x JOIN address a ON a.id = x.address_id "+
		"JOIN address_kind k ON k.id = a.kind_id LEFT JOIN service s ON s.id = a.service_id "+
		"WHERE x.decision = 'removed' ORDER BY x.at DESC, x.address_id", nil, func(scan func(...any)) {
		var aid, at int64
		var kind, value string
		var service, name sql.NullString
		scan(&aid, &kind, &value, &service, &name, &at)
		out = append(out, M{"address_id": aid, "label": Pretty(kind, value), "kind": kind, "service": nullable(service.String),
			"name": nullable(name.String), "at": at})
	})
	return out
}

// RestoreSpam: a handle removed as spam by mistake may come back: what the sources still have of it
// returns with their next import.
func RestoreSpam(s *Store, addressID int64) error {
	return s.Write(func(tx *sql.Tx) error {
		if db.Changed(tx, "DELETE FROM spam WHERE address_id = ? AND decision = 'removed'", addressID) == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SpamSuggestions is the people blocked on a phone or a service whom the user may remove as spam:
// not named, in no contact, with something in the archive, and not said either way yet:
// [{person_id, name, where}] (where: the devices and services they are blocked on).
func SpamSuggestions(s *Store) []M {
	ppl := PeopleOf(s)
	act := active(s)
	q := s.Read()
	decided := map[int64]bool{}
	for _, aid := range db.Ints(q, "SELECT address_id FROM spam") {
		decided[ppl.PersonOf[aid]] = true
	}
	where := map[int64][]string{}
	var order []int64
	db.Each(q, "SELECT address_id, phone FROM blocked ORDER BY rowid", nil, func(scan func(...any)) {
		var aid int64
		var w string
		scan(&aid, &w)
		pid, ok := ppl.PersonOf[aid]
		if !ok || decided[pid] || !act[pid] || spamAllowed(ppl, pid) != "" {
			return
		}
		if _, ok := where[pid]; !ok {
			order = append(order, pid)
		}
		for _, x := range where[pid] {
			if x == w {
				return
			}
		}
		where[pid] = append(where[pid], w)
	})
	out := []M{}
	for _, pid := range order {
		out = append(out, M{"person_id": pid, "name": ppl.Name(pid), "where": where[pid]})
	}
	return out
}
