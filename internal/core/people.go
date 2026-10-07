// Ports everysaid/core/queries.py: people, suggested merges, people without a name, group suggestions.
package core

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"everysaid/internal/db"
	"everysaid/internal/text"
)

// Person is a person in full, or nil when there is none.
func Person(s *Store, personID int64) M {
	ppl := PeopleOf(s)
	q := s.Read()
	if _, ok := ppl.Handles[personID]; !ok && !db.Exists(q, "SELECT 1 FROM person WHERE id = ?", personID) {
		return nil
	}
	addrs := ppl.Addresses(personID)
	byService := M{}
	stats := M{"messages": int64(0), "calls": int64(0), "first": nil, "last": nil, "by_service": byService}
	var first, last *int64
	widen := func(lo, hi int64) {
		if first == nil || lo < *first {
			first = ptrOf(lo)
		}
		if last == nil || hi > *last {
			last = ptrOf(hi)
		}
	}
	ix := Index(s)
	lk := lookupsOf(s)
	if c := ix.Chats[fmt.Sprintf("p%d", personID)]; c != nil && len(c.Conversations) > 0 {
		db.Each(q, "SELECT service_id, count(*), min(ts), max(ts) FROM message WHERE conversation_id IN ("+
			db.Marks(len(c.Conversations))+") GROUP BY service_id", db.Args(c.Conversations), func(scan func(...any)) {
			var sid, n, lo, hi int64
			scan(&sid, &n, &lo, &hi)
			byService[lk.Service[sid]] = n
			stats["messages"] = stats["messages"].(int64) + n
			widen(lo, hi)
		})
	}
	if len(addrs) > 0 {
		var n int64
		var lo, hi sql.NullInt64
		db.Row(q, "SELECT count(*), min(ts), max(ts) FROM call WHERE address_id IN ("+db.Marks(len(addrs))+")", db.Args(addrs),
			&n, &lo, &hi)
		stats["calls"] = n
		if lo.Valid {
			widen(lo.Int64, hi.Int64)
		}
	}
	stats["first"], stats["last"] = first, last
	groups := []M{}
	if len(addrs) > 0 {
		seen := map[string]bool{}
		for _, cid := range db.Ints(q, "SELECT DISTINCT c.id FROM conversation_member cm JOIN conversation c ON c.id = cm.conversation_id "+
			"WHERE c.is_group AND cm.address_id IN ("+db.Marks(len(addrs))+")", db.Args(addrs)...) {
			chat := ix.ConvChat[cid]
			if c, ok := ix.Chats[chat]; ok && !seen[chat] { // merged groups: once
				seen[chat] = true
				groups = append(groups, M{"chat_id": chat, "title": ChatTitle(s, c)})
			}
		}
	}
	var contact any
	if c := ppl.Contact(personID); c != nil {
		contact = M{"id": c.ID, "name": c.Name, "organization": c.Organization}
	}
	contacts := []M{}
	distinct := map[int64]string{}
	var order []int64
	for _, c := range ppl.Contacts[personID] {
		if _, ok := distinct[c.ID]; !ok {
			order = append(order, c.ID)
		}
		distinct[c.ID] = c.Name
	}
	if len(distinct) > 1 {
		for _, id := range order {
			contacts = append(contacts, M{"id": id, "name": distinct[id]})
		}
	}
	name, from := ppl.Info(personID)
	var given, note, pinned any
	if g, ok := ppl.Given[personID]; ok {
		given = g
	}
	if n, ok := ppl.Notes[personID]; ok {
		note = n
	}
	if p, ok := ppl.Pinned[personID]; ok {
		pinned = p
	}
	return M{"id": personID, "name": name, "given_name": given, "name_from": from, "name_source": pinned,
		"self_named": ppl.SelfNamed(personID), "aka": ppl.Aka(personID), "note": note,
		"handles": ppl.Describe(personID), "me": ppl.Me[personID], "contact": contact, "contacts": contacts,
		"avatar": ppl.Avatar(personID) != "", "stats": stats, "groups": groups}
}

