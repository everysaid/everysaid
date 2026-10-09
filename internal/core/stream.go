// Ports everysaid/core/queries.py: streams, a message, receipts, a message in context.
package core

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/emoticons"
)

// ErrNotFound: no such chat (or person, message) in the archive.
var ErrNotFound = errors.New("not found")

// Item is a raw row of a stream: a message ("m") or a call ("c"), its id and time.
type Item struct {
	Type string
	ID   int64
	TS   int64
}

func cursorOf(m M) string {
	t := "c"
	if m["type"] == "message" {
		t = "m"
	}
	return fmt.Sprintf("%d:%s:%d", m["ts"], t, m["id"])
}

type cursorKey struct {
	ts int64
	t  int // 0 call, 1 message
	id int64
}

func (a cursorKey) less(b cursorKey) bool {
	if a.ts != b.ts {
		return a.ts < b.ts
	}
	if a.t != b.t {
		return a.t < b.t
	}
	return a.id < b.id
}

func parseCursor(c string) (cursorKey, error) {
	parts := strings.Split(c, ":")
	if len(parts) != 3 {
		return cursorKey{}, fmt.Errorf("bad cursor %q", c)
	}
	ts, err1 := strconv.ParseInt(parts[0], 10, 64)
	id, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil {
		return cursorKey{}, fmt.Errorf("bad cursor %q", c)
	}
	t := 0
	if parts[1] == "m" {
		t = 1
	}
	return cursorKey{ts, t, id}, nil
}

func keyOf(r Item) cursorKey {
	t := 1
	if r.Type == "c" {
		t = 0
	}
	return cursorKey{r.TS, t, r.ID}
}

func streamSources(s *Store, chatID string) (*Chat, []int64, []int64, error) {
	c := Index(s).Chats[chatID]
	if c == nil {
		return nil, nil, nil, ErrNotFound
	}
	var addrs []int64
	if c.Type == "person" {
		addrs = PeopleOf(s).Addresses(c.PersonID)
	}
	return c, c.Conversations, addrs, nil
}

