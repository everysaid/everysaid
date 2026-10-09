// Package signal is the `signal` source: Signal through a helper program linked to the account as a
// secondary device (like Signal Desktop).
//
// New: the Python never had Signal. The helper (bridges/signal, everysaid-signal) is written in
// Rust on presage and libsignal, which are AGPL-3.0; it is a program of its own, spoken to in JSON
// lines over its stdin and stdout, so that Everysaid's binary links no AGPL code. Its state (the
// device's keys, what it received) is kept, encrypted with a passphrase this plugin makes and keeps
// in the keyring, in `<data>/signal/<instance>/`; what it says goes to `<cache>/signal/<instance>/
// signal.db` and the files it fetches to `media/` beside it, and from there into the archive
// (import.go).
//
// One helper runs per instance at a time (two connections of one device would share its
// messages between them): the live connection's while it runs, else one started for the work at
// hand (an import, a link, a message sent) and stopped after it.
package signal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

type M = plugins.M

// Plugin is the `signal` source.
type Plugin struct{}

func init() { plugins.Register(Plugin{}) }

func (Plugin) Info() *plugins.Info {
	return &plugins.Info{
		ID: "signal", Name: "Signal", Kind: "source",
		Services:    []string{Service},
		ServiceInfo: sourcekit.Looks(Service),
		NameWeights: []plugins.Weight{{Key: "signal/book", Weight: 80}, {Key: "signal/profile", Weight: 30}},
		Description: "Signal as it arrives, through a helper linked to the account as a device (like Signal " +
			"Desktop): what arrives from the moment it is linked, not the history before it.",
		Modes:       []string{"import", "live"},
		LiveDefault: true,
		Needs:       []string{"the Signal helper (everysaid-signal)", "a link from the phone (a QR code)"},
		Settings: []plugins.Setting{
			{Key: "helper", Label: "The Signal helper (everysaid-signal)", Type: "path",
				Help: "Empty: the one next to Everysaid, else the one on the PATH"},
			{Key: "device_name", Label: "This device's name on the phone", Default: "Everysaid"},
			{Key: "media", Label: "Download pictures, videos and files", Type: "bool", Default: true},
			{Key: "read_receipts", Label: "Send read receipts", Type: "bool", Default: false,
				Help: "When a chat is opened here, the others see it read (it is marked read on the phone in any case)"},
		},
		CanSend: true, CanReply: true, CanMention: true, CanMarkRead: true, CanSendFiles: true,
		// any emoji, Signal's own quick ones first; edits and deletions for everyone within the day
		// Signal's apps allow (global.normalDeleteMaxAgeInSeconds, a day by default, for both)
		CanReact: true, Reactions: []string{"❤️", "👍", "👎", "😂", "😮", "😢"}, FreeReactions: true,
		CanEdit: true, CanDelete: true, EditWindow: 24 * time.Hour, DeleteWindow: 24 * time.Hour,
		Actions: []plugins.Action{{ID: "link", Label: "Link this computer (QR code)"},
			{ID: "sync", Label: "Ask the phone for its contacts"},
			{ID: "unlink", Label: "Unlink this computer", Confirm: "Unlink this computer from Signal? The link goes, and " +
				"so does whatever waits unread on Signal's server for this computer; the archive keeps everything " +
				"already brought in. If the phone still lists this computer, remove it there too."}},
	}
}

// StoreDir holds the helper's state (the device's keys): <data>/signal/<instance>.
func StoreDir(c *plugins.Context) string {
	return filepath.Join(config.Data, "signal", fmt.Sprint(c.ID))
}

// CacheDir holds signal.db and the files: <cache>/signal/<instance>.
func CacheDir(c *plugins.Context) string {
	return filepath.Join(config.Cache, "signal", fmt.Sprint(c.ID))
}

func dbPath(c *plugins.Context) string   { return filepath.Join(CacheDir(c), "signal.db") }
func mediaDir(c *plugins.Context) string { return filepath.Join(CacheDir(c), "media") }

