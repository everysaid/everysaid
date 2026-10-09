// Ports everysaid/core/queries.py: search, and everything between two instants.
package core

import (
	"fmt"
	"sort"
	"strings"

	"everysaid/internal/db"
	"everysaid/internal/text"
)

// highlight is the text around the first match, as [[piece, is_match], ...].
func highlight(txt string, m *text.Matcher, width int) [][2]any {
	out := [][2]any{}
	if txt == "" {
		return out
	}
	rs := []rune(txt)
	spans := m.Spans(txt)
	if len(spans) == 0 {
		return append(out, [2]any{string(rs[:min(width, len(rs))]), false})
	}
	start := max(0, spans[0].Start-floorDivInt(width, 3))
	end := min(len(rs), start+width)
	pos := start
	if start > 0 {
		out = append(out, [2]any{"…", false})
	}
	for _, sp := range spans {
		if sp.Start < max(start, pos) || sp.End > end { // outside, or inside one already marked
			continue
		}
		if sp.Start > pos {
			out = append(out, [2]any{string(rs[pos:sp.Start]), false})
		}
		out = append(out, [2]any{string(rs[sp.Start:sp.End]), true})
		pos = sp.End
	}
	if pos < end {
		out = append(out, [2]any{string(rs[pos:end]), false})
	}
	if end < len(rs) {
		out = append(out, [2]any{"…", false})
	}
	return out
}

func floorDivInt(a, b int) int { return int(floorDiv(int64(a), int64(b))) }

