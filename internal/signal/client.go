//go:build signal

package signal

// The client: mautrix-signal's signalmeow (third_party/mautrix-signal, patched: PATCHES.md) in this
// process, linked as a secondary device. It answers the commands and says the events of the earlier
// helper's protocol (transport.go), so that the store and the importer read Signal as they did.
// The device (its keys, signalmeow's tables signalmeow_*) is in keys.db; signal_device names each
// plugin instance's. What Signal sends is acknowledged only once the store kept it: the handler of
// a message waits for its event to be handled (signalmeow leaves an envelope its handler refused
// unacknowledged, and the server sends it again).

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/store"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/web"

	"everysaid/internal/keys"
)

// available: Signal is built in.
var available = true

const deviceSchema = `
CREATE TABLE IF NOT EXISTS signal_device (     -- the linked device of each plugin instance
    instance INTEGER PRIMARY KEY,
    aci TEXT NOT NULL,                  -- its account (signalmeow_device.aci_uuid)
    name TEXT                           -- the device's name on the phone
);`

// client is signalmeow for one plugin instance.
type client struct {
	cfg       clientConfig
	ev        *eventQueue
	pool      *sql.DB
	container *store.Container
	log       zerolog.Logger

	ctx    context.Context // the client's life
	cancel context.CancelFunc
	work   sync.WaitGroup // what runs in the background (a profile asked for, the transfer, the storage)

	mu           sync.Mutex
	cli          *signalmeow.Client
	name         string
	receiving    bool
	download     bool
	checked      bool              // this connection's checks (number, storage, transfer) are under way
	transferring bool              // the history from the phone is awaited
	revisions    map[string]uint32 // groups described, at their revision
	described    map[string]bool   // people described (or being), by ACI
	ended        chan struct{}     // closed when the receiving ends
	stopLoops    func()            // stops the receiving's connections (once; a second call waits for the first)
	stopped      chan struct{}
	once         sync.Once
}

// logLines turns a logger's output into lines for the plugin's log.
type logLines struct {
	mu   sync.Mutex
	buf  []byte
	line func(string)
}

func (w *logLines) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		if s := strings.TrimSpace(string(w.buf[:i])); s != "" && w.line != nil {
			w.line(s)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// newLogger is signalmeow's logger: warnings and errors (EVERYSAID_SIGNAL_LOG=info, debug or trace
// for more), into the plugin's log.
func newLogger(line func(string)) zerolog.Logger {
	level := zerolog.WarnLevel
	if l, err := zerolog.ParseLevel(os.Getenv("EVERYSAID_SIGNAL_LOG")); err == nil && l != zerolog.NoLevel {
		level = l
	}
	out := zerolog.ConsoleWriter{Out: &logLines{line: line}, NoColor: true, PartsExclude: []string{zerolog.TimestampFieldName}}
	return zerolog.New(out).Level(level).With().Timestamp().Logger()
}

// openDevices is signalmeow's container over keys.db (its tables brought up to date, signal_device
// made), on a pool of its own.
func openDevices(ctx context.Context, log zerolog.Logger) (*sql.DB, *store.Container, error) {
	pool, err := keys.Own("", true)
	if err != nil {
		return nil, nil, err
	}
	d, err := dbutil.NewWithDB(pool, keys.Driver)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	container := store.NewStore(d, dbutil.ZeroLogger(log))
	if err := container.Upgrade(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("signalmeow's tables: %w", err)
	}
	if _, err := pool.Exec(deviceSchema); err != nil {
		pool.Close()
		return nil, nil, err
	}
	return pool, container, nil
}

func startClient(cfg clientConfig) (transport, error) {
	log := newLogger(cfg.Logs)
	signalmeow.SetLogger(log.With().Str("component", "libsignal").Logger())
	ctx, cancel := context.WithCancel(log.WithContext(context.Background()))
	pool, container, err := openDevices(ctx, log)
	if err != nil {
		cancel()
		return nil, err
	}
	c := &client{cfg: cfg, pool: pool, container: container, log: log, ctx: ctx, cancel: cancel,
		revisions: map[string]uint32{}, described: map[string]bool{}, stopped: make(chan struct{})}
	c.ev = newEventQueue(cfg.Handle)
	var aci string
	pool.QueryRow("SELECT aci, coalesce(name, '') FROM signal_device WHERE instance = ?", cfg.Instance).Scan(&aci, &c.name)
	if u, err := uuid.Parse(aci); err == nil {
		dev, err := container.DeviceByACI(ctx, u)
		if err != nil {
			c.Kill()
			return nil, err
		}
		if dev != nil && dev.IsDeviceLoggedIn() {
			c.cli = signalmeow.NewClient(dev, log, c.handle)
		}
	}
	return c, nil
}

// dropDevice removes an instance's device from keys.db: signalmeow's rows of it (its keys, sessions,
// contacts, groups) and signal_device's.
func dropDevice(instance int64) error {
	ctx := context.Background()
	pool, container, err := openDevices(ctx, zerolog.Nop())
	if err != nil {
		return err
	}
	defer pool.Close()
	var aci string
	if pool.QueryRow("SELECT aci FROM signal_device WHERE instance = ?", instance).Scan(&aci) != nil {
		return nil
	}
	u, err := uuid.Parse(aci)
	if err != nil {
		return err
	}
	var others int
	pool.QueryRow("SELECT count(*) FROM signal_device WHERE aci = ? AND instance != ?", aci, instance).Scan(&others)
	if others == 0 { // another instance linked the same account: the device is theirs too
		if err := container.DeleteDevice(ctx, &store.DeviceData{ACI: u}); err != nil {
			return err
		}
	}
	_, err = pool.Exec("DELETE FROM signal_device WHERE instance = ?", instance)
	return err
}

func (c *client) signal() *signalmeow.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cli
}

func (c *client) linked() (*signalmeow.Client, error) {
	cli := c.signal()
	if cli == nil || !cli.IsLoggedIn() {
		return nil, refuse("not_linked", "not linked to a Signal account")
	}
	return cli, nil
}

func (c *client) own(cli *signalmeow.Client) string { return cli.Store.ACIServiceID().String() }

func (c *client) conv(cli *signalmeow.Client) conv {
	return conv{own: c.own(cli), groupID: groupIDOf, groupChanges: func(g *signalpb.GroupContextV2) (M, bool) {
		return c.groupChanges(cli, g)
	}}
}

