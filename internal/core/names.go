// Ports everysaid/core/names.py.
//
// Who is who: each person's display name, handles and avatar.
//
// Names come from sources: `contacts` (an address book plugin: the contacts that list one of the
// person's addresses) and, for each service, the names it has shown for their handles
// (`handle_name`), by kind: `<service>/book` (the service's copy of the user's address book),
// `<service>/chat` (a chat's name), `<service>/profile` (a name they chose themselves).
//
// A person's name is the one the user gave (`person.name`); else, if the user pinned where it comes
// from (`person.name_source`: a source, or one handle), from there; else from the first source that
// has one, in the user's order (setting `name_order`), else by the weight the plugins declare; else
// any name seen; else their best handle, formatted (a phone number in international form, an
// email, @username). Within one source, the name most of their handles share, then the latest.
// The user's own addresses (`account`) make up "me".
package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/nyaruka/phonenumbers/v2"

	"everysaid/internal/config"
	"everysaid/internal/db"
)

// HandleOrder: a name where a service gives nothing else, before an id.
var HandleOrder = []string{"phone", "email", "username", "sender", "uri", "name", "id"}

func handleRank(kind string) int {
	for i, k := range HandleOrder {
		if k == kind {
			return i
		}
	}
	return 99
}

// NameOrder is the sources of names, most trusted first: the user's order (setting `name_order`),
// then any source they did not place, by the weight its plugins declare.
func NameOrder(q db.Querier) []string {
	ws := NameWeights()
	weights := map[string]int{}
	for _, w := range ws {
		weights[w.Key] = w.Weight
	}
	var mine []string
	if raw := db.Str(q, "SELECT value FROM setting WHERE key = 'name_order'"); raw != "" {
		var set []string
		json.Unmarshal([]byte(raw), &set)
		for _, x := range set {
			if _, ok := weights[x]; ok {
				mine = append(mine, x)
			}
		}
	}
	placed := map[string]bool{}
	for _, x := range mine {
		placed[x] = true
	}
	var rest []string
	for _, w := range ws {
		if !placed[w.Key] {
			rest = append(rest, w.Key)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return weights[rest[i]] > weights[rest[j]] })
	return append(mine, rest...)
}

// PrettyPhone is a number in international form, where it is one.
func PrettyPhone(value string) string {
	if !strings.HasPrefix(value, "+") {
		return value
	}
	n, err := phonenumbers.Parse(value, "")
	if err != nil {
		return value
	}
	return phonenumbers.Format(n, phonenumbers.INTERNATIONAL)
}

// Pretty is a handle for showing.
func Pretty(kind, value string) string {
	switch kind {
	case "phone":
		return PrettyPhone(value)
	case "username":
		return "@" + value
	}
	return value
}

type cand struct {
	name string
	aid  int64
	seen int64
}

// pick is one name of one source: the one most of the person's handles share, then the latest
// seen (the first of equals, as Python's max).
func pick(cands []cand) string {
	type agg struct {
		aids map[int64]bool
		seen int64
	}
	by := map[string]*agg{}
	var order []string
	for _, c := range cands {
		a := by[c.name]
		if a == nil {
			a = &agg{aids: map[int64]bool{}}
			by[c.name] = a
			order = append(order, c.name)
		}
		a.aids[c.aid] = true
		if c.seen > a.seen {
			a.seen = c.seen
		}
	}
	best := ""
	for i, n := range order {
		if i == 0 || len(by[n].aids) > len(by[best].aids) || (len(by[n].aids) == len(by[best].aids) && by[n].seen > by[best].seen) {
			best = n
		}
	}
	return best
}

// Handle is one of a person's handles.
type Handle struct {
	Kind, Value, Service string // Service "" for the shared kinds
	AddressID            int64
}

// ContactRef is a contact that lists one of a person's addresses.
type ContactRef struct {
	ID           int64
	Name         string
	Photo        *string
	Organization *string
	AddressID    int64
	Updated      int64
}

