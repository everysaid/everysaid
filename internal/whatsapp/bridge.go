package whatsapp

// Ports bridges/whatsapp/main.go (main: the client, its events, pairing, the REST API) as a value
// the plugin runs while it is live: Run connects and keeps the connection until its context ends;
// what the REST API offered (send, read, download, status, unblock) are its methods.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/config"
)

// Options are what the bridge's flags were: sending allowed (-send), its limits, and downloading the
// files of messages as they arrive (-download).
type Options struct {
	Send     bool
	Limits   SendLimits
	Download bool
}

// ConfigOptions are the options as config.toml sets them ([whatsapp] send, download), with the
// bridge's defaults; the limits are the source's settings (limits). Sending stays
// off unless the owner turns it on there, as the bridge needed -send: the plugin's own setting is a
// second key, not the only one.
func ConfigOptions() Options {
	return Options{
		Send:     config.Bool("whatsapp", "send", false),
		Download: config.Bool("whatsapp", "download", true),
	}
}

// Hooks are how a running bridge speaks: a QR code to scan (its text, and the code drawn as text),
// the connection's events, and its warnings.
type Hooks struct {
	QR    func(code, drawn string)
	Event func(event, code, detail string)
	Warn  func(text string)
}

// Bridge is one store folder's connection, while it runs.
type Bridge struct {
	Dir   string
	opts  Options
	hooks Hooks
	log   waLog.Logger

	mu        sync.Mutex
	store     *MessageStore
	client    *whatsmeow.Client
	sender    *Sender
	downloads *Downloads
	pairing   chan chan error // a request to link a device, answered when it is done

	// While an event, a send or a read is handled the stores stay open: ending takes this for
	// writing, after which nothing is written (closed).
	handling sync.RWMutex
	closed   bool
	// history-sync notifications received and not yet handled: whatsmeow acknowledges one to the phone
	// at once and downloads it later, apart from the connection, so it would be lost if the store
	// closed in between
	history     map[string]int
	historyWake chan struct{} // fetchHistory's: a history notification kept
	said        chan any      // what Run acts on: connected, logged out, a temporary ban, a connect failure
}

var (
	runningMu sync.Mutex
	running   = map[string]*Bridge{}
)

func key(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// Running is the bridge running on a store folder in this process, or nil.
func Running(dir string) *Bridge {
	runningMu.Lock()
	defer runningMu.Unlock()
	return running[key(dir)]
}

// ErrRunning: the store folder is already connected in this process.
var ErrRunning = errors.New("this WhatsApp store is already connected")

// New is a bridge on a store folder, not running yet.
func New(dir string, opts Options, hooks Hooks) *Bridge {
	b := &Bridge{Dir: dir, opts: opts, hooks: hooks, pairing: make(chan chan error), history: map[string]int{},
		said: make(chan any, 64)}
	b.log = &logger{b: b}
	return b
}

// logger keeps whatsmeow's warnings and errors (to the plugin's log); what it says at lower levels
// names chats and people, and stays unsaid.
type logger struct {
	b      *Bridge
	module string
}

func (l *logger) say(level, msg string, args ...any) {
	if l.b.hooks.Warn != nil {
		text := fmt.Sprintf(msg, args...)
		if l.module != "" {
			text = l.module + ": " + text
		}
		l.b.hooks.Warn(level + " " + text)
	}
}
func (l *logger) Warnf(msg string, args ...any)  { l.say("warning:", msg, args...) }
func (l *logger) Errorf(msg string, args ...any) { l.say("error:", msg, args...) }
func (l *logger) Infof(string, ...any)           {}
func (l *logger) Debugf(string, ...any)          {}
func (l *logger) Sub(module string) waLog.Logger {
	if l.module != "" {
		module = l.module + "/" + module
	}
	return &logger{b: l.b, module: module}
}

// OpenDevices opens the store folder's whatsapp.db (whatsmeow's device store), made if it is not there.
func OpenDevices(ctx context.Context, dir string, log waLog.Logger) (*sqlstore.Container, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "whatsapp.db")
	c, err := sqlstore.New(ctx, "sqlite", dsn(path), log)
	if err == nil {
		private(dir, path)
	}
	return c, err
}