// Call answers a command: its fields as JSON gives them.
func (c *client) Call(ctx context.Context, cmd string, args map[string]any) (json.RawMessage, error) {
	select {
	case <-c.stopped:
		return nil, ErrStopped
	default:
	}
	raw, _ := json.Marshal(args)
	var result any
	var err error
	switch cmd {
	case "open", "status":
		result = c.status()
	case "link":
		var r struct {
			DeviceName string `json:"device_name"`
		}
		json.Unmarshal(raw, &r)
		result, err = c.link(ctx, orElse(r.DeviceName, "Everysaid"))
	case "sync":
		result, err = c.sync(ctx)
	case "contacts":
		result, err = c.contacts(ctx)
	case "groups":
		result, err = c.groups(ctx)
	case "receive":
		r := struct {
			Download *bool `json:"download"`
		}{}
		json.Unmarshal(raw, &r)
		result, err = c.receive(r.Download == nil || *r.Download)
	case "send":
		var r sendRequest
		if err = json.Unmarshal(raw, &r); err == nil {
			result, err = c.send(ctx, r)
		}
	case "react":
		var r struct {
			Chat   chatRef `json:"chat"`
			Target ref     `json:"target"`
			Emoji  string  `json:"emoji"`
			Remove bool    `json:"remove"`
		}
		if err = json.Unmarshal(raw, &r); err == nil {
			result, err = c.react(ctx, r.Chat, r.Target, r.Emoji, r.Remove)
		}
	case "edit":
		var r struct {
			Chat       chatRef `json:"chat"`
			TargetTS   uint64  `json:"target_ts"`
			OriginalTS *uint64 `json:"original_ts"`
			Text       string  `json:"text"`
		}
		if err = json.Unmarshal(raw, &r); err == nil {
			original := r.TargetTS
			if r.OriginalTS != nil {
				original = *r.OriginalTS
			}
			result, err = c.edit(ctx, r.Chat, r.TargetTS, original, r.Text)
		}
	case "delete":
		var r struct {
			Chat     chatRef `json:"chat"`
			TargetTS uint64  `json:"target_ts"`
		}
		if err = json.Unmarshal(raw, &r); err == nil {
			result, err = c.delete(ctx, r.Chat, r.TargetTS)
		}
	case "mark_read":
		r := struct {
			Messages []ref `json:"messages"`
			Receipts *bool `json:"receipts"`
		}{}
		if err = json.Unmarshal(raw, &r); err == nil {
			result, err = c.markRead(ctx, r.Messages, r.Receipts == nil || *r.Receipts)
		}
	case "fetch":
		var r struct {
			Messages []ref `json:"messages"`
		}
		if err = json.Unmarshal(raw, &r); err == nil {
			result, err = c.fetchAgain(ctx, r.Messages)
		}
	case "discover":
		var r struct {
			Numbers []string `json:"numbers"`
		}
		if err = json.Unmarshal(raw, &r); err == nil {
			result, err = c.discover(ctx, r.Numbers)
		}
	case "history":
		var r struct {
			Since uint64 `json:"since"`
		}
		json.Unmarshal(raw, &r)
		result, err = c.history(ctx, r.Since)
	case "quit":
		c.Close()
		result = M{}
	default:
		return nil, refuse("bad_request", "unknown command "+cmd)
	}
	if err != nil {
		var ce *ClientError
		if errors.As(err, &ce) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrStopped) {
			return nil, err
		}
		return nil, refuse("failed", err.Error())
	}
	return json.Marshal(result)
}

func orElse(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}

func (c *client) status() M {
	cli := c.signal()
	c.mu.Lock()
	receiving, name := c.receiving, c.name
	c.mu.Unlock()
	if cli == nil || !cli.IsLoggedIn() {
		return M{"open": true, "linked": false, "receiving": false}
	}
	d := cli.Store
	return M{"open": true, "linked": true, "receiving": receiving, "aci": d.ACIServiceID().String(),
		"pni": d.PNIServiceID().String(), "phone": d.Number, "device_id": d.DeviceID, "device_name": name}
}

// link links this device to the account: a link_url event for the phone to scan, then (once it did)
// the status. The phone is offered the transfer of its history (received once connected).
func (c *client) link(ctx context.Context, name string) (any, error) {
	if cli := c.signal(); cli != nil && cli.IsLoggedIn() {
		return nil, refuse("already_linked", "already linked")
	}
	ctx = c.log.WithContext(ctx)
	for r := range signalmeow.PerformProvisioning(ctx, c.container, name, true) {
		switch r.State {
		case signalmeow.StateProvisioningURLReceived:
			c.ev.say("link_url", M{"url": r.ProvisioningURL})
		case signalmeow.StateProvisioningError:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(r.Err, context.DeadlineExceeded) || strings.Contains(r.Err.Error(), "deadline exceeded") {
				return nil, context.DeadlineExceeded // the phone did not scan the code in time
			}
			return nil, refuse("failed", r.Err.Error())
		case signalmeow.StateProvisioningDataReceived:
			if _, err := c.pool.Exec("INSERT INTO signal_device (instance, aci, name) VALUES (?, ?, ?) ON CONFLICT (instance) "+
				"DO UPDATE SET aci = excluded.aci, name = excluded.name", c.cfg.Instance, r.ProvisioningData.ACI.String(), name); err != nil {
				return nil, err
			}
			dev, err := c.container.DeviceByACI(ctx, r.ProvisioningData.ACI)
			if err != nil || dev == nil {
				return nil, fmt.Errorf("the device just linked: %v", err)
			}
			c.mu.Lock()
			c.cli, c.name = signalmeow.NewClient(dev, c.log, c.handle), name
			c.mu.Unlock()
			return c.status(), nil
		}
	}
	return nil, refuse("failed", "linking ended")
}

