// The routes of people and labels (app.py, "people" and "labels: the user's lists").
package server

import (
	"errors"
	"fmt"
	"path/filepath"

	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

// Avatars is where the people's pictures are kept: <cache>/avatars.
func Avatars() string { return filepath.Join(config.Cache, "avatars") }

// described is a person with their labels (the models' only when the user shows them) and a name
// found for them.
func (s *Server) described(p M) M {
	if p != nil {
		pid, _ := asInt(p["id"])
		p["labels"] = core.PersonLabels(s.Store, pid, s.setting("show_tone", false))
		p["guess"] = core.Guess(s.Store, pid)
		p["analysed"] = core.Analysed(s.Store, pid)
	}
	return p
}

// person is a person described, or a 404.
func (s *Server) person(pid int64) (any, error) {
	p := core.Person(s.Store, pid)
	if p == nil {
		return nil, notFound("")
	}
	return s.described(p), nil
}

// personOrNull is a person described, or null (as the Python answered after a change).
func (s *Server) personOrNull(pid int64) any {
	p := core.Person(s.Store, pid)
	if p == nil {
		return nil
	}
	return s.described(p)
}

// recent is a person's latest messages with text: [{ts, outgoing, text}] (queries._recent).
func recent(s *core.Store, personID int64, n int) []M {
	out := []M{}
	c := core.Index(s).Chats[fmt.Sprintf("p%d", personID)]
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
			out = append(out, M{"ts": ts, "outgoing": o, "text": core.Cut(t, 160)})
		})
	return out
}

// analyser is the local analysis instance in use: the first one turned on and set up, or 0.
func (s *Server) analyser() int64 {
	for _, row := range plugins.Instances(s.Store, "analysis") {
		p := plugins.Get(row.Plugin)
		if row.Enabled && p != nil {
			if ok, _ := plugins.Check(p, plugins.NewContext(s.Host, row)); ok {
				return row.ID
			}
		}
	}
	return 0
}

// optString is a body's field as a core.Opt: absent (left), null (cleared) or text.
func optString(body M, key string) (core.Opt[string], error) {
	v, ok := body[key]
	if !ok {
		return core.Opt[string]{}, nil
	}
	switch x := v.(type) {
	case nil:
		return core.Clear[string](), nil
	case string:
		return core.To(x), nil
	}
	return core.Opt[string]{}, fmt.Errorf("%s: not text", key)
}

func optInt(body M, key string) (core.Opt[int64], error) {
	v, ok := body[key]
	if !ok {
		return core.Opt[int64]{}, nil
	}
	if v == nil {
		return core.Clear[int64](), nil
	}
	n, err := pyInt(v)
	if err != nil {
		return core.Opt[int64]{}, err
	}
	return core.To(n), nil
}

