// What whatsapp-mcp gave that mcp_server.py did not: messages by sender, chat, time and text; the
// one-to-one chat with a contact; the last interaction with someone.
package mcp

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/text"
)

type listFilter struct {
	chat, sender, query, service, kind string
	since, until                       *int64
	limit, offset                      int
	oldestFirst                        bool
}

// listMessages is the messages a filter picks: {total, items}, each with its chat.
func listMessages(s *core.Store, f listFilter) (any, error) {
	if f.chat == "" && f.sender == "" && strings.TrimSpace(f.query) == "" && f.service == "" && f.kind == "" &&
		f.since == nil && f.until == nil {
		return nil, errors.New("give at least one of chat, sender, query, service, kind, since, until")
	}
	ix := core.Index(s)
	where, args := []string{visible(s)}, []any{}
	if f.chat != "" {
		c := ix.Chats[f.chat]
		if c == nil {
			return core.M{"error": "no such chat"}, nil
		}
		where = append(where, "m.conversation_id IN ("+db.Marks(len(c.Conversations))+")")
		args = append(args, db.Args(c.Conversations)...)
	}
	switch {
	case f.sender == "":
	case f.sender == "me":
		where = append(where, "m.outgoing = 1")
	default:
		pid, ok := personArg(f.sender)
		addrs := core.PeopleOf(s).Addresses(pid)
		if !ok || len(addrs) == 0 {
			return core.M{"error": "no such person"}, nil
		}
		// what they wrote: in groups as their sender; in their own chat, where a source names no sender
		var own []int64
		if c := ix.Chats[fmt.Sprintf("p%d", pid)]; c != nil {
			own = c.Conversations
		}
		where = append(where, fmt.Sprintf("m.outgoing = 0 AND (m.sender_id IN (%s) OR (m.sender_id IS NULL AND m.conversation_id IN (%s)))",
			db.Marks(len(addrs)), db.Marks(len(own))))
		args = append(append(args, db.Args(addrs)...), db.Args(own)...)
	}
	// every word, anywhere, accents and case ignored: as core.Search's default
	for _, w := range text.NewMatcher(f.query, false, false).Words {
		folded := text.Fold(strings.TrimRight(w, "*"))
		if folded == "" {
			continue
		}
		quoted := `"` + strings.ReplaceAll(folded, `"`, `""`) + `"`
		if len([]rune(folded)) < 3 {
			where = append(where, "m.id IN (SELECT rowid FROM message_fts WHERE message_fts MATCH ?)")
			args = append(args, quoted+"*")
		} else {
			where = append(where, "m.id IN (SELECT rowid FROM message_tri WHERE message_tri MATCH ?)")
			args = append(args, quoted)
		}
	}
	if f.service != "" {
		where = append(where, "m.service_id = (SELECT id FROM service WHERE name = ?)")
		args = append(args, f.service)
	}
	if f.kind != "" {
		where = append(where, "m.kind_id = (SELECT id FROM message_kind WHERE name = ?)")
		args = append(args, f.kind)
	}
	if f.since != nil {
		where = append(where, "m.ts >= ?")
		args = append(args, *f.since)
	}
	if f.until != nil {
		where = append(where, "m.ts < ?")
		args = append(args, *f.until)
	}
	w := strings.Join(where, " AND ")
	order := "DESC"
	if f.oldestFirst {
		order = "ASC"
	}
	q := s.Read()
	total := db.Int(q, "SELECT count(*) FROM message m WHERE "+w, args...)
	var rows []core.Item
	db.Each(q, "SELECT m.id, m.ts FROM message m WHERE "+w+" ORDER BY m.ts "+order+", m.id "+order+" LIMIT ? OFFSET ?",
		append(args, f.limit, f.offset), func(scan func(...any)) {
			r := core.Item{Type: "m"}
			scan(&r.ID, &r.TS)
			rows = append(rows, r)
		})
	return core.M{"total": total, "items": withChats(s, core.Hydrate(s, rows, nil))}, nil
}

// withChats is messages, slim, each with the chat it is in.
func withChats(s *core.Store, items []core.M) []core.M {
	ix := core.Index(s)
	out := []core.M{}
	for _, it := range items {
		cid := ix.ConvChat[it["conversation_id"].(int64)]
		var title any
		if c := ix.Chats[cid]; c != nil {
			title = core.ChatTitle(s, c)
		}
		if cid != "" {
			it["chat_id"] = cid
		}
		m := slim(it)
		m["chat_title"] = title
		out = append(out, m)
	}
	return out
}

// visible is SQL on `message m`: not of a service the user hid, nor of a conversation held only
// by accounts the user hid (as the core shows nothing of them).
func visible(s *core.Store) string {
	settings := core.Settings(s)
	var out []string
	if names := strList(settings["hidden_services"]); len(names) > 0 {
		ids := db.Ints(s.Read(), "SELECT id FROM service WHERE name IN ("+db.Marks(len(names))+")", db.Args(names)...)
		if len(ids) > 0 {
			out = append(out, "m.service_id NOT IN ("+joinInts(ids)+")")
		}
	}
	hidden := map[int64]bool{}
	for _, v := range strList(settings["hidden_accounts"]) {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			hidden[id] = true
		}
	}
	if len(hidden) > 0 {
		own := map[int64][]int64{}
		db.Each(s.Read(), "SELECT conversation_id, address_id FROM conversation_member "+
			"WHERE address_id IN (SELECT address_id FROM account)", nil, func(scan func(...any)) {
			var c, a int64
			scan(&c, &a)
			own[c] = append(own[c], a)
		})
		var convs []int64
		for c, aids := range own {
			all := true
			for _, a := range aids {
				all = all && hidden[a]
			}
			if all {
				convs = append(convs, c)
			}
		}
		if len(convs) > 0 {
			out = append(out, "m.conversation_id NOT IN ("+joinInts(convs)+")")
		}
	}
	if len(out) == 0 {
		return "1"
	}
	return strings.Join(out, " AND ")
}

