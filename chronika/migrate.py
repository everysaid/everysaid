"""Bring an archive of schema v1 (before versioning, October 2026) to v2, in one transaction.

What changes (see `archive.py` for the schema itself):

- address: the kinds `viber` and `whatsapp` become `id` within their service (`address.service_id`);
  `alpha` becomes `sender`; senders and emails that are URIs (sip:...) become `uri`.
- person, person_address: one person per address, as the archive behaved until now (a number is one
  address across SMS, Viber and WhatsApp); a Viber member id whose number `viber_member` knows joins
  that number's person.
- account: the owner's numbers from config `[owner] numbers`.
- device: the iPhone and the Android phone with the periods the importers had built in (the Android phone for
  the years it was in use), and the WhatsApp bridge; each source points to
  its device, and the sources with media name their folder (`media_root`).
- message: `key_scope` and `fingerprint` (for messages without a key); the subtypes become the
  vocabulary ('removed' -> 'deleted', 'call incoming ×2' -> 'call'), the source's code in
  `subtype_code` where it can be told; a position on a message that is not a location moves to
  `sender_lat`, `sender_lon`.
- reaction: `emoji` is NULL where only Viber's code is known, the code in `code`.
- call: `detail` in the vocabulary ('missed (WhatsApp 4)' -> 'missed', 'not connected (WhatsApp 5)'
  -> 'failed'), the source's code in `detail_code`; call_member gets `outcome_code`.
- library_link: one row per (file, library); the links to a look-alike the archive keeps (library
  'archive') move to `media_same`.

Every row count of messages, calls, conversations, origins, attachments, media and reactions stays
the same; message ids are kept, so the full-text index stays valid.
"""
from datetime import datetime
from zoneinfo import ZoneInfo
import os

from . import archive, config, extras

# What the v1 importers had built in (archive.ANDROID_ERA): the Android phone in use for a
# configured period, the iPhone after it.
ANDROID_FROM = int(datetime(2020, 1, 1, tzinfo=ZoneInfo("UTC")).timestamp() * 1000)
ANDROID_UNTIL = int(datetime(2022, 1, 1, tzinfo=ZoneInfo("UTC")).timestamp() * 1000)
WHATSAPP_CODES = {v: f"whatsapp:{k}" for k, v in extras.WHATSAPP_SUBTYPES.items()}
VIBER_IPHONE_CODES = {v: f"viber:{k}" for k, v in extras.VIBER_IPHONE_SUBTYPES.items()}
VIBER_IPHONE_CODES.update({"poll": "viber:Poll", "gif": "viber:EXPRESSION_PANEL_GIF"})
VIBER_DESKTOP_CODES = {v: f"viber:{k}" for k, v in extras.VIBER_DESKTOP_SUBTYPES.items()}
VIBER_DESKTOP_CODES.update({"video note": "viber:ivmInfo", "gif": "viber:EXPRESSION_PANEL_GIF"})


def ddl(table, name):
    """The v2 CREATE TABLE of `table`, under another name."""
    head = f"CREATE TABLE IF NOT EXISTS {table} ("
    for s in archive.statements(archive.SCHEMA):
        if s.startswith(head):
            return f"CREATE TABLE {name} (" + s[len(head):]
    raise KeyError(table)


def rebuild(db, table, select):
    """Replace a table by its v2 form, filled by `select` (columns in the v2 order)."""
    db.execute(ddl(table, f"{table}_v2"))
    db.execute(f"INSERT INTO {table}_v2 {select}")
    db.execute(f"DROP TABLE {table}")
    db.execute(f"ALTER TABLE {table}_v2 RENAME TO {table}")


def add_column(db, table, column):
    if column.split()[0] not in {r[1] for r in db.execute(f"PRAGMA table_info({table})")}:
        db.execute(f"ALTER TABLE {table} ADD COLUMN {column}")


