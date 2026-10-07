// The members of the groups, asked of Telegram (the Python never asked: a group's members were
// whoever wrote there). The sync asks for every group it reads; the live connection for those not
// asked for a day when it connects, and for a group at once when Telegram says its members changed.
package telegram

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
	"everysaid/internal/telegramstore"
)

// memberPage is how many members channels.getParticipants gives at most at a time.
const memberPage = 200

// memberList is a group's members as Telegram gave them: complete false where it gave only some
// (hidden members, an admin's right needed) or none.
type memberList struct {
	ids      []int64
	users    []*tg.User // their entities, as the answers carried them
	complete bool
	count    int // how many Telegram says the group has
}

// asksMembers says whether a chat's members can be asked for: a basic group or a supergroup the
// owner is still in (one left, or a basic group moved to a supergroup, answers nothing).
func asksMembers(chat any) bool {
	switch e := chat.(type) {
	case *tg.Chat:
		return !e.Left && !e.Deactivated
	case *tg.Channel:
		return !e.Broadcast && !e.Left && !e.Min
	}
	return false
}

// members asks Telegram who is in a group: a basic group's whole list comes with its full
// information (messages.getFullChat); a supergroup's is read memberPage at a time
// (channels.getParticipants, the recent ones, which are all of them where Telegram shows them)
// until Telegram gives no more. A refusal after some pages is the error, with what came before it.
func (c *conn) members(ctx context.Context, chat any) (memberList, error) {
	var out memberList
	seen := map[int64]bool{}
	add := func(id int64, users map[int64]any) {
		if id == 0 || seen[id] {
			return
		}
		seen[id] = true
		out.ids = append(out.ids, id)
		if u, ok := users[id].(*tg.User); ok {
			out.users = append(out.users, u)
		}
	}
	switch e := chat.(type) {
	case *tg.Chat:
		r, err := c.api.MessagesGetFullChat(ctx, e.ID)
		if err != nil {
			return out, err
		}
		users := c.seen(r.Users, r.Chats)
		full, _ := r.FullChat.(*tg.ChatFull)
		if full == nil {
			return out, nil
		}
		switch p := full.Participants.(type) {
		case *tg.ChatParticipants:
			for _, x := range p.Participants {
				add(x.GetUserID(), users)
			}
			out.complete, out.count = len(out.ids) > 0, len(out.ids)
		case *tg.ChatParticipantsForbidden: // only the owner, if that
			if self, ok := p.GetSelfParticipant(); ok {
				add(self.GetUserID(), users)
			}
			out.count = e.ParticipantsCount
		}
		return out, nil
	case *tg.Channel:
		hash, _ := e.GetAccessHash()
		req := &tg.ChannelsGetParticipantsRequest{Channel: &tg.InputChannel{ChannelID: e.ID, AccessHash: hash},
			Filter: &tg.ChannelParticipantsRecent{}, Limit: memberPage}
		for {
			r, err := c.api.ChannelsGetParticipants(ctx, req)
			if err != nil {
				return out, err
			}
			p, ok := r.(*tg.ChannelsChannelParticipants)
			if !ok {
				break
			}
			users := c.seen(p.Users, p.Chats)
			out.count = p.Count
			for _, x := range p.Participants {
				add(participant(x), users)
			}
			req.Offset += len(p.Participants)
			if len(p.Participants) == 0 || req.Offset >= p.Count {
				break
			}
		}
		out.complete = len(out.ids) > 0 && len(out.ids) >= out.count
		return out, nil
	}
	return out, nil
}

// participant is the user id of a supergroup's member, 0 for one who is not one (left, or a chat).
func participant(x tg.ChannelParticipantClass) int64 {
	switch p := x.(type) {
	case *tg.ChannelParticipant:
		return p.UserID
	case *tg.ChannelParticipantSelf:
		return p.UserID
	case *tg.ChannelParticipantCreator:
		return p.UserID
	case *tg.ChannelParticipantAdmin:
		return p.UserID
	case *tg.ChannelParticipantBanned: // restricted, still in it unless it says it left
		if u, ok := p.Peer.(*tg.PeerUser); ok && !p.Left {
			return u.UserID
		}
	}
	return 0
}

// storeMembers writes what members gave into the store, with the members' entities.
func storeMembers(q db.Querier, chatID int64, list memberList) {
	for _, u := range list.users {
		db.Exec(q, "INSERT OR REPLACE INTO entity VALUES (?, ?)", u.ID, Dump(u))
	}
	if err := telegramstore.NoteMembers(q, chatID, list.ids, list.complete, list.count); err != nil {
		panic(err)
	}
}

// Said when Telegram gives no member of a group, or only some.
const (
	membersRefused = "{chat}: Telegram did not give its members ({e})"
	membersPartial = "{chat}: Telegram gave {n} of its {count} members"
)

