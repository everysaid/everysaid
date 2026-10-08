// Ports everysaid/plugins/telegram_live.py: the user's account connected while the app runs.
//
// On connecting it brings what arrived while it was not connected, and each chat's state
// (archived, pinned, muted; then live, as it changes on any of the user's devices); then new
// messages (incoming, and outgoing from any of the user's devices) and edits, pushed by Telegram as
// they happen, are written to telegram.db as the sync writes them, then imported into the archive
// at once. Messages sent from the app go through the same client and the same path. Channels and
// bots stay out.
package telegram

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/tdp"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

const (
	notSignedIn = "Not signed in to Telegram yet (everysaid telegram-sync --save-credentials, --login)"
	expired     = "The Telegram sign-in has expired: everysaid telegram-sync --login"
	// the live connection waits for telegram-sync run by hand (or another server) to finish
	waitingOther = "waiting: Telegram is connected by another Everysaid (telegram-sync run by hand?)"
)

// connect runs fn with a connected, signed-in client, offered to the others meanwhile (the caller
// holds the gate). handler: updates (nil: none); tweak: more options.
func connect(ctx context.Context, handler *updates.Manager, fn func(ctx context.Context, cn *conn, self *tg.User) error,
	tweak ...func(*telegram.Options)) error {
	if _, _, ok := credentials(); !ok || !hasSession() {
		return pluginErr(notSignedIn)
	}
	var h telegram.UpdateHandler
	if handler != nil {
		h = handler
	}
	client, err := newClient(60*time.Second, &keyringSession{}, h, tweak...)
	if err != nil {
		return err
	}
	return client.Run(ctx, func(ctx context.Context) error {
		ok, err := authorized(ctx, client)
		if err != nil {
			return err
		}
		if !ok {
			return pluginErr(expired)
		}
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		cn := newConn(client.API())
		defer one.share(cn)()
		return fn(ctx, cn, self)
	})
}

// withConn runs fn on the connection open in this process (the live one, or a sync's), else on
// one made for it (with the dialogs read, for the access hashes a session does not keep).
func withConn(ctx context.Context, fn func(ctx context.Context, cn *conn) error) error {
	open, done, err := one.take(ctx, true, nil)
	if errors.Is(err, errHeld) {
		return pluginErr(heldElsewhere)
	}
	if err != nil {
		return err
	}
	defer done()
	if open != nil {
		return fn(ctx, open)
	}
	return connect(ctx, nil, func(ctx context.Context, cn *conn, _ *tg.User) error {
		if _, err := cn.dialogs(ctx); err != nil {
			return err
		}
		return fn(ctx, cn)
	})
}

// --- writing what arrives --------------------------------------------------------------------------

// currentSettings are the instance's settings as they are now: the choice may have changed.
func currentSettings(c *plugins.Context) M {
	if row := plugins.GetInstance(c.Store(), c.ID); row != nil {
		var s M
		if json.Unmarshal([]byte(row.Settings), &s) == nil && s != nil {
			return s
		}
	}
	return c.Settings
}

// storeMessages writes messages of one chat to telegram.db and imports them; it returns how many.
func storeMessages(c *plugins.Context, chat any, msgs []sent) (n int, err error) {
	defer db.Recover(&err)
	kind := EntityKind(chat)
	if kind == "channel" || kind == "bot" {
		return 0, nil
	}
	chatID := EntityID(chat)
	store, err := openStore(DBPath())
	if err != nil {
		return 0, err
	}
	keys := map[[2]int64]bool{}
	err = func() error {
		defer store.Close()
		tx, err := store.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		db.Exec(tx, "INSERT INTO chat (id, kind, title, archived, json) VALUES (?, ?, ?, 0, ?) "+
			"ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, title = excluded.title, json = excluded.json",
			chatID, kind, DisplayName(chat), Dump(chat.(tdp.Object)))
		for _, s := range msgs {
			db.Exec(tx, "INSERT INTO message (chat_id, id, date, json) VALUES (?, ?, ?, ?) "+
				"ON CONFLICT (chat_id, id) DO UPDATE SET json = excluded.json",
				chatID, s.Message.GetID(), messageDate(s.Message), Dump(s.Message.(tdp.Object)))
			if o, ok := s.Sender.(tdp.Object); ok && s.Sender != nil {
				db.Exec(tx, "INSERT OR REPLACE INTO entity VALUES (?, ?)", EntityID(s.Sender), Dump(o))
			}
			keys[[2]int64{chatID, int64(s.Message.GetID())}] = true
		}
		db.Exec(tx, "UPDATE chat SET synced_at = ? WHERE id = ?", time.Now().Unix(), chatID)
		return tx.Commit()
	}()
	if err != nil {
		return 0, err
	}
	if idSet(currentSettings(c)["skip_chats"])[chatID] {
		return 0, nil // kept in telegram.db, not imported: the user left this chat out
	}
	_, _, err = sourcekit.RunImporters(c, []sourcekit.Step{{Label: "Telegram live", Run: func(a *archive.Archive, out func(string)) error {
		return importTelegram(a, out, keys, nil)
	}}})
	return len(keys), err
}

