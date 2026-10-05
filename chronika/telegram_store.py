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
"""


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