def ids(db, table):
    return dict(db.execute(f"SELECT name, id FROM {table}").fetchall())


def case(column, mapping, default="NULL"):
    """A SQL CASE mapping the values of `column`."""
    whens = " ".join(f"WHEN {q(k)} THEN {q(v)}" for k, v in mapping.items())
    return f"CASE {column} {whens} ELSE {default} END" if mapping else default


def q(value):
    return "'" + str(value).replace("'", "''") + "'"


def addresses(db):
    kinds, services = ids(db, "address_kind"), ids(db, "service")
    db.execute("UPDATE address_kind SET name = 'sender' WHERE name = 'alpha'")
    db.execute("UPDATE address_kind SET name = 'id' WHERE name = 'viber'")
    db.execute("UPDATE address_kind SET name = 'username' WHERE name = 'whatsapp'")
    for name in ("name", "uri"):
        db.execute("INSERT OR IGNORE INTO address_kind (name) VALUES (?)", (name,))
    new = ids(db, "address_kind")
    db.create_function("kind_of", 1, lambda v: archive.address(v)[0], deterministic=True)
    uri = "kind_of(value) = 'uri'"     # sip:...
    rebuild(db, "address", f"""SELECT id,
        CASE kind_id WHEN {kinds['whatsapp']} THEN {new['id']}
                     WHEN {kinds['alpha']} THEN CASE WHEN {uri} THEN {new['uri']} ELSE {new['sender']} END
                     WHEN {kinds['email']} THEN CASE WHEN {uri} THEN {new['uri']} ELSE kind_id END
                     ELSE kind_id END,
        value,
        CASE kind_id WHEN {kinds['viber']} THEN {services['viber']} WHEN {kinds['whatsapp']} THEN {services['whatsapp']} END
        FROM address ORDER BY id""")


def messages(db):
    kinds, services = ids(db, "message_kind"), ids(db, "service")
    viber, whatsapp = services["viber"], services["whatsapp"]
    sources = ids(db, "source")
    desktop = sources.get("acme/viber", -1)
    db.create_function("fingerprint", 4, archive.fingerprint, deterministic=True)
    kind_name = case("kind_id", {v: k for k, v in kinds.items()})
    subtype = ("CASE WHEN subtype = 'removed' THEN 'deleted' WHEN subtype LIKE 'call %' THEN 'call' "
               "ELSE subtype END")
    code = f"""CASE
        WHEN service_id = {whatsapp} THEN {case('subtype', WHATSAPP_CODES)}
        WHEN service_id = {viber} AND subtype LIKE 'call %' THEN 'viber:' || substr(subtype, 6)
        WHEN service_id = {viber} AND subtype IS NOT NULL THEN
            CASE WHEN EXISTS (SELECT 1 FROM message_origin o WHERE o.message_id = message.id AND o.source_id = {desktop})
                 THEN {case('subtype', VIBER_DESKTOP_CODES)}
                 ELSE {case("CASE subtype WHEN 'removed' THEN 'deleted' ELSE subtype END", VIBER_IPHONE_CODES)} END
        END"""
    location = kinds["location"]
    rebuild(db, "message", f"""SELECT id, service_id, conversation_id, ts, outgoing, sender_id, kind_id, text,
        key, NULL,
        CASE WHEN key IS NULL THEN fingerprint(ts, outgoing, {kind_name}, text) END,
        {subtype}, {code}, reply_to, reply_key, reply_text, edited, deleted, forwarded, starred,
        CASE WHEN kind_id = {location} THEN lat END, CASE WHEN kind_id = {location} THEN lon END, place,
        CASE WHEN kind_id != {location} THEN lat END, CASE WHEN kind_id != {location} THEN lon END
        FROM message ORDER BY id""")


