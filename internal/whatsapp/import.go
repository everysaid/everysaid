package whatsapp

// Ports run_importers of everysaid/plugins/sources.py (privately: the shared helpers of
// the source plugins are another part of the port), and WhatsappBridge.run_import.

import (
	"strings"
	"sync"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
)

// step is one importer: its label, and the function that runs it on the archive.
type step struct {
	label string
	run   func(a *archive.Archive, out func(string)) error
}

// importLock: one import at a time. Python's host has one (host.import_lock), shared by every
// plugin; a host that offers it (ImportLock) is used, else this package's own.
var importLock sync.Mutex

func lockOf(c *plugins.Context) sync.Locker {
	if h, ok := c.Host().(interface{ ImportLock() sync.Locker }); ok {
		return h.ImportLock()
	}
	return &importLock
}

// runImporters runs the steps; the sources they bring are then tied to this instance. It returns
// the ids of the new messages and calls ([before, after]).
func runImporters(c *plugins.Context, steps []step) (msgs, calls [2]int64, err error) {
	l := lockOf(c)
	l.Lock()
	func() {
		defer l.Unlock()
		var a *archive.Archive
		a, err = archive.Open(c.Store().Path)
		if err != nil {
			return
		}
		defer a.Close()
		defer archive.Recover(&err)
		msgs[0] = a.Int("SELECT ifnull(max(id), 0) FROM message")
		calls[0] = a.Int("SELECT ifnull(max(id), 0) FROM call")
		t0 := time.Now().Unix() - 1
		out := func(line string) { // an importer's lines, already in the user's language
			if strings.TrimSpace(line) != "" {
				c.Logf("%s", line)
			}
		}
		for _, s := range steps {
			c.Log("== {label}", map[string]any{"label": i18n.Tr(s.label, c.Lang())})
			if err = s.run(a, out); err != nil {
				return
			}
		}
		a.Exec("UPDATE source SET instance_id = ? WHERE instance_id IS NULL AND imported_at >= ?", c.ID, t0)
		a.Commit()
		msgs[1] = a.Int("SELECT ifnull(max(id), 0) FROM message")
		calls[1] = a.Int("SELECT ifnull(max(id), 0) FROM call")
	}()
	if err != nil {
		return
	}
	if msgs[1] > msgs[0] || calls[1] > calls[0] {
		c.Emit(M{"type": "new", "messages": []int64{msgs[0], msgs[1]}, "calls": []int64{calls[0], calls[1]}})
	}
	c.Log("new messages: {m}, new calls: {c}", map[string]any{"m": msgs[1] - msgs[0], "c": calls[1] - calls[0]})
	return
}

// runImport imports the store folder's databases only (an iPhone's are another instance's): the
// messages, the calls, the files the bridge downloaded.
func runImport(c *plugins.Context) error {
	bridge, store := paths(c)
	changed := false
	_, _, err := runImporters(c, []step{
		{"WhatsApp (bridge)", func(a *archive.Archive, out func(string)) error {
			ch, err := importWhatsApp(a, out, bridge, store)
			changed = changed || ch
			return err
		}},
		{"WhatsApp calls (bridge)", func(a *archive.Archive, out func(string)) error { return importCalls(a, out, bridge, store) }},
		{"files", func(a *archive.Archive, out func(string)) error { return importMedia(a, out, bridge) }},
	})
	if err != nil {
		return err
	}
	if changed { // edits, deletions, reactions on messages already shown
		c.Emit(M{"type": "changed"})
	}
	return nil
}
