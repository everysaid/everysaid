// Ports the Telegram plugin of everysaid/plugins/sources.py (its class, and privately what it
// needs of the module: run_importers, run_script's reading of a script's lines, LOOKS and ICONS
// for Telegram; the shared helpers of the source plugins are another part of the port).
package telegram

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/i18n"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
)

type M = plugins.M

// Source is the archive's source name of telegram.db.
const Source = "telegram"

// The importers this plugin runs (variables, so that a test can watch them).
var (
	importTelegram = func(a *archive.Archive, out func(string), only map[[2]int64]bool, skip map[int64]bool) error {
		return importers.Telegram(a, out, importers.TelegramOptions{Only: only, Skip: skip})
	}
	importReads = func(a *archive.Archive, chats map[int64]bool) error {
		return importers.TelegramReads(a, nil, "", chats)
	}
	importMedia = func(a *archive.Archive, out func(string)) error {
		return importers.Media(a, out, importers.TelegramMedia)
	}
)

var looks = map[string]plugins.ServiceInfo{
	"telegram": {Name: "Telegram", Color: "#2aabee", Short: "Tg",
		Icon: "M11.944 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12 12 12 0 0 0 12-12A12 12 0 0 0 12 0a12 12 0 0 0-.056 0zm4.962 7.224c.1-.002.321.023.465.14a.506.506 0 0 1 .171.325c.016.093.036.306.02.472-.18 1.898-.962 6.502-1.36 8.627-.168.9-.499 1.201-.82 1.23-.696.065-1.225-.46-1.9-.902-1.056-.693-1.653-1.124-2.678-1.8-1.185-.78-.417-1.21.258-1.91.177-.184 3.247-2.977 3.307-3.23.007-.032.014-.15-.056-.212s-.174-.041-.249-.024c-.106.024-1.793 1.14-5.061 3.345-.48.33-.913.49-1.302.48-.428-.008-1.252-.241-1.865-.44-.752-.245-1.349-.374-1.297-.789.027-.216.325-.437.893-.663 3.498-1.524 5.83-2.529 6.998-3.014 3.332-1.386 4.025-1.627 4.476-1.635z"},
}

// Plugin is Telegram with the user's own account.
type Plugin struct{}

func init() { plugins.Register(Plugin{}) }

var info = &plugins.Info{
	ID: "telegram", Name: "Telegram", Kind: "source", Services: []string{"telegram"},
	ServiceInfo:  looks,
	NameWeights:  []plugins.Weight{{Key: "telegram/profile", Weight: 40}}, // chosen by each person
	StateWeights: map[string]int{"muted": 60, "pinned": 0},
	Description: "Every chat but channels and bots, through Telegram's API with the user's own account " +
		"(Telethon): the whole history, then live.",
	Modes:       []string{"import", "live"},
	LiveDefault: true,
	Needs:       []string{"api_id and api_hash from my.telegram.org", "a login (a code that arrives in Telegram)"},
	Settings: []plugins.Setting{
		{Key: "media", Label: "Download pictures and videos", Type: "bool", Default: false},
		{Key: "read_receipts", Label: "Send read receipts", Type: "bool", Default: false,
			Help: "When a chat is opened here, the others see it read, and it is read on the phone too"},
	},
	CanSend: true, CanReply: true, CanMention: true, CanMarkRead: true, CanSendFiles: true,
}

func (Plugin) Info() *plugins.Info { return info }

func (Plugin) Check(c *plugins.Context) (bool, string) {
	if _, _, ok := credentials(); !ok {
		return false, "missing: api_id and api_hash (scripts/telegram-sync.py --save-credentials)"
	}
	if !hasSession() {
		return false, "missing: a login (scripts/telegram-sync.py --login)"
	}
	return true, "ready"
}

// ids is a setting holding chat ids (numbers or their text).
func ids(v any) []int64 {
	list, _ := v.([]any)
	var out []int64
	for _, x := range list {
		if n, err := strconv.ParseInt(strings.TrimSuffix(fmt.Sprint(x), ".0"), 10, 64); err == nil {
			out = append(out, n)
		} else if f, ok := x.(float64); ok {
			out = append(out, int64(f))
		}
	}
	return out
}

func idSet(v any) map[int64]bool {
	out := map[int64]bool{}
	for _, n := range ids(v) {
		out[n] = true
	}
	return out
}

func (Plugin) RunImport(c *plugins.Context) error {
	ctx := context.Background()
	if err := runSync(ctx, c, SyncOptions{}); err != nil {
		return err
	}
	skip := idSet(c.Settings["skip_chats"])
	steps := []step{{"Telegram", func(a *archive.Archive, out func(string)) error {
		return importTelegram(a, out, nil, skip)
	}}}
	mediaChats := ids(c.Settings["media_chats"])
	if c.Bool("media") || len(mediaChats) > 0 {
		if err := runSync(ctx, c, SyncOptions{Media: true, Chats: mediaChats}); err != nil {
			return err
		}
		steps = append(steps, step{"files", importMedia})
	}
	_, _, err := runImporters(c, steps)
	return err
}

// runSync is the sync (telegram-sync.py, which Python ran as a script), its lines into the log.
func runSync(ctx context.Context, c *plugins.Context, o SyncOptions) error {
	w := &logWriter{c: c}
	o.Lang = c.Lang()
	err := Sync(ctx, o, w)
	w.Close()
	if err != nil && w.last != "" {
		return fmt.Errorf("%s: %w", w.last, err)
	}
	return err
}

