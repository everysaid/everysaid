// Ports everysaid/core/queries.py: calls, media, a day's timeline, statistics.
package core

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/db"
)

// CallsOptions: Unnamed false: without the calls of people who have no name, and of hidden numbers;
// Short false: without those of numbers of five digits or fewer (neither in one chat's calls).
type CallsOptions struct {
	ChatID  string
	Missed  bool
	Service string
	Before  int64
	Limit   int
	Unnamed bool
	Short   bool
}

// Calls is calls, newest first: {items, has_more}.
func Calls(s *Store, o CallsOptions) (M, error) {
	q := s.Read()
	limit := o.Limit
	if limit == 0 {
		limit = Page
	}
	where, args := []string{shown(s, "", "")}, []any{}
	if o.ChatID != "" {
		_, _, addrs, err := streamSources(s, o.ChatID)
		if err != nil {
			return nil, err
		}
		where = append(where, "address_id IN ("+db.Marks(len(addrs))+")")
		args = append(args, db.Args(addrs)...)
	} else if !o.Unnamed {
		nameless := joinIDs(unnamedPeople(s).Addresses)
		where = append(where, fmt.Sprintf("(conversation_id IS NOT NULL OR (address_id IS NOT NULL AND address_id NOT IN (%s)))", nameless))
	}
	if !o.Short && o.ChatID == "" {
		if short := shortNumbers(s).Addresses; len(short) > 0 {
			where = append(where, fmt.Sprintf("(address_id IS NULL OR address_id NOT IN (%s))", joinIDs(short)))
		}
	}
	if o.Missed {
		where = append(where, "outgoing = 0 AND answered = 0")
	}
	if o.Service != "" {
		where = append(where, "service_id = (SELECT id FROM service WHERE name = ?)")
		args = append(args, o.Service)
	}
	if o.Before != 0 {
		where = append(where, "ts < ?")
		args = append(args, o.Before)
	}
	// a page ends between two instants: the next one starts before the last time it has, so the
	// calls of that time all go in this one (a page may be a little longer than asked)
	var page []Item
	more := false
	rows := db.Query(q, "SELECT id, ts FROM call WHERE "+strings.Join(where, " AND ")+" ORDER BY ts DESC, id DESC", args...)
	for rows.Next() {
		r := Item{Type: "c"}
		rows.Scan(&r.ID, &r.TS)
		if len(page) >= limit && r.TS != page[len(page)-1].TS {
			more = true
			break
		}
		page = append(page, r)
	}
	rows.Close()
	items := Hydrate(s, page, nil)
	ppl := PeopleOf(s)
	for i, it := range items {
		var aid sql.NullInt64
		db.Row(q, "SELECT address_id FROM call WHERE id = ?", []any{page[i].ID}, &aid)
		it["chat_id"] = nil
		if aid.Valid {
			if pid, ok := ppl.PersonOf[aid.Int64]; ok {
				it["chat_id"] = fmt.Sprintf("p%d", pid)
			}
		}
	}
	return M{"items": items, "has_more": more}, nil
}

// MediaKinds are the kinds of message each media view shows.
var MediaKinds = map[string][]string{"image": {"image"}, "video": {"video"}, "voice": {"voice"}, "file": {"file"},
	"all": {"image", "video", "voice", "file", "sticker"}}

// MediaOptions are the media view's filters.
type MediaOptions struct {
	ChatID        string
	Kind          string
	Before        int64
	Limit         int
	AvailableOnly bool
}

