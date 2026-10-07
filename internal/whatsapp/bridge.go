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

// ConfigOptions are the options as config.toml sets them ([whatsapp] send, send_per_minute,
// send_per_hour, send_per_day, send_same_text, download), with the bridge's defaults. Sending stays
// off unless the owner turns it on there, as the bridge needed -send: the plugin's own setting is a
// second key, not the only one.
func ConfigOptions() Options {
	return Options{
		Send: config.Bool("whatsapp", "send", false),
		Limits: SendLimits{
			PerMinute: config.Int("whatsapp", "send_per_minute", 6),
			PerHour:   config.Int("whatsapp", "send_per_hour", 60),
			PerDay:    config.Int("whatsapp", "send_per_day", 300),
			SameText:  config.Int("whatsapp", "send_same_text", 3),
		},
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
	b := &Bridge{Dir: dir, opts: opts, hooks: hooks, pairing: make(chan chan error)}
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
	return sqlstore.New(ctx, "sqlite", dsn(filepath.Join(dir, "whatsapp.db")), log)
}

// Run connects to WhatsApp and stays connected until ctx ends (nil) or the connection cannot be
// made. A store without a linked device waits for Pair.
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

	client.AddEventHandler(func(evt any) {
		switch v := evt.(type) {
		case *events.Message:
			processMessage(client, store, v, "", b.log)
			b.mu.Lock()
			d := b.downloads
			b.mu.Unlock()
			if d != nil {
				d.queue(v.Info.ID, v.Info.Chat.String())
			}
		case *events.HistorySync:
			handleHistorySync(client, store, v, b.log)
		case *events.CallOffer, *events.CallOfferNotice, *events.CallAccept, *events.CallReject, *events.CallTerminate:
			handleCallEvent(client, store, v, b.log)
		case *events.Receipt:
			handleReceipt(store, v, b.log)
		case *events.JoinedGroup, *events.GroupInfo:
			handleGroupEvent(client, store, v, b.log)
		case *events.Connected, *events.Disconnected, *events.LoggedOut, *events.TemporaryBan,
			*events.ConnectFailure, *events.StreamReplaced, *events.ClientOutdated:
			handleStateEvent(store, v, b.log)
		}
	})
	defer func() {
		client.RemoveEventHandlers()
		client.Disconnect()
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

	// a moment for the connection to settle
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(2 * time.Second):
	}
	if !client.IsConnected() {
		return errors.New("failed to establish stable connection")
	}

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
	<-ctx.Done()
	return nil
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

// Send sends (POST /api/send): an HTTP status and the answer, as the bridge gave them.
func (b *Bridge) Send(req SendRequest) (int, SendResponse) {
	_, _, sender := b.parts()
	if sender == nil {
		return 503, SendResponse{Message: ErrNotConnected.Error()}
	}
	return sender.send(req)
}

// MarkRead sends read receipts (POST /api/read).
func (b *Bridge) MarkRead(req ReadRequest) (int, map[string]any) {
	_, _, sender := b.parts()
	if sender == nil {
		return 503, map[string]any{"success": false, "message": ErrNotConnected.Error()}
	}
	req.Recipient = strings.TrimSpace(req.Recipient)
	return sender.markRead(req)
}

// Download is a message's file, downloaded now unless it was before (POST /api/download).
func (b *Bridge) Download(id, chatJID string) (string, error) {
	client, store, _ := b.parts()
	if store == nil {
		return "", ErrNotConnected
	}
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
	out["limits"] = sender.limits
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
