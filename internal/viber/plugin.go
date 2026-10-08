// Package viber is the `viber-desktop` source: Viber through the running Viber Desktop, driven from
// inside by Everysaid's bridge (bridges/viber). It brings Viber Desktop's history (whatever the phone
// it is linked to gave it) and, live, what arrives, with edits, deletions and reactions followed;
// and it sends: text, files, replies, mentions, reactions, edits, deletions for everyone, read
// receipts. Linux only (the bridge is an LD_PRELOAD library); there is no official way.
//
// Reading goes through a plain copy of Viber's database that the bridge writes through Viber's own
// unlocked connection (snapshot), read by the same importer as the iPhone's Viber; a message both
// have is kept once, by its token.
package viber

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

type M = plugins.M

type Plugin struct{}

func init() { plugins.Register(Plugin{}) }

// quick are Viber's own reactions, its codes 1 to 5 in this order; any other emoji goes as itself.
var quick = []string{"❤️", "😂", "😮", "😢", "😡"}

// sourceName is the archive's source of what this plugin brings (the iPhone's Viber is another).
const sourceName = "viber-desktop/viber"

func (Plugin) Info() *plugins.Info {
	return &plugins.Info{
		ID: "viber-desktop", Name: "Viber Desktop", Kind: "source",
		Services:    []string{"viber"},
		ServiceInfo: sourcekit.Looks("viber"),
		Description: "Viber through the Viber Desktop running on this computer, with Everysaid's bridge " +
			"(bridges/viber): its history, what arrives, and sending. Linux only: there is no official way.",
		Platforms:   []string{"linux"},
		Modes:       []string{"import", "live"},
		LiveDefault: true,
		Needs:       []string{"Viber Desktop", "Everysaid's Viber bridge (bridges/viber)"},
		Settings: []plugins.Setting{
			{Key: "socket", Label: "The bridge's socket", Type: "path", Default: DefaultSocket()},
			{Key: "send", Label: "Sending messages", Type: "bool", Default: false,
				Help: "Also needs the bridge started with VIBER_ALLOW_SEND=1"},
			{Key: "read_receipts", Label: "Send read receipts", Type: "bool", Default: false,
				Help: "When a chat is opened here, the others see it read, and it is read on the phone too"},
			{Key: "interval", Label: "Check every (seconds)", Type: "number", Default: 60},
		},
		CanSend: true, CanReply: true, CanMention: true, CanMarkRead: true, CanSendFiles: true,
		CanReact: true, Reactions: quick, FreeReactions: true, CanEdit: true, CanDelete: true,
	}
}

func socket(c *plugins.Context) string {
	if s := c.Str("socket"); s != "" {
		return config.ExpandUser(s)
	}
	return DefaultSocket()
}

func running(c *plugins.Context) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := call(ctx, socket(c), "ping", 3*time.Second)
	return err == nil && strings.TrimSpace(out) == "pong"
}

const (
	notRunning = "Viber Desktop is not running with Everysaid's bridge"
	waiting    = "Viber Desktop is not running with Everysaid's bridge: waiting for it"
)

func (Plugin) Check(c *plugins.Context) (bool, string) {
	if !running(c) {
		return false, notRunning
	}
	ck := checkOf(c)
	if m := ck.lacks(nil, "read", "live"); m != "" {
		return false, cannot + ": " + m
	}
	if m := ck.lacks(nil); m != "" {
		return true, readyBut + ": " + m
	}
	return true, "ready"
}

func (Plugin) NotSending(c *plugins.Context) string {
	if !c.Bool("send") {
		return "Sending is off in this source's settings"
	}
	if !running(c) {
		return notRunning
	}
	if m := checkOf(c).lacks(nil, "send"); m != "" {
		return cannot + ": " + m
	}
	return ""
}

