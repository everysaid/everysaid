package whatsapp

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"everysaid/internal/importers"
)

// The history the phone sends comes as a notification (acknowledged to the phone at once) and a
// file to download. As mautrix-whatsapp does, the notification is kept first (whatsapp_history) and
// the file downloaded apart, tried again until its history is written: a failed download, or the
// bridge ending meanwhile, no longer loses it (whatsmeow's own way deletes the file on WhatsApp's
// server after one try, whatever came of it).

// pendHistory keeps a history notification until its history is written.
func (store *Store) pendHistory(n *waE2E.HistorySyncNotification) error {
	b, err := proto.Marshal(n)
	if err != nil {
		return err
	}
	_, err = store.db.Exec("INSERT OR IGNORE INTO whatsapp_history (store, direct_path, notification) VALUES (?, ?, ?)",
		store.key, n.GetDirectPath(), b)
	return err
}

// historyRetry is how long after a failed download it is tried again (doubling, up to a day).
func historyRetry(tries int) time.Duration {
	return min(time.Minute<<min(tries, 12), 24*time.Hour)
}

// historyTries is how many failed downloads give a history up (WhatsApp keeps the file a while).
const historyTries = 20

// fetchHistory downloads the pending histories, oldest first, and hands each to handle (true: it is
// written; nil: its download failed, it is tried again later); it runs until ctx ends, woken when
// one is added.
func fetchHistory(ctx context.Context, client *whatsmeow.Client, store *Store, wake <-chan struct{},
	handle func(path string, h *events.HistorySync) bool, logger waLog.Logger) {
	for {
		next := time.Hour
		for {
			var path string
			var raw []byte
			var tries int
			err := store.db.QueryRow("SELECT direct_path, notification, tries FROM whatsapp_history WHERE store = ? AND next_at <= ? "+
				"ORDER BY rowid LIMIT 1", store.key, time.Now().Unix()).Scan(&path, &raw, &tries)
			if err != nil {
				break // none due
			}
			n := &waE2E.HistorySyncNotification{}
			if err := proto.Unmarshal(raw, n); err != nil {
				store.exec("DELETE FROM whatsapp_history WHERE store = ? AND direct_path = ?", store.key, path)
				continue
			}
			data, err := client.DownloadHistorySync(ctx, n, true)
			if ctx.Err() != nil {
				return
			}
			if err != nil && !client.IsConnected() { // not a try: when connected again (Connected wakes it)
				store.exec("UPDATE whatsapp_history SET next_at = ? WHERE store = ? AND direct_path = ?",
					time.Now().Add(time.Minute).Unix(), store.key, path)
				handle(path, nil)
				break
			}
			if err != nil {
				gone := errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410)
				if gone || tries+1 >= historyTries {
					logger.Errorf("History from the phone could not be downloaded, given up: %v", err)
					store.exec("DELETE FROM whatsapp_history WHERE store = ? AND direct_path = ?", store.key, path)
				} else {
					logger.Warnf("History from the phone could not be downloaded (tried again later): %v", err)
					store.exec("UPDATE whatsapp_history SET tries = tries + 1, next_at = ? WHERE store = ? AND direct_path = ?",
						time.Now().Add(historyRetry(tries)).Unix(), store.key, path)
				}
				handle(path, nil) // what waits for it need not
				continue
			}
			if !handle(path, &events.HistorySync{Data: data, Notification: n}) {
				// the bridge ending, or the archive not written: kept, tried again later
				store.exec("UPDATE whatsapp_history SET next_at = ? WHERE store = ? AND direct_path = ?",
					time.Now().Add(time.Minute).Unix(), store.key, path)
				if ctx.Err() != nil {
					return
				}
				continue
			}
			store.exec("DELETE FROM whatsapp_history WHERE store = ? AND direct_path = ?", store.key, path)
			if err := client.DeleteMedia(ctx, whatsmeow.MediaHistory, n.GetDirectPath(), n.GetFileEncSHA256(), n.GetEncHandle()); err != nil {
				logger.Warnf("Failed to delete history sync media from server: %v", err)
			}
		}
		var at int64
		if store.db.QueryRow("SELECT coalesce(min(next_at), 0) FROM whatsapp_history WHERE store = ?", store.key).Scan(&at) == nil && at > 0 {
			next = max(time.Until(time.Unix(at, 0)), time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-time.After(next):
		}
	}
}