// active are the people the archive has something of: a message, a call, a chat, a reaction, a mention.
func active(s *Store) map[int64]bool {
	return Cached(s, "active", func() map[int64]bool {
		ppl := PeopleOf(s)
		out := map[int64]bool{}
		for _, a := range db.Ints(s.Read(), "SELECT DISTINCT sender_id FROM message UNION SELECT address_id FROM call UNION "+
			"SELECT address_id FROM call_member UNION SELECT address_id FROM conversation_member UNION "+
			"SELECT address_id FROM reaction UNION SELECT address_id FROM mention UNION SELECT address_id FROM receipt") {
			if pid, ok := ppl.PersonOf[a]; ok {
				out[pid] = true
			}
		}
		return out
	})
}

// PeopleListOptions: Unnamed false: without those who have no name, unless Q asks for them. Only:
// just these people (those with a label). Short false: without those of short numbers.
type PeopleListOptions struct {
	Q             string
	Limit, Offset int
	Unnamed       bool
	Only          map[int64]bool
	Short         bool
}

// PeopleList is the people the archive has something of.
func PeopleList(s *Store, o PeopleListOptions) M {
	ppl := PeopleOf(s)
	var qf []string
	if o.Q != "" {
		qf = strings.Fields(text.Fold(o.Q))
	}
	hidden := map[int64]bool{}
	if !o.Unnamed && qf == nil {
		for k := range unnamedPeople(s).People {
			hidden[k] = true
		}
	}
	if !o.Short && qf == nil {
		for k := range shortNumbers(s).People {
			hidden[k] = true
		}
	}
	act := active(s)
	type entry struct {
		m       M
		fold    string
		unnamed bool
	}
	unnamed := unnamedPeople(s).People
	var list []entry
	for _, pid := range ppl.PeopleOrder {
		if ppl.Me[pid] || hidden[pid] || !act[pid] || (o.Only != nil && !o.Only[pid]) {
			continue
		}
		name := ppl.Name(pid)
		if qf != nil {
			texts := []string{name}
			for _, h := range ppl.Handles[pid] {
				texts = append(texts, h.Value)
			}
			if !named(qf, texts...) {
				continue
			}
		}
		list = append(list, entry{M{"id": pid, "name": name, "handles": len(ppl.Handles[pid])}, text.Fold(name), unnamed[pid]})
	}
	// by name, those without one after them all: their numbers and handles ("+30…", "@…") would
	// otherwise come first and be among the names
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].unnamed != list[j].unnamed {
			return list[j].unnamed
		}
		return list[i].fold < list[j].fold
	})
	limit := o.Limit
	if limit == 0 {
		limit = 100
	}
	items := []M{}
	for i := o.Offset; i < len(list) && i < o.Offset+limit; i++ {
		items = append(items, list[i].m)
	}
	return M{"items": items, "total": len(list)}
}

// greeklish: Greek letters (folded: lower case, no accents, final sigma as sigma) as Latin ones, by sound.
var greeklish = func() map[rune]string {
	latin := []string{"a", "v", "g", "d", "e", "z", "i", "th", "i", "k", "l", "m",
		"n", "x", "o", "p", "r", "s", "t", "y", "f", "h", "ps", "o"}
	out := map[rune]string{}
	i := 0
	for c := rune(0x3b1); c < 0x3ca; c++ {
		if c == 0x3c2 {
			continue
		}
		out[c] = latin[i]
		i++
	}
	return out
}()

var sounds = [][2]string{{"oy", "u"}, {"ou", "u"}, {"ey", "ev"}, {"ay", "av"}, {"eu", "ev"}, {"au", "av"}, {"ng", "g"},
	{"ei", "i"}, {"oi", "i"}, {"ai", "e"}, {"y", "i"}, {"ch", "h"}, {"kh", "h"}, {"ph", "f"}, {"w", "o"}, {"c", "k"},
	{"mp", "b"}, {"nt", "d"}, {"gk", "g"}, {"gg", "g"}}

var notLatin = regexp.MustCompile(`[^a-z]`)

