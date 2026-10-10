package meta

// The live connection: messagix (Messenger) or instameow (Instagram) connected to Meta with the
// account's cookies, what they say written into the archive as it comes (one thing at a time, in
// order), Messenger's encrypted chats through whatsmeow beside it, the files downloaded as they
// arrive, and each chat's earlier history brought back to what the archive has, where the user
// wants it. What the session needs to connect again (the cookies as Meta renews them, the client's
// state) goes back to keys.db as it changes.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/dgw"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	tps "go.mau.fi/mautrix-meta/pkg/messagix/types"

	"everysaid/internal/errs"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
)

var (
	sessionsMu sync.Mutex
	sessions   = map[int64]*liveSession{}
)

// running is an instance's live session (nil: not connected).
func running(iid int64) *liveSession {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	return sessions[iid]
}

// liveSession is one instance's connection.
type liveSession struct {
	c            *plugins.Context
	service      string
	cli          client   // Messenger's
	ig           igClient // Instagram's
	sess         *session
	self         string
	selfName     string
	selfUsername string
	tr           *translator
	names        map[string]importers.MetaUser
	igCursor     string // Instagram: where the inbox's second page starts

	mu      sync.Mutex
	threads map[string]threadInfo
	e2ee    *whatsmeow.Client

	sessMu    sync.Mutex // s.sess, saved, loggedOut
	loggedOut bool

	events    chan item
	fatal     chan error
	wake      chan struct{}
	history   chan rangeOp
	igHistory chan igHistoryOp
	saved     time.Time
	connected atomic.Bool
}

// item is something to write, in order: a table, an encrypted chat's event or an Instagram change,
// or what was read already; done hears how it went (a message is acknowledged only once it is in
// the archive).
type item struct {
	tbl     *table.LSTable
	initial bool
	wa      any
	ops     *ops
	done    chan error
}

// newTranslator reads for an account.
func newTranslator(self string) *translator {
	return &translator{self: self, fbToWA: map[string]string{}, igKeys: map[string]string{}}
}

// newSession is a session of an instance, with what keys.db knows of its threads.
func newSession(c *plugins.Context, service string, sess *session, threads map[string]threadInfo) *liveSession {
	s := &liveSession{c: c, service: service, sess: sess, self: sess.UserID, selfName: sess.Name,
		names: map[string]importers.MetaUser{}, threads: threads, events: make(chan item, 64), fatal: make(chan error, 1),
		wake: make(chan struct{}, 1), history: make(chan rangeOp, 256), igHistory: make(chan igHistoryOp, 256),
		tr: newTranslator(sess.UserID)}
	for k, t := range threads {
		if t.FBKey != "" {
			s.tr.fbToWA[t.FBKey] = k
		}
		if t.IGID != "" {
			s.tr.igKeys[t.IGID] = k
		}
	}
	return s
}

func register(s *liveSession) {
	sessionsMu.Lock()
	sessions[s.c.ID] = s
	sessionsMu.Unlock()
}

func unregister(s *liveSession) {
	sessionsMu.Lock()
	if sessions[s.c.ID] == s {
		delete(sessions, s.c.ID)
	}
	sessionsMu.Unlock()
}

// fail ends the connection with an error.
func (s *liveSession) fail(err error) {
	select {
	case s.fatal <- err:
	default:
	}
}

// loop writes what comes, in order, until ctx ends or the connection fails.
func (s *liveSession) loop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-s.fatal:
			return err
		case it := <-s.events:
			err := s.handle(ctx, it)
			if it.done != nil {
				it.done <- err
			}
		}
	}
}

func (s *liveSession) label() string {
	if s.service == "instagram" {
		return "Instagram"
	}
	return "Messenger"
}

func (s *liveSession) selfUser() importers.MetaUser {
	return importers.MetaUser{ID: s.self, Name: s.selfName, Username: s.selfUsername}
}

func (s *liveSession) waClient() *whatsmeow.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.e2ee
}

func (s *liveSession) thread(key string) threadInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threads[key]
}

