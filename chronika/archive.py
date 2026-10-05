"""The archive database: schema and the helpers the importers share.

One row per message whatever its source; `message_origin` records which source row it came from
(so that a later import knows what it has). Our ids are our own; `message.key` is the service's
own id where it has one (Viber token, WhatsApp stanza id, iMessage guid), unique per service, or
per conversation for services whose ids are only unique within a chat (`service.key_scope`;
`message.key_scope` is then the conversation). Messages without a key carry a `fingerprint`
(time, direction, kind and text), the way to tell a message seen before in sources without ids.
What a message carries beyond its text (the message it answers, reactions, edits, deletions,
forwarding, a star, a place) is in its own columns and in `reaction`, read by `extras.py`; the
source rows themselves are not kept: they are in the sources.

People: an `address` is one handle (a phone number or email, shared by every service, or an id,
username or name within one service); a `person` has one or more addresses, and may point to a
contact elsewhere. The owner's own handles are `account` rows, seeded from config `[owner] numbers`.

Sources belong to a `device`, whose period of use decides which copy of a record found on two
devices is kept; each source names the folder its media paths are relative to.

Pictures are not kept here for good: each `media` row stays as the record of what a message carried,
while the file itself either moves to a photo library (`library_link` says where) or is removed.

Plugins: every way the archive is fed or gives files away is a `plugin_instance`, of a kind:
`source` (messages, calls, people), `library` (where kept pictures go: a folder, immich) or
`contacts` (an address book). Sources hang from the instance that reads them; a library link names
the instance that holds the file. Contacts from an address book are in `contact`, joined to the
addresses they list.

Names: `handle_name` keeps every name a service has shown for a handle, of a kind (the
service's copy of the user's address book, a chat's name, a name people chose for themselves), with
when it was seen; `core/names.py` picks a person's name from them.

State: what the sources say about a conversation (archived, muted, pinned, read up to) is in
`state_report`, one row per source; what the user chose in the app, per chat, in `chat_state`;
settings in `setting`, so every device sees them. `core/queries.py` combines them.

Search: `message_fts` (by words) and `message_tri` (by trigrams, for parts of words) hold each
message's text folded (`text.fold()`: lower case, no accents, final sigma as sigma), written by
`add_message()`, not by a trigger (the fold is Python's).

The schema has a version (`PRAGMA user_version`), 1 until the first release: until then it changes
in place, without migrations.
"""
import hashlib
import os
import re
import sqlite3
import sys
import time

import phonenumbers

from . import config, text as text_mod

DB = os.path.join(config.DATA, "archive.db")   # in the home snapshots
# What can be made again lives in the cache: the iPhone's decrypted databases and new media
# (iphone-sync.py, from the encrypted backup). The archive's own media (media/<ab>/<sha256><ext>,
# hard links or copies of those) are in the media store, the data folder by default, since some
# exist nowhere else once their source is gone; they stay until they go to the photo library.
CACHE = config.CACHE
IPHONE_DATA = os.path.join(CACHE, "iphone")
MEDIA_ROOT = config.MEDIA_STORE     # media/<ab>/<sha256><ext>: in the data folder (some exist nowhere else)
APPLE_EPOCH = 978307200
# The devices' names, which source names start with ('iphone/sms'). Android exports
# (android-export.py) are one folder per phone, `<export>/<device>/android.db`; an export of the
# earlier layout, `<export>/<device>.db` with `mms-parts/` beside it, belongs to `[android] device`
# (the Android phone's: android.db).
IPHONE = config.get("iphone", "device", "iphone")
ANDROID = config.get("android", "device", "android")
TZ = config.TIMEZONE
VERSION = 1                     # until the first release: the schema changes in place (no migrations yet)

SERVICES = ("sms", "mms", "imessage", "rcs", "viber", "whatsapp", "phone", "facetime", "telegram",
            "messenger", "signal")
KEY_PER_CONVERSATION = ("telegram",)    # message ids unique only within a chat
# phone, email, sender (an SMS sender name), uri: the same for every service; id, username, name:
# within one service (a Viber member id, a WhatsApp LID, a Telegram user id or username, a name
# where a service gives nothing else).
ADDRESS_KINDS = ("phone", "email", "sender", "id", "username", "name", "uri")
SHARED_KINDS = ("phone", "email", "sender", "uri")
NAMELIKE = re.compile(r"[^\W\d_]")      # a name has a letter in it (else it is a number or a symbol)
MESSAGE_KINDS = ("text", "image", "video", "voice", "file", "sticker", "location", "contact",
                 "call", "system", "reaction")
# What `message.subtype`, `call.detail` and `call_member.outcome` may say; the service's own code
# goes beside them (`subtype_code`, `detail_code`, `outcome_code`, `reaction.code`), e.g. 'whatsapp:59'.
VOCABULARY = {
    "message.subtype": ("link", "gif", "video note", "deleted", "notice", "call", "invalid",
                        "group event", "poll", "pin", "location", "tapback"),
    "call.detail": ("missed", "unanswered", "rejected", "blocked", "busy", "failed"),
    "call_member.outcome": ("joined", "missed", "unanswered", "rejected", "busy", "failed"),
}