// Chats are the chats the sync has seen, with the user's choice for each.
func (Plugin) Chats(c *plugins.Context) ([]M, error) {
	out := []M{}
	if _, err := os.Stat(DBPath()); err != nil {
		return out, nil
	}
	skip := idSet(c.Settings["skip_chats"])
	media := idSet(c.Settings["media_chats"])
	d, err := db.ReadOnly(DBPath())
	if err != nil {
		return nil, err
	}
	defer d.Close()
	err = func() (err error) {
		defer db.Recover(&err)
		db.Each(d, "SELECT c.id, c.kind, c.title, c.archived, count(m.id), min(m.date), max(m.date) FROM chat c "+
			"LEFT JOIN message m ON m.chat_id = c.id GROUP BY c.id ORDER BY max(m.date) DESC", nil, func(scan func(...any)) {
			var id, archived, n int64
			var kind string
			var title *string
			var first, last *int64
			scan(&id, &kind, &title, &archived, &n, &first, &last)
			var f, l int64
			if first != nil {
				f = *first
			}
			if last != nil {
				l = *last
			}
			out = append(out, M{"id": id, "kind": kind, "title": title, "archived": archived != 0, "messages": n,
				"first": f * 1000, "last": l * 1000, "import": !skip[id], "media": media[id]})
		})
		return nil
	}()
	return out, err
}

func (Plugin) Live(ctx context.Context, c *plugins.Context) error { return live(ctx, c) }

func (Plugin) Send(ctx context.Context, c *plugins.Context, conv plugins.Conversation, text string, reply *plugins.Reply,
	mentions []plugins.Mention, file *plugins.File) (any, error) {
	return send(ctx, c, conv, text, reply, mentions, file)
}

// MarkRead sends read receipts for the chat, where the user turned them on; nothing otherwise.
func (Plugin) MarkRead(ctx context.Context, c *plugins.Context, conv plugins.Conversation, until int64) (int, error) {
	if !c.Bool("read_receipts") {
		return 0, nil
	}
	return markRead(ctx, c, conv, until)
}

// --- run_importers, privately --------------------------------------------------------------------

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

// withArchive runs fn on the archive under the import lock, committing at the end.
func withArchive(c *plugins.Context, fn func(a *archive.Archive) error) (err error) {
	l := lockOf(c)
	l.Lock()
	defer l.Unlock()
	a, err := archive.Open(c.Store().Path)
	if err != nil {
		return err
	}
	defer a.Close()
	defer archive.Recover(&err)
	if err := fn(a); err != nil {
		return err
	}
	a.Commit()
	return nil
}

// runImporters runs the steps; the sources they bring are then tied to this instance. It returns
// the ids of the new messages and calls ([before, after]).
func runImporters(c *plugins.Context, steps []step) (msgs, calls [2]int64, err error) {
	err = withArchive(c, func(a *archive.Archive) error {
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
			if err := s.run(a, out); err != nil {
				return err
			}
		}
		a.Exec("UPDATE source SET instance_id = ? WHERE instance_id IS NULL AND imported_at >= ?", c.ID, t0)
		a.Commit()
		msgs[1] = a.Int("SELECT ifnull(max(id), 0) FROM message")
		calls[1] = a.Int("SELECT ifnull(max(id), 0) FROM call")
		return nil
	})
	if err != nil {
		return
	}
	if msgs[1] > msgs[0] || calls[1] > calls[0] {
		c.Emit(M{"type": "new", "messages": []int64{msgs[0], msgs[1]}, "calls": []int64{calls[0], calls[1]}})
	}
	c.Log("new messages: {m}, new calls: {c}", map[string]any{"m": msgs[1] - msgs[0], "c": calls[1] - calls[0]})
	return
}

// source is telegram.db's source in the archive, tied to this instance where it is no one's yet.
func source(a *archive.Archive, iid int64) int64 {
	src := a.Source(Source, DBPath(), "telegram", MediaPath())
	a.Exec("UPDATE source SET instance_id = ? WHERE id = ? AND instance_id IS NULL", iid, src)
	return src
}

// logWriter is run_script's reading of a script's output, for the sync run in process: a line
// ends with \n; a \r draws the line again (a progress bar), shown in place as it changes (at most
// every 0.2 s) and in the log as each drawing ends. The lines are already in the user's language.
type logWriter struct {
	c       *plugins.Context
	pending string
	shown   string
	sent    string
	drawn   time.Time
	last    string
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.pending += string(p)
	for {
		i := strings.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		text := strings.TrimRight(w.pending[:i], "\r")
		w.pending = w.pending[i+1:]
		parts := strings.Split(text, "\r")
		line := parts[len(parts)-1]
		if line == "" {
			line = w.shown
		}
		line = strings.TrimRight(line, " \t")
		redrawn := w.shown != "" || strings.Contains(text, "\r")
		w.shown, w.sent = "", ""
		if strings.TrimSpace(line) != "" {
			if redrawn {
				w.c.Redrawn(line)
			} else {
				w.c.Logf("%s", line)
			}
			w.last = strings.TrimSpace(line)
		}
	}
	if strings.Contains(w.pending, "\r") {
		parts := strings.Split(w.pending, "\r")
		for i := len(parts) - 1; i >= 0; i-- {
			if strings.TrimSpace(parts[i]) != "" {
				w.shown = parts[i]
				break
			}
		}
		w.pending = parts[len(parts)-1]
	}
	if w.shown != "" && w.shown != w.sent && time.Since(w.drawn) >= 200*time.Millisecond {
		w.c.Progress(w.shown)
		w.sent, w.drawn = w.shown, time.Now()
	}
	return len(p), nil
}

// Close logs what is left without an end of line.
func (w *logWriter) Close() {
	if strings.TrimSpace(w.pending) != "" {
		parts := strings.Split(w.pending, "\r")
		line := strings.TrimRight(parts[len(parts)-1], " \t")
		w.c.Logf("%s", line)
		w.last = strings.TrimSpace(line)
	}
	w.pending = ""
}