// noteThread keeps what was learned of a thread (in keys.db, where it changed).
func (s *liveSession) noteThread(key string, t threadInfo) {
	s.mu.Lock()
	old, ok := s.threads[key]
	if t.FBKey == "" {
		t.FBKey = old.FBKey
	}
	if t.IGID == "" {
		t.IGID = old.IGID
	}
	if t.IGThread == "" {
		t.IGThread = old.IGThread
	}
	if t.IGID != "" {
		s.tr.igKeys[t.IGID] = key
	}
	if t.Type == 0 || isEncrypted(old.Type) && !isEncrypted(t.Type) {
		t.Type = old.Type // an encrypted chat stays one (Meta's tables may still call it by its old type)
	}
	changed := !ok || old != t
	s.threads[key] = t
	if t.FBKey != "" {
		s.tr.fbToWA[t.FBKey] = key
	}
	s.mu.Unlock()
	if changed {
		if err := saveThread(s.c.ID, key, t); err != nil {
			s.c.Logf("keys.db: %v", err)
		}
	}
}

// isEncrypted says whether a thread type is of an end-to-end encrypted chat.
func isEncrypted(typ int64) bool {
	return typ == int64(table.ENCRYPTED_OVER_WA_ONE_TO_ONE) || typ == int64(table.ENCRYPTED_OVER_WA_GROUP)
}

// cookiesOf is a session's cookies as messagix takes them.
func cookiesOf(sess *session) *cookies.Cookies {
	ck := &cookies.Cookies{Platform: tps.PlatformFromString(sess.Platform)}
	json.Unmarshal(sess.Cookies, ck)
	return ck
}

// loggedIn says whether a session can connect (it has the cookies that are the login).
func loggedIn(sess *session) bool {
	return sess != nil && cookiesOf(sess).IsLoggedIn()
}

// errLoggedOut: Meta no longer takes the cookies.
var (
	errLoggedOut   = errs.Plugin("Logged out of Messenger: log in again (paste the cookies)", 409)
	errLoggedOutIG = errs.Plugin("Logged out of Instagram: log in again (paste the cookies)", 409)
)

// errAnotherAccount: the cookies are not of the account the source has.
var errAnotherAccount = errs.Plugin("The cookies are of another account than this source's", 409)

// errCheck: Meta asks for a check on its site first.
var errCheck = errs.Plugin("Meta asks for a check on its site: open facebook.com in the browser, then try again", 409)

// loggedOutError is a service's "logged out".
func loggedOutError(service string) error {
	if service == "instagram" {
		return errLoggedOutIG
	}
	return errLoggedOut
}

// connectError is what a failure to load the messages page means for the user (iid 0: a login,
// with no session to clear).
func connectError(err error, service string, sess *session, iid int64) error {
	switch {
	case errors.Is(err, httpclient.ErrTokenInvalidated):
		if iid != 0 {
			sess.Cookies, sess.State = json.RawMessage("{}"), nil // logged out: the device's keys stay
			saveSession(iid, sess)
		}
		return loggedOutError(service)
	case errors.Is(err, httpclient.ErrChallengeRequired), errors.Is(err, httpclient.ErrCheckpointRequired),
		errors.Is(err, httpclient.ErrConsentRequired), errors.Is(err, httpclient.ErrAccountSuspended):
		return errCheck
	}
	return err
}

// live is the connection of an instance, until ctx ends or it fails.
func live(ctx context.Context, c *plugins.Context, service string) error {
	sess, err := loadSession(c.ID)
	if err != nil {
		return err
	}
	if !loggedIn(sess) {
		return errLoggedOut
	}
	threads, err := loadThreads(c.ID)
	if err != nil {
		return err
	}
	logger, closer := libraryLog(c.ID)
	defer closer.Close()
	s := newSession(c, service, sess, threads)
	s.cli = newClient(cookiesOf(sess), logger)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cli.SetEventHandler(func(_ context.Context, evt any) { s.onEvent(ctx, evt) })
	var initial *table.LSTable
	if len(sess.State) == 0 || s.cli.LoadState(sess.State) != nil {
		user, tbl, err := s.cli.LoadMessagesPage(ctx)
		if err != nil {
			return connectError(err, service, sess, c.ID)
		}
		if id := strconv.FormatInt(user.GetFBID(), 10); id != sess.UserID {
			return errAnotherAccount
		}
		s.selfName = user.GetName()
		initial = tbl
	}
	register(s)
	defer func() {
		unregister(s)
		s.cli.SetEventHandler(nil)
		s.cli.Disconnect()
		if e := s.waClient(); e != nil {
			e.Disconnect()
		}
		s.save(true)
	}()
	if initial != nil {
		s.handle(ctx, item{tbl: initial, initial: true})
	}
	if err := s.cli.Connect(ctx); err != nil {
		s.sessMu.Lock()
		s.sess.State = nil // what it connected with may be what failed: the page next time
		s.sessMu.Unlock()
		return err
	}
	c.Log("{service}: connected as {name}", map[string]any{"service": s.label(), "name": s.selfName})
	go s.downloader(ctx)
	go s.historyLoop(ctx)
	s.wakeDownloader()
	return s.loop(ctx)
}

