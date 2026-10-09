// Ports everysaid/core/queries.py: lookups, the chat list, a chat's state.
//
// Questions about the archive. Everything returns plain maps and lists (JSON as it is).
//
// Chats: the list a messenger shows. A person is one chat, whatever services they were reached on:
// all their one-to-one conversations and their calls make one stream (`p<person id>`). A group, or
// a conversation that is no one person's (a notes-to-self chat), is a chat of its own
// (`c<conversation id>`).
//
// Streams are read page by page with a cursor: `before` (older) or `after` (newer) an item, given
// as the `cursor` of an item (`<ts>:<m|c>:<id>`), or `around` a time (Unix ms).
package core

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"everysaid/internal/db"
	"everysaid/internal/emoticons"
	"everysaid/internal/i18n"
	"everysaid/internal/text"
)

// Page is a stream's page size.
const Page = 60

// --- lookups -------------------------------------------------------------------------------------

type lookups struct {
	Service map[int64]string
	Kind    map[int64]string
}

func lookupsOf(s *Store) lookups {
	return Cached(s, "lookups", func() lookups {
		l := lookups{Service: map[int64]string{}, Kind: map[int64]string{}}
		db.Each(s.Read(), "SELECT id, name FROM service", nil, func(scan func(...any)) {
			var id int64
			var n string
			scan(&id, &n)
			l.Service[id] = n
		})
		db.Each(s.Read(), "SELECT id, name FROM message_kind", nil, func(scan func(...any)) {
			var id int64
			var n string
			scan(&id, &n)
			l.Kind[id] = n
		})
		return l
	})
}

func (l lookups) serviceID(name string) any {
	for id, n := range l.Service {
		if n == name {
			return id
		}
	}
	return nil
}

func (l lookups) kindID(name string) any {
	for id, n := range l.Kind {
		if n == name {
			return id
		}
	}
	return nil
}

// tables are the archive's tables (one made before a table was added has it after its next import).
func tables(s *Store) map[string]bool {
	return Cached(s, "tables", func() map[string]bool {
		out := map[string]bool{}
		for _, n := range db.Strs(s.Read(), "SELECT name FROM sqlite_master WHERE type = 'table'") {
			out[n] = true
		}
		return out
	})
}

// UnreadSince is the setting `unread_since` (Unix ms): nothing older is unread.
func UnreadSince(s *Store) int64 {
	var v float64
	if s.Setting("unread_since", &v) {
		return int64(v)
	}
	return 0
}

// settingStrings is a setting holding a list, as strings.
func settingStrings(s *Store, key string) []string {
	var raw []any
	s.Setting(key, &raw)
	var out []string
	for _, v := range raw {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case float64:
			out = append(out, strconv.FormatInt(int64(x), 10))
		}
	}
	return out
}

// --- the chat list -------------------------------------------------------------------------------

// hiddenServices are the ids of the services the user hid (setting `hidden_services`). Nothing of
// them shows anywhere (chats, streams, calls, search, media), though the archive keeps all of it.
func hiddenServices(s *Store) map[int64]bool {
	names := map[string]bool{}
	for _, n := range settingStrings(s, "hidden_services") {
		names[n] = true
	}
	out := map[int64]bool{}
	for id, n := range lookupsOf(s).Service {
		if names[n] {
			out[id] = true
		}
	}
	return out
}

// hiddenConversations are the conversations held only by the owner's accounts the user hid
// (setting `hidden_accounts`, address ids). One also held by an account shown stays.
func hiddenConversations(s *Store) map[int64]bool {
	hidden := map[int64]bool{}
	var keys []string
	for _, v := range settingStrings(s, "hidden_accounts") {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			hidden[id] = true
		}
	}
	if len(hidden) == 0 {
		return map[int64]bool{}
	}
	for id := range hidden {
		keys = append(keys, strconv.FormatInt(id, 10))
	}
	sort.Strings(keys)
	return Cached(s, "hidden_conversations:"+strings.Join(keys, ","), func() map[int64]bool {
		own := map[int64]map[int64]bool{}
		db.Each(s.Read(), "SELECT conversation_id, address_id FROM conversation_member "+
			"WHERE address_id IN (SELECT address_id FROM account)", nil, func(scan func(...any)) {
			var c, a int64
			scan(&c, &a)
			if own[c] == nil {
				own[c] = map[int64]bool{}
			}
			own[c][a] = true
		})
		out := map[int64]bool{}
		for c, aids := range own {
			all := true
			for a := range aids {
				if !hidden[a] {
					all = false
				}
			}
			if all {
				out[c] = true
			}
		}
		return out
	})
}

