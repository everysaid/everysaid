// Package plugins holds what every plugin is (a manifest, a few methods), the plugins Everysaid
// knows by id, and the instances of them in an archive.
//
// Kinds: `source` (brings messages, calls, people, media), `library` (where kept pictures and
// videos go), `contacts` (an address book), `analysis` (local models that suggest what no source
// says). A plugin is code; a `plugin_instance` row is one use of it (one phone, one account, one
// folder), with its own settings, secrets and state.
//
// Every plugin gives its manifest (Info). What else it can do it says by the interfaces it
// implements: Importer (bring what is new, then stop), Liver (stay connected), Sender, ReadMarker,
// ChatLister, Asker, Informer, Actioner; library plugins Library, contacts plugins Syncer.
package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/i18n"
)

type M = core.M

// Option is one choice of a select setting.
type Option struct{ Value, Label string }

// Keep: choosing an option of a select asks for a secret, kept; leaving it takes the secret away.
type Keep struct{ Key, Label string }

// Setting is one setting of a plugin, as its form shows it.
type Setting struct {
	Key      string
	Label    string
	Type     string // text, path, url, number, bool, secret, select ("" is text)
	Required bool
	Default  any
	Help     string
	Options  []Option
	Pattern  string          // what a value must look like (a regex), if anything
	Keeps    map[string]Keep // select: {option: secret}
}

func (s Setting) typ() string {
	if s.Type == "" {
		return "text"
	}
	return s.Type
}

// Valid says whether a value is of the setting's type (a number, an http(s) address, true or
// false) and fits its pattern; nothing given is for Required to judge.
func (s Setting) Valid(value any) bool {
	if value == nil || value == "" {
		return true
	}
	switch s.typ() {
	case "number":
		switch v := value.(type) {
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return false
			}
		case int, int64:
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return false
			}
		default:
			return false
		}
	case "bool":
		if _, ok := value.(bool); !ok {
			return false
		}
	case "url":
		v, ok := value.(string)
		if !ok {
			return false
		}
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return false
		}
	}
	if s.Pattern == "" {
		return true
	}
	ok, _ := regexp.MatchString("^(?:"+s.Pattern+")$", fmt.Sprint(value))
	return ok
}

func (s Setting) Manifest(lang string) M {
	opts := []M{}
	for _, o := range s.Options {
		opts = append(opts, M{"value": o.Value, "label": i18n.Tr(o.Label, lang)})
	}
	keeps := M{}
	for o, k := range s.Keeps {
		keeps[o] = M{"key": k.Key, "label": i18n.Tr(k.Label, lang)}
	}
	return M{"key": s.Key, "label": i18n.Tr(s.Label, lang), "type": s.typ(), "required": s.Required,
		"default": s.Default, "help": i18n.Tr(s.Help, lang), "options": opts, "keeps": keeps}
}

// Action is an extra button of a plugin.
type Action struct {
	ID, Label string
	Confirm   string // what the UI asks before running it (an action not to be undone), else ""
}

// ServiceInfo is how a service looks: its name, colour, short name, icon (an SVG path on a 24x24
// view, drawn in the colour), and whether it has messages (false for a service of calls only).
type ServiceInfo struct {
	Name, Color, Short, Icon string
	Messages                 *bool
}

