package archive

import (
	"os"
	"path/filepath"

	"everysaid/internal/db"
)

// Spam: a handle the user removed as spam (`spam`, decision 'removed') loses its one-to-one
// conversations, with every message in them and what hangs on those, its calls (a group's stay), and
// the names the services showed for it. What it wrote in groups stays, as do the address and its
// person: the address is how the next import knows it. The sources still have what was removed, so
// every import removes it again (Resolve, and the call importers at their end).

// Purged is what PurgeSpam removed; Files: the media files no longer used, relative to the media
// root, for the caller to delete once committed.
type Purged struct {
	Messages, Calls, Conversations int
	Files                          []string
}

// PurgeSpam removes what belongs to the handles removed as spam (only: just these addresses; nil,
// all).
func PurgeSpam(q db.Querier, only []int64) (out Purged) {
	query, args := "SELECT address_id FROM spam WHERE decision = 'removed' "+
		"AND address_id NOT IN (SELECT address_id FROM account)", []any(nil)
	if only != nil {
		if len(only) == 0 {
			return out
		}
		query += " AND address_id IN (" + db.Marks(len(only)) + ")"
		args = db.Args(only)
	}
	addrs := db.Ints(q, query, args...)
	if len(addrs) == 0 {
		return out
	}
	in := "(" + db.Marks(len(addrs)) + ")"
	convs, calls := SpamScope(q, addrs)
	var msgs []int64
	chunked(convs, func(marks string, ids []any) {
		msgs = append(msgs, db.Ints(q, "SELECT id FROM message WHERE conversation_id IN "+marks, ids...)...)
		// message_origin has no index by message: one pass over it for these chats' messages
		db.Exec(q, "DELETE FROM message_origin WHERE message_id IN "+
			"(SELECT id FROM message WHERE conversation_id IN "+marks+")", ids...)
	})
	shas := map[string]bool{}
	chunked(msgs, func(marks string, ids []any) {
		for _, s := range db.Strs(q, "SELECT DISTINCT sha256 FROM attachment WHERE message_id IN "+marks, ids...) {
			shas[s] = true
		}
		db.Exec(q, "UPDATE message SET reply_to = NULL WHERE reply_to IN "+marks, ids...)
		for _, t := range []string{"reaction", "mention", "receipt", "attachment", "notice"} {
			db.Exec(q, "DELETE FROM "+t+" WHERE message_id IN "+marks, ids...)
		}
		db.Exec(q, "DELETE FROM message_fts WHERE rowid IN "+marks, ids...)
		db.Exec(q, "DELETE FROM message WHERE id IN "+marks, ids...)
	})
	chunked(calls, func(marks string, ids []any) {
		db.Exec(q, "DELETE FROM call_member WHERE call_id IN "+marks, ids...)
		db.Exec(q, "DELETE FROM call_origin WHERE call_id IN "+marks, ids...)
		db.Exec(q, "DELETE FROM call WHERE id IN "+marks, ids...)
	})
	chunked(convs, func(marks string, ids []any) {
		for _, t := range []string{"state_report", "conversation_member"} {
			db.Exec(q, "DELETE FROM "+t+" WHERE conversation_id IN "+marks, ids...)
		}
		db.Exec(q, "DELETE FROM chat_state WHERE chat IN (SELECT 'c' || id FROM conversation WHERE id IN "+marks+")", ids...)
		db.Exec(q, "DELETE FROM conversation WHERE id IN "+marks, ids...)
	})
	db.Exec(q, "DELETE FROM handle_name WHERE address_id IN "+in, db.Args(addrs)...)
	// people who have nothing but such handles: what the app kept of their chat goes too
	people := db.Ints(q, "SELECT DISTINCT person_id FROM person_address pa WHERE address_id IN "+in+
		" AND NOT EXISTS (SELECT 1 FROM person_address o WHERE o.person_id = pa.person_id AND o.address_id NOT IN "+
		"(SELECT address_id FROM spam WHERE decision = 'removed'))", db.Args(addrs)...)
	chunked(people, func(marks string, ids []any) {
		for _, t := range []string{"name_guess", "person_label", "analysis"} {
			db.Exec(q, "DELETE FROM "+t+" WHERE person_id IN "+marks, ids...)
		}
		db.Exec(q, "DELETE FROM chat_state WHERE chat IN (SELECT 'p' || id FROM person WHERE id IN "+marks+")", ids...)
	})
	for sha := range shas {
		var path string
		if db.Row(q, "SELECT path FROM media WHERE sha256 = ? AND NOT EXISTS (SELECT 1 FROM attachment WHERE sha256 = ?) "+
			"AND NOT EXISTS (SELECT 1 FROM library_link WHERE sha256 = ?) "+
			"AND NOT EXISTS (SELECT 1 FROM media_same WHERE sha256 = ? OR same_as = ?)",
			[]any{sha, sha, sha, sha, sha}, &path) {
			db.Exec(q, "DELETE FROM media_decision WHERE sha256 = ?", sha)
			db.Exec(q, "DELETE FROM media WHERE sha256 = ?", sha)
			out.Files = append(out.Files, path)
		}
	}
	out.Messages, out.Calls, out.Conversations = len(msgs), len(calls), len(convs)
	return out
}

