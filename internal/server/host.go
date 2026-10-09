// Ports everysaid/server/host.py.
//
// The plugin host: runs plugin instances for one archive, and tells the open apps what happened.
//
// Imports run in a goroutine each (one import at a time: they write a lot; the plugins take
// ImportLock); live connections are goroutines that stay up while the server runs, restarted after
// an error with a growing pause. Every event (a plugin's log line, its state, new messages) goes to
// the apps listening on the WebSocket; new incoming messages also go out as push notifications,
// except for muted and archived chats.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/mcp"
	"everysaid/internal/plugins"
)

type M = core.M

// Host runs the plugin instances of one archive.
type Host struct {
	store *core.Store
	push  *Push
	log   *slog.Logger

	importLock sync.Mutex

	mu        sync.Mutex
	ctx       context.Context // the server's life: live connections and work end with it
	listeners map[chan M]struct{}
	queue     []M
	wake      chan struct{}
	running   map[int64]string // instance id -> "import"
	live      map[int64]context.CancelFunc
	lines     map[int64][]string // the instance's last log lines, for its panel
	bars      map[int64]string
	marking   map[[2]int64]bool            // (instance, conversation) being told it was read now
	reach     map[string]map[string]*reach // chat -> service -> where a person was looked for (find.go)

	outbox     *sql.DB // what waits to be sent (outbox.go)
	outboxOnce sync.Once
	outboxWake chan struct{}
	wg         sync.WaitGroup
}

// NewHost makes the host of an archive; nothing runs until Start.
func NewHost(store *core.Store, push *Push, log *slog.Logger) *Host {
	return &Host{store: store, push: push, log: log, listeners: map[chan M]struct{}{}, wake: make(chan struct{}, 1),
		running: map[int64]string{}, live: map[int64]context.CancelFunc{}, lines: map[int64][]string{},
		bars: map[int64]string{}, marking: map[[2]int64]bool{}, outboxWake: make(chan struct{}, 1)}
}

// Store is the archive (plugins.Host).
func (h *Host) Store() *core.Store { return h.store }

// ImportLock is held by whatever writes a lot into the archive: one import at a time.
func (h *Host) ImportLock() sync.Locker { return &h.importLock }

// --- events --------------------------------------------------------------------------------------

// Listen is a channel of the events from now on (dropped when it is full: an app that does not read
// gets the next ones, and refreshes on reconnecting anyway).
func (h *Host) Listen() chan M {
	ch := make(chan M, 1000)
	h.mu.Lock()
	h.listeners[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Host) Unlisten(ch chan M) {
	h.mu.Lock()
	delete(h.listeners, ch)
	h.mu.Unlock()
}

// Emit sends an event to the open apps, in the order emitted; a plugin's log lines are also kept for
// its panel. Before Start (and after the server stops) events go nowhere.
func (h *Host) Emit(event M) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if iid, ok := asInt(event["instance"]); ok {
		line, _ := event["line"].(string)
		switch event["type"] {
		case "plugin_log":
			l := append(h.lines[iid], line)
			if len(l) > 500 {
				l = l[len(l)-500:]
			}
			h.lines[iid] = l
		case "plugin_progress":
			h.bars[iid] = line
		}
	}
	if h.ctx == nil || h.ctx.Err() != nil {
		return
	}
	h.queue = append(h.queue, event)
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// dispatch hands the events to the listeners, one at a time (a "new" one described first: which
// chats, and push notifications).
func (h *Host) dispatch(ctx context.Context) {
	defer h.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
		}
		for {
			h.mu.Lock()
			if len(h.queue) == 0 {
				h.mu.Unlock()
				break
			}
			event := h.queue[0]
			h.queue = h.queue[1:]
			h.mu.Unlock()
			if event["type"] == "new" {
				var err error
				if event, err = h.describeNew(event); err != nil {
					h.log.Error("new messages", "error", err)
					continue
				}
			}
			h.mu.Lock()
			for ch := range h.listeners {
				select {
				case ch <- event:
				default:
				}
			}
			h.mu.Unlock()
		}
	}
}

// describeNew: which chats got what, so that the apps refresh those; push for incoming ones, except
// in archived chats (which stay archived: it is decided once, when the chat is first seen, then only
// by the user).
func (h *Host) describeNew(event M) (out M, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v\n%s", r, debug.Stack())
		}
	}()
	m0, m1 := pair(event["messages"])
	c0, c1 := pair(event["calls"])
	q := h.store.Read()
	states := core.States(h.store)
	chats := map[string]int64{}
	var incoming []Incoming
	mine := map[int64]bool{}
	for _, a := range db.Ints(q, "SELECT address_id FROM account") {
		mine[a] = true
	}
	type row struct {
		id, conv int64
		outgoing bool
		text     sql.NullString
		kind     string
		sender   sql.NullInt64
	}
	var rows []row
	db.Each(q, "SELECT m.id, m.conversation_id, m.outgoing, m.text, k.name, m.sender_id FROM message m "+
		"JOIN message_kind k ON k.id = m.kind_id WHERE m.id > ? AND m.id <= ? ORDER BY m.id", []any{m0, m1},
		func(scan func(...any)) {
			var r row
			scan(&r.id, &r.conv, &r.outgoing, &r.text, &r.kind, &r.sender)
			rows = append(rows, r)
		})
	for _, r := range rows {
		cid := core.ChatOfConversation(h.store, r.conv)
		if cid == "" {
			continue
		}
		chats[cid]++
		// no push for the owner's own, written on another device (a source may give it as received)
		if !r.outgoing && !(r.sender.Valid && mine[r.sender.Int64]) && r.kind != "system" && !states[cid].Archived {
			incoming = append(incoming, Incoming{cid, r.id, r.text.String, r.kind})
		}
	}
	if h.push != nil && len(incoming) > 0 {
		h.push.Notify(h.store, incoming)
	}
	return M{"type": "new", "chats": chats, "calls": c1 - c0}, nil
}