// private keeps the store folder and a database of it to the user alone: the session's keys and the
// messages are in them (SQLite makes a database, and its journal, as the umask allows, often
// readable by all; a store made by the standalone bridge is so).
func private(dir, path string) {
	os.Chmod(dir, 0700)
	os.Chmod(path, 0600)
}

// Run connects to WhatsApp and stays connected until ctx ends (nil), the device is logged out or a
// temporary ban ends (nil: to be started again), or the connection cannot be made (stay). A store
// without a linked device waits for Pair.
func (b *Bridge) Run(ctx context.Context) error {
	k := key(b.Dir)
	runningMu.Lock()
	if running[k] != nil {
		runningMu.Unlock()
		return ErrRunning
	}
	running[k] = b
	runningMu.Unlock()
	defer func() {
		runningMu.Lock()
		delete(running, k)
		runningMu.Unlock()
	}()

	container, err := OpenDevices(ctx, b.Dir, b.log.Sub("Database"))
	if err != nil {
		return fmt.Errorf("failed to connect to database: %v", err)
	}
	defer container.Close()
	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		if err != sql.ErrNoRows {
			return fmt.Errorf("failed to get device: %v", err)
		}
		deviceStore = container.NewDevice()
	}
	client := whatsmeow.NewClient(deviceStore, b.log.Sub("Client"))
	// as mautrix-whatsapp: a message acknowledged only once stored (and kept decrypted meanwhile, so
	// that a crash does not lose it); one that cannot be read asked of the phone too; the history
	// downloaded by the bridge (history.go)
	client.SynchronousAck = true
	client.EnableDecryptedEventBuffer = true
	client.AutomaticMessageRerequestFromPhone = true
	client.ManualHistorySyncDownload = true
	store, err := OpenMessages(b.Dir)
	if err != nil {
		return err
	}
	defer store.Close()
	store.said = b.hooks.Event
	sender := &Sender{client: client, store: store, logger: b.log, enabled: b.opts.Send, limits: b.opts.Limits}
	b.mu.Lock()
	b.store, b.client, b.sender = store, client, sender
	b.mu.Unlock()

	client.AddEventHandlerWithSuccessStatus(func(evt any) bool { return b.handle(client, store, evt) })
	hctx, stopHistory := context.WithCancel(context.Background())
	b.historyWake = make(chan struct{}, 1)
	go fetchHistory(hctx, client, store, b.historyWake, func(path string, h *events.HistorySync) bool {
		if h == nil {
			b.historyPending(path, -1)
			return false
		}
		return b.handle(client, store, h)
	}, b.log)
	defer func() {
		b.end(client)
		stopHistory()
		b.mu.Lock()
		if b.downloads != nil {
			b.downloads.stop()
		}
		b.store, b.client, b.sender, b.downloads = nil, nil, nil, nil
		b.mu.Unlock()
	}()

	if client.Store.ID == nil {
		if err := b.waitPairing(ctx, client); err != nil || ctx.Err() != nil {
			return err
		}
	} else if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}
	return b.stay(ctx, func() {
		nameLIDChats(client, store, b.log)
		go refreshGroups(client, store, b.log)
		store.setState("send_enabled", "")
		if sender.enabled {
			store.setState("send_enabled", "1")
		}
		if b.opts.Download {
			d := startDownloads(client, store, b.log)
			b.mu.Lock()
			b.downloads = d
			b.mu.Unlock()
		}
	})
}

