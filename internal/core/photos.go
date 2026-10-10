package core

// Photos of people and groups, chosen when read, as names are (names.go): a person's photo is the
// one the user pinned (`person.photo_source`: 'contacts', a service, 'address:<id>' for one of
// their handles, 'none' for initials), else from the first source that has one in the order of
// names (setting `name_order`, then the plugins' weights): an address book's photo at the place of
// `contacts`, a service's at the place of `<service>/profile` (a service with no such name after
// all of them). Within a service, the latest photo of each handle (handle_photo); the one most of
// their handles share, then the latest set, wins. A photo whose file is not there gives way to the
// one before it of that handle, then to the next source; with none, initials. A group's photo is
// its conversations' latest (chat_photo; a group merged across services chooses by the same
// order).

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
)

// Photo is a photo to show: a contact's (ContactID) or a service's (Sha), and its file.
type Photo struct {
	Source    string // "contacts" or the service
	AddressID int64  // the handle whose photo it is (0: a contact's)
	ContactID int64
	Sha       string
	File      string
	First     int64 // when it was first seen (a contact's: when the contact changed)
}

// photoRow is one row of handle_photo or chat_photo.
type photoRow struct {
	owner       int64 // the address, or the conversation
	service     string
	sha, path   string // path: the media's, "" where the archive has no such file
	first, last int64
	removed     *int64
}

// AvatarsDir is where the address books' photos are kept: <cache>/avatars.
func AvatarsDir() string { return filepath.Join(config.Cache, "avatars") }

func mediaFile(path string) string {
	if path == "" {
		return ""
	}
	f := filepath.Join(archive.MediaRoot(), filepath.FromSlash(path))
	if st, err := os.Stat(f); err != nil || !st.Mode().IsRegular() {
		return ""
	}
	return f
}

// loadPhotoRows reads handle_photo (by person) or chat_photo (by conversation): each owner's rows,
// the latest first.
func loadPhotoRows(q db.Querier, table, column, join string) map[int64][]photoRow {
	out := map[int64][]photoRow{}
	db.Each(q, "SELECT "+join+", p."+column+", s.name, coalesce(p.sha256, ''), coalesce(md.path, ''), p.first_seen, p.last_seen, p.removed_at "+
		"FROM "+table+" p JOIN service s ON s.id = p.service_id LEFT JOIN media md ON md.sha256 = p.sha256 "+
		map[string]string{"handle_photo": "JOIN person_address pa ON pa.address_id = p.address_id ", "chat_photo": ""}[table]+
		"ORDER BY p.first_seen DESC, p.id DESC", nil, func(scan func(...any)) {
		var key int64
		var r photoRow
		var removed sql.NullInt64
		scan(&key, &r.owner, &r.service, &r.sha, &r.path, &r.first, &r.last, &removed)
		if removed.Valid {
			r.removed = &removed.Int64
		}
		out[key] = append(out[key], r)
	})
	return out
}

