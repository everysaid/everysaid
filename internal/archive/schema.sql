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
    kind TEXT NOT NULL CHECK (kind IN ('source', 'library', 'contacts', 'analysis')),
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
CREATE TABLE IF NOT EXISTS group_link (    -- groups the user merged: one chat, c<into_id>
    conversation_id INTEGER PRIMARY KEY REFERENCES conversation,
    into_id INTEGER NOT NULL REFERENCES conversation    -- never itself linked
);
CREATE TABLE IF NOT EXISTS group_dismissed (    -- suggested group merges the user turned down
    a INTEGER NOT NULL REFERENCES conversation, -- a < b
    b INTEGER NOT NULL REFERENCES conversation,
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
CREATE TABLE IF NOT EXISTS mention (
    message_id INTEGER NOT NULL REFERENCES message,
    address_id INTEGER NOT NULL REFERENCES address,  -- whom the text names with @
    token TEXT,                         -- how the text names them: "@<number or LID>" (WhatsApp), "@username"
                                        -- or the name itself (Telegram), "@Name" (Viber); NULL: not known
    PRIMARY KEY (message_id, address_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS receipt (           -- who got and read the owner's messages, where the service says
    message_id INTEGER NOT NULL REFERENCES message,
    address_id INTEGER NOT NULL REFERENCES address,
    delivered_at INTEGER,               -- Unix ms; 0: so, but when is not known; NULL: not (yet)
    read_at INTEGER,
    played_at INTEGER,                  -- a voice message or video played
    PRIMARY KEY (message_id, address_id)
) WITHOUT ROWID;
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
CREATE TABLE IF NOT EXISTS label (     -- the words people are described by: their chats' tone, who they are to the owner
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('tone', 'relation')),    -- tone: many to a person; relation: one
    key TEXT UNIQUE,                    -- one the app brings (its words in the app's languages); NULL: the user's own
    name TEXT,                          -- the user's name for it (their own, or one the app brings, renamed)
    meaning TEXT,                       -- what it means, for the local models (one the app brings: NULL, its own);
                                        -- '': never suggested by a model, only given by the user
    sensitive INTEGER NOT NULL DEFAULT 0,   -- suggested only with a line of the chat that shows it and two models
    position INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS person_label (
    person_id INTEGER NOT NULL REFERENCES person,
    label_id INTEGER NOT NULL REFERENCES label ON DELETE CASCADE,
    state TEXT NOT NULL CHECK (state IN ('suggested', 'yes', 'no')),   -- suggested by the models; yes, no: the user's
    votes INTEGER,                      -- suggested: how many of the models said so,
    models INTEGER,                     -- of how many
    evidence TEXT,                      -- a line of the chat that shows it
    at INTEGER NOT NULL,                -- Unix seconds
    PRIMARY KEY (person_id, label_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS name_guess (    -- a name for someone no source names, found in what they wrote or their handles
    person_id INTEGER NOT NULL REFERENCES person,
    how TEXT NOT NULL CHECK (how IN ('models', 'handle')),
    name TEXT NOT NULL,
    votes INTEGER,                      -- models: how many said so, of how many
    models INTEGER,
    evidence TEXT,                      -- the phrase that shows it
    dismissed INTEGER NOT NULL DEFAULT 0,   -- the user said it is wrong
    at INTEGER NOT NULL,
    PRIMARY KEY (person_id, how)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS analysis (   -- the people the local analysis has read, and on what
    person_id INTEGER PRIMARY KEY REFERENCES person,
    messages INTEGER NOT NULL,          -- how many messages their chat had (read again when it has many more)
    labels TEXT NOT NULL,               -- the labels it judged by (a digest): another list, judged again if asked
    models TEXT NOT NULL,               -- which models read it
    at INTEGER NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS message_fts USING fts5(
    text, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 2');
CREATE VIRTUAL TABLE IF NOT EXISTS message_tri USING fts5(     -- the same text in trigrams: parts of words
    text, content='', contentless_delete=1, tokenize='trigram');
CREATE TRIGGER IF NOT EXISTS message_subtype_insert BEFORE INSERT ON message WHEN new.subtype IS NOT NULL AND NOT EXISTS (SELECT 1 FROM vocabulary WHERE field = 'message.subtype' AND name = new.subtype) BEGIN SELECT RAISE(ABORT, 'message.subtype not in the vocabulary'); END;
CREATE TRIGGER IF NOT EXISTS message_subtype_update BEFORE UPDATE OF subtype ON message WHEN new.subtype IS NOT NULL AND NOT EXISTS (SELECT 1 FROM vocabulary WHERE field = 'message.subtype' AND name = new.subtype) BEGIN SELECT RAISE(ABORT, 'message.subtype not in the vocabulary'); END;
CREATE TRIGGER IF NOT EXISTS call_detail_insert BEFORE INSERT ON call WHEN new.detail IS NOT NULL AND NOT EXISTS (SELECT 1 FROM vocabulary WHERE field = 'call.detail' AND name = new.detail) BEGIN SELECT RAISE(ABORT, 'call.detail not in the vocabulary'); END;
CREATE TRIGGER IF NOT EXISTS call_detail_update BEFORE UPDATE OF detail ON call WHEN new.detail IS NOT NULL AND NOT EXISTS (SELECT 1 FROM vocabulary WHERE field = 'call.detail' AND name = new.detail) BEGIN SELECT RAISE(ABORT, 'call.detail not in the vocabulary'); END;
CREATE TRIGGER IF NOT EXISTS call_member_outcome_insert BEFORE INSERT ON call_member WHEN new.outcome IS NOT NULL AND NOT EXISTS (SELECT 1 FROM vocabulary WHERE field = 'call_member.outcome' AND name = new.outcome) BEGIN SELECT RAISE(ABORT, 'call_member.outcome not in the vocabulary'); END;
CREATE TRIGGER IF NOT EXISTS call_member_outcome_update BEFORE UPDATE OF outcome ON call_member WHEN new.outcome IS NOT NULL AND NOT EXISTS (SELECT 1 FROM vocabulary WHERE field = 'call_member.outcome' AND name = new.outcome) BEGIN SELECT RAISE(ABORT, 'call_member.outcome not in the vocabulary'); END;