func (Plugin) InfoFacts(c *plugins.Context) []plugins.Fact {
	if !running(c) {
		return []plugins.Fact{{Label: "Viber Desktop", Value: "not running"}}
	}
	facts := []plugins.Fact{{Label: "Viber Desktop", Value: "running"}}
	ck := checkOf(c)
	switch {
	case !ck.Known:
		facts = append(facts, plugins.Fact{Label: "Viber Desktop version", Value: olderBridge})
	default:
		facts = append(facts, plugins.Fact{Label: "Viber Desktop version", Value: ck.Version})
		if m := ck.lacks(func(s string) string { return i18n.Tr(s, c.Lang()) }); m != "" {
			facts = append(facts, plugins.Fact{Label: "Missing in this version", Value: m})
		}
	}
	return facts
}

// --- what this Viber Desktop can do (the bridge's check) ------------------------------------------

const (
	cannot      = "This version of Viber Desktop cannot"
	readyBut    = "Ready, but this version of Viber Desktop cannot"
	olderBridge = "unknown (a bridge older than its check: make -C bridges/viber/inject, then restart Viber)"
)

// capabilities are the bridge's, by its names, as the user is told them, in this order.
var capabilities = []struct{ name, label string }{
	{"read", "reading"}, {"live", "receiving live"}, {"send", "sending"}, {"file", "files"},
	{"compose", "replies and edits"}, {"react", "reactions"}, {"delete", "deleting for everyone"},
	{"read-receipts", "read receipts"},
}