// skeleton is a name as it sounds, in Latin letters, its words in order: the same name in Greek
// letters, the words in either order, y or i for the same sound, are one.
func skeleton(name string) string {
	var b strings.Builder
	for _, r := range text.Fold(name) {
		if l, ok := greeklish[r]; ok {
			b.WriteString(l)
		} else {
			b.WriteRune(r)
		}
	}
	var words []string
	for _, w := range pyFields(b.String()) {
		w = notLatin.ReplaceAllString(w, "")
		for _, s := range sounds {
			w = strings.ReplaceAll(w, s[0], s[1])
		}
		w = squeeze(w)
		if w != "" {
			words = append(words, w)
		}
	}
	sort.Strings(words)
	return strings.Join(words, " ")
}

// squeeze is re.sub(r"(.)\1+", r"\1", w): runs of one letter as one.
func squeeze(w string) string {
	var b strings.Builder
	var last rune = -1
	for _, r := range w {
		if r != last {
			b.WriteRune(r)
		}
		last = r
	}
	return b.String()
}

// pyFields is Python's str.split(): on runs of Unicode whitespace.
func pyFields(s string) []string {
	return strings.FieldsFunc(s, isPySpace)
}

func isPySpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' || (r >= 0x1c && r <= 0x1f) ||
		r == 0x85 || r == 0xa0 || r == 0x1680 || (r >= 0x2000 && r <= 0x200a) || r == 0x2028 || r == 0x2029 ||
		r == 0x202f || r == 0x205f || r == 0x3000
}

type suggestion struct {
	pids []int64 // sorted
	why  []string
	name string
}