// onEvent is what messagix says, from its own goroutines.
func (s *liveSession) onEvent(ctx context.Context, evt any) {
	switch e := evt.(type) {
	case *table.LSTable:
		s.push(ctx, item{tbl: e})
	case *messagix.ConnectedEvent:
		s.connected.Store(true)
		go s.connectE2EE(ctx)
		go s.threadBackfill(ctx)
	case *messagix.ReconnectedEvent:
		s.connected.Store(true)
		if s.waClient() == nil {
			go s.connectE2EE(ctx)
		}
	case *messagix.TransientDisconnectEvent:
		s.connected.Store(false)
	case *messagix.PermanentErrorEvent:
		s.connected.Store(false)
		err := e.Err
		if websocket.CloseStatus(e.Err) == dgw.CloseStatusUnauthorized {
			s.logOut()
			err = errLoggedOut
		}
		s.fail(err)
	}
}

func (s *liveSession) push(ctx context.Context, it item) {
	select {
	case s.events <- it:
	case <-ctx.Done():
	}
}

// handle writes an item into the archive.
func (s *liveSession) handle(ctx context.Context, it item) error {
	o := &ops{}
	switch {
	case it.ops != nil:
		o = it.ops
	case it.tbl != nil:
		o = s.tr.translate(it.tbl)
	case it.wa != nil:
		switch e := it.wa.(type) {
		case *events.FBMessage:
			s.tr.waMessage(o, e)
		case *events.Receipt:
			s.tr.waReceipt(o, e)
		default:
			var err error
			if o, err = s.handleIG(ctx, e); err != nil {
				return err
			}
		}
	}
	res, err := s.apply(o)
	if err != nil {
		s.c.Log("{service}: not written into the archive ({e})", map[string]any{"service": s.label(), "e": err.Error()})
		return err
	}
	if len(res.files) > 0 && s.c.Bool("media") {
		if err := queueFiles(s.c.ID, res.files); err != nil {
			s.c.Logf("keys.db: %v", err)
		}
		s.wakeDownloader()
	}
	if s.c.Bool("history") {
		for _, r := range o.ranges {
			if r.HasMoreBefore && res.allNew(r.IDs) && r.MinID != "" {
				select {
				case s.history <- r:
				default:
				}
			}
		}
		for _, h := range o.igHistory {
			if h.HasMore && h.IGID != "" && h.Cursor != "" && res.allNew(h.IDs) {
				select {
				case s.igHistory <- h:
				default:
				}
			}
		}
	}
	if it.tbl != nil && !it.initial {
		s.cli.PostHandlePublishResponse(it.tbl)
	}
	s.save(false)
	return nil
}

// save keeps the cookies (Meta renews them) and messagix's state, at most once a minute unless now.
func (s *liveSession) save(now bool) {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	if s.loggedOut || !now && time.Since(s.saved) < time.Minute {
		return
	}
	s.saved = time.Now()
	jar, dump := s.cookieJar, s.dumpState
	if ck, err := json.Marshal(jar()); err == nil && cookiesOf(&session{Platform: s.sess.Platform, Cookies: ck}).IsLoggedIn() {
		s.sess.Cookies = ck
	}
	if st, err := dump(); err == nil && len(st) > 0 {
		s.sess.State = st
	}
	s.sess.Name = s.selfName
	if err := saveSession(s.c.ID, s.sess); err != nil {
		s.c.Logf("keys.db: %v", err)
	}
}

// cookieJar, dumpState: the client's cookies as they are now, its state.
func (s *liveSession) cookieJar() *cookies.Cookies {
	if s.ig != nil {
		return s.ig.GetCookies()
	}
	return s.cli.GetCookies()
}

func (s *liveSession) dumpState() (json.RawMessage, error) {
	if s.ig != nil {
		return s.ig.DumpState()
	}
	return s.cli.DumpState()
}

// logOut: Meta no longer takes the cookies. They go (the encryption device's keys stay, for when the
// user logs in again), and nothing renews them.
func (s *liveSession) logOut() {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	s.loggedOut = true
	s.sess.Cookies, s.sess.State = json.RawMessage("{}"), nil
	if err := saveSession(s.c.ID, s.sess); err != nil {
		s.c.Logf("keys.db: %v", err)
	}
}