def reactions(db):
    services = ids(db, "service")
    viber = {v: f"viber:{k}" for k, v in extras.VIBER_REACTIONS.items()}
    tapback = {v: f"imessage:{k}" for k, v in extras.TAPBACKS.items()}
    rebuild(db, "reaction", f"""SELECT r.message_id,
        CASE WHEN r.emoji LIKE 'viber:%' THEN NULL ELSE r.emoji END,
        CASE WHEN r.emoji LIKE 'viber:%' THEN r.emoji
             WHEN m.service_id = {services['viber']} THEN {case('r.emoji', viber)}
             WHEN m.service_id = {services['imessage']} THEN {case('r.emoji', tapback)} END,
        r.count, r.address_id, r.outgoing
        FROM reaction r JOIN message m ON m.id = r.message_id ORDER BY r.rowid""")


def calls(db):
    services, sources = ids(db, "service"), ids(db, "source")
    add_column(db, "call", "detail_code TEXT")
    add_column(db, "call_member", "outcome_code TEXT")
    db.execute("UPDATE call SET detail = 'missed', detail_code = 'whatsapp:4' WHERE detail = 'missed (WhatsApp 4)'")
    db.execute("UPDATE call SET detail = 'failed', detail_code = 'whatsapp:5' WHERE detail = 'not connected (WhatsApp 5)'")
    for detail, code in (("rejected", "android:5"), ("blocked", "android:6")):
        db.execute("UPDATE call SET detail_code = ? WHERE detail = ?", (code, detail))
    db.execute("UPDATE call SET detail_code = 'whatsapp:1' WHERE detail IN ('missed', 'unanswered') "
               "AND detail_code IS NULL AND service_id = ?", (services["whatsapp"],))
    if "acme/calls" in sources:
        db.execute("UPDATE call SET detail_code = 'android:3' WHERE detail = 'missed' AND detail_code IS NULL "
                   "AND id IN (SELECT call_id FROM call_origin WHERE source_id = ?)", (sources["acme/calls"],))
    if "sms-alerts" in sources:
        db.execute("UPDATE call SET detail_code = 'carrier:gr' WHERE detail_code IS NULL AND detail IS NOT NULL "
                   "AND id IN (SELECT call_id FROM call_origin WHERE source_id = ?)", (sources["sms-alerts"],))
    db.execute("UPDATE call_member SET outcome_code = 'whatsapp:0' WHERE outcome = 'joined'")


def library(db):
    db.execute(ddl("media_same", "media_same"))
    db.execute("INSERT INTO media_same SELECT sha256, asset_id, method, score, linked_at FROM library_link "
               "WHERE library = 'archive'")
    rebuild(db, "library_link", "SELECT sha256, library, asset_id, method, score, linked_at FROM library_link "
                                "WHERE library != 'archive'")


def devices(db):
    add_column(db, "source", "device_id INTEGER REFERENCES device")
    add_column(db, "source", "media_root TEXT")
    db.execute(ddl("device", "device"))
    db.executemany("INSERT INTO device (name, kind, used_from, used_until) VALUES (?, ?, ?, ?)",
                   [("iphone", "ios", ANDROID_UNTIL, None), ("acme", "android", ANDROID_FROM, ANDROID_UNTIL),
                    ("whatsapp-bridge", "bridge", None, None)])
    db.execute("UPDATE source SET device_id = (SELECT id FROM device WHERE name = 'iphone') WHERE name LIKE 'iphone/%'")
    db.execute("UPDATE source SET device_id = (SELECT id FROM device WHERE name = 'acme') WHERE name LIKE 'acme/%'")
    db.execute("UPDATE source SET device_id = (SELECT id FROM device WHERE name = 'whatsapp-bridge') "
               "WHERE name = 'whatsapp-bridge'")
    roots = {"iphone/whatsapp": "{cache}/iphone/whatsapp-media", "iphone/viber": "{cache}/iphone/viber-media"}
    row = db.execute("SELECT path FROM source WHERE name = 'acme/mms'").fetchone()
    if row:
        roots["acme/mms"] = os.path.dirname(row[0])
        roots["acme/viber"] = os.path.join(os.path.dirname(row[0]), "viber-media")
    db.executemany("UPDATE source SET media_root = ? WHERE name = ?", [(r, n) for n, r in roots.items()])


