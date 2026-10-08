// The routes of calls, media, statistics, plugins, devices, settings and push (app.py, from "calls,
// media, timeline, stats" on), and the router itself.
package server

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/mcp"
	"everysaid/internal/plugins"
)

// handle adds a route.
func (s *Server) handle(pattern string, body routeOpt, fn handler) {
	s.mux.HandleFunc(pattern, s.wrap(body, fn))
}

func (s *Server) routes() {
	s.authRoutes()
	s.chatRoutes()
	s.peopleRoutes()
	s.spamRoutes()
	s.miscRoutes()
	s.pluginRoutes()
	s.mux.HandleFunc("GET /api/events", s.events)
	// the MCP server over HTTP, for an assistant on another machine: its own token (`everysaid user
	// mcp-token`, or Settings), no cookie, no origin
	mcpHandler := mcp.Handler(s.mcpEnv)
	for _, m := range []string{"GET", "POST", "DELETE"} {
		s.mux.Handle(m+" /mcp", mcpHandler)
	}
	s.mux.HandleFunc("GET /{path...}", s.spa)
}

const cacheImmutable = "private, max-age=604800, immutable"

func (s *Server) miscRoutes() {
	h := s.handle

	h("GET /api/calls", bodyNone, func(q *req) (any, error) {
		missed, err := q.boolQ("missed", false)
		if err != nil {
			return nil, err
		}
		before, err := q.intQ("before", 0)
		if err != nil {
			return nil, err
		}
		limit, err := q.intQ("limit", 60)
		if err != nil {
			return nil, err
		}
		out, err := core.Calls(s.Store, core.CallsOptions{ChatID: q.str("chat"), Missed: missed, Service: q.str("service"),
			Before: before, Limit: clampLimit(limit, 200), Unnamed: true,
			Short: s.setting("show_short_numbers", false)})
		if err != nil {
			return nil, is404(err, nil)
		}
		return out, nil
	})

	h("GET /api/media", bodyNone, func(q *req) (any, error) {
		before, err := q.intQ("before", 0)
		if err != nil {
			return nil, err
		}
		limit, err := q.intQ("limit", 60)
		if err != nil {
			return nil, err
		}
		available, err := q.boolQ("available", false)
		if err != nil {
			return nil, err
		}
		kind := "all"
		if q.has("kind") {
			kind = q.str("kind")
		}
		out, err := core.Media(s.Store, core.MediaOptions{ChatID: q.str("chat"), Kind: kind, Before: before,
			Limit: clampLimit(limit, 200), AvailableOnly: available})
		if err != nil {
			return nil, is404(err, nil)
		}
		return out, nil
	})

	h("POST /api/media/{sha}/decision", bodyRequired, func(q *req) (any, error) {
		decision := ""
		if v := q.get("decision"); v != nil {
			decision = pyStr(v)
		}
		date, err := optInt(q.body, "date_ms")
		if err != nil {
			return nil, failed(400, err.Error())
		}
		if err := core.DecideMedia(s.Store, q.r.PathValue("sha"), decision, date.Value); err != nil {
			return nil, passUser(err, func(e error) error { return failed(400, e.Error()) })
		}
		if decision == "remove" { // what the user chose to delete (carried out later, outside the app)
			s.Auth.Log(&q.uid, "media to remove", pyCut(q.r.PathValue("sha"), 12))
		}
		return M{"ok": true}, nil
	})

	h("POST /api/media/{sha}/library", bodyOptional, func(q *req) (any, error) {
		sha := q.r.PathValue("sha")
		var iid int64
		if v := q.get("instance_id"); truthy(v) {
			n, err := pyInt(v)
			if err != nil {
				return nil, failed(409, err.Error())
			}
			iid = n
		}
		date, err := optInt(q.body, "date_ms")
		if err != nil {
			return nil, failed(409, err.Error())
		}
		out, err := s.Host.ToLibrary(sha, iid, date.Value)
		if err != nil {
			return nil, passUser(err, func(e error) error { return failed(409, e.Error()) })
		}
		if err := core.DecideMedia(s.Store, sha, "library", nil); err != nil {
			return nil, passUser(err, func(e error) error { return failed(409, e.Error()) })
		}
		return out, nil
	})

	h("GET /api/media/{sha}/{size}", bodyNone, func(q *req) (any, error) {
		sha, size := q.r.PathValue("sha"), q.r.PathValue("size")
		if (size != "thumb" && size != "preview" && size != "original") || !isAlnum(sha) {
			return nil, notFound("")
		}
		if local := s.Host.LocalFile(sha); local != "" {
			if size == "original" {
				return done, serveFile(q.w, q.r, local, "", cacheImmutable)
			}
			if thumb, typ := MakeThumb(local, sha, size); thumb != "" {
				return done, serveFile(q.w, q.r, thumb, typ, cacheImmutable)
			}
			return nil, notFound("no_preview")
		}
		want := "original"
		if size == "thumb" {
			want = "thumbnail"
		} else if size == "preview" {
			want = "preview"
		}
		got := s.Host.Fetch(sha, want)
		if got == nil {
			return nil, notFound("file_gone")
		}
		if got.Path != "" {
			if size != "original" {
				if thumb, typ := MakeThumb(got.Path, sha, size); thumb != "" {
					return done, serveFile(q.w, q.r, thumb, typ, cacheImmutable)
				}
			}
			return done, serveFile(q.w, q.r, got.Path, "", cacheImmutable)
		}
		typ := got.Type
		if typ == "" {
			typ = "application/octet-stream"
		}
		q.w.Header().Set("Content-Type", typ)
		q.w.Header().Set("Cache-Control", cacheImmutable)
		inert(q.w.Header(), typ)
		q.w.Write(got.Data)
		return done, nil
	})

	h("GET /api/stats", bodyNone, func(q *req) (any, error) {
		archived, err := q.boolQ("archived", false)
		if err != nil {
			return nil, err
		}
		return core.Stats(s.Store, archived), nil
	})

	h("GET /api/devices", bodyNone, func(q *req) (any, error) {
		return M{"items": core.Devices(s.Store)}, nil
	})

	h("PATCH /api/devices/{did}", bodyRequired, func(q *req) (any, error) {
		did, err := q.pathInt("did")
		if err != nil {
			return nil, err
		}
		from, err1 := optInt(q.body, "used_from")
		until, err2 := optInt(q.body, "used_until")
		if err1 != nil || err2 != nil {
			return nil, failed(400, "used_from, used_until")
		}
		if err := core.SetDevicePeriod(s.Store, did, from, until); err != nil {
			return nil, is404(err, nil)
		}
		return M{"items": core.Devices(s.Store)}, nil
	})

	h("GET /api/settings", bodyNone, func(q *req) (any, error) {
		out := core.Settings(s.Store)
		out["name_order"] = core.NameOrder(s.Store.Read())
		return out, nil
	})

	// The sources of names: in the order in use, and the plugins' default order.
	h("GET /api/names", bodyNone, func(q *req) (any, error) {
		known := map[string]M{}
		sources := plugins.NameSources(q.lang())
		for _, x := range sources {
			known[x["id"].(string)] = x
		}
		present := core.NameSourcesPresent(s.Store.Read()) // only what this archive has, or may have
		order, def := []M{}, []M{}
		for _, k := range core.NameOrder(s.Store.Read()) {
			if x, ok := known[k]; ok && present[k] {
				order = append(order, x)
			}
		}
		for _, x := range sources {
			if present[x["id"].(string)] {
				def = append(def, x)
			}
		}
		return M{"order": order, "default": def, "custom": core.Settings(s.Store)["name_order"] != nil}, nil
	})

	h("GET /api/services", bodyNone, func(q *req) (any, error) {
		return plugins.Services(q.lang()), nil
	})

	// The services the archive has anything of, hidden or not: [{id, messages, calls, hidden,
	// accounts}]; accounts: the owner's on it that chats were on, [{id (address), label, chats, hidden}].
	h("GET /api/services/used", bodyNone, func(q *req) (any, error) {
		used := core.Cached(s.Store, "services_used", func() servicesUsed { return buildServicesUsed(s.Store) })
		hidden := map[string]bool{}
		for _, x := range stringList(s.Store.SettingAny("hidden_services", nil)) {
			hidden[x] = true
		}
		hiddenAccounts := map[int64]bool{}
		if list, ok := s.Store.SettingAny("hidden_accounts", nil).([]any); ok {
			for _, x := range list {
				if n, ok := asInt(x); ok {
					hiddenAccounts[n] = true
				}
			}
		}
		items := []M{}
		for _, k := range used.order {
			accounts := []M{}
			for _, a := range used.accounts[k] {
				accounts = append(accounts, M{"id": a.id, "label": a.label, "chats": a.chats, "hidden": hiddenAccounts[a.id]})
			}
			items = append(items, M{"id": k, "messages": used.counts[k][0], "calls": used.counts[k][1], "hidden": hidden[k],
				"accounts": accounts})
		}
		return M{"items": items}, nil
	})

	h("PUT /api/settings", bodyRequired, func(q *req) (any, error) {
		simple := map[string]bool{"theme": true, "language": true, "push_preview": true, "density": true, "send_enter": true,
			"unread_since": true, "show_tone": true, "mcp_labels": true, "hide_empty_groups": true,
			"show_short_numbers": true}
		weights := map[string]bool{}
		for _, w := range plugins.NameWeights() {
			weights[w.Key] = true
		}
		keys := make([]string, 0, len(q.body))
		for k := range q.body {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := q.body[k]
			ok := false
			switch {
			case simple[k]:
				ok = true
			case k == "hidden_services":
				ok = allOf(v, func(x any) bool { _, isStr := x.(string); return isStr })
			case k == "hidden_accounts":
				ok = allOf(v, func(x any) bool { _, isInt := x.(int64); return isInt })
			case k == "name_order":
				if v == nil {
					ok = true // back to the plugins' defaults
				} else if allOf(v, func(x any) bool { _, isStr := x.(string); return isStr }) {
					seen := map[string]bool{}
					ok = true
					for _, x := range v.([]any) {
						if seen[x.(string)] || !weights[x.(string)] {
							ok = false
						}
						seen[x.(string)] = true
					}
				}
			}
			if ok {
				if err := core.SetSetting(s.Store, k, v); err != nil {
					return nil, err
				}
			}
		}
		return core.Settings(s.Store), nil
	})

	h("GET /api/push/key", bodyNone, func(q *req) (any, error) {
		key, err := s.Push.Key()
		if err != nil {
			return nil, err
		}
		return M{"key": key}, nil
	})

	h("POST /api/push/subscribe", bodyRequired, func(q *req) (any, error) {
		endpoint, _ := q.get("endpoint").(string)
		if !pushEndpointOK(q.r.Context(), endpoint) { // the server will post to it: only a push service on the internet
			return nil, errs.New("push.bad_subscription", 400, nil)
		}
		s.Push.Subscribe(q.uid, q.body)
		return M{"ok": true}, nil
	})

	h("POST /api/push/unsubscribe", bodyRequired, func(q *req) (any, error) {
		s.Push.Unsubscribe(q.uid, q.get("endpoint"))
		return M{"ok": true}, nil
	})

	h("POST /api/push/test", bodyNone, func(q *req) (any, error) {
		subs := s.Push.Subscriptions(s.Store.Path)
		if len(subs) == 0 {
			return nil, errs.New("push.no_devices", 409, nil)
		}
		s.Push.Send(subs, []map[string]any{{"title": "Everysaid", "body": i18n.Tr("Test notification", s.Store.Language()),
			"chat": nil, "tag": "test"}})
		return M{"ok": true, "devices": len(subs)}, nil
	})

	if s.opts.Demo {
		// Only in the demo: a message arrives in a chat, from the other side (for trying and tests).
		h("POST /api/demo/incoming", bodyRequired, func(q *req) (any, error) {
			if s.opts.DemoIncoming == nil {
				return nil, notFound("")
			}
			c := core.Index(s.Store).Chats[q.text("chat")]
			if c == nil {
				return nil, notFound("")
			}
			var convs []int64
			for _, cid := range c.Conversations {
				if db.Exists(s.Store.Read(), "SELECT 1 FROM conversation x JOIN service s ON s.id = x.service_id "+
					"WHERE x.id = ? AND s.name = ?", cid, q.get("service")) {
					convs = append(convs, cid)
				}
			}
			if len(convs) == 0 {
				convs = c.Conversations
			}
			if len(convs) == 0 {
				return nil, notFound("")
			}
			text := q.text("text")
			if text == "" {
				text = "…"
			}
			mid, err := s.opts.DemoIncoming(s.Host, convs[0], text)
			if err != nil {
				return nil, err
			}
			return M{"id": mid}, nil
		})
	}
}

func isAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func allOf(v any, ok func(any) bool) bool {
	list, isList := v.([]any)
	if !isList {
		return false
	}
	for _, x := range list {
		if !ok(x) {
			return false
		}
	}
	return true
}

func stringList(v any) []string {
	var out []string
	if list, ok := v.([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

type account struct {
	id    int64
	label string
	chats int64
}

type servicesUsed struct {
	order    []string // the most used first
	counts   map[string][2]int64
	accounts map[string][]account
}

func buildServicesUsed(s *core.Store) servicesUsed {
	q := s.Read()
	out := servicesUsed{counts: map[string][2]int64{}, accounts: map[string][]account{}}
	var seen []string
	add := func(name string, i int, n int64) {
		c, ok := out.counts[name]
		if !ok {
			seen = append(seen, name)
		}
		c[i] = n
		out.counts[name] = c
	}
	db.Each(q, "SELECT s.name, count(*) FROM message m JOIN service s ON s.id = m.service_id GROUP BY 1", nil, func(scan func(...any)) {
		var name string
		var n int64
		scan(&name, &n)
		add(name, 0, n)
	})
	db.Each(q, "SELECT s.name, count(*) FROM call c JOIN service s ON s.id = c.service_id GROUP BY 1", nil, func(scan func(...any)) {
		var name string
		var n int64
		scan(&name, &n)
		add(name, 1, n)
	})
	db.Each(q, "SELECT s.name, cm.address_id, a.value, count(*) FROM conversation_member cm "+
		"JOIN conversation c ON c.id = cm.conversation_id JOIN service s ON s.id = c.service_id "+
		"JOIN address a ON a.id = cm.address_id WHERE cm.address_id IN (SELECT address_id FROM account) "+
		"GROUP BY 1, 2 ORDER BY 4 DESC", nil, func(scan func(...any)) {
		var name string
		var a account
		var label sql.NullString
		scan(&name, &a.id, &label, &a.chats)
		a.label = label.String
		out.accounts[name] = append(out.accounts[name], a)
	})
	sort.SliceStable(seen, func(i, j int) bool {
		a, b := out.counts[seen[i]], out.counts[seen[j]]
		return a[0]+a[1] > b[0]+b[1]
	})
	out.order = seen
	return out
}

// --- plugins -------------------------------------------------------------------------------------

// checked: settings whose values look as they must (a browser may fill a field with anything).
func (s *Server) checked(p plugins.Plugin, settings M, lang string) error {
	for _, st := range p.Info().Settings {
		if v, ok := settings[st.Key]; ok && !st.Valid(v) {
			value := pyStr(v)
			return errs.New("settings.invalid", 400, M{"field": i18n.Tr(st.Label, lang), "value": value})
		}
	}
	return nil
}

// keepSecrets keeps the secrets given (fields of type secret, and those an option keeps); and takes
// away those of an option no longer chosen (the user chose not to keep them).
func (s *Server) keepSecrets(p plugins.Plugin, iid int64, body M) error {
	ctx, err := s.Host.Ctx(iid)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, st := range p.Info().Settings {
		if st.Type == "secret" {
			wanted[st.Key] = true
		}
		for _, k := range st.Keeps {
			wanted[k.Key] = true
		}
	}
	if secrets, ok := body["secrets"].(map[string]any); ok {
		for k, v := range secrets {
			if text, isStr := v.(string); isStr && wanted[k] && text != "" {
				if _, err := ctx.SaveSecret(k, text); err != nil {
					return err
				}
			}
		}
	}
	for _, st := range p.Info().Settings {
		for option, k := range st.Keeps {
			if ctx.Settings[st.Key] != any(option) {
				ctx.DeleteSecret(k.Key)
			}
		}
	}
	return nil
}

func (s *Server) status(iid int64, lang string) (any, error) {
	out, err := s.Host.Status(iid, lang)
	if err != nil {
		return nil, is404(err, nil)
	}
	return out, nil
}

func (s *Server) pluginRoutes() {
	h := s.handle

	h("GET /api/plugins/catalog", bodyNone, func(q *req) (any, error) {
		return M{"items": plugins.Catalog(q.lang())}, nil
	})

	h("GET /api/plugins", bodyNone, func(q *req) (any, error) {
		items := []any{}
		for _, r := range plugins.Instances(s.Store, "") {
			st, err := s.status(r.ID, q.lang())
			if err != nil {
				return nil, err
			}
			items = append(items, st)
		}
		return M{"items": items}, nil
	})

	h("POST /api/plugins", bodyRequired, func(q *req) (any, error) {
		id, _ := q.get("plugin").(string)
		p := plugins.Get(id)
		if p == nil {
			return nil, errs.New("unknown_plugin", 400, nil)
		}
		settings, _ := q.get("settings").(map[string]any)
		if err := s.checked(p, settings, q.xlang()); err != nil {
			return nil, err
		}
		label := q.text("label")
		if label == "" {
			label = p.Info().Name
		}
		label = strings.TrimFunc(label, isPySpace)
		keep := M{}
		for k, v := range settings {
			for _, st := range p.Info().Settings {
				if st.Key == k && st.Type != "secret" {
					keep[k] = v
				}
			}
		}
		iid, err := func() (iid int64, err error) {
			defer db.Recover(&err) // the same label twice: refused by the database
			return plugins.Create(s.Store, p.Info().ID, label, keep)
		}()
		if err != nil {
			return nil, passUser(err, func(e error) error { return failed(409, e.Error()) })
		}
		if err := s.keepSecrets(p, iid, q.body); err != nil {
			return nil, err
		}
		return s.status(iid, q.lang())
	})

	h("GET /api/plugins/{iid}", bodyNone, func(q *req) (any, error) {
		iid, err := q.pathInt("iid")
		if err != nil {
			return nil, err
		}
		return s.status(iid, q.lang())
	})

	h("PATCH /api/plugins/{iid}", bodyRequired, func(q *req) (any, error) {
		iid, err := q.pathInt("iid")
		if err != nil {
			return nil, err
		}
		row := plugins.GetInstance(s.Store, iid)
		if row == nil {
			return nil, notFound("")
		}
		p := plugins.Get(row.Plugin)
		allowed := map[string]bool{}
		if p != nil {
			for _, st := range p.Info().Settings {
				if st.Type != "secret" {
					allowed[st.Key] = true
				}
			}
		}
		settings := M{}
		if given, ok := q.get("settings").(map[string]any); ok {
			for k, v := range given {
				if allowed[k] {
					settings[k] = v
				}
			}
		}
		if p != nil {
			if err := s.checked(p, settings, q.xlang()); err != nil {
				return nil, err
			}
		}
		var label *string
		if v, ok := q.get("label").(string); ok {
			label = &v
		}
		var enabled *bool
		if v := q.get("enabled"); v != nil {
			b := truthy(v)
			enabled = &b
		}
		if len(settings) == 0 {
			settings = nil
		}
		if err := plugins.Update(s.Store, iid, label, settings, enabled, truthy(q.get("is_default"))); err != nil {
			return nil, err
		}
		if p != nil {
			if err := s.keepSecrets(p, iid, q.body); err != nil {
				return nil, err
			}
		}
		if v, ok := q.get("enabled").(bool); ok && !v {
			s.Host.StopLive(iid, false)
		}
		return s.status(iid, q.lang())
	})

	h("DELETE /api/plugins/{iid}", bodyNone, func(q *req) (any, error) {
		iid, err := q.pathInt("iid")
		if err != nil {
			return nil, err
		}
		row := plugins.GetInstance(s.Store, iid)
		if row == nil {
			return nil, notFound("")
		}
		s.Host.StopLive(iid, false)
		if err := plugins.Remove(s.Store, iid); err != nil {
			return nil, err
		}
		s.Auth.Log(&q.uid, "plugin removed", fmt.Sprintf("%d %s: %s", iid, row.Plugin, row.Label))
		return M{"ok": true}, nil
	})

	// The instance's whole log files, newest first: one per run, one per day of a live connection.
	h("GET /api/plugins/{iid}/logs", bodyNone, func(q *req) (any, error) {
		iid, err := q.pathInt("iid")
		if err != nil {
			return nil, err
		}
		if plugins.GetInstance(s.Store, iid) == nil {
			return nil, notFound("")
		}
		folder := filepath.Join(config.Logs, "plugin-"+itoa(iid))
		entries, _ := os.ReadDir(folder)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		items := []M{}
		for _, n := range names {
			if !strings.HasSuffix(n, ".log") {
				continue
			}
			st, err := os.Stat(filepath.Join(folder, n))
			if err != nil {
				continue
			}
			items = append(items, M{"name": n, "size": st.Size(), "modified": st.ModTime().Unix()})
		}
		return M{"items": items}, nil
	})

	h("GET /api/plugins/{iid}/logs/{name}", bodyNone, func(q *req) (any, error) {
		iid, err := q.pathInt("iid")
		if err != nil {
			return nil, err
		}
		name := q.r.PathValue("name")
		if strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, ".log") {
			return nil, notFound("")
		}
		p := filepath.Join(config.Logs, "plugin-"+itoa(iid), name)
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			return nil, notFound("")
		}
		return done, serveFile(q.w, q.r, p, "text/plain; charset=utf-8", "")
	})

	h("POST /api/plugins/{iid}/run", bodyOptional, func(q *req) (any, error) {
		iid, err := q.pathInt("iid")
		if err != nil {
			return nil, err
		}
		if plugins.GetInstance(s.Store, iid) == nil {
			return nil, notFound("")
		}
		action, _ := q.get("action").(string)
		given, _ := q.get("given").(map[string]any)
		if err := s.Host.Run(iid, action, given); err != nil {
			return nil, passUser(err, func(e error) error { return failed(409, e.Error()) })
		}
		return M{"ok": true}, nil
	})

	h("GET /api/plugins/{iid}/chats", bodyNone, func(q *req) (any, error) {
		iid, err := q.pathInt("iid")
		if err != nil {
			return nil, err
		}
		row := plugins.GetInstance(s.Store, iid)
		if row == nil {
			return nil, notFound("")
		}
		items := []M{}
		if lister, ok := plugins.Get(row.Plugin).(plugins.ChatLister); ok {
			got, err := lister.Chats(plugins.NewContext(s.Host, *row))
			if err != nil {
				return nil, passUser(err, func(e error) error { return failed(409, e.Error()) })
			}
			if got != nil {
				items = got
			}
		}
		return M{"items": items}, nil
	})

	// The user's choice: which chats are imported, and whose media are downloaded.
	h("PUT /api/plugins/{iid}/chats", bodyRequired, func(q *req) (any, error) {
		iid, err := q.pathInt("iid")
		if err != nil {
			return nil, err
		}
		if plugins.GetInstance(s.Store, iid) == nil {
			return nil, notFound("")
		}
		skip, err1 := ints(q.get("skip"))
		media, err2 := ints(q.get("media"))
		if err1 != nil || err2 != nil {
			return nil, failed(400, "skip, media")
		}
		if skip == nil {
			skip = []int64{}
		}
		if media == nil {
			media = []int64{}
		}
		if err := plugins.Update(s.Store, iid, nil, M{"skip_chats": skip, "media_chats": media}, nil, false); err != nil {
			return nil, err
		}
		return M{"ok": true}, nil
	})

	h("POST /api/plugins/{iid}/live", bodyRequired, func(q *req) (any, error) {
		iid, err := q.pathInt("iid")
		if err != nil {
			return nil, err
		}
		if plugins.GetInstance(s.Store, iid) == nil {
			return nil, notFound("")
		}
		if truthy(q.get("on")) {
			if err := s.Host.StartLive(iid); err != nil {
				return nil, passUser(err, func(e error) error { return failed(400, e.Error()) })
			}
		} else {
			s.Host.StopLive(iid, true)
		}
		return s.status(iid, q.lang())
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