// noteDeleted records messages deleted on Telegram in telegram.db and has the archive mark them.
// chat 0: ids of the account's common box (private chats and small groups), unique across them, so
// each one's chat is the stored row's. Messages never stored (a channel's, a bot's) are passed by.
func noteDeleted(c *plugins.Context, chat int64, ids []int) (err error) {
	defer db.Recover(&err)
	if len(ids) == 0 {
		return nil
	}
	store, err := openStore(DBPath())
	if err != nil {
		return err
	}
	keys := map[[2]int64]bool{}
	err = func() error {
		defer store.Close()
		tx, err := store.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		query, args := "SELECT chat_id, id FROM message WHERE chat_id > ? AND id IN ("+db.Marks(len(ids))+")",
			append([]any{-channelMark}, db.Args(ids)...)
		if chat != 0 {
			query, args = "SELECT chat_id, id FROM message WHERE chat_id = ? AND id IN ("+db.Marks(len(ids))+")",
				append([]any{chat}, db.Args(ids)...)
		}
		db.Each(tx, query, args, func(scan func(...any)) {
			var k [2]int64
			scan(&k[0], &k[1])
			keys[k] = true
		})
		now := time.Now().Unix()
		for k := range keys {
			db.Exec(tx, "INSERT OR IGNORE INTO deleted (chat_id, id, at) VALUES (?, ?, ?)", k[0], k[1], now)
		}
		return tx.Commit()
	}()
	skip := idSet(currentSettings(c)["skip_chats"])
	for k := range keys {
		if skip[k[0]] {
			delete(keys, k) // kept in telegram.db, not imported: the user left this chat out
		}
	}
	if err != nil || len(keys) == 0 {
		return err
	}
	_, _, err = sourcekit.RunImporters(c, []sourcekit.Step{{Label: "Telegram live", Run: func(a *archive.Archive, out func(string)) error {
		return importTelegram(a, out, keys, nil)
	}}})
	return err
}

// untilMS is Telegram's mute_until as Unix ms: 0 not muted, -1 for ever (past the year 3000).
func untilMS(s tg.PeerNotifySettings) int64 {
	until, ok := s.GetMuteUntil()
	if !ok {
		return 0
	}
	if time.Unix(int64(until), 0).UTC().Year() >= 3000 {
		return -1
	}
	return max(int64(until)*1000, 0)
}

type stateItem struct {
	chat  int64
	field string
	value int64
}

// reportStates puts what Telegram says now of chats into the archive's state reports.
func reportStates(c *plugins.Context, items []stateItem) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	err := withArchive(c, func(a *archive.Archive) error {
		src := source(a, c.ID)
		for _, it := range items {
			conv, _ := a.FindConversation("telegram", strconv.FormatInt(it.chat, 10))
			a.ReportState(src, conv, it.field, it.value, now, 0)
		}
		a.InitArchived()
		return nil
	})
	if err != nil {
		return err
	}
	c.Emit(M{"type": "changed"})
	return nil
}

type readItem struct {
	chat          int64
	inbox, outbox *int64 // nil: not said
}

// noteReads records how far chats were read into telegram.db, then, for the chats where either
// moved on, into the archive (their read_until, and receipts of the owner's messages).
func noteReads(c *plugins.Context, items []readItem, seenNow bool) (err error) {
	defer db.Recover(&err)
	store, err := openStore(DBPath())
	if err != nil {
		return err
	}
	moved := map[int64]bool{}
	err = func() error {
		defer store.Close()
		tx, err := store.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		known := map[int64]bool{} // channels and bots are not
		for _, id := range db.Ints(tx, "SELECT id FROM chat") {
			known[id] = true
		}
		for _, it := range items {
			if known[it.chat] && noteRead(tx, it.chat, it.inbox, it.outbox, seenNow) {
				moved[it.chat] = true
			}
		}
		return tx.Commit()
	}()
	if err != nil || len(moved) == 0 {
		return err
	}
	if err := withArchive(c, func(a *archive.Archive) error {
		source(a, c.ID)
		return importReads(a, moved)
	}); err != nil {
		return err
	}
	c.Emit(M{"type": "changed"})
	return nil
}