func joinIDs(ids map[int64]bool) string {
	parts := make([]string, 0, len(ids))
	for id := range ids {
		parts = append(parts, fmt.Sprint(id))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// archivedScope is a condition (SQL) for rows of archived chats (archived true) or of the others
// (false): on messages, or on calls (calls: by their conversation, else the person's addresses).
func archivedScope(s *Store, archived, calls bool) string {
	ix := Index(s)
	states := States(s)
	ppl := PeopleOf(s)
	convs, addrs := map[int64]bool{}, map[int64]bool{}
	for cid, c := range ix.Chats {
		if states[cid].Archived {
			for _, cv := range c.Conversations {
				convs[cv] = true
			}
			if c.Type == "person" {
				for _, a := range ppl.Addresses(c.PersonID) {
					addrs[a] = true
				}
			}
		}
	}
	inside := fmt.Sprintf("conversation_id IN (%s)", joinIDs(convs))
	if calls {
		inside = fmt.Sprintf("((conversation_id IS NOT NULL AND %s) OR (conversation_id IS NULL AND address_id IS NOT NULL "+
			"AND address_id IN (%s)))", inside, joinIDs(addrs))
	}
	if archived {
		return inside
	}
	return "NOT " + inside
}

// SearchOptions are a search's filters. Case: as typed (case and accents); else both ignored.
// Whole: whole words only (a word ending in * a prefix); else anywhere, inside words too.
// Archived: only in the archived chats (true), only in the others (false), in all (nil); a chat
// asked for is searched whatever it is.
type SearchOptions struct {
	ChatID        string
	Service, Kind string
	Since, Until  *int64
	Outgoing      *bool
	Limit, Offset int
	Case, Whole   bool
	Archived      *bool
}

func (o SearchOptions) limit() int {
	if o.Limit == 0 {
		return 50
	}
	return o.Limit
}

// Search is the messages whose text has every word of q, newest first, with the chat they are in
// and the matches marked (a word of one or two letters: at the start of words; WordFilters). Without
// words but with dates: everything of those days, calls too, oldest first (see Between).
func Search(s *Store, q string, o SearchOptions) (M, error) {
	m := text.NewMatcher(q, o.Case, o.Whole)
	if o.ChatID != "" {
		o.Archived = nil
	}
	if len(m.Words) == 0 {
		if o.Since == nil && o.Until == nil {
			return M{"items": []M{}, "total": 0}, nil
		}
		return Between(s, o)
	}
	where, args, verify := WordFilters(s, q, o.Case, o.Whole)
	if len(where) == 0 {
		return M{"items": []M{}, "total": 0}, nil
	}
	// a message deleted for everyone is kept, not offered: shown only when opened in its chat
	where = append(where, shown(s, "m.service_id", ""), "NOT m.deleted")
	q2 := s.Read()
	lk := lookupsOf(s)
	if o.Service != "" {
		where = append(where, "m.service_id = ?")
		args = append(args, lk.serviceID(o.Service))
	}
	if o.Kind != "" {
		where = append(where, "m.kind_id = ?")
		args = append(args, lk.kindID(o.Kind))
	}
	if o.Since != nil {
		where = append(where, "m.ts >= ?")
		args = append(args, *o.Since)
	}
	if o.Until != nil {
		where = append(where, "m.ts < ?")
		args = append(args, *o.Until)
	}
	if o.Outgoing != nil {
		where = append(where, "m.outgoing = ?")
		args = append(args, db.B(*o.Outgoing))
	}
	if o.Archived != nil {
		where = append(where, archivedScope(s, *o.Archived, false)) // (message m alone: its columns)
	}
	every := strings.Join(where, " AND ") // without the chat: for the chats it was found in
	var convs []int64
	if o.ChatID != "" {
		_, cs, _, err := streamSources(s, o.ChatID)
		if err != nil {
			return nil, err
		}
		convs = cs
		where = append(where, "m.conversation_id IN ("+db.Marks(len(convs))+")")
	}
	chatArgs := db.Args(convs)
	w := strings.Join(where, " AND ")
	ix := Index(s)
	perConv := map[int64]int64{}
	var perOrder []int64
	addConv := func(c, n int64) {
		if _, ok := perConv[c]; !ok {
			perOrder = append(perOrder, c)
		}
		perConv[c] += n
	}
	var total int64
	var rows []Item
	limit := o.limit()
	if o.Case || verify != nil { // of what the index finds, those that have the words (as typed, with Case)
		type found struct{ id, ts, conv int64 }
		var all []found
		db.Each(q2, "SELECT m.id, m.ts, m.conversation_id, m.text FROM message m WHERE "+every+" ORDER BY m.ts DESC", args,
			func(scan func(...any)) {
				var f found
				var txt *string
				scan(&f.id, &f.ts, &f.conv, &txt)
				if txt != nil && (verify == nil || verify(*txt)) && (!o.Case || m.Matches(*txt)) {
					all = append(all, f)
				}
			})
		for _, f := range all {
			addConv(f.conv, 1)
		}
		if o.ChatID != "" {
			in := map[int64]bool{}
			for _, c := range convs {
				in[c] = true
			}
			var kept []found
			for _, f := range all {
				if in[f.conv] {
					kept = append(kept, f)
				}
			}
			all = kept
		}
		total = int64(len(all))
		for i := o.Offset; i < len(all) && i < o.Offset+limit; i++ {
			rows = append(rows, Item{"m", all[i].id, all[i].ts})
		}
	} else {
		total = db.Int(q2, "SELECT count(*) FROM message m WHERE "+w, append(append([]any{}, args...), chatArgs...)...)
		db.Each(q2, "SELECT m.id, m.ts FROM message m WHERE "+w+" ORDER BY m.ts DESC LIMIT ? OFFSET ?",
			append(append(append([]any{}, args...), chatArgs...), limit, o.Offset), func(scan func(...any)) {
				r := Item{Type: "m"}
				scan(&r.ID, &r.TS)
				rows = append(rows, r)
			})
		if o.Offset == 0 {
			db.Each(q2, "SELECT m.conversation_id, count(*) FROM message m WHERE "+every+" GROUP BY 1", args,
				func(scan func(...any)) {
					var c, n int64
					scan(&c, &n)
					addConv(c, n)
				})
		}
	}
	// where it was found: each chat (a person's conversations together) with how many
	var chats any
	if o.Offset == 0 {
		chats = perChat(s, ix, perOrder, func(c int64) (string, int64) { return ix.ConvChat[c], perConv[c] }, true)
	}
	items := Hydrate(s, rows, nil)
	for _, it := range items {
		cid := ix.ConvChat[it["conversation_id"].(int64)]
		it["chat_id"], it["chat_title"] = nil, nil
		if c, ok := ix.Chats[cid]; ok {
			it["chat_id"], it["chat_title"] = cid, ChatTitle(s, c)
		} else if cid != "" {
			it["chat_id"] = cid
		}
		txt, _ := it["text"].(string)
		it["highlight"] = highlight(txt, m, 180)
	}
	return M{"items": items, "total": total, "chats": chats}, nil
}

// perChat sums counts into chats ([{chat_id, title, type, count}], the most first, 30 at most).
// keys: the things counted, in the order first counted; of gives each one's chat and count.
func perChat[K comparable](s *Store, ix *ChatIndex, keys []K, of func(K) (string, int64), limitFirst bool) []M {
	counts := map[string]int64{}
	var order []string
	for _, k := range keys {
		chat, n := of(k)
		if _, ok := counts[chat]; !ok {
			order = append(order, chat)
		}
		counts[chat] += n
	}
	if limitFirst {
		// search(): only the chats in the index count, then the 30 with most
		var in []string
		for _, c := range order {
			if _, ok := ix.Chats[c]; ok {
				in = append(in, c)
			}
		}
		order = in
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	out := []M{}
	for _, c := range order {
		chat, ok := ix.Chats[c]
		if !ok {
			continue
		}
		out = append(out, M{"chat_id": c, "title": ChatTitle(s, chat), "type": chat.Type, "count": counts[c]})
		if len(out) == 30 {
			break
		}
	}
	return out
}

// Between is everything between two instants (Unix ms, either open), oldest first: messages and
// calls (not when a kind of message is asked for), with the chat each is in; as Search gives it,
// with the chats it is in and how much of it in each.
func Between(s *Store, o SearchOptions) (M, error) {
	q := s.Read()
	lk := lookupsOf(s)
	ix := Index(s)
	ppl := PeopleOf(s)
	since, until := int64(-1)<<62, int64(1)<<62
	if o.Since != nil {
		since = *o.Since
	}
	if o.Until != nil {
		until = *o.Until
	}
	where, args := []string{"ts >= ?", "ts < ?", shown(s, "", "")}, []any{since, until}
	if o.Service != "" {
		where = append(where, "service_id = ?")
		args = append(args, lk.serviceID(o.Service))
	}
	if o.Outgoing != nil {
		where = append(where, "outgoing = ?")
		args = append(args, db.B(*o.Outgoing))
	}
	every := strings.Join(where, " AND ")
	mScope, cScope := "", ""
	if o.Archived != nil { // (as Search says)
		mScope, cScope = " AND "+archivedScope(s, *o.Archived, false), " AND "+archivedScope(s, *o.Archived, true)
	}
	mWhere, mArgs := every+mScope, append([]any{}, args...)
	cWhere, cArgs := every+cScope, append([]any{}, args...)
	if o.Kind != "" {
		mWhere += " AND kind_id = ?"
		mArgs = append(mArgs, lk.kindID(o.Kind))
	}
	if o.ChatID != "" {
		_, convs, addrs, err := streamSources(s, o.ChatID)
		if err != nil {
			return nil, err
		}
		mWhere += " AND conversation_id IN (" + db.Marks(len(convs)) + ")"
		cWhere += " AND (conversation_id IN (" + db.Marks(len(convs)) + ") OR (conversation_id IS NULL " +
			"AND address_id IN (" + db.Marks(len(addrs)) + ")))"
		mArgs = append(mArgs, db.Args(convs)...)
		cArgs = append(append(cArgs, db.Args(convs)...), db.Args(addrs)...)
	}
	calls := ""
	allArgs := mArgs
	if o.Kind == "" {
		calls = " UNION ALL SELECT 'c', id, ts FROM call WHERE " + cWhere
		allArgs = append(append([]any{}, mArgs...), cArgs...)
	}
	both := "SELECT 'm' AS t, id, ts FROM message WHERE " + mWhere + calls
	total := db.Int(q, "SELECT count(*) FROM ("+both+")", allArgs...)
	var rows []Item
	db.Each(q, both+" ORDER BY ts, t DESC, id LIMIT ? OFFSET ?", append(append([]any{}, allArgs...), o.limit(), o.Offset),
		func(scan func(...any)) {
			var r Item
			scan(&r.Type, &r.ID, &r.TS)
			rows = append(rows, r)
		})
	callChat := func(conv *int64, aid *int64) string {
		if conv != nil {
			return ix.ConvChat[*conv]
		}
		if aid != nil {
			if pid, ok := ppl.PersonOf[*aid]; ok {
				return fmt.Sprintf("p%d", pid)
			}
		}
		return ""
	}
	var chats any
	if o.Offset == 0 { // where it is: each chat with how much (whatever chat was asked for)
		type counted struct {
			chat string
			n    int64
		}
		var found []counted
		kindArg := args
		kindSQL := ""
		if o.Kind != "" {
			kindSQL = " AND kind_id = ?"
			kindArg = append(append([]any{}, args...), lk.kindID(o.Kind))
		}
		db.Each(q, "SELECT conversation_id, count(*) FROM message WHERE "+every+mScope+kindSQL+" GROUP BY 1", kindArg,
			func(scan func(...any)) {
				var c, n int64
				scan(&c, &n)
				found = append(found, counted{ix.ConvChat[c], n})
			})
		if o.Kind == "" {
			db.Each(q, "SELECT conversation_id, address_id, count(*) FROM call WHERE "+every+cScope+" GROUP BY 1, 2", args,
				func(scan func(...any)) {
					var c, a *int64
					var n int64
					scan(&c, &a, &n)
					found = append(found, counted{callChat(c, a), n})
				})
		}
		idx := make([]int, len(found))
		for i := range idx {
			idx[i] = i
		}
		chats = perChat(s, ix, idx, func(i int) (string, int64) { return found[i].chat, found[i].n }, false)
	}
	items := Hydrate(s, rows, nil)
	callAt := map[int64][2]*int64{}
	var callIDs []int64
	for _, it := range items {
		if it["type"] == "call" {
			callIDs = append(callIDs, it["id"].(int64))
		}
	}
	if len(callIDs) > 0 {
		db.Each(q, "SELECT id, conversation_id, address_id FROM call WHERE id IN ("+db.Marks(len(callIDs))+")", db.Args(callIDs),
			func(scan func(...any)) {
				var id int64
				var c, a *int64
				scan(&id, &c, &a)
				callAt[id] = [2]*int64{c, a}
			})
	}
	for _, it := range items {
		var cid string
		if it["type"] == "message" {
			cid = ix.ConvChat[it["conversation_id"].(int64)]
		} else {
			at := callAt[it["id"].(int64)]
			cid = callChat(at[0], at[1])
		}
		it["chat_id"], it["chat_title"] = nil, nil
		if cid != "" {
			it["chat_id"] = cid
		}
		if c, ok := ix.Chats[cid]; ok {
			it["chat_title"] = ChatTitle(s, c)
		}
	}
	return M{"items": items, "total": total, "chats": chats}, nil
}