// Alert tells the user something about the app (a plugin's warning): on the open apps, and as a push.
func (h *Host) Alert(title, body string) {
	h.Emit(M{"type": "alert", "title": title, "body": body})
	if h.push != nil {
		h.push.Alert(h.store, title, body)
	}
}

// --- instances -----------------------------------------------------------------------------------

// Ctx is an instance's context, made afresh from its row (its settings as they are now).
func (h *Host) Ctx(iid int64) (*plugins.Context, error) {
	row := plugins.GetInstance(h.store, iid)
	if row == nil {
		return nil, &plugins.NoInstance{ID: iid}
	}
	return plugins.NewContext(h, *row), nil
}

func (h *Host) isRunning(iid int64) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running[iid]
}

func (h *Host) isLive(iid int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.live[iid]
	return ok
}

// Status is an instance as its card shows it: the instance, whether it is ready and what it needs,
// its last log lines.
func (h *Host) Status(iid int64, lang string) (M, error) {
	row := plugins.GetInstance(h.store, iid)
	if row == nil {
		return nil, core.ErrNotFound
	}
	p := plugins.Get(row.Plugin)
	ctx := plugins.NewContext(h, *row)
	ready, readyText := false, "unknown plugin"
	if p != nil {
		ready, readyText = plugins.Check(p, ctx)
	}
	out := plugins.Public(*row, lang)
	h.mu.Lock()
	var running any
	if r := h.running[iid]; r != "" {
		running = r
	}
	_, live := h.live[iid]
	lines := h.lines[iid]
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	logLines := append([]string{}, lines...)
	bar := h.bars[iid]
	h.mu.Unlock()
	asks, idle, info := []M{}, []string{}, []M{}
	if p != nil {
		if a, ok := p.(plugins.Asker); ok {
			for _, x := range a.Asks(ctx) {
				asks = append(asks, M{"key": x.Key, "label": i18n.Tr(x.Label, lang)})
			}
		}
		if a, ok := p.(plugins.IdleActioner); ok {
			if got := a.IdleActions(ctx); got != nil {
				idle = got
			}
		}
		if a, ok := p.(plugins.Informer); ok {
			for _, f := range a.InfoFacts(ctx) {
				info = append(info, M{"label": i18n.Tr(f.Label, lang), "value": i18n.Tr(f.Value, lang)})
			}
		}
	}
	out["running"] = running
	out["ready"] = ready
	out["ready_text"] = i18n.Tr(readyText, lang)
	out["live_capable"] = p != nil && p.Info().HasMode("live")
	out["live"] = live
	out["can_send"] = p != nil && p.Info().CanSend
	out["log"] = logLines
	out["bar"] = bar
	out["asks"] = asks
	out["idle_actions"] = idle
	out["info"] = info
	return out, nil
}

func (h *Host) setStatus(iid int64, status string) {
	h.store.MustWrite(func(tx *sql.Tx) {
		db.Exec(tx, "UPDATE plugin_instance SET last_run = ?, last_status = ? WHERE id = ?", time.Now().Unix(),
			core.Cut(status, 500), iid)
	})
}

// Run is an import (or a plugin's own action) of one instance, in a goroutine of its own. given:
// what the user typed in for this run (the plugin's Asks): kept only by this run, in memory.
func (h *Host) Run(iid int64, action string, given map[string]any) error {
	ctx, err := h.Ctx(iid)
	if err != nil {
		return err
	}
	h.mu.Lock()
	if h.running[iid] != "" {
		h.mu.Unlock()
		return errs.New("host.running", 409, nil)
	}
	h.running[iid] = "import"
	h.lines[iid] = nil // the panel shows this run (the earlier ones are in their files)
	h.bars[iid] = ""
	h.mu.Unlock()
	p := plugins.Get(ctx.PluginID)
	wanted := map[string]bool{}
	if a, ok := p.(plugins.Asker); ok && p != nil {
		for _, x := range a.Asks(ctx) {
			wanted[x.Key] = true
		}
	}
	for k, v := range given {
		if s, ok := v.(string); ok && wanted[k] && s != "" {
			ctx.Given[k] = s
		}
	}
	h.Emit(M{"type": "plugin", "instance": iid, "running": "import"})
	what := action
	if what == "" {
		what = "import"
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer h.recovered("run", iid)
		ctx.SetLogPath(plugins.RunLogPath(iid, fileWord(what)))
		defer func() {
			ctx.SetLogPath("")
			ctx.Given = map[string]string{} // gone with the run
		}()
		h.work(ctx, p, iid, action)
	}()
	return nil
}