func dialogReads(dialogs []dialog) []readItem {
	var out []readItem
	for _, d := range dialogs {
		if k := EntityKind(d.Entity); k != "channel" && k != "bot" {
			out = append(out, readItem{d.ID, intp(d.Dialog.ReadInboxMaxID), intp(d.Dialog.ReadOutboxMaxID)})
		}
	}
	return out
}

func dialogStates(dialogs []dialog) []stateItem {
	var out []stateItem
	for _, d := range dialogs {
		if k := EntityKind(d.Entity); k == "channel" || k == "bot" {
			continue
		}
		out = append(out, stateItem{d.ID, "archived", int64(boolInt(d.Archived))}, stateItem{d.ID, "pinned", int64(boolInt(d.Pinned))},
			stateItem{d.ID, "muted", untilMS(d.Dialog.NotifySettings)})
	}
	return out
}

// catchUp brings what arrived while the app was not connected: each chat's messages after the
// newest that telegram.db has (a chat new since then, all of it). It gives the groups whose members
// changed meanwhile, as their service messages say.
func catchUp(ctx context.Context, c *plugins.Context, cn *conn, dialogs []dialog) (changed []int64, err error) {
	have := map[int64]int64{}
	if err := func() (err error) {
		defer db.Recover(&err)
		store, err := openStore(DBPath())
		if err != nil {
			return err
		}
		defer store.Close()
		db.Each(store, "SELECT chat_id, max(id) FROM message GROUP BY chat_id", nil, func(scan func(...any)) {
			var chat, id int64
			scan(&chat, &id)
			have[chat] = id
		})
		return nil
	}(); err != nil {
		return nil, err
	}
	total, chats := 0, 0
	for _, d := range dialogs {
		if k := EntityKind(d.Entity); k == "channel" || k == "bot" {
			continue
		}
		since := have[d.ID]
		if d.Message == nil || int64(d.Message.GetID()) <= since {
			continue
		}
		peer, err := dialogPeer(d)
		if err != nil {
			return nil, err
		}
		var batch []sent
		if err := cn.history(ctx, peer, int(since), func(chunk []sent) error {
			batch = append(batch, chunk...)
			return nil
		}); err != nil {
			return nil, err
		}
		if len(batch) > 0 {
			if _, err := storeMessages(c, d.Entity, batch); err != nil {
				return nil, err
			}
			total += len(batch)
			chats++
		}
		for _, s := range batch {
			if changesMembers(s.Message) {
				changed = append(changed, d.ID)
				break
			}
		}
	}
	if total > 0 {
		c.Log("caught up: {n} new messages in {chats} chats", map[string]any{"n": total, "chats": chats})
	} else {
		c.Log("caught up: nothing new", nil)
	}
	return changed, nil
}

// --- the live connection ---------------------------------------------------------------------------

// lookup is an entity by marked id: from an update's, else from those seen.
func lookup(e tg.Entities, cn *conn, id int64) any {
	switch {
	case id > 0:
		if u, ok := e.Users[id]; ok {
			return u
		}
	case id <= -channelMark:
		if ch, ok := e.Channels[-id-channelMark]; ok {
			return ch
		}
	case id < 0:
		if ch, ok := e.Chats[-id]; ok {
			return ch
		}
	}
	if x := cn.entity(id); x != nil {
		return x
	}
	return nil
}

func chatLabel(chat any) any {
	switch x := chat.(type) {
	case *tg.User:
		if x.FirstName != "" {
			return x.FirstName
		}
	default:
		if t := DisplayName(chat); t != "" {
			return t
		}
	}
	return EntityID(chat)
}

// liveConn is the live connection while it runs (what FetchMedia uses).
var liveConn atomic.Pointer[conn]

