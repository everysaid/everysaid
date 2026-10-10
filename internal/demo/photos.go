package demo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"everysaid/internal/archive"
	"everysaid/internal/importers"
)

// photos gives invented people and groups the photos their services showed: someone with only a
// service's photo, someone whose photo changed (an older one in their history), someone with both
// an address book's photo and a service's (a choice), and two groups.
func photos(a *archive.Archive, store *importers.Store, people []*person, dir string, seed, now int64) error {
	n := 0
	keep := func(initials string) (string, error) {
		n++
		f := filepath.Join(dir, fmt.Sprintf("photo%d.jpg", n))
		if err := avatar(f, seed+500+int64(n), initials); err != nil {
			return "", err
		}
		sha := store.Keep(f)
		return sha, os.Remove(f) // the store has it
	}
	initials := func(name string) string {
		out := ""
		for _, w := range strings.Fields(name) {
			out += string([]rune(w)[0])
		}
		return out
	}
	day := int64(86400)
	changed, both, only := false, false, 0
	for i, p := range people {
		hasContactPhoto := p.contact && i%3 != 2
		h := phone(p.number)
		switch {
		case contains(p.services, "whatsapp") && !hasContactPhoto && !changed:
			changed = true
			for _, at := range [][2]int64{{now - 400*day, now - 120*day}, {now - 120*day, now}} {
				sha, err := keep(initials(p.name))
				if err != nil {
					return err
				}
				a.HandlePhoto(h, "whatsapp", sha, at[0])
				a.HandlePhoto(h, "whatsapp", sha, at[1])
			}
		case contains(p.services, "telegram") && hasContactPhoto && !both:
			both = true
			sha, err := keep(initials(p.name))
			if err != nil {
				return err
			}
			a.HandlePhoto(h, "telegram", sha, now-30*day)
			a.HandlePhoto(h, "telegram", sha, now)
		case !hasContactPhoto && only < 3 && (contains(p.services, "telegram") || contains(p.services, "viber")):
			only++
			svc := "telegram"
			if !contains(p.services, svc) {
				svc = "viber"
			}
			sha, err := keep(initials(p.name))
			if err != nil {
				return err
			}
			a.HandlePhoto(h, svc, sha, now-60*day)
			a.HandlePhoto(h, svc, sha, now)
		}
	}
	for _, g := range groups[:2] {
		conv, ok := a.FindConversation(g.service, "demo-group-"+g.title)
		if !ok {
			continue
		}
		sha, err := keep(initials(g.title))
		if err != nil {
			return err
		}
		a.ChatPhoto(conv, g.service, sha, now-200*day)
		a.ChatPhoto(conv, g.service, sha, now)
	}
	return nil
}