// fileWord is a word as a part of a file name on any system ("person:12" is "person-12").
func fileWord(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '-'
		}
		return r
	}, s)
}

// work is the run itself (in its goroutine, its lines in a log file of its own).
func (h *Host) work(ctx *plugins.Context, p plugins.Plugin, iid int64, action string) {
	defer func() {
		h.mu.Lock()
		delete(h.running, iid)
		h.mu.Unlock()
		h.Emit(M{"type": "plugin", "instance": iid, "running": nil})
		h.Emit(M{"type": "changed"})
	}()
	what := action
	if what == "" {
		what = "import"
	}
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
				ctx.Logf("%s", strings.TrimSpace(string(debug.Stack()))) // whole, in the log file and the panel
			}
		}()
		ctx.Log("— {what} —", M{"what": i18n.Tr(what, ctx.Lang())})
		switch {
		case p == nil:
			return errors.New("unknown plugin")
		case action != "":
			a, ok := p.(plugins.Actioner)
			if !ok {
				return fmt.Errorf("no action %s", action)
			}
			return a.Action(ctx, action)
		default:
			if s, ok := p.(plugins.Syncer); ok {
				return s.Sync(ctx)
			}
			if i, ok := p.(plugins.Importer); ok {
				return i.RunImport(ctx)
			}
			return errors.New("nothing to run")
		}
	}()
	if err != nil {
		ctx.Log("error: {e}", M{"e": err.Error()})
		h.setStatus(iid, err.Error()) // the reason ("ok" when it worked): the interface says the rest
		return
	}
	h.setStatus(iid, "ok")
	h.AutoLive(iid) // now set up, perhaps
}

// AutoLive starts a live connection the plugin wants by default, unless the user turned it off or it
// is not set up yet.
func (h *Host) AutoLive(iid int64) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("live connection", "instance", iid, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	h.mu.Lock()
	started := h.ctx != nil
	h.mu.Unlock()
	if !started {
		return
	}
	row := plugins.GetInstance(h.store, iid)
	if row == nil {
		return
	}
	p := plugins.Get(row.Plugin)
	if p == nil || h.isLive(iid) || !row.Enabled || !p.Info().HasMode("live") {
		return
	}
	var set M
	json.Unmarshal([]byte(row.Settings), &set)
	want, ok := set["_live"]
	if !(ok && truthy(want) || !ok && p.Info().LiveDefault) {
		return
	}
	if ok, _ := plugins.Check(p, plugins.NewContext(h, *row)); !ok {
		return
	}
	if err := h.StartLive(iid); err != nil {
		if c, e := h.Ctx(iid); e == nil {
			c.Logf("live: %v", err)
		}
	}
}

// StartLive starts an instance's live connection (remembered: it comes back with the server).
func (h *Host) StartLive(iid int64) error {
	if h.isLive(iid) {
		return nil
	}
	if os.Getenv("EVERYSAID_NO_LIVE") != "" {
		// a trial on a copy of an archive: never connect with the accounts its sources hold, which
		// the user's own server may be connected with
		return errors.New("live connections are off (EVERYSAID_NO_LIVE)")
	}
	c, err := h.Ctx(iid)
	if err != nil {
		return err
	}
	p := plugins.Get(c.PluginID)
	liver, ok := p.(plugins.Liver)
	if p == nil || !ok || !p.Info().HasMode("live") {
		return errs.New("host.no_live", 0, nil)
	}
	h.mu.Lock()
	if h.ctx == nil {
		h.mu.Unlock()
		return errors.New("the server is not running")
	}
	if _, ok := h.live[iid]; ok {
		h.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(h.ctx)
	h.live[iid] = cancel
	h.mu.Unlock()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.keep(ctx, iid, p, liver)
	}()
	return plugins.Update(h.store, iid, nil, M{"_live": true}, nil, false)
}

// keep keeps a live connection up, again after a growing pause when it fails.
func (h *Host) keep(ctx context.Context, iid int64, p plugins.Plugin, liver plugins.Liver) {
	pause := 5 * time.Second
	sleep := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
	for ctx.Err() == nil {
		var stop bool
		pause, stop = h.connect(ctx, iid, p, liver, pause, sleep)
		if stop {
			return
		}
	}
}