func live(ctx context.Context, c *plugins.Context) error {
	// a client of its own, for its updates; after the one another process may have open
	_, release, err := one.take(ctx, false, func() { c.Log(waitingOther, nil) })
	if err != nil {
		if ctx.Err() != nil {
			return nil // the server's end
		}
		return err
	}
	defer release()
	d := tg.NewUpdateDispatcher()
	// a gap Telegram cannot fill with updates (too long an absence) is read again from the chats
	again := make(chan struct{}, 1)
	resync := func() {
		select {
		case again <- struct{}{}:
		default:
		}
	}
	mgr := updates.New(updates.Config{Handler: d, Storage: newFileState(StatePath()),
		OnTooLong: resync, OnChannelTooLong: func(int64) { resync() }})
	var readies atomic.Int32
	reconnected := make(chan struct{}, 1)
	onState := func(o *telegram.Options) {
		o.OnConnectionState = func(s telegram.ConnectionState) {
			if s == telegram.ConnectionStateReady && readies.Add(1) > 1 {
				select {
				case reconnected <- struct{}{}:
				default:
				}
			}
		}
	}
	err = connect(ctx, mgr, func(ctx context.Context, cn *conn, self *tg.User) error {
		dialogs, err := cn.dialogs(ctx) // the access hashes of every chat (a session keeps none)
		if err != nil {
			return err
		}
		c.Log("connected to Telegram", nil)
		ctx, stop := context.WithCancel(ctx)
		defer stop()
		queue := newMemberQueue()
		go queue.run(ctx, c, cn)
		handlers(c, cn, d, queue.add)
		liveConn.Store(cn)
		defer liveConn.Store(nil)
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- mgr.Run(ctx, cn.api, self.ID, updates.AuthOptions{OnStart: func(context.Context) { close(started) }})
		}()
		select {
		case <-started:
		case err := <-done:
			return err
		}
		if err := settle(ctx, c, cn, dialogs); err != nil { // after the handlers: nothing falls between the two
			return err
		}
		return follow(ctx, c, cn, mgr, done, reconnected, again)
	}, onState)
	if ctx.Err() != nil {
		return nil
	}
	if d, ok := tgerr.AsFloodWait(err); ok { // trying again sooner would only be refused again
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil
		}
	}
	return err
}

// settle brings the chats' states, how far they were read, what arrived while not connected, and
// the members of the groups where they changed meanwhile or were not asked for a while.
func settle(ctx context.Context, c *plugins.Context, cn *conn, dialogs []dialog) error {
	if err := reportStates(c, dialogStates(dialogs)); err != nil {
		return err
	}
	if err := noteReads(c, dialogReads(dialogs), false); err != nil {
		return err
	}
	if err := noteBlocked(ctx, c, cn); err != nil { // a help for the user, not what the connection needs
		c.Log("the blocked people could not be read: {e}", map[string]any{"e": err.Error()})
	}
	changed, err := catchUp(ctx, c, cn, dialogs)
	if err != nil {
		return err
	}
	stale, err := staleMembers(dialogs)
	if err != nil {
		return err
	}
	for _, id := range changed {
		if !slices.Contains(stale, id) {
			stale = append(stale, id)
		}
	}
	return refreshMembers(ctx, c, cn, stale)
}

// follow keeps the connection up to date until it ends (done): back after the connection dropped,
// it asks Telegram for what came meanwhile, as Telegram Desktop does (else that waits for the next
// update, or a quarter of an hour); when Telegram says the gap is too long to fill (again), it reads
// the chats again as on connecting.
func follow(ctx context.Context, c *plugins.Context, cn *conn, mgr telegram.UpdateHandler, done <-chan error,
	reconnected, again <-chan struct{}) error {
	for {
		select {
		case err := <-done:
			return err
		case <-reconnected:
			if err := mgr.Handle(ctx, &tg.UpdatesTooLong{}); err != nil {
				c.Log("error: {e}", map[string]any{"e": err.Error()})
			}
		case <-again:
			dialogs, err := cn.dialogs(ctx)
			if err == nil {
				err = settle(ctx, c, cn, dialogs)
			}
			if err != nil {
				return err
			}
		}
	}
}

