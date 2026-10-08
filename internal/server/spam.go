// Removing someone as spam (core/spam.go), with what the sources do about it: the service is told
// (reported, blocked, the chat deleted there) where the user asks and a source can, and each source
// drops its own copy of their chats.
package server

import (
	"context"
	"fmt"

	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
)

// spamReporters are the sources that can tell a service someone is spam, by service (the first
// enabled one of each).
func (h *Host) spamReporters() map[string]int64 {
	out := map[string]int64{}
	for _, row := range plugins.Instances(h.store, "source") {
		p := plugins.Get(row.Plugin)
		if p == nil || !row.Enabled || !p.Info().CanReportSpam {
			continue
		}
		if _, ok := p.(plugins.SpamReporter); !ok {
			continue
		}
		for _, svc := range p.Info().Services {
			if _, ok := out[svc]; !ok {
				out[svc] = row.ID
			}
		}
	}
	return out
}

// SpamCheck is core.SpamCheck, with the services among them that a source can report to
// (`reportable`).
func (h *Host) SpamCheck(pid int64) (M, error) {
	out, _, err := core.SpamCheck(h.store, pid)
	if err != nil {
		return nil, err
	}
	reporters := h.spamReporters()
	reportable := []string{}
	for _, svc := range out["services"].([]string) {
		if _, ok := reporters[svc]; ok {
			reportable = append(reportable, svc)
		}
	}
	out["reportable"] = reportable
	return out, nil
}

// RemoveSpam removes the person as spam. report: each of their chats is first reported where a
// source can; one that fails is said (`failed`: [{service, error}]) and the removal goes on. Then each
// source drops its own copy of those chats, and the archive what it has of them.
func (h *Host) RemoveSpam(ctx context.Context, pid int64, report bool, lang string) (out M, err error) {
	defer db.Recover(&err)
	check, convs, err := core.SpamCheck(h.store, pid)
	if err != nil {
		return nil, err
	}
	if why := check["refused"].(string); why != "" {
		_, err := core.RemoveSpam(h.store, pid) // says why, as a user's error
		return nil, err
	}
	reported, failed := []string{}, []M{}
	if report {
		reporters := h.spamReporters()
		for _, c := range convs {
			iid, ok := reporters[c.Service]
			if !ok {
				continue
			}
			pc, err := h.Ctx(iid)
			if err == nil {
				conv := plugins.Conversation{ID: c.ID, Key: c.Key, Service: c.Service}
				err = plugins.Get(pc.PluginID).(plugins.SpamReporter).ReportSpam(ctx, pc, conv)
			}
			if err != nil {
				failed = append(failed, M{"service": c.Service, "error": i18n.Tr(err.Error(), lang)})
				continue
			}
			pc.Log("reported as spam: {chat}", M{"chat": c.Key})
			reported = append(reported, c.Service)
		}
	}
	for _, row := range plugins.Instances(h.store, "source") {
		p := plugins.Get(row.Plugin)
		f, ok := p.(plugins.Forgetter)
		if !ok || !row.Enabled {
			continue
		}
		pc := plugins.NewContext(h, row)
		for _, c := range convs {
			if contains(p.Info().Services, c.Service) {
				if err := f.Forget(pc, plugins.Conversation{ID: c.ID, Key: c.Key, Service: c.Service}); err != nil {
					pc.Log("could not drop its copy of {chat}: {e}", M{"chat": c.Key, "e": err.Error()})
				}
			}
		}
	}
	purged, err := core.RemoveSpam(h.store, pid)
	if err != nil {
		return nil, err
	}
	h.Emit(M{"type": "changed"})
	return M{"messages": purged.Messages, "calls": purged.Calls, "chats": purged.Conversations,
		"reported": reported, "failed": failed}, nil
}

func (s *Server) spamRoutes() {
	h := s.handle

	h("GET /api/people/{pid}/spam", bodyNone, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		out, err := s.Host.SpamCheck(pid)
		return out, is404(err, nil)
	})

	// report: tell the services too, where a source can
	h("POST /api/people/{pid}/spam", bodyRequired, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		out, err := s.Host.RemoveSpam(q.r.Context(), pid, truthy(q.get("report")), q.lang())
		if err != nil {
			return nil, is404(err, nil)
		}
		s.Auth.Log(&q.uid, "removed as spam", fmt.Sprint(pid))
		return out, nil
	})

	h("POST /api/people/{pid}/not-spam", bodyNone, func(q *req) (any, error) {
		pid, err := q.pathInt("pid")
		if err != nil {
			return nil, err
		}
		if err := core.NotSpam(s.Store, pid); err != nil {
			return nil, is404(err, nil)
		}
		return M{"ok": true}, nil
	})

	h("GET /api/spam", bodyNone, func(q *req) (any, error) {
		return M{"removed": core.SpamRemoved(s.Store), "suggestions": core.SpamSuggestions(s.Store)}, nil
	})

	h("POST /api/spam/{aid}/restore", bodyNone, func(q *req) (any, error) {
		aid, err := q.pathInt("aid")
		if err != nil {
			return nil, err
		}
		if err := core.RestoreSpam(s.Store, aid); err != nil {
			return nil, is404(err, nil)
		}
		s.Auth.Log(&q.uid, "restored from spam", fmt.Sprint(aid))
		return M{"ok": true}, nil
	})
}