// photoOrder is the sources of photos in the order of names: contacts, and the services whose
// profile names are placed; then any other service with photos, by name.
func photoOrder(order []string, services map[string]bool) []string {
	var out []string
	placed := map[string]bool{}
	for _, k := range order {
		src := ""
		if k == "contacts" {
			src = "contacts"
		} else if svc, ok := strings.CutSuffix(k, "/profile"); ok {
			src = svc
		}
		if src != "" && !placed[src] {
			placed[src] = true
			out = append(out, src)
		}
	}
	var rest []string
	for svc := range services {
		if !placed[svc] {
			rest = append(rest, svc)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// current is, for each owner of the rows (of one service), its photo now: the latest row's, or the
// one before it where its file is not there; none where the latest says none.
func current(rows []photoRow, service string, only int64) []photoRow {
	var out []photoRow
	done := map[int64]bool{}
	for _, r := range rows { // the latest first
		if r.service != service || done[r.owner] || (only != 0 && r.owner != only) {
			continue
		}
		if r.sha == "" {
			done[r.owner] = true // none now
			continue
		}
		if f := mediaFile(r.path); f != "" {
			r.path = f
			out = append(out, r)
			done[r.owner] = true
		}
	}
	// the photo most of them share, then the latest set
	count := map[string]int{}
	for _, r := range out {
		count[r.sha]++
	}
	sort.SliceStable(out, func(i, j int) bool {
		if count[out[i].sha] != count[out[j].sha] {
			return count[out[i].sha] > count[out[j].sha]
		}
		return out[i].first > out[j].first
	})
	return out
}

// photoCandidates are a person's photos from one source (only: of that handle alone), best first.
func (p *People) photoCandidates(pid int64, source string, only int64) []Photo {
	var out []Photo
	if source == "contacts" {
		cs := append([]ContactRef(nil), p.Contacts[pid]...)
		best := p.Contact(pid)
		sort.SliceStable(cs, func(i, j int) bool {
			if (best != nil && cs[i].ID == best.ID) != (best != nil && cs[j].ID == best.ID) {
				return best != nil && cs[i].ID == best.ID
			}
			return cs[i].Updated > cs[j].Updated
		})
		seen := map[int64]bool{}
		for _, c := range cs {
			if c.Photo == nil || seen[c.ID] || (only != 0 && c.AddressID != only) || !filepath.IsLocal(*c.Photo) {
				continue
			}
			f := filepath.Join(AvatarsDir(), *c.Photo)
			if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
				seen[c.ID] = true
				out = append(out, Photo{Source: "contacts", ContactID: c.ID, AddressID: c.AddressID, File: f, First: c.Updated})
			}
		}
		return out
	}
	for _, r := range current(p.HandlePhotos[pid], source, only) {
		out = append(out, Photo{Source: source, AddressID: r.owner, Sha: r.sha, File: r.path, First: r.first})
	}
	return out
}

// PhotoSources are the sources of a person's photos, in the order they are chosen.
func (p *People) PhotoSources(pid int64) []string {
	services := map[string]bool{}
	for _, r := range p.HandlePhotos[pid] {
		services[r.service] = true
	}
	return photoOrder(p.Order, services)
}

// Photo is the person's photo, nil for none (initials).
func (p *People) Photo(pid int64) *Photo {
	p.mu.Lock()
	if ph, ok := p.photo[pid]; ok {
		p.mu.Unlock()
		return ph
	}
	p.mu.Unlock()
	ph := p.choosePhoto(pid, p.PhotoPinned[pid])
	p.mu.Lock()
	p.photo[pid] = ph
	p.mu.Unlock()
	return ph
}

// choosePhoto is the photo a pin gives ("" by the order), else the order's.
func (p *People) choosePhoto(pid int64, pin string) *Photo {
	if pin == "none" {
		return nil
	}
	sources := p.PhotoSources(pid)
	var only int64
	if a, ok := strings.CutPrefix(pin, "address:"); ok {
		only, _ = strconv.ParseInt(a, 10, 64)
	} else if pin != "" {
		if c := p.photoCandidates(pid, pin, 0); len(c) > 0 {
			return &c[0]
		}
	}
	if only != 0 {
		for _, src := range sources {
			if c := p.photoCandidates(pid, src, only); len(c) > 0 {
				return &c[0]
			}
		}
	}
	for _, src := range sources { // the pinned one has none: by the order
		if c := p.photoCandidates(pid, src, 0); len(c) > 0 {
			return &c[0]
		}
	}
	return nil
}

// PhotoChoice is the photo a pin would show (for the user's choice; "auto": by the order, "": the one
// shown), nil for none.
func (p *People) PhotoChoice(pid int64, pin string) *Photo {
	switch pin {
	case "":
		return p.Photo(pid)
	case "auto": // what the order gives, whatever is pinned
		return p.choosePhoto(pid, "")
	case "none":
		return nil
	}
	if a, ok := strings.CutPrefix(pin, "address:"); ok {
		only, _ := strconv.ParseInt(a, 10, 64)
		for _, src := range p.PhotoSources(pid) {
			if c := p.photoCandidates(pid, src, only); len(c) > 0 {
				return &c[0]
			}
		}
		return nil
	}
	if c := p.photoCandidates(pid, pin, 0); len(c) > 0 {
		return &c[0]
	}
	return nil
}

// Avatar is a key of the person's photo ("" for none): it changes when the photo does.
func (p *People) Avatar(pid int64) string {
	ph := p.Photo(pid)
	switch {
	case ph == nil:
		return ""
	case ph.Sha != "":
		return ph.Sha[:12]
	}
	if st, err := os.Stat(ph.File); err == nil {
		return fmt.Sprintf("c%d-%d", ph.ContactID, st.ModTime().Unix())
	}
	return fmt.Sprintf("c%d", ph.ContactID)
}

// PhotoChoices are what the user may pin a person's photo to: each source with a photo, and each
// handle with one of its own where the source has more than one: [{pin, source, address_id,
// label}], in the order of choice.
func (p *People) PhotoChoices(pid int64) []M {
	out := []M{}
	label := map[int64]string{}
	for _, h := range p.Handles[pid] {
		label[h.AddressID] = Pretty(h.Kind, h.Value)
	}
	for _, src := range p.PhotoSources(pid) {
		cands := p.photoCandidates(pid, src, 0)
		if len(cands) == 0 {
			continue
		}
		out = append(out, M{"pin": src, "source": src, "address_id": nil, "label": nil})
		if src == "contacts" || len(cands) < 2 {
			continue
		}
		for _, c := range cands {
			out = append(out, M{"pin": fmt.Sprintf("address:%d", c.AddressID), "source": src, "address_id": c.AddressID,
				"label": nullable(label[c.AddressID])})
		}
	}
	return out
}

// PhotoHistory is every photo the services have shown for the person's handles, the latest first:
// [{source, address_id, label, sha, first_seen, last_seen, removed_at, current, available}].
func (p *People) PhotoHistory(pid int64) []M {
	label := map[int64]string{}
	for _, h := range p.Handles[pid] {
		label[h.AddressID] = Pretty(h.Kind, h.Value)
	}
	latest := map[[2]any]bool{}
	out := []M{}
	for _, r := range p.HandlePhotos[pid] { // the latest first
		k := [2]any{r.owner, r.service}
		isLatest := !latest[k]
		latest[k] = true
		if r.sha == "" {
			continue
		}
		var removed any
		if r.removed != nil {
			removed = *r.removed
		}
		out = append(out, M{"source": r.service, "address_id": r.owner, "label": nullable(label[r.owner]), "sha": r.sha,
			"first_seen": r.first, "last_seen": r.last, "removed_at": removed, "current": isLatest,
			"available": mediaFile(r.path) != ""})
	}
	return out
}

// groupPhotos are the conversations' photo rows, by conversation, the latest first.
func groupPhotos(s *Store) map[int64][]photoRow {
	return Cached(s, "group_photos", func() map[int64][]photoRow {
		return loadPhotoRows(s.Read(), "chat_photo", "conversation_id", "p.conversation_id")
	})
}

// GroupPhoto is a group chat's photo (nil for none): its conversations' latest, by the order of the
// services.
func GroupPhoto(s *Store, chat *Chat) *Photo {
	if chat == nil || chat.Type == "person" {
		return nil
	}
	all := groupPhotos(s)
	var rows []photoRow
	services := map[string]bool{}
	for _, cid := range chat.Conversations {
		for _, r := range all[cid] {
			rows = append(rows, r)
			services[r.service] = true
		}
	}
	if len(rows) == 0 {
		return nil
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].first > rows[j].first })
	for _, src := range photoOrder(PeopleOf(s).Order, services) {
		if c := current(rows, src, 0); len(c) > 0 {
			return &Photo{Source: src, Sha: c[0].sha, File: c[0].path, First: c[0].first}
		}
	}
	return nil
}

// GroupAvatar is a key of a group chat's photo ("" for none).
func GroupAvatar(s *Store, chat *Chat) string {
	if ph := GroupPhoto(s, chat); ph != nil {
		return ph.Sha[:12]
	}
	return ""
}
