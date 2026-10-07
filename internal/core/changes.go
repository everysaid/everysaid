// Ports everysaid/core/changes.py.
//
// Changes the user makes in the app. Each is one transaction; nothing of the history is deleted
// here: merging people moves their addresses to one person, splitting moves one address to a new
// person, merging groups links their conversations into one chat, and messages, calls and media
// stay as they are.
package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/pyjson"
)

// Opt is a value that may be left as it is (Set false), or set to a value or to nothing (nil).
type Opt[T any] struct {
	Set   bool
	Value *T
}

// To is an Opt set to a value; Clear is one set to nothing.
func To[T any](v T) Opt[T] { return Opt[T]{true, &v} }
func Clear[T any]() Opt[T] { return Opt[T]{Set: true} }

func trimmedOrNil(p *string) any {
	if p == nil {
		return nil
	}
	if s := strings.TrimSpace(*p); s != "" {
		return s
	}
	return nil
}

// SetPerson: name, the user's own name for them; nameSource, where their name comes from (a
// source of names, 'address:<id>' for one of their handles, or nothing: by the order).
func SetPerson(s *Store, personID int64, name, note, nameSource Opt[string]) error {
	return s.Write(func(tx *sql.Tx) error {
		if !db.Exists(tx, "SELECT 1 FROM person WHERE id = ?", personID) {
			return ErrNotFound
		}
		if nameSource.Set {
			src := ""
			if nameSource.Value != nil {
				src = *nameSource.Value
			}
			if strings.HasPrefix(src, "address:") {
				aid, _ := strconv.ParseInt(strings.SplitN(src, ":", 2)[1], 10, 64)
				if !db.Exists(tx, "SELECT 1 FROM person_address WHERE address_id = ? AND person_id = ?", aid, personID) {
					return errs.New("people.not_their_handle", 0, nil)
				}
			}
			db.Exec(tx, "UPDATE person SET name_source = ? WHERE id = ?", db.NullStr(src), personID)
		}
		if name.Set {
			db.Exec(tx, "UPDATE person SET name = ? WHERE id = ?", trimmedOrNil(name.Value), personID)
		}
		if note.Set {
			db.Exec(tx, "UPDATE person SET note = ? WHERE id = ?", trimmedOrNil(note.Value), personID)
		}
		return nil
	})
}

// MergePeople: `other` becomes part of `into`: its addresses move over (marked as merged by the
// user), its name and note are kept where `into` has none, and its own row goes.
func MergePeople(s *Store, into, other int64) (int64, error) {
	if into == other {
		return 0, errs.New("people.same", 0, nil)
	}
	err := s.Write(func(tx *sql.Tx) error { return merge(tx, into, other) })
	return into, err
}

func merge(tx *sql.Tx, into, other int64) error {
	type row struct{ name, note, uid, url, source sql.NullString }
	rows := map[int64]row{}
	db.Each(tx, "SELECT id, name, note, contact_uid, contact_url, name_source FROM person WHERE id IN (?, ?)",
		[]any{into, other}, func(scan func(...any)) {
			var id int64
			var r row
			scan(&id, &r.name, &r.note, &r.uid, &r.url, &r.source)
			rows[id] = r
		})
	a, okA := rows[into]
	b, okB := rows[other]
	if !okA || !okB {
		return ErrNotFound
	}
	db.Exec(tx, "UPDATE person_address SET person_id = ?, how = 'manual' WHERE person_id = ?", into, other)
	or := func(x, y sql.NullString) any {
		if x.String != "" {
			return x.String
		}
		if y.String != "" {
			return y.String
		}
		return nil
	}
	var notes []string
	for _, n := range []sql.NullString{a.note, b.note} {
		if n.String != "" {
			notes = append(notes, n.String)
		}
	}
	db.Exec(tx, "UPDATE person SET name = ?, note = ?, contact_uid = ?, contact_url = ?, name_source = ? WHERE id = ?",
		or(a.name, b.name), db.NullStr(strings.Join(notes, "\n\n")), or(a.uid, b.uid), or(a.url, b.url),
		or(a.source, b.source), into)
	pInto, pOther := fmt.Sprintf("p%d", into), fmt.Sprintf("p%d", other)
	mergeChatState(tx, pInto, pOther)
	db.Exec(tx, "DELETE FROM merge_dismissed WHERE a = ? OR b = ?", other, other)
	movedPerson(tx, into, other)
	db.Exec(tx, "DELETE FROM person WHERE id = ?", other)
	return nil
}

