// Package telegramstore ports the store part of everysaid/telegram_store.py: what Telegram's API
// gave, each message whole (its fields as JSON), in keys.db's tables of the prefix telegram_ (the
// peers' access hashes are in it). Written by the sync and the live connection; read by the
// importer (importers.Telegram).
package telegramstore

import (
	"database/sql"
	"path/filepath"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/keys"
)

const Schema = `
CREATE TABLE IF NOT EXISTS telegram_chat (
    id INTEGER PRIMARY KEY,             -- Telethon's marked peer id (groups negative, -100... supergroups)
    kind TEXT NOT NULL,                 -- user, saved, group, supergroup
    title TEXT,
    archived INTEGER NOT NULL,
    json TEXT NOT NULL,                 -- the entity
    synced_at INTEGER
);
CREATE TABLE IF NOT EXISTS telegram_entity (    -- people (and chats) seen as senders, by marked peer id
    id INTEGER PRIMARY KEY,
    json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS telegram_message (
    chat_id INTEGER NOT NULL,
    id INTEGER NOT NULL,                -- unique only within the chat
    date INTEGER NOT NULL,              -- Unix seconds
    json TEXT NOT NULL,
    file TEXT,                          -- the downloaded media, relative to media/
    PRIMARY KEY (chat_id, id)           -- (not WITHOUT ROWID: its rows, a message's JSON, are larger
);                                      -- than such a table keeps in a page, and left most of each empty)
CREATE TABLE IF NOT EXISTS telegram_chat_read (     -- how far each chat was read, as Telegram says
    chat_id INTEGER PRIMARY KEY,
    inbox INTEGER,                      -- the owner read up to this message (on any device)
    outbox INTEGER,                     -- the others read the owner's messages up to this one
    outbox_at INTEGER,                  -- when it last moved, Unix s, where the live connection saw it happen
    observed_at INTEGER NOT NULL        -- Unix s
);
CREATE TABLE IF NOT EXISTS telegram_deleted (       -- messages deleted on Telegram, as the live connection saw it
    chat_id INTEGER NOT NULL,
    id INTEGER NOT NULL,
    at INTEGER NOT NULL,                -- when it was seen, Unix s
    PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS telegram_chat_member (   -- the members of each group, as Telegram last gave them (people in entity)
    chat_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    PRIMARY KEY (chat_id, user_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS telegram_chat_member_list ( -- when each group's members were last asked for
    chat_id INTEGER PRIMARY KEY,
    complete INTEGER NOT NULL,          -- Telegram gave all of them (else only some: hidden members, refused)
    count INTEGER,                      -- how many Telegram says the group has
    fetched_at INTEGER NOT NULL         -- Unix s
);
CREATE TABLE IF NOT EXISTS telegram_read_through (   -- each chat's history read with no gap up to this message
    chat_id INTEGER PRIMARY KEY,            -- (live messages do not move it: one may have been missed below)
    id INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS telegram_migrated (       -- the basic group each supergroup was (0: none), as Telegram said
    channel_id INTEGER PRIMARY KEY,         -- the supergroup's marked id
    chat_id INTEGER NOT NULL,               -- the basic group's id (unmarked), 0 if none
    max_id INTEGER NOT NULL                 -- its last message
);
CREATE TABLE IF NOT EXISTS telegram_read_by (        -- who read each of the owner's messages in a group, as Telegram says
    chat_id INTEGER NOT NULL,               -- (it says so for a week, in groups of up to a hundred)
    id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    at INTEGER NOT NULL,                    -- Unix s
    PRIMARY KEY (chat_id, id, user_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS telegram_poll (           -- which message has each poll (a vote's update names only the poll)
    poll_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL,
    id INTEGER NOT NULL,
    PRIMARY KEY (poll_id, chat_id, id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS telegram_photo (          -- the photos seen of each user and group, a row for each
    peer_id INTEGER NOT NULL,               -- marked id
    photo_id INTEGER NOT NULL,              -- Telegram's; 0: none
    file TEXT,                              -- the file, relative to media/ (gone once the archive has it)
    sha256 TEXT,                            -- its content's
    first_seen INTEGER NOT NULL,            -- Unix s
    last_seen INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS telegram_photo_peer ON telegram_photo (peer_id, first_seen);
CREATE TRIGGER IF NOT EXISTS telegram_message_poll_insert AFTER INSERT ON telegram_message
    WHEN json_extract(new.json, '$.media.poll.id') IS NOT NULL BEGIN
    INSERT OR IGNORE INTO telegram_poll VALUES (json_extract(new.json, '$.media.poll.id'), new.chat_id, new.id); END;
CREATE TRIGGER IF NOT EXISTS telegram_message_poll_update AFTER UPDATE OF json ON telegram_message
    WHEN json_extract(new.json, '$.media.poll.id') IS NOT NULL BEGIN
    INSERT OR IGNORE INTO telegram_poll VALUES (json_extract(new.json, '$.media.poll.id'), new.chat_id, new.id); END;
`