def people(db):
    """One person per address; a Viber member id joins its number's person where viber_member knows it."""
    db.execute("INSERT INTO person (id) SELECT id FROM address ORDER BY id")
    db.execute("INSERT INTO person_address (address_id, person_id) SELECT id, id FROM address")
    ident = ids(db, "address_kind")["id"]
    viber = ids(db, "service")["viber"]
    joined = 0
    for aid, number in db.execute(
            "SELECT a.id, v.number FROM address a JOIN viber_member v ON v.mid = a.value "
            "WHERE a.kind_id = ? AND a.service_id = ?", (ident, viber)).fetchall():
        kind, value = archive.address(number)
        row = db.execute("SELECT person_id FROM person_address p JOIN address a ON a.id = p.address_id "
                         "WHERE a.kind_id = ? AND a.value = ? AND a.service_id IS NULL",
                         (ids(db, "address_kind")[kind], value)).fetchone() if kind == "phone" else None
        if row:
            db.execute("UPDATE person_address SET person_id = ?, how = 'number' WHERE address_id = ?", (row[0], aid))
            db.execute("DELETE FROM person WHERE id = ?", (aid,))
            joined += 1
    return joined


def accounts(db):
    for number in config.OWN_NUMBERS:
        kind, value = archive.address(number)
        kid = ids(db, "address_kind")[kind]
        row = db.execute("SELECT id FROM address WHERE kind_id = ? AND value = ? AND service_id IS NULL",
                         (kid, value)).fetchone()
        if row:
            aid = row[0]
        else:
            aid = db.execute("INSERT INTO address (kind_id, value) VALUES (?, ?)", (kid, value)).lastrowid
            pid = db.execute("INSERT INTO person DEFAULT VALUES").lastrowid
            db.execute("INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", (aid, pid))
        db.execute("INSERT OR IGNORE INTO account (address_id) VALUES (?)", (aid,))


def migrate(db):
    """db: a connection with isolation_level=None. Returns what was done, for the report; raises
    (after rolling back) when a check fails."""
    if archive.version(db) == archive.VERSION:
        return {}
    if archive.version(db) != 1:
        raise RuntimeError(f"σχήμα v{archive.version(db)}: δεν ξέρω να το μεταφέρω")
    db.execute("PRAGMA foreign_keys = OFF")
    db.execute("BEGIN")
    try:
        add_column(db, "service", "key_scope TEXT NOT NULL DEFAULT 'service'")
        addresses(db)
        messages(db)
        reactions(db)
        calls(db)
        library(db)
        devices(db)
        for s in archive.statements(archive.SCHEMA):      # the new tables, the indexes and triggers
            db.execute(s)
        archive.seed(db)
        joined = people(db)
        accounts(db)
        off = check_vocabulary(db)
        problems = db.execute("PRAGMA foreign_key_check").fetchall()
        if off or problems:
            raise RuntimeError(f"εκτός λεξιλογίου: {off[:5]}, foreign keys: {problems[:5]}")
        db.execute(f"PRAGMA user_version = {archive.VERSION}")
        db.execute("COMMIT")
    except BaseException:
        db.execute("ROLLBACK")
        raise
    finally:
        db.execute("PRAGMA foreign_keys = ON")
    return {"viber ids joined to a number": joined}


def check_vocabulary(db):
    off = []
    for field in archive.VOCABULARY:
        table, column = field.split(".")
        off += db.execute(f"SELECT DISTINCT '{field}', {column} FROM {table} WHERE {column} IS NOT NULL "
                          f"AND {column} NOT IN (SELECT name FROM vocabulary WHERE field = ?)", (field,)).fetchall()
    return off