// mergeChatState: the user's choices for the other's chat are kept where theirs for `into` are
// older or missing; archived only if both were (one of them in view keeps the whole in view).
func mergeChatState(tx *sql.Tx, into, other string) {
	both := len(db.Strs(tx, "SELECT DISTINCT chat FROM chat_state WHERE field = 'archived' AND value = 1 AND chat IN (?, ?)",
		into, other)) == 2
	db.Exec(tx, "INSERT INTO chat_state SELECT ?, field, value, set_at, always FROM chat_state WHERE chat = ? "+
		"ON CONFLICT (chat, field) DO UPDATE SET value = excluded.value, set_at = excluded.set_at, "+
		"always = excluded.always WHERE excluded.set_at > chat_state.set_at", into, other)
	if !both {
		db.Exec(tx, "UPDATE chat_state SET value = 0 WHERE chat = ? AND field = 'archived'", into)
	}
	db.Exec(tx, "DELETE FROM chat_state WHERE chat = ?", other)
}

// SplitAddress: one address leaves its person for a new person of its own (undoing a wrong merge).
// It returns the person the address now has.
func SplitAddress(s *Store, addressID int64) (int64, error) {
	var out int64
	err := s.Write(func(tx *sql.Tx) error {
		pid, ok := db.IntOK(tx, "SELECT person_id FROM person_address WHERE address_id = ?", addressID)
		if !ok {
			return ErrNotFound
		}
		if db.Int(tx, "SELECT count(*) FROM person_address WHERE person_id = ?", pid) < 2 {
			out = pid
			return nil
		}
		out = db.LastID(tx, "INSERT INTO person DEFAULT VALUES")
		db.Exec(tx, "UPDATE person_address SET person_id = ?, how = 'manual' WHERE address_id = ?", out, addressID)
		db.Exec(tx, "DELETE FROM analysis WHERE person_id = ?", pid) // read again, without it
		// its chat is new, but not newly seen: archived as the chat it left
		db.Exec(tx, "INSERT INTO chat_state SELECT ?, field, value, set_at, always FROM chat_state "+
			"WHERE chat = ? AND field = 'archived'", fmt.Sprintf("p%d", out), fmt.Sprintf("p%d", pid))
		return nil
	})
	return out, err
}

// StateValue is the user's choice for one field of a chat: a bool (pinned, muted, archived), a
// time (read_until, Unix ms) or "now", or nil to follow the services again.
type StateValue = any