// handlers are what the connection does with what Telegram pushes. One bad update must not stop
// the connection: its error is logged. members is told of a group whose members changed.
func handlers(c *plugins.Context, cn *conn, d tg.UpdateDispatcher, members func(chat int64)) {
	logged := func(err error) error {
		if err != nil {
			c.Log("error: {e}", map[string]any{"e": err.Error()})
		}
		return nil
	}
	onMessage := func(ctx context.Context, e tg.Entities, m tg.MessageClass) error {
		// service messages too (calls, a group's events): Telethon's NewMessage left them out, and
		// the catch-up, starting after the newest message stored, never brought them afterwards
		peer := messagePeer(m)
		if _, empty := m.(*tg.MessageEmpty); empty || peer == nil {
			return nil
		}
		cn.seen(entityLists(e))
		chatID := PeerID(peer)
		chat := lookup(e, cn, chatID)
		if chat == nil { // a chat not met yet: the dialogs again
			if _, err := cn.dialogs(ctx); err != nil {
				return logged(err)
			}
			if chat = cn.entity(chatID); chat == nil {
				return logged(fmt.Errorf("unknown chat %d", chatID))
			}
		}
		var sender any
		if id := senderID(m); id != 0 {
			sender = lookup(e, cn, id)
		}
		n, err := storeMessages(c, chat, []sent{{m, sender}})
		if err != nil {
			return logged(err)
		}
		if changesMembers(m) {
			members(chatID)
		}
		if n > 0 {
			c.Log("new message in {chat}", map[string]any{"chat": chatLabel(chat)})
		}
		return nil
	}
	d.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		return onMessage(ctx, e, u.Message)
	})
	d.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		return onMessage(ctx, e, u.Message)
	})
	d.OnEditMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateEditMessage) error {
		return onMessage(ctx, e, u.Message)
	})
	d.OnEditChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateEditChannelMessage) error {
		return onMessage(ctx, e, u.Message)
	})

	// deleted on any device, by the user or (for everyone) by the others: kept, said deleted
	d.OnDeleteMessages(func(ctx context.Context, e tg.Entities, u *tg.UpdateDeleteMessages) error {
		return logged(noteDeleted(c, 0, u.Messages))
	})
	d.OnDeleteChannelMessages(func(ctx context.Context, e tg.Entities, u *tg.UpdateDeleteChannelMessages) error {
		return logged(noteDeleted(c, PeerID(&tg.PeerChannel{ChannelID: u.ChannelID}), u.Messages))
	})

	// someone blocked, or no longer, on any of the user's devices
	d.OnPeerBlocked(func(ctx context.Context, e tg.Entities, u *tg.UpdatePeerBlocked) error {
		return logged(peerBlocked(c, u))
	})

	// archived, pinned or muted on any of the user's devices
	d.OnFolderPeers(func(ctx context.Context, e tg.Entities, u *tg.UpdateFolderPeers) error {
		var items []stateItem
		for _, fp := range u.FolderPeers {
			items = append(items, stateItem{PeerID(fp.Peer), "archived", int64(boolInt(fp.FolderID == 1))})
		}
		return logged(reportStates(c, items))
	})
	d.OnDialogPinned(func(ctx context.Context, e tg.Entities, u *tg.UpdateDialogPinned) error {
		p, ok := u.Peer.(*tg.DialogPeer)
		if !ok {
			return nil
		}
		return logged(reportStates(c, []stateItem{{PeerID(p.Peer), "pinned", int64(boolInt(u.Pinned))}}))
	})
	d.OnNotifySettings(func(ctx context.Context, e tg.Entities, u *tg.UpdateNotifySettings) error {
		p, ok := u.Peer.(*tg.NotifyPeer)
		if !ok {
			return nil
		}
		return logged(reportStates(c, []stateItem{{PeerID(p.Peer), "muted", untilMS(u.NotifySettings)}}))
	})

	// someone joined or left a group (also seen as its service message, where the group shows one)
	d.OnChatParticipants(func(ctx context.Context, e tg.Entities, u *tg.UpdateChatParticipants) error {
		members(PeerID(&tg.PeerChat{ChatID: u.Participants.GetChatID()}))
		return nil
	})
	d.OnChatParticipantAdd(func(ctx context.Context, e tg.Entities, u *tg.UpdateChatParticipantAdd) error {
		members(PeerID(&tg.PeerChat{ChatID: u.ChatID}))
		return nil
	})
	d.OnChatParticipantDelete(func(ctx context.Context, e tg.Entities, u *tg.UpdateChatParticipantDelete) error {
		members(PeerID(&tg.PeerChat{ChatID: u.ChatID}))
		return nil
	})
	d.OnChannelParticipant(func(ctx context.Context, e tg.Entities, u *tg.UpdateChannelParticipant) error {
		members(PeerID(&tg.PeerChannel{ChannelID: u.ChannelID}))
		return nil
	})

	// read on any of the owner's devices, or by the others (seen as it happens: its time is now)
	d.OnReadHistoryInbox(func(ctx context.Context, e tg.Entities, u *tg.UpdateReadHistoryInbox) error {
		return logged(noteReads(c, []readItem{{PeerID(u.Peer), intp(u.MaxID), nil}}, false))
	})
	d.OnReadHistoryOutbox(func(ctx context.Context, e tg.Entities, u *tg.UpdateReadHistoryOutbox) error {
		return logged(noteReads(c, []readItem{{PeerID(u.Peer), nil, intp(u.MaxID)}}, true))
	})
	d.OnReadChannelInbox(func(ctx context.Context, e tg.Entities, u *tg.UpdateReadChannelInbox) error {
		return logged(noteReads(c, []readItem{{PeerID(&tg.PeerChannel{ChannelID: u.ChannelID}), intp(u.MaxID), nil}}, false))
	})
	d.OnReadChannelOutbox(func(ctx context.Context, e tg.Entities, u *tg.UpdateReadChannelOutbox) error {
		return logged(noteReads(c, []readItem{{PeerID(&tg.PeerChannel{ChannelID: u.ChannelID}), nil, intp(u.MaxID)}}, true))
	})
}