// lacks is what of the named capabilities (all, with none named) this Viber is missing, as the user
// is told it ("" for nothing), each word through say where given.
func (ck checked) lacks(say func(string) string, names ...string) string {
	var out []string
	for _, cp := range capabilities {
		if _, gone := ck.Missing[cp.name]; gone && (len(names) == 0 || contains(names, cp.name)) {
			if say != nil {
				out = append(out, say(cp.label))
			} else {
				out = append(out, cp.label)
			}
		}
	}
	return strings.Join(out, ", ")
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// checkFor: how long the bridge's check is kept (a page of sources, each chat's composer ask it).
var checkFor = 10 * time.Second

var checks sync.Map // socket → keptCheck

type keptCheck struct {
	at time.Time
	ck checked
}

// checkOf is the bridge's check, kept a moment; nothing missing where the bridge cannot say.
func checkOf(c *plugins.Context) checked {
	if k, ok := checks.Load(socket(c)); ok && time.Since(k.(keptCheck).at) < checkFor {
		return k.(keptCheck).ck
	}
	return checkNow(context.Background(), c)
}

func checkNow(ctx context.Context, c *plugins.Context) checked {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ck, err := check(ctx, socket(c))
	if err != nil {
		return checked{}
	}
	checks.Store(socket(c), keptCheck{time.Now(), ck})
	return ck
}

// able refuses an action this Viber can no longer do, saying what is missing.
func able(c *plugins.Context, names ...string) error {
	if m := checkOf(c).lacks(nil, names...); m != "" {
		return errs.Plugin(cannot+": "+m, 0)
	}
	return nil
}

// noticeVersion keeps the version of Viber Desktop and what it is missing; when either changes
// (an update), it is said in the log and to the user's devices.
func noticeVersion(ctx context.Context, c *plugins.Context) {
	ck := checkNow(ctx, c)
	if !ck.Known || ck.Version == "" {
		return
	}
	var gone []string
	for _, cp := range capabilities {
		if _, ok := ck.Missing[cp.name]; ok {
			gone = append(gone, cp.name)
		}
	}
	missing := strings.Join(gone, ",")
	was, _ := c.State["viber_version"].(string)
	wasMissing, _ := c.State["viber_missing"].(string)
	if was == ck.Version && wasMissing == missing {
		return
	}
	c.SaveState(M{"viber_version": ck.Version, "viber_missing": missing})
	if was == "" && missing == "" {
		return // the first seen, all there
	}
	params := map[string]any{"from": was, "to": ck.Version, "version": ck.Version, "what": ck.lacks(nil)}
	var text string
	switch {
	case was != "" && was != ck.Version && missing == "":
		text = "Viber Desktop {from} → {to}: everything the bridge uses is there"
	case was != "" && was != ck.Version:
		text = "Viber Desktop {from} → {to}, missing: {what}"
	case missing == "":
		text = "Viber Desktop {version}: everything the bridge uses is there"
	default:
		text = "Viber Desktop {version}, missing: {what}"
	}
	c.Log(text, params)
	params["what"] = ck.lacks(func(s string) string { return i18n.Tr(s, c.Lang()) })
	c.Host().Alert("Viber Desktop", i18n.T(text, c.Lang(), params))
}

// --- reading -------------------------------------------------------------------------------------

func dir(c *plugins.Context) string { return filepath.Join(config.Cache, "viber", fmt.Sprint(c.ID)) }

func snapshotPath(c *plugins.Context) string { return filepath.Join(dir(c), "desktop.db") }

// locks: one snapshot and import at a time per instance (live, and after each action).
var locks sync.Map

func lockOf(c *plugins.Context) *sync.Mutex {
	l, _ := locks.LoadOrStore(c.ID, &sync.Mutex{})
	return l.(*sync.Mutex)
}

// snapshot has the bridge write a plain copy of Viber's database (private: it is the history).
func snapshot(ctx context.Context, c *plugins.Context) error {
	if err := os.MkdirAll(dir(c), 0o700); err != nil {
		return err
	}
	if err := act(ctx, socket(c), "snapshot "+snapshotPath(c)); err != nil {
		if errors.Is(err, ErrNotRunning) {
			return errs.Plugin(notRunning, 503)
		}
		return err
	}
	return nil
}

// runImport brings Viber Desktop's database as it is now into the archive.
func runImport(ctx context.Context, c *plugins.Context) error {
	return importUnless(ctx, c, nil)
}

// importUnless imports, unless done says (once the import before it has ended) that what is wanted
// is already in.
func importUnless(ctx context.Context, c *plugins.Context, done func() bool) error {
	l := lockOf(c)
	l.Lock()
	defer l.Unlock()
	if done != nil && done() {
		return nil
	}
	if err := snapshot(ctx, c); err != nil {
		return err
	}
	_, _, err := sourcekit.RunImporters(c, []sourcekit.Step{
		{Label: "Viber Desktop", Run: func(a *archive.Archive, out func(string)) error {
			return importers.Viber(a, out, importers.ViberOptions{DesktopDB: snapshotPath(c), NoIphone: true,
				DesktopSource: sourceName})
		}},
		{Label: "files", Run: func(a *archive.Archive, out func(string)) error {
			return importers.Media(a, out, importers.MediaViberDesktop(snapshotPath(c), sourceName))
		}},
	})
	return err
}

func (Plugin) RunImport(c *plugins.Context) error { return runImport(context.Background(), c) }

// Live imports at once, then again whenever Viber adds events (a message, a reaction, an edit),
// gathered for a moment, and every `interval` seconds for what changes without one (a deletion).
// Viber Desktop stopped (restarted, updated) is waited for here, and followed again when it is
// back: not a failure, whose pauses would grow with each restart.
func (Plugin) Live(ctx context.Context, c *plugins.Context) error {
	every := max(minEvery, time.Duration(c.Num("interval"))*time.Second)
	if c.Num("interval") == 0 {
		every = time.Minute
	}
	w := &watchdog{every: every}
	for ctx.Err() == nil {
		if !running(c) {
			c.Log(waiting, nil)
			for !running(c) {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(liveWait):
				}
			}
		}
		noticeVersion(ctx, c)
		if err := follow(ctx, c, every, w); err != nil && ctx.Err() == nil {
			c.Log("error: {e}", map[string]any{"e": err})
			select { // what failed is not tried again at once
			case <-ctx.Done():
			case <-time.After(liveWait):
			}
		}
	}
	return nil
}