func idList[K comparable](set map[K]bool) string {
	var parts []string
	for k := range set {
		parts = append(parts, fmt.Sprint(k))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// shown is SQL: the rows of services not hidden, and not of conversations of hidden accounts (their
// conversation column: by the service column's table, or `conversation`).
func shown(s *Store, column, conversation string) string {
	if column == "" {
		column = "service_id"
	}
	var out []string
	if ids := hiddenServices(s); len(ids) > 0 {
		out = append(out, fmt.Sprintf("%s NOT IN (%s)", column, idList(ids)))
	}
	if convs := hiddenConversations(s); len(convs) > 0 {
		col := conversation
		if col == "" {
			col = strings.Replace(column, "service_id", "conversation_id", 1)
		}
		out = append(out, fmt.Sprintf("(%s IS NULL OR %s NOT IN (%s))", col, col, idList(convs)))
	}
	if len(out) == 0 {
		return "1"
	}
	return strings.Join(out, " AND ")
}

// Chat is one chat of the list, as the index builds it.
type Chat struct {
	ID             string
	Type           string // person, group, conversation
	PersonID       int64  // a person's chat
	ConversationID int64  // a group's or a conversation's: the first (head) of them
	Title          *string
	TitleTS        int64
	Conversations  []int64
	Services       map[string]bool
	LastTS         int64
	LastMessage    int64
	HasCalls       bool
	Others         map[int64]bool // a group's people, but the owner (nil: not a group's)
}

// ServiceList is the chat's services, sorted.
func (c *Chat) ServiceList() []string {
	out := make([]string, 0, len(c.Services))
	for s := range c.Services {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ChatIndex is which conversations and addresses make each chat.
type ChatIndex struct {
	Chats    map[string]*Chat
	Order    []string         // the chats in the order they were made (Python's dict order)
	ConvChat map[int64]string // conversation -> chat
}

// Index is the chat index, built once per archive version.
func Index(s *Store) *ChatIndex {
	return Cached(s, "chat_index", func() *ChatIndex { return buildIndex(s) })
}

func buildIndex(s *Store) *ChatIndex {
	q := s.Read()
	ppl := PeopleOf(s)
	lk := lookupsOf(s)
	members := map[int64][]int64{}
	db.Each(q, "SELECT conversation_id, address_id FROM conversation_member", nil, func(scan func(...any)) {
		var c, a int64
		scan(&c, &a)
		members[c] = append(members[c], a)
	})
	last := map[int64]int64{}
	db.Each(q, "SELECT conversation_id, max(ts) FROM message GROUP BY conversation_id", nil, func(scan func(...any)) {
		var c int64
		var t sql.NullInt64
		scan(&c, &t)
		last[c] = t.Int64
	})
	links := map[int64]int64{}
	db.Each(q, "SELECT conversation_id, into_id FROM group_link", nil, func(scan func(...any)) {
		var c, i int64
		scan(&c, &i)
		links[c] = i
	})
	ix := &ChatIndex{Chats: map[string]*Chat{}, ConvChat: map[int64]string{}}
	type row struct {
		id, sid int64
		group   bool
		title   sql.NullString
	}
	var rows []row
	db.Each(q, "SELECT id, service_id, is_group, title FROM conversation WHERE "+shown(s, "", "id"), nil, func(scan func(...any)) {
		var r row
		scan(&r.id, &r.sid, &r.group, &r.title)
		rows = append(rows, r)
	})
	othersOf := func(cid int64) map[int64]bool {
		out := map[int64]bool{}
		for _, a := range members[cid] {
			if ppl.OwnAddresses[a] {
				continue
			}
			if pid, ok := ppl.PersonOf[a]; ok && !ppl.Me[pid] {
				out[pid] = true
			}
		}
		return out
	}
	// the user's notes to themselves, on every service (Viber's notes, Telegram's saved messages,
	// an SMS to one's own number): one chat, with the id of the first of them (none of anyone else,
	// ever: not a chat whose people the sources did not list; one the owner wrote from another
	// device of theirs, which a source may give as received, is still theirs)
	mine := map[int64]bool{}
	for a := range ppl.OwnAddresses {
		mine[a] = true
	}
	for pid := range ppl.Me {
		for _, a := range ppl.Addresses(pid) {
			mine[a] = true
		}
	}
	// and the owner a member of it (their own number or account, or the source said so): not a chat
	// whose source listed no one
	var mineOnly []int64
	for _, r := range rows {
		if r.group || len(members[r.id]) == 0 {
			continue
		}
		all := true
		for _, a := range members[r.id] {
			if !mine[a] {
				all = false
			}
		}
		if all {
			mineOnly = append(mineOnly, r.id)
		}
	}
	// of those, the ones where no one else wrote (a service's own notice, "messages are end-to-end
	// encrypted", is no one's); asked of them alone, not of every message in the archive
	heard := map[int64]bool{}
	if len(mineOnly) > 0 {
		db.Each(q, "SELECT DISTINCT conversation_id, sender_id FROM message WHERE conversation_id IN ("+
			db.Marks(len(mineOnly))+") AND outgoing = 0 AND kind_id != (SELECT id FROM message_kind WHERE name = 'system')",
			db.Args(mineOnly), func(scan func(...any)) {
				var c int64
				var sender sql.NullInt64
				scan(&c, &sender)
				if !sender.Valid || !mine[sender.Int64] {
					heard[c] = true
				}
			})
	}
	alone := map[int64]bool{}
	notes := int64(0)
	for _, id := range mineOnly {
		if !heard[id] {
			alone[id] = true
			if notes == 0 || id < notes {
				notes = id
			}
		}
	}
	get := func(key string, make func() *Chat) *Chat {
		if c, ok := ix.Chats[key]; ok {
			return c
		}
		c := make()
		ix.Chats[key] = c
		ix.Order = append(ix.Order, key)
		return c
	}
	for _, r := range rows {
		others := othersOf(r.id)
		var key string
		var chat *Chat
		switch {
		case alone[r.id]:
			key = fmt.Sprintf("c%d", notes)
			chat = get(key, func() *Chat {
				return &Chat{ID: key, Type: "conversation", ConversationID: notes, TitleTS: -1, Services: map[string]bool{}}
			})
		case !r.group && len(others) == 1:
			var pid int64
			for p := range others {
				pid = p
			}
			key = fmt.Sprintf("p%d", pid)
			chat = get(key, func() *Chat { return &Chat{ID: key, Type: "person", PersonID: pid, Services: map[string]bool{}} })
		default:
			head := r.id
			if into, ok := links[r.id]; ok {
				head = into
			}
			key = fmt.Sprintf("c%d", head)
			typ := "conversation"
			if r.group {
				typ = "group"
			}
			chat = get(key, func() *Chat {
				return &Chat{ID: key, Type: typ, ConversationID: head, TitleTS: -1, Services: map[string]bool{}}
			})
			if r.title.Valid && r.title.String != "" && last[r.id] > chat.TitleTS { // merged: the latest one's name
				t := r.title.String
				chat.Title, chat.TitleTS = &t, last[r.id]
			}
			if chat.Others == nil {
				chat.Others = map[int64]bool{}
			}
			for p := range others { // its people, but the owner
				chat.Others[p] = true
			}
		}
		chat.Conversations = append(chat.Conversations, r.id)
		chat.Services[lk.Service[r.sid]] = true
		chat.LastTS = max(chat.LastTS, last[r.id])
		chat.LastMessage = max(chat.LastMessage, last[r.id]) // the list's order
		ix.ConvChat[r.id] = key
	}
	db.Each(q, "SELECT conversation_id, max(ts) FROM call WHERE conversation_id IS NOT NULL AND "+shown(s, "", "")+
		" GROUP BY conversation_id", nil, func(scan func(...any)) { // a group's calls
		var c int64
		var t sql.NullInt64
		scan(&c, &t)
		if key, ok := ix.ConvChat[c]; ok {
			chat := ix.Chats[key]
			chat.LastTS = max(chat.LastTS, t.Int64)
		}
	})
	db.Each(q, "SELECT address_id, service_id, max(ts) FROM call WHERE conversation_id IS NULL "+
		"AND address_id IS NOT NULL AND "+shown(s, "", "")+" GROUP BY address_id, service_id", nil, func(scan func(...any)) {
		var aid, sid int64
		var t sql.NullInt64
		scan(&aid, &sid, &t)
		pid, ok := ppl.PersonOf[aid]
		if !ok || ppl.Me[pid] {
			return
		}
		key := fmt.Sprintf("p%d", pid)
		chat := get(key, func() *Chat { return &Chat{ID: key, Type: "person", PersonID: pid, Services: map[string]bool{}} })
		chat.Services[lk.Service[sid]] = true
		chat.LastTS = max(chat.LastTS, t.Int64)
		chat.HasCalls = true
	})
	return ix
}

// Fields of a chat's state.
var Fields = []string{"pinned", "muted", "archived", "read_until"}

// ChatState is what applies to a chat.
type ChatState struct {
	Pinned, Muted, Archived bool
	ReadUntil               *int64
	Origin                  M // field -> "user" | service | nil
}

type report struct {
	Conversation      int64
	Plugin, Service   string
	Value             int64
	Observed, Changed int64
}

type userState struct {
	Value  int64
	SetAt  int64
	Always int64
}

type stateParts struct {
	Reports map[string][]report
	User    map[string]*userState
}

// statePartsOf is what states combines, per chat, built once per archive version.
func statePartsOf(s *Store) map[string]*stateParts {
	return Cached(s, "states", func() map[string]*stateParts {
		q := s.Read()
		ix := Index(s)
		parts := map[string]*stateParts{}
		get := func(chat string) *stateParts {
			p := parts[chat]
			if p == nil {
				p = &stateParts{Reports: map[string][]report{}, User: map[string]*userState{}}
				parts[chat] = p
			}
			return p
		}
		db.Each(q, "SELECT r.conversation_id, i.plugin, s.name, r.field, r.value, r.observed_at, r.changed_at FROM state_report r "+
			"JOIN plugin_instance i ON i.id = r.instance_id AND i.enabled JOIN conversation c ON c.id = r.conversation_id "+
			"JOIN service s ON s.id = c.service_id", nil, func(scan func(...any)) {
			var r report
			var field string
			scan(&r.Conversation, &r.Plugin, &r.Service, &field, &r.Value, &r.Observed, &r.Changed)
			if chat, ok := ix.ConvChat[r.Conversation]; ok {
				p := get(chat)
				p.Reports[field] = append(p.Reports[field], r)
			}
		})
		db.Each(q, "SELECT chat, field, value, set_at, always FROM chat_state", nil, func(scan func(...any)) {
			var chat, field string
			var u userState
			scan(&chat, &field, &u.Value, &u.SetAt, &u.Always)
			if _, ok := ix.Chats[chat]; ok {
				get(chat).User[field] = &u
			}
		})
		return parts
	})
}

// States is what applies to each chat, from what its sources report (`state_report`) and what the
// user chose (`chat_state`).
//
// Between sources: of one service, the newest report; between services, the highest weight its
// plugin declares for the field (0: not applied), then the latest change; read_until, the latest
// (read anywhere is read). Between the user and the sources, the later change wins, unless the
// user chose `always`. Archived is ours alone: set at first from the services
// (Archive.InitArchived), then only by the user. Origin: {field: "user" | service} of what applies.
func States(s *Store) map[string]ChatState {
	raw := statePartsOf(s)
	now := time.Now().UnixMilli()
	out := map[string]ChatState{}
	for chat, parts := range raw {
		values := map[string]*int64{}
		origin := M{}
		for _, f := range Fields {
			var reps []report
			if f != "archived" { // archived: ours alone
				reps = parts.Reports[f]
			}
			user := parts.User[f]
			var svc *report
			if f == "read_until" {
				for i := range reps {
					if svc == nil || reps[i].Value > svc.Value {
						svc = &reps[i]
					}
				}
			} else {
				type k struct {
					conv    int64
					service string
				}
				newest := map[k]int{}
				var order []k
				for i, r := range reps { // one service, several sources: the newest says
					key := k{r.Conversation, r.Service}
					j, ok := newest[key]
					if !ok {
						newest[key] = i
						order = append(order, key)
					} else if r.Observed > reps[j].Observed {
						newest[key] = i
					}
				}
				for _, key := range order {
					r := &reps[newest[key]]
					w := StateWeight(r.Plugin, f)
					if w <= 0 {
						continue
					}
					if svc == nil {
						svc = r
						continue
					}
					sw := StateWeight(svc.Plugin, f)
					if w > sw || (w == sw && r.Changed > svc.Changed) {
						svc = r
					}
				}
			}
			var v *int64
			var by any
			if user != nil && (user.Always != 0 || svc == nil || user.SetAt >= svc.Changed) {
				x := user.Value
				if f == "read_until" && svc != nil {
					x = max(x, svc.Value)
				}
				v, by = &x, "user"
			} else if svc != nil {
				x := svc.Value
				v, by = &x, svc.Service
			} else if f != "read_until" {
				zero := int64(0)
				v = &zero
			}
			values[f], origin[f] = v, by
		}
		m := *values["muted"] // the user's: 0/1; a service's: until (ms), -1 for ever
		muted := m != 0
		if origin["muted"] != "user" {
			muted = m == -1 || m > now
		}
		out[chat] = ChatState{Pinned: *values["pinned"] != 0, Muted: muted, Archived: *values["archived"] != 0,
			ReadUntil: values["read_until"], Origin: origin}
	}
	return out
}

func stateOf(states map[string]ChatState, chat string) ChatState {
	if st, ok := states[chat]; ok {
		return st
	}
	return ChatState{Origin: M{}}
}

// stateReports is what each service says about a chat, for showing: {field: [{service, value}]}
// (muted: whether muted now).
func stateReports(s *Store, chatID string) M {
	parts := statePartsOf(s)[chatID]
	out := M{}
	if parts == nil {
		return out
	}
	now := time.Now().UnixMilli()
	for f, reps := range parts.Reports {
		sorted := append([]report(nil), reps...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Observed < sorted[j].Observed })
		by := map[string]any{}
		var order []string
		for _, r := range sorted {
			var v any = r.Value
			if f == "muted" {
				v = r.Value == -1 || r.Value > now
			}
			if _, ok := by[r.Service]; !ok {
				order = append(order, r.Service)
			}
			by[r.Service] = v
		}
		list := []M{}
		for _, k := range order {
			list = append(list, M{"service": k, "value": by[k]})
		}
		out[f] = list
	}
	return out
}

// ChatTitle is a chat's name.
func ChatTitle(s *Store, chat *Chat) string {
	if chat.Type == "person" {
		return PeopleOf(s).Name(chat.PersonID)
	}
	if chat.Title != nil && *chat.Title != "" {
		return *chat.Title
	}
	if chat.Type == "conversation" {
		return i18n.Tr("Notes", s.Language()) // notes to oneself
	}
	ppl := PeopleOf(s)
	var names []string
	for _, a := range db.Ints(s.Read(), "SELECT address_id FROM conversation_member WHERE conversation_id = ? LIMIT 4", chat.ConversationID) {
		if n := ppl.NameOf(a); n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("#%d", chat.ConversationID)
}

// Cut is the first n characters of a text (Python's s[:n]).
func Cut(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

func lastItem(s *Store, chat *Chat, calls bool) M {
	q := s.Read()
	lk := lookupsOf(s)
	convs := chat.Conversations
	type msg struct {
		id, ts    int64
		outgoing  bool
		kind, sid int64
		text      sql.NullString
		sender    sql.NullInt64
		subtype   sql.NullString
		deleted   bool
	}
	var row *msg
	if len(convs) > 0 {
		var m msg
		if db.Row(q, "SELECT id, ts, outgoing, kind_id, text, service_id, sender_id, subtype, deleted FROM message "+
			"WHERE conversation_id IN ("+db.Marks(len(convs))+") ORDER BY ts DESC, id DESC LIMIT 1", db.Args(convs),
			&m.id, &m.ts, &m.outgoing, &m.kind, &m.text, &m.sid, &m.sender, &m.subtype, &m.deleted) {
			row = &m
		}
	}
	if calls && chat.HasCalls {
		addrs := PeopleOf(s).Addresses(chat.PersonID)
		var id, ts, sid int64
		var outgoing, answered, video bool
		var detail sql.NullString
		if db.Row(q, "SELECT id, ts, outgoing, answered, video, service_id, detail FROM call "+
			"WHERE address_id IN ("+db.Marks(len(addrs))+") AND conversation_id IS NULL ORDER BY ts DESC LIMIT 1",
			db.Args(addrs), &id, &ts, &outgoing, &answered, &video, &sid, &detail) && (row == nil || ts >= row.ts) {
			return M{"type": "call", "ts": ts, "outgoing": outgoing, "answered": answered, "video": video,
				"service": lk.Service[sid], "detail": nullString(detail)}
		}
	}
	if row == nil {
		return nil
	}
	txt := row.text.String
	if lk.Service[row.sid] == "viber" {
		txt = emoticons.ViberEmoji(txt)
	}
	var sender any
	if chat.Type == "group" && row.sender.Valid && row.sender.Int64 != 0 {
		sender = PeopleOf(s).NameOfAddress(row.sender.Int64)
	}
	var notice any
	var code string
	var args sql.NullString
	if tables(s)["notice"] && db.Row(q, "SELECT code, args FROM notice WHERE message_id = ?", []any{row.id}, &code, &args) {
		notice = noticeOf(PeopleOf(s), code, args)
	}
	return M{"type": "message", "id": row.id, "ts": row.ts, "outgoing": row.outgoing, "kind": lk.Kind[row.kind],
		"text": Cut(txt, 160), "service": lk.Service[row.sid], "subtype": nullString(row.subtype),
		"deleted": row.deleted, "sender": sender, "notice": notice}
}

func nullString(v sql.NullString) any {
	if v.Valid {
		return v.String
	}
	return nil
}

func nullInt(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func unread(s *Store, chat *Chat, since int64) int64 {
	if len(chat.Conversations) == 0 {
		return 0
	}
	args := append(db.Args(chat.Conversations), since)
	return db.Int(s.Read(), "SELECT count(*) FROM message WHERE conversation_id IN ("+db.Marks(len(chat.Conversations))+
		") AND outgoing = 0 AND ts > ? "+
		"AND (sender_id IS NULL OR sender_id NOT IN (SELECT address_id FROM account)) "+ // not the owner's own, from another device
		"AND kind_id != (SELECT id FROM message_kind WHERE name = 'system')", args...) // nor a service's notice
}

// named says whether every word (folded) is part of one of the texts: a name's words in another
// case or without accents find it.
func named(words []string, texts ...string) bool {
	folded := make([]string, len(texts))
	for i, t := range texts {
		folded[i] = text.Fold(t)
	}
	for _, w := range words {
		found := false
		for _, f := range folded {
			if strings.Contains(f, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// byHandle says whether a search finds a person by their handles, as a messenger finds a contact
// by number: every word in one of them, or the digits typed (spaces, +, dashes, brackets left out)
// in one of their numbers.
func byHandle(ppl *People, pid int64, words []string, q string) bool {
	values := []string{}
	for _, h := range ppl.Handles[pid] {
		values = append(values, h.Value)
	}
	if named(words, values...) {
		return true
	}
	digits := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return r
		case r == ' ' || r == '+' || r == '-' || r == '(' || r == ')' || r == '.' || r == '/':
			return -1
		}
		return 'x'
	}, q)
	if len(digits) < 3 || strings.Contains(digits, "x") {
		return false
	}
	for _, h := range ppl.Handles[pid] {
		if h.Kind == "phone" && strings.Contains(h.Value, digits) {
			return true
		}
	}
	return false
}

type peopleSet struct {
	People    map[int64]bool
	Addresses map[int64]bool
}

// unnamedPeople are the people no source gives a name, only their handle, and their addresses.
func unnamedPeople(s *Store) peopleSet {
	return Cached(s, "unnamed", func() peopleSet {
		ppl := PeopleOf(s)
		out := peopleSet{map[int64]bool{}, map[int64]bool{}}
		for pid := range ppl.Handles {
			if ppl.Me[pid] {
				continue
			}
			if _, src := ppl.Info(pid); src == "handle" {
				out.People[pid] = true
				for _, a := range ppl.Addresses(pid) {
					out.Addresses[a] = true
				}
			}
		}
		return out
	})
}

func isShortNumber(v string) bool {
	d := strings.TrimLeft(v, "+")
	if d == "" || len([]rune(d)) > 5 {
		return false
	}
	for _, r := range d {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// shortNumbers are those whose every handle is a number of five digits or fewer (short codes:
// carriers, banks, services), and their addresses.
func shortNumbers(s *Store) peopleSet {
	return Cached(s, "short_numbers", func() peopleSet {
		ppl := PeopleOf(s)
		out := peopleSet{map[int64]bool{}, map[int64]bool{}}
		for pid, hs := range ppl.Handles {
			if ppl.Me[pid] || len(hs) == 0 {
				continue
			}
			all := true
			for _, h := range hs {
				if h.Kind != "phone" || !isShortNumber(h.Value) {
					all = false
					break
				}
			}
			if all {
				out.People[pid] = true
				for _, a := range ppl.Addresses(pid) {
					out.Addresses[a] = true
				}
			}
		}
		return out
	})
}

// ChatsOptions are the chat list's filters.
type ChatsOptions struct {
	IncludeArchived bool
	Kind            string // person, group or conversation
	Q               string // parts of the title (each word)
	Limit, Offset   int
	// Unnamed false: without the people who have no name, unless they wrote something unread or Q
	// asks for them.
	Unnamed bool
	// MinMessages, MaxMessages: only chats with at least, or at most (when HasMax), so many messages.
	MinMessages     int64
	MaxMessages     int64
	HasMax          bool
	WithServices    []string
	WithoutServices []string
	PeopleOnly      map[int64]bool // only the chats of these people (nil: all)
	// EmptyGroups false: without the groups with no one in them but the owner, unless they have
	// something unread or Q asks for them.
	EmptyGroups bool
	// Short false: without the people of numbers of five digits or fewer, unless Q asks for them.
	Short bool
}

// DefaultChatsOptions are Python's defaults.
func DefaultChatsOptions() ChatsOptions {
	return ChatsOptions{Unnamed: true, EmptyGroups: true, Short: true}
}

func conversationSizes(s *Store) map[int64]int64 {
	return Cached(s, "conversation_sizes", func() map[int64]int64 {
		out := map[int64]int64{}
		db.Each(s.Read(), "SELECT conversation_id, count(*) FROM message GROUP BY conversation_id", nil, func(scan func(...any)) {
			var c, n int64
			scan(&c, &n)
			out[c] = n
		})
		return out
	})
}

// Chats is the chat list, newest first, pinned ones on top: [{id, type, title, services, last,
// unread, pinned, muted, avatar}]: the chats with a message in them (calls have a page of their
// own), by their latest message.
func Chats(s *Store, o ChatsOptions) []M {
	ix := Index(s)
	states := States(s)
	var sizes map[int64]int64
	if o.MinMessages != 0 || o.HasMax {
		sizes = conversationSizes(s)
	}
	base := UnreadSince(s)
	ppl := PeopleOf(s)
	var qf []string
	if o.Q != "" {
		qf = strings.Fields(text.Fold(o.Q))
	}
	hidden := map[int64]bool{}
	if !o.Unnamed && qf == nil {
		hidden = unnamedPeople(s).People
	}
	shorts := map[int64]bool{}
	if !o.Short && qf == nil {
		shorts = shortNumbers(s).People
	}
	type item struct {
		pinned bool
		lastTS int64
		chat   *Chat
		title  string
		st     ChatState
	}
	var items []item
	for _, id := range ix.Order {
		chat := ix.Chats[id]
		lastTS := chat.LastMessage
		if lastTS == 0 { // no message in it: calls only (they have their own page), or a source's empty chat
			continue
		}
		st := stateOf(states, chat.ID)
		if st.Archived && !o.IncludeArchived {
			continue
		}
		if o.Kind != "" && chat.Type != o.Kind {
			continue
		}
		isPerson := chat.Type == "person"
		if o.PeopleOnly != nil && !(isPerson && o.PeopleOnly[chat.PersonID]) {
			continue
		}
		if isPerson && shorts[chat.PersonID] {
			continue
		}
		skip := false
		for _, sv := range o.WithServices {
			if !chat.Services[sv] {
				skip = true
			}
		}
		for _, sv := range o.WithoutServices {
			if chat.Services[sv] {
				skip = true
			}
		}
		if skip {
			continue
		}
		if o.MinMessages != 0 || o.HasMax {
			var n int64
			for _, c := range chat.Conversations {
				n += sizes[c]
			}
			if n < o.MinMessages || (o.HasMax && n > o.MaxMessages) {
				continue
			}
		}
		if (isPerson && hidden[chat.PersonID]) || (!o.EmptyGroups && qf == nil && chat.Type == "group" && len(chat.Others) == 0) {
			since := base
			if st.ReadUntil != nil && *st.ReadUntil > since {
				since = *st.ReadUntil
			}
			if lastTS <= since || unread(s, chat, since) == 0 {
				continue
			}
		}
		title := ChatTitle(s, chat)
		if qf != nil && !named(qf, title) && !(isPerson && byHandle(ppl, chat.PersonID, qf, o.Q)) {
			continue
		}
		items = append(items, item{st.Pinned, lastTS, chat, title, st})
	}
	// Python: sort by (pinned, last_ts), reverse, stable
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].pinned != items[j].pinned {
			return items[i].pinned
		}
		return items[i].lastTS > items[j].lastTS
	})
	if o.Limit > 0 {
		lo := min(o.Offset, len(items))
		hi := min(o.Offset+o.Limit, len(items))
		items = items[lo:hi]
	}
	out := []M{}
	for _, it := range items {
		chat := it.chat
		since := base
		if it.st.ReadUntil != nil && *it.st.ReadUntil > since {
			since = *it.st.ReadUntil
		}
		var u int64
		if it.lastTS > since {
			u = unread(s, chat, since)
		}
		avatar := false
		if chat.Type == "person" {
			avatar = ppl.Avatar(chat.PersonID) != ""
		}
		out = append(out, M{
			"id": chat.ID, "type": chat.Type, "title": it.title,
			"person_id": personIDOf(chat), "conversation_id": conversationIDOf(chat),
			"services": chat.ServiceList(), "last_ts": it.lastTS,
			"last": lastItem(s, chat, false), "unread": u,
			"pinned": it.pinned, "muted": it.st.Muted, "archived": it.st.Archived, "avatar": avatar,
		})
	}
	return out
}

func personIDOf(c *Chat) any {
	if c.Type == "person" {
		return c.PersonID
	}
	return nil
}

func conversationIDOf(c *Chat) any {
	if c.Type == "person" {
		return nil
	}
	return c.ConversationID
}

// GetChat is one chat in full, or nil.
func GetChat(s *Store, chatID string) M {
	ix := Index(s)
	c := ix.Chats[chatID]
	if c == nil {
		return nil
	}
	st := stateOf(States(s), chatID)
	userState := M{}
	if parts := statePartsOf(s)[chatID]; parts != nil {
		for f, u := range parts.User {
			userState[f] = M{"value": u.Value, "set_at": u.SetAt, "always": u.Always}
		}
	}
	out := M{"id": c.ID, "type": c.Type, "title": ChatTitle(s, c), "services": c.ServiceList(),
		"person_id": personIDOf(c), "conversation_id": conversationIDOf(c),
		"conversations": nonNil(c.Conversations), "last_ts": c.LastTS, "pinned": st.Pinned,
		"muted": st.Muted, "archived": st.Archived, "read_until": st.ReadUntil, "state_from": st.Origin,
		"state_reports": stateReports(s, chatID), "state_user": userState}
	convs := c.Conversations
	lk := lookupsOf(s)
	out["last_service"] = nil
	if len(convs) > 0 { // where the chat was last active: the way to answer
		if sid, ok := db.IntOK(s.Read(), "SELECT service_id FROM message WHERE conversation_id IN ("+db.Marks(len(convs))+
			") ORDER BY ts DESC, id DESC LIMIT 1", db.Args(convs)...); ok {
			if n, ok := lk.Service[sid]; ok {
				out["last_service"] = n
			}
		}
	}
	if c.Type == "person" {
		out["person"] = Person(s, c.PersonID)
		return out
	}
	q := s.Read()
	ppl := PeopleOf(s)
	members := []M{}
	db.Each(q, "SELECT cm.address_id, group_concat(c.service_id) FROM conversation_member cm "+
		"JOIN conversation c ON c.id = cm.conversation_id WHERE cm.conversation_id IN ("+db.Marks(len(convs))+") "+
		"GROUP BY cm.address_id", db.Args(convs), func(scan func(...any)) {
		var a int64
		var sids string
		scan(&a, &sids)
		if ppl.OwnAddresses[a] {
			return
		}
		set := map[string]bool{}
		for _, x := range strings.Split(sids, ",") {
			id, _ := strconv.ParseInt(x, 10, 64)
			set[lk.Service[id]] = true
		}
		services := make([]string, 0, len(set))
		for k := range set {
			services = append(services, k)
		}
		sort.Strings(services)
		var pid any
		if p, ok := ppl.PersonOf[a]; ok {
			pid = p
		}
		members = append(members, M{"person_id": pid, "name": ppl.NameOfAddress(a), "address_id": a, "services": services})
	})
	out["members"] = members
	if c.Type == "group" { // the groups it is made of (more than one when the user merged them)
		groups := []M{}
		db.Each(q, "SELECT c.id, c.service_id, c.title, count(m.id), min(m.ts), max(m.ts) FROM conversation c "+
			"LEFT JOIN message m ON m.conversation_id = c.id WHERE c.id IN ("+db.Marks(len(convs))+") "+
			"GROUP BY c.id ORDER BY max(m.ts) DESC", db.Args(convs), func(scan func(...any)) {
			var id, sid, n int64
			var title sql.NullString
			var first, last sql.NullInt64
			scan(&id, &sid, &title, &n, &first, &last)
			groups = append(groups, M{"conversation_id": id, "service": lk.Service[sid], "title": nullString(title),
				"messages": n, "first_ts": nullInt(first), "last_ts": nullInt(last)})
		})
		out["groups"] = groups
	}
	return out
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

// ChatOfConversation is the chat a conversation is in ("" when none).
func ChatOfConversation(s *Store, conversationID int64) string {
	return Index(s).ConvChat[conversationID]
}