SCHEMA = """
CREATE TABLE IF NOT EXISTS service (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    key_scope TEXT NOT NULL DEFAULT 'service'   -- 'conversation': message ids unique only per chat
);
CREATE TABLE IF NOT EXISTS address_kind (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS message_kind (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS vocabulary (
    field TEXT NOT NULL,                -- 'message.subtype', 'call.detail', 'call_member.outcome'
    name TEXT NOT NULL,
    PRIMARY KEY (field, name)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS device (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,          -- e.g. 'iphone', 'acme', 'whatsapp-bridge'
    kind TEXT,                          -- ios, android, bridge, export...
    used_from INTEGER,                  -- Unix ms: when it was the device in use (NULL: not known)
    used_until INTEGER
);
CREATE TABLE IF NOT EXISTS plugin_instance (
    id INTEGER PRIMARY KEY,
    plugin TEXT NOT NULL,               -- the plugin's id, e.g. 'iphone-backup', 'telegram', 'immich'
    kind TEXT NOT NULL CHECK (kind IN ('source', 'library', 'contacts')),
    label TEXT NOT NULL,                -- the user's name for it, e.g. 'iPhone', 'Old phone'
    settings TEXT NOT NULL DEFAULT '{}',    -- JSON, as the plugin's settings schema says
    state TEXT NOT NULL DEFAULT '{}',   -- JSON: the plugin's own cursors
    enabled INTEGER NOT NULL DEFAULT 1,
    device_id INTEGER REFERENCES device,
    is_default INTEGER NOT NULL DEFAULT 0,  -- the library kept files go to unless the user says
    created_at INTEGER NOT NULL,        -- Unix seconds
    last_run INTEGER,                   -- Unix seconds of the last import or connection
    last_status TEXT,                   -- what the last run said (ok, or the error)
    UNIQUE (plugin, label)
);
CREATE TABLE IF NOT EXISTS source (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,          -- e.g. 'iphone/sms', 'acme/sms'
    path TEXT NOT NULL,
    imported_at INTEGER,                -- Unix seconds of the last import
    device_id INTEGER REFERENCES device,
    media_root TEXT,                    -- the folder attachment.source_path is relative to; {cache}, {data}
    instance_id INTEGER REFERENCES plugin_instance  -- the plugin instance that reads it
);
CREATE TABLE IF NOT EXISTS address (
    id INTEGER PRIMARY KEY,
    kind_id INTEGER NOT NULL REFERENCES address_kind,
    value TEXT NOT NULL,                -- normalised: +E.164 or a short code, lower-case email, sender name
    service_id INTEGER REFERENCES service   -- NULL for phone, email, sender, uri
);
CREATE UNIQUE INDEX IF NOT EXISTS address_unique ON address (kind_id, value, ifnull(service_id, 0));
CREATE TABLE IF NOT EXISTS person (
    id INTEGER PRIMARY KEY,
    name TEXT,                          -- set by the owner; else names come from the contact
    contact_uid TEXT,                   -- the vCard UID of their contact
    contact_url TEXT,                   -- where that contact lives (a CardDAV href, any provider)
    note TEXT,
    name_source TEXT                    -- where the name comes from when the user pinned one: a source of
                                        -- names ('contacts', 'whatsapp/book'), or 'address:<id>'; NULL: by order
);
CREATE TABLE IF NOT EXISTS person_address (
    address_id INTEGER PRIMARY KEY REFERENCES address,
    person_id INTEGER NOT NULL REFERENCES person,
    how TEXT NOT NULL DEFAULT 'auto'    -- auto: one person per new address; manual: merged by the owner
);
CREATE INDEX IF NOT EXISTS person_address_person ON person_address (person_id);
CREATE TABLE IF NOT EXISTS handle_name (    -- every name a service has shown for a handle; many may share one
    address_id INTEGER NOT NULL REFERENCES address,
    service_id INTEGER NOT NULL REFERENCES service,
    kind TEXT NOT NULL CHECK (kind IN ('book', 'chat', 'profile')),  -- book: the service's copy of the
                                        -- user's address book; chat: a chat's name; profile: chosen by them
    name TEXT NOT NULL,
    first_seen INTEGER NOT NULL,        -- Unix seconds
    last_seen INTEGER NOT NULL,
    current INTEGER NOT NULL DEFAULT 1, -- the latest of this handle, service and kind
    PRIMARY KEY (address_id, service_id, kind, name)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS merge_dismissed (    -- suggested merges the user turned down
    a INTEGER NOT NULL REFERENCES person,       -- a < b
    b INTEGER NOT NULL REFERENCES person,
    at INTEGER NOT NULL,
    PRIMARY KEY (a, b)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS account (    -- the owner's own handles
    id INTEGER PRIMARY KEY,
    address_id INTEGER NOT NULL REFERENCES address,
    service_id INTEGER REFERENCES service,  -- NULL: every service that uses this handle
    label TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS account_unique ON account (address_id, ifnull(service_id, 0));
CREATE TABLE IF NOT EXISTS conversation (
    id INTEGER PRIMARY KEY,
    service_id INTEGER NOT NULL REFERENCES service,
    key TEXT NOT NULL,                  -- the members' addresses, or the service's chat id
    title TEXT,
    is_group INTEGER NOT NULL DEFAULT 0,
    UNIQUE (service_id, key)
);
CREATE TABLE IF NOT EXISTS conversation_member (
    conversation_id INTEGER NOT NULL REFERENCES conversation,
    address_id INTEGER NOT NULL REFERENCES address,
    PRIMARY KEY (conversation_id, address_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS message (
    id INTEGER PRIMARY KEY,
    service_id INTEGER NOT NULL REFERENCES service,
    conversation_id INTEGER NOT NULL REFERENCES conversation,
    ts INTEGER NOT NULL,                -- Unix milliseconds, UTC
    outgoing INTEGER NOT NULL,
    sender_id INTEGER REFERENCES address,   -- NULL when outgoing
    kind_id INTEGER NOT NULL REFERENCES message_kind,
    text TEXT,
    key TEXT,                           -- the service's own id; NULL for SMS and MMS
    key_scope INTEGER REFERENCES conversation,  -- the conversation, where the key is unique only there
    fingerprint TEXT,                   -- messages without a key: see fingerprint()
    subtype TEXT,                       -- the service's finer kind (vocabulary): link, gif, video note, poll...
    subtype_code TEXT,                  -- the service's own code for it, e.g. 'whatsapp:54'
    reply_to INTEGER REFERENCES message,    -- the message this one answers
    reply_key TEXT,                     -- its key, also when it is not in the archive
    reply_text TEXT,                    -- the quoted text, kept only when it is not
    edited INTEGER NOT NULL DEFAULT 0,
    deleted INTEGER NOT NULL DEFAULT 0, -- deleted by its sender: what is left is the notice
    forwarded INTEGER NOT NULL DEFAULT 0,
    starred INTEGER NOT NULL DEFAULT 0,
    lat REAL, lon REAL, place TEXT,     -- a location shared
    sender_lat REAL, sender_lon REAL,   -- where the sender was when sending (older Viber)
    status TEXT                         -- a message sent from the app: sending, sent, failed (NULL: as imported)
);
CREATE UNIQUE INDEX IF NOT EXISTS message_key ON message (service_id, key, ifnull(key_scope, 0));
CREATE INDEX IF NOT EXISTS message_conversation_ts ON message (conversation_id, ts);
CREATE INDEX IF NOT EXISTS message_ts ON message (ts);
CREATE INDEX IF NOT EXISTS message_fingerprint ON message (conversation_id, fingerprint)
    WHERE fingerprint IS NOT NULL;
CREATE TABLE IF NOT EXISTS reaction (
    message_id INTEGER NOT NULL REFERENCES message,
    emoji TEXT,                         -- NULL where only the service's code is known
    code TEXT,                          -- the service's own code, e.g. 'viber:6', 'imessage:2000'
    count INTEGER NOT NULL DEFAULT 1,   -- how many reacted so (Viber gives only counts)
    address_id INTEGER REFERENCES address,  -- who, where the service says
    outgoing INTEGER                    -- 1: the owner's own reaction
);
CREATE INDEX IF NOT EXISTS reaction_message ON reaction (message_id);
CREATE TABLE IF NOT EXISTS message_origin (
    source_id INTEGER NOT NULL REFERENCES source,
    row_key TEXT NOT NULL,              -- the row's id within that source
    message_id INTEGER NOT NULL REFERENCES message,
    PRIMARY KEY (source_id, row_key)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS message_origin_message ON message_origin (message_id);
CREATE TABLE IF NOT EXISTS call (
    id INTEGER PRIMARY KEY,
    service_id INTEGER NOT NULL REFERENCES service,
    address_id INTEGER REFERENCES address,  -- NULL for hidden numbers
    ts INTEGER NOT NULL,                -- Unix milliseconds, UTC
    outgoing INTEGER NOT NULL,
    answered INTEGER NOT NULL,          -- connected: picked up, or for outgoing calls a duration > 0
    duration INTEGER NOT NULL,          -- seconds
    key TEXT,
    detail TEXT,                        -- vocabulary: missed, unanswered, rejected, blocked, busy, failed
    video INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 1,    -- calls the carrier counted as one notice ("3 ΦΟΡΕΣ")
    conversation_id INTEGER REFERENCES conversation,    -- a group call: its group
    detail_code TEXT,                   -- the source's own code, e.g. 'android:5', 'whatsapp:4'
    UNIQUE (service_id, key)
);
CREATE TABLE IF NOT EXISTS call_member (     -- who took part in a group call, and how
    call_id INTEGER NOT NULL REFERENCES call,
    address_id INTEGER REFERENCES address,
    outcome TEXT,
    outcome_code TEXT,
    UNIQUE (call_id, address_id)
);
CREATE INDEX IF NOT EXISTS call_ts ON call (ts);
CREATE INDEX IF NOT EXISTS call_address ON call (address_id, ts);
CREATE TABLE IF NOT EXISTS call_origin (
    source_id INTEGER NOT NULL REFERENCES source,
    row_key TEXT NOT NULL,
    call_id INTEGER NOT NULL REFERENCES call,
    PRIMARY KEY (source_id, row_key)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS call_origin_call ON call_origin (call_id);
CREATE TABLE IF NOT EXISTS viber_member (    -- Viber member id -> phone number, as the sources said
    mid TEXT PRIMARY KEY,
    number TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS blocked (   -- numbers the owner blocked on a phone
    address_id INTEGER NOT NULL REFERENCES address,
    phone TEXT NOT NULL,                -- the device it was blocked on
    original TEXT,                      -- as the phone wrote it
    UNIQUE (address_id, phone)
);
CREATE TABLE IF NOT EXISTS media (
    sha256 TEXT PRIMARY KEY,
    size INTEGER NOT NULL,
    mime TEXT,
    path TEXT NOT NULL                  -- relative to the media root: media/ab/<sha256>.<ext>
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS attachment (
    id INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL REFERENCES message,
    sha256 TEXT NOT NULL REFERENCES media,
    source_id INTEGER NOT NULL REFERENCES source,
    source_path TEXT NOT NULL,          -- the file within that source (relative to its media_root)
    UNIQUE (source_id, source_path, message_id)
);
CREATE INDEX IF NOT EXISTS attachment_message ON attachment (message_id);
CREATE TABLE IF NOT EXISTS library_link (
    sha256 TEXT NOT NULL REFERENCES media,
    library TEXT NOT NULL,              -- the library's name, e.g. 'immich' (the instance's label)
    asset_id TEXT NOT NULL,             -- its id there (for a folder: the path within it)
    method TEXT NOT NULL,               -- how it was matched: checksum, phash, clip, upload
    score REAL,                         -- phash distance or CLIP similarity
    linked_at INTEGER NOT NULL,         -- Unix seconds; the local copies were removed then
    instance_id INTEGER REFERENCES plugin_instance,     -- the library plugin instance that holds it
    PRIMARY KEY (sha256, library)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS media_same (     -- a file removed as the same picture as one the archive keeps
    sha256 TEXT PRIMARY KEY REFERENCES media,
    same_as TEXT NOT NULL REFERENCES media,
    method TEXT NOT NULL,
    score REAL,
    linked_at INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS media_decision (  -- the user's choice about a file (sorting)
    sha256 TEXT PRIMARY KEY REFERENCES media,
    decision TEXT NOT NULL CHECK (decision IN ('keep', 'remove', 'library')),
    date_ms INTEGER,                    -- the date the user gave it (else EXIF, else the message's)
    at INTEGER NOT NULL                 -- Unix seconds: the newest decision is the one that counts
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS contact (    -- a contact from an address book (a `contacts` plugin instance)
    id INTEGER PRIMARY KEY,
    instance_id INTEGER NOT NULL REFERENCES plugin_instance,
    uid TEXT NOT NULL,                  -- the vCard UID
    url TEXT,                           -- where it lives (a CardDAV href)
    name TEXT,
    organization TEXT,
    photo TEXT,                         -- the photo's file in the cache (avatars/<sha256>.<ext>), if any
    updated_at INTEGER NOT NULL,
    UNIQUE (instance_id, uid)
);
CREATE TABLE IF NOT EXISTS contact_address (    -- the numbers and emails a contact lists
    contact_id INTEGER NOT NULL REFERENCES contact ON DELETE CASCADE,
    address_id INTEGER NOT NULL REFERENCES address,
    label TEXT,
    PRIMARY KEY (contact_id, address_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS contact_address_address ON contact_address (address_id);
CREATE TABLE IF NOT EXISTS state_report (   -- what a source says about a conversation's state
    conversation_id INTEGER NOT NULL REFERENCES conversation,
    instance_id INTEGER NOT NULL REFERENCES plugin_instance,
    field TEXT NOT NULL CHECK (field IN ('archived', 'muted', 'pinned', 'read_until')),
    value INTEGER NOT NULL,             -- archived, pinned: 0/1; muted: until (Unix ms, -1 for ever, 0 not);
                                        -- read_until: Unix ms
    observed_at INTEGER NOT NULL,       -- Unix ms: when the source's data was so (a backup's time)
    changed_at INTEGER NOT NULL,        -- Unix ms: when it became so (the service's, else first seen)
    PRIMARY KEY (conversation_id, instance_id, field)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS chat_state (     -- what the user chose in the app, per chat (p<person>, c<conversation>)
    chat TEXT NOT NULL,
    field TEXT NOT NULL CHECK (field IN ('archived', 'muted', 'pinned', 'read_until')),
    value INTEGER NOT NULL,             -- as in state_report (muted: 0/1)
    set_at INTEGER NOT NULL,            -- Unix ms; a later change by a service wins, unless `always`
    always INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chat, field)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS setting (    -- the user's settings that every device shares (JSON values)
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;
CREATE VIRTUAL TABLE IF NOT EXISTS message_fts USING fts5(
    text, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 2');
CREATE VIRTUAL TABLE IF NOT EXISTS message_tri USING fts5(     -- the same text in trigrams: parts of words
    text, content='', contentless_delete=1, tokenize='trigram');
"""