// Info is a plugin's manifest.
type Info struct {
	ID, Name, Kind string // kind: source, library, contacts, analysis
	Services       []string
	Description    string
	Modes          []string // import, live
	LiveDefault    bool     // with "live": connect on its own once set up (the user can turn it off)
	Platforms      []string // linux, darwin, windows; nil: all
	Needs          []string // plain words for the user: "a cable", "libimobiledevice", "a login"
	Settings       []Setting
	CanSend        bool
	CanReply       bool // can send an answer to a given message (quoting it)
	CanMention     bool // can name people of a group in what it sends (@)
	CanMarkRead    bool // can tell the service a chat was read (read receipts), where the user allows
	CanSendFiles   bool // can send a file with a caption
	CanReact       bool // can put the user's reaction on a message, change it and take it back
	// Reactions: the emoji it can put, the service's own first (its quick ones); nil with CanReact,
	// any emoji. FreeReactions: any emoji besides these.
	Reactions     []string
	FreeReactions bool
	CanEdit       bool // can change the text of the user's own message
	CanDelete     bool // can delete the user's own message for everyone
	// EditWindow, DeleteWindow: how long after it was sent the service lets a message be edited,
	// deleted for everyone (0: no limit).
	EditWindow, DeleteWindow time.Duration
	// CanReportSpam: can tell the service someone is spam (SpamReporter): report and block them there.
	CanReportSpam bool
	Actions       []Action
	ServiceInfo   map[string]ServiceInfo
	// The names it brings for people, and how much they are trusted by default: "<service>/<kind>"
	// (kind: book, chat, profile), or "contacts" for an address book. A list: between equal weights,
	// the order they are declared in decides.
	NameWeights []Weight
	// The state of chats it reports (muted, pinned, read_until; archived only starts the app's own),
	// and how much it counts against other services by default ({field: weight}; 0: shown, not applied).
	StateWeights map[string]int
}

func (i *Info) HasMode(mode string) bool {
	modes := i.Modes
	if len(modes) == 0 {
		modes = []string{"import"}
	}
	for _, m := range modes {
		if m == mode {
			return true
		}
	}
	return false
}

// Platform is this system's name as plugins list them.
func Platform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	}
	return runtime.GOOS
}

// Available says whether the plugin runs on this system.
func (i *Info) Available() bool {
	if len(i.Platforms) == 0 {
		return true
	}
	for _, p := range i.Platforms {
		if p == Platform() {
			return true
		}
	}
	return false
}

// Weight is how much a source of names is trusted.
type Weight struct {
	Key    string
	Weight int
}

// Plugin is what every plugin is.
type Plugin interface {
	Info() *Info
}

// Importer brings what is new, then stops.
type Importer interface {
	RunImport(c *Context) error
}

// Liver stays connected, bringing what arrives, until ctx ends (nil) or it fails.
type Liver interface {
	Live(ctx context.Context, c *Context) error
}

// Conversation is a conversation as a plugin is given it.
type Conversation struct {
	ID      int64
	Key     string
	Service string
}

// Mention: a member of a group the text names, where it is in the text (in characters).
type Mention struct {
	Start, Length int
	AddressID     int64
}

// File is a file to send, the text its caption.
type File struct {
	Data     []byte
	Filename string
	MimeType string
}

// Ref is a message of the archive as a plugin is given it: its id, its key on the service, whether
// it is the user's own, and its time (Unix ms).
type Ref struct {
	ID       int64
	Key      string
	Outgoing bool
	TS       int64
}

// Reply is the message answered.
type Reply = Ref

// Sender sends into a conversation; it returns what it said: a Sent where it knows what went.
type Sender interface {
	Send(ctx context.Context, c *Context, conv Conversation, text string, reply *Reply, mentions []Mention, file *File) (any, error)
}

// Sent is what a Sender's message became in the archive, once it is there: the keys (message.key,
// in the conversation sent to), or the ids where the plugin wrote the rows itself.
type Sent struct {
	Keys []string `json:"keys,omitempty"`
	IDs  []int64  `json:"ids,omitempty"`
}

// Reactor puts the user's reaction on a message (emoji), in place of the one there was; "" takes it
// back. Only emoji its Info allows are given.
type Reactor interface {
	React(ctx context.Context, c *Context, conv Conversation, msg Ref, emoji string) error
}

// Editor changes the text of the user's own message (a caption, for a file).
type Editor interface {
	Edit(ctx context.Context, c *Context, conv Conversation, msg Ref, text string) error
}

// Deleter deletes the user's own message for everyone in the conversation.
type Deleter interface {
	Delete(ctx context.Context, c *Context, conv Conversation, msg Ref) error
}

// SpamReporter tells the service that the other person of a one-to-one conversation is spam: it
// reports them where the service lets it, blocks them, and deletes the chat with them there.
type SpamReporter interface {
	ReportSpam(ctx context.Context, c *Context, conv Conversation) error
}

