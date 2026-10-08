// Spam and the people the user blocked: someone removed as spam in the app is reported to Telegram,
// blocked and their chat deleted there (ReportSpam), and their chat dropped from telegram.db
// (Forget); the people blocked on Telegram, from any device, are written to the archive (`blocked`),
// where the app suggests removing them.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

const (
	notAPerson  = "Only a chat with one person can be reported as spam"
	peerUnknown = "Telegram does not know this person any more"
)

// blockedWhere is the archive's `blocked.phone` of what is blocked on Telegram.
const blockedWhere = "telegram"

// ReportSpam reports the person of a one-to-one chat as spam, blocks them, and deletes the chat on
// Telegram (for the user only); the block is written to the archive at once.
func (Plugin) ReportSpam(ctx context.Context, c *plugins.Context, conv plugins.Conversation) error {
	user, err := strconv.ParseInt(conv.Key, 10, 64)
	if err != nil || user <= 0 {
		return pluginErr(notAPerson)
	}
	return withConn(ctx, func(ctx context.Context, cn *conn) error {
		peer, err := spamPeer(ctx, cn, user)
		if err != nil {
			return err
		}
		if _, err := cn.api.MessagesReportSpam(ctx, peer); err != nil && !tgerr.Is(err, "PEER_ID_INVALID") {
			return err
		}
		if _, err := cn.api.ContactsBlock(ctx, &tg.ContactsBlockRequest{ID: peer}); err != nil &&
			!tgerr.Is(err, "CONTACT_ID_INVALID") {
			return err
		}
		for { // Telegram deletes a long chat a part at a time
			r, err := cn.api.MessagesDeleteHistory(ctx, &tg.MessagesDeleteHistoryRequest{Peer: peer})
			if err != nil {
				return err
			}
			if r.Offset <= 0 {
				break
			}
		}
		return withArchive(c, func(a *archive.Archive) error {
			a.Blocked(blockedWhere, archive.H("id", fmt.Sprint(user), "telegram"), true)
			return nil
		})
	})
}

// spamPeer is the user's input peer: from the dialogs, else from telegram.db (a chat the user
// already deleted on Telegram is no longer among the dialogs).
func spamPeer(ctx context.Context, cn *conn, user int64) (tg.InputPeerClass, error) {
	if p, err := cn.peer(ctx, user); err == nil {
		return p, nil
	}
	store, err := openStore(DBPath())
	if err != nil {
		return nil, err
	}
	defer store.Close()
	for _, q := range []string{"SELECT json FROM chat WHERE id = ?", "SELECT json FROM entity WHERE id = ?"} {
		var e struct {
			Type       string `json:"_"`
			AccessHash *int64 `json:"access_hash"`
		}
		if raw := db.Str(store, q, user); raw != "" && json.Unmarshal([]byte(raw), &e) == nil &&
			e.Type == "User" && e.AccessHash != nil {
			return &tg.InputPeerUser{UserID: user, AccessHash: *e.AccessHash}, nil
		}
	}
	return nil, pluginErr(peerUnknown)
}

// Forget drops the chat from telegram.db, with the files downloaded of it.
func (Plugin) Forget(c *plugins.Context, conv plugins.Conversation) (err error) {
	defer db.Recover(&err)
	chat, err := strconv.ParseInt(conv.Key, 10, 64)
	if err != nil {
		return err
	}
	if _, err := os.Stat(DBPath()); err != nil {
		return nil // no store: nothing kept
	}
	store, err := openStore(DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	tx, err := store.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	files := db.Strs(tx, "SELECT file FROM message WHERE chat_id = ? AND file IS NOT NULL AND file != ''", chat)
	for _, t := range []string{"message", "deleted", "chat_read", "chat_member", "chat_member_list"} {
		db.Exec(tx, "DELETE FROM "+t+" WHERE chat_id = ?", chat)
	}
	db.Exec(tx, "DELETE FROM chat WHERE id = ?", chat)
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, f := range files {
		if filepath.IsLocal(f) {
			os.Remove(filepath.Join(MediaPath(), f))
		}
	}
	return nil
}

// noteBlocked writes the people blocked on Telegram to the archive, as Telegram lists them now.
func noteBlocked(ctx context.Context, c *plugins.Context, cn *conn) error {
	var users []archive.Handle
	for offset := 0; ; {
		r, err := cn.api.ContactsGetBlocked(ctx, &tg.ContactsGetBlockedRequest{Offset: offset, Limit: 100})
		if err != nil {
			return err
		}
		var page []tg.PeerBlocked
		switch r := r.(type) {
		case *tg.ContactsBlocked:
			page = r.Blocked
		case *tg.ContactsBlockedSlice:
			page = r.Blocked
		}
		for _, b := range page {
			if u, ok := b.PeerID.(*tg.PeerUser); ok {
				users = append(users, archive.H("id", fmt.Sprint(u.UserID), "telegram"))
			}
		}
		if _, ok := r.(*tg.ContactsBlockedSlice); !ok || len(page) == 0 {
			break
		}
		offset += len(page)
	}
	return withArchive(c, func(a *archive.Archive) error {
		a.SetBlocked(blockedWhere, users, nil)
		return nil
	})
}

// peerBlocked records one person blocked, or no longer, on any of the user's devices.
func peerBlocked(c *plugins.Context, u *tg.UpdatePeerBlocked) error {
	p, ok := u.PeerID.(*tg.PeerUser)
	if !ok {
		return nil
	}
	return withArchive(c, func(a *archive.Archive) error {
		a.Blocked(blockedWhere, archive.H("id", fmt.Sprint(p.UserID), "telegram"), u.Blocked)
		return nil
	})
}