// Media is files of messages, newest first: [{sha256, mime, size, available, message_id, ts,
// chat_id, decision}].
func Media(s *Store, o MediaOptions) (M, error) {
	q := s.Read()
	lk := lookupsOf(s)
	limit := o.Limit
	if limit == 0 {
		limit = Page
	}
	want, ok := MediaKinds[o.Kind]
	if !ok {
		want = MediaKinds["all"]
	}
	var kinds []int64
	for id, name := range lk.Kind {
		for _, w := range want {
			if name == w {
				kinds = append(kinds, id)
			}
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	where := []string{"m.kind_id IN (" + db.Marks(len(kinds)) + ")", shown(s, "m.service_id", "")}
	args := db.Args(kinds)
	if o.ChatID != "" {
		_, convs, _, err := streamSources(s, o.ChatID)
		if err != nil {
			return nil, err
		}
		where = append(where, "m.conversation_id IN ("+db.Marks(len(convs))+")")
		args = append(args, db.Args(convs)...)
	}
	if o.Before != 0 {
		where = append(where, "m.ts < ?")
		args = append(args, o.Before)
	}
	ix := Index(s)
	out := []M{}
	seen := map[string]bool{}
	// read on until the page is full, however many files are left out on the way (seen already, or
	// gone when only those there are asked for), and to the end of the last instant it has: the
	// next page starts before that time
	more := false
	rows := db.Query(q, "SELECT m.id, m.ts, m.conversation_id, md.sha256, md.mime, md.size, md.path, "+
		"(SELECT count(*) FROM library_link l WHERE l.sha256 = md.sha256), d.decision "+
		"FROM message m JOIN attachment a ON a.message_id = m.id JOIN media md ON md.sha256 = a.sha256 "+
		"LEFT JOIN media_decision d ON d.sha256 = md.sha256 "+
		"WHERE "+strings.Join(where, " AND ")+" ORDER BY m.ts DESC, m.id DESC, a.id", args...)
	defer rows.Close()
	for rows.Next() {
		var mid, ts, conv, size, linked int64
		var sha, path string
		var mime, decision sql.NullString
		rows.Scan(&mid, &ts, &conv, &sha, &mime, &size, &path, &linked, &decision)
		if seen[sha] {
			continue
		}
		seen[sha] = true
		available := availability(path, linked)
		if o.AvailableOnly && available == "gone" {
			continue
		}
		if len(out) >= limit && ts != out[len(out)-1]["ts"].(int64) {
			more = true
			break
		}
		var chat any
		if c, ok := ix.ConvChat[conv]; ok {
			chat = c
		}
		out = append(out, M{"sha256": sha, "mime": nullString(mime), "size": size, "available": available,
			"message_id": mid, "ts": ts, "chat_id": chat, "decision": nullString(decision)})
	}
	return M{"items": out, "has_more": more}, nil
}

// Timeline is everything between two instants (Unix ms), across chats, oldest first.
func Timeline(s *Store, dayStart, dayEnd int64) M {
	q := s.Read()
	var rows []Item
	db.Each(q, "SELECT id, ts FROM message WHERE ts >= ? AND ts < ? AND "+shown(s, "", "")+" ORDER BY ts, id",
		[]any{dayStart, dayEnd}, func(scan func(...any)) {
			r := Item{Type: "m"}
			scan(&r.ID, &r.TS)
			rows = append(rows, r)
		})
	db.Each(q, "SELECT id, ts FROM call WHERE ts >= ? AND ts < ? AND "+shown(s, "", "")+" ORDER BY ts, id",
		[]any{dayStart, dayEnd}, func(scan func(...any)) {
			r := Item{Type: "c"}
			scan(&r.ID, &r.TS)
			rows = append(rows, r)
		})
	// Python: by (ts, "c" before "m", id)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.TS != b.TS {
			return a.TS < b.TS
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.ID < b.ID
	})
	page := rows
	if len(page) > 2000 {
		page = page[:2000]
	}
	items := Hydrate(s, page, nil)
	ix := Index(s)
	for _, it := range items {
		if it["type"] == "message" {
			it["chat_id"] = nil
			if c, ok := ix.ConvChat[it["conversation_id"].(int64)]; ok {
				it["chat_id"] = c
			}
		}
	}
	return M{"items": items, "truncated": len(rows) > 2000}
}

type statRow struct {
	conv      sql.NullInt64
	sid       int64
	year      string
	n, lo, hi int64
	addr      sql.NullInt64
}

type counted struct {
	msgs, calls []statRow
}