// connect is one connection of keep, and the pause after it: the next pause, and whether to stop
// (the server's end, or the instance gone). What breaks here (a log line on a broken archive) is
// written to the server's log, and the connection is tried again.
func (h *Host) connect(ctx context.Context, iid int64, p plugins.Plugin, liver plugins.Liver, pause time.Duration,
	sleep func(time.Duration) bool) (next time.Duration, stop bool) {
	next = pause
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("live connection", "instance", iid, "panic", r, "stack", string(debug.Stack()))
			next, stop = min(pause*2, 600*time.Second), !sleep(pause)
		}
	}()
	c, err := h.Ctx(iid)
	if err != nil {
		return next, true // the instance went
	}
	if ok, why := plugins.Check(p, c); !ok {
		c.Logf("live: %s", why)
		return next, !sleep(time.Minute)
	}
	h.Emit(M{"type": "plugin", "instance": iid, "live": true})
	h.wakeOutbox() // what waits may go now
	err = func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
				h.log.Error("live connection", "instance", iid, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		return liver.Live(ctx, c)
	}()
	if ctx.Err() != nil {
		return next, true
	}
	if err == nil {
		next = 5 * time.Second
	} else {
		c.Log("live: error {e}; again in {pause}s", M{"e": err.Error(), "pause": int(pause.Seconds())})
	}
	if !sleep(next) {
		return next, true
	}
	if err != nil {
		next = min(pause*2, 600*time.Second)
	}
	return next, false
}

// recovered, deferred at the top of a goroutine of the host, writes what broke it to the server's
// log instead of ending the server.
func (h *Host) recovered(what string, iid int64) {
	if r := recover(); r != nil {
		h.log.Error(what, "instance", iid, "panic", r, "stack", string(debug.Stack()))
	}
}

// StopLive ends an instance's live connection; remember: the user turned it off (it stays off).
func (h *Host) StopLive(iid int64, remember bool) {
	h.mu.Lock()
	if cancel, ok := h.live[iid]; ok {
		cancel()
		delete(h.live, iid)
	}
	h.mu.Unlock()
	if remember {
		plugins.Update(h.store, iid, nil, M{"_live": false}, nil, false)
	}
	if row := plugins.GetInstance(h.store, iid); row != nil {
		if s, ok := plugins.Get(row.Plugin).(plugins.LiveStopper); ok {
			c := plugins.NewContext(h, *row)
			h.wg.Add(1)
			go func() {
				defer h.wg.Done()
				defer h.recovered("live stopped", iid)
				s.LiveStopped(c)
			}()
		}
	}
	h.Emit(M{"type": "plugin", "instance": iid, "live": false})
}

// Start runs the host until ctx ends: events reach the apps, and the live connections the user
// turned on (or the plugins want by default, once set up) start.
func (h *Host) Start(ctx context.Context) {
	h.mu.Lock()
	if h.ctx != nil {
		h.mu.Unlock()
		return
	}
	h.ctx = ctx
	h.mu.Unlock()
	h.wg.Add(2)
	go h.dispatch(ctx)
	go h.runOutbox(ctx)
	for _, row := range h.sourcesAndAnalysis() {
		h.AutoLive(row.ID)
	}
}