// liveWait is how often a stopped Viber Desktop is looked for; minEvery the shortest interval.
var (
	liveWait = 5 * time.Second
	minEvery = 10 * time.Second
)

// watchdog tells when live receiving stopped working: the checks every interval bring new events
// that the bridge did not tell of, twice running (said once, until it works again).
type watchdog struct {
	every time.Duration
	heard atomic.Bool // the bridge told of events since the last check
	last  int64       // the newest event at the last check
	quiet int         // checks running that brought events not told of
	said  bool
}

func (w *watchdog) checked(c *plugins.Context, newest int64) {
	heard := w.heard.Swap(false)
	switch {
	case heard:
		w.quiet = 0
		if w.said {
			w.said = false
			c.Log("Receiving live from Viber Desktop works again", nil)
		}
	case w.last != 0 && newest > w.last:
		w.quiet++
	}
	w.last = newest
	if w.quiet >= 2 && !w.said {
		w.said = true
		text := "Receiving live from Viber Desktop does not work: new messages come only with the check every {every} seconds"
		params := map[string]any{"every": int(w.every.Seconds())}
		c.Log(text, params)
		c.Host().Alert("Viber Desktop", i18n.T(text, c.Lang(), params))
	}
}

// newestEvent is the newest of Viber's events in the last copy of its database.
func newestEvent(c *plugins.Context) int64 {
	q, err := db.ReadOnly(snapshotPath(c))
	if err != nil {
		return 0
	}
	defer q.Close()
	return db.Int(q, "SELECT ifnull(max(EventID), 0) FROM Events")
}