// online is the linked client, connected to Signal: what sends needs the connection (signalmeow
// sends on its websockets), so a client not receiving yet starts to (what then arrives is kept as
// at any other time), and the call waits for the connection (until ctx).
func (c *client) online(ctx context.Context) (*signalmeow.Client, error) {
	cli, err := c.linked()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	receiving := c.receiving
	c.mu.Unlock()
	if !receiving {
		if _, err := c.receive(c.cfg.Download); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	ended := c.ended
	c.mu.Unlock()
	for {
		select { // the receiving ended (refused, stopped): its connections are going
		case <-ended:
			return nil, refuse("failed", "not connected to Signal")
		default:
		}
		// (signalmeow sets its websockets to nil when its loops stop)
		if a, u := cli.AuthedWS, cli.UnauthedWS; a != nil && u != nil && a.IsConnected() && u.IsConnected() {
			return cli, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.ctx.Done():
			return nil, ErrStopped
		case <-ended:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (c *client) sync(ctx context.Context) (any, error) {
	cli, err := c.online(ctx)
	if err != nil {
		return nil, err
	}
	if err := cli.SendContactSyncRequest(ctx); err != nil {
		return nil, err
	}
	return M{"requested": true}, nil
}

func contactJSON(r *types.Recipient) M {
	name := strings.TrimSpace(r.ContactName)
	if name == "" {
		name = strings.TrimSpace(r.Nickname)
	}
	ev := M{"aci": r.ACI.String(), "phone": nil, "name": nil, "profile_name": nil}
	if r.E164 != "" {
		ev["phone"] = r.E164
	}
	if name != "" {
		ev["name"] = name
	}
	if p := strings.TrimSpace(r.Profile.Name); p != "" {
		ev["profile_name"] = p
	}
	return ev
}

func (c *client) contacts(ctx context.Context) (any, error) {
	cli, err := c.linked()
	if err != nil {
		return nil, err
	}
	list, err := cli.Store.RecipientStore.LoadAllContacts(ctx)
	if err != nil {
		return nil, err
	}
	out := []M{}
	for _, r := range list {
		if r.ACI != uuid.Nil {
			out = append(out, contactJSON(r))
		}
	}
	return M{"contacts": out}, nil
}

func groupJSON(g *signalmeow.Group) M {
	members, pending := []string{}, []string{}
	for _, m := range g.Members {
		members = append(members, m.ACI.String())
	}
	for _, p := range g.PendingMembers {
		pending = append(pending, p.ServiceID.String())
	}
	return M{"id": string(g.GroupIdentifier), "title": g.Title, "description": g.Description, "revision": g.Revision,
		"members": members, "pending": pending}
}

func (c *client) groups(ctx context.Context) (any, error) {
	cli, err := c.online(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := c.pool.QueryContext(ctx, "SELECT group_identifier FROM signalmeow_groups WHERE account_id = ? ORDER BY 1",
		cli.Store.ACI.String())
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	out := []M{}
	for _, id := range ids {
		if g, _, err := cli.RetrieveGroupByID(ctx, types.GroupIdentifier(id), 0); err == nil {
			out = append(out, groupJSON(g))
		}
	}
	return M{"groups": out}, nil
}

// receive connects and receives, until the client ends or Signal refuses it (receive_ended).
func (c *client) receive(download bool) (any, error) {
	cli, err := c.linked()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.receiving {
		c.mu.Unlock()
		return M{"started": false}, nil
	}
	prev := c.stopLoops
	c.receiving, c.download, c.checked, c.ended = true, download, false, make(chan struct{})
	c.stopLoops = sync.OnceFunc(func() { cli.StopReceiveLoops() })
	ended := c.ended
	c.mu.Unlock()
	if prev != nil {
		prev() // the connections of a receiving that ended are closed first (or waited for)
	}
	cli.SyncContactsOnConnect = false // asked for by the plugin, once
	status, err := cli.StartReceiveLoops(c.ctx)
	if err != nil {
		c.mu.Lock()
		c.receiving = false
		c.mu.Unlock()
		return nil, err
	}
	c.work.Add(1)
	go func() {
		defer c.work.Done()
		for {
			select {
			case s, ok := <-status:
				if !ok {
					c.endReceiving(cli, ended, nil, "")
					return
				}
				switch s.Event {
				case signalmeow.SignalConnectionEventConnected:
					c.mu.Lock()
					c.checked = false // each connection checks the number again
					c.mu.Unlock()
				case signalmeow.SignalConnectionEventLoggedOut:
					c.endReceiving(cli, ended, s.Err, "unlinked")
					return
				case signalmeow.SignalConnectionEventFatalError:
					c.endReceiving(cli, ended, s.Err, refusal(s.Err))
					return
				case signalmeow.SignalConnectionCleanShutdown:
					c.endReceiving(cli, ended, nil, "")
					return
				}
			case <-ended:
				return
			}
		}
	}()
	return M{"started": true}, nil
}

// refusal is the code of a refusal by Signal's server: unlinked (the device was removed from the
// phone, or its account is gone), outdated (HTTP 499: this version is too old); "" for another.
func refusal(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, web.ErrClientOutdated):
		return "outdated"
	case strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403"):
		return "unlinked"
	}
	return ""
}

// endReceiving ends this receiving once: the connection closed, said with why (nil: by itself).
func (c *client) endReceiving(cli *signalmeow.Client, ended chan struct{}, why error, code string) {
	c.mu.Lock()
	if c.ended != ended || !c.receiving {
		c.mu.Unlock()
		return
	}
	c.receiving = false
	close(ended)
	stop := c.stopLoops
	c.mu.Unlock()
	go stop() // not here: this may be one of the receiving's own goroutines
	var text any
	if why != nil {
		text = why.Error()
	}
	ev := M{"error": text}
	if code != "" {
		ev["code"] = code
	}
	select {
	case <-c.ctx.Done(): // the client is ending: no failure
		ev = M{"error": nil, "code": "stopped"}
	default:
	}
	c.ev.say("receive_ended", ev)
}

// handle is signalmeow's handler of what Signal sends: false leaves it unacknowledged.
func (c *client) handle(evt events.SignalEvent) bool {
	cli := c.signal()
	if cli == nil {
		return true
	}
	switch e := evt.(type) {
	case *events.Content:
		return c.content(cli, e)
	case *events.DecryptionError:
		// nothing to note of what was not to be shown (typing, receipts...)
		if e.ContentHint == libsignalgo.UnidentifiedSenderMessageContentHintImplicit {
			return true
		}
		sender := libsignalgo.NewACIServiceID(e.Sender).String()
		chat := contactChat(sender)
		if e.GroupID != nil {
			chat = M{"kind": "group", "id": e.GroupID.String()}
		}
		return c.ev.sayAndWait("unreadable", M{"sender": sender, "device": e.SenderDevice, "ts": e.Timestamp, "chat": chat,
			"retry": e.Retried, "resendable": e.ContentHint == libsignalgo.UnidentifiedSenderMessageContentHintResendable}) == nil
	case *events.ACIFound:
		// someone written to by number answered from their ACI with a signature of that number's PNI
		// (Signal's apps add it until they hear back): the two chats are one person's
		return c.ev.sayAndWait("ids", M{"aci": e.ACI.String(), "pni": e.PNI.String(), "phone": nil}) == nil
	case *events.ContactList:
		if e.IsFromDB { // the storage service's: read whole by readStorage
			return true
		}
		list := []M{}
		for _, r := range e.Contacts {
			if r.ACI != uuid.Nil {
				list = append(list, contactJSON(r))
			}
		}
		c.mu.Lock()
		for _, r := range list {
			c.described[r["aci"].(string)] = true
		}
		c.mu.Unlock()
		return c.ev.sayAndWait("contacts", M{"contacts": list}) == nil
	case *events.QueueEmpty:
		c.queueEmpty(cli)
	case *events.LoggedOut:
		c.mu.Lock()
		ended := c.ended
		c.mu.Unlock()
		c.endReceiving(cli, ended, e.Error, "unlinked")
	}
	return true
}

// content is one decrypted content: its events, each kept before it is acknowledged.
func (c *client) content(cli *signalmeow.Client, e *events.Content) bool {
	m := meta{sender: e.Sender.String(), destination: e.Destination.String(), device: uint32(e.SenderDevice),
		ts: e.Envelope.GetClientTimestamp(), serverTS: e.Envelope.GetServerTimestamp()}
	own := c.own(cli)
	// someone could not read a message: one sent from here only is said (signalmeow sends it again)
	if b := e.Content.GetDecryptionErrorMessage(); b != nil {
		dem, err := libsignalgo.DeserializeDecryptionErrorMessage(b)
		if err != nil {
			return true
		}
		if device, err := dem.GetDeviceID(); err != nil || int(device) != cli.Store.DeviceID {
			return true
		}
	}
	// a group seen first in this message: its key kept, so that the group can be read
	if k := masterKey(dataOf(e.Content).GetGroupV2()); k != nil && (e.Content.GetSyncMessage() == nil || m.sender == own) {
		cli.StoreMasterKey(c.ctx, serializedKey(k))
	}
	c.mu.Lock()
	download := c.download
	c.mu.Unlock()
	for _, x := range c.conv(cli).convert(m, e.Content) {
		if m.sender != own && !c.mayWriteIn(cli, x, m.sender) {
			c.log.Warn().Str("sender", m.sender).Msg("a group message from someone not in the group: dropped")
			continue
		}
		if err := c.deliver(c.ctx, cli, x, download, true); err != nil {
			return false
		}
	}
	if m.sender != own && e.Sender.Type == libsignalgo.ServiceIDTypeACI {
		c.describeLater(cli, e.Sender.UUID)
	}
	if sm := e.Content.GetSyncMessage(); sm != nil && m.sender == own {
		switch {
		case sm.GetFetchLatest().GetType() == signalpb.SyncMessage_FetchLatest_STORAGE_MANIFEST:
			c.later(func() { c.readStorage(cli) }) // the phone changed a chat's state
		case sm.GetKeys() != nil:
			c.later(func() { // signalmeow keeps the storage key after this handler
				time.Sleep(5 * time.Second)
				c.readStorage(cli)
			})
		}
	}
	return true
}

func dataOf(content *signalpb.Content) *signalpb.DataMessage {
	if dm := content.GetDataMessage(); dm != nil {
		return dm
	}
	if dm := content.GetEditMessage().GetDataMessage(); dm != nil {
		return dm
	}
	// the phone's copy of what it sent (a group's first message seen here may be one)
	if sent := content.GetSyncMessage().GetSent(); sent != nil {
		if dm := sent.GetMessage(); dm != nil {
			return dm
		}
		return sent.GetEditMessage().GetDataMessage()
	}
	return nil
}

func serializedKey(k []byte) types.SerializedGroupMasterKey {
	return types.SerializedGroupMasterKey(base64Std(k))
}

// later runs work in the background, while the client lives.
func (c *client) later(f func()) {
	select {
	case <-c.ctx.Done():
		return
	default:
	}
	c.work.Add(1)
	go func() {
		defer c.work.Done()
		f()
	}()
}

// group is a group as Signal has it at least at revision (nil: not known).
func (c *client) group(cli *signalmeow.Client, key []byte, revision uint32) *signalmeow.Group {
	gid := types.GroupIdentifier(groupIDOf(key))
	if gid == "" {
		return nil
	}
	g, _, err := cli.RetrieveGroupByID(c.ctx, gid, revision)
	if err != nil {
		c.log.Warn().Err(err).Msg("a group could not be read")
		return nil
	}
	return g
}

func revisionOf(ev M) (uint32, bool) {
	r, ok := ev["group_revision"].(uint32)
	return r, ok
}

// mayWriteIn: as Signal Desktop, one who is not a member of a group at the message's revision is not
// heard. Only the group at exactly that revision says so (one who left since wrote while a member);
// taken as it comes: a message of no group, of a group not known or of another revision, and a
// change of the group (one leaving sends it).
func (c *client) mayWriteIn(cli *signalmeow.Client, x converted, sender string) bool {
	if x.group == nil || x.ev["group_change"] == true {
		return true
	}
	rev, ok := revisionOf(x.ev)
	if !ok {
		return true
	}
	g := c.group(cli, x.group, rev)
	if g == nil || g.Revision != rev {
		return true
	}
	for _, m := range g.Members {
		if m.ACI.String() == sender {
			return true
		}
	}
	return false
}

// deletionAllowed: a deletion of another's message only from an admin of its group.
func (c *client) deletionAllowed(cli *signalmeow.Client, x converted) bool {
	if x.name != "delete" || x.ev["admin"] != true {
		return true
	}
	sender, _ := x.ev["sender"].(string)
	target, _ := x.ev["target_author"].(string)
	var admins []string
	known := false
	if x.group != nil {
		rev, _ := revisionOf(x.ev)
		if g := c.group(cli, x.group, rev); g != nil {
			known = true
			for _, m := range g.Members {
				if m.Role == signalmeow.GroupMember_ADMINISTRATOR {
					admins = append(admins, m.ACI.String())
				}
			}
		}
	}
	ok := adminDeleteAllowed(sender, target, admins, known)
	if !ok {
		c.log.Warn().Str("sender", sender).Msg("a deletion of another's message by someone not an admin of the group: ignored")
	}
	return ok
}

// deliver says an event: its group described first where that changed, its files fetched where
// wanted. wait: until it was handled (the error is the handler's), for what Signal waits for.
func (c *client) deliver(ctx context.Context, cli *signalmeow.Client, x converted, download, wait bool) error {
	if !c.deletionAllowed(cli, x) {
		return nil
	}
	if x.group != nil {
		rev, _ := revisionOf(x.ev)
		if g := c.group(cli, x.group, rev); g != nil {
			c.mu.Lock()
			known, ok := c.revisions[string(g.GroupIdentifier)]
			c.revisions[string(g.GroupIdentifier)] = g.Revision
			c.mu.Unlock()
			if !ok || known != g.Revision {
				c.ev.say("group", groupJSON(g))
			}
		}
	}
	c.files(ctx, x, download)
	if wait {
		return c.ev.sayAndWait(x.name, x.ev)
	}
	c.ev.say(x.name, x.ev)
	return nil
}

// maxLongText is how much of a long message's text is kept (Signal Desktop's
// MAX_BODY_ATTACHMENT_BYTE_LENGTH).
const maxLongText = 64 * 1024

// files fetches an event's files into the media folder (where download), named by attachmentName;
// a long message's text (fetched whatever the setting) becomes its text, and leaves its files.
func (c *client) files(ctx context.Context, x converted, download bool) {
	list, _ := x.ev["attachments"].([]M)
	if len(list) == 0 || len(list) != len(x.atts) {
		return
	}
	ts := toU64(x.ev["ts"])
	author, _ := x.ev["sender"].(string)
	os.MkdirAll(c.cfg.Media, 0o700)
	long := -1
	for i, ptr := range x.atts {
		if ptr.GetContentType() != longText {
			continue
		}
		long = i
		name := attachmentName(ts, author, i, ptr.GetContentType(), ptr.GetFileName())
		path := filepath.Join(c.cfg.Media, name)
		if ptr.GetSize() > maxLongText {
			c.log.Warn().Uint32("size", ptr.GetSize()).Msg("the text of a long message is too long: its first 2 KiB are kept")
			long = -1
			break
		}
		if !isFile(path) {
			if err := c.fetch(ctx, ptr, hashOf(x, i), path); err != nil {
				c.log.Warn().Err(err).Msg("the text of a long message")
				list[i]["error"] = err.Error()
				long = -1
				break
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			long = -1
			break
		}
		os.Remove(path) // the text is in the event: the file is not kept
		if len(b) > maxLongText {
			b = b[:maxLongText]
		}
		x.ev["text"] = strings.TrimRight(strings.ToValidUTF8(string(b), "�"), "�")
		x.ev["long_text"] = true
		break
	}
	for i, ptr := range x.atts {
		if i == long {
			continue
		}
		name := attachmentName(ts, author, i, ptr.GetContentType(), ptr.GetFileName())
		path := filepath.Join(c.cfg.Media, name)
		switch {
		case isFile(path):
			list[i]["file"] = name
		case download && ptr.GetCdnKey() == "" && ptr.GetCdnId() == 0:
			list[i]["error"] = "not on Signal's server" // a file of the history the phone kept to itself
		case download:
			if err := c.fetch(ctx, ptr, hashOf(x, i), path); err != nil {
				list[i]["error"] = err.Error()
			} else {
				list[i]["file"] = name
			}
		}
	}
	// the text's file stays in the list (the importer leaves it out), so that each file keeps the
	// name of its place among the message's, also when fetched again or kept by an edit
	if long >= 0 {
		delete(list[long], "error")
	}
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func toU64(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case int64:
		return uint64(n)
	case float64:
		return uint64(n)
	case int:
		return uint64(n)
	case json.Number:
		u, _ := strconv.ParseUint(string(n), 10, 64)
		return u
	}
	return 0
}

// fetch downloads a file into path (by a part file, renamed once whole).
func (c *client) fetch(ctx context.Context, ptr *signalpb.AttachmentPointer, hash []byte, path string) error {
	part := path + ".part"
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel) // the client ending ends it too
	defer stop()
	_, err = signalmeow.DownloadAttachmentWithPointer(ctx, ptr, hash, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, path)
}

// describeLater: someone whose message came, described once their profile is fetched: a `contact`
// event, so that a group's members have their names before the phone next sends its contacts.
func (c *client) describeLater(cli *signalmeow.Client, aci uuid.UUID) {
	c.mu.Lock()
	if c.described[aci.String()] {
		c.mu.Unlock()
		return
	}
	c.described[aci.String()] = true
	c.mu.Unlock()
	c.later(func() {
		select {
		case <-time.After(5 * time.Second):
		case <-c.ctx.Done():
			return
		}
		r, err := cli.ContactByACI(c.ctx, aci)
		if err == nil && r != nil && (strings.TrimSpace(r.Profile.Name) != "" || strings.TrimSpace(r.ContactName) != "") {
			c.ev.say("contact", contactJSON(r))
			return
		}
		c.mu.Lock()
		delete(c.described, aci.String()) // tried again at their next message
		c.mu.Unlock()
	})
}

// queueEmpty: what waited on the server has all come. Once a connection: whether the account's
// number is still the one this device was linked with, the chats' state on the phone, and the
// history the phone transfers once linked.
func (c *client) queueEmpty(cli *signalmeow.Client) {
	c.mu.Lock()
	first := !c.checked
	c.checked = true
	c.mu.Unlock()
	if first {
		c.later(func() { c.checkNumber(cli) })
		c.later(func() { c.readStorage(cli) })
		c.later(func() { c.transfer(cli) })
	}
	c.ev.say("queue_empty", nil)
}

// checkNumber says number_changed where the account's number (its PNI) is no longer the one this
// device was linked with: after a change of number, what is sent to the new one cannot be read here
// until the device is linked again (said, never done for the owner).
func (c *client) checkNumber(cli *signalmeow.Client) {
	ws := cli.AuthedWS
	if ws == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	resp, err := ws.SendRequest(ctx, http.MethodGet, "/v1/accounts/whoami", nil, nil)
	if err != nil || resp.GetStatus() != http.StatusOK {
		return
	}
	var who struct {
		PNI    string `json:"pni"`
		Number string `json:"number"`
	}
	if json.Unmarshal(resp.GetBody(), &who) != nil {
		return
	}
	if pni, err := uuid.Parse(who.PNI); err == nil && pni != uuid.Nil && pni != cli.Store.PNI {
		c.log.Warn().Msg("the account's number changed since this device was linked")
		c.ev.say("number_changed", M{"phone": who.Number})
	}
}

// readStorage reads Signal's storage service (the phone's record of contacts, groups and the
// account): the chats' state (archived, pinned, muted), each person's ids and names.
func (c *client) readStorage(cli *signalmeow.Client) {
	if cli.Store.MasterKey == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Minute)
	defer cancel()
	update, err := cli.FetchStorage(ctx, cli.Store.MasterKey, 0, nil)
	if err != nil || update == nil {
		if err != nil {
			c.log.Warn().Err(err).Msg("the storage service could not be read")
		}
		return
	}
	now := time.Now().UnixMilli()
	type state struct {
		chat M
		set  M
	}
	var chats []state
	pinned := map[string]bool{}
	muted := func(until uint64) int64 {
		if until >= math.MaxInt64 {
			return -1 // for ever
		}
		return int64(until)
	}
	own := c.own(cli)
	for _, rec := range update.NewRecords {
		r := rec.StorageRecord
		switch {
		case r.GetContact() != nil:
			x := r.GetContact()
			aci, _ := signalmeow.ParseStringOrBinaryUUID(x.GetAci(), x.GetAciBinary())
			pni, _ := signalmeow.ParseStringOrBinaryUUID(x.GetPni(), x.GetPniBinary())
			var a, p, phone any
			if aci != uuid.Nil {
				a = aci.String()
			}
			if pni != uuid.Nil {
				p = libsignalgo.NewPNIServiceID(pni).String()
			}
			if x.GetE164() != "" {
				phone = x.GetE164()
			}
			if a != nil && (p != nil || phone != nil) || p != nil && phone != nil {
				c.ev.say("ids", M{"aci": a, "pni": p, "phone": phone})
			}
			if a != nil {
				name, profile := joinNonEmpty(x.GetSystemGivenName(), x.GetSystemFamilyName()), joinNonEmpty(x.GetGivenName(), x.GetFamilyName())
				if name != "" || profile != "" {
					ev := M{"aci": a, "phone": phone, "name": nil, "profile_name": nil}
					if name != "" {
						ev["name"] = name
					}
					if profile != "" {
						ev["profile_name"] = profile
					}
					c.ev.say("contact", ev)
				}
			}
			id, _ := a.(string)
			if id == "" {
				id, _ = p.(string)
			}
			if id != "" {
				chats = append(chats, state{contactChat(id), M{"archived": b2i(x.GetArchived()), "muted": muted(x.GetMutedUntilTimestamp())}})
			}
		case r.GetGroupV2() != nil:
			x := r.GetGroupV2()
			if id := groupIDOf(x.GetMasterKey()); id != "" {
				chats = append(chats, state{M{"kind": "group", "id": id}, M{"archived": b2i(x.GetArchived()),
					"muted": muted(x.GetMutedUntilTimestamp())}})
			}
		case r.GetAccount() != nil:
			x := r.GetAccount()
			for _, p := range x.GetPinnedConversations() {
				if ct := p.GetContact(); ct != nil {
					pinned[serviceIDText(ct.GetServiceId(), ct.GetServiceIdBinary())] = true
				} else if k := p.GetGroupMasterKey(); len(k) > 0 {
					pinned[groupIDOf(k)] = true
				}
			}
			chats = append(chats, state{contactChat(own), M{"archived": b2i(x.GetNoteToSelfArchived())}})
		}
	}
	for _, s := range chats {
		s.set["pinned"] = b2i(pinned[s.chat["id"].(string)])
		c.ev.say("chat_state", M{"chat": s.chat, "state": s.set, "ts": now})
	}
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// groupChanges reads a group's change as its actions (Signal's apps show one line for each):
// {"editor", "actions"}, each action {"type", ...} with people by ACI (or PNI for one invited by
// number). False where the change cannot be read.
func (c *client) groupChanges(cli *signalmeow.Client, g *signalpb.GroupContextV2) (M, bool) {
	ch, err := cli.DecryptGroupChange(c.ctx, g)
	if err != nil || ch == nil {
		return nil, false
	}
	editor := ch.SourceServiceID.String()
	aci := func(u uuid.UUID) string { return u.String() }
	level := func(l signalmeow.AccessControl) string {
		return map[signalmeow.AccessControl]string{signalmeow.AccessControl_ANY: "anyone", signalmeow.AccessControl_MEMBER: "members",
			signalmeow.AccessControl_ADMINISTRATOR: "admins", signalmeow.AccessControl_UNSATISFIABLE: "off"}[l]
	}
	actions := []M{}
	add := func(m M) { actions = append(actions, m) }
	for _, m := range ch.AddMembers {
		if aci(m.ACI) == editor {
			add(M{"type": "joined_link", "who": editor})
		} else {
			add(M{"type": "added", "who": aci(m.ACI)})
		}
	}
	for _, u := range ch.DeleteMembers {
		if u != nil && aci(*u) == editor {
			add(M{"type": "left", "who": editor})
		} else if u != nil {
			add(M{"type": "removed", "who": aci(*u)})
		}
	}
	for _, m := range ch.ModifyMemberRoles {
		add(M{"type": "admin", "who": aci(m.ACI), "on": m.Role == signalmeow.GroupMember_ADMINISTRATOR})
	}
	for _, p := range ch.AddPendingMembers {
		add(M{"type": "invited", "who": p.ServiceID.String()})
	}
	for _, s := range ch.DeletePendingMembers {
		if s == nil {
			continue
		}
		if s.String() == editor {
			add(M{"type": "invite_declined", "who": s.String()})
		} else {
			add(M{"type": "invite_revoked", "who": s.String()})
		}
	}
	for _, p := range ch.PromotePendingMembers {
		add(M{"type": "invite_accepted", "who": aci(p.ACI)})
	}
	for _, p := range ch.PromotePendingPniAciMembers {
		add(M{"type": "invite_accepted", "who": aci(p.ACI)})
	}
	for _, r := range ch.AddRequestingMembers {
		add(M{"type": "requested", "who": aci(r.ACI)})
	}
	for _, u := range ch.DeleteRequestingMembers {
		if u != nil && aci(*u) == editor {
			add(M{"type": "request_withdrawn", "who": editor})
		} else if u != nil {
			add(M{"type": "request_denied", "who": aci(*u)})
		}
	}
	for _, m := range ch.PromoteRequestingMembers {
		add(M{"type": "request_approved", "who": aci(m.ACI)})
	}
	if t := ch.ModifyTitle; t != nil {
		add(M{"type": "title", "title": *t})
	}
	if a := ch.ModifyAvatar; a != nil {
		if *a == "" {
			add(M{"type": "avatar", "removed": true})
		} else {
			add(M{"type": "avatar"})
		}
	}
	if t := ch.ModifyDisappearingMessagesDuration; t != nil {
		add(M{"type": "timer", "seconds": *t})
	}
	if d := ch.ModifyDescription; d != nil {
		add(M{"type": "description", "text": *d})
	}
	if l := ch.ModifyAttributesAccess; l != nil {
		add(M{"type": "access_info", "level": orElse(level(*l), "unknown")})
	}
	if l := ch.ModifyMemberAccess; l != nil {
		add(M{"type": "access_members", "level": orElse(level(*l), "unknown")})
	}
	if l := ch.ModifyAddFromInviteLinkAccess; l != nil {
		add(M{"type": "access_link", "level": orElse(level(*l), "unknown")})
	}
	if ch.ModifyInviteLinkPassword != nil {
		add(M{"type": "link_reset"})
	}
	if on := ch.ModifyAnnouncementsOnly; on != nil {
		add(M{"type": "announcements", "on": *on})
	}
	for _, b := range ch.AddBannedMembers {
		add(M{"type": "banned", "who": b.ServiceID.String()})
	}
	for _, s := range ch.DeleteBannedMembers {
		if s != nil {
			add(M{"type": "unbanned", "who": s.String()})
		}
	}
	// the group ended by an admin: not in signalmeow's reading of the change, read from its actions
	var change signalpb.GroupChange
	var acts signalpb.GroupChange_Actions
	if proto.Unmarshal(g.GetGroupChange(), &change) == nil && proto.Unmarshal(change.GetActions(), &acts) == nil &&
		acts.GetTerminateGroup() != nil {
		add(M{"type": "ended"})
	}
	return M{"editor": editor, "actions": actions}, true
}

// sendRequest is what `send` is asked.
type sendRequest struct {
	Chat  chatRef `json:"chat"`
	Text  string  `json:"text"`
	Quote *struct {
		TS     uint64  `json:"ts"`
		Author string  `json:"author"`
		Text   *string `json:"text"`
	} `json:"quote"`
	Mentions    []mentionEv `json:"mentions"`
	Attachments []struct {
		Path        string `json:"path"`
		ContentType string `json:"content_type"`
		Filename    string `json:"filename"`
		Voice       bool   `json:"voice"`
	} `json:"attachments"`
}

func now() uint64 { return uint64(time.Now().UnixMilli()) }

// sendTo sends a content into a chat: to the person, or to the group's members (signalmeow puts the
// group's context in the message).
func (c *client) sendTo(ctx context.Context, cli *signalmeow.Client, chat chatRef, content *signalpb.Content) error {
	switch chat.Kind {
	case "contact":
		sid, err := libsignalgo.ServiceIDFromString(chat.ID)
		if err != nil {
			return refuse("bad_request", "not a Signal id")
		}
		res := cli.SendMessage(ctx, sid, content)
		if !res.WasSuccessful {
			if res.Error != nil {
				return res.Error
			}
			return errors.New("not sent")
		}
		return nil
	case "group":
		gid := types.GroupIdentifier(chat.ID)
		if k, err := cli.Store.GroupStore.MasterKeyFromGroupIdentifier(ctx, gid); err != nil || k == "" {
			return refuse("unknown_group", "unknown group")
		}
		_, err := cli.SendGroupMessage(ctx, gid, content)
		return err
	}
	return refuse("bad_request", "chat kind is contact or group")
}

func chatJSON(chat chatRef) M { return M{"kind": chat.Kind, "id": chat.ID} }

func (c *client) send(ctx context.Context, req sendRequest) (any, error) {
	cli, err := c.online(ctx)
	if err != nil {
		return nil, err
	}
	ts := now()
	dm := &signalpb.DataMessage{Timestamp: proto.Uint64(ts)}
	if req.Text != "" {
		dm.Body = proto.String(req.Text)
	}
	if q := req.Quote; q != nil {
		dm.Quote = &signalpb.DataMessage_Quote{Id: proto.Uint64(q.TS), AuthorAci: proto.String(q.Author), Text: q.Text,
			Type: signalpb.DataMessage_Quote_NORMAL.Enum()}
	}
	for _, x := range req.Mentions {
		dm.BodyRanges = append(dm.BodyRanges, &signalpb.BodyRange{Start: proto.Uint32(uint32(x.Start)),
			Length: proto.Uint32(uint32(x.Length)), AssociatedValue: &signalpb.BodyRange_MentionAci{MentionAci: x.ACI}})
	}
	var local [][]byte
	for _, a := range req.Attachments {
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return nil, err
		}
		name := orElse(a.Filename, filepath.Base(a.Path))
		ct := a.ContentType
		if ct == "" {
			ct = orElse(mime.TypeByExtension(filepath.Ext(name)), "application/octet-stream")
		}
		ptr, err := cli.UploadAttachment(ctx, data)
		if err != nil {
			return nil, err
		}
		ptr.ContentType, ptr.FileName = proto.String(ct), proto.String(name)
		if a.Voice {
			ptr.Flags = proto.Uint32(uint32(signalpb.AttachmentPointer_VOICE_MESSAGE))
		}
		dm.Attachments = append(dm.Attachments, ptr)
		local = append(local, data)
	}
	if err := c.sendTo(ctx, cli, req.Chat, signalmeow.WrapDataMessage(dm)); err != nil {
		return nil, err
	}
	// what was sent comes back as the others' messages do, its files kept like theirs
	x := c.conv(cli).sentEvent(uint32(cli.Store.DeviceID), chatJSON(req.Chat), dm, ts)
	if list, _ := x.ev["attachments"].([]M); len(list) == len(local) {
		os.MkdirAll(c.cfg.Media, 0o700)
		for i, data := range local {
			name := attachmentName(ts, c.own(cli), i, x.atts[i].GetContentType(), x.atts[i].GetFileName())
			if os.WriteFile(filepath.Join(c.cfg.Media, name), data, 0o600) == nil {
				list[i]["file"] = name
			}
		}
	}
	c.ev.say(x.name, x.ev)
	return M{"ts": ts}, nil
}

// sentBack says what this client sent besides a message (a reaction, an edit, a deletion), as the
// events of the others' are, so that it is kept the same way.
func (c *client) sentBack(cli *signalmeow.Client, chat chatRef, content *signalpb.Content, ts uint64) []converted {
	to := ""
	if chat.Kind == "contact" {
		to = chat.ID
	}
	return c.conv(cli).sentEvents(uint32(cli.Store.DeviceID), to, content, ts)
}

func (c *client) react(ctx context.Context, chat chatRef, target ref, emoji string, remove bool) (any, error) {
	if emoji == "" {
		return nil, refuse("bad_request", "no emoji")
	}
	cli, err := c.online(ctx)
	if err != nil {
		return nil, err
	}
	ts := now()
	dm := &signalpb.DataMessage{Timestamp: proto.Uint64(ts), Reaction: &signalpb.DataMessage_Reaction{Emoji: proto.String(emoji),
		Remove: proto.Bool(remove), TargetAuthorAci: proto.String(target.Author), TargetSentTimestamp: proto.Uint64(uint64(target.TS))}}
	content := signalmeow.WrapDataMessage(dm)
	if err := c.sendTo(ctx, cli, chat, content); err != nil {
		return nil, err
	}
	c.sayBack(cli, chat, content, ts, 0, 0)
	return M{"ts": ts}, nil
}

// sayBack says the events of what was sent: in a group's chat where it went to one (signalmeow put
// the group's context in it), with an edit's files as the message's were fetched.
func (c *client) sayBack(cli *signalmeow.Client, chat chatRef, content *signalpb.Content, ts, original, target uint64) {
	for _, x := range c.sentBack(cli, chat, content, ts) {
		if chat.Kind == "group" {
			x.ev["chat"] = chatJSON(chat)
		}
		if list, _ := x.ev["attachments"].([]M); x.name == "edit" && len(list) == len(x.atts) {
			for i, ptr := range x.atts {
				for _, at := range []uint64{original, target} {
					name := attachmentName(at, c.own(cli), i, ptr.GetContentType(), ptr.GetFileName())
					if isFile(filepath.Join(c.cfg.Media, name)) {
						list[i]["file"] = name
						break
					}
				}
			}
		}
		c.ev.say(x.name, x.ev)
	}
}

// edit gives the owner's message a new text. Signal's edit replaces the whole message, so it is made
// from the one the store holds (its files and quote kept): the version edited (target), else the
// first.
func (c *client) edit(ctx context.Context, chat chatRef, target, original uint64, text string) (any, error) {
	cli, err := c.online(ctx)
	if err != nil {
		return nil, err
	}
	own := c.own(cli)
	var old *signalpb.DataMessage
	if s := c.cfg.Store; s != nil {
		var js string
		if s.DB.QueryRow("SELECT json FROM signal_edit WHERE instance = ? AND author = ? AND ts = ?", s.Instance, own, target).Scan(&js) != nil {
			raw, _ := s.MessageJSON(own, int64(original))
			js = string(raw)
		}
		var e event
		if js != "" && json.Unmarshal([]byte(js), &e) == nil {
			old = storedMessage(e)
		}
	}
	if text == "" && (old == nil || len(old.Attachments) == 0) {
		return nil, refuse("bad_request", "an edit leaves nothing of the message")
	}
	ts := now()
	content := signalmeow.WrapEditMessage(&signalpb.EditMessage{TargetSentTimestamp: proto.Uint64(target),
		DataMessage: edited(old, text, ts)})
	if err := c.sendTo(ctx, cli, chat, content); err != nil {
		return nil, err
	}
	c.sayBack(cli, chat, content, ts, original, target)
	return M{"ts": ts}, nil
}

func (c *client) delete(ctx context.Context, chat chatRef, target uint64) (any, error) {
	cli, err := c.online(ctx)
	if err != nil {
		return nil, err
	}
	ts := now()
	content := signalmeow.WrapDataMessage(&signalpb.DataMessage{Timestamp: proto.Uint64(ts),
		Delete: &signalpb.DataMessage_Delete{TargetSentTimestamp: proto.Uint64(target)}})
	if err := c.sendTo(ctx, cli, chat, content); err != nil {
		return nil, err
	}
	c.sayBack(cli, chat, content, ts, 0, 0)
	return M{"ts": ts}, nil
}

// markRead: the others' messages read, said to the account's other devices (the phone) always, as
// Signal Desktop does, and to each author with a read receipt where receipts (the user's choice;
// signalmeow sends the read sync with each receipt).
func (c *client) markRead(ctx context.Context, messages []ref, receipts bool) (any, error) {
	cli, err := c.online(ctx)
	if err != nil {
		return nil, err
	}
	own := c.own(cli)
	byAuthor := map[string][]uint64{}
	var authors []string
	for _, m := range messages {
		if m.Author == own {
			continue
		}
		if _, ok := byAuthor[m.Author]; !ok {
			authors = append(authors, m.Author)
		}
		byAuthor[m.Author] = append(byAuthor[m.Author], uint64(m.TS))
	}
	marked := 0
	var reads []*signalpb.SyncMessage_Read
	for _, author := range authors {
		stamps := byAuthor[author]
		sid, err := libsignalgo.ServiceIDFromString(author)
		if err != nil {
			return nil, refuse("bad_request", "not a Signal id")
		}
		if receipts {
			if res := cli.SendMessage(ctx, sid, signalmeow.ReadReceptMessageForTimestamps(stamps)); !res.WasSuccessful {
				return nil, fmt.Errorf("a read receipt: %v", res.Error)
			}
		} else {
			for _, t := range stamps {
				reads = append(reads, &signalpb.SyncMessage_Read{SenderAci: proto.String(author), Timestamp: proto.Uint64(t)})
			}
		}
		marked += len(stamps)
	}
	if len(reads) > 0 {
		if res := cli.SendMessage(ctx, cli.Store.ACIServiceID(), signalmeow.WrapSyncMessage(&signalpb.SyncMessage{Read: reads})); !res.WasSuccessful {
			return nil, fmt.Errorf("a read sync: %v", res.Error)
		}
	}
	return M{"marked": marked}, nil
}

// fetchAgain fetches again the files of messages the store holds, where they failed before: each
// message comes again as its event, its files in it where they came now.
func (c *client) fetchAgain(ctx context.Context, refs []ref) (any, error) {
	cli, err := c.linked()
	if err != nil {
		return nil, err
	}
	found := 0
	for _, r := range refs {
		if c.cfg.Store == nil {
			break
		}
		raw, ok := c.cfg.Store.MessageJSON(r.Author, r.TS)
		if !ok {
			continue
		}
		var e event
		var ev M
		if json.Unmarshal(raw, &e) != nil || json.Unmarshal(raw, &ev) != nil || len(e.Attachments) == 0 {
			continue
		}
		var atts []*signalpb.AttachmentPointer
		var hashes [][]byte
		list := []M{}
		for i, a := range e.Attachments {
			p := pointerOf(a.Pointer)
			if p == nil {
				atts = nil
				break
			}
			atts = append(atts, p)
			hash, _ := base64.StdEncoding.DecodeString(a.Hash)
			hashes = append(hashes, hash)
			m, _ := ev["attachments"].([]any)[i].(map[string]any)
			delete(m, "error")
			list = append(list, m)
		}
		if atts == nil {
			continue
		}
		delete(ev, "event")
		ev["attachments"] = list
		if err := c.deliver(ctx, cli, converted{name: "message", ev: ev, atts: atts, hashes: hashes}, true, false); err == nil {
			found++
		}
	}
	return M{"found": found}, nil
}

// discover: which numbers have a Signal account (Signal's contact discovery): `{found: [{number,
// aci, pni}]}` (one of the ids; the ACI where Signal gives it), each also an `ids` event, which
// ties the account to the number.
func (c *client) discover(ctx context.Context, numbers []string) (any, error) {
	cli, err := c.online(ctx)
	if err != nil {
		return nil, err
	}
	var e164s []uint64
	for _, n := range numbers {
		if v, err := strconv.ParseUint(strings.TrimPrefix(n, "+"), 10, 64); strings.HasPrefix(n, "+") && err == nil {
			e164s = append(e164s, v)
		}
	}
	found := []M{}
	if len(e164s) == 0 {
		return M{"found": found}, nil
	}
	got, err := cli.LookupPhone(ctx, e164s...)
	if err != nil {
		return nil, err
	}
	for _, n := range e164s {
		entry, ok := got[n]
		if !ok {
			continue
		}
		number := "+" + strconv.FormatUint(n, 10)
		var aci, pni any
		switch {
		case entry.ACI != uuid.Nil:
			aci = entry.ACI.String()
		case entry.PNI != uuid.Nil:
			pni = libsignalgo.NewPNIServiceID(entry.PNI).String()
		default:
			continue
		}
		c.ev.say("ids", M{"aci": aci, "pni": pni, "phone": number})
		found = append(found, M{"number": number, "aci": aci, "pni": pni})
	}
	return M{"found": found}, nil
}

func (c *client) Settle()               { c.ev.Settle() }
func (c *client) Done() <-chan struct{} { return c.stopped }

// Close ends the client: the receiving stops after the envelope in hand (one not acknowledged comes
// again), what runs in the background ends (for half a minute at most), the events said are handled.
func (c *client) Close() {
	c.end(30 * time.Second)
}

// Kill ends the client at once.
func (c *client) Kill() { c.end(0) }

func (c *client) end(wait time.Duration) {
	c.once.Do(func() {
		c.cancel()
		cli := c.signal()
		c.mu.Lock()
		receiving, ended, stop := c.receiving, c.ended, c.stopLoops
		c.mu.Unlock()
		if cli != nil && receiving {
			c.endReceiving(cli, ended, nil, "")
		}
		if stop != nil {
			// the connections closed: what was in hand is not acknowledged, and comes again (waited for
			// a while: signalmeow's loops may hang on a full channel)
			stopped := make(chan struct{})
			go func() { stop(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(wait):
			}
		}
		done := make(chan struct{})
		go func() { c.work.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(wait):
		}
		c.ev.end()
		select {
		case <-c.ev.done:
		case <-time.After(wait):
		}
		c.pool.Close()
		close(c.stopped)
	})
}
