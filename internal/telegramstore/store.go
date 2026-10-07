// Package telegramstore ports the store part of everysaid/telegram_store.py:
// <cache>/telegram/telegram.db, what Telegram's API gave, each message whole (its fields as JSON).
// Written by the sync and the live connection; read by the importer (importers.Telegram).
package telegramstore

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/db"
)

const Schema = `
CREATE TABLE IF NOT EXISTS chat (
    id INTEGER PRIMARY KEY,             -- Telethon's marked peer id (groups negative, -100... supergroups)
    kind TEXT NOT NULL,                 -- user, saved, group, supergroup
    title TEXT,
    archived INTEGER NOT NULL,
    json TEXT NOT NULL,                 -- the entity
    synced_at INTEGER
);
CREATE TABLE IF NOT EXISTS entity (    -- people (and chats) seen as senders, by marked peer id
    id INTEGER PRIMARY KEY,
    json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS message (
    chat_id INTEGER NOT NULL,
    id INTEGER NOT NULL,                -- unique only within the chat
    date INTEGER NOT NULL,              -- Unix seconds
    json TEXT NOT NULL,
    file TEXT,                          -- the downloaded media, relative to media/
    PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS chat_read (     -- how far each chat was read, as Telegram says
    chat_id INTEGER PRIMARY KEY,
    inbox INTEGER,                      -- the owner read up to this message (on any device)
    outbox INTEGER,                     -- the others read the owner's messages up to this one
    outbox_at INTEGER,                  -- when it last moved, Unix s, where the live connection saw it happen
    observed_at INTEGER NOT NULL        -- Unix s
);
`

// DB is the store's path; Media the folder of its downloaded files.
func DB() string    { return filepath.Join(config.Cache, "telegram", "telegram.db") }
func Media() string { return filepath.Join(config.Cache, "telegram", "media") }

// Open opens the store at path ("" for DB()), made when new.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		path = DB()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	d, err := db.Open(path, "journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := d.Exec(Schema); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// NoteRead records how far a chat was read (inbox, outbox: nil where not said). seenNow: the outbox
// moved now, seen live. It says whether either moved on.
func NoteRead(q db.Querier, chatID int64, inbox, outbox *int64, seenNow bool) (moved bool, err error) {
	defer db.Recover(&err)
	now := time.Now().Unix()
	var wasIn, wasOut int64
	found := db.Row(q, "SELECT coalesce(inbox, 0), coalesce(outbox, 0) FROM chat_read WHERE chat_id = ?",
		[]any{chatID}, &wasIn, &wasOut)
	moved = !found || (inbox != nil && *inbox > wasIn) || (outbox != nil && *outbox > wasOut)
	db.Exec(q, "INSERT INTO chat_read (chat_id, observed_at) VALUES (?, ?) ON CONFLICT (chat_id) DO NOTHING", chatID, now)
	if inbox != nil {
		db.Exec(q, "UPDATE chat_read SET inbox = max(coalesce(inbox, 0), ?), observed_at = ? WHERE chat_id = ?",
			*inbox, now, chatID)
	}
	if outbox != nil {
		// moved on: seen now, its time is now; else when is not known (an earlier time would be wrong
		// for the messages read since)
		db.Exec(q, "UPDATE chat_read SET outbox_at = CASE WHEN ? <= coalesce(outbox, 0) THEN outbox_at "+
			"WHEN ? THEN ? ELSE NULL END, outbox = max(coalesce(outbox, 0), ?), observed_at = ? WHERE chat_id = ?",
			*outbox, db.B(seenNow), now, *outbox, now, chatID)
	}
	return moved, nil
}
