package telegram

import (
	"context"

	"github.com/gotd/td/tg"

	"everysaid/internal/db"
)

// withMigrated adds to the dialogs the basic groups the supergroups among them were before Telegram
// made them supergroups: the dialogs no longer list them, and their history (up to the change) is
// read as their own chat, as Telegram Desktop shows it above the supergroup's. Telegram is asked once
// for each supergroup (telegram.db's migrated keeps the answer, also that there was none).
func withMigrated(ctx context.Context, cn *conn, dialogs []dialog) (out []dialog, err error) {
	defer db.Recover(&err)
	store, err := openStore(DBPath())
	if err != nil {
		return dialogs, err
	}
	defer store.Close()
	out = dialogs
	for _, d := range dialogs {
		ch, ok := d.Entity.(*tg.Channel)
		if !ok || ch.Broadcast {
			continue
		}
		var chatID, maxID int64
		if !db.Row(store, "SELECT chat_id, max_id FROM migrated WHERE channel_id = ?", []any{d.ID}, &chatID, &maxID) {
			full, err := cn.api.ChannelsGetFullChannel(ctx, &tg.InputChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash})
			if err != nil {
				if stopsAll(ctx, err) {
					return dialogs, err
				}
				continue // asked again next time
			}
			cn.seen(full.Users, full.Chats)
			if f, ok := full.FullChat.(*tg.ChannelFull); ok {
				if id, ok := f.GetMigratedFromChatID(); ok {
					chatID = id
					maxID = int64(f.MigratedFromMaxID)
				}
			}
			db.Exec(store, "INSERT OR REPLACE INTO migrated VALUES (?, ?, ?)", d.ID, chatID, maxID)
		}
		if chatID == 0 {
			continue
		}
		old, ok := cn.entity(-chatID).(*tg.Chat) // not one the owner was never in (forbidden, unknown)
		if !ok {
			continue
		}
		out = append(out, dialog{ID: -chatID, Entity: old, Name: DisplayName(old), Archived: d.Archived,
			Dialog: &tg.Dialog{}, Message: &tg.Message{ID: int(maxID)}})
	}
	return out, nil
}