// stay keeps the connection until ctx ends, doing ready's work once it is first up. whatsmeow
// reconnects by itself after a network's failure; where it does not, Run ends as WhatsApp Desktop
// would act: logged out (the device is gone), it starts again to be linked anew; temporarily banned,
// it waits for the ban to end before connecting again; a connect failure of another kind is tried
// again later (the host's growing pause). Replaced by another client, or too old, it stays
// disconnected until the user starts it again: connecting again would take the session back each time.
func (b *Bridge) stay(ctx context.Context, ready func()) error {
	up := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case evt := <-b.said:
			switch v := evt.(type) {
			case *events.Connected:
				if !up {
					up = true
					ready()
				}
			case *events.LoggedOut:
				return nil
			case *events.TemporaryBan:
				wait := v.Expire
				if wait <= 0 {
					wait = time.Hour
				}
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
				return nil
			case *events.ConnectFailure:
				return fmt.Errorf("connect failure: %s", v.Reason)
			}
		}
	}
}

// handle is the client's event handler: false for what could not be stored (WhatsApp is not told
// it arrived, and gives it again), or when the bridge is ending.
func (b *Bridge) handle(client *whatsmeow.Client, store *MessageStore, evt any) bool {
	b.handling.RLock()
	defer b.handling.RUnlock()
	if b.closed {
		return false
	}
	switch v := evt.(type) {
	case *events.Message:
		if n := v.Message.GetProtocolMessage().GetHistorySyncNotification(); n != nil && v.Info.IsFromMe {
			if err := store.pendHistory(n); err != nil {
				b.log.Errorf("Failed to keep a history notification: %v", err)
				return false
			}
			b.historyPending(n.GetDirectPath(), 1)
			select {
			case b.historyWake <- struct{}{}:
			default:
			}
		}
		if !processMessage(client, store, v, "", b.log) {
			return false
		}
		b.mu.Lock()
		d := b.downloads
		b.mu.Unlock()
		if d != nil {
			d.queue(v.Info.ID, v.Info.Chat.String())
		}
	case *events.HistorySync:
		ok := handleHistorySync(client, store, v, b.log)
		b.historyPending(v.Notification.GetDirectPath(), -1)
		if !ok {
			return false
		}
	case *events.UndecryptableMessage:
		handleUndecryptable(store, v, b.log)
	case *events.MediaRetry:
		mediaRetried(store, v, func(id, chat string) {
			b.mu.Lock()
			d := b.downloads
			b.mu.Unlock()
			if d != nil {
				d.queue(id, chat)
			}
		}, b.log)
	case *events.CallOffer, *events.CallOfferNotice, *events.CallAccept, *events.CallReject, *events.CallTerminate:
		handleCallEvent(client, store, v, b.log)
	case *events.Receipt:
		handleReceipt(store, v, b.log)
	case *events.JoinedGroup, *events.GroupInfo:
		handleGroupEvent(client, store, v, b.log)
	case *events.Picture:
		store.storeGroupPicture(v, b.log)
	case *events.Blocklist:
		handleBlocklist(client, store, v, b.log)
	case *events.Connected, *events.Disconnected, *events.LoggedOut, *events.TemporaryBan,
		*events.ConnectFailure, *events.StreamReplaced, *events.ClientOutdated:
		handleStateEvent(store, v, b.log)
		if _, ok := v.(*events.Connected); ok {
			go refreshBlocklist(client, store, b.log) // not in the event handler: it waits for an answer
			// the history waiting for a connection
			select {
			case b.historyWake <- struct{}{}:
			default:
			}
		}
		select {
		case b.said <- v:
		default:
		}
	}
	return true
}

func (b *Bridge) historyPending(key string, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.history[key] += n; b.history[key] <= 0 {
		delete(b.history, key)
	}
}

func (b *Bridge) historyWaiting() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.history) > 0
}

// How long ending waits for history the phone sent and whatsmeow is still downloading.
var historyWait = time.Minute