// DB is where the store is: keys.db, its tables those of the prefix telegram_. Media is the folder
// of its downloaded files.
func DB() string    { return keys.Path() }
func Media() string { return filepath.Join(config.Cache, "telegram", "media") }

// Open opens the store in the keys database at path ("" for keys.db), its tables made when new: a
// pool of its own, which the caller closes. Its transactions begin as writers': one that reads, then
// writes (a deletion, a read) while the live connection or a sync writes, waits rather than fails.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		path = DB()
	}
	d, err := keys.Own(path, true)
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
	found := db.Row(q, "SELECT coalesce(inbox, 0), coalesce(outbox, 0) FROM telegram_chat_read WHERE chat_id = ?",
		[]any{chatID}, &wasIn, &wasOut)
	moved = !found || (inbox != nil && *inbox > wasIn) || (outbox != nil && *outbox > wasOut)
	db.Exec(q, "INSERT INTO telegram_chat_read (chat_id, observed_at) VALUES (?, ?) ON CONFLICT (chat_id) DO NOTHING", chatID, now)
	if inbox != nil {
		db.Exec(q, "UPDATE telegram_chat_read SET inbox = max(coalesce(inbox, 0), ?), observed_at = ? WHERE chat_id = ?",
			*inbox, now, chatID)
	}
	if outbox != nil {
		// moved on: seen now, its time is now; else when is not known (an earlier time would be wrong
		// for the messages read since)
		db.Exec(q, "UPDATE telegram_chat_read SET outbox_at = CASE WHEN ? <= coalesce(outbox, 0) THEN outbox_at "+
			"WHEN ? THEN ? ELSE NULL END, outbox = max(coalesce(outbox, 0), ?), observed_at = ? WHERE chat_id = ?",
			*outbox, db.B(seenNow), now, *outbox, now, chatID)
	}
	return moved, nil
}

// NoteMembers records the members Telegram gave for a group (user ids; their entities are the
// caller's). complete: all of them, which replace the list kept (those who left go away); else they
// are added to it, since the others may well still be there.
func NoteMembers(q db.Querier, chatID int64, users []int64, complete bool, count int) (err error) {
	defer db.Recover(&err)
	if complete {
		db.Exec(q, "DELETE FROM telegram_chat_member WHERE chat_id = ?", chatID)
	}
	for _, u := range users {
		db.Exec(q, "INSERT OR IGNORE INTO telegram_chat_member (chat_id, user_id) VALUES (?, ?)", chatID, u)
	}
	db.Exec(q, "INSERT INTO telegram_chat_member_list (chat_id, complete, count, fetched_at) VALUES (?, ?, ?, ?) "+
		"ON CONFLICT (chat_id) DO UPDATE SET complete = excluded.complete, count = excluded.count, fetched_at = excluded.fetched_at",
		chatID, db.B(complete), count, time.Now().Unix())
	return nil
}