func (s *Server) peopleRoutes() {
	h := s.handle

	// label: only the people with it (theirs by the user, or suggested where the user shows those)
	h("GET /api/people", bodyNone, func(q *req) (any, error) {
		limit, err := q.intQ("limit", 100)
		if err != nil {
			return nil, err
		}
		offset, err := q.intQ("offset", 0)
		if err != nil {
			return nil, err
		}
		label, err := q.intQ("label", 0)
		if err != nil {
			return nil, err
		}
		tagged := core.ByPerson(s.Store, s.setting("show_tone", false))
		var only map[int64]bool
		if label != 0 {
			only = s.labelled(label)
		}
		out := core.PeopleList(s.Store, core.PeopleListOptions{Q: q.str("q"), Limit: clampLimit(limit, 5000), Offset: int(max(0, offset)),
			Unnamed: s.setting("show_unnamed", true) || label != 0, Short: s.setting("show_short_numbers", false) || label != 0,
			Only: only})
		if items, ok := out["items"].([]M); ok {
			for _, p := range items {
				pid, _ := asInt(p["id"])
				if ls := tagged[pid]; ls != nil {
					p["labels"] = ls
				} else {
					p["labels"] = []M{}
				}
			}
		}
		return out, nil
	})

	h("GET /api/people/suggestions", bodyNone, func(q *req) (any, error) {
		limit, err := q.intQ("limit", 50)
		if err != nil {
			return nil, err
		}
		recentToo, err := q.boolQ("recent", false)
		if err != nil {
			return nil, err
		}
		return M{"items": core.MergeSuggestions(s.Store, clampLimit(limit, 2000), recentToo)}, nil
	})

	h("GET /api/people/unnamed", bodyNone, func(q *req) (any, error) {
		limit, err := q.intQ("limit", 50)
		if err != nil {
			return nil, err
		}
		offset, err := q.intQ("offset", 0)
		if err != nil {
			return nil, err
		}
		guessed, err := q.boolQ("guessed", false)
		if err != nil {
			return nil, err
		}
		var first map[int64]bool
		if guessed {
			first = core.Guessed(s.Store)
		}
		out := core.UnnamedPeople(s.Store, clampLimit(limit, 200), int(max(0, offset)), first)
		if items, ok := out["items"].([]M); ok {
			for _, p := range items {
				s.described(p)
			}
		}
		return out, nil
	})

	h("GET /api/people/apart", bodyNone, func(q *req) (any, error) {
		return M{"items": core.MergesDismissed(s.Store)}, nil
	})

	h("POST /api/people/apart/undo", bodyRequired, func(q *req) (any, error) {
		a, err1 := pyInt(q.get("a"))
		b, err2 := pyInt(q.get("b"))
		if err := errors.Join(err1, err2); err != nil {
			return nil, failed(400, err.Error())
		}
		if err := core.UndismissMerge(s.Store, a, b); err != nil {
			return nil, err
		}
		return M{"ok": true}, nil
	})

	// merge: [[person ids], ...]; apart: [[a, b], ...]
	h("POST /api/people/suggestions/apply", bodyRequired, func(q *req) (any, error) {
		var merges [][]int64
		var apart [][2]int64
		bad := func(e error) (any, error) { return nil, failed(400, e.Error()) }
		if list, ok := q.get("merge").([]any); ok {
			for _, g := range list {
				ids, err := ints(g)
				if err != nil {
					return bad(err)
				}
				merges = append(merges, ids)
			}
		} else if truthy(q.get("merge")) {
			return bad(errors.New("merge: not a list"))
		}
		if list, ok := q.get("apart").([]any); ok {
			for _, p := range list {
				ids, err := ints(p)
				if err != nil {
					return bad(err)
				}
				if len(ids) != 2 {
					return bad(fmt.Errorf("apart: a pair of %d", len(ids)))
				}
				apart = append(apart, [2]int64{ids[0], ids[1]})
			}
		} else if truthy(q.get("apart")) {
			return bad(errors.New("apart: not a list"))
		}
		merged, keptApart, err := core.ApplyMerges(s.Store, merges, apart)
		if err != nil {
			return nil, passUser(err, func(e error) error { return failed(400, e.Error()) })
		}
		return M{"merged": merged, "apart": keptApart}, nil
	})

	h("POST /api/people/suggestions/dismiss", bodyRequired, func(q *req) (any, error) {
		ids, err := ints(q.get("people"))
		if err != nil {
			return nil, failed(400, err.Error())
		}
		if err := core.DismissMerge(s.Store, ids); err != nil {
			return nil, err
		}
		return M{"ok": true}, nil
	})

	// recent: with their latest messages, so many (a little of their history, to tell them apart)
	h("GET /api/people/{pid}", bodyNone, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		n, err := q.intQ("recent", 0)
		if err != nil {
			return nil, err
		}
		p, err := s.person(pid)
		if err != nil {
			return nil, err
		}
		if n != 0 {
			p.(M)["recent"] = recent(s.Store, pid, int(max(0, min(n, 20))))
		}
		return p, nil
	})

	// state: yes, no, or null (the user's word taken back)
	h("PUT /api/people/{pid}/labels/{lid}", bodyRequired, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		lid, err := q.pathInt("lid")
		if err != nil {
			return nil, err
		}
		state := ""
		switch v := q.get("state").(type) {
		case nil:
		case string:
			state = v
			if state == "" {
				state = "''" // not one of the states: refused, as the Python did
			}
		default:
			state = pyStr(v)
		}
		if err := core.SetPersonLabel(s.Store, pid, lid, state); err != nil {
			return nil, is404(err, func(e error) error { return failed(400, e.Error()) })
		}
		return s.personOrNull(pid), nil
	})

	// how: models or handle; accept: true (their name) or false (wrong, not suggested again)
	h("POST /api/people/{pid}/guess", bodyRequired, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		how, _ := q.get("how").(string)
		if _, err := core.DecideGuess(s.Store, pid, how, truthy(q.get("accept"))); err != nil {
			return nil, err
		}
		return s.personOrNull(pid), nil
	})

	h("GET /api/analysis", bodyNone, func(q *req) (any, error) {
		iid := s.analyser()
		var inst any
		if iid != 0 {
			inst = iid
		}
		return M{"instance": inst, "running": iid != 0 && s.Host.isRunning(iid) != ""}, nil
	})

	// The person's chat read by the local analysis now (in the background); it says when done.
	h("POST /api/people/{pid}/analyse/now", bodyNone, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		iid := s.analyser()
		if iid == 0 {
			return nil, errs.New("analysis.none", 409, nil)
		}
		if err := s.Host.Run(iid, fmt.Sprintf("person:%d", pid), nil); err != nil {
			return nil, err
		}
		return M{"instance": iid}, nil
	})

	h("POST /api/people/{pid}/analyse", bodyNone, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		if err := core.AnalyseAgain(s.Store, pid); err != nil {
			return nil, err
		}
		return s.person(pid)
	})

	h("GET /api/labels", bodyNone, func(q *req) (any, error) {
		return M{"items": core.Labels(s.Store, ""), "stale": core.Stale(s.Store)}, nil
	})

	h("POST /api/labels", bodyRequired, func(q *req) (any, error) {
		kind, _ := q.get("kind").(string)
		if kind != "tone" && kind != "relation" {
			return nil, failed(400, "kind")
		}
		name, _ := q.get("name").(string)
		lid, err := core.AddLabel(s.Store, kind, name, q.text("meaning"), truthy(q.get("sensitive")))
		if err != nil {
			return nil, passUser(err, func(e error) error { return failed(400, e.Error()) })
		}
		return M{"id": lid, "items": core.Labels(s.Store, "")}, nil
	})

	h("PATCH /api/labels/{lid}", bodyRequired, func(q *req) (any, error) {
		lid, err := q.pathInt("lid")
		if err != nil {
			return nil, err
		}
		name, err1 := optString(q.body, "name")
		meaning, err2 := optString(q.body, "meaning")
		if err := errors.Join(err1, err2); err != nil {
			return nil, failed(400, err.Error())
		}
		var sensitive core.Opt[bool]
		if v, ok := q.body["sensitive"]; ok {
			sensitive = core.To(truthy(v))
		}
		if err := core.EditLabel(s.Store, lid, name, meaning, sensitive); err != nil {
			return nil, is404(err, nil)
		}
		return M{"items": core.Labels(s.Store, "")}, nil
	})

	h("PUT /api/labels/order", bodyRequired, func(q *req) (any, error) {
		ids, err := ints(q.get("ids"))
		if err != nil {
			return nil, failed(400, err.Error())
		}
		if err := core.OrderLabels(s.Store, ids); err != nil {
			return nil, err
		}
		return M{"items": core.Labels(s.Store, "")}, nil
	})

	h("DELETE /api/labels/{lid}", bodyNone, func(q *req) (any, error) {
		lid, err := q.pathInt("lid")
		if err != nil {
			return nil, err
		}
		if err := core.RemoveLabel(s.Store, lid); err != nil {
			return nil, is404(err, nil)
		}
		s.Auth.Log(&q.uid, "label removed", itoa(lid))
		return M{"items": core.Labels(s.Store, "")}, nil
	})

	// This label becomes `into`.
	h("POST /api/labels/{lid}/merge", bodyRequired, func(q *req) (any, error) {
		lid, err := q.pathInt("lid")
		if err != nil {
			return nil, err
		}
		into, err := pyInt(q.get("into"))
		if err != nil {
			return nil, failed(400, err.Error())
		}
		if err := core.MergeLabels(s.Store, into, lid); err != nil {
			return nil, passUser(err, func(e error) error { return failed(400, e.Error()) })
		}
		s.Auth.Log(&q.uid, "label merged", fmt.Sprintf("%d into %d", lid, into))
		return M{"items": core.Labels(s.Store, "")}, nil
	})

	h("PATCH /api/people/{pid}", bodyRequired, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		name, err1 := optString(q.body, "name")
		note, err2 := optString(q.body, "note")
		source, err3 := optString(q.body, "name_source")
		if err := errors.Join(err1, err2, err3); err != nil {
			return nil, failed(400, err.Error())
		}
		if err := core.SetPerson(s.Store, pid, name, note, source); err != nil {
			return nil, is404(err, func(e error) error { return failed(400, e.Error()) })
		}
		return s.personOrNull(pid), nil
	})

	h("POST /api/people/{pid}/merge", bodyRequired, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		other, err := pyInt(q.get("other"))
		if err != nil {
			return nil, failed(400, err.Error())
		}
		if _, err := core.MergePeople(s.Store, pid, other); err != nil {
			return nil, passUser(err, func(e error) error { return failed(400, e.Error()) })
		}
		return s.personOrNull(pid), nil
	})

	h("POST /api/addresses/{aid}/split", bodyNone, func(q *req) (any, error) {
		aid, err := q.pathInt("aid")
		if err != nil {
			return nil, err
		}
		pid, err := core.SplitAddress(s.Store, aid)
		if err != nil {
			return nil, is404(err, nil)
		}
		return M{"person_id": pid}, nil
	})

	h("GET /api/avatar/{pid}", bodyNone, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		f := core.PeopleOf(s.Store).Avatar(pid)
		if f == "" {
			return nil, notFound("no_photo")
		}
		if err := serveFile(q.w, q.r, filepath.Join(Avatars(), f), "", "private, max-age=86400"); err != nil {
			return nil, notFound("no_photo")
		}
		return done, nil
	})
}
