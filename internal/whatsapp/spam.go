package whatsapp

// Spam and the people the user blocked: someone removed as spam in the app is blocked on WhatsApp
// (whatsmeow cannot report: only the phone can), and their chat dropped from messages.db (Forget);
// the account's blocklist is kept in messages.db (`blocklist`, from every device), for the importer
// to bring into the archive, where the app suggests removing them.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

func (store *MessageStore) migrateBlocklist() error {
	_, err := store.db.Exec(`
		CREATE TABLE IF NOT EXISTS blocklist (    -- the people the account blocked, as WhatsApp last said
			jid TEXT PRIMARY KEY
		);
	`)
	return err
}

// setBlocklist keeps the whole blocklist as WhatsApp gives it.
func (store *MessageStore) setBlocklist(jids []types.JID) error {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM blocklist"); err != nil {
		return err
	}
	for _, j := range jids {
		if _, err := tx.Exec("INSERT OR IGNORE INTO blocklist (jid) VALUES (?)", j.ToNonAD().String()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// refreshBlocklist asks WhatsApp for the blocklist and keeps it.
func refreshBlocklist(client *whatsmeow.Client, store *MessageStore, logger waLog.Logger) {
	list, err := client.GetBlocklist(context.Background())
	if err == nil {
		err = store.setBlocklist(list.JIDs)
	}
	if err != nil {
		logger.Warnf("Failed to read the blocklist: %v", err)
	}
}

// handleBlocklist: someone blocked or unblocked on any device; "modify": the list is to be asked for again.
func handleBlocklist(client *whatsmeow.Client, store *MessageStore, v *events.Blocklist, logger waLog.Logger) {
	if v.Action == events.BlocklistActionModify || len(v.Changes) == 0 {
		refreshBlocklist(client, store, logger)
		return
	}
	for _, c := range v.Changes {
		jid := c.JID.ToNonAD().String()
		if c.Action == events.BlocklistChangeActionBlock {
			store.db.Exec("INSERT OR IGNORE INTO blocklist (jid) VALUES (?)", jid)
		} else {
			store.db.Exec("DELETE FROM blocklist WHERE jid = ?", jid)
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

// Forget drops the chat from messages.db (under its number and its LID), with its downloaded files.
func (p Plugin) Forget(c *plugins.Context, conv plugins.Conversation) (err error) {
	defer db.Recover(&err)
	messages, devices := paths(c)
	if _, err := os.Stat(messages); err != nil {
		return nil // no store: nothing kept
	}
	jids := chatJIDs(devices, recipient(conv.Key))
	store, err := OpenMessages(storeDir(c))
	if err != nil {
		return err
	}
	defer store.Close()
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var files []string
	for _, j := range jids {
		files = append(files, db.Strs(tx, "SELECT media_path FROM messages WHERE chat_jid = ? AND coalesce(media_path, '') != ''", j)...)
		db.Exec(tx, "DELETE FROM call_participants WHERE call_id IN (SELECT id FROM calls WHERE chat_jid = ?)", j)
		for _, t := range []string{"messages", "reactions", "receipts", "calls"} {
			db.Exec(tx, "DELETE FROM "+t+" WHERE chat_jid = ?", j)
		}
		db.Exec(tx, "DELETE FROM chats WHERE jid = ?", j)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, f := range files {
		if filepath.IsLocal(f) {
			os.Remove(filepath.Join(storeDir(c), f))
		}
	}
	return nil
}

// chatJIDs are the jids a one-to-one chat may be kept under: its number's and its LID, as the
// device's store maps them.
func chatJIDs(devices, recipient string) []string {
	var pn, lid string
	if strings.HasSuffix(recipient, "@lid") {
		lid = strings.TrimSuffix(recipient, "@lid")
	} else {
		pn = strings.TrimSuffix(recipient, "@s.whatsapp.net")
	}
	if d, err := sql.Open("sqlite", readOnly(devices)); err == nil {
		func() {
			defer d.Close()
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