// follow imports, then again as Viber adds events and every `every`, until the bridge goes away.
func follow(ctx context.Context, c *plugins.Context, every time.Duration, w *watchdog) error {
	if err := runImport(ctx, c); err != nil {
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	kick := make(chan struct{}, 1)
	ended := make(chan error, 1)
	go func() {
		ended <- subscribe(ctx, socket(c), func() {}, func(event) {
			w.heard.Store(true)
			select {
			case kick <- struct{}{}:
			default:
			}
		})
	}()
	for {
		periodic := false
		select {
		case <-ctx.Done():
			return nil
		case <-ended:
			return nil // Viber stopped: waited for by Live
		case <-kick:
			select { // what arrives together comes in once
			case <-ctx.Done():
				return nil
			case <-time.After(700 * time.Millisecond):
			}
			select {
			case <-kick:
			default:
			}
		case <-time.After(every):
			periodic = true
		}
		if err := runImport(ctx, c); err != nil {
			c.Log("error: {e}", map[string]any{"e": err})
		} else if periodic {
			w.checked(c, newestEvent(c))
		}
	}
}

// --- the copy, to find Viber's own ids -----------------------------------------------------------

// lookup runs fn on the last copy of Viber's database; where it finds nothing, once more on a fresh
// copy (what was found is then newer than the last import).
func lookup[T comparable](ctx context.Context, c *plugins.Context, fn func(q *sql.DB) T) (T, error) {
	var zero T
	for fresh := 0; fresh < 2; fresh++ {
		if _, err := os.Stat(snapshotPath(c)); fresh == 1 || err != nil {
			l := lockOf(c)
			l.Lock()
			err := snapshot(ctx, c)
			l.Unlock()
			if err != nil {
				return zero, err
			}
		}
		q, err := db.ReadOnly(snapshotPath(c))
		if err != nil {
			return zero, err
		}
		v := fn(q)
		q.Close()
		if v != zero {
			return v, nil
		}
	}
	return zero, nil
}

func last10(s string) string {
	var d []rune
	for _, r := range s {
		if r >= '0' && r <= '9' {
			d = append(d, r)
		}
	}
	if len(d) > 10 {
		d = d[len(d)-10:]
	}
	return string(d)
}

// chatOf is Viber Desktop's chat of a conversation: a group (or the notes) by its token, a person's
// by their number.
func chatOf(ctx context.Context, c *plugins.Context, conv plugins.Conversation) (int64, error) {
	id, err := lookup(ctx, c, func(q *sql.DB) int64 {
		if t, ok := strings.CutPrefix(conv.Key, "group:"); ok {
			return db.Int(q, "SELECT ChatID FROM ChatInfo WHERE CAST(Token AS TEXT) = ?", t)
		}
		n := last10(conv.Key)
		if len(n) < 6 {
			return 0
		}
		return db.Int(q, "SELECT r.ChatID FROM ChatRelation r JOIN Contact k USING (ContactID) "+
			"JOIN ChatInfo i ON i.ChatID = r.ChatID WHERE (i.Token IS NULL OR i.Token = '' OR i.Token = 0) "+
			"AND substr(k.Number, -10) = ? ORDER BY i.TimeStamp DESC LIMIT 1", n)
	})
	if err == nil && id == 0 {
		err = errs.Plugin("This chat is not in Viber Desktop", 0)
	}
	return id, err
}

// eventOf is Viber Desktop's event of a message (by its token).
func eventOf(ctx context.Context, c *plugins.Context, msg plugins.Ref) (int64, error) {
	id, err := lookup(ctx, c, func(q *sql.DB) int64 {
		return db.Int(q, "SELECT EventID FROM Events WHERE CAST(Token AS TEXT) = ?", msg.Key)
	})
	if err == nil && id == 0 {
		err = errs.Plugin("Viber Desktop does not have this message", 0)
	}
	return id, err
}

// contactOf is Viber Desktop's contact of a person of the archive (by number, or Viber member id).
func contactOf(ctx context.Context, c *plugins.Context, addressID int64) (int64, error) {
	var value string
	if !db.Row(c.Store().Read(), "SELECT value FROM address WHERE id = ?", []any{addressID}, &value) {
		return 0, errs.Plugin("Unknown person to mention", 0)
	}
	mids := db.Strs(c.Store().Read(), "SELECT mid FROM viber_member WHERE number = ?", value)
	id, err := lookup(ctx, c, func(q *sql.DB) int64 {
		if n := last10(value); len(n) >= 6 {
			if id := db.Int(q, "SELECT ContactID FROM Contact WHERE substr(Number, -10) = ? LIMIT 1", n); id != 0 {
				return id
			}
		}
		for _, m := range append(mids, value) {
			if id := db.Int(q, "SELECT ContactID FROM Contact WHERE MID = ? LIMIT 1", m); id != 0 {
				return id
			}
		}
		return 0
	})
	if err == nil && id == 0 {
		err = errs.Plugin("This person is not among Viber Desktop's contacts", 0)
	}
	return id, err
}

// --- sending -------------------------------------------------------------------------------------

// failed is the bridge's refusal as the user is told it.
func failed(err error) error {
	var be *BridgeError
	switch {
	case errors.Is(err, ErrNotRunning):
		return errs.Plugin(notRunning, 503)
	case errors.As(err, &be) && be.What == "send-disabled":
		return errs.Plugin("Viber Desktop's bridge was started without sending (VIBER_ALLOW_SEND=1)", 0)
	case errors.As(err, &be) && be.What == "compose-mismatch":
		return errs.Plugin("Viber Desktop did not take the text as written: nothing was sent", 0)
	case errors.As(err, &be) && be.What == "not-editable":
		return errs.Plugin("Viber does not let this message be edited", 0)
	case errors.As(err, &be):
		return &errs.UserError{Code: "plugin", Status: 409, Text: "Viber Desktop could not do it: {what}",
			Params: map[string]any{"what": be.What}}
	}
	return err
}

func gate(c *plugins.Context) error {
	if !c.Bool("send") {
		return errs.Plugin("Sending is off in this source's settings", 0)
	}
	return nil
}

// settleAfter is how long Viber takes to write what an action did.
var settleAfter = time.Second

// settle brings what Viber wrote for an action into the archive.
func settle(ctx context.Context, c *plugins.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(settleAfter):
	}
	if err := runImport(context.WithoutCancel(ctx), c); err != nil {
		c.Log("error: {e}", map[string]any{"e": err})
	}
}