// historyStub adds what an entry of the history with no message stands for, where it is known: a
// missed call (as the call log's), a message deleted for everyone.
func historyStub(client *whatsmeow.Client, b *importers.WABatch, chat types.JID, w *waWeb.WebMessageInfo) {
	key := w.GetKey()
	at := int64(w.GetMessageTimestamp()) * 1000
	switch st := w.GetMessageStubType(); st {
	case waWeb.WebMessageInfo_CALL_MISSED_VOICE, waWeb.WebMessageInfo_CALL_MISSED_VIDEO,
		waWeb.WebMessageInfo_CALL_MISSED_GROUP_VOICE, waWeb.WebMessageInfo_CALL_MISSED_GROUP_VIDEO:
		video := st == waWeb.WebMessageInfo_CALL_MISSED_VIDEO || st == waWeb.WebMessageInfo_CALL_MISSED_GROUP_VIDEO
		b.CallLogs = append(b.CallLogs, importers.WACallLog{ID: key.GetID(), Chat: chat.String(), FromMe: key.GetFromMe(),
			Group: chat.Server == types.GroupServer, Video: video, At: at, Outcome: "MISSED"})
	case waWeb.WebMessageInfo_REVOKE:
		// the message deleted is the one this entry stands in for: its author's
		sender := key.GetParticipant()
		if sender == "" {
			sender = chat.String()
			if key.GetFromMe() && client != nil && client.Store.ID != nil {
				sender = client.Store.ID.ToNonAD().String()
			}
		}
		b.Changes = append(b.Changes, importers.WAChange{Chat: chat.String(), Target: key.GetID(), Change: "delete",
			Sender: sender, FromMe: key.GetFromMe(), At: at})
	}
}

// historyVotes adds the votes a poll of the history carries (already read by the phone).
func historyVotes(client *whatsmeow.Client, b *importers.WABatch, chat types.JID, w *waWeb.WebMessageInfo) {
	for _, u := range w.GetPollUpdates() {
		k := u.GetPollUpdateMessageKey()
		voter := k.GetParticipant()
		switch {
		case k.GetFromMe():
			voter = ownJID(client)
		case voter == "":
			voter = chat.String()
		}
		if j, err := types.ParseJID(voter); err == nil {
			j = j.ToNonAD()
			if j.Server == types.HiddenUserServer && client.Store != nil && client.Store.LIDs != nil {
				// one voter by one jid: their number's, as the live votes are kept
				if pn, err := client.Store.LIDs.GetPNForLID(context.Background(), j); err == nil && !pn.IsEmpty() {
					j = pn.ToNonAD()
				}
			}
			voter = j.String()
		}
		chosen := []string{}
		for _, h := range u.GetVote().GetSelectedOptions() {
			chosen = append(chosen, hex.EncodeToString(h))
		}
		b.Votes = append(b.Votes, importers.WAVote{Chat: chat.String(), Poll: w.GetKey().GetID(), Voter: voter, Options: chosen,
			At: u.GetSenderTimestampMS()})
	}
}

// deletedForMe adds a message deleted for the user alone (from here or another of their devices),
// as a deletion of the archive's copy (any message of the chat, whoever wrote it).
func deletedForMe(b *importers.WABatch, chats []string, id, own string, at time.Time) {
	for _, chat := range chats {
		b.Changes = append(b.Changes, importers.WAChange{Chat: chat, Target: id, Change: "delete_me", Sender: own, FromMe: true,
			At: at.UnixMilli()})
	}
}