// device is the session's encryption device number (0: none yet); setDevice keeps a new one.
func (s *liveSession) device() uint16 {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	return s.sess.WADevice
}

func (s *liveSession) setDevice(n uint16) error {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	s.sess.WADevice = n
	return saveSession(s.c.ID, s.sess)
}

// historyPause is the pause between two requests for a chat's earlier messages.
var historyPause = 2 * time.Second

// historyLoop asks for each chat's earlier messages, one page at a time, while each page brought
// only messages new to the archive.
func (s *liveSession) historyLoop(ctx context.Context) {
	for {
		var r rangeOp
		select {
		case <-ctx.Done():
			return
		case r = <-s.history:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(historyPause):
		}
		resp, err := s.cli.ExecuteTasks(ctx, &socket.FetchMessagesTask{ThreadKey: r.FBKey, Direction: 0,
			ReferenceTimestampMs: r.MinTS, ReferenceMessageId: r.MinID, SyncGroup: 1, Cursor: s.cli.GetCursor(1)})
		if err != nil {
			s.c.Log("{service}: earlier messages not brought ({e})", map[string]any{"service": s.label(), "e": err.Error()})
			continue
		}
		if resp != nil {
			s.push(ctx, item{tbl: resp, initial: true})
		}
	}
}

// threadBackfill asks once for every thread past the first page (the account's whole list).
func (s *liveSession) threadBackfill(ctx context.Context) {
	if !s.c.Bool("history") || s.c.State["threads_listed"] == true {
		return
	}
	var prev int64
	for ctx.Err() == nil {
		ks, tbl, err := s.cli.FetchMoreThreads(ctx, 1)
		if err != nil {
			s.c.Log("{service}: the list of chats not brought ({e})", map[string]any{"service": s.label(), "e": err.Error()})
			return
		}
		if tbl != nil {
			s.push(ctx, item{tbl: tbl, initial: true})
		}
		if tbl == nil || ks == nil || !ks.HasMoreBefore || ks.MinThreadKey == prev {
			break
		}
		prev = ks.MinThreadKey
		select {
		case <-ctx.Done():
			return
		case <-time.After(historyPause):
		}
	}
	if ctx.Err() == nil {
		s.c.SaveState(plugins.M{"threads_listed": true})
	}
}

// connectE2EE connects the encrypted chats, with the account's device (registered once).
func (s *liveSession) connectE2EE(ctx context.Context) {
	if s.waClient() != nil {
		return
	}
	if err := s.e2eeConnect(ctx); err != nil && ctx.Err() == nil {
		s.c.Log("{service}: the encrypted chats are not connected ({e})", map[string]any{"service": s.label(), "e": err.Error()})
	}
}

func (s *liveSession) e2eeConnect(ctx context.Context) error {
	container, err := devices(ctx)
	if err != nil {
		return err
	}
	var dev *store.Device
	if n := s.device(); n != 0 {
		dev, err = container.GetDevice(ctx, types.JID{User: s.self, Device: n, Server: types.MessengerServer})
		if err != nil {
			return err
		}
	}
	register := dev == nil
	if register {
		dev = container.NewDevice()
	}
	e, err := s.cli.E2EE(ctx, dev, fbid(s.self), register)
	if err != nil {
		return err
	}
	if register {
		if err := dev.Save(ctx); err != nil {
			return err
		}
		if err := s.setDevice(dev.ID.Device); err != nil {
			return err
		}
	}
	e.SynchronousAck = true
	e.EnableDecryptedEventBuffer = true
	e.AddEventHandlerWithSuccessStatus(func(evt any) bool { return s.onWA(ctx, evt) })
	s.mu.Lock()
	s.e2ee = e
	s.mu.Unlock()
	return e.Connect()
}

// onWA is what the encrypted chats say; a message is acknowledged once it is written.
func (s *liveSession) onWA(ctx context.Context, evt any) bool {
	switch evt.(type) {
	case *events.FBMessage, *events.Receipt:
		done := make(chan error, 1)
		s.push(ctx, item{wa: evt, done: done})
		select {
		case err := <-done:
			return err == nil
		case <-ctx.Done():
			return false
		}
	case *events.LoggedOut:
		s.mu.Lock()
		s.e2ee = nil
		s.mu.Unlock()
		s.c.Log("{service}: the encrypted chats' device was logged out", map[string]any{"service": s.label()})
	}
	return true
}