// fetch is the raw message and call rows of a stream, within `where` on (ts, id); hidden: not these
// services. args are the messages' and callArgs the calls' (nil: the same).
func fetch(s *Store, convs, addrs []int64, where string, args, callArgs []any, order string, limit int, hidden []string) []Item {
	if callArgs == nil {
		callArgs = args
	}
	q := s.Read()
	lk := lookupsOf(s)
	var ids []string
	for id, name := range lk.Service {
		for _, h := range hidden {
			if name == h {
				ids = append(ids, strconv.FormatInt(id, 10))
			}
		}
	}
	for id := range hiddenServices(s) {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	if len(ids) > 0 {
		sort.Strings(ids)
		where = fmt.Sprintf("%s AND service_id NOT IN (%s)", where, strings.Join(ids, ","))
	}
	var rows []Item
	if len(convs) > 0 {
		a := append(append(db.Args(convs), args...), limit)
		db.Each(q, "SELECT id, ts FROM message WHERE conversation_id IN ("+db.Marks(len(convs))+") AND "+where+
			" ORDER BY ts "+order+", id "+order+" LIMIT ?", a, func(scan func(...any)) {
			r := Item{Type: "m"}
			scan(&r.ID, &r.TS)
			rows = append(rows, r)
		})
	}
	// a person's calls by their addresses; a chat's own calls (a group call) by its conversations
	for _, of := range []struct {
		cond string
		ids  []int64
	}{{"address_id IN (%s) AND conversation_id IS NULL", addrs}, {"conversation_id IN (%s)", convs}} {
		if len(of.ids) == 0 {
			continue
		}
		a := append(append(db.Args(of.ids), callArgs...), limit)
		db.Each(q, "SELECT id, ts FROM call WHERE "+fmt.Sprintf(of.cond, db.Marks(len(of.ids)))+" AND "+
			where+" ORDER BY ts "+order+", id "+order+" LIMIT ?", a, func(scan func(...any)) {
			r := Item{Type: "c"}
			scan(&r.ID, &r.TS)
			rows = append(rows, r)
		})
	}
	return rows
}

// callsOf is SQL: the calls of a chat, those with its people's addresses (not of a group) and those
// of its conversations (a group call), with its arguments.
func callsOf(convs, addrs []int64) (string, []any) {
	return "(conversation_id IN (" + db.Marks(len(convs)) + ") OR (conversation_id IS NULL AND address_id IN (" +
		db.Marks(len(addrs)) + ")))", append(db.Args(convs), db.Args(addrs)...)
}

// StreamOptions: a page Before or After a cursor, or Around a time (Unix ms); Hidden services'
// messages and calls are left out (the user turning some of a person's services off).
type StreamOptions struct {
	Before, After string
	Around        *int64
	Limit         int
	Hidden        []string
}

// Stream is a page of a chat's stream, oldest first: {items, has_older, has_newer}.
func Stream(s *Store, chatID string, o StreamOptions) (M, error) {
	c, convs, addrs, err := streamSources(s, chatID)
	if err != nil {
		return nil, err
	}
	limit := o.Limit
	if limit == 0 {
		limit = Page
	}
	if o.Around != nil {
		around := *o.Around
		older, err := Stream(s, chatID, StreamOptions{Before: fmt.Sprintf("%d:m:0", around), Limit: limit / 2, Hidden: o.Hidden})
		if err != nil {
			return nil, err
		}
		newer, err := Stream(s, chatID, StreamOptions{After: fmt.Sprintf("%d:m:%d", around-1, int64(1)<<62),
			Limit: limit - limit/2, Hidden: o.Hidden})
		if err != nil {
			return nil, err
		}
		return M{"items": append(older["items"].([]M), newer["items"].([]M)...), "has_older": older["has_older"],
			"has_newer": newer["has_newer"]}, nil
	}
	if o.After != "" {
		k, err := parseCursor(o.After)
		if err != nil {
			return nil, err
		}
		// at the cursor's instant calls come before messages: after a message, the later messages and
		// no call of that instant; after a call, the later calls and every message of it
		mi, ci := int64(-1), int64(1)<<62
		if k.t == 1 {
			mi = k.id
		} else {
			ci = k.id
		}
		rows := fetch(s, convs, addrs, "(ts > ? OR (ts = ? AND id > ?))", []any{k.ts, k.ts, mi}, []any{k.ts, k.ts, ci},
			"ASC", limit+1, o.Hidden)
		sort.SliceStable(rows, func(a, b int) bool { return keyOf(rows[a]).less(keyOf(rows[b])) })
		var kept []Item
		for _, r := range rows {
			if k.less(keyOf(r)) {
				kept = append(kept, r)
			}
		}
		more := len(kept) > limit
		if more {
			kept = kept[:limit]
		}
		return M{"items": Hydrate(s, kept, c), "has_older": true, "has_newer": more}, nil
	}
	var rows []Item
	var k cursorKey
	if o.Before != "" {
		k, err = parseCursor(o.Before)
		if err != nil {
			return nil, err
		}
		// before a message, the earlier messages and every call of its instant; before a call, the
		// earlier calls and no message of it
		mi, ci := int64(-1), int64(1)<<62
		if k.t == 1 {
			mi = k.id
		} else {
			ci = k.id
		}
		rows = fetch(s, convs, addrs, "(ts < ? OR (ts = ? AND id < ?))", []any{k.ts, k.ts, mi}, []any{k.ts, k.ts, ci},
			"DESC", limit+1, o.Hidden)
	} else {
		rows = fetch(s, convs, addrs, "1", nil, nil, "DESC", limit+1, o.Hidden)
	}
	sort.SliceStable(rows, func(a, b int) bool { return keyOf(rows[b]).less(keyOf(rows[a])) })
	if o.Before != "" {
		var kept []Item
		for _, r := range rows {
			if keyOf(r).less(k) {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	for a, b := 0, len(rows)-1; a < b; a, b = a+1, b-1 {
		rows[a], rows[b] = rows[b], rows[a]
	}
	return M{"items": Hydrate(s, rows, c), "has_older": more, "has_newer": o.Before != ""}, nil
}

// who is a person, or an address no person has (as Python's person_of.get(a, ("a", a))).
type who struct {
	addr bool
	id   int64
}

func whoOf(ppl *People, a int64) who {
	if pid, ok := ppl.PersonOf[a]; ok {
		return who{false, pid}
	}
	return who{true, a}
}

// Hydrate is full items for raw rows, in the order given.
func Hydrate(s *Store, rows []Item, chat *Chat) []M {
	q := s.Read()
	lk := lookupsOf(s)
	ppl := PeopleOf(s)
	var mids, cids []int64
	for _, r := range rows {
		if r.Type == "m" {
			mids = append(mids, r.ID)
		} else {
			cids = append(cids, r.ID)
		}
	}
	msgs, calls := map[int64]M{}, map[int64]M{}
	type extra struct {
		replyTo   sql.NullInt64
		conv, ts  int64
		outgoing  bool
		mentions  int
		reactions []M
		attach    []M
		mentionsL []M
	}
	ex := map[int64]*extra{}
	now := time.Now().UnixMilli()
	if len(mids) > 0 {
		marks := db.Marks(len(mids))
		db.Each(q, "SELECT id, ts, service_id, conversation_id, outgoing, sender_id, kind_id, text, subtype, reply_to, "+
			"reply_text, edited, deleted, forwarded, starred, lat, lon, place, status, key IS NOT NULL, forward_from, album, pinned "+
			"FROM message WHERE id IN ("+marks+")", db.Args(mids), func(scan func(...any)) {
			var mid, ts, sid, conv, kind int64
			var outgoing, edited, deleted, forwarded, starred, keyed bool
			var sender, replyTo, pinned sql.NullInt64
			var txt, subtype, replyText, place, status, forwardFrom, album sql.NullString
			var lat, lon sql.NullFloat64
			scan(&mid, &ts, &sid, &conv, &outgoing, &sender, &kind, &txt, &subtype, &replyTo, &replyText, &edited,
				&deleted, &forwarded, &starred, &lat, &lon, &place, &status, &keyed, &forwardFrom, &album, &pinned)
			var senderID, senderName any
			selfNamed := false
			if sender.Valid && sender.Int64 != 0 {
				if pid, ok := ppl.PersonOf[sender.Int64]; ok {
					senderID = pid
					selfNamed = ppl.SelfNamed(pid)
				}
				senderName = ppl.NameOfAddress(sender.Int64)
			}
			var location any
			if lat.Valid || (place.Valid && place.String != "") {
				location = M{"lat": nullFloat(lat), "lon": nullFloat(lon), "place": nullString(place)}
			}
			msgs[mid] = M{
				"type": "message", "id": mid, "ts": ts, "service": lk.Service[sid], "conversation_id": conv,
				"outgoing": outgoing, "kind": lk.Kind[kind], "subtype": nullString(subtype), "text": nullString(txt),
				"sender_id": senderID, "sender": senderName, "sender_self_named": selfNamed,
				"reply_to": nullInt(replyTo), "reply_text": nullString(replyText), "edited": edited, "deleted": deleted,
				"forwarded": forwarded, "starred": starred, "status": nullString(status),
				"forward_from": nullString(forwardFrom), "album": nullString(album),
				"pinned":    pinned.Valid && (pinned.Int64 < 0 || pinned.Int64 > now), // pinned now
				"keyed":     keyed,                                                    // the service's own id is known: an answer to it can be sent
				"location":  location,
				"reactions": []M{}, "attachments": []M{}, "mentions": []M{}, "receipts": nil, "notice": nil,
			}
			ex[mid] = &extra{replyTo: replyTo, conv: conv, ts: ts, outgoing: outgoing}
		})
		replies := map[int64]bool{}
		for _, e := range ex {
			if e.replyTo.Valid && e.replyTo.Int64 != 0 {
				replies[e.replyTo.Int64] = true
			}
		}
		if len(replies) > 0 {
			ids := make([]int64, 0, len(replies))
			for id := range replies {
				ids = append(ids, id)
			}
			type quote struct {
				txt      sql.NullString
				outgoing bool
				sender   sql.NullInt64
				kind     int64
			}
			quoted := map[int64]quote{}
			db.Each(q, "SELECT id, text, outgoing, sender_id, kind_id FROM message WHERE id IN ("+db.Marks(len(ids))+")",
				db.Args(ids), func(scan func(...any)) {
					var id int64
					var x quote
					scan(&id, &x.txt, &x.outgoing, &x.sender, &x.kind)
					quoted[id] = x
				})
			for mid, m := range msgs {
				e := ex[mid]
				if !e.replyTo.Valid {
					continue
				}
				x, ok := quoted[e.replyTo.Int64]
				if !ok {
					continue
				}
				var sender any
				if !x.outgoing {
					if x.sender.Valid {
						sender = ppl.NameOfAddress(x.sender.Int64)
					}
				}
				m["reply"] = M{"id": e.replyTo.Int64, "text": Cut(x.txt.String, 200), "outgoing": x.outgoing,
					"sender": sender, "kind": lk.Kind[x.kind]}
			}
		}
		db.Each(q, "SELECT message_id, emoji, code, count, address_id, outgoing FROM reaction WHERE message_id IN ("+marks+")",
			db.Args(mids), func(scan func(...any)) {
				var mid, count int64
				var emoji, code sql.NullString
				var who, outgoing sql.NullInt64
				scan(&mid, &emoji, &code, &count, &who, &outgoing)
				mine := outgoing.Valid && outgoing.Int64 != 0
				var whoName any
				if !mine && who.Valid {
					whoName = ppl.NameOfAddress(who.Int64)
				}
				msgs[mid]["reactions"] = append(msgs[mid]["reactions"].([]M), M{"emoji": nullString(emoji),
					"code": nullString(code), "count": count, "mine": mine, "who": whoName})
			})
		tbl := tables(s)
		if tbl["mention"] {
			db.Each(q, "SELECT message_id, address_id, token FROM mention WHERE message_id IN ("+marks+")", db.Args(mids),
				func(scan func(...any)) {
					var mid, who int64
					var token sql.NullString
					scan(&mid, &who, &token)
					var pid any
					if p, ok := ppl.PersonOf[who]; ok {
						pid = p
					}
					msgs[mid]["mentions"] = append(msgs[mid]["mentions"].([]M), M{"token": nullString(token),
						"person_id": pid, "me": ppl.OwnAddresses[who], "name": ppl.NameOfAddress(who)})
				})
		}
		if tbl["notice"] {
			db.Each(q, "SELECT message_id, code, args FROM notice WHERE message_id IN ("+marks+")", db.Args(mids),
				func(scan func(...any)) {
					var mid int64
					var code string
					var args sql.NullString
					scan(&mid, &code, &args)
					msgs[mid]["notice"] = noticeOf(ppl, code, args)
				})
		}
		var mine []int64
		for _, mid := range mids {
			if e := ex[mid]; e != nil && e.outgoing {
				mine = append(mine, mid)
			}
		}
		if len(mine) > 0 && tbl["receipt"] {
			type flags struct{ delivered, read, played bool }
			got := map[int64]map[who]flags{} // message -> person -> flags, each person once
			var gotOrder []int64
			db.Each(q, "SELECT message_id, address_id, delivered_at, read_at, played_at FROM receipt "+
				"WHERE message_id IN ("+db.Marks(len(mine))+")", db.Args(mine), func(scan func(...any)) {
				var mid, a int64
				var d, r, p sql.NullInt64
				scan(&mid, &a, &d, &r, &p)
				if ppl.OwnAddresses[a] {
					return
				}
				if got[mid] == nil {
					got[mid] = map[who]flags{}
					gotOrder = append(gotOrder, mid)
				}
				got[mid][whoOf(ppl, a)] = flags{d.Valid || r.Valid, r.Valid, p.Valid}
			})
			window := map[recipKey]map[who]bool{}
			for _, mid := range gotOrder {
				people := got[mid]
				e := ex[mid]
				to := map[who]bool{}
				for w := range recipients(s, q, ppl, e.conv, e.ts, window) {
					to[w] = true
				}
				var delivered, read, played int
				for w, f := range people {
					to[w] = true
					if f.delivered {
						delivered++
					}
					if f.read {
						read++
					}
					if f.played {
						played++
					}
				}
				msgs[mid]["receipts"] = M{"to": len(to), "delivered": delivered, "read": read, "played": played}
			}
		}
		db.Each(q, "SELECT a.message_id, m.sha256, m.mime, m.size, m.path, a.name, "+
			"(SELECT count(*) FROM library_link l WHERE l.sha256 = m.sha256) "+
			"FROM attachment a JOIN media m ON m.sha256 = a.sha256 WHERE a.message_id IN ("+marks+")", db.Args(mids),
			func(scan func(...any)) {
				var mid, size, linked int64
				var sha, path string
				var mime, name sql.NullString
				scan(&mid, &sha, &mime, &size, &path, &name, &linked)
				att := msgs[mid]["attachments"].([]M)
				for _, x := range att {
					if x["sha256"] == sha {
						return
					}
				}
				msgs[mid]["attachments"] = append(att, M{"sha256": sha, "mime": nullString(mime), "size": size,
					"name": nullString(name), "available": availability(path, linked)})
			})
	}
	if len(cids) > 0 {
		db.Each(q, "SELECT id, ts, service_id, address_id, outgoing, answered, duration, detail, video, attempts "+
			"FROM call WHERE id IN ("+db.Marks(len(cids))+")", db.Args(cids), func(scan func(...any)) {
			var cid, ts, sid, duration, attempts int64
			var aid sql.NullInt64
			var outgoing, answered, video bool
			var detail sql.NullString
			scan(&cid, &ts, &sid, &aid, &outgoing, &answered, &duration, &detail, &video, &attempts)
			var with any
			if aid.Valid {
				with = ppl.NameOfAddress(aid.Int64)
			}
			calls[cid] = M{"type": "call", "id": cid, "ts": ts, "service": lk.Service[sid], "outgoing": outgoing,
				"answered": answered, "duration": duration, "detail": nullString(detail), "video": video,
				"attempts": attempts, "with": with}
		})
	}
	for _, m := range msgs { // Viber's "(inlove)" as 😍 (not where mentions point into the text)
		if m["service"] == "viber" && len(m["mentions"].([]M)) == 0 {
			if t, ok := m["text"].(string); ok {
				m["text"] = emoticons.ViberEmoji(t)
			}
			if r, ok := m["reply"].(M); ok {
				r["text"] = emoticons.ViberEmoji(r["text"].(string))
			}
		}
	}
	out := []M{}
	for _, r := range rows {
		var item M
		if r.Type == "m" {
			item = msgs[r.ID]
		} else {
			item = calls[r.ID]
		}
		if item != nil {
			item["cursor"] = cursorOf(item)
			out = append(out, item)
		}
	}
	return out
}

func nullFloat(v sql.NullFloat64) any {
	if v.Valid {
		return v.Float64
	}
	return nil
}

// availability says where a file is: "local" (in the media store), "library" (only in a photo
// library), or "gone".
func availability(path string, linked int64) string {
	if _, err := os.Stat(filepath.Join(archive.MediaRoot(), path)); err == nil {
		return "local"
	}
	if linked > 0 {
		return "library"
	}
	return "gone"
}

// GetMessage is one message in full, with the chat it is in; nil when there is none.
func GetMessage(s *Store, messageID int64) M {
	items := Hydrate(s, []Item{{Type: "m", ID: messageID}}, nil)
	if len(items) == 0 {
		return nil
	}
	m := items[0]
	m["chat_id"] = nil
	if c := ChatOfConversation(s, m["conversation_id"].(int64)); c != "" {
		m["chat_id"] = c
	}
	return m
}

// RecipientsWindow is how far around a message the receipts of a group tell whom it went to.
const RecipientsWindow = 30 * 86400_000

type recipKey struct{ conv, slot int64 }

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// recipients is whom a message of the user's went to, as people: in a chat with one person, them;
// in a group, whoever the service said got any of the user's messages there within a month of it
// (a member who left, or came later, is not waited for); before any such word, the members it knows.
func recipients(s *Store, q db.Querier, ppl *People, conv, ts int64, cache map[recipKey]map[who]bool) map[who]bool {
	key := recipKey{conv, floorDiv(ts, RecipientsWindow)}
	if got, ok := cache[key]; ok {
		return got
	}
	members := map[who]bool{}
	for _, a := range db.Ints(q, "SELECT address_id FROM conversation_member WHERE conversation_id = ?", conv) {
		if !ppl.OwnAddresses[a] {
			members[whoOf(ppl, a)] = true
		}
	}
	if len(members) > 1 {
		lo, hi := (key.slot-1)*RecipientsWindow, (key.slot+2)*RecipientsWindow
		seen := map[who]bool{}
		for _, a := range db.Ints(q, "SELECT DISTINCT r.address_id FROM message m JOIN receipt r ON r.message_id = m.id "+
			"WHERE m.conversation_id = ? AND m.outgoing AND m.ts BETWEEN ? AND ?", conv, lo, hi) {
			if !ppl.OwnAddresses[a] {
				seen[whoOf(ppl, a)] = true
			}
		}
		if len(seen) > 0 {
			members = seen
		}
	}
	cache[key] = members
	return members
}

// Receipts is who got, read and played one of the user's messages, and when (Unix ms; 0: so, when
// not known), and those it went to who have not yet: [{person_id, name, delivered_at, read_at,
// played_at}], each person once; nil when there is no such message.
func Receipts(s *Store, messageID int64) []M {
	q := s.Read()
	var conv, ts int64
	var outgoing bool
	if !db.Row(q, "SELECT conversation_id, outgoing, ts FROM message WHERE id = ?", []any{messageID}, &conv, &outgoing, &ts) {
		return nil
	}
	ppl := PeopleOf(s)
	type times [3]*int64
	got := map[who]times{}
	var gotOrder []who
	names := map[who]any{}
	setName := func(w who, a int64) {
		if _, ok := names[w]; !ok {
			names[w] = ppl.NameOfAddress(a)
		}
	}
	if outgoing && tables(s)["receipt"] {
		db.Each(q, "SELECT address_id, delivered_at, read_at, played_at FROM receipt WHERE message_id = ?", []any{messageID},
			func(scan func(...any)) {
				var a int64
				var d, r, p sql.NullInt64
				scan(&a, &d, &r, &p)
				if ppl.OwnAddresses[a] {
					return
				}
				w := whoOf(ppl, a)
				setName(w, a)
				if r.Valid && !d.Valid {
					d = r // read: delivered too
				}
				was, ok := got[w]
				if !ok {
					gotOrder = append(gotOrder, w)
				}
				now := times{ptr(d), ptr(r), ptr(p)}
				// the same person by two addresses (a number and a LID): what either says, the earliest known
				var merged times
				for i := range merged {
					x, y := was[i], now[i]
					switch {
					case x == nil:
						merged[i] = y
					case y == nil:
						merged[i] = x
					case *x != 0 && *y != 0:
						merged[i] = ptrOf(min(*x, *y))
					default:
						merged[i] = ptrOf(max(*x, *y))
					}
				}
				got[w] = merged
			})
	}
	if outgoing {
		for _, a := range db.Ints(q, "SELECT address_id FROM conversation_member WHERE conversation_id = ?", conv) {
			setName(whoOf(ppl, a), a)
		}
	}
	var waiting []who
	if outgoing {
		for w := range recipients(s, q, ppl, conv, ts, map[recipKey]map[who]bool{}) {
			if _, ok := got[w]; !ok {
				waiting = append(waiting, w)
			}
		}
		sort.Slice(waiting, func(i, j int) bool {
			if waiting[i].addr != waiting[j].addr {
				return !waiting[i].addr
			}
			return waiting[i].id < waiting[j].id
		})
	}
	out := []M{}
	add := func(w who, t times) {
		var pid any
		if !w.addr {
			pid = w.id
		}
		out = append(out, M{"person_id": pid, "name": names[w], "delivered_at": t[0], "read_at": t[1], "played_at": t[2]})
	}
	for _, w := range gotOrder {
		add(w, got[w])
	}
	for _, w := range waiting {
		add(w, times{})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := out[i]["read_at"].(*int64) == nil, out[j]["read_at"].(*int64) == nil
		if ri != rj {
			return !ri
		}
		di, dj := out[i]["delivered_at"].(*int64) == nil, out[j]["delivered_at"].(*int64) == nil
		if di != dj {
			return !di
		}
		ni, _ := out[i]["name"].(string)
		nj, _ := out[j]["name"].(string)
		return ni < nj
	})
	return out
}

func ptr(v sql.NullInt64) *int64 {
	if v.Valid {
		x := v.Int64
		return &x
	}
	return nil
}

func ptrOf(x int64) *int64 { return &x }

// Context is a message with the n before and after it in its chat.
func Context(s *Store, messageID int64, n int) (M, error) {
	m := GetMessage(s, messageID)
	if m == nil {
		return nil, nil
	}
	chatID, _ := m["chat_id"].(string)
	around := m["ts"].(int64) + 1
	page, err := Stream(s, chatID, StreamOptions{Around: &around, Limit: 2*n + 1})
	if err != nil {
		return nil, err
	}
	return M{"chat_id": m["chat_id"], "message": m, "items": page["items"]}, nil
}