// passphrase is the helper's store's: made the first time, kept in the keyring (never shown). A
// store already made is never given a new one (the keyring locked, or cleared): it would be lost.
func passphrase(c *plugins.Context) (string, error) {
	p, err := config.Secret(c.SecretName("passphrase"))
	if err != nil {
		return "", err
	}
	if p != "" {
		return p, nil
	}
	if _, err := os.Stat(filepath.Join(StoreDir(c), "presage.db")); err == nil {
		return "", errs.Plugin("The Signal helper's store cannot be opened with the passphrase in the keyring", 0)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	p = hex.EncodeToString(b)
	if _, err := c.SaveSecret("passphrase", p); err != nil {
		return "", err
	}
	return p, nil
}

func (p Plugin) Check(c *plugins.Context) (bool, string) {
	if FindHelper(c.Str("helper")) == "" {
		return false, "missing: the Signal helper (everysaid-signal)"
	}
	return true, "ready"
}

func (p Plugin) InfoFacts(c *plugins.Context) []plugins.Fact {
	k := instanceOf(c.ID)
	k.mu.Lock()
	status, url, phase := k.status, k.linkURL, k.phase
	k.mu.Unlock()
	aci, _ := c.State["aci"].(string)
	connection := "not connected to Signal"
	switch {
	case removed(c):
		connection = "removed from the phone's linked devices: Unlink this computer, then link it again"
	case url != "":
		connection = "waiting for the phone to scan the code"
	case aci == "" && !status.Linked:
		connection = "not linked yet (Link this computer)"
	case phase == "syncing":
		connection = "linked: bringing what waited on Signal's server"
	case phase == "ready":
		connection = "connected to Signal"
	case longUnconnected(c):
		connection = staleText
	}
	facts := []plugins.Fact{{Label: "Connection", Value: connection}}
	if url != "" {
		facts = append(facts, plugins.Fact{Label: "Link code", Value: url})
	}
	if phone, _ := c.State["phone"].(string); phone != "" {
		facts = append(facts, plugins.Fact{Label: "Account", Value: phone})
	}
	if contacts, groups, ok := counts(dbPath(c)); ok && aci != "" {
		facts = append(facts, plugins.Fact{Label: "Contacts", Value: fmt.Sprint(contacts)},
			plugins.Fact{Label: "Groups", Value: fmt.Sprint(groups)})
	}
	return facts
}

func (p Plugin) IdleActions(c *plugins.Context) []string {
	if aci, _ := c.State["aci"].(string); aci != "" {
		return []string{"link"}
	}
	return []string{"sync", "unlink"}
}

// instance is what runs for a plugin instance: at most one helper at a time.
type instance struct {
	run sync.Mutex // held by whoever runs the helper (the live connection, or one piece of work)

	mu        sync.Mutex
	live      *conn  // the live connection's, while it runs
	status    Status // the helper's last word on its account
	linkURL   string // a link waiting for the phone to scan it
	linking   bool   // a link asked of the helper, not yet answered
	unlinking bool   // "Unlink this computer" under way: no helper is started
	phase     string // "", linking, syncing (receiving what waited on the server), ready
	linked    chan struct{}
}

// setPhase moves the instance on, said to the user's devices (the card shows it).
func (k *instance) setPhase(c *plugins.Context, phase string) {
	k.mu.Lock()
	changed := k.phase != phase
	k.phase = phase
	k.mu.Unlock()
	if changed {
		c.Emit(M{"type": "changed"})
	}
}

// counts are how many contacts and groups signal.db has (false: none yet).
func counts(path string) (int64, int64, bool) {
	if _, err := os.Stat(path); err != nil {
		return 0, 0, false
	}
	d, err := db.ReadOnly(path)
	if err != nil {
		return 0, 0, false
	}
	defer d.Close()
	var contacts, groups int64
	if d.QueryRow("SELECT (SELECT count(*) FROM contact), (SELECT count(*) FROM grp)").Scan(&contacts, &groups) != nil {
		return 0, 0, false
	}
	return contacts, groups, true
}

var (
	instMu    sync.Mutex
	instances = map[int64]*instance{}
)

func instanceOf(id int64) *instance {
	instMu.Lock()
	defer instMu.Unlock()
	k := instances[id]
	if k == nil {
		k = &instance{linked: make(chan struct{}, 1)}
		instances[id] = k
	}
	return k
}

// conn is a running helper and what it feeds: signal.db, and the import after it.
type conn struct {
	h     *Helper
	c     *plugins.Context
	k     *instance
	store *Store
	own   string

	synced     bool          // the phone's contacts came once (kept in the instance's state)
	syncAsked  bool          // the phone was asked for its contacts (once, if they did not come by themselves)
	dirty      chan struct{} // something new for the archive
	queueEmpty chan struct{} // what waited on the server has all come
	ended      chan string   // the receiving ended (why)
}

// open starts the helper, opens its store, and signal.db.
func open(c *plugins.Context, k *instance) (*conn, error) {
	path := FindHelper(c.Str("helper"))
	if path == "" {
		return nil, errs.Plugin("The Signal helper (everysaid-signal) is not installed: put it next to Everysaid or on the PATH", 0)
	}
	pass, err := passphrase(c)
	if err != nil {
		return nil, err
	}
	st, err := OpenStore(dbPath(c))
	if err != nil {
		return nil, err
	}
	n := &conn{c: c, k: k, store: st, dirty: make(chan struct{}, 1), queueEmpty: make(chan struct{}, 1),
		ended: make(chan string, 1)}
	n.synced, _ = c.State["contacts_synced"].(bool)
	n.h, err = StartHelper(path, n.event, func(line string) { c.Logf("[signal] %s", line) })
	if err != nil {
		st.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	raw, err := n.h.Call(ctx, "open", map[string]any{"store": StoreDir(c), "attachments": mediaDir(c), "passphrase": pass})
	if err != nil {
		n.close()
		var he *HelperError
		if errors.As(err, &he) && he.Code == "locked" {
			return nil, errs.Plugin("The Signal helper's store cannot be opened with the passphrase in the keyring", 0)
		}
		return nil, err
	}
	if err := n.setStatus(raw); err != nil {
		n.close()
		return nil, err
	}
	return n, nil
}

func (n *conn) close() {
	n.k.setPhase(n.c, "")
	n.h.Close()
	n.store.Close()
}

// setStatus keeps the helper's word on its account: in the instance, signal.db and its state.
func (n *conn) setStatus(raw json.RawMessage) error {
	st, err := decodeStatus(raw)
	if err != nil {
		return err
	}
	n.k.mu.Lock()
	n.k.status = st
	n.k.mu.Unlock()
	if st.Linked {
		n.own = st.ACI
		n.store.Account(st)
		if was, _ := n.c.State["aci"].(string); was != st.ACI {
			n.c.SaveState(M{"aci": st.ACI, "phone": st.Phone})
		}
	}
	return nil
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// event is one of the helper's events, as it comes.
func (n *conn) event(name string, raw []byte) {
	switch name {
	case "link_url":
		var e struct {
			URL string `json:"url"`
		}
		json.Unmarshal(raw, &e)
		n.k.mu.Lock()
		n.k.linkURL = e.URL
		n.k.mu.Unlock()
		n.k.setPhase(n.c, "linking")
		n.c.Log("Scan this code in Signal on the phone (Settings, Linked devices):", nil)
		n.c.Logf("%s", e.URL)
		n.c.Emit(M{"type": "plugin_qr", "instance": n.c.ID, "code": e.URL})
		n.c.Emit(M{"type": "changed"})
	case "queue_empty":
		n.k.mu.Lock()
		first := n.k.phase != "ready"
		n.k.mu.Unlock()
		if first {
			n.c.Log("Up to date with Signal", nil)
			n.k.setPhase(n.c, "ready")
			n.c.SaveState(M{"connected_at": time.Now().Unix()})
		}
		// the phone sends its contacts by itself once linked; where they did not come, asked once
		// (after the queue: Signal's apps send nothing before they have read what waited)
		if !n.synced && !n.syncAsked {
			n.syncAsked = true
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if _, err := n.h.Call(ctx, "sync", nil); err == nil {
				n.c.Log("Asked the phone for its contacts", nil)
			}
			cancel()
		}
		if n.c.Bool("media") {
			n.fetchAgain()
		}
		signal(n.queueEmpty)
		signal(n.dirty)
	case "contacts":
		kept, err := n.store.Apply(raw)
		if err != nil {
			n.c.Log("error: {e}", map[string]any{"e": err})
			return
		}
		var e struct {
			Contacts []any `json:"contacts"`
		}
		json.Unmarshal(raw, &e)
		n.c.Log("Contacts from the phone: {n}", map[string]any{"n": len(e.Contacts)})
		if !n.synced {
			n.synced = true
			n.c.SaveState(M{"contacts_synced": true})
		}
		if kept {
			signal(n.dirty)
		}
	case "receive_ended":
		var e struct {
			Error *string `json:"error"`
			Code  string  `json:"code"`
		}
		json.Unmarshal(raw, &e)
		switch e.Code {
		case "unlinked":
			n.c.Log(removedText, nil)
			n.c.SaveState(M{"unlinked": true})
			n.k.setPhase(n.c, "")
		case "outdated":
			n.c.Log(outdatedText, nil)
		case "stopped": // the helper is being stopped (an unlink, the end): no failure
			return
		}
		select {
		case n.ended <- deref(e.Error):
		default:
		}
	case "decryption_error": // the other side could not decrypt one of the owner's messages
		var e struct {
			Sender string `json:"sender"`
		}
		json.Unmarshal(raw, &e)
		n.c.Log("{who} could not read a message sent from here", map[string]any{"who": e.Sender})
	case "number_changed":
		n.c.Log("Signal's number of this account changed since this computer was linked: what is sent to the new number does not come here (the phone has it); link this computer again (“Unlink this computer”, then link it) for what comes from then on", nil)
	case "unreadable":
		var e struct {
			Sender string `json:"sender"`
			TS     int64  `json:"ts"`
			Retry  bool   `json:"retry"`
		}
		json.Unmarshal(raw, &e)
		if e.Retry {
			n.c.Log("a message from {who} could not be read; they were asked to send it again", map[string]any{"who": e.Sender})
		} else {
			n.c.Log("a message from {who} could not be read", map[string]any{"who": e.Sender})
		}
		if _, err := n.store.Apply(raw); err != nil {
			n.c.Log("error: {e}", map[string]any{"e": err})
		}
	default:
		kept, err := n.store.Apply(raw)
		if err != nil {
			n.c.Log("error: {e}", map[string]any{"e": err})
			return
		}
		if kept {
			signal(n.dirty)
		}
	}
}

// staleText says that Signal will soon remove this computer for not connecting.
const staleText = "not connected for over 20 days: Signal removes a linked device after about 30 days without connecting"

// longUnconnected is whether a linked computer last reached Signal over 20 days ago (Signal removes
// a linked device after about 30 days without connecting).
func longUnconnected(c *plugins.Context) bool {
	var at int64
	switch v := c.State["connected_at"].(type) { // as saved now, or as read back (JSON)
	case int64:
		at = v
	case float64:
		at = int64(v)
	default:
		return false
	}
	return time.Since(time.Unix(at, 0)) > 20*24*time.Hour
}

// healthyRun is how long a connection ran for its end not to count as a failure.
const healthyRun = 10 * time.Minute

// outdatedText says that Signal's server refuses this version of the helper.
const outdatedText = "Signal no longer accepts this version: Everysaid needs an update to connect to Signal again"

// removedText says that the phone removed this computer from the account's linked devices, and the
// way out.
const removedText = "This computer was removed from Signal's linked devices on the phone: use “Unlink this computer”, then link it again"

// removed: Signal's server refused this device (it was removed from the phone); nothing connects
// again until it is unlinked here.
func removed(c *plugins.Context) bool {
	r, _ := c.State["unlinked"].(bool)
	return r
}

// fetchAgain asks the helper again for the files that failed to come (each a few times, while
// Signal's server may still have them); they come as their messages again.
func (n *conn) fetchAgain() {
	refs := n.store.FailedFiles(time.Now().Add(-fetchDays * 24 * time.Hour).UnixMilli())
	if len(refs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := n.h.Call(ctx, "fetch", map[string]any{"messages": refs}); err == nil {
		n.c.Log("Files that had not come, asked again: {n}", map[string]any{"n": len(refs)})
	}
}

// helperFor is the helper for a piece of work: the live connection's while it runs, else one
// started for it (done stops it). It waits for another piece of work to end, until ctx does.
func helperFor(ctx context.Context, c *plugins.Context) (*conn, func(), error) {
	k := instanceOf(c.ID)
	for {
		k.mu.Lock()
		live, unlinking := k.live, k.unlinking
		k.mu.Unlock()
		if live != nil && !unlinking {
			return live, func() {}, nil
		}
		if !unlinking && k.run.TryLock() {
			break
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	n, err := open(c, k)
	if err != nil {
		k.run.Unlock()
		return nil, nil, err
	}
	return n, func() { n.close(); k.run.Unlock() }, nil
}

func skipChats(c *plugins.Context) map[string]bool {
	skip := map[string]bool{}
	if list, ok := c.Settings["skip_chats"].([]any); ok {
		for _, x := range list {
			skip[fmt.Sprint(x)] = true
		}
	}
	return skip
}

// importNow brings signal.db into the archive, as the other sources' imports do; changes to
// messages already shown are said too.
func importNow(c *plugins.Context) error {
	var n Counts
	_, _, err := sourcekit.RunImporters(c, []sourcekit.Step{{Label: "Signal", Run: func(a *archive.Archive, out func(string)) (err error) {
		n, err = Import(a, dbPath(c), mediaDir(c), c.ID, skipChats(c))
		return err
	}}})
	if err == nil && n.Changes > 0 { // edits, deletions, reactions on messages already shown
		c.Emit(M{"type": "changed"})
	}
	return err
}

// receive starts receiving: first what waited on Signal's server (the phase "syncing", until the
// queue is empty), then what arrives.
func (n *conn) receive(ctx context.Context) error {
	n.k.setPhase(n.c, "syncing")
	n.c.Log("Bringing what waited on Signal's server", nil)
	_, err := n.h.Call(ctx, "receive", map[string]any{"download": n.c.Bool("media")})
	return notLinked(err)
}

// drain waits for what waited on the server (or for the receiving to end, or ctx), then for it to
// be in signal.db.
func (n *conn) drain(ctx context.Context) {
	select {
	case <-n.queueEmpty:
	case why := <-n.ended:
		if why != "" {
			n.c.Log("error: {e}", map[string]any{"e": why})
		}
	case <-ctx.Done():
	}
	n.h.Settle()
}

// RunImport brings what waited on Signal's server (where the live connection does not run) and
// what signal.db has into the archive.
func (p Plugin) RunImport(c *plugins.Context) error {
	if removed(c) {
		return errs.Plugin(removedText, 0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	n, done, err := helperFor(ctx, c)
	if err != nil {
		return err
	}
	k := instanceOf(c.ID)
	k.mu.Lock()
	live := k.live == n
	linked := k.status.Linked
	k.mu.Unlock()
	if !live {
		if !linked {
			done()
			return errs.Plugin("Not linked to Signal yet: use “Link this computer” and scan the code with the phone", 0)
		}
		if err := n.receive(ctx); err != nil {
			done()
			return err
		}
		n.drain(ctx) // what waited on the server, then the import
	}
	done()
	return importNow(c)
}

// Live receives as messages arrive, keeping them in signal.db and importing them a moment later.
// Not linked, it waits for “Link this computer” (which then goes through it).
func (p Plugin) Live(ctx context.Context, c *plugins.Context) error {
	k := instanceOf(c.ID)
	for k.isUnlinking() || !k.run.TryLock() { // a piece of work under way ends first
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer k.run.Unlock()
	n, err := open(c, k)
	if err != nil {
		return err
	}
	defer n.close()
	k.mu.Lock()
	k.live = n
	linked := k.status.Linked
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		k.live = nil
		k.mu.Unlock()
	}()
	if linked && removed(c) { // nothing connects: it waits for "Unlink this computer"
		c.Log(removedText, nil)
		select {
		case <-n.h.Done():
			return nil
		case <-ctx.Done():
			return nil
		}
	}
	if !linked {
		c.Log("Not linked to Signal yet: use “Link this computer” and scan the code with the phone", nil)
		select {
		case <-k.linked:
		case <-n.h.Done():
			return ErrStopped
		case <-ctx.Done():
			return nil
		}
	}
	c.Log("connected to Signal", nil)
	// what this device received while signal.db did not keep it (the app stopped half way)
	hctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	since := n.store.Newest() - int64(24*time.Hour/time.Millisecond)
	if raw, err := n.h.Call(hctx, "history", map[string]any{"since": max(since, 0), "chats": n.store.PNIs()}); err == nil {
		var h struct {
			Events []json.RawMessage `json:"events"`
		}
		json.Unmarshal(raw, &h)
		for _, e := range h.Events {
			n.store.Apply(e)
		}
	}
	cancel()
	if err := n.receive(ctx); err != nil {
		return err
	}
	started := time.Now() // connected
	if err := importNow(c); err != nil {
		c.Log("error: {e}", map[string]any{"e": err})
	}
	var later <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-n.h.Done():
			if k.isUnlinking() {
				return nil
			}
			return ErrStopped
		case why := <-n.ended:
			if removed(c) {
				return nil // again, to wait for "Unlink this computer"
			}
			if why == "" {
				why = "the connection to Signal ended"
			}
			// after a good while connected, an end is no failure: the host connects again soon,
			// not after a pause doubled by every end before
			if time.Since(started) > healthyRun {
				c.Log("live: {e}; connecting again", map[string]any{"e": why})
				return nil
			}
			return errs.Plugin(why, 0)
		case <-n.dirty:
			if later == nil { // a moment for what comes together (a message and its group, a burst)
				later = time.After(time.Second)
			}
		case <-later:
			later = nil
			if err := importNow(c); err != nil {
				c.Log("error: {e}", map[string]any{"e": err})
			}
		}
	}
}

// Action: "link" links this computer to the account (a code for the phone to scan; through the
// live connection where it runs); "sync" asks the phone for its contacts again.
func (p Plugin) Action(c *plugins.Context, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if name == "unlink" {
		return unlink(ctx, c)
	}
	n, done, err := helperFor(ctx, c)
	if err != nil {
		return err
	}
	switch name {
	case "link":
		// one at a time: a second would start over on the store the first is linking (the live one)
		n.k.mu.Lock()
		busy := n.k.linking
		n.k.linking = true
		n.k.mu.Unlock()
		if busy {
			done()
			return errs.Plugin("A code is already waiting for the phone", 0)
		}
		// the code is for a few minutes: then it is said so, and the user asks for another
		lctx, lcancel := context.WithTimeout(ctx, linkWait)
		raw, err := n.h.Call(lctx, "link", map[string]any{"device_name": c.Str("device_name")})
		lcancel()
		n.k.mu.Lock()
		n.k.linkURL, n.k.linking = "", false
		n.k.mu.Unlock()
		c.Emit(M{"type": "changed"})
		var he *HelperError
		switch {
		case errors.As(err, &he) && he.Code == "already_linked":
			done()
			if removed(c) {
				return errs.Plugin(removedText, 0)
			}
			return errs.Plugin("Already linked to Signal", 0)
		case errors.Is(err, context.DeadlineExceeded):
			n.k.setPhase(c, "")
			n.k.mu.Lock()
			live := n.k.live == n
			n.k.mu.Unlock()
			done()
			if live { // the live helper is still waiting for the scan: it starts again
				n.h.Kill()
			}
			return errs.Plugin("No code was scanned in time", 0)
		case err != nil:
			n.k.setPhase(c, "")
			done()
			if errors.As(err, &he) || errors.Is(err, ErrStopped) { // Signal's own words, for the log
				c.Log("error: {e}", map[string]any{"e": err})
				return errs.Plugin("Linking to Signal failed: try “Link this computer” again", 0)
			}
			return err
		}
		if err := n.setStatus(raw); err != nil {
			done()
			return err
		}
		c.Log("Linked to Signal as {phone}", map[string]any{"phone": n.k.status.Phone})
		n.k.mu.Lock()
		live := n.k.live == n
		n.k.mu.Unlock()
		if live { // the live connection goes on by itself: the first sync, then what arrives
			signal(n.k.linked)
			done()
			return nil
		}
		// no live connection: the first sync (contacts, groups, what waited) now, then the import
		if err := n.receive(ctx); err == nil {
			n.drain(ctx)
		}
		done()
		return importNow(c)
	case "sync":
		defer done()
		if _, err := n.h.Call(ctx, "sync", nil); err != nil {
			return notLinked(err)
		}
		c.Log("Asked the phone for its contacts", nil)
		return nil
	}
	done()
	return errs.Plugin("unknown action", 0)
}

// unlink takes this computer off the account, as the user chose (the UI asked first): the helper
// stops, everything signal.db has goes into the archive, then the helper's store (the device's keys,
// what waits) and signal.db are removed; "Link this computer" works again. The files fetched stay.
func unlink(ctx context.Context, c *plugins.Context) error {
	k := instanceOf(c.ID)
	k.mu.Lock()
	if k.unlinking {
		k.mu.Unlock()
		return nil
	}
	k.unlinking = true
	live := k.live
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		k.unlinking = false
		k.mu.Unlock()
		c.Emit(M{"type": "changed"})
	}()
	if live != nil {
		live.h.Close()
	}
	for !k.run.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer k.run.Unlock()
	if err := importNow(c); err != nil {
		return err
	}
	store := StoreDir(c)
	if config.Data == "" || filepath.Base(store) != fmt.Sprint(c.ID) || filepath.Base(filepath.Dir(store)) != "signal" {
		return fmt.Errorf("not the helper's store: %s", store)
	}
	if err := os.RemoveAll(store); err != nil {
		return err
	}
	for _, f := range []string{dbPath(c), dbPath(c) + "-wal", dbPath(c) + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	c.SaveState(M{"aci": "", "phone": "", "contacts_synced": false, "unlinked": false, "connected_at": nil})
	k.mu.Lock()
	k.status, k.linkURL = Status{}, ""
	k.mu.Unlock()
	k.setPhase(c, "")
	c.Log("Unlinked from Signal: if the phone still lists this computer, remove it there (Settings, Linked devices)", nil)
	return nil
}

func (k *instance) isUnlinking() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.unlinking
}

// linkWait is how long a link code waits for the phone.
var linkWait = 5 * time.Minute

// notLinked says the helper's not_linked in the user's words.
func notLinked(err error) error {
	var he *HelperError
	if errors.As(err, &he) && he.Code == "not_linked" {
		return errs.Plugin("Not linked to Signal yet: use “Link this computer” and scan the code with the phone", 0)
	}
	return err
}

// Chats are the chats signal.db has, with the user's choice for each.
func (p Plugin) Chats(c *plugins.Context) ([]M, error) {
	list, err := ChatList(dbPath(c), skipChats(c))
	if err != nil {
		return nil, err
	}
	out := make([]M, len(list))
	for i, x := range list {
		out[i] = x
	}
	return out, nil
}

// isGroupKey: a person's chat is keyed by their ACI (a UUID, or "PNI:<uuid>"), a group's by its id.
func isGroupKey(key string) bool {
	k := strings.TrimPrefix(key, "PNI:")
	return !(len(k) == 36 && strings.Count(k, "-") == 4)
}

// aciOf is a person's Signal ACI, from any of their addresses: the address itself where it is one,
// else one that is a member of the conversation.
func aciOf(c *plugins.Context, addressID, conversationID int64) (string, error) {
	var v string
	if !db.Row(c.Store().Read(), "SELECT a.value FROM person_address mine JOIN person_address theirs ON theirs.person_id = mine.person_id "+
		"JOIN address a ON a.id = theirs.address_id JOIN address_kind k ON k.id = a.kind_id "+
		"JOIN service s ON s.id = a.service_id WHERE mine.address_id = ? AND k.name = 'id' AND s.name = 'signal' "+
		"ORDER BY a.id = mine.address_id DESC, EXISTS (SELECT 1 FROM conversation_member cm WHERE "+
		"cm.conversation_id = ? AND cm.address_id = a.id) DESC, a.id LIMIT 1", []any{addressID, conversationID}, &v) {
		return "", errs.Plugin("Unknown person to mention", 0)
	}
	return v, nil
}

type outMention struct {
	Start  int    `json:"start"`
	Length int    `json:"length"`
	ACI    string `json:"aci"`
}

// mentionsOut is the text with each mention ({start, length} in characters, e.g. "@name") written
// as Signal's apps do, U+FFFC where the name was, and where each is in UTF-16 units.
func mentionsOut(c *plugins.Context, text string, ms []plugins.Mention, conversationID int64) (string, []outMention, error) {
	runes := []rune(text)
	sorted := append([]plugins.Mention(nil), ms...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	var out []rune
	var placed []outMention
	at := 0
	for _, m := range sorted {
		start, end := min(max(m.Start, at), len(runes)), min(max(m.Start+m.Length, m.Start), len(runes))
		aci, err := aciOf(c, m.AddressID, conversationID)
		if err != nil {
			return "", nil, err
		}
		out = append(out, runes[at:start]...)
		placed = append(placed, outMention{Start: len(utf16.Encode(out)), Length: 1, ACI: aci})
		out = append(out, '\uFFFC')
		at = max(end, start)
	}
	out = append(out, runes[at:]...)
	return string(out), placed, nil
}

// Send sends into a conversation (a reply quoting a message, mentions, a file with the text as its
// caption); what was sent comes into the archive as Signal's other messages do.
func (p Plugin) Send(ctx context.Context, c *plugins.Context, conv plugins.Conversation, text string,
	reply *plugins.Reply, mentions []plugins.Mention, file *plugins.File) (any, error) {
	if removed(c) {
		return nil, errs.Plugin(removedText, 0)
	}
	n, done, err := helperFor(ctx, c)
	if err != nil {
		return nil, err
	}
	defer done()
	kind := "contact"
	if isGroupKey(conv.Key) {
		kind = "group"
	}
	req := map[string]any{"chat": M{"kind": kind, "id": conv.Key}, "text": text}
	if len(mentions) > 0 {
		t, placed, err := mentionsOut(c, text, mentions, conv.ID)
		if err != nil {
			return nil, err
		}
		req["text"], req["mentions"] = t, placed
	}
	if reply != nil {
		author, ts, ok := SplitKey(reply.Key)
		if !ok {
			return nil, errs.Plugin("This message cannot be answered", 0)
		}
		q := M{"ts": ts, "author": author}
		var t *string
		if db.Row(c.Store().Read(), "SELECT text FROM message WHERE id = ?", []any{reply.ID}, &t) && t != nil {
			q["text"] = *t
		}
		req["quote"] = q
	}
	if file != nil {
		tmp, err := os.CreateTemp(CacheDir(c), "send-*-"+filepath.Base(file.Filename))
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmp.Name())
		_, err = tmp.Write(file.Data)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		a := M{"path": tmp.Name()}
		if file.Filename != "" {
			a["filename"] = file.Filename
		}
		if file.MimeType != "" {
			a["content_type"] = file.MimeType
		}
		req["attachments"] = []M{a}
	}
	raw, err := n.h.Call(ctx, "send", req)
	if err != nil {
		var he *HelperError
		if errors.As(err, &he) && he.Code == "failed" {
			return nil, errs.Plugin("Sending failed", 0)
		}
		return nil, notLinked(err)
	}
	n.h.Settle() // the message it sent is in signal.db
	if err := importNow(c); err != nil {
		return nil, err
	}
	var answer struct {
		TS int64 `json:"ts"`
	}
	json.Unmarshal(raw, &answer)
	return plugins.Sent{Keys: []string{Key(n.own, answer.TS)}}, nil
}

// MarkRead marks the others' messages of the conversation up to `until` (Unix ms) read, as Signal
// Desktop does: on the owner's phone always (it tells no one else), with read receipts to their
// authors where the user turned them on; it returns how many were marked.
func (p Plugin) MarkRead(ctx context.Context, c *plugins.Context, conv plugins.Conversation, until int64) (int, error) {
	k := instanceOf(c.ID)
	k.mu.Lock()
	n := k.live
	k.mu.Unlock()
	if n == nil {
		return 0, nil // only through the live connection
	}
	unread := n.store.Unread(conv.Key, until)
	if len(unread) == 0 {
		return 0, nil
	}
	raw, err := n.h.Call(ctx, "mark_read", map[string]any{"messages": unread, "receipts": c.Bool("read_receipts")})
	if err != nil {
		return 0, notLinked(err)
	}
	n.store.Read(unread, time.Now().UnixMilli())
	if err := importNow(c); err != nil {
		return 0, err
	}
	var answer struct {
		Marked int `json:"marked"`
	}
	json.Unmarshal(raw, &answer)
	return answer.Marked, nil
}

// maxEdits is how many times Signal's apps let a message be edited (but in the notes to oneself).
const maxEdits = 10

// act sends what the user did to a message (a reaction, an edit, a deletion) through the helper,
// which says it back as Signal's events: kept in signal.db, then brought into the archive as the
// same change from the phone would be. ask gets the helper (its signal.db, its account) and the
// message's author and times, and says the request, or why there is none (nil and no error: nothing
// to do).
func act(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, cmd string,
	ask func(n *conn, author string, times []int64) (M, error)) error {
	if removed(c) {
		return errs.Plugin(removedText, 0)
	}
	author, ts, ok := SplitKey(msg.Key)
	if !ok {
		return errs.Plugin("Signal cannot find this message", 0)
	}
	n, done, err := helperFor(ctx, c)
	if err != nil {
		return err
	}
	defer done()
	req, err := ask(n, author, n.store.Revisions(author, ts))
	if err != nil || req == nil {
		return err
	}
	kind := "contact"
	if isGroupKey(conv.Key) {
		kind = "group"
	}
	req["chat"] = M{"kind": kind, "id": conv.Key}
	if _, err := n.h.Call(ctx, cmd, req); err != nil {
		var he *HelperError
		if errors.As(err, &he) && he.Code == "failed" {
			return errs.Plugin("Sending failed", 0)
		}
		return notLinked(err)
	}
	n.h.Settle() // what it sent is in signal.db
	return importNow(c)
}

// React puts the user's reaction on a message, in place of the one there was; "" takes it back,
// which Signal wants said with the emoji that was there. Like Signal's apps, it is aimed at the
// message's newest version.
func (p Plugin) React(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, emoji string) error {
	return act(ctx, c, conv, msg, "react", func(n *conn, author string, times []int64) (M, error) {
		target := M{"author": author, "ts": times[len(times)-1]}
		if emoji != "" {
			return M{"target": target, "emoji": emoji}, nil
		}
		was, known := n.store.MyReaction(n.own, author, times)
		if !known { // put before signal.db kept reactions: the archive has it
			var e *string
			db.Row(c.Store().Read(), "SELECT emoji FROM reaction WHERE message_id = ? AND outgoing = 1", []any{msg.ID}, &e)
			was = deref(e)
		}
		if was == "" {
			return nil, nil // none there
		}
		return M{"target": target, "emoji": was, "remove": true}, nil
	})
}

// Edit gives the user's own message a new text (a file's caption), as Signal's apps allow: not a
// sticker, a voice note, a shared contact, a poll or a view-once message, not one deleted, and so
// many times.
func (p Plugin) Edit(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, text string) error {
	return act(ctx, c, conv, msg, "edit", func(n *conn, author string, times []int64) (M, error) {
		if e, ok := n.store.Message(author, times[0]); ok {
			for _, a := range e.Attachments {
				if a.Sticker || a.Voice {
					return nil, errs.Plugin("Signal does not let this message be edited", 0)
				}
			}
			if len(e.Contacts) > 0 || e.Poll != nil || e.ViewOnce {
				return nil, errs.Plugin("Signal does not let this message be edited", 0)
			}
		}
		if n.store.Deleted(author, times) {
			return nil, errs.Plugin("Signal does not let this message be edited", 0)
		}
		if len(times)-1 >= maxEdits && conv.Key != n.own {
			return nil, errs.Plugin("Signal lets a message be edited only 10 times", 0)
		}
		return M{"target_ts": times[len(times)-1], "original_ts": times[0], "text": text}, nil
	})
}

// Delete deletes the user's own message for everyone in the conversation.
func (p Plugin) Delete(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref) error {
	return act(ctx, c, conv, msg, "delete", func(n *conn, author string, times []int64) (M, error) {
		if n.store.Deleted(author, times) {
			return nil, nil // already
		}
		return M{"target_ts": times[len(times)-1]}, nil
	})
}
