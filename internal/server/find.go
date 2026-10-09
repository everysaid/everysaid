// Finding a person on the services the archive has no conversation with them on yet: each source
// that can say who has an account (plugins.Finder) is asked at once, by the person's numbers, and
// each answer goes to the apps as it comes ("reach" events). What was found is kept a while, for a
// first message there (Send).
package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

// findFor is how long a source is waited for; foundFor how long what it said is kept.
const (
	findFor  = 30 * time.Second
	foundFor = time.Hour
)

// reach is where a person's chat may be written to on one service: "asking", "found", "none" or
// "failed"; found, through which instance and conversation key; asking, of how many sources still.
type reach struct {
	State   string
	iid     int64
	key     string
	at      time.Time
	pending int
	failed  bool
}

// Find asks, for a person's chat, each source that can send and find whether the person has an
// account on its services the chat has none of yet; it returns those services, each answer
// following as a "reach" event {chat, service, state}.
func (h *Host) Find(chatID string) ([]string, error) {
	c := core.ChatOf(h.store, chatID)
	if c == nil {
		return nil, core.ErrNotFound
	}
	if c.Type != "person" {
		return nil, errs.New("chat.not_a_person", 409, nil)
	}
	phones := h.phones(c.PersonID)
	if len(phones) == 0 {
		return nil, errs.New("chat.no_phone", 409, nil)
	}
	now := time.Now()
	h.mu.Lock()
	if h.reach == nil {
		h.reach = map[string]map[string]*reach{}
	}
	got := map[string]*reach{}
	for svc, r := range h.reach[chatID] { // what was said stays while asked again (not what gave no answer)
		if (r.State == "found" || r.State == "none") && now.Sub(r.at) < foundFor {
			got[svc] = r
		}
	}
	type ask struct {
		s        sender
		services []string
	}
	var asks []ask
	for _, s := range h.Senders() {
		if _, ok := s.p.(plugins.Finder); !ok {
			continue
		}
		var services []string
		for _, svc := range s.p.Info().Services {
			if c.Services[svc] || got[svc] != nil && (got[svc].State == "found" || got[svc].State == "none") {
				continue
			}
			if got[svc] == nil {
				got[svc] = &reach{State: "asking", at: now}
			}
			got[svc].pending++
			services = append(services, svc)
		}
		if len(services) > 0 {
			asks = append(asks, ask{s, services})
		}
	}
	h.reach[chatID] = got
	out := make([]string, 0, len(got))
	for svc := range got {
		out = append(out, svc)
	}
	h.mu.Unlock()
	sort.Strings(out)
	for _, a := range asks {
		go h.ask(chatID, a.s, a.services, phones, got)
	}
	return out, nil
}

// ask is one source's answer for a chat: a service is "found" as soon as one source found it,
// "none" (or "failed", where one could not say) once every source asked for it answered.
func (h *Host) ask(chatID string, s sender, services, phones []string, rs map[string]*reach) {
	found := map[string]plugins.Found{}
	pc, err := h.Ctx(s.iid)
	if err == nil {
		ctx, cancel := context.WithTimeout(h.lifetime(), findFor)
		var got []plugins.Found
		got, err = func() (got []plugins.Found, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("%v", r)
				}
			}()
			return s.p.(plugins.Finder).Find(ctx, pc, phones)
		}()
		cancel()
		for _, f := range got {
			if _, ok := found[f.Service]; !ok && f.Key != "" && contains(services, f.Service) {
				found[f.Service] = f
			}
		}
	}
	if err != nil {
		h.log.Warn("finding a person", "instance", s.iid, "error", err)
	}
	now := time.Now()
	var events []M
	h.mu.Lock()
	for _, svc := range services {
		r := rs[svc]
		if r.State != "asking" {
			continue // found by another source
		}
		r.pending--
		r.failed = r.failed || err != nil
		if f, ok := found[svc]; ok {
			*r = reach{State: "found", iid: s.iid, key: f.Key, at: now}
		} else if r.pending == 0 {
			r.State = map[bool]string{false: "none", true: "failed"}[r.failed]
		}
		if r.State != "asking" && h.reach[chatID][svc] == r { // not asked again since
			events = append(events, M{"type": "reach", "chat": chatID, "service": svc, "state": r.State})
		}
	}
	h.mu.Unlock()
	for _, e := range events {
		h.Emit(e)
	}
}

// lifetime is the server's context (a background one before Start).
func (h *Host) lifetime() context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx == nil {
		return context.Background()
	}
	return h.ctx
}

// Reach is what was asked for a chat lately: {service: state}.
func (h *Host) Reach(chatID string) map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]string{}
	for svc, r := range h.reach[chatID] {
		if (r.State != "found" && r.State != "none") || time.Since(r.at) < foundFor {
			out[svc] = r.State
		}
	}
	return out
}

// reached is where a first message to a chat on a service goes: the instance and key found.
func (h *Host) reached(chatID, service string) (int64, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.reach[chatID][service]
	if r == nil || r.State != "found" || time.Since(r.at) >= foundFor {
		return 0, "", false
	}
	return r.iid, r.key, true
}

// forget drops what was found for a chat on a service, once it has a conversation there.
func (h *Host) forget(chatID, service string) {
	h.mu.Lock()
	delete(h.reach[chatID], service)
	h.mu.Unlock()
}

// conversationOf is the conversation of a service with this key (0: none yet).
func (h *Host) conversationOf(service, key string) int64 {
	return db.Int(h.store.Read(), "SELECT c.id FROM conversation c JOIN service s ON s.id = c.service_id "+
		"WHERE s.name = ? AND c.key = ?", service, strings.TrimSpace(key))
}

// phones are a person's numbers (+E.164, not a short code).
func (h *Host) phones(personID int64) []string {
	return db.Strs(h.store.Read(), "SELECT a.value FROM address a JOIN address_kind k ON k.id = a.kind_id "+
		"JOIN person_address pa ON pa.address_id = a.id WHERE pa.person_id = ? AND k.name = 'phone' "+
		"AND a.value LIKE '+%' AND length(a.value) >= 9 ORDER BY a.value", personID)
}

// Findable says whether a chat is a person's with a number, who could be looked for on a service the
// chat has none of: a source that can send and find reaches one not asked lately (found there, or
// said not to be: nothing more to look for; one that gave no answer may be asked again).
func (h *Host) Findable(chatID string) bool {
	c := core.ChatOf(h.store, chatID)
	if c == nil || c.Type != "person" || len(h.phones(c.PersonID)) == 0 {
		return false
	}
	said := h.Reach(chatID)
	for _, s := range h.Senders() {
		if _, ok := s.p.(plugins.Finder); ok {
			for _, svc := range s.p.Info().Services {
				if !c.Services[svc] && said[svc] != "found" && said[svc] != "none" {
					return true
				}
			}
		}
	}
	return false
}