func pidsKey(pids map[int64]bool) (string, []int64) {
	list := make([]int64, 0, len(pids))
	for p := range pids {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	parts := make([]string, len(list))
	for i, p := range list {
		parts[i] = fmt.Sprint(p)
	}
	return strings.Join(parts, ","), list
}

// MergeSuggestions are people who are likely one, with why, the strongest first; shown, never applied:
//   - contact: one address-book contact lists handles of each (the user's own word);
//   - book: a service's copy of the user's address book gives them the same name;
//   - name: the same name they chose or a chat shows, only when it is rare here (no more than 3
//     people) and has at least two words;
//   - similar: names that sound the same (skeleton: accents, word order, Greek or Latin letters),
//     by the same rule of rare names of two words.
//
// Pairs the user turned down are left out of a group, which is shown if two people remain.
// recent: each person with their latest messages, a little of their history to tell them apart.
func MergeSuggestions(s *Store, limit int, recent bool) []M {
	if limit == 0 {
		limit = 50
	}
	ppl := PeopleOf(s)
	q := s.Read()
	dismissed := map[[2]int64]bool{}
	db.Each(q, "SELECT a, b FROM merge_dismissed", nil, func(scan func(...any)) {
		var a, b int64
		scan(&a, &b)
		dismissed[[2]int64{a, b}] = true
	})
	groups := map[string]*suggestion{}
	var order []string
	add := func(in map[int64]bool, why, name string) {
		pids := map[int64]bool{}
		for p := range in {
			if !ppl.Me[p] {
				pids[p] = true
			}
		}
		// those turned down with every other one of them leave the group
		kept := map[int64]bool{}
		for p := range pids {
			for o := range pids {
				if o != p && !dismissed[[2]int64{min(p, o), max(p, o)}] {
					kept[p] = true
					break
				}
			}
		}
		if len(kept) < 2 {
			return
		}
		key, list := pidsKey(kept)
		g := groups[key]
		if g == nil {
			g = &suggestion{pids: list, name: name}
			groups[key] = g
			order = append(order, key)
		}
		for _, w := range g.why {
			if w == why {
				return
			}
		}
		g.why = append(g.why, why)
	}
	type contactKey struct {
		id   int64
		name string
	}
	byContact := map[contactKey]map[int64]bool{}
	var contactOrder []contactKey
	for _, pid := range orderedKeys(ppl.Contacts, ppl.PeopleOrder) {
		for _, c := range ppl.Contacts[pid] {
			k := contactKey{c.ID, c.Name}
			if byContact[k] == nil {
				byContact[k] = map[int64]bool{}
				contactOrder = append(contactOrder, k)
			}
			byContact[k][pid] = true
		}
	}
	for _, k := range contactOrder {
		add(byContact[k], "contact", k.name)
	}
	byName := map[string]map[string]map[int64]bool{"book": {}, "name": {}}
	nameOrder := map[string][]string{}
	spelled := map[string]string{}
	for _, pid := range orderedKeys(ppl.SeenNames, ppl.PeopleOrder) {
		for _, x := range ppl.SeenNames[pid] {
			if !x.Current {
				continue
			}
			fold := text.Fold(x.Name)
			kind := "name"
			if strings.HasSuffix(x.Source, "/book") {
				kind = "book"
			}
			if byName[kind][fold] == nil {
				byName[kind][fold] = map[int64]bool{}
				nameOrder[kind] = append(nameOrder[kind], fold)
			}
			byName[kind][fold][pid] = true
			if _, ok := spelled[fold]; !ok {
				spelled[fold] = x.Name
			}
		}
	}
	for _, fold := range nameOrder["book"] {
		add(byName["book"][fold], "book", spelled[fold])
	}
	for _, fold := range nameOrder["name"] {
		pids := byName["name"][fold]
		if len(pids) <= 3 && len(pyFields(fold)) >= 2 {
			add(pids, "name", spelled[fold])
		}
	}
	bySound := map[string]map[int64]bool{}
	var soundOrder []string
	sounded := map[string]string{}
	for _, pid := range ppl.PeopleOrder {
		if _, src := ppl.Info(pid); src == "handle" {
			continue
		}
		// Python iterates a set of names here; its order only decides which spelling is shown
		names := map[string]bool{ppl.Name(pid): true}
		for _, x := range ppl.SeenNames[pid] {
			if x.Current {
				names[x.Name] = true
			}
		}
		for _, c := range ppl.Contacts[pid] {
			if c.Name != "" {
				names[c.Name] = true
			}
		}
		sortedNames := make([]string, 0, len(names))
		for n := range names {
			sortedNames = append(sortedNames, n)
		}
		sort.Strings(sortedNames)
		for _, n := range sortedNames {
			sk := skeleton(n)
			if len(pyFields(sk)) >= 2 {
				if bySound[sk] == nil {
					bySound[sk] = map[int64]bool{}
					soundOrder = append(soundOrder, sk)
				}
				bySound[sk][pid] = true
				if _, ok := sounded[sk]; !ok {
					sounded[sk] = n
				}
			}
		}
	}
	for _, sk := range soundOrder {
		pids := bySound[sk]
		if len(pids) < 2 || len(pids) > 3 {
			continue
		}
		within := false
		for _, key := range order {
			in := map[int64]bool{}
			for _, p := range groups[key].pids {
				in[p] = true
			}
			all := true
			for p := range pids {
				if !in[p] {
					all = false
					break
				}
			}
			if all {
				within = true
				break
			}
		}
		if !within {
			add(pids, "similar", sounded[sk])
		}
	}
	strength := map[string]int{"contact": 0, "book": 1, "name": 2, "similar": 3}
	rank := func(g *suggestion) (int, int) {
		best := 99
		for _, w := range g.why {
			best = min(best, strength[w])
		}
		return best, -len(g.why)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a1, a2 := rank(groups[order[i]])
		b1, b2 := rank(groups[order[j]])
		if a1 != b1 {
			return a1 < b1
		}
		return a2 < b2
	})
	out := []M{}
	for i, key := range order {
		if i == limit {
			break
		}
		g := groups[key]
		people := []M{}
		for j, pid := range g.pids {
			if j == 5 {
				break
			}
			p := Person(s, pid)
			if recent {
				p["recent"] = recentOf(s, pid, 2)
			}
			people = append(people, p)
		}
		out = append(out, M{"name": g.name, "why": g.why, "people": people})
	}
	return out
}