// Wait waits, after the server's context ended, for the live connections and runs to stop (at most
// so long).
func (h *Host) Wait(d time.Duration) {
	h.mu.Lock()
	for iid, cancel := range h.live {
		cancel()
		delete(h.live, iid)
	}
	h.mu.Unlock()
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

func (h *Host) sourcesAndAnalysis() []plugins.Instance {
	return append(plugins.Instances(h.store, "source"), plugins.Instances(h.store, "analysis")...)
}

// --- sending -------------------------------------------------------------------------------------

type sender struct {
	iid int64
	p   plugins.Plugin
}

// Senders are the sources that may send now, as each plugin says.
func (h *Host) Senders() []sender {
	var out []sender
	for _, row := range h.sourcesAndAnalysis() {
		p := plugins.Get(row.Plugin)
		if row.Enabled && p != nil && p.Info().CanSend && plugins.Sending(p, plugins.NewContext(h, row)) {
			out = append(out, sender{row.ID, p})
		}
	}
	return out
}

// Able is the services something can send to now with a flag (can_reply, can_mention,
// can_send_files; "" for any); kept until the archive changes, and at most a minute (a login outside
// the app changes only the keyring, and a plugin's check may read it: too slow for every chat opened).
func (h *Host) Able(flag string) map[string]bool {
	key := "able:" + flag
	return core.CachedFor(h.store, key, time.Minute, func() map[string]bool {
		out := map[string]bool{}
		for _, s := range h.Senders() {
			i := s.p.Info()
			ok := map[string]bool{"": true, "can_reply": i.CanReply, "can_mention": i.CanMention, "can_send_files": i.CanSendFiles}[flag]
			if ok {
				for _, svc := range i.Services {
					out[svc] = true
				}
			}
		}
		return out
	})
}

// Unsendable is {service: why}: the services an enabled source reaches but may not send to now, each
// with what its source says is missing (English, as plugins word it).
func (h *Host) Unsendable() map[string]string {
	return core.CachedFor(h.store, "unsendable", time.Minute, func() map[string]string {
		out := map[string]string{}
		for _, row := range h.sourcesAndAnalysis() {
			p := plugins.Get(row.Plugin)
			if row.Enabled && p != nil && p.Info().CanSend {
				if reason := plugins.NotSending(p, plugins.NewContext(h, row)); reason != "" {
					for _, s := range p.Info().Services {
						if _, ok := out[s]; !ok {
							out[s] = reason
						}
					}
				}
			}
		}
		return out
	})
}

// SendRequest is what the user sends into a chat.
type SendRequest struct {
	Text           string
	ConversationID int64  // through this conversation of the chat (0: where it was last active)
	Service        string // through this service ("" any)
	ReplyTo        int64  // the message of the chat it answers (0: none)
	Mentions       []plugins.Mention
	File           *plugins.File
}

// Send sends text in a chat through the plugin that reaches its service; it returns what it said,
// with the messages that went where the plugin knows them (to take the place of what the chat
// showed while it was sent).
// ReplyTo: then through its conversation, by a plugin that can reply. Mentions: members of the
// group the text names (left out where the plugin cannot mention: the text says them anyway).
// File: the text its caption, only through a plugin that can send files.
func (h *Host) Send(ctx context.Context, chatID string, r SendRequest) (out M, err error) {
	defer db.Recover(&err)
	c := core.Index(h.store).Chats[chatID]
	if c == nil {
		return nil, core.ErrNotFound
	}
	convs := c.Conversations
	q := h.store.Read()
	var answered *plugins.Reply
	conversationID := r.ConversationID
	if r.ReplyTo != 0 {
		var conv int64
		var key sql.NullString
		if !db.Row(q, "SELECT conversation_id, key FROM message WHERE id = ?", []any{r.ReplyTo}, &conv, &key) || !contains(convs, conv) {
			return nil, errs.New("chat.not_in_chat", 0, nil)
		}
		if key.String == "" {
			return nil, errs.New("chat.cannot_reply", 409, nil)
		}
		conversationID, answered = conv, &plugins.Reply{ID: r.ReplyTo, Key: key.String}
	}
	if conversationID != 0 {
		if !contains(convs, conversationID) {
			return nil, errs.New("chat.not_in_chat", 0, nil)
		}
		convs = []int64{conversationID}
	}
	type option struct {
		last         int64
		conv         int64
		key, service string
		s            sender
	}
	var options []option
	senders := h.Senders()
	for _, conv := range convs {
		var key sql.NullString
		var svc string
		db.Row(q, "SELECT c.key, s.name FROM conversation c JOIN service s ON s.id = c.service_id WHERE c.id = ?",
			[]any{conv}, &key, &svc)
		last := db.Int(q, "SELECT coalesce(max(ts), 0) FROM message WHERE conversation_id = ?", conv)
		for _, s := range senders {
			i := s.p.Info()
			if contains(i.Services, svc) && (answered == nil || i.CanReply) && (r.File == nil || i.CanSendFiles) {
				options = append(options, option{last, conv, key.String, svc, s})
			}
		}
	}
	if r.Service != "" {
		var keep []option
		for _, o := range options {
			if o.service == r.Service {
				keep = append(keep, o)
			}
		}
		options = keep
		// a first message where the person was found (Find): into a conversation the archive has not yet
		if iid, key, ok := h.reached(chatID, r.Service); ok && len(keep) == 0 && answered == nil && conversationID == 0 {
			for _, s := range senders {
				if s.iid == iid && (r.File == nil || s.p.Info().CanSendFiles) {
					options = append(options, option{key: key, service: r.Service, s: s})
				}
			}
		}
	}
	if len(options) == 0 {
		code := "chat.no_sender"
		if answered != nil {
			code = "chat.cannot_reply"
		} else if r.File != nil {
			code = "chat.cannot_send_files"
		}
		return nil, errs.New(code, 409, nil)
	}
	sort.SliceStable(options, func(i, j int) bool { return options[i].last > options[j].last }) // where the chat was last active
	o := options[0]
	if len(r.Mentions) > 0 {
		members := map[int64]bool{}
		for _, a := range db.Ints(q, "SELECT address_id FROM conversation_member WHERE conversation_id = ?", o.conv) {
			members[a] = true
		}
		for _, m := range r.Mentions {
			if !members[m.AddressID] {
				return nil, errs.New("chat.not_a_member", 0, nil)
			}
		}
	}
	snd, ok := o.s.p.(plugins.Sender)
	if !ok {
		return nil, errs.New("chat.no_sender", 409, nil)
	}
	pc, err := h.Ctx(o.s.iid)
	if err != nil {
		return nil, err
	}
	var mentions []plugins.Mention
	if len(r.Mentions) > 0 && o.s.p.Info().CanMention {
		mentions = r.Mentions
	}
	result, err := snd.Send(ctx, pc, plugins.Conversation{ID: o.conv, Key: o.key, Service: o.service}, r.Text, answered, mentions, r.File)
	if err != nil {
		return nil, err
	}
	if o.conv == 0 { // a first message: its conversation, if the source has brought it already
		if o.conv = h.conversationOf(o.service, o.key); o.conv != 0 {
			h.forget(chatID, o.service)
		}
	}
	h.Emit(M{"type": "changed"})
	return M{"service": o.service, "conversation_id": o.conv, "messages": h.sent(o.conv, result), "result": result}, nil
}

// sent is what a plugin's Send says went, as the chat shows it (none where it cannot say).
func (h *Host) sent(conv int64, result any) []M {
	s, ok := result.(plugins.Sent)
	if !ok {
		return []M{}
	}
	q := h.store.Read()
	var items []core.Item
	for _, id := range s.IDs {
		items = append(items, core.Item{Type: "m", ID: id})
	}
	for _, k := range s.Keys {
		if id := db.Int(q, "SELECT id FROM message WHERE conversation_id = ? AND key = ?", conv, k); id != 0 {
			items = append(items, core.Item{Type: "m", ID: id})
		}
	}
	return core.Hydrate(h.store, items, nil)
}

// MessageAction is what the user does to one message: a reaction ("" takes the user's back), an
// edit, a deletion for everyone.
type MessageAction struct {
	Kind  string // react, edit, delete
	Emoji string
	Text  string
}

// able says whether a plugin's manifest allows an action ("" for sending at all).
func able(i *plugins.Info, kind string) bool {
	switch kind {
	case "react":
		return i.CanReact
	case "edit":
		return i.CanEdit
	case "delete":
		return i.CanDelete
	}
	return true
}

// Allows says whether the plugin may put this emoji; the variation selector is left out of the
// comparison ("❤️" is Telegram's "❤").
func Allows(i *plugins.Info, emoji string) bool {
	if emoji == "" || i.Reactions == nil || i.FreeReactions {
		return true
	}
	bare := func(s string) string { return strings.ReplaceAll(s, "\ufe0f", "") }
	for _, r := range i.Reactions {
		if bare(r) == bare(emoji) {
			return true
		}
	}
	return false
}

// Act does an action on a message through the source that reaches its service and can; the source's
// own import then brings the change into the archive. Edits and deletions are of the user's own
// messages only, within the service's time for them.
func (h *Host) Act(ctx context.Context, messageID int64, a MessageAction) (out M, err error) {
	defer db.Recover(&err)
	q := h.store.Read()
	var conv, ts int64
	var outgoing bool
	var key, convKey sql.NullString
	var svc string
	if !db.Row(q, "SELECT m.conversation_id, m.ts, m.outgoing, m.key, c.key, s.name FROM message m "+
		"JOIN conversation c ON c.id = m.conversation_id JOIN service s ON s.id = c.service_id WHERE m.id = ?",
		[]any{messageID}, &conv, &ts, &outgoing, &key, &convKey, &svc) {
		return nil, core.ErrNotFound
	}
	if key.String == "" {
		return nil, errs.New("message.no_key", 409, nil)
	}
	if a.Kind != "react" && !outgoing {
		return nil, errs.New("message.not_own", 409, nil)
	}
	var chosen *sender
	for _, s := range h.Senders() {
		i := s.p.Info()
		if contains(i.Services, svc) && able(i, a.Kind) {
			s := s
			chosen = &s
			break
		}
	}
	if chosen == nil {
		code := map[string]string{"react": "message.cannot_react", "edit": "message.cannot_edit",
			"delete": "message.cannot_delete"}[a.Kind]
		return nil, errs.New(code, 409, nil)
	}
	i := chosen.p.Info()
	window := map[string]time.Duration{"edit": i.EditWindow, "delete": i.DeleteWindow}[a.Kind]
	if window > 0 && time.Since(time.UnixMilli(ts)) > window {
		return nil, errs.New("message.too_late", 409, nil)
	}
	if a.Kind == "react" && !Allows(i, a.Emoji) {
		return nil, errs.New("message.reaction_not_allowed", 409, nil)
	}
	pc, err := h.Ctx(chosen.iid)
	if err != nil {
		return nil, err
	}
	c := plugins.Conversation{ID: conv, Key: convKey.String, Service: svc}
	ref := plugins.Ref{ID: messageID, Key: key.String, Outgoing: outgoing, TS: ts}
	switch a.Kind {
	case "react":
		if r, ok := chosen.p.(plugins.Reactor); ok {
			err = r.React(ctx, pc, c, ref, a.Emoji)
		} else {
			err = errs.New("message.cannot_react", 409, nil)
		}
	case "edit":
		if r, ok := chosen.p.(plugins.Editor); ok {
			err = r.Edit(ctx, pc, c, ref, a.Text)
		} else {
			err = errs.New("message.cannot_edit", 409, nil)
		}
	case "delete":
		if r, ok := chosen.p.(plugins.Deleter); ok {
			err = r.Delete(ctx, pc, c, ref)
		} else {
			err = errs.New("message.cannot_delete", 409, nil)
		}
	}
	if err != nil {
		return nil, err
	}
	h.Emit(M{"type": "changed"})
	return M{"service": svc, "conversation_id": conv}, nil
}

// Reactable is {service: the emoji its source can put now (nil: any)}, for the services a source may
// react on now; editable and deletable, {service: the seconds after sending it allows (0: always)}.
func (h *Host) Reactable() (react map[string][]string, free map[string]bool, edit, del map[string]int64) {
	react, free, edit, del = map[string][]string{}, map[string]bool{}, map[string]int64{}, map[string]int64{}
	for _, s := range h.Senders() {
		i := s.p.Info()
		for _, svc := range i.Services {
			if _, ok := react[svc]; i.CanReact && !ok {
				react[svc] = i.Reactions
				free[svc] = i.Reactions == nil || i.FreeReactions
			}
			if _, ok := edit[svc]; i.CanEdit && !ok {
				edit[svc] = int64(i.EditWindow / time.Second)
			}
			if _, ok := del[svc]; i.CanDelete && !ok {
				del[svc] = int64(i.DeleteWindow / time.Second)
			}
		}
	}
	return
}

// MarkRead: the user read the chat up to `until` (Unix ms) here: each of its conversations with
// something newer from the others than the service last said was read is told so, through the
// plugins that can and are connected (a live one only while its connection runs; each sends only
// where its user allowed), one at a time per conversation. Their errors go to their logs.
func (h *Host) MarkRead(ctx context.Context, chatID string, until int64) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v\n%s", r, debug.Stack())
		}
	}()
	c := core.Index(h.store).Chats[chatID]
	if c == nil {
		return nil
	}
	q := h.store.Read()
	for _, row := range h.sourcesAndAnalysis() {
		p := plugins.Get(row.Plugin)
		if p == nil || !row.Enabled || !p.Info().CanMarkRead {
			continue
		}
		marker, ok := p.(plugins.ReadMarker)
		if !ok || p.Info().HasMode("live") && !h.isLive(row.ID) {
			continue
		}
		pc := plugins.NewContext(h, row)
		for _, conv := range c.Conversations {
			var key sql.NullString
			var svc string
			var newest, told sql.NullInt64
			db.Row(q, "SELECT c.key, s.name, (SELECT max(ts) FROM message WHERE conversation_id = c.id AND NOT outgoing "+
				"AND ts <= ?), (SELECT max(value) FROM state_report WHERE conversation_id = c.id AND field = 'read_until') "+
				"FROM conversation c JOIN service s ON s.id = c.service_id WHERE c.id = ?", []any{until, conv},
				&key, &svc, &newest, &told)
			if !contains(p.Info().Services, svc) || newest.Int64 == 0 || told.Int64 >= newest.Int64 {
				continue
			}
			k := [2]int64{row.ID, conv}
			h.mu.Lock() // looked at and taken at once: of two reads at once, one tells the service
			busy := h.marking[k]
			h.marking[k] = true
			h.mu.Unlock()
			if busy {
				continue
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						pc.Log("read receipts: {e}", M{"e": fmt.Sprint(r)})
					}
					h.mu.Lock()
					delete(h.marking, k)
					h.mu.Unlock()
				}()
				if _, err := marker.MarkRead(ctx, pc, plugins.Conversation{ID: conv, Key: key.String, Service: svc}, until); err != nil {
					pc.Log("read receipts: {e}", M{"e": err.Error()}) // reading here must not fail on a service's error
				}
			}()
		}
	}
	return nil
}

