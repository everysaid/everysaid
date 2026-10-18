package whatsapp

// Spam and the people the user blocked: someone removed as spam in the app is blocked on WhatsApp
// (whatsmeow cannot report: only the phone can), and what the bridge keeps of their chat dropped
// (Forget); the account's blocklist is kept in keys.db (whatsapp_blocked, from every device), for
// the import to bring into the archive, where the app suggests removing them.

import (
	"context"
	"os"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/keys"
	"everysaid/internal/plugins"
)

// setBlocklist keeps the whole blocklist as WhatsApp gives it.
func (store *Store) setBlocklist(jids []types.JID) error {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM whatsapp_blocked WHERE store = ?", store.key); err != nil {
		return err
	}
	for _, j := range jids {
		if _, err := tx.Exec("INSERT OR IGNORE INTO whatsapp_blocked (store, jid) VALUES (?, ?)", store.key, j.ToNonAD().String()); err != nil {
			return err
		}
	}
	// WhatsApp has said it (an empty list too): the archive's is to be as it
	if _, err := tx.Exec("INSERT OR REPLACE INTO whatsapp_state (store, key, value, at) VALUES (?, 'blocklist', '1', unixepoch())",
		store.key); err != nil {
		return err
	}
	return tx.Commit()
}

// refreshBlocklist asks WhatsApp for the blocklist and keeps it.
func refreshBlocklist(client *whatsmeow.Client, store *Store, logger waLog.Logger) {
	list, err := client.GetBlocklist(context.Background())
	if err == nil {
		err = store.setBlocklist(list.JIDs)
	}
	if err != nil {
		logger.Warnf("Failed to read the blocklist: %v", err)
	}
}

// handleBlocklist: someone blocked or unblocked on any device; "modify": the list is to be asked for again.
func handleBlocklist(client *whatsmeow.Client, store *Store, v *events.Blocklist, logger waLog.Logger) {
	if v.Action == events.BlocklistActionModify || len(v.Changes) == 0 {
		refreshBlocklist(client, store, logger)
		return
	}
	for _, c := range v.Changes {
		jid := c.JID.ToNonAD().String()
		if c.Action == events.BlocklistChangeActionBlock {
			store.exec("INSERT OR IGNORE INTO whatsapp_blocked (store, jid) VALUES (?, ?)", store.key, jid)
		} else {
			store.exec("DELETE FROM whatsapp_blocked WHERE store = ? AND jid = ?", store.key, jid)
		}
	}
}

// Block blocks a person on WhatsApp (recipient: a number's digits, or a jid).
func (b *Bridge) Block(ctx context.Context, recipient string) error {
	client, store, sender := b.parts()
	if sender == nil || !b.open() {
		return ErrNotConnected
	}
	defer b.handling.RUnlock()
	jids, err := sender.jidsOf(recipient)
	if err != nil {
		return err
	}
	list, err := client.UpdateBlocklist(ctx, jids[0], events.BlocklistChangeActionBlock)
	if err != nil {
		return err
	}
	return store.setBlocklist(list.JIDs)
}

// ReportSpam blocks the person on WhatsApp and imports the blocklist as it is now.
func (p Plugin) ReportSpam(ctx context.Context, c *plugins.Context, conv plugins.Conversation) error {
	b := Running(storeDir(c))
	if b == nil {
		return errs.Plugin("not connected to WhatsApp", 503)
	}
	if err := b.Block(ctx, recipient(conv.Key)); err != nil {
		return err
	}
	return runImport(c)
}

// Forget drops what the bridge keeps of the chat (under its number and its LID), with the files it
// downloaded that the archive has not taken yet.
func (p Plugin) Forget(c *plugins.Context, conv plugins.Conversation) (err error) {
	defer db.Recover(&err)
	d, err := keys.Open()
	if err != nil {
		return err
	}
	if !db.Exists(d, "SELECT 1 FROM sqlite_master WHERE name = 'whatsapp_chat'") {
		return nil // never connected: nothing kept
	}
	store := key(storeDir(c))
	jids := chatJIDs(recipient(conv.Key))
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var files []string
	for _, j := range jids {
		files = append(files, db.Strs(tx, "SELECT path FROM whatsapp_media WHERE store = ? AND chat_jid = ? AND coalesce(path, '') != ''",
			store, j)...)
		for _, t := range []string{"whatsapp_media", "whatsapp_recent", "whatsapp_pending", "whatsapp_vote", "whatsapp_unreadable"} {
			db.Exec(tx, "DELETE FROM "+t+" WHERE store = ? AND chat_jid = ?", store, j)
		}
		db.Exec(tx, "DELETE FROM whatsapp_call WHERE store = ? AND chat_jid = ?", store, j)
		db.Exec(tx, "DELETE FROM whatsapp_chat WHERE store = ? AND jid = ?", store, j)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, f := range files {
		if p := localFile(storeDir(c), f); p != "" {
			os.Remove(p)
		}
	}
	return nil
}

// chatJIDs are the jids a one-to-one chat may be kept under: its number's and its LID, as the
// device's store maps them.
func chatJIDs(recipient string) []string {
	var pn, lid string
	if strings.HasSuffix(recipient, "@lid") {
		lid = strings.TrimSuffix(recipient, "@lid")
	} else {
		pn = strings.TrimSuffix(recipient, "@s.whatsapp.net")
	}
	if d, err := keys.Open(); err == nil {
		func() {
			defer func() { recover() }() // no map (a store never linked): the key's own jid only
			if pn != "" {
				lid = db.Str(d, "SELECT lid FROM whatsmeow_lid_map WHERE pn = ?", pn)
			} else {
				pn = db.Str(d, "SELECT pn FROM whatsmeow_lid_map WHERE lid = ?", lid)
			}
		}()
	}
	var out []string
	if pn != "" {
		out = append(out, pn+"@s.whatsapp.net")
	}
	if lid != "" {
		out = append(out, lid+"@lid")
	}
	return out
}