// Forgetter drops what the instance keeps of a conversation outside the archive (its own copy of
// the service's messages), once the user removed it as spam.
type Forgetter interface {
	Forget(c *Context, conv Conversation) error
}

// SendingChecker says why the instance may not send now ("" when it may); without it, it may when
// set up.
type SendingChecker interface {
	NotSending(c *Context) string
}

// ReadMarker tells the service the user read a conversation up to `until` (Unix ms), if the
// instance's settings allow; it returns how many messages were marked.
type ReadMarker interface {
	MarkRead(ctx context.Context, c *Context, conv Conversation, until int64) (int, error)
}

// MediaFetcher brings a message's file from its service now, where it can (a live connection
// downloading what it never did): the file's path on this machine; "" and nil when it cannot (not
// connected, not a message it knows).
type MediaFetcher interface {
	FetchMedia(ctx context.Context, c *Context, messageID int64) (string, error)
}

// ChatLister gives the chats it can see, for the user's choice of what to import.
type ChatLister interface {
	Chats(c *Context) ([]M, error)
}

// Ask is something a run needs typed in, kept nowhere.
type Ask struct{ Key, Label string }

// Asker says what it needs typed in for each run (in c.Given).
type Asker interface {
	Asks(c *Context) []Ask
}

// Fact is a fact to show on its card.
type Fact struct{ Label, Value string }

type Informer interface {
	InfoFacts(c *Context) []Fact
}

// Actioner runs one of its actions; IdleActions are those with nothing to do now.
type Actioner interface {
	Action(c *Context, name string) error
}

type IdleActioner interface {
	IdleActions(c *Context) []string
}

// Checker says whether the instance is ready: (ok, message). Without it: its required settings.
type Checker interface {
	Check(c *Context) (bool, string)
}

// Syncer brings an address book (contacts plugins).
type Syncer interface {
	Sync(c *Context) error
}

// Fetched is a file a library gives back: a path, or bytes and their type.
type Fetched struct {
	Path string
	Data []byte
	Type string
}

// Library is where kept pictures and videos go.
type Library interface {
	Find(c *Context, sha256, path string) (string, error)
	Store(c *Context, path string, meta M) (string, error)
	Fetch(c *Context, ref, size string) (*Fetched, error)
}

// FileFetcher is a library that writes a stored file into a file of this computer itself (dest),
// a large original streamed rather than held in memory; it gives the file's type.
type FileFetcher interface {
	FetchTo(c *Context, ref, size, dest string) (string, error)
}

// Manifest is the plugin's manifest for the UI.
func Manifest(p Plugin, lang string) M {
	i := p.Info()
	settings := []M{}
	for _, s := range i.Settings {
		settings = append(settings, s.Manifest(lang))
	}
	needs := []string{}
	for _, n := range i.Needs {
		needs = append(needs, i18n.Tr(n, lang))
	}
	actions := []M{}
	for _, a := range i.Actions {
		x := M{"id": a.ID, "label": i18n.Tr(a.Label, lang)}
		if a.Confirm != "" {
			x["confirm"] = i18n.Tr(a.Confirm, lang)
		}
		actions = append(actions, x)
	}
	modes := i.Modes
	if len(modes) == 0 {
		modes = []string{"import"}
	}
	platforms := i.Platforms
	if len(platforms) == 0 {
		platforms = []string{"linux", "darwin", "win32"}
	}
	nw, sw := M{}, M{}
	for _, w := range i.NameWeights {
		nw[w.Key] = w.Weight
	}
	for k, v := range i.StateWeights {
		sw[k] = v
	}
	_, hasChats := p.(ChatLister)
	services := i.Services
	if services == nil {
		services = []string{}
	}
	return M{"id": i.ID, "name": i18n.Tr(i.Name, lang), "kind": i.Kind, "services": services,
		"description": i18n.Tr(i.Description, lang), "modes": modes, "platforms": platforms,
		"available": i.Available(), "needs": needs, "settings": settings, "can_send": i.CanSend,
		"can_reply": i.CanReply, "can_mention": i.CanMention, "can_mark_read": i.CanMarkRead,
		"can_send_files": i.CanSendFiles, "can_react": i.CanReact, "reactions": i.Reactions,
		"free_reactions": i.FreeReactions, "can_edit": i.CanEdit, "can_delete": i.CanDelete,
		"actions": actions, "has_chats": hasChats,
		"live_default": i.LiveDefault, "name_weights": nw, "state_weights": sw}
}