func entityLists(e tg.Entities) ([]tg.UserClass, []tg.ChatClass) {
	var users []tg.UserClass
	var chats []tg.ChatClass
	for _, u := range e.Users {
		users = append(users, u)
	}
	for _, ch := range e.Chats {
		chats = append(chats, ch)
	}
	for _, ch := range e.Channels {
		chats = append(chats, ch)
	}
	return users, chats
}

// --- sending ---------------------------------------------------------------------------------------

// telegramUser is a person's Telegram user id, from any of their addresses (their number, their
// id): the address itself where it is one, else one that is a member of the conversation.
func telegramUser(c *plugins.Context, addressID, conversationID int64) (int64, error) {
	var value string
	ok := db.Row(c.Store().Read(),
		"SELECT a.value FROM person_address mine JOIN person_address theirs ON theirs.person_id = mine.person_id "+
			"JOIN address a ON a.id = theirs.address_id JOIN address_kind k ON k.id = a.kind_id "+
			"JOIN service s ON s.id = a.service_id WHERE mine.address_id = ? AND k.name = 'id' AND s.name = 'telegram' "+
			"ORDER BY a.id = mine.address_id DESC, EXISTS (SELECT 1 FROM conversation_member cm WHERE "+
			"cm.conversation_id = ? AND cm.address_id = a.id) DESC, a.id LIMIT 1",
		[]any{addressID, conversationID}, &value)
	if !ok {
		return 0, pluginErr("Unknown person to mention")
	}
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}

// inputUser is the user as Telegram takes them: from those seen, else from telegram.db (a member
// who never came up in the dialogs this connection read).
func inputUser(cn *conn, uid int64) (tg.InputUserClass, error) {
	if p, err := cn.inputPeer(uid); err == nil {
		if u, ok := p.(*tg.InputPeerUser); ok {
			return &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash}, nil
		}
	}
	var hash *int64
	err := func() (err error) {
		defer db.Recover(&err)
		store, err := openStore(DBPath())
		if err != nil {
			return err
		}
		defer store.Close()
		var js string
		if !db.Row(store, "SELECT json FROM entity WHERE id = ?", []any{uid}, &js) {
			return nil
		}
		dec := json.NewDecoder(strings.NewReader(js))
		dec.UseNumber()
		var known map[string]any
		if dec.Decode(&known) == nil {
			if n, ok := known["access_hash"].(json.Number); ok {
				if h, err := n.Int64(); err == nil {
					hash = &h
				}
			}
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	if hash == nil {
		return nil, pluginErr("Unknown person to mention")
	}
	return &tg.InputUser{UserID: uid, AccessHash: *hash}, nil
}

func units(r []rune) int { return len(utf16.Encode(r)) }

// mentionEntities is the text with each mention (in characters, e.g. "@name") written as Telegram
// has it for a user named, not by username: the name (without "@"), linked to them, at its place in
// the text as sent, counted in UTF-16 units.
func mentionEntities(c *plugins.Context, cn *conn, text string, mentions []plugins.Mention, conversationID int64) (string, []tg.MessageEntityClass, error) {
	runes := []rune(text)
	ms := append([]plugins.Mention{}, mentions...)
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Start < ms[j].Start })
	var out []rune
	var entities []tg.MessageEntityClass
	at := 0
	clip := func(i int) int { return max(0, min(i, len(runes))) }
	for _, m := range ms {
		start, end := clip(m.Start), clip(m.Start+m.Length)
		name := []rune(strings.TrimPrefix(string(runes[start:max(start, end)]), "@"))
		if at < start {
			out = append(out, runes[at:start]...)
		}
		uid, err := telegramUser(c, m.AddressID, conversationID)
		if err != nil {
			return "", nil, err
		}
		user, err := inputUser(cn, uid)
		if err != nil {
			return "", nil, err
		}
		entities = append(entities, &tg.InputMessageEntityMentionName{Offset: units(out), Length: units(name), UserID: user})
		out = append(out, name...)
		at = max(at, end)
	}
	if at < len(runes) {
		out = append(out, runes[at:]...)
	}
	return string(out), entities, nil
}

var imageExt = regexp.MustCompile(`(?i)^\.(png|jpe?g)`)

// maxPhoto is the largest picture Telegram takes as a photo; a larger one goes as a file, as
// Telegram Desktop sends it.
const maxPhoto = 10 << 20