// SetChatState records the user's choices for a chat. A service's later change wins over them,
// unless `always`.
func SetChatState(s *Store, chatID string, always bool, fields map[string]StateValue) error {
	allowed := map[string]bool{"pinned": true, "muted": true, "archived": true, "read_until": true}
	for f := range fields {
		if !allowed[f] {
			return fmt.Errorf("unknown field %s", f)
		}
	}
	if _, ok := Index(s).Chats[chatID]; !ok {
		return ErrNotFound
	}
	now := time.Now().UnixMilli()
	names := make([]string, 0, len(fields))
	for f := range fields {
		names = append(names, f)
	}
	sort.Strings(names)
	return s.Write(func(tx *sql.Tx) error {
		for _, f := range names {
			v := fields[f]
			if v == nil {
				db.Exec(tx, "DELETE FROM chat_state WHERE chat = ? AND field = ?", chatID, f)
				continue
			}
			var value int64
			switch {
			case v == "now":
				value = now
			case f == "read_until":
				value = toInt(v)
			default:
				value = int64(db.B(truthy(v)))
			}
			db.Exec(tx, "INSERT OR REPLACE INTO chat_state VALUES (?, ?, ?, ?, ?)", chatID, f, value, now, db.B(always))
		}
		return nil
	})
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case bool:
		return int64(db.B(x))
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

func groupOf(ix *ChatIndex, chatID string) (*Chat, error) {
	c := ix.Chats[chatID]
	if c == nil {
		return nil, ErrNotFound
	}
	if c.Type != "group" {
		return nil, errs.New("chat.not_group", 0, nil)
	}
	return c, nil
}

// MergeGroups: the group chat `other` becomes part of `into` (chat ids): their conversations are
// shown as one chat, `into`'s id; the user's choices for the other are kept where `into` has none or
// older ones, and it is archived only if both were.
func MergeGroups(s *Store, into, other string) (string, error) {
	ix := Index(s)
	a, err := groupOf(ix, into)
	if err != nil {
		return "", err
	}
	b, err := groupOf(ix, other)
	if err != nil {
		return "", err
	}
	if a == b {
		return "", errs.New("chat.same_group", 0, nil)
	}
	head, gone := a.ConversationID, b.ConversationID
	err = s.Write(func(tx *sql.Tx) error {
		for _, c := range b.Conversations {
			db.Exec(tx, "INSERT OR REPLACE INTO group_link VALUES (?, ?)", c, head)
		}
		mergeChatState(tx, into, other)
		db.Exec(tx, "DELETE FROM group_dismissed WHERE a = ? OR b = ?", gone, gone)
		return nil
	})
	return into, err
}

// SplitGroup: one group leaves a merged group chat, a chat of its own again, archived as the chat
// it left. When it is the one whose id the chat has, the others keep the chat (and the user's
// choices) under the id of the latest of them. It returns the chat the others are in.
func SplitGroup(s *Store, chatID string, conversationID int64) (string, error) {
	ix := Index(s)
	c, err := groupOf(ix, chatID)
	if err != nil {
		return "", err
	}
	if ix.ConvChat[conversationID] != chatID {
		return "", errs.New("chat.not_in_chat", 0, nil)
	}
	if len(c.Conversations) < 2 {
		return chatID, nil
	}
	out := chatID
	err = s.Write(func(tx *sql.Tx) error {
		head := c.ConversationID
		if conversationID == head { // the rest move to a new head, the latest of them
			var rest []int64
			for _, x := range c.Conversations {
				if x != head {
					rest = append(rest, x)
				}
			}
			newHead := db.Int(tx, "SELECT c.id FROM conversation c LEFT JOIN message m ON m.conversation_id = c.id "+
				"WHERE c.id IN ("+db.Marks(len(rest))+") GROUP BY c.id ORDER BY max(m.ts) DESC, c.id LIMIT 1", db.Args(rest)...)
			db.Exec(tx, "DELETE FROM group_link WHERE conversation_id IN (?, ?)", newHead, head)
			db.Exec(tx, "UPDATE group_link SET into_id = ? WHERE into_id = ?", newHead, head)
			db.Exec(tx, "INSERT OR REPLACE INTO chat_state SELECT ?, field, value, set_at, always FROM chat_state "+
				"WHERE chat = ?", fmt.Sprintf("c%d", newHead), chatID)
			db.Exec(tx, "DELETE FROM chat_state WHERE chat = ? AND field != 'archived'", chatID)
			out = fmt.Sprintf("c%d", newHead)
			return nil
		}
		db.Exec(tx, "DELETE FROM group_link WHERE conversation_id = ?", conversationID)
		// its chat is new, but not newly seen: archived as the chat it left
		db.Exec(tx, "INSERT OR REPLACE INTO chat_state SELECT ?, field, value, set_at, always FROM chat_state "+
			"WHERE chat = ? AND field = 'archived'", fmt.Sprintf("c%d", conversationID), chatID)
		return nil
	})
	return out, err
}

// DismissGroupMerge: the user says these groups are not one: the suggestion is not shown again.
func DismissGroupMerge(s *Store, chatIDs []string) error {
	ix := Index(s)
	set := map[int64]bool{}
	for _, c := range chatIDs {
		g, err := groupOf(ix, c)
		if err != nil {
			return err
		}
		set[g.ConversationID] = true
	}
	ids := sortedIDs(set)
	now := time.Now().Unix()
	return s.Write(func(tx *sql.Tx) error {
		for i, a := range ids {
			for _, b := range ids[i+1:] {
				db.Exec(tx, "INSERT OR REPLACE INTO group_dismissed VALUES (?, ?, ?)", a, b, now)
			}
		}
		return nil
	})
}

func sortedIDs(set map[int64]bool) []int64 {
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// DismissMerge: the user says these people are not one: the suggestion is not shown again.
func DismissMerge(s *Store, personIDs []int64) error {
	set := map[int64]bool{}
	for _, p := range personIDs {
		set[p] = true
	}
	ids := sortedIDs(set)
	now := time.Now().Unix()
	return s.Write(func(tx *sql.Tx) error {
		for i, a := range ids {
			for _, b := range ids[i+1:] {
				db.Exec(tx, "INSERT OR REPLACE INTO merge_dismissed VALUES (?, ?, ?)", a, b, now)
			}
		}
		return nil
	})
}

// UndismissMerge: a pair turned down by mistake may be suggested again.
func UndismissMerge(s *Store, a, b int64) error {
	a, b = min(a, b), max(a, b)
	return s.Write(func(tx *sql.Tx) error {
		db.Exec(tx, "DELETE FROM merge_dismissed WHERE a = ? AND b = ?", a, b)
		return nil
	})
}

// ApplyMerges makes many decisions on suggested merges at once, in one transaction: each group of
// `merges` becomes one person (the first of it), and each pair in `apart` is recorded as not one.
// A person may be in several of them: one already merged counts as the one it went into. It returns
// how many were merged and kept apart.
func ApplyMerges(s *Store, merges [][]int64, apart [][2]int64) (int, int, error) {
	went := map[int64]int64{}
	now := func(pid int64) int64 {
		for {
			next, ok := went[pid]
			if !ok {
				return pid
			}
			pid = next
		}
	}
	merged, keptApart := 0, 0
	err := s.Write(func(tx *sql.Tx) error {
		known := map[int64]bool{}
		for _, id := range db.Ints(tx, "SELECT id FROM person") {
			known[id] = true
		}
		for _, group := range merges {
			var ids []int64
			seen := map[int64]bool{}
			for _, p := range group {
				x := now(p)
				if !seen[x] {
					seen[x] = true
					ids = append(ids, x)
				}
			}
			for _, p := range ids {
				if !known[p] {
					return ErrNotFound
				}
			}
			for _, other := range ids[1:] {
				if err := merge(tx, ids[0], other); err != nil {
					return err
				}
				went[other] = ids[0]
				merged++
			}
		}
		t := time.Now().Unix()
		for _, pair := range apart {
			a, b := now(pair[0]), now(pair[1])
			a, b = min(a, b), max(a, b)
			if a != b {
				db.Exec(tx, "INSERT OR REPLACE INTO merge_dismissed VALUES (?, ?, ?)", a, b, t)
				keptApart++
			}
		}
		return nil
	})
	return merged, keptApart, err
}

// SetSetting stores a shared setting (JSON, written as Python's json.dumps writes it).
func SetSetting(s *Store, key string, value any) error {
	return s.Write(func(tx *sql.Tx) error {
		db.Exec(tx, "INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
			key, pyjson.Dumps(value, true))
		return nil
	})
}

// Settings is every shared setting.
func Settings(s *Store) M {
	out := M{}
	db.Each(s.Read(), "SELECT key, value FROM setting", nil, func(scan func(...any)) {
		var k, v string
		scan(&k, &v)
		var x any
		json.Unmarshal([]byte(v), &x)
		out[k] = x
	})
	return out
}

// DecideMedia records keep, remove or library (send to the default library); "" takes the decision
// away. The newest decision about a file is the one that counts, and is what a library step or a
// removal later reads.
func DecideMedia(s *Store, sha256, decision string, dateMs *int64) error {
	switch decision {
	case "keep", "remove", "library", "":
	default:
		return fmt.Errorf("unknown decision %q", decision)
	}
	return s.Write(func(tx *sql.Tx) error {
		if !db.Exists(tx, "SELECT 1 FROM media WHERE sha256 = ?", sha256) {
			return ErrNotFound
		}
		if decision == "" {
			db.Exec(tx, "DELETE FROM media_decision WHERE sha256 = ?", sha256)
			return nil
		}
		var date any
		if dateMs != nil {
			date = *dateMs
		}
		db.Exec(tx, "INSERT INTO media_decision (sha256, decision, date_ms, at) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT (sha256) DO UPDATE SET decision = excluded.decision, "+
			"date_ms = coalesce(excluded.date_ms, media_decision.date_ms), at = excluded.at",
			sha256, decision, date, time.Now().Unix())
		return nil
	})
}

// SetDevicePeriod: when a phone was the one in use (Unix ms; nil: not known); the copy of a record
// found on two devices that is kept is the one of the device in use at its time.
func SetDevicePeriod(s *Store, deviceID int64, usedFrom, usedUntil Opt[int64]) error {
	return s.Write(func(tx *sql.Tx) error {
		if !db.Exists(tx, "SELECT 1 FROM device WHERE id = ?", deviceID) {
			return ErrNotFound
		}
		if usedFrom.Set {
			db.Exec(tx, "UPDATE device SET used_from = ? WHERE id = ?", optAny(usedFrom), deviceID)
		}
		if usedUntil.Set {
			db.Exec(tx, "UPDATE device SET used_until = ? WHERE id = ?", optAny(usedUntil), deviceID)
		}
		return nil
	})
}

func optAny[T any](o Opt[T]) any {
	if o.Value == nil {
		return nil
	}
	return *o.Value
}

// Devices are the devices sources were read from.
func Devices(s *Store) []M {
	out := []M{}
	db.Each(s.Read(), "SELECT id, name, kind, used_from, used_until FROM device ORDER BY id", nil, func(scan func(...any)) {
		var id int64
		var name string
		var kind sql.NullString
		var from, until sql.NullInt64
		scan(&id, &name, &kind, &from, &until)
		out = append(out, M{"id": id, "name": name, "kind": nullString(kind), "used_from": nullInt(from),
			"used_until": nullInt(until)})
	})
	return out
}
