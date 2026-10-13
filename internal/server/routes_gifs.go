package server

import (
	"errors"
	"strconv"

	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
)

// The GIFs to send: the GIFs plugins, in the order added (each instance a tab: the archive's own,
// History, or one that finds them, as it says whether it can now), what one finds, and a GIF's file,
// which the app shows and sends back as a file (animated).
func (s *Server) gifRoutes() {
	h := s.handle

	h("GET /api/gifs", bodyNone, func(q *req) (any, error) {
		items := []M{}
		for _, r := range plugins.Instances(s.Store, "gifs") {
			p := plugins.Get(r.Plugin)
			if !r.Enabled || p == nil {
				continue
			}
			name := r.Label // the plugin's own name, where the user gave none, in the user's language
			if name == p.Info().Name {
				name = i18n.Tr(name, q.lang())
			}
			it := M{"instance": r.ID, "name": name, "why": nil}
			switch g := p.(type) {
			case plugins.GIFHistory:
				it["history"] = true
			case plugins.GIFSearcher:
				if w := g.NotSearching(plugins.NewContext(s.Host, r)); w != "" {
					it["why"] = i18n.Tr(w, q.lang())
				}
			default:
				continue
			}
			items = append(items, it)
		}
		return M{"items": items}, nil
	})

	h("GET /api/gifs/{instance}/search", bodyNone, func(q *req) (any, error) {
		g, c, err := s.gifSearcher(q)
		if err != nil {
			return nil, err
		}
		found, next, err := g.SearchGIFs(q.r.Context(), c, q.str("q"), q.str("offset"))
		if err != nil {
			return nil, passUser(err, pluginFailed)
		}
		if found == nil {
			found = []plugins.GIF{}
		}
		return M{"items": found, "next": next}, nil
	})

	// a GIF found was sent: the searcher told, where it asks to be (it does not change what went)
	h("POST /api/gifs/{instance}/{id}/sent", bodyNone, func(q *req) (any, error) {
		g, c, err := s.gifSearcher(q)
		if err != nil {
			return nil, err
		}
		if sh, ok := g.(plugins.GIFSharer); ok {
			if err := sh.SentGIF(q.r.Context(), c, q.r.PathValue("id")); err != nil {
				s.log.Warn("a GIF's sending not told", "instance", c.ID, "error", err)
			}
		}
		return M{"ok": true}, nil
	})

	h("GET /api/gifs/{instance}/{id}", bodyNone, func(q *req) (any, error) {
		g, c, err := s.gifSearcher(q)
		if err != nil {
			return nil, err
		}
		f, err := g.GIF(q.r.Context(), c, q.r.PathValue("id"))
		if err != nil {
			return nil, passUser(err, pluginFailed)
		}
		q.w.Header().Set("Content-Type", f.MimeType)
		q.w.Header().Set("Content-Length", strconv.Itoa(len(f.Data)))
		q.w.Header().Set("Cache-Control", "private, max-age=3600")
		inert(q.w.Header(), f.MimeType)
		q.w.Write(f.Data)
		return done, nil
	})
}

// gifSearcher is the instance of the path, where it finds GIFs and can now.
func (s *Server) gifSearcher(q *req) (plugins.GIFSearcher, *plugins.Context, error) {
	iid, err := q.pathInt("instance")
	if err != nil {
		return nil, nil, err
	}
	r := plugins.GetInstance(s.Store, iid)
	if r == nil || !r.Enabled {
		return nil, nil, notFound("")
	}
	g, ok := plugins.Get(r.Plugin).(plugins.GIFSearcher)
	if !ok {
		return nil, nil, notFound("")
	}
	c := plugins.NewContext(s.Host, *r)
	if why := g.NotSearching(c); why != "" {
		return nil, nil, errs.Plugin(why, 409)
	}
	return g, c, nil
}

// pluginFailed is a plugin's failure (a service not answering) as the user is told it.
func pluginFailed(err error) error {
	var ue *errs.UserError
	if errors.As(err, &ue) {
		return err
	}
	return errs.Plugin(err.Error(), 502)
}