// orderedKeys is the keys of a map of people in the order the people first came.
func orderedKeys[V any](m map[int64]V, order []int64) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, pid := range order {
		if _, ok := m[pid]; ok {
			out = append(out, pid)
			seen[pid] = true
		}
	}
	var rest []int64
	for pid := range m {
		if !seen[pid] {
			rest = append(rest, pid)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
	return append(out, rest...)
}

// UnnamedPeople is the people no source names who have something in the archive, those with the
// most first, to name or to merge: {items: [person, with messages and recent], total}. first:
// people to put before the rest (those a name was found for).
func UnnamedPeople(s *Store, limit, offset int, first map[int64]bool) M {
	if limit == 0 {
		limit = 50
	}
	ix := Index(s)
	perConv := conversationSizes(s)
	calls := Cached(s, "person_calls", func() map[int64]int64 { return callsPerPerson(s) })
	act := active(s)
	type sized struct {
		first    bool
		n, calls int64
		pid      int64
	}
	var list []sized
	for pid := range unnamedPeople(s).People {
		if !act[pid] {
			continue
		}
		var n int64
		if c := ix.Chats[fmt.Sprintf("p%d", pid)]; c != nil {
			for _, cid := range c.Conversations {
				n += perConv[cid]
			}
		}
		list = append(list, sized{first != nil && first[pid], n, calls[pid], pid})
	}
	// Python: sorted reverse by the tuple
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.first != b.first {
			return a.first
		}
		if a.n != b.n {
			return a.n > b.n
		}
		if a.calls != b.calls {
			return a.calls > b.calls
		}
		return a.pid > b.pid
	})
	items := []M{}
	for i := offset; i < len(list) && i < offset+limit; i++ {
		p := Person(s, list[i].pid)
		p["recent"] = recentOf(s, list[i].pid, 3)
		items = append(items, p)
	}
	return M{"items": items, "total": len(list)}
}

func callsPerPerson(s *Store) map[int64]int64 {
	ppl := PeopleOf(s)
	out := map[int64]int64{}
	db.Each(s.Read(), "SELECT address_id, count(*) FROM call WHERE address_id IS NOT NULL GROUP BY 1", nil, func(scan func(...any)) {
		var a, n int64
		scan(&a, &n)
		if pid, ok := ppl.PersonOf[a]; ok {
			out[pid] += n
		}
	})
	return out
}

// MergesDismissed is the pairs the user said are not one, the latest first: [{a, b: {id, name}, at}].
func MergesDismissed(s *Store) []M {
	ppl := PeopleOf(s)
	out := []M{}
	db.Each(s.Read(), "SELECT a, b, at FROM merge_dismissed ORDER BY at DESC, a, b", nil, func(scan func(...any)) {
		var a, b, at int64
		scan(&a, &b, &at)
		out = append(out, M{"a": M{"id": a, "name": ppl.Name(a)}, "b": M{"id": b, "name": ppl.Name(b)}, "at": at})
	})
	return out
}

// recentOf is a person's latest messages with text: [{ts, outgoing, text}].
func recentOf(s *Store, personID int64, n int) []M {
	out := []M{}
	c := Index(s).Chats[fmt.Sprintf("p%d", personID)]
	if c == nil || len(c.Conversations) == 0 {
		return out
	}
	db.Each(s.Read(), "SELECT ts, outgoing, text FROM message WHERE conversation_id IN ("+db.Marks(len(c.Conversations))+
		") AND text IS NOT NULL AND text != '' ORDER BY ts DESC LIMIT ?", append(db.Args(c.Conversations), n),
		func(scan func(...any)) {
			var ts int64
			var o bool
			var t string
			scan(&ts, &o, &t)
			out = append(out, M{"ts": ts, "outgoing": o, "text": Cut(t, 160)})
		})
	return out
}