// notAPhoto are Telegram's refusals of a picture as a photo (its size, its sides, its format):
// the picture is sent again as a file.
var notAPhoto = []string{"PHOTO_INVALID_DIMENSIONS", "PHOTO_SAVE_FILE_INVALID", "PHOTO_EXT_INVALID",
	"PHOTO_INVALID", "IMAGE_PROCESS_FAILED"}

// media is what send_file makes of a file: a photo where its name says a PNG or JPEG and it is not
// sent as a document, else a document with its name (and a video's attribute).
func media(ctx context.Context, api *tg.Client, f *plugins.File, forceDocument bool) (tg.InputMediaClass, error) {
	name := f.Filename
	if name == "" {
		name = "file"
	}
	handle, err := uploader.NewUploader(api).FromBytes(ctx, name, f.Data)
	if err != nil {
		return nil, err
	}
	ext := filepath.Ext(name)
	isImage := imageExt.MatchString(ext)
	if isImage && !forceDocument && len(f.Data) <= maxPhoto {
		return &tg.InputMediaUploadedPhoto{File: handle}, nil
	}
	mimeType := f.MimeType // the type given (Telethon guessed it from the name only)
	if mimeType == "" && ext != "" {
		mimeType, _, _ = strings.Cut(mime.TypeByExtension(ext), ";")
	}
	attrs := []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: filepath.Base(name)}}
	if !forceDocument && strings.HasPrefix(mimeType, "video/") {
		attrs = append(attrs, &tg.DocumentAttributeVideo{W: 1, H: 1})
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	doc := &tg.InputMediaUploadedDocument{File: handle, MimeType: mimeType, Attributes: attrs}
	doc.SetForceFile(forceDocument && !isImage)
	return doc, nil
}

// sentMessage is the message an answer to sending holds (Telethon's _get_response_message), with
// the users and chats it brought. A short answer is made into the message Telethon made of it.
func sentMessage(r tg.UpdatesClass, randomID int64, peer tg.PeerClass, text string, replyTo int) (tg.MessageClass, []tg.UserClass, []tg.ChatClass) {
	if s, ok := r.(*tg.UpdateShortSentMessage); ok {
		m := &tg.Message{ID: s.ID, PeerID: peer, Message: text, Date: s.Date, Out: s.Out}
		if s.Media != nil {
			m.SetMedia(s.Media)
		}
		if len(s.Entities) > 0 {
			m.SetEntities(s.Entities)
		}
		if p, ok := s.GetTTLPeriod(); ok {
			m.SetTTLPeriod(p)
		}
		if replyTo != 0 { // Telethon kept the request's InputReplyToMessage here; the same id
			h := &tg.MessageReplyHeader{}
			h.SetReplyToMsgID(replyTo)
			m.SetReplyTo(h)
		}
		return m, nil, nil
	}
	var list []tg.UpdateClass
	var users []tg.UserClass
	var chats []tg.ChatClass
	switch u := r.(type) {
	case *tg.Updates:
		list, users, chats = u.Updates, u.Users, u.Chats
	case *tg.UpdatesCombined:
		list, users, chats = u.Updates, u.Users, u.Chats
	case *tg.UpdateShort:
		list = []tg.UpdateClass{u.Update}
	}
	id := 0
	for _, u := range list {
		if x, ok := u.(*tg.UpdateMessageID); ok && x.RandomID == randomID {
			id = x.ID
		}
	}
	for _, u := range list {
		var m tg.MessageClass
		switch x := u.(type) {
		case *tg.UpdateNewMessage:
			m = x.Message
		case *tg.UpdateNewChannelMessage:
			m = x.Message
		}
		if m != nil && (id == 0 || m.GetID() == id) {
			return m, users, chats
		}
	}
	return nil, users, chats
}

// fetchEntity is get_entity of an input peer: the user or chat as Telegram has it now.
func fetchEntity(ctx context.Context, cn *conn, peer tg.InputPeerClass) (any, error) {
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		users, err := cn.api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: p.UserID, AccessHash: p.AccessHash}})
		if err != nil {
			return nil, err
		}
		cn.seen(users, nil)
		return cn.entity(p.UserID), nil
	case *tg.InputPeerChat:
		r, err := cn.api.MessagesGetChats(ctx, []int64{p.ChatID})
		if err != nil {
			return nil, err
		}
		cn.seen(nil, r.GetChats())
		return cn.entity(-p.ChatID), nil
	case *tg.InputPeerChannel:
		r, err := cn.api.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash}})
		if err != nil {
			return nil, err
		}
		cn.seen(nil, r.GetChats())
		return cn.entity(-(channelMark + p.ChannelID)), nil
	}
	return nil, fmt.Errorf("cannot fetch %T", peer)
}

