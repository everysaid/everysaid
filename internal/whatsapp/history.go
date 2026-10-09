package whatsapp

import (
	"context"
	"errors"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// The history the phone sends comes as a notification (acknowledged to the phone at once) and a
// file to download. As mautrix-whatsapp does, the notification is kept first (history_pending) and
// the file downloaded apart, tried again until its history is stored: a failed download, or the
// bridge ending meanwhile, no longer loses it (whatsmeow's own way deletes the file on WhatsApp's
// server after one try, whatever came of it).

func (store *MessageStore) migrateHistory() error {
	_, err := store.db.Exec(`
		CREATE TABLE IF NOT EXISTS history_pending (  -- history the phone sent, not stored yet
			direct_path TEXT PRIMARY KEY,
			notification BLOB NOT NULL,   -- waE2E.HistorySyncNotification
			tries INTEGER NOT NULL DEFAULT 0,
			next_at INTEGER NOT NULL DEFAULT 0  -- Unix s: not tried before
		);
	`)
	return err
}

// pendHistory keeps a history notification until its history is stored.
func (store *MessageStore) pendHistory(n *waE2E.HistorySyncNotification) error {
	b, err := proto.Marshal(n)
	if err != nil {
		return err
	}
	_, err = store.db.Exec("INSERT OR IGNORE INTO history_pending (direct_path, notification) VALUES (?, ?)",
		n.GetDirectPath(), b)
	return err
}

// historyRetry is how long after a failed download it is tried again (doubling, up to a day).
func historyRetry(tries int) time.Duration {
	return min(time.Minute<<min(tries, 12), 24*time.Hour)
}

// historyTries is how many failed downloads give a history up (WhatsApp keeps the file a while).
const historyTries = 20

// fetchHistory downloads the pending histories, oldest first, and hands each to handle (true: it is
// stored; nil: its download failed, it is tried again later); it runs until ctx ends, woken when one
// is added.
func fetchHistory(ctx context.Context, client *whatsmeow.Client, store *MessageStore, wake <-chan struct{},
	handle func(path string, h *events.HistorySync) bool, logger waLog.Logger) {
	for {
		next := time.Hour
		for {
			var path string
			var raw []byte
			var tries int
			err := store.db.QueryRow("SELECT direct_path, notification, tries FROM history_pending WHERE next_at <= ? "+
				"ORDER BY rowid LIMIT 1", time.Now().Unix()).Scan(&path, &raw, &tries)
			if err != nil {
				break // none due (or the store closed)
			}
			n := &waE2E.HistorySyncNotification{}
			if err := proto.Unmarshal(raw, n); err != nil {
				store.db.Exec("DELETE FROM history_pending WHERE direct_path = ?", path)
				continue
			}
			data, err := client.DownloadHistorySync(ctx, n, true)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				gone := errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410)
				if gone || tries+1 >= historyTries {
					logger.Errorf("History from the phone could not be downloaded, given up: %v", err)
					store.db.Exec("DELETE FROM history_pending WHERE direct_path = ?", path)
				} else {
					logger.Warnf("History from the phone could not be downloaded (tried again later): %v", err)
					store.db.Exec("UPDATE history_pending SET tries = tries + 1, next_at = ? WHERE direct_path = ?",
						time.Now().Add(historyRetry(tries)).Unix(), path)
				}
				handle(path, nil) // what waits for it need not
				continue
			}
			if !handle(path, &events.HistorySync{Data: data, Notification: n}) {
				return // the bridge is ending: kept for the next start
			}
			store.db.Exec("DELETE FROM history_pending WHERE direct_path = ?", path)
			if err := client.DeleteMedia(ctx, whatsmeow.MediaHistory, n.GetDirectPath(), n.GetFileEncSHA256(), n.GetEncHandle()); err != nil {
				logger.Warnf("Failed to delete history sync media from server: %v", err)
			}
		}
		var at int64
		if store.db.QueryRow("SELECT min(next_at) FROM history_pending").Scan(&at) == nil && at > 0 {
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