// askMembers asks for a group's members and keeps them; say is told when Telegram refused them or
// gave only some, which is not a failure (what came is kept, and whoever wrote stays a member).
// Only the context's end and the store's failures are errors.
func askMembers(ctx context.Context, cn *conn, store *sql.DB, chat any, label any, say func(string, map[string]any)) (bool, error) {
	list, err := cn.members(ctx, chat)
	if err != nil && ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil && len(list.ids) == 0 {
		say(membersRefused, map[string]any{"chat": label, "e": err.Error()})
		return false, nil
	}
	if len(list.ids) == 0 && !list.complete {
		return false, nil // nothing to go on (a group the owner cannot see into)
	}
	if !list.complete {
		say(membersPartial, map[string]any{"chat": label, "n": len(list.ids), "count": list.count})
	}
	tx, err := store.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := func() (err error) {
		defer db.Recover(&err)
		storeMembers(tx, EntityID(chat), list)
		return nil
	}(); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// --- the live connection ---------------------------------------------------------------------------

// membersStale: on connecting, the groups whose members were not asked for this long are asked again.
const membersStale = 24 * time.Hour

// changesMembers says whether a service message is a change of a group's members.
func changesMembers(m tg.MessageClass) bool {
	s, ok := m.(*tg.MessageService)
	if !ok {
		return false
	}
	switch s.Action.(type) {
	case *tg.MessageActionChatAddUser, *tg.MessageActionChatDeleteUser, *tg.MessageActionChatJoinedByLink,
		*tg.MessageActionChatJoinedByRequest, *tg.MessageActionChatCreate, *tg.MessageActionChannelMigrateFrom:
		return true
	}
	return false
}

// staleMembers are the groups among the dialogs whose members were never asked for, or not for
// membersStale.
func staleMembers(dialogs []dialog) (out []int64, err error) {
	defer db.Recover(&err)
	store, err := openStore(DBPath())
	if err != nil {
		return nil, err
	}
	defer store.Close()
	asked := map[int64]int64{}
	db.Each(store, "SELECT chat_id, fetched_at FROM chat_member_list", nil, func(scan func(...any)) {
		var id, at int64
		scan(&id, &at)
		asked[id] = at
	})
	since := time.Now().Add(-membersStale).Unix()
	for _, d := range dialogs {
		if at, ok := asked[d.ID]; asksMembers(d.Entity) && (!ok || at < since) {
			out = append(out, d.ID)
		}
	}
	return out, nil
}

// refreshMembers asks Telegram for those groups' members, keeps them in telegram.db and puts them
// into the archive (the chats the user left out are kept, not imported).
func refreshMembers(ctx context.Context, c *plugins.Context, cn *conn, chats []int64) error {
	if len(chats) == 0 {
		return nil
	}
	store, err := openStore(DBPath())
	if err != nil {
		return err
	}
	got := map[int64]bool{}
	err = func() error {
		defer store.Close()
		for _, id := range chats {
			chat := cn.entity(id)
			if chat == nil { // a group not met yet: the dialogs again
				if _, err := cn.peer(ctx, id); err != nil {
					c.Log(membersRefused, map[string]any{"chat": id, "e": err.Error()})
					continue
				}
				chat = cn.entity(id)
			}
			if !asksMembers(chat) {
				continue
			}
			ok, err := askMembers(ctx, cn, store, chat, chatLabel(chat), c.Log)
			if err != nil {
				return err
			}
			if ok {
				got[id] = true
			}
		}
		return nil
	}()
	if err != nil {
		return err
	}
	for id := range idSet(currentSettings(c)["skip_chats"]) {
		delete(got, id)
	}
	if len(got) == 0 {
		return nil
	}
	if err := withArchive(c, func(a *archive.Archive) error {
		source(a, c.ID)
		return importMembers(a, got)
	}); err != nil {
		return err
	}
	c.Emit(M{"type": "changed"})
	return nil
}

// memberQueue gathers the groups whose members Telegram says changed, and asks for them a moment
// later: one change often comes as several updates (the service message, the participants').
type memberQueue struct {
	mu    sync.Mutex
	chats map[int64]bool
	wake  chan struct{}
}

func newMemberQueue() *memberQueue {
	return &memberQueue{chats: map[int64]bool{}, wake: make(chan struct{}, 1)}
}

// memberDelay is how long the queue waits for the rest of a change.
var memberDelay = 2 * time.Second

func (q *memberQueue) add(chat int64) {
	q.mu.Lock()
	q.chats[chat] = true
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// run asks for the queued groups until ctx ends; a failure is logged, the connection goes on.
func (q *memberQueue) run(ctx context.Context, c *plugins.Context, cn *conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(memberDelay):
		}
		q.mu.Lock()
		var chats []int64
		for id := range q.chats {
			chats = append(chats, id)
		}
		q.chats = map[int64]bool{}
		q.mu.Unlock()
		sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
		if err := refreshMembers(ctx, c, cn, chats); err != nil && ctx.Err() == nil {
			c.Log("error: {e}", map[string]any{"e": err.Error()})
		}
	}
}