// Stats is counts over the archive; the archived chats left out unless asked for.
func Stats(s *Store, includeArchived bool) M {
	return Cached(s, fmt.Sprintf("stats:%v", includeArchived), func() M {
		// per conversation (or, for calls without one, per address): counts by service and year, and
		// the first and last instant; summed below over the chats in view
		c := Cached(s, "stats-counted", func() counted {
			var out counted
			db.Each(s.Read(), "SELECT conversation_id, service_id, "+yearOf(s, "ts")+", count(*), "+
				"min(ts), max(ts) FROM message GROUP BY 1, 2, 3", nil, func(scan func(...any)) {
				var r statRow
				var year sql.NullString
				scan(&r.conv, &r.sid, &year, &r.n, &r.lo, &r.hi)
				r.year = year.String
				out.msgs = append(out.msgs, r)
			})
			db.Each(s.Read(), "SELECT conversation_id, address_id, service_id, count(*) FROM call GROUP BY 1, 2, 3", nil,
				func(scan func(...any)) {
					var r statRow
					scan(&r.conv, &r.addr, &r.sid, &r.n)
					out.calls = append(out.calls, r)
				})
			return out
		})
		lk := lookupsOf(s)
		ix := Index(s)
		states := States(s)
		ppl := PeopleOf(s)
		var shownChats []*Chat
		for _, id := range ix.Order {
			if includeArchived || !states[id].Archived {
				shownChats = append(shownChats, ix.Chats[id])
			}
		}
		convs := map[int64]*Chat{}
		addrs := map[int64]bool{}
		people, groups := 0, 0
		for _, ch := range shownChats {
			for _, cv := range ch.Conversations {
				convs[cv] = ch
			}
			if ch.Type == "person" {
				people++
				if ch.HasCalls {
					for _, a := range ppl.Addresses(ch.PersonID) {
						addrs[a] = true
					}
				}
			}
			if ch.Type == "group" {
				groups++
			}
		}
		byService, callsByService, byYear := map[string]int64{}, map[string]int64{}, map[string]int64{}
		perChat := map[string]int64{}
		var chatOrder []string
		var first, last *int64
		var messages, calls int64
		for _, r := range c.msgs {
			ch := convs[r.conv.Int64]
			if ch == nil {
				continue
			}
			byService[lk.Service[r.sid]] += r.n
			byYear[r.year] += r.n
			if _, ok := perChat[ch.ID]; !ok {
				chatOrder = append(chatOrder, ch.ID)
			}
			perChat[ch.ID] += r.n
			messages += r.n
			if first == nil || r.lo < *first {
				first = ptrOf(r.lo)
			}
			if last == nil || r.hi > *last {
				last = ptrOf(r.hi)
			}
		}
		for _, r := range c.calls {
			var in bool
			if r.conv.Valid {
				in = convs[r.conv.Int64] != nil
			} else {
				in = r.addr.Valid && addrs[r.addr.Int64]
			}
			if in {
				callsByService[lk.Service[r.sid]] += r.n
				calls += r.n
			}
		}
		top := func(kind string) []M {
			var ids []string
			for _, id := range chatOrder {
				if ix.Chats[id].Type == kind {
					ids = append(ids, id)
				}
			}
			sort.SliceStable(ids, func(i, j int) bool { return perChat[ids[i]] > perChat[ids[j]] })
			out := []M{}
			for i, id := range ids {
				if i == 20 {
					break
				}
				out = append(out, M{"chat_id": id, "title": ChatTitle(s, ix.Chats[id]), "messages": perChat[id]})
			}
			return out
		}
		return M{
			"messages": messages, "calls": calls, "people": people, "groups": groups,
			"by_service": byService, "calls_by_service": callsByService, "by_year": byYear,
			"first": first, "last": last, "top_people": top("person"), "top_groups": top("group"),
		}
	})
}

// yearOf is SQL: the year of a time (Unix ms) in the owner's time zone, as text. SQLite knows only
// UTC and the process's zone, so the years' first instants are written into it.
func yearOf(s *Store, column string) string {
	lo, ok := db.IntOK(s.Read(), "SELECT min(ts) FROM message")
	hi, _ := db.IntOK(s.Read(), "SELECT max(ts) FROM message")
	if !ok {
		return "NULL"
	}
	tz := config.Timezone
	if tz == nil {
		tz = time.Local
	}
	first, last := time.UnixMilli(lo).In(tz).Year(), time.UnixMilli(hi).In(tz).Year()
	if first == last {
		return fmt.Sprintf("'%04d'", last)
	}
	var b strings.Builder
	b.WriteString("CASE")
	for y := first; y < last; y++ {
		fmt.Fprintf(&b, " WHEN %s < %d THEN '%04d'", column, time.Date(y+1, 1, 1, 0, 0, 0, 0, tz).UnixMilli(), y)
	}
	fmt.Fprintf(&b, " ELSE '%04d' END", last)
	return b.String()
}

// Now is the time in Unix ms (a variable, for tests).
var Now = func() int64 { return time.Now().UnixMilli() }