// Seen is a name a service showed for one of a person's handles.
type Seen struct {
	Source      string // <service>/<kind>
	Name        string
	AddressID   int64
	First, Last int64
	Current     bool
}

// People is a snapshot of every person's name and handles; build with PeopleOf(store), which
// caches it until the archive changes.
type People struct {
	Handles      map[int64][]Handle // person -> handles
	PeopleOrder  []int64            // the people in the order they first came (Python's dict order)
	PersonOf     map[int64]int64    // address -> person
	OwnAddresses map[int64]bool
	Me           map[int64]bool
	Given        map[int64]string
	Pinned       map[int64]string
	Notes        map[int64]string
	Contacts     map[int64][]ContactRef
	SeenNames    map[int64][]Seen
	Order        []string

	mu   sync.Mutex
	info map[int64][2]string
}

// NewPeople reads the snapshot.
func NewPeople(q db.Querier) *People {
	p := &People{Handles: map[int64][]Handle{}, PersonOf: map[int64]int64{}, OwnAddresses: map[int64]bool{},
		Me: map[int64]bool{}, Given: map[int64]string{}, Pinned: map[int64]string{}, Notes: map[int64]string{},
		Contacts: map[int64][]ContactRef{}, SeenNames: map[int64][]Seen{}, info: map[int64][2]string{}}
	db.Each(q, "SELECT a.id, pa.person_id, k.name, a.value, s.name FROM address a "+
		"JOIN person_address pa ON pa.address_id = a.id JOIN address_kind k ON k.id = a.kind_id "+
		"LEFT JOIN service s ON s.id = a.service_id", nil, func(scan func(...any)) {
		var aid, pid int64
		var kind, value string
		var service sql.NullString
		scan(&aid, &pid, &kind, &value, &service)
		if _, ok := p.Handles[pid]; !ok {
			p.PeopleOrder = append(p.PeopleOrder, pid)
		}
		p.Handles[pid] = append(p.Handles[pid], Handle{kind, value, service.String, aid})
		p.PersonOf[aid] = pid
	})
	for _, a := range db.Ints(q, "SELECT address_id FROM account") {
		p.OwnAddresses[a] = true
		if pid, ok := p.PersonOf[a]; ok {
			p.Me[pid] = true
		}
	}
	db.Each(q, "SELECT id, name, name_source, note FROM person WHERE name != '' OR name_source IS NOT NULL OR note IS NOT NULL",
		nil, func(scan func(...any)) {
			var pid int64
			var name, source, note sql.NullString
			scan(&pid, &name, &source, &note)
			if name.String != "" {
				p.Given[pid] = name.String
			}
			if source.String != "" {
				p.Pinned[pid] = source.String
			}
			if note.Valid {
				p.Notes[pid] = note.String
			}
		})
	db.Each(q, "SELECT pa.person_id, c.id, c.name, c.photo, c.organization, ca.address_id, c.updated_at "+
		"FROM contact_address ca JOIN contact c ON c.id = ca.contact_id "+
		"JOIN person_address pa ON pa.address_id = ca.address_id "+
		"JOIN plugin_instance i ON i.id = c.instance_id WHERE i.enabled", nil, func(scan func(...any)) {
		var pid int64
		var c ContactRef
		var name, photo, org sql.NullString
		scan(&pid, &c.ID, &name, &photo, &org, &c.AddressID, &c.Updated)
		c.Name = name.String
		if photo.Valid {
			c.Photo = &photo.String
		}
		if org.Valid {
			c.Organization = &org.String
		}
		p.Contacts[pid] = append(p.Contacts[pid], c)
	})
	db.Each(q, "SELECT pa.person_id, s.name, h.kind, h.name, h.address_id, h.first_seen, h.last_seen, h.current "+
		"FROM handle_name h JOIN person_address pa ON pa.address_id = h.address_id "+
		"JOIN service s ON s.id = h.service_id", nil, func(scan func(...any)) {
		var pid int64
		var service, kind string
		var s Seen
		scan(&pid, &service, &kind, &s.Name, &s.AddressID, &s.First, &s.Last, &s.Current)
		s.Source = service + "/" + kind
		p.SeenNames[pid] = append(p.SeenNames[pid], s)
	})
	p.Order = NameOrder(q)
	return p
}