// parts is the text with each mention (its place, in characters) made a mention of a contact.
func parts(ctx context.Context, c *plugins.Context, text string, ms []plugins.Mention) ([]part, error) {
	sorted := append([]plugins.Mention(nil), ms...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	runes := []rune(text)
	var out []part
	textPart := func(s string) {
		if s != "" {
			out = append(out, part{Text: &s})
		}
	}
	at := 0
	for _, m := range sorted {
		start, end := min(max(m.Start, at), len(runes)), min(max(m.Start+m.Length, at), len(runes))
		id, err := contactOf(ctx, c, m.AddressID)
		if err != nil {
			return nil, err
		}
		textPart(string(runes[at:start]))
		out = append(out, part{Mention: id})
		at = end
	}
	textPart(string(runes[at:]))
	return out, nil
}

// sentDir keeps the files given to Viber to send (it reads them while it uploads): a day, then gone.
func sentDir(c *plugins.Context) (string, error) {
	d := filepath.Join(dir(c), "sent")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	if es, err := os.ReadDir(d); err == nil {
		for _, e := range es {
			if i, err := e.Info(); err == nil && time.Since(i.ModTime()) > 24*time.Hour {
				os.Remove(filepath.Join(d, e.Name()))
			}
		}
	}
	return d, nil
}

// Send sends into a chat Viber Desktop has: a file (Viber sends it without a caption: the text
// follows as a message), the text, a reply quoting a message, mentions of the group's people. What
// went is known from the events Viber adds for it, and returned once it is in the archive.
func (Plugin) Send(ctx context.Context, c *plugins.Context, conv plugins.Conversation, text string,
	reply *plugins.Reply, ms []plugins.Mention, file *plugins.File) (any, error) {
	if err := gate(c); err != nil {
		return nil, err
	}
	var need []string
	if file != nil {
		need = append(need, "file")
	}
	if text != "" && (reply != nil || len(ms) > 0) {
		need = append(need, "compose")
	} else if text != "" {
		need = append(need, "send")
	}
	if err := able(c, need...); err != nil {
		return nil, err
	}
	chat, err := chatOf(ctx, c, conv)
	if err != nil {
		return nil, err
	}
	var wanted []func(event) bool // what each message sent looks like, in the order they go
	if file != nil {
		wanted = append(wanted, func(e event) bool { return e.Body == "" })
	}
	if text != "" {
		wanted = append(wanted, func(e event) bool {
			if len(ms) > 0 { // Viber writes the mentions its own way: @, the name between U+2068 and U+2069
				return strings.ContainsRune(e.Body, '\u2068')
			}
			return strings.TrimSpace(e.Body) == strings.TrimSpace(text)
		})
	}
	w := watchSent(ctx, c, chat, wanted)
	defer w.stop()
	if file != nil {
		d, err := sentDir(c)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(file.Filename)
		if name == "." || name == "/" || name == "" {
			name = "file"
		}
		f, err := os.CreateTemp(d, "*-"+strings.ReplaceAll(name, "\n", " "))
		if err != nil {
			return nil, err
		}
		_, err = f.Write(file.Data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = act(ctx, socket(c), fmt.Sprintf("file %d %s", chat, f.Name()))
		}
		if err != nil {
			os.Remove(f.Name())
			return nil, failed(err)
		}
		if text != "" { // the text after the file: Viber writes the file once it is uploaded
			waitFile(ctx, c, f.Name())
		}
	}
	if text != "" {
		if reply == nil && len(ms) == 0 {
			err = act(ctx, socket(c), fmt.Sprintf("send %d %s", chat, escapeLine(text)))
		} else {
			cmp := composition{Chat: chat}
			if reply != nil {
				if cmp.Reply, err = eventOf(ctx, c, *reply); err != nil {
					return nil, err
				}
			}
			if cmp.Parts, err = parts(ctx, c, text, ms); err != nil {
				return nil, err
			}
			err = compose(ctx, socket(c), cmp)
		}
		if err != nil {
			return nil, failed(err)
		}
	}
	keys := w.wait(ctx)
	if keys == nil {
		settle(ctx, c)
		return M{"chat": chat}, nil
	}
	have := func() bool {
		return db.Int(c.Store().Read(), "SELECT count(*) FROM message WHERE conversation_id = ? AND key IN ("+
			db.Marks(len(keys))+")", append([]any{conv.ID}, db.Args(keys)...)...) == int64(len(keys))
	}
	if err := importUnless(context.WithoutCancel(ctx), c, have); err != nil {
		c.Log("error: {e}", map[string]any{"e": err})
	}
	return plugins.Sent{Keys: keys}, nil
}

// sentWait is how long the events of what was sent are waited for, once it went.
var sentWait = 5 * time.Second

// sentWatch follows the events Viber adds, from before something is sent, for those of what goes.
type sentWatch struct {
	stop  func()
	found chan []string // the tokens, once each wanted message has its event
}

// watchSent watches for the user's messages in chat that match wanted, one event each, in order; it
// watches nothing where the bridge cannot be followed (what went is then not known).
func watchSent(ctx context.Context, c *plugins.Context, chat int64, wanted []func(event) bool) *sentWatch {
	ctx, stop := context.WithCancel(ctx)
	w := &sentWatch{stop: stop, found: make(chan []string, 1)}
	if len(wanted) == 0 {
		return w
	}
	since := time.Now().Add(-2 * time.Second).UnixMilli() // a subscriber may first get some it had not
	ready := make(chan bool, 1)
	go func() {
		var keys []string
		err := subscribe(ctx, socket(c), func() { ready <- true }, func(e event) {
			if len(keys) == len(wanted) || !e.Outgoing || e.Type != 0 || e.Chat != chat || e.TS < since ||
				e.Token == "" || e.Token == "0" || !wanted[len(keys)](e) || !claim(e.Token) {
				return
			}
			if keys = append(keys, e.Token); len(keys) == len(wanted) {
				w.found <- keys
			}
		})
		if err != nil {
			c.Log("error: {e}", map[string]any{"e": err})
		}
		select { // no longer followed: not waited for
		case w.found <- nil:
		default:
		}
		select {
		case ready <- false:
		default:
		}
	}()
	select { // sending waits until the events are followed (or cannot be)
	case <-ready:
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
	}
	return w
}

// claimed: the events already taken as some send's (each send sees them all: two of the same text
// sent at once take one each), kept a minute.
var claimed = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// claim takes an event's token for a send; false where another took it.
func claim(token string) bool {
	claimed.Lock()
	defer claimed.Unlock()
	for k, at := range claimed.at {
		if time.Since(at) > time.Minute {
			delete(claimed.at, k)
		}
	}
	if _, ok := claimed.at[token]; ok {
		return false
	}
	claimed.at[token] = time.Now()
	return true
}

// wait is the tokens of what was sent (nil where they did not all come in time).
func (w *sentWatch) wait(ctx context.Context) []string {
	select {
	case keys := <-w.found:
		return keys
	case <-ctx.Done():
	case <-time.After(sentWait):
	}
	return nil
}

// fileWait is how long a file may take to upload before its text goes anyway.
var fileWait = 30 * time.Second

// sentStatus is Messages.Status of a message of the user's that went (a file's, once uploaded: its
// time is then the upload's end).
const sentStatus = 130

// waitFile waits until Viber has sent the message of a file it was given.
func waitFile(ctx context.Context, c *plugins.Context, path string) {
	end := time.Now().Add(fileWait)
	for time.Now().Before(end) && ctx.Err() == nil {
		l := lockOf(c)
		l.Lock()
		err := snapshot(ctx, c)
		l.Unlock()
		if err != nil {
			return
		}
		if q, err := db.ReadOnly(snapshotPath(c)); err == nil {
			found := db.Int(q, "SELECT count(*) FROM Messages WHERE PayloadPath = ? AND Status = ?", path, sentStatus) > 0
			q.Close()
			if found {
				return
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// quickCode is Viber's code of one of its own reactions (0: another emoji).
func quickCode(emoji string) int {
	bare := func(s string) string { return strings.ReplaceAll(s, "️", "") }
	for i, q := range quick {
		if bare(q) == bare(emoji) {
			return i + 1
		}
	}
	return 0
}

func (Plugin) React(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, emoji string) error {
	if err := gate(c); err != nil {
		return err
	}
	if err := able(c, "react"); err != nil {
		return err
	}
	ev, err := eventOf(ctx, c, msg)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("unreact %d", ev)
	if code := quickCode(emoji); code != 0 {
		line = fmt.Sprintf("react %d %d", ev, code)
	} else if emoji != "" {
		line = fmt.Sprintf("react %d %s", ev, strings.TrimSpace(emoji))
	}
	if err := act(ctx, socket(c), line); err != nil {
		return failed(err)
	}
	settle(ctx, c)
	return nil
}

func (Plugin) Edit(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, text string) error {
	if err := gate(c); err != nil {
		return err
	}
	if err := able(c, "compose"); err != nil {
		return err
	}
	chat, err := chatOf(ctx, c, conv)
	if err != nil {
		return err
	}
	ev, err := eventOf(ctx, c, msg)
	if err != nil {
		return err
	}
	if err := compose(ctx, socket(c), composition{Chat: chat, Edit: ev, Parts: []part{{Text: &text}}}); err != nil {
		return failed(err)
	}
	settle(ctx, c)
	return nil
}

func (Plugin) Delete(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref) error {
	if err := gate(c); err != nil {
		return err
	}
	if err := able(c, "delete"); err != nil {
		return err
	}
	ev, err := eventOf(ctx, c, msg)
	if err != nil {
		return err
	}
	if err := act(ctx, socket(c), fmt.Sprintf("delete %d", ev)); err != nil {
		return failed(err)
	}
	// marked here at once: the message may also leave Viber's database before the next import (deleted
	// from the history too, on any device), which an import cannot tell from history it never had
	if err := markDeleted(c, msg.ID); err != nil {
		return err
	}
	settle(ctx, c)
	return nil
}

func markDeleted(c *plugins.Context, messageID int64) (err error) {
	l := sourcekit.ImportLock(c)
	l.Lock()
	defer l.Unlock()
	a, err := archive.Open(c.Store().Path)
	if err != nil {
		return err
	}
	defer a.Close()
	defer archive.Recover(&err)
	importers.ApplyChange(a, messageID, importers.Change{Deleted: true})
	a.Commit()
	return nil
}

// MarkRead tells Viber the chat was read, where the user turned read receipts on.
func (Plugin) MarkRead(ctx context.Context, c *plugins.Context, conv plugins.Conversation, until int64) (int, error) {
	if !c.Bool("read_receipts") || !c.Bool("send") {
		return 0, nil
	}
	if err := able(c, "read-receipts"); err != nil {
		return 0, err
	}
	chat, err := chatOf(ctx, c, conv)
	if err != nil {
		return 0, nil // a chat Viber Desktop does not have: nothing to tell it
	}
	if err := act(ctx, socket(c), fmt.Sprintf("read %d", chat)); err != nil {
		return 0, failed(err)
	}
	return 1, nil
}
