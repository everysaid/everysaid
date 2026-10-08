// Ports the Telegram plugin of everysaid/plugins/sources.py; what the source plugins share
// (run_importers, the import lock, LOOKS and ICONS, run_script's reading of a script's lines) is
// internal/plugins/sourcekit.
package telegram

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
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
	importMembers = func(a *archive.Archive, chats map[int64]bool) error {
		return importers.TelegramMembers(a, nil, "", chats)
	}
	importMedia = func(a *archive.Archive, out func(string)) error {
		return importers.Media(a, out, importers.TelegramMedia)
	}
)

// Plugin is Telegram with the user's own account.
type Plugin struct{}

func init() { plugins.Register(Plugin{}) }

var info = &plugins.Info{
	ID: "telegram", Name: "Telegram", Kind: "source", Services: []string{"telegram"},
	ServiceInfo:  sourcekit.Looks("telegram"),
	NameWeights:  []plugins.Weight{{Key: "telegram/profile", Weight: 40}}, // chosen by each person
	StateWeights: map[string]int{"muted": 60, "pinned": 0},
	Description: "Every chat but channels and bots, through Telegram's API with the user's own account: " +
		"the whole history, then live.",
	Modes:       []string{"import", "live"},
	LiveDefault: true,
	Needs:       []string{"api_id and api_hash from my.telegram.org", "a login (a code that arrives in Telegram)"},
	Settings: []plugins.Setting{
		{Key: "media", Label: "Download pictures and videos", Type: "bool", Default: false},
		{Key: "read_receipts", Label: "Send read receipts", Type: "bool", Default: false,
			Help: "When a chat is opened here, the others see it read, and it is read on the phone too"},
	},
	CanSend: true, CanReply: true, CanMention: true, CanMarkRead: true, CanSendFiles: true,
	CanReact: true, Reactions: reactions, CanEdit: true, CanDelete: true, CanReportSpam: true,
	// Telegram's edit_time_limit (its apps offer no edit after it, but in Saved Messages); deleting
	// for everyone has had no limit in private chats and groups since 2019
	EditWindow: 48 * time.Hour,
}

func (Plugin) Info() *plugins.Info { return info }

func (Plugin) Check(c *plugins.Context) (bool, string) {
	if _, _, ok := credentials(); !ok {
		return false, "missing: api_id and api_hash (everysaid telegram-sync --save-credentials)"
	}
	if !hasSession() {
		return false, "missing: a login (everysaid telegram-sync --login)"
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
	steps := []sourcekit.Step{{Label: "Telegram", Run: func(a *archive.Archive, out func(string)) error {
		return importTelegram(a, out, nil, skip)
	}}}
	mediaChats := ids(c.Settings["media_chats"])
	if c.Bool("media") || len(mediaChats) > 0 {
		if err := runSync(ctx, c, SyncOptions{Media: true, Chats: mediaChats}); err != nil {
			return err
		}
		steps = append(steps, sourcekit.Step{Label: "files", Run: importMedia})
	}
	_, _, err := sourcekit.RunImporters(c, steps)
	return err
}

// runSync is the sync (telegram-sync.py, which Python ran as a script), its lines into the log.
func runSync(ctx context.Context, c *plugins.Context, o SyncOptions) error {
	last := ""
	t := sourcekit.Terminal(c)
	w := lastLine{t, &last}
	o.Lang = c.Lang()
	err := Sync(ctx, o, w)
	t.Close()
	if err != nil && last != "" && last != err.Error() { // its own last words say what it was doing
		return fmt.Errorf("%s: %w", last, err)
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

// --- the archive -----------------------------------------------------------------------------------

// withArchive runs fn on the archive under the import lock, committing at the end.
func withArchive(c *plugins.Context, fn func(a *archive.Archive) error) (err error) {
	l := sourcekit.ImportLock(c)
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

// source is telegram.db's source in the archive, tied to this instance where it is no one's yet.
func source(a *archive.Archive, iid int64) int64 {
	src := a.Source(Source, DBPath(), "telegram", MediaPath())
	a.Exec("UPDATE source SET instance_id = ? WHERE id = ? AND instance_id IS NULL", iid, src)
	return src
}

// lastLine passes what the sync says on, keeping its last line (to say why, when it fails).
type lastLine struct {
	w    io.Writer
	last *string
}

func (l lastLine) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.ReplaceAll(string(p), "\r", "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			*l.last = strings.TrimSpace(line)
		}
	}
	return l.w.Write(p)
}