def _vocabulary_triggers():
    out = []
    for field in VOCABULARY:
        table, column = field.split(".")
        for when in ("INSERT", f"UPDATE OF {column}"):
            name = f"{table}_{column}_{when.split()[0].lower()}"
            out.append(
                f"CREATE TRIGGER IF NOT EXISTS {name} BEFORE {when} ON {table} "
                f"WHEN new.{column} IS NOT NULL AND NOT EXISTS (SELECT 1 FROM vocabulary "
                f"WHERE field = '{field}' AND name = new.{column}) "
                f"BEGIN SELECT RAISE(ABORT, '{field} not in the vocabulary'); END;")
    return "\n".join(out)


SCHEMA += _vocabulary_triggers()


def statements(script):
    """A script's statements one by one (triggers whole), to run inside one transaction."""
    out, buf = [], ""
    for line in script.splitlines(keepends=True):
        buf += line
        if sqlite3.complete_statement(buf):
            if buf.strip() and not buf.strip().startswith("--"):
                out.append(buf.strip())
            buf = ""
    return out


def seed(db):
    """The lookup tables' rows, and the owner's numbers from config as accounts."""
    for table, names in (("service", SERVICES), ("address_kind", ADDRESS_KINDS),
                         ("message_kind", MESSAGE_KINDS)):
        db.executemany(f"INSERT OR IGNORE INTO {table} (name) VALUES (?)", [(n,) for n in names])
    db.executemany("UPDATE service SET key_scope = 'conversation' WHERE name = ?",
                   [(n,) for n in KEY_PER_CONVERSATION])
    db.executemany("INSERT OR IGNORE INTO vocabulary VALUES (?, ?)",
                   [(f, n) for f, names in VOCABULARY.items() for n in names])