// strList is a setting's list as text (numbers too, as the settings may hold address ids either way).
func strList(v any) []string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			switch y := x.(type) {
			case string:
				out = append(out, y)
			case float64:
				out = append(out, strconv.FormatInt(int64(y), 10))
			}
		}
	}
	return out
}

func joinInts(ids []int64) string {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// --- a contact's chat --------------------------------------------------------------------------

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func last10(d string) string {
	if len(d) > 10 {
		return d[len(d)-10:]
	}
	return d
}

// looksLikeNumber: digits, with only the signs people write numbers with.
func looksLikeNumber(s string) bool {
	if len(digits(s)) < 5 {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) && !strings.ContainsRune("+-() ./", r) {
			return false
		}
	}
	return true
}

// directChat is the people a number, a handle or a name points to, with their chats: those whose
// handle it is first, then those named so; within each, the latest contact first.
func directChat(s *core.Store, contact string) []core.M {
	contact = strings.TrimSpace(contact)
	ppl := core.PeopleOf(s)
	ix := core.Index(s)
	var found []int64
	seen := map[int64]bool{}
	add := func(pid int64) {
		if !seen[pid] && !ppl.Me[pid] {
			seen[pid] = true
			found = append(found, pid)
		}
	}
	if contact == "" {
		return []core.M{}
	}
	if looksLikeNumber(contact) { // the project's rule: the same number is the same last 10 digits
		key := last10(digits(contact))
		for _, pid := range ppl.PeopleOrder {
			for _, h := range ppl.Handles[pid] {
				if d := digits(h.Value); len(d) >= len(key) && last10(d) == key && looksLikeNumber(h.Value) {
					add(pid)
				}
			}
		}
	} else {
		want := strings.ToLower(strings.TrimPrefix(contact, "@"))
		for _, pid := range ppl.PeopleOrder {
			for _, h := range ppl.Handles[pid] {
				if strings.ToLower(strings.TrimPrefix(h.Value, "@")) == want {
					add(pid)
				}
			}
		}
	}
	byHandle := len(found)
	if byHandle == 0 {
		for _, p := range core.PeopleList(s, core.PeopleListOptions{Q: contact, Limit: 50, Unnamed: true, Short: true})["items"].([]core.M) {
			add(p["id"].(int64))
		}
	}
	lastOf := func(pid int64) int64 {
		if c := ix.Chats[fmt.Sprintf("p%d", pid)]; c != nil {
			return c.LastTS
		}
		return 0
	}
	sort.SliceStable(found, func(i, j int) bool { return lastOf(found[i]) > lastOf(found[j]) })
	out := []core.M{}
	for _, pid := range found {
		handles := []string{}
		for _, h := range ppl.Describe(pid) {
			handles = append(handles, fmt.Sprint(h["label"]))
		}
		m := core.M{"person_id": pid, "name": ppl.Name(pid), "chat": nil, "handles": handles, "services": []string{},
			"last": nil, "matched": "handle"}
		if byHandle == 0 {
			m["matched"] = "name"
		}
		if c := ix.Chats[fmt.Sprintf("p%d", pid)]; c != nil {
			m["chat"], m["services"], m["last"] = c.ID, c.ServiceList(), when(c.LastTS)
		}
		out = append(out, m)
		if len(out) == 10 {
			break
		}
	}
	return out
}

// --- the last interaction ----------------------------------------------------------------------

func lastInteraction(s *core.Store, pid int64) (any, error) {
	ppl := core.PeopleOf(s)
	addrs := ppl.Addresses(pid)
	if len(addrs) == 0 {
		return core.M{"error": "no such person"}, nil
	}
	ix := core.Index(s)
	chatID := fmt.Sprintf("p%d", pid)
	out := core.M{"person_id": pid, "name": ppl.Name(pid), "chat": nil, "last": nil, "last_in_group": nil}
	var own []int64
	if c := ix.Chats[chatID]; c != nil {
		out["chat"] = chatID
		own = c.Conversations
		page, err := core.Stream(s, chatID, core.StreamOptions{Limit: 1})
		if err != nil {
			return nil, err
		}
		if items := page["items"].([]core.M); len(items) > 0 {
			out["last"] = slim(items[len(items)-1])
		}
	}
	var rows []core.Item
	db.Each(s.Read(), fmt.Sprintf("SELECT m.id, m.ts FROM message m WHERE m.outgoing = 0 AND m.sender_id IN (%s) "+
		"AND m.conversation_id NOT IN (%s) AND %s ORDER BY m.ts DESC, m.id DESC LIMIT 1", db.Marks(len(addrs)),
		db.Marks(len(own)), visible(s)), append(db.Args(addrs), db.Args(own)...), func(scan func(...any)) {
		r := core.Item{Type: "m"}
		scan(&r.ID, &r.TS)
		rows = append(rows, r)
	})
	if items := withChats(s, core.Hydrate(s, rows, nil)); len(items) > 0 {
		out["last_in_group"] = items[0]
	}
	return out, nil
}

// --- small things ------------------------------------------------------------------------------

func timeIn(layout, value string) (int64, error) {
	t, err := time.ParseInLocation(layout, strings.TrimSpace(value), zone())
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

// said is a failure in words the assistant reads (a user error's code is for the interface).
func said(err error) string {
	var ue *errs.UserError
	if errors.As(err, &ue) && ue.Text == "" {
		switch ue.Code {
		case "library.none":
			return "no photo library is set up"
		case "file_gone":
			return "the file is no longer here, only its record"
		}
	}
	return err.Error()
}
