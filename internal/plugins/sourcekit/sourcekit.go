// Package sourcekit is what the source plugins share. Ports run_importers, LOOKS and looks of
// everysaid/plugins/sources.py, and everysaid/plugins/icons.py (icons.go).
//
// Most source plugins wrap what the project already has (the extraction from the phones and the
// importers): an import first brings the source up to date where it can (a backup over the cable,
// an export over adb, the service's API), then runs the importers for what it brings. Records
// found by two plugins are kept once, with both origins (the importers' own deduplication).
package sourcekit

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/importers"
	"everysaid/internal/phones"
	"everysaid/internal/plugins"
)

type look struct {
	name, color, short string
	messages           bool
}

// Looks is how the services given look, as a plugin declares them (its ServiceInfo).
func Looks(services ...string) map[string]plugins.ServiceInfo {
	out := map[string]plugins.ServiceInfo{}
	for _, s := range services {
		l := looks[s]
		info := plugins.ServiceInfo{Name: l.name, Color: l.color, Short: l.short, Icon: Icons[s]}
		if !l.messages {
			no := false
			info.Messages = &no
		}
		out[s] = info
	}
	return out
}

// Step is one importer: its label (said in the user's language), and what it runs on the archive.
// out takes its lines, already in the user's language.
type Step struct {
	Label string
	Run   func(a *archive.Archive, out func(string)) error
}

// importLock: one import at a time. Python's host has one (host.import_lock), shared by every
// plugin; a host that offers it (ImportLock) is used, else this one, shared by the plugins of this
// process that use this package.
var importLock sync.Mutex

// ImportLock is the lock every import holds while it writes the archive.
func ImportLock(c *plugins.Context) sync.Locker {
	if h, ok := c.Host().(interface{ ImportLock() sync.Locker }); ok {
		return h.ImportLock()
	}
	return &importLock
}

// RunImporters runs the steps, in the archive; the sources they bring are then tied to this
// instance. It returns the ids of the new messages and calls ([before, after]).
func RunImporters(c *plugins.Context, steps []Step) (msgs, calls [2]int64, err error) {
	l := ImportLock(c)
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
		lang := importers.Lang
		importers.Lang = c.Lang // the importers' lines in the user's language, not the system's
		defer func() { importers.Lang = lang }()
		msgs[0] = a.Int("SELECT ifnull(max(id), 0) FROM message")
		calls[0] = a.Int("SELECT ifnull(max(id), 0) FROM call")
		t0 := time.Now().Unix() - 1
		out := func(text string) {
			for _, line := range strings.Split(text, "\n") {
				if strings.TrimSpace(line) != "" {
					c.Logf("%s", line)
				}
			}
		}
		for _, s := range steps {
			c.Log("== {label}", map[string]any{"label": i18n.Tr(s.Label, c.Lang())})
			if err = s.Run(a, out); err != nil {
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
	// the card's "last run": an import of a live connection is one too, not only one the user started
	c.Store().MustWrite(func(tx *sql.Tx) {
		db.Exec(tx, "UPDATE plugin_instance SET last_run = ?, last_status = 'ok' WHERE id = ?", time.Now().Unix(), c.ID)
	})
	if msgs[1] > msgs[0] || calls[1] > calls[0] {
		c.Emit(plugins.M{"type": "new", "messages": []int64{msgs[0], msgs[1]}, "calls": []int64{calls[0], calls[1]}})
	}
	c.Log("new messages: {m}, new calls: {c}", map[string]any{"m": msgs[1] - msgs[0], "c": calls[1] - calls[0]})
	return
}

// Say is a run's lines into the instance's log (the phones' extraction says them through it).
func Say(c *plugins.Context) phones.Say {
	return func(text string, params map[string]any) { c.Log(text, params) }
}

// Terminal reads a tool's output into the instance's log as a terminal shows it: its lines, and a
// progress bar drawn in place. Close it at the end.
func Terminal(c *plugins.Context) *phones.Terminal {
	return phones.NewTerminal(func(line string, redrawn bool) {
		if redrawn {
			c.Redrawn(line)
		} else {
			c.Logf("%s", line)
		}
	}, c.Progress)
}

// UserError is a failure of the phones' extraction as the user is told it (in their language, by
// the server); other errors as they are.
func UserError(err error) error {
	var f *phones.Failure
	if errors.As(err, &f) && f.Text != "" {
		return &errs.UserError{Code: "plugin", Status: 409, Text: f.Text, Params: f.Params}
	}
	return err
}