// end disconnects, lets the history already acknowledged arrive (for a while), and waits for what
// is being handled; after it nothing is written.
func (b *Bridge) end(client *whatsmeow.Client) {
	client.Disconnect()
	for deadline := time.Now().Add(historyWait); b.historyWaiting() && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
	}
	b.handling.Lock()
	b.closed = true
	b.handling.Unlock()
	client.RemoveEventHandlers()
}

// open holds the stores open while a request is handled: false once the bridge has ended.
func (b *Bridge) open() bool {
	b.handling.RLock()
	if b.closed {
		b.handling.RUnlock()
		return false
	}
	return true
}

// waitPairing waits for a request to link (Pair), then shows QR codes until one is scanned (nil),
// or they run out (the request is told; the next one starts again).
func (b *Bridge) waitPairing(ctx context.Context, client *whatsmeow.Client) error {
	for {
		var answer chan error
		select {
		case <-ctx.Done():
			return nil
		case answer = <-b.pairing:
		}
		err := b.pair(ctx, client)
		answer <- err
		if err == nil {
			return nil
		}
		client.Disconnect()
	}
}

// ErrPairTimeout: no QR code was scanned in time.
var ErrPairTimeout = errors.New("no QR code was scanned in time")

func (b *Bridge) pair(ctx context.Context, client *whatsmeow.Client) error {
	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}
	for evt := range qrChan {
		switch evt.Event {
		case whatsmeow.QRChannelEventCode:
			if b.hooks.QR != nil {
				b.hooks.QR(evt.Code, DrawQR(evt.Code))
			}
		case whatsmeow.QRChannelSuccess.Event:
			return nil
		case whatsmeow.QRChannelEventError:
			return fmt.Errorf("linking failed: %v", evt.Error)
		default: // timeout, or the socket closed
			return ErrPairTimeout
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrPairTimeout
}

// DrawQR is a QR code drawn in text (half blocks), as the bridge printed it in its terminal.
func DrawQR(code string) string {
	var out strings.Builder
	qrterminal.GenerateHalfBlock(code, qrterminal.L, &out)
	return strings.TrimRight(out.String(), "\n")
}

// ErrLinked: the store already has a linked device.
var ErrLinked = errors.New("already linked")

// Pair asks the running bridge to link a device: QR codes go to Hooks.QR until one is scanned
// (nil) or they run out; the connection then goes on as linked.
func (b *Bridge) Pair(ctx context.Context) error {
	if b.Linked() {
		return ErrLinked
	}
	answer := make(chan error, 1)
	select {
	case b.pairing <- answer:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second): // not waiting for one (connecting, or stopped)
		return errors.New("the connection is not waiting to be linked")
	}
	select {
	case err := <-answer:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Linked says whether the running bridge has a linked device.
func (b *Bridge) Linked() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client != nil && b.client.Store.ID != nil
}

// HasDevice says whether a store folder holds a linked device (read only; false when there is none).
func HasDevice(dir string) bool {
	path := filepath.Join(dir, "whatsapp.db")
	if _, err := os.Stat(path); err != nil {
		return false
	}
	d, err := sql.Open("sqlite", readOnly(path))
	if err != nil {
		return false
	}
	defer d.Close()
	var n int
	if d.QueryRow("SELECT count(*) FROM whatsmeow_device").Scan(&n) != nil {
		return false
	}
	return n > 0
}

func readOnly(path string) string {
	return strings.Replace(dsn(path), "?", "?mode=ro&", 1)
}

func (b *Bridge) parts() (*whatsmeow.Client, *MessageStore, *Sender) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client, b.store, b.sender
}

// ErrNotConnected: the bridge is not running (or not yet).
var ErrNotConnected = errors.New("not connected to WhatsApp")

// SetLimits changes the limits of sending, from the next message on.
func (b *Bridge) SetLimits(l SendLimits) {
	if _, _, sender := b.parts(); sender != nil {
		sender.mu.Lock()
		sender.limits = l
		sender.mu.Unlock()
	}
}

