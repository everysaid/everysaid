package telegram

// Who read the owner's messages in a group: Telegram tells only that someone did (the chat's outbox
// read moving on), and who, asked of each message (messages.getMessageReadParticipants: a week
// back, in groups of up to a hundred). Asked when the outbox moves on, for the owner's latest
// messages there, at most once in a while for each chat (the last change still asked after it).

import (
	"context"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

const (
	readersBack  = 7 * 24 * time.Hour // how far back Telegram says who read
	readersLast  = 20                 // the owner's latest messages asked about each time someone reads
	readersAll   = 200                // and at each connection (the week's, in a busy group)
	readersEvery = 30 * time.Second   // at most this often for one chat
)

// readersAsking: the chats being asked about, and whether they changed again meanwhile.
var readersAsking = struct {
	sync.Mutex
	again map[int64]bool
}{again: map[int64]bool{}}

// askReaders asks who read the owner's latest messages in a group, now and, if the chat changed
// meanwhile, again a while later; one round at a time for each chat.
func askReaders(ctx context.Context, c *plugins.Context, cn *conn, chatID int64) {
	readersAsking.Lock()
	if _, busy := readersAsking.again[chatID]; busy {
		readersAsking.again[chatID] = true
		readersAsking.Unlock()
		return
	}
	readersAsking.again[chatID] = false
	readersAsking.Unlock()
	go func() {
		for {
			if err := readers(ctx, c, cn, chatID, readersLast); err != nil {
				c.Log("error: {e}", map[string]any{"e": err})
			}
			select {
			case <-ctx.Done():
			case <-time.After(readersEvery):
			}
			readersAsking.Lock()
			if !readersAsking.again[chatID] || ctx.Err() != nil {
				delete(readersAsking.again, chatID)
				readersAsking.Unlock()
				return
			}
			readersAsking.again[chatID] = false
			readersAsking.Unlock()
		}
	}()
}

// readers asks who read the owner's latest messages (at most last) of the last week in a group,
// keeps it, and brings it into the archive.
func readers(ctx context.Context, c *plugins.Context, cn *conn, chatID int64, last int) (err error) {
	defer db.Recover(&err)
	store, err := openStore(DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	kind := db.Str(store, "SELECT kind FROM chat WHERE id = ?", chatID)
	if kind != "group" && kind != "supergroup" {
		return nil
	}
	ids := db.Ints(store, "SELECT id FROM message WHERE chat_id = ? AND date > ? AND json_extract(json, '$.out') "+
		"AND json_extract(json, '$._') = 'Message' ORDER BY id DESC LIMIT ?",
		chatID, time.Now().Add(-readersBack).Unix(), last)
	if len(ids) == 0 {
		return nil
	}
	peer, err := cn.peer(ctx, chatID)
	if err != nil {
		return nil // a chat not known now: asked again when it changes
	}
	got := false
	for _, id := range ids {
		rs, err := cn.api.MessagesGetMessageReadParticipants(ctx, &tg.MessagesGetMessageReadParticipantsRequest{
			Peer: peer, MsgID: int(id)})
		switch {
		case tgerr.Is(err, "CHAT_TOO_BIG", "MSG_TOO_OLD", "CHAT_ADMIN_REQUIRED", "PEER_ID_INVALID"):
			// a group too large, or a message too old, for Telegram to say: an older one neither
			if tgerr.Is(err, "MSG_TOO_OLD") {
				continue
			}
			return nil
		case err != nil:
			return err
		}
		for _, r := range rs {
			res := db.Exec(store, "INSERT INTO read_by (chat_id, id, user_id, at) VALUES (?, ?, ?, ?) "+
				"ON CONFLICT (chat_id, id, user_id) DO NOTHING", chatID, id, r.UserID, r.Date)
			if n, _ := res.RowsAffected(); n > 0 {
				got = true
			}
		}
	}
	if !got {
		return nil
	}
	if err := withArchive(c, func(a *archive.Archive) error {
		source(a, c.ID)
		return importReads(a, map[int64]bool{chatID: true})
	}); err != nil {
		return err
	}
	c.Emit(M{"type": "changed"})
	return nil
}

// recentGroups are the groups the owner wrote in this last week: asked about once connected (read
// while the app was not).
func recentGroups() (ids []int64) {
	defer func() {
		if recover() != nil {
			ids = nil
		}
	}()
	store, err := openStore(DBPath())
	if err != nil {
		return nil
	}
	defer store.Close()
	return db.Ints(store, "SELECT DISTINCT m.chat_id FROM message m JOIN chat c ON c.id = m.chat_id "+
		"WHERE c.kind IN ('group', 'supergroup') AND m.date > ? AND json_extract(m.json, '$.out')",
		time.Now().Add(-readersBack).Unix())
}