// GroupSuggestions are pairs of group chats that are likely one group (on two services, or made
// again), the likeliest first; shown, never applied:
//   - members: most of their members are the same people (at least two of them);
//   - name: the same name, and someone in both.
//
// chatID: only those with this chat. Pairs the user turned down are left out.
func GroupSuggestions(s *Store, chatID string, limit int) []M {
	if limit == 0 {
		limit = 50
	}
	ix := Index(s)
	ppl := PeopleOf(s)
	q := s.Read()
	dismissed := map[[2]int64]bool{}
	db.Each(q, "SELECT a, b FROM group_dismissed", nil, func(scan func(...any)) {
		var a, b int64
		scan(&a, &b)
		dismissed[[2]int64{a, b}] = true
	})
	groups := map[string]*Chat{}
	convGroup := map[int64]string{}
	for _, id := range ix.Order {
		c := ix.Chats[id]
		if c.Type == "group" {
			groups[id] = c
			for _, cv := range c.Conversations {
				convGroup[cv] = id
			}
		}
	}
	members := map[string]map[int64]bool{}
	var memberOrder []string
	db.Each(q, "SELECT conversation_id, address_id FROM conversation_member", nil, func(scan func(...any)) {
		var cv, aid int64
		scan(&cv, &aid)
		g, ok := convGroup[cv]
		pid, has := ppl.PersonOf[aid]
		if ok && !ppl.OwnAddresses[aid] && has && !ppl.Me[pid] {
			if members[g] == nil {
				members[g] = map[int64]bool{}
				memberOrder = append(memberOrder, g)
			}
			members[g][pid] = true
		}
	})
	inGroups := map[int64]map[string]bool{}
	var personOrder []int64
	for _, g := range memberOrder {
		for pid := range members[g] {
			if inGroups[pid] == nil {
				inGroups[pid] = map[string]bool{}
				personOrder = append(personOrder, pid)
			}
			inGroups[pid][g] = true
		}
	}
	type pair struct{ a, b string }
	shared := map[pair]int{}
	var pairOrder []pair
	for _, pid := range personOrder {
		gs := make([]string, 0, len(inGroups[pid]))
		for g := range inGroups[pid] {
			gs = append(gs, g)
		}
		sort.Strings(gs)
		for i, a := range gs {
			for _, b := range gs[i+1:] {
				p := pair{a, b}
				if _, ok := shared[p]; !ok {
					pairOrder = append(pairOrder, p)
				}
				shared[p]++
			}
		}
	}
	names := map[string]string{}
	for g, c := range groups {
		if c.Title != nil && *c.Title != "" {
			names[g] = text.Fold(*c.Title)
		}
	}
	type found struct {
		nWhy  int
		alike float64
		n     int
		a, b  string
		why   []string
	}
	var out []found
	for _, p := range pairOrder {
		n := shared[p]
		if chatID != "" && chatID != p.a && chatID != p.b {
			continue
		}
		ha, hb := groups[p.a].ConversationID, groups[p.b].ConversationID
		if ha > hb {
			ha, hb = hb, ha
		}
		if dismissed[[2]int64{ha, hb}] {
			continue
		}
		union := map[int64]bool{}
		for x := range members[p.a] {
			union[x] = true
		}
		for x := range members[p.b] {
			union[x] = true
		}
		alike := float64(n) / float64(len(union))
		var why []string
		if n >= 2 && alike >= 0.6 {
			why = append(why, "members")
		}
		if names[p.a] != "" && names[p.a] == names[p.b] {
			why = append(why, "name")
		}
		if len(why) > 0 {
			out = append(out, found{len(why), alike, n, p.a, p.b, why})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].nWhy != out[j].nWhy {
			return out[i].nWhy > out[j].nWhy
		}
		if out[i].alike != out[j].alike {
			return out[i].alike > out[j].alike
		}
		return out[i].n > out[j].n
	})
	res := []M{}
	for i, f := range out {
		if i == limit {
			break
		}
		chats := []M{}
		for _, g := range []string{f.a, f.b} {
			c := groups[g]
			chats = append(chats, M{"chat_id": g, "title": ChatTitle(s, c), "services": c.ServiceList(), "last_ts": c.LastTS,
				"members": len(members[g])})
		}
		res = append(res, M{"why": f.why, "shared": f.n, "chats": chats})
	}
	return res
}