class Names(dict):
    """name -> id of a lookup table; a name not there yet is added."""

    def __init__(self, db, table):
        super().__init__(db.execute(f"SELECT name, id FROM {table}").fetchall())
        self.db, self.table = db, table

    def __missing__(self, name):
        self.db.execute(f"INSERT OR IGNORE INTO {self.table} (name) VALUES (?)", (name,))
        self[name] = self.db.execute(f"SELECT id FROM {self.table} WHERE name = ?", (name,)).fetchone()[0]
        return self[name]


def version(db):
    """The schema version (`PRAGMA user_version`): 0 for an empty database."""
    return db.execute("PRAGMA user_version").fetchone()[0]


class Archive:
    def __init__(self, path=DB):
        os.umask(0o077)
        os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
        self.db = sqlite3.connect(path)
        self.db.execute("PRAGMA foreign_keys = ON")
        self.db.execute("PRAGMA journal_mode = WAL")
        v = version(self.db)
        if v not in (0, VERSION):
            sys.exit(f"Το {path} έχει άγνωστο σχήμα (v{v}, γνωστό: v{VERSION}).")
        self.db.executescript(SCHEMA)
        seed(self.db)
        self.db.execute(f"PRAGMA user_version = {VERSION}")
        self.service = Names(self.db, "service")
        self.address_kind = Names(self.db, "address_kind")
        self.message_kind = Names(self.db, "message_kind")
        self.key_scoped = {sid for (sid,) in self.db.execute("SELECT id FROM service WHERE key_scope = 'conversation'")}
        self._addresses = {}
        self._conversations = {}
        self._devices = None
        self.pending_edits, self.pending_tapbacks = [], []
        for number in config.OWN_NUMBERS:
            self.account(address(number))
        self.db.commit()

    def source(self, name, path, device=None, media_root=None):
        """The source's id; device: the device it was read from (registered when new)."""
        device_id = self.device(device) if device else None
        self.db.execute("INSERT INTO source (name, path, device_id, media_root) VALUES (?, ?, ?, ?) "
                        "ON CONFLICT (name) DO UPDATE SET path = excluded.path, "
                        "device_id = coalesce(source.device_id, excluded.device_id), "
                        "media_root = coalesce(excluded.media_root, source.media_root)",
                        (name, path, device_id, contract(media_root) if media_root else None))
        return self.db.execute("SELECT id FROM source WHERE name = ?", (name,)).fetchone()[0]

    def device(self, name, kind=None):
        self.db.execute("INSERT OR IGNORE INTO device (name, kind) VALUES (?, ?)", (name, kind))
        self._devices = None
        return self.db.execute("SELECT id FROM device WHERE name = ?", (name,)).fetchone()[0]

    def keeper(self, devices, ts):
        """Of devices that hold copies of one record, the one whose copy is kept: the one in use at
        ts (Unix ms), else the most recent one (it carries the history copied from phone to phone),
        else the first named."""
        if self._devices is None:
            self._devices = {n: (f, u) for n, f, u in self.db.execute("SELECT name, used_from, used_until FROM device")}
        periods = [(d, *self._devices.get(d, (None, None))) for d in devices]
        for d, start, until in periods:
            if (start is not None or until is not None) and (start or 0) <= ts and (until is None or ts < until):
                return d
        dated = [(start, d) for d, start, _ in periods if start is not None]
        return max(dated)[1] if dated else devices[0]

    def imported(self, source_id):
        self.db.execute("UPDATE source SET imported_at = ? WHERE id = ?", (int(time.time()), source_id))

    def has_origin(self, source_id, row_key, table="message_origin"):
        return self.db.execute(f"SELECT 1 FROM {table} WHERE source_id = ? AND row_key = ?",
                               (source_id, row_key)).fetchone() is not None

    def record_pairs(self, sources, pairs, table="message_origin"):
        """A record found on both phones is kept once; its other copy is recorded as a second origin
        of the same row, so later imports know it without reading the other phone again.
        pairs: (kept rec, other rec) with .source and .row_key; sources: rec.source -> source id."""
        for kept, other in pairs:
            row = self.db.execute(f"SELECT * FROM {table} WHERE source_id = ? AND row_key = ?",
                                  (sources[kept.source], kept.row_key)).fetchone()
            if row:
                self.db.execute(f"INSERT OR IGNORE INTO {table} VALUES (?, ?, ?)",
                                (sources[other.source], other.row_key, row[2]))

    def address(self, kind, value, service=None):
        """The id of a handle; service only for the kinds that live within one (id, username, name).
        A new handle is a new person, until the owner merges people."""
        if kind in SHARED_KINDS:
            service = None
        elif service is None:
            raise ValueError(f"an address of kind {kind} needs its service")
        k = (kind, value, service)
        if k not in self._addresses:
            kid, sid = self.address_kind[kind], self.service[service] if service else None
            cur = self.db.execute("INSERT OR IGNORE INTO address (kind_id, value, service_id) VALUES (?, ?, ?)",
                                  (kid, value, sid))
            aid = self.db.execute("SELECT id FROM address WHERE kind_id = ? AND value = ? AND service_id IS ?",
                                  (kid, value, sid)).fetchone()[0]
            if cur.rowcount:
                pid = self.db.execute("INSERT INTO person DEFAULT VALUES").lastrowid
                self.db.execute("INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", (aid, pid))
            self._addresses[k] = aid
        return self._addresses[k]

    def known(self, handle):
        """Whether the archive has this handle ((kind, value[, service]))."""
        kind, value, service = (*handle, None)[:3]
        if kind in SHARED_KINDS:
            service = None
        if (kind, value, service) in self._addresses:
            return True
        return self.db.execute("SELECT 1 FROM address WHERE kind_id = ? AND value = ? AND service_id IS ?",
                               (self.address_kind[kind], value, self.service[service] if service else None)
                               ).fetchone() is not None

    def alias(self, handle, of):
        """Another handle of the person who has `of` (both (kind, value[, service])), such as a
        username or the name a service shows: new, it joins that person; already someone's, it is
        left as it is (merging people is the owner's)."""
        person = self.db.execute("SELECT person_id FROM person_address WHERE address_id = ?",
                                 (self.address(*of),)).fetchone()[0]
        kind, value, service = (*handle, None)[:3]
        if kind in SHARED_KINDS:
            service = None
        k = (kind, value, service)
        if k in self._addresses:
            return
        kid, sid = self.address_kind[kind], self.service[service] if service else None
        cur = self.db.execute("INSERT OR IGNORE INTO address (kind_id, value, service_id) VALUES (?, ?, ?)",
                              (kid, value, sid))
        aid = self.db.execute("SELECT id FROM address WHERE kind_id = ? AND value = ? AND service_id IS ?",
                              (kid, value, sid)).fetchone()[0]
        if cur.rowcount:
            self.db.execute("INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", (aid, person))
        self._addresses[k] = aid

    def handle_name(self, handle, service, name, kind, seen_at=None):
        """Record a name `service` shows for a handle the archive has ((kind, value[, service])):
        kind 'book' (its copy of the user's address book), 'chat' (a chat's name) or 'profile'
        (chosen by them). Not for the user's own handles, nor a "name" without a letter (a number)."""
        name = (name or "").strip()
        if not NAMELIKE.search(name) or not self.known(handle):
            return
        aid = self.address(*handle)
        if self.db.execute("SELECT 1 FROM account WHERE address_id = ?", (aid,)).fetchone():
            return
        sid, t = self.service[service], int(seen_at or time.time())
        self.db.execute("INSERT INTO handle_name (address_id, service_id, kind, name, first_seen, last_seen) "
                        "VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO UPDATE SET "
                        "first_seen = min(first_seen, excluded.first_seen), last_seen = max(last_seen, excluded.last_seen)",
                        (aid, sid, kind, name, t, t))
        # the current one: the latest seen of this handle, service and kind
        self.db.execute("UPDATE handle_name SET current = (name = (SELECT name FROM handle_name WHERE address_id = ? "
                        "AND service_id = ? AND kind = ? ORDER BY last_seen DESC LIMIT 1)) "
                        "WHERE address_id = ? AND service_id = ? AND kind = ?", (aid, sid, kind, aid, sid, kind))

    def find_conversation(self, service, key):
        """The id of a conversation of a service by its key, if the archive has it."""
        row = self.db.execute("SELECT id FROM conversation WHERE service_id = ? AND key = ?",
                              (self.service[service], key)).fetchone()
        return row[0] if row else None

    def report_state(self, source_id, conversation_id, field, value, observed_at, changed_at=None):
        """What a source says about a conversation: archived, muted (until, Unix ms; -1 for ever),
        pinned, read_until. observed_at: when its data was so (ms); changed_at: when it became so,
        if the service says; else the first time it was seen so."""
        if conversation_id is None:
            return
        iid = self.db.execute("SELECT instance_id FROM source WHERE id = ?", (source_id,)).fetchone()[0]
        if iid is None:
            return
        old = self.db.execute("SELECT value, observed_at FROM state_report WHERE conversation_id = ? AND "
                              "instance_id = ? AND field = ?", (conversation_id, iid, field)).fetchone()
        if old and old[1] > observed_at:
            return                      # older news than what is there
        if old and old[0] == value:
            self.db.execute("UPDATE state_report SET observed_at = ? WHERE conversation_id = ? AND instance_id = ? "
                            "AND field = ?", (observed_at, conversation_id, iid, field))
            return
        self.db.execute("INSERT OR REPLACE INTO state_report VALUES (?, ?, ?, ?, ?, ?)",
                        (conversation_id, iid, field, int(value), observed_at, changed_at or observed_at))

    def account(self, handle, service=None, label=None):
        """Record one of the owner's own handles ((kind, value[, service]) as address() gives)."""
        aid = self.address(*handle)
        self.db.execute("INSERT OR IGNORE INTO account (address_id, service_id, label) VALUES (?, ?, ?)",
                        (aid, self.service[service] if service else None, label))

    def own(self):
        """The owner's handles, as (kind, value) or (kind, value, service) the way the importers make them."""
        out = set()
        for kind, value, service in self.db.execute(
                "SELECT k.name, a.value, s.name FROM account x JOIN address a ON a.id = x.address_id "
                "JOIN address_kind k ON k.id = a.kind_id LEFT JOIN service s ON s.id = a.service_id"):
            out.add((kind, value, service) if service else (kind, value))
        return out

    def conversation(self, service, members, key=None, title=None):
        """members: (kind, value[, service]) handles; key defaults to the sorted member values."""
        sid = self.service[service]
        key = key or ",".join(sorted(m[1] for m in members))
        if (sid, key) not in self._conversations:
            self.db.execute("INSERT OR IGNORE INTO conversation (service_id, key, title, is_group) "
                            "VALUES (?, ?, ?, ?)", (sid, key, title, int(len(members) > 1)))
            cid = self.db.execute("SELECT id FROM conversation WHERE service_id = ? AND key = ?",
                                  (sid, key)).fetchone()[0]
            self.db.executemany("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)",
                                [(cid, self.address(*m)) for m in members])
            self._conversations[(sid, key)] = cid
        return self._conversations[(sid, key)]

    def message_by_key(self, service, key, conversation_id=None):
        """The id of the message with this key, within the conversation where the service needs it."""
        sid = self.service[service]
        scope = conversation_id if sid in self.key_scoped else None
        row = self.db.execute("SELECT id FROM message WHERE service_id = ? AND key = ? AND key_scope IS ?",
                              (sid, key, scope)).fetchone()
        return row[0] if row else None

    def add_message(self, source_id, row_key, *, service, conversation_id, ts, outgoing,
                    sender_id, kind, text, key=None, extras=None):
        """extras: what extras.py read from the source row (see there)."""
        x = extras or {}
        sid = self.service[service]
        text = x.get("text") or text
        lat, lon, place = x.get("lat"), x.get("lon"), x.get("place")
        position = (None, None) if kind == "location" else (lat, lon)    # on another kind: where the sender was
        if kind != "location":
            lat = lon = None
        cur = self.db.execute(
            "INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text, key, "
            "key_scope, fingerprint, subtype, subtype_code, reply_key, reply_text, edited, deleted, forwarded, "
            "starred, lat, lon, place, sender_lat, sender_lon) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (sid, conversation_id, ts, int(outgoing), sender_id, self.message_kind[kind], text, key,
             conversation_id if key is not None and sid in self.key_scoped else None,
             None if key is not None else fingerprint(ts, outgoing, kind, text),
             x.get("subtype"), x.get("subtype_code"), x.get("reply_key"), x.get("reply_text"),
             x.get("edited", 0), x.get("deleted", 0), x.get("forwarded", 0), x.get("starred", 0),
             lat, lon, place, *position))
        mid = cur.lastrowid
        if text:
            folded = text_mod.fold(text)
            self.db.execute("INSERT INTO message_fts (rowid, text) VALUES (?, ?)", (mid, folded))
            self.db.execute("INSERT INTO message_tri (rowid, text) VALUES (?, ?)", (mid, folded))
        self.db.execute("INSERT INTO message_origin VALUES (?, ?, ?)", (source_id, row_key, mid))
        self.add_reactions(mid, x.get("reactions"))
        if x.get("edits_key"):
            self.pending_edits.append((sid, x["edits_key"]))
        if x.get("reacts_to"):
            self.pending_tapbacks.append((sid, *x["reacts_to"], sender_id, int(outgoing)))
        return mid

    def add_reactions(self, message_id, reactions):
        """reactions: (emoji or None, code or None, count, sender (kind, value[, service]) or
        address id or None, outgoing or None)."""
        for emoji, code, count, who, outgoing in reactions or ():
            if who == "peer":               # the other person of a one-to-one chat
                members = self.db.execute(
                    "SELECT cm.address_id FROM message m JOIN conversation c ON c.id = m.conversation_id "
                    "JOIN conversation_member cm ON cm.conversation_id = c.id WHERE m.id = ? AND NOT c.is_group",
                    (message_id,)).fetchall()
                who = members[0][0] if len(members) == 1 else None
            elif isinstance(who, tuple):
                who = self.address(*who)
            self.db.execute("INSERT INTO reaction (message_id, emoji, code, count, address_id, outgoing) "
                            "VALUES (?, ?, ?, ?, ?, ?)", (message_id, emoji, code, count or 1, who, outgoing))

    def init_archived(self):
        """The app's own "archived", for chats it has not set yet that a source has reported on: a
        person's chat archived if every one of their conversations a source reports on is archived
        there (one in view keeps them in view), a group if it is. From then on it is the app's alone:
        what the services do later does not change it. Nothing is overwritten."""
        db = self.db
        own = {r[0] for r in db.execute("SELECT address_id FROM account")}
        person = dict(db.execute("SELECT address_id, person_id FROM person_address"))
        me = {person[a] for a in own if a in person}
        newest = {}         # conversation -> archived, as its newest report says
        for conv, value in db.execute("SELECT conversation_id, value FROM state_report WHERE field = 'archived' "
                                      "ORDER BY observed_at"):
            newest[conv] = value
        if not newest:
            return
        members = {}
        for conv, aid in db.execute("SELECT conversation_id, address_id FROM conversation_member"):
            members.setdefault(conv, []).append(aid)
        chats = {}          # chat id -> [archived per reported conversation]
        for conv, is_group in db.execute("SELECT id, is_group FROM conversation"):
            if conv not in newest:
                continue
            others = {person.get(a) for a in members.get(conv, []) if a not in own} - {None} - me
            chat = f"p{others.pop()}" if not is_group and len(others) == 1 else f"c{conv}"
            chats.setdefault(chat, []).append(newest[conv])
        now = int(time.time() * 1000)
        db.executemany("INSERT OR IGNORE INTO chat_state (chat, field, value, set_at) VALUES (?, 'archived', ?, ?)",
                       [(chat, int(all(vs)), now) for chat, vs in chats.items()])

    def resolve(self):
        """After an import: link replies to the messages they answer (keeping the quoted text only
        where that is not in the archive), mark the messages that edit events edited, and turn
        reactions sent as messages (tapbacks) into reactions on their message; and start the app's own
        "archived" for chats new to it (init_archived)."""
        self.init_archived()
        db = self.db
        db.execute("UPDATE message SET reply_to = (SELECT t.id FROM message t WHERE t.service_id = "
                   "message.service_id AND t.key = message.reply_key AND t.key_scope IS message.key_scope) "
                   "WHERE reply_key IS NOT NULL AND reply_to IS NULL")
        db.execute("UPDATE message SET reply_text = NULL WHERE reply_to IS NOT NULL AND reply_text IS NOT NULL")
        for service_id, key in self.pending_edits:
            db.execute("UPDATE message SET edited = 1 WHERE service_id = ? AND key = ?", (service_id, key))
        for service_id, key, emoji, code, sender_id, outgoing in self.pending_tapbacks:
            target = db.execute("SELECT id FROM message WHERE service_id = ? AND key = ?", (service_id, key)).fetchone()
            if target:
                db.execute("INSERT INTO reaction (message_id, emoji, code, count, address_id, outgoing) "
                           "VALUES (?, ?, ?, 1, ?, ?)", (target[0], emoji, code, sender_id, outgoing))
        self.pending_edits, self.pending_tapbacks = [], []

    def add_call(self, source_id, row_key, *, service, address_id, ts, outgoing, answered,
                 duration, key=None, detail=None, detail_code=None, video=0, attempts=1, conversation_id=None):
        cur = self.db.execute(
            "INSERT INTO call (service_id, address_id, ts, outgoing, answered, duration, key, detail, detail_code, "
            "video, attempts, conversation_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (self.service[service], address_id, ts, int(outgoing), int(answered), duration, key, detail,
             detail_code, int(bool(video)), attempts or 1, conversation_id))
        self.db.execute("INSERT INTO call_origin VALUES (?, ?, ?)", (source_id, row_key, cur.lastrowid))
        return cur.lastrowid


