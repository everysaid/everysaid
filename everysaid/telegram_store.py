"""`<cache>/telegram/telegram.db`: what Telegram's API gave, each message whole (Telethon's own
fields as JSON). Written by `scripts/telegram-sync.py` and by the live connection
(`plugins/telegram_live.py`); read by the importer (`telegram.py`)."""
from datetime import datetime
import json
import os
import sqlite3

from .telegram import DB

SCHEMA = """
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
"""


def note_read(db, chat_id, inbox=None, outbox=None, seen_now=False):
    """How far a chat was read (either may be None: not said). seen_now: the outbox moved now, seen live.
    Returns whether either moved on."""
    now = int(datetime.now().timestamp())
    was = db.execute("SELECT coalesce(inbox, 0), coalesce(outbox, 0) FROM chat_read WHERE chat_id = ?", (chat_id,)).fetchone()
    moved = not was or (inbox or 0) > was[0] or (outbox or 0) > was[1]
    db.execute("INSERT INTO chat_read (chat_id, observed_at) VALUES (?, ?) ON CONFLICT (chat_id) DO NOTHING",
               (chat_id, now))
    if inbox is not None:
        db.execute("UPDATE chat_read SET inbox = max(coalesce(inbox, 0), ?), observed_at = ? WHERE chat_id = ?",
                   (inbox, now, chat_id))
    if outbox is not None:
        # moved on: seen now, its time is now; else when is not known (an earlier time would be wrong
        # for the messages read since)
        db.execute("UPDATE chat_read SET outbox_at = CASE WHEN ? <= coalesce(outbox, 0) THEN outbox_at "
                   "WHEN ? THEN ? ELSE NULL END, outbox = max(coalesce(outbox, 0), ?), observed_at = ? WHERE chat_id = ?",
                   (outbox, int(seen_now), now, outbox, now, chat_id))
    return moved


def open_store(path=DB):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    db = sqlite3.connect(path)
    db.execute("PRAGMA journal_mode = WAL")
    db.execute("PRAGMA busy_timeout = 30000")
    db.executescript(SCHEMA)
    return db


def entity_kind(e):
    """user, saved, bot, group, supergroup or channel, from a Telethon entity."""
    t = type(e).__name__
    if t == "User":
        return "saved" if getattr(e, "is_self", False) else "bot" if getattr(e, "bot", False) else "user"
    if t in ("Channel", "ChannelForbidden"):
        return "channel" if getattr(e, "broadcast", False) else "supergroup"
    return "group"


def plain(value):
    """Telethon's to_dict() made JSON: dates as Unix seconds, binary fields left out."""
    if isinstance(value, dict):
        return {k: plain(v) for k, v in value.items() if not isinstance(v, bytes)}
    if isinstance(value, list):
        return [plain(v) for v in value if not isinstance(v, bytes)]
    if isinstance(value, datetime):
        return int(value.timestamp())
    return value


def dump(obj):
    return json.dumps(plain(obj.to_dict()), ensure_ascii=False, separators=(",", ":"))