// PeopleOf is the store's snapshot of people, made again when the archive changes.
func PeopleOf(s *Store) *People {
	return Cached(s, "people", func() *People { return NewPeople(s.Read()) })
}

func (p *People) candidates(pid int64, source string, address int64, byAddress bool) []cand {
	var out []cand
	if source == "contacts" {
		for _, c := range p.Contacts[pid] {
			if c.Name != "" && (!byAddress || c.AddressID == address) {
				out = append(out, cand{c.Name, c.AddressID, c.Updated})
			}
		}
		return out
	}
	for _, s := range p.SeenNames[pid] {
		if s.Current && s.Source == source && (!byAddress || s.AddressID == address) {
			out = append(out, cand{s.Name, s.AddressID, s.Last})
		}
	}
	return out
}

// Info is (name, source): source is "user", "contacts", "<service>/<kind>", or "handle".
func (p *People) Info(pid int64) (string, string) {
	p.mu.Lock()
	if v, ok := p.info[pid]; ok {
		p.mu.Unlock()
		return v[0], v[1]
	}
	p.mu.Unlock()
	name, source := p.compute(pid)
	p.mu.Lock()
	p.info[pid] = [2]string{name, source}
	p.mu.Unlock()
	return name, source
}

func (p *People) compute(pid int64) (string, string) {
	if n, ok := p.Given[pid]; ok {
		return n, "user"
	}
	pin := p.Pinned[pid]
	var address int64
	byAddress := false
	if strings.HasPrefix(pin, "address:") {
		if a, err := strconv.ParseInt(strings.SplitN(pin, ":", 2)[1], 10, 64); err == nil {
			address, byAddress = a, true
		}
	}
	sources := p.Order
	if pin != "" && !byAddress {
		sources = []string{pin}
	}
	for _, where := range sources {
		if n := pick(p.candidates(pid, where, address, byAddress)); n != "" {
			return n, where
		}
	}
	var any []cand
	for _, s := range p.SeenNames[pid] {
		if s.Current {
			any = append(any, cand{s.Name, s.AddressID, s.Last})
		}
	}
	if n := pick(any); n != "" {
		for _, s := range p.SeenNames[pid] {
			if s.Current && s.Name == n {
				return n, s.Source
			}
		}
	}
	hs := p.Handles[pid]
	if len(hs) > 0 {
		best := hs[0]
		for _, h := range hs[1:] {
			if handleRank(h.Kind) < handleRank(best.Kind) {
				best = h
			}
		}
		return Pretty(best.Kind, best.Value), "handle"
	}
	return fmt.Sprintf("#%d", pid), "handle"
}

// Name is the person's name.
func (p *People) Name(pid int64) string {
	n, _ := p.Info(pid)
	return n
}

// SelfNamed says whether the person's name is one they chose themselves (shown marked in groups).
func (p *People) SelfNamed(pid int64) bool {
	_, source := p.Info(pid)
	return strings.HasSuffix(source, "/profile")
}

// NameOfAddress is the name of the person who has the address (nil: none).
func (p *People) NameOfAddress(aid any) any {
	id, ok := asID(aid)
	if !ok {
		return nil
	}
	pid, found := p.PersonOf[id]
	if !found {
		return nil
	}
	return p.Name(pid)
}

// NameOf is the name of the person who has the address id, "" when there is none.
func (p *People) NameOf(aid int64) string {
	pid, found := p.PersonOf[aid]
	if !found {
		return ""
	}
	return p.Name(pid)
}

func asID(v any) (int64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case int64:
		return x, true
	case int:
		return int64(x), true
	case sql.NullInt64:
		return x.Int64, x.Valid
	case *int64:
		if x == nil {
			return 0, false
		}
		return *x, true
	}
	return 0, false
}