def android_exports(root=config.ANDROID_EXPORT):
    """(device, database, folder) of each Android export there is: the earlier single one, then one
    per phone folder."""
    out = []
    legacy = os.path.join(root, f"{ANDROID}.db")
    if os.path.exists(legacy):
        out.append((ANDROID, legacy, root))
    for name in sorted(os.listdir(root)) if os.path.isdir(root) else ():
        path = os.path.join(root, name, "android.db")
        if os.path.exists(path):
            out.append((name, path, os.path.dirname(path)))
    return out


def fingerprint(ts, outgoing, kind, text):
    """What tells a message without a key from another in its conversation: time, direction,
    kind and text (not the sender, whose address may later be merged)."""
    return hashlib.sha1(f"{ts}|{int(outgoing)}|{kind}|{text or ''}".encode()).hexdigest()[:20]


def contract(path):
    """A path as stored: the cache and data folders written as {cache} and {data}."""
    for token, root in (("{cache}", config.CACHE), ("{data}", config.DATA)):
        if path == root or path.startswith(root + os.sep):
            return token + path[len(root):]
    return path


def expand(path):
    """A stored path made whole again (see contract())."""
    for token, root in (("{cache}", config.CACHE), ("{data}", config.DATA)):
        if path and path.startswith(token):
            return root + path[len(token):]
    return path