// Send sends (POST /api/send): an HTTP status and the answer, as the bridge gave them.
func (b *Bridge) Send(req SendRequest) (int, SendResponse) {
	_, _, sender := b.parts()
	if sender == nil || !b.open() {
		return 503, SendResponse{Message: ErrNotConnected.Error()}
	}
	defer b.handling.RUnlock()
	return sender.send(req)
}

// Find is which of the numbers (+digits) WhatsApp has.
func (b *Bridge) Find(ctx context.Context, phones []string) (map[string]bool, error) {
	client, _, sender := b.parts()
	if sender == nil || !b.open() {
		return nil, ErrNotConnected
	}
	defer b.handling.RUnlock()
	if !client.IsConnected() {
		return nil, ErrNotConnected
	}
	return sender.find(ctx, phones)
}

// MarkRead sends read receipts (POST /api/read).
func (b *Bridge) MarkRead(req ReadRequest) (int, map[string]any) {
	_, _, sender := b.parts()
	if sender == nil || !b.open() {
		return 503, map[string]any{"success": false, "message": ErrNotConnected.Error()}
	}
	defer b.handling.RUnlock()
	req.Recipient = strings.TrimSpace(req.Recipient)
	return sender.markRead(req)
}

// Download is a message's file, downloaded now unless it was before (POST /api/download).
func (b *Bridge) Download(id, chatJID string) (string, error) {
	client, store, _ := b.parts()
	if store == nil || !b.open() {
		return "", ErrNotConnected
	}
	defer b.handling.RUnlock()
	return store.download(client, id, chatJID)
}

// StatusEvent is one of the connection's last events.
type StatusEvent struct {
	At     time.Time `json:"at"`
	Event  string    `json:"event"`
	Code   string    `json:"code,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Status is what /api/status said: the connection, sending, any block, the limits, the counts
// sent, the last events; and whether a device is linked.
func (b *Bridge) Status() map[string]any {
	client, store, sender := b.parts()
	if store == nil {
		return nil
	}
	out := status(store)
	out["connected"] = client.IsConnected()
	out["logged_in"] = client.IsLoggedIn()
	out["linked"] = client.Store.ID != nil
	out["send_enabled"] = sender.enabled
	sender.mu.Lock()
	out["limits"] = sender.limits
	sender.mu.Unlock()
	out["sent"] = sender.counts()
	return out
}

func status(store *MessageStore) map[string]any {
	connection, connectionAt := store.state("connection")
	blocked, blockedAt := store.state("send_blocked")
	banUntil, _ := store.state("ban_until")
	evts := []StatusEvent{}
	if rows, err := store.db.Query("SELECT at, event, code, detail FROM bridge_events ORDER BY rowid DESC LIMIT 20"); err == nil {
		for rows.Next() {
			var e StatusEvent
			var code, detail sql.NullString
			if rows.Scan(&e.At, &e.Event, &code, &detail) == nil {
				e.Code, e.Detail = code.String, detail.String
				evts = append(evts, e)
			}
		}
		rows.Close()
	}
	return map[string]any{"connection": connection, "connection_at": connectionAt, "ban_until": banUntil,
		"send_blocked": blocked, "blocked_at": blockedAt, "events": evts}
}

// Unblock clears the send block (POST /api/unblock): a person's decision, never the code's.
// It works on the store whether the bridge runs or not; it returns the reason cleared.
func Unblock(dir string) (string, error) {
	store := func() *MessageStore {
		if b := Running(dir); b != nil {
			_, s, _ := b.parts()
			return s
		}
		return nil
	}()
	if store == nil {
		s, err := OpenMessages(dir)
		if err != nil {
			return "", err
		}
		defer s.Close()
		store = s
	}
	old, _ := store.state("send_blocked")
	store.setState("send_blocked", "")
	store.logEvent("unblocked", "", old)
	return old, nil
}