// Aka is every other name the person has had: [{name, source, first_seen, last_seen, current}],
// the latest first.
func (p *People) Aka(pid int64) []M {
	shown := p.Name(pid)
	type key struct{ name, source string }
	out := map[key]M{}
	var order []key
	for _, s := range p.SeenNames[pid] {
		if s.Name == shown {
			continue
		}
		k := key{s.Name, s.Source}
		o, ok := out[k]
		if !ok {
			o = M{"name": s.Name, "source": s.Source, "first_seen": s.First, "last_seen": s.Last, "current": false}
			out[k] = o
			order = append(order, k)
		}
		o["first_seen"] = min(o["first_seen"].(int64), s.First)
		o["last_seen"] = max(o["last_seen"].(int64), s.Last)
		o["current"] = o["current"].(bool) || s.Current
	}
	for _, c := range p.Contacts[pid] {
		if c.Name != "" && c.Name != shown {
			k := key{c.Name, "contacts"}
			if _, ok := out[k]; !ok {
				out[k] = M{"name": c.Name, "source": "contacts", "first_seen": c.Updated, "last_seen": c.Updated, "current": true}
				order = append(order, k)
			}
		}
	}
	list := make([]M, 0, len(order))
	for _, k := range order {
		list = append(list, out[k])
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i]["last_seen"].(int64) > list[j]["last_seen"].(int64) })
	return list
}

// Contact is the person's contact: the one most of their handles are in, then one with a photo,
// then the latest; nil when none.
func (p *People) Contact(pid int64) *ContactRef {
	cs := p.Contacts[pid]
	if len(cs) == 0 {
		return nil
	}
	count := map[int64]map[int64]bool{}
	for _, c := range cs {
		if count[c.ID] == nil {
			count[c.ID] = map[int64]bool{}
		}
		count[c.ID][c.AddressID] = true
	}
	better := func(a, b ContactRef) bool { // a > b
		if len(count[a.ID]) != len(count[b.ID]) {
			return len(count[a.ID]) > len(count[b.ID])
		}
		if (a.Photo != nil) != (b.Photo != nil) {
			return a.Photo != nil
		}
		return a.Updated > b.Updated
	}
	best := cs[0]
	for _, c := range cs[1:] {
		if better(c, best) {
			best = c
		}
	}
	return &best
}

// Avatar is the contact photo's file name in the cache, if the person has one.
func (p *People) Avatar(pid int64) string {
	if c := p.Contact(pid); c != nil && c.Photo != nil {
		return *c.Photo
	}
	return ""
}

// Addresses are the person's address ids.
func (p *People) Addresses(pid int64) []int64 {
	var out []int64
	for _, h := range p.Handles[pid] {
		out = append(out, h.AddressID)
	}
	return out
}

// Describe is the person's handles for showing: [{kind, value, service, label, address_id}].
func (p *People) Describe(pid int64) []M {
	hs := append([]Handle(nil), p.Handles[pid]...)
	sort.SliceStable(hs, func(i, j int) bool { return handleRank(hs[i].Kind) < handleRank(hs[j].Kind) })
	out := []M{}
	for _, h := range hs {
		out = append(out, M{"kind": h.Kind, "value": h.Value, "service": nullable(h.Service),
			"label": Pretty(h.Kind, h.Value), "address_id": h.AddressID})
	}
	return out
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// OwnName is the owner's name from config ([owner] name).
func OwnName() string { return config.String("owner", "name", "") }

// NameSourcesPresent is the sources of names this archive has: `contacts` with an address book
// enabled, and each service and kind it has names of.
func NameSourcesPresent(q db.Querier) map[string]bool {
	out := map[string]bool{}
	if db.Exists(q, "SELECT 1 FROM plugin_instance WHERE kind = 'contacts' AND enabled") {
		out["contacts"] = true
	}
	db.Each(q, "SELECT DISTINCT s.name, h.kind FROM handle_name h JOIN service s ON s.id = h.service_id", nil,
		func(scan func(...any)) {
			var s, k string
			scan(&s, &k)
			out[s+"/"+k] = true
		})
	return out
}