func send(ctx context.Context, c *plugins.Context, conv plugins.Conversation, text string, reply *plugins.Reply,
	mentions []plugins.Mention, file *plugins.File) (any, error) {
	var out any
	err := withConn(ctx, func(ctx context.Context, cn *conn) error {
		chatID, err := strconv.ParseInt(conv.Key, 10, 64)
		if err != nil {
			return err
		}
		peer, err := cn.peer(ctx, chatID)
		if err != nil {
			return err
		}
		var entities []tg.MessageEntityClass
		if len(mentions) > 0 {
			if text, entities, err = mentionEntities(c, cn, text, mentions, conv.ID); err != nil {
				return err
			}
		}
		replyTo := 0
		if reply != nil {
			n, err := strconv.Atoi(reply.Key)
			if err != nil {
				return err
			}
			replyTo = n
		}
		randomID, err := crypto.RandInt64(rand.Reader)
		if err != nil {
			return err
		}
		var replyHeader tg.InputReplyToClass
		if replyTo != 0 {
			replyHeader = &tg.InputReplyToMessage{ReplyToMsgID: replyTo}
		}
		var r tg.UpdatesClass
		if file != nil {
			force := !strings.HasPrefix(file.MimeType, "image/") && !strings.HasPrefix(file.MimeType, "video/")
			m, err := media(ctx, cn.api, file, force)
			if err != nil {
				return err
			}
			req := &tg.MessagesSendMediaRequest{Peer: peer, Media: m, Message: text, RandomID: randomID}
			if replyHeader != nil {
				req.SetReplyTo(replyHeader)
			}
			if len(entities) > 0 {
				req.SetEntities(entities)
			}
			r, err = cn.api.MessagesSendMedia(ctx, req)
			if _, photo := m.(*tg.InputMediaUploadedPhoto); photo && tgerr.Is(err, notAPhoto...) {
				if req.Media, err = media(ctx, cn.api, file, true); err != nil {
					return err
				}
				r, err = cn.api.MessagesSendMedia(ctx, req)
			}
			if err != nil {
				return err
			}
		} else {
			req := &tg.MessagesSendMessageRequest{Peer: peer, Message: text, RandomID: randomID}
			if replyHeader != nil {
				req.SetReplyTo(replyHeader)
			}
			if len(entities) > 0 {
				req.SetEntities(entities)
			}
			if r, err = cn.api.MessagesSendMessage(ctx, req); err != nil {
				return err
			}
		}
		msg, users, chats := sentMessage(r, randomID, peerOf(chatID), text, replyTo)
		if msg == nil {
			return errors.New("sent, but the answer has no message")
		}
		answered := cn.seen(users, chats)
		chat, err := fetchEntity(ctx, cn, peer)
		if err != nil {
			return err
		}
		if chat == nil {
			chat = cn.entity(chatID)
		}
		var sender any
		if id := senderID(msg); id != 0 {
			if sender = answered[id]; sender == nil {
				sender = cn.entity(id)
			}
		}
		if _, err := storeMessages(c, chat, []sent{{msg, sender}}); err != nil {
			return err
		}
		out = plugins.Sent{Keys: []string{strconv.Itoa(msg.GetID())}}
		return nil
	})
	return out, err
}

// markRead sends read receipts up to the newest message from the others at or before `until`
// (Unix ms).
func markRead(ctx context.Context, c *plugins.Context, conv plugins.Conversation, until int64) (int, error) {
	var key sql.NullString
	if !db.Row(c.Store().Read(), "SELECT key FROM message WHERE conversation_id = ? AND NOT outgoing AND ts <= ? AND key IS NOT NULL "+
		"ORDER BY ts DESC, id DESC LIMIT 1", []any{conv.ID, until}, &key) {
		return 0, nil
	}
	maxID, err := strconv.Atoi(key.String)
	if err != nil {
		return 0, err
	}
	chat, err := strconv.ParseInt(conv.Key, 10, 64)
	if err != nil {
		return 0, err
	}
	err = withConn(ctx, func(ctx context.Context, cn *conn) error {
		peer, err := cn.peer(ctx, chat)
		if err != nil {
			return err
		}
		if ch, ok := peer.(*tg.InputPeerChannel); ok {
			_, err = cn.api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
				Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}, MaxID: maxID})
		} else {
			_, err = cn.api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: peer, MaxID: maxID})
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	if err := noteReads(c, []readItem{{chat, intp(maxID), nil}}, false); err != nil {
		return 0, err
	}
	return 1, nil
}
