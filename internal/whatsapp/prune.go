package whatsapp

// messages.db keeps a message only until the archive has it (importers.WhatsAppPrune takes out what
// the archive has, written more than a week ago): what the bridge itself needs of the older ones
// stays in tables of state. `stored` is when each row of those tables was last written, kept by
// triggers whatever writes it, so that a row changed after an import read it waits for the next
// import before it may go; incoming_chats says that someone wrote in a chat (the bridge sends only
// there), once their messages are gone; pending_reads keeps a read on another device of a message
// the store no longer has, for the importer to move the chat's read state.

import (
	"fmt"
	"strings"
)

// pruned are the tables whose rows leave once the archive has them.
var pruned = []string{"messages", "reactions", "receipts", "chat_events", "group_event", "calls", "pending_changes", "pending_reads"}

func (store *MessageStore) migratePrune() error {
	if _, err := store.db.Exec(`
		CREATE TABLE IF NOT EXISTS pending_reads (   -- read on another device, of a message the store no longer has
			chat_jid TEXT,
			id TEXT,
			read_at TIMESTAMP,
			PRIMARY KEY (chat_jid, id)
		);
		CREATE TABLE IF NOT EXISTS stored (          -- when each row of the pruned tables was last written
			tbl TEXT,
			rid INTEGER,
			at INTEGER NOT NULL,                     -- Unix seconds
			PRIMARY KEY (tbl, rid)
		) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS incoming_chats (  -- the chats where someone wrote (the bridge sends only there)
			jid TEXT PRIMARY KEY
		)`); err != nil {
		return err
	}
	for _, t := range pruned {
		var had int
		store.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?", t+"_stored_insert").Scan(&had)
		for _, on := range []string{"INSERT", "UPDATE"} {
			if _, err := store.db.Exec(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %[1]s_stored_%[2]s AFTER %[3]s ON %[1]s
				BEGIN INSERT INTO stored VALUES ('%[1]s', NEW.rowid, unixepoch()) ON CONFLICT (tbl, rid) DO UPDATE SET at = excluded.at; END`,
				t, strings.ToLower(on), on)); err != nil {
				return err
			}
		}
		if _, err := store.db.Exec(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %[1]s_stored_delete AFTER DELETE ON %[1]s
			BEGIN DELETE FROM stored WHERE tbl = '%[1]s' AND rid = OLD.rowid; END`, t)); err != nil {
			return err
		}
		if had == 0 { // rows from before: kept a week from now
			if _, err := store.db.Exec(fmt.Sprintf("INSERT OR IGNORE INTO stored SELECT '%s', rowid, unixepoch() FROM %s", t, t)); err != nil {
				return err
			}
		}
	}
	if _, err := store.db.Exec(`INSERT OR IGNORE INTO incoming_chats SELECT DISTINCT chat_jid FROM messages WHERE NOT is_from_me;
		CREATE TRIGGER IF NOT EXISTS messages_incoming AFTER INSERT ON messages WHEN NOT NEW.is_from_me
			BEGIN INSERT OR IGNORE INTO incoming_chats VALUES (NEW.chat_jid); END`); err != nil {
		return err
	}
	return nil
}
