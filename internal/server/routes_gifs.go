package server

import (
	"errors"
	"strconv"

	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
)

// The GIFs to send: the plugins that find them (each instance one, as it says whether it can now),
// what one finds, and a GIF's file, which the app shows and sends back as a file (animated).
func (s *Server) gifRoutes() {
	h := s.handle

	h("GET /api/gifs", bodyNone, func(q *req) (any, error) {
		items := []M{}
		for _, r := range plugins.Instances(s.Store, "") {
			p := plugins.Get(r.Plugin)
			g, ok := p.(plugins.GIFSearcher)
			if !r.Enabled || !ok {
				continue
			}
			why := any(nil)
			if w := g.NotSearching(plugins.NewContext(s.Host, r)); w != "" {
				why = i18n.Tr(w, q.lang())
			}
			items = append(items, M{"instance": r.ID, "name": r.Label, "why": why})
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