// SpamScope is what removing these addresses as spam takes: their one-to-one conversations, and
// their calls that are not a group's.
func SpamScope(q db.Querier, addrs []int64) (convs, calls []int64) {
	if len(addrs) == 0 {
		return nil, nil
	}
	in := "(" + db.Marks(len(addrs)) + ")"
	convs = db.Ints(q, "SELECT DISTINCT cm.conversation_id FROM conversation_member cm JOIN conversation c ON c.id = cm.conversation_id "+
		"WHERE NOT c.is_group AND cm.address_id IN "+in, db.Args(addrs)...)
	calls = db.Ints(q, "SELECT id FROM call WHERE conversation_id IS NULL AND address_id IN "+in, db.Args(addrs)...)
	return convs, calls
}

// RemoveMediaFiles deletes media files (paths relative to the media root, as `media.path` has them).
func RemoveMediaFiles(paths []string) {
	root := MediaRoot()
	for _, p := range paths {
		if p == "" || filepath.IsAbs(p) || !filepath.IsLocal(p) {
			continue
		}
		os.Remove(filepath.Join(root, p))
	}
}

// chunked calls fn with the ids a few hundred at a time, as "(?, ?, ...)" and their arguments.
func chunked(ids []int64, fn func(marks string, args []any)) {
	for len(ids) > 0 {
		n := min(len(ids), 500)
		fn("("+db.Marks(n)+")", db.Args(ids[:n]))
		ids = ids[n:]
	}
}

// SetBlocked records the handles blocked on a device or service (`where`) as it lists them all now;
// those no longer listed are dropped. original: as the device wrote each, where it says ("" none).
func (a *Archive) SetBlocked(where string, handles []Handle, original []string) {
	keep := map[int64]bool{}
	for i, h := range handles {
		aid := a.Address(h)
		keep[aid] = true
		orig := ""
		if i < len(original) {
			orig = original[i]
		}
		a.Exec("INSERT INTO blocked (address_id, phone, original) VALUES (?, ?, ?) "+
			"ON CONFLICT DO UPDATE SET original = coalesce(excluded.original, original)", aid, where, nullStr(orig))
	}
	for _, aid := range a.Ints("SELECT address_id FROM blocked WHERE phone = ?", where) {
		if !keep[aid] {
			a.Exec("DELETE FROM blocked WHERE address_id = ? AND phone = ?", aid, where)
		}
	}
}

// Blocked records one handle blocked (or no longer) on a device or service.
func (a *Archive) Blocked(where string, h Handle, blocked bool) {
	aid := a.Address(h)
	if blocked {
		a.Exec("INSERT OR IGNORE INTO blocked (address_id, phone) VALUES (?, ?)", aid, where)
	} else {
		a.Exec("DELETE FROM blocked WHERE address_id = ? AND phone = ?", aid, where)
	}
}