// Check is whether an instance is ready: the plugin's own check, else its required settings.
func Check(p Plugin, c *Context) (bool, string) {
	if ch, ok := p.(Checker); ok {
		return ch.Check(c)
	}
	return CheckSettings(p, c)
}

// CheckSettings is the default check: every required setting and secret given.
func CheckSettings(p Plugin, c *Context) (bool, string) {
	var missing []string
	for _, s := range p.Info().Settings {
		if !s.Required {
			continue
		}
		if s.typ() == "secret" {
			if c.Secret(s.Key) == "" {
				missing = append(missing, s.Label)
			}
		} else if !Truthy(c.Settings[s.Key]) {
			missing = append(missing, s.Label)
		}
	}
	if len(missing) > 0 {
		return false, "missing: " + strings.Join(missing, ", ")
	}
	return true, "ready"
}

// NotSending is why the instance may not send now ("" when it may).
func NotSending(p Plugin, c *Context) string {
	if s, ok := p.(SendingChecker); ok {
		return s.NotSending(c)
	}
	if ok, why := Check(p, c); !ok {
		return why
	}
	return ""
}

// Sending says whether this instance may send now.
func Sending(p Plugin, c *Context) bool { return p.Info().CanSend && NotSending(p, c) == "" }

// Host is what a plugin at work reaches of the server.
type Host interface {
	Store() *core.Store
	Emit(event M)
	Alert(title, body string)
}

// Context is one plugin instance at work: its settings, secrets and state, the archive, and a log.
type Context struct {
	host                  Host
	ID                    int64
	PluginID, Kind, Label string
	Settings              M
	State                 M
	DeviceID              *int64

	mu    sync.Mutex
	Lines []LogLine
	Bar   string            // the line a progress bar draws again and again, under the lines
	Given map[string]string // what the user typed in for this run (Asks): never stored

	logPath string // the log file of the run going on, if any
}

// LogLine is a line of the instance's log, with its time (Unix seconds).
type LogLine struct {
	At   int64
	Text string
}

// NewContext makes the context of an instance row.
func NewContext(h Host, row Instance) *Context {
	c := &Context{host: h, ID: row.ID, PluginID: row.Plugin, Kind: row.Kind, Label: row.Label,
		Settings: M{}, State: M{}, DeviceID: row.DeviceID, Given: map[string]string{}}
	if p := Get(row.Plugin); p != nil {
		for _, s := range p.Info().Settings {
			if s.Default != nil && s.typ() != "secret" {
				c.Settings[s.Key] = s.Default
			}
		}
	}
	var set M
	json.Unmarshal([]byte(row.Settings), &set)
	for k, v := range set {
		c.Settings[k] = v
	}
	json.Unmarshal([]byte(row.State), &c.State)
	if c.State == nil {
		c.State = M{}
	}
	return c
}

func (c *Context) Store() *core.Store { return c.host.Store() }
func (c *Context) Host() Host         { return c.host }

// Lang is the language the user last chose, for words said without a request.
func (c *Context) Lang() string { return c.Store().Language() }

// Str is a setting as text ("" when not set).
func (c *Context) Str(key string) string {
	if v, ok := c.Settings[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return ""
}

// Bool is a setting as a bool.
func (c *Context) Bool(key string) bool {
	switch v := c.Settings[key].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != "" && v != "0" && v != "false"
	}
	return false
}

// Num is a setting as a number (0 when not set).
func (c *Context) Num(key string) float64 {
	switch v := c.Settings[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		var f float64
		fmt.Sscan(v, &f)
		return f
	}
	return 0
}