// MarkReadSoon is MarkRead in the background, not waited for (what fails outside a plugin goes to
// the server's log, not lost).
func (h *Host) MarkReadSoon(chatID string, until int64) {
	h.mu.Lock()
	ctx := h.ctx
	h.mu.Unlock()
	if ctx == nil {
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		if err := h.MarkRead(ctx, chatID, until); err != nil {
			h.log.Error("read receipts", "error", err)
		}
	}()
}

// --- libraries -----------------------------------------------------------------------------------

func (h *Host) libraries() []plugins.Instance {
	var out []plugins.Instance
	for _, r := range plugins.Instances(h.store, "library") {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out
}

func (h *Host) defaultLibrary() *plugins.Instance {
	libs := h.libraries()
	for i := range libs {
		if libs[i].IsDefault {
			return &libs[i]
		}
	}
	if len(libs) > 0 {
		return &libs[0]
	}
	return nil
}

// LocalFile is where the archive keeps a file, if it is there.
func (h *Host) LocalFile(sha256 string) string {
	rel := db.Str(h.store.Read(), "SELECT path FROM media WHERE sha256 = ?", sha256)
	if rel == "" {
		return ""
	}
	p := filepath.Join(archive.MediaRoot(), rel)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func link(store *core.Store, iid int64, label, sha256, ref, method string) {
	store.MustWrite(func(tx *sql.Tx) {
		db.Exec(tx, "INSERT INTO library_link (sha256, library, asset_id, method, linked_at, instance_id) "+
			"VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (sha256, library) DO UPDATE SET asset_id = excluded.asset_id, "+
			"method = excluded.method, instance_id = excluded.instance_id", sha256, label, ref, method, time.Now().Unix(), iid)
	})
}

// ToLibrary stores a file in a library (the default one unless named), unless it is there already;
// either way the archive records the link.
func (h *Host) ToLibrary(sha256 string, iid int64, dateMs *int64) (out M, err error) {
	defer db.Recover(&err)
	var row *plugins.Instance
	if iid != 0 {
		row = plugins.GetInstance(h.store, iid)
	} else {
		row = h.defaultLibrary()
	}
	if row == nil {
		return nil, errs.New("library.none", 409, nil)
	}
	path := h.LocalFile(sha256)
	p := plugins.Get(row.Plugin)
	lib, ok := p.(plugins.Library)
	if p == nil || !ok {
		return nil, fmt.Errorf("%s: not a library", row.Plugin)
	}
	ctx := plugins.NewContext(h, *row)
	q := h.store.Read()
	var known string
	if db.Row(q, "SELECT asset_id FROM library_link WHERE sha256 = ? AND instance_id = ?", []any{sha256, row.ID}, &known) {
		// stored there before (the stored copy may differ: a date or a make written in)
		return M{"already": true, "ref": known, "library": row.Label}, nil
	}
	if path == "" {
		return nil, errs.New("file_gone", 409, nil)
	}
	found, err := lib.Find(ctx, sha256, path)
	if err != nil {
		return nil, err
	}
	if found != "" {
		link(h.store, row.ID, row.Label, sha256, found, "checksum")
		return M{"already": true, "ref": found, "library": row.Label}, nil
	}
	var mime, service sql.NullString
	var msgTS sql.NullInt64
	db.Row(q, "SELECT md.mime, min(m.ts), s.name FROM media md JOIN attachment a ON a.sha256 = md.sha256 "+
		"JOIN message m ON m.id = a.message_id JOIN service s ON s.id = m.service_id WHERE md.sha256 = ?", []any{sha256},
		&mime, &msgTS, &service)
	var decided sql.NullInt64
	db.Row(q, "SELECT date_ms FROM media_decision WHERE sha256 = ?", []any{sha256}, &decided)
	var when any = nullInt(msgTS)
	if decided.Int64 != 0 {
		when = decided.Int64
	}
	if dateMs != nil && *dateMs != 0 {
		when = *dateMs
	}
	ref, err := lib.Store(ctx, path, M{"sha256": sha256, "mime": nullStr(mime), "date_ms": when, "service": nullStr(service)})
	if err != nil {
		return nil, err
	}
	link(h.store, row.ID, row.Label, sha256, ref, "upload")
	return M{"already": false, "ref": ref, "library": row.Label}, nil
}

// Fetch is a file from the library that holds it (a path, or bytes and their type), or nil.
func (h *Host) Fetch(sha256, size string) *plugins.Fetched {
	type ref struct {
		iid   int64
		asset string
	}
	var refs []ref
	db.Each(h.store.Read(), "SELECT instance_id, asset_id FROM library_link WHERE sha256 = ? AND instance_id IS NOT NULL",
		[]any{sha256}, func(scan func(...any)) {
			var r ref
			scan(&r.iid, &r.asset)
			refs = append(refs, r)
		})
	for _, r := range refs {
		row := plugins.GetInstance(h.store, r.iid)
		if row == nil || !row.Enabled {
			continue
		}
		lib, ok := plugins.Get(row.Plugin).(plugins.Library)
		if !ok {
			continue
		}
		got, err := func() (got *plugins.Fetched, err error) {
			defer db.Recover(&err)
			return lib.Fetch(plugins.NewContext(h, *row), r.asset, size)
		}()
		if err == nil && got != nil {
			return got
		}
	}
	return nil
}

// FetchMedia (the MCP's MediaFetcher) asks the sources that read a message's service, and can bring
// a file now, for its file: enabled ones, a live one only while its connection runs. The first path
// given wins; mcp.ErrNoFetch when none could.
func (h *Host) FetchMedia(ctx context.Context, _ *core.Store, messageID int64) (path string, err error) {
	defer db.Recover(&err)
	svc := db.Str(h.store.Read(), "SELECT s.name FROM message m JOIN service s ON s.id = m.service_id WHERE m.id = ?", messageID)
	if svc == "" {
		return "", mcp.ErrNoFetch
	}
	var last error
	for _, row := range h.sourcesAndAnalysis() {
		p := plugins.Get(row.Plugin)
		f, ok := p.(plugins.MediaFetcher)
		if !ok || !row.Enabled || !contains(p.Info().Services, svc) || p.Info().HasMode("live") && !h.isLive(row.ID) {
			continue
		}
		got, err := func() (got string, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("%v", r)
				}
			}()
			return f.FetchMedia(ctx, plugins.NewContext(h, row), messageID)
		}()
		if err == nil && got != "" {
			return got, nil
		}
		if err != nil {
			last = err
		}
	}
	if last != nil {
		return "", last
	}
	return "", mcp.ErrNoFetch
}

// --- helpers -------------------------------------------------------------------------------------

func contains[T comparable](xs []T, x T) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// pair is [a, b] of an event's ids, however they came (Go ints, or numbers from JSON).
func pair(v any) (int64, int64) {
	switch x := v.(type) {
	case []int64:
		if len(x) == 2 {
			return x[0], x[1]
		}
	case [2]int64:
		return x[0], x[1]
	case []any:
		if len(x) == 2 {
			a, _ := asInt(x[0])
			b, _ := asInt(x[1])
			return a, b
		}
	}
	return 0, 0
}

func asInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	}
	return 0, false
}