URI = re.compile(r"^[a-z][a-z0-9+.-]*:\S", re.I)


def address(raw, region=config.REGION):
    """(kind, normalised value) of a phone number, email, URI or alphanumeric sender."""
    s = (raw or "").strip()
    if "@" in s and not s.lower().startswith("sip:"):
        return "email", s.lower()
    if URI.match(s) and not s.lower().startswith("tel:"):
        return "uri", s.lower()
    digits = re.sub(r"[\s\-().]", "", s.removeprefix("tel:"))
    if not re.fullmatch(r"\+?\d+", digits):
        return "sender", s
    if digits.startswith("00"):
        digits = "+" + digits[2:]
    return "phone", phone(digits, region)


def phone(digits, region):
    """+E.164 where the number is valid as written (national ones in `region`) or with a '+' put
    before it; otherwise short codes (under 10 digits) as bare digits (the iPhone adds a '+',
    Android does not), and anything longer as '+digits'."""
    bare = digits.lstrip("+")
    tries = [(digits, None)] if digits.startswith("+") else [(bare, region), ("+" + bare, None)]
    for text, reg in tries:
        if not reg and not text.startswith("+"):
            continue
        try:
            n = phonenumbers.parse(text, reg)
        except phonenumbers.NumberParseException:
            continue
        if phonenumbers.is_valid_number(n):
            return phonenumbers.format_number(n, phonenumbers.PhoneNumberFormat.E164)
    return bare if len(bare) < 10 else "+" + bare