// SetLogPath sends the instance's log lines to a file of one run (else to the day's file, as a
// live connection's do); "" ends it.
func (c *Context) SetLogPath(p string) {
	c.mu.Lock()
	c.logPath = p
	c.mu.Unlock()
}

// RunLogPath is the file of a run: <logs>/plugin-<id>/<date>-<time>-<what>.log.
func RunLogPath(iid int64, what string) string {
	folder := filepath.Join(config.Logs, fmt.Sprintf("plugin-%d", iid))
	os.MkdirAll(folder, 0o700)
	return filepath.Join(folder, time.Now().Format("20060102-150405")+"-"+what+".log")
}

// Log is a line of the instance's log, in English with {params}: said in the user's language. The
// last lines are kept for the interface; every line goes to the log files.
func (c *Context) Log(text string, params map[string]any) { c.log(text, params, false) }

// Logf is Log of a line already made (not translated).
func (c *Context) Logf(format string, args ...any) { c.log(fmt.Sprintf(format, args...), nil, false) }

// Redrawn is a progress bar's line as it ended: it stays the one bar line of the interface.
func (c *Context) Redrawn(text string) { c.log(text, nil, true) }

func (c *Context) log(text string, params map[string]any, redrawn bool) {
	line := i18n.T(text, c.Lang(), params)
	c.mu.Lock()
	path := c.logPath
	c.mu.Unlock()
	if path == "" {
		path = filepath.Join(config.Logs, fmt.Sprintf("plugin-%d", c.ID), time.Now().Format("20060102")+"-live.log")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			stamp := time.Now().Format("2006-01-02 15:04:05")
			var b strings.Builder
			for _, p := range splitLines(line) {
				b.WriteString(stamp + " " + p + "\n")
			}
			f.WriteString(b.String())
			f.Close()
		} // a full disk must not stop an import
	}
	if redrawn {
		c.Progress(line)
		return
	}
	c.mu.Lock()
	c.Lines = append(c.Lines, LogLine{time.Now().Unix(), line})
	if len(c.Lines) > 500 {
		c.Lines = c.Lines[len(c.Lines)-500:]
	}
	c.mu.Unlock()
	c.host.Emit(M{"type": "plugin_log", "instance": c.ID, "line": line})
}

// splitLines is Python's str.splitlines (at \n, \r, \r\n and Unicode's other line ends; none
// after the last), and [""] for nothing: each a line of the log file.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, s[start:i])
			if r == '\r' && strings.HasPrefix(s[i+n:], "\n") {
				n++
			}
			start = i + n
		}
		i += n
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// Progress is a line being drawn again (a progress bar): shown in place as it changes, not in the log.
func (c *Context) Progress(line string) {
	c.mu.Lock()
	c.Bar = line
	c.mu.Unlock()
	c.host.Emit(M{"type": "plugin_progress", "instance": c.ID, "line": line})
}

// LastLines are the last n lines of the log.
func (c *Context) LastLines(n int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []string{}
	from := len(c.Lines) - n
	if from < 0 {
		from = 0
	}
	for _, l := range c.Lines[from:] {
		out = append(out, l.Text)
	}
	return out
}

func (c *Context) SecretName(key string) string { return fmt.Sprintf("plugin-%d-%s", c.ID, key) }

func (c *Context) Secret(key string) string { return config.SecretOrEmpty(c.SecretName(key)) }

func (c *Context) SaveSecret(key, value string) (string, error) {
	return config.SaveSecret(c.SecretName(key), value)
}

func (c *Context) DeleteSecret(key string) error { return config.DeleteSecret(c.SecretName(key)) }

// SaveState keeps values in the instance's state.
func (c *Context) SaveState(values M) {
	c.mu.Lock()
	for k, v := range values {
		c.State[k] = v
	}
	b, _ := json.Marshal(c.State)
	c.mu.Unlock()
	c.Store().MustWrite(func(tx *sql.Tx) {
		db.Exec(tx, "UPDATE plugin_instance SET state = ? WHERE id = ?", string(b), c.ID)
	})
}

func (c *Context) Emit(event M) { c.host.Emit(event) }

// Truthy is Python's truth of a JSON value: not None, "", 0, false, [] or {}.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}
