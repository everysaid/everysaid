"""Import WhatsApp from the iPhone's ChatStorage.sqlite and the live bridge's messages.db (config
`[whatsapp] bridge`). Either may be missing.

Messages are matched by stanza id (the message id WhatsApp sends, the same on every device). The
iPhone is read first; the bridge adds only what the archive does not have yet (what arrived after
the last iPhone backup). People are stored by phone number: WhatsApp's LIDs (`...@lid`) are mapped
to numbers through the iPhone's WhatsApp contacts and the bridge's `whatsmeow_lid_map`; LIDs with no
known number stay as they are. Groups are keyed by their jid. Channels (`@newsletter`) and status
(`status@broadcast`) are not wanted and not imported.
"""
from collections import defaultdict
from datetime import datetime
import os
import re
import sqlite3

from . import config, extras
from .archive import IPHONE, IPHONE_DATA, APPLE_EPOCH, address

IPHONE_DB = f"{IPHONE_DATA}/whatsapp.sqlite"
CONTACTS_DB = f"{IPHONE_DATA}/whatsapp-contacts.sqlite"
BRIDGE_DIR = config.WHATSAPP_BRIDGE     # there only where a bridge runs
BRIDGE_DB = os.path.join(BRIDGE_DIR, "messages.db") if BRIDGE_DIR else None
BRIDGE_STORE = os.path.join(BRIDGE_DIR, "whatsapp.db") if BRIDGE_DIR else None

# ZWAMESSAGE.ZMESSAGETYPE, from the files each type carries (`file`, `ffprobe`) and its metadata:
# 11 are GIFs (silent mp4), 54 round video notes (square, with sound), 14 deleted messages; 19, 20,
# 25, 30, 31 and 41 are business messages (templates, buttons) whose text is only in the media
# item's protobuf metadata. Anything else (10, 28, 59, 66, ...) carries nothing and is 'system'.
IPHONE_KINDS = {0: "text", 7: "text", 1: "image", 11: "image", 38: "image", 2: "video", 39: "video",
                54: "video", 3: "voice", 4: "contact", 5: "location", 8: "file", 15: "sticker",
                19: "text", 20: "text", 25: "text", 30: "text", 31: "text", 41: "text"}
METADATA_TEXT = (19, 20, 25, 30, 31, 41)
# The bridge's kinds (`messages.kind`); a bridge from before that column says only its media type.
BRIDGE_KINDS = {"text": "text", "image": "image", "video": "video", "audio": "voice", "voice": "voice",
                "document": "file", "sticker": "sticker", "location": "location", "contact": "contact",
                "poll": "text"}
BRIDGE_MEDIA_KINDS = {"": "text", "image": "image", "video": "video", "audio": "voice", "document": "file",
                      "sticker": "sticker"}


def ro(path):
    db = config.read_only(path)
    db.row_factory = sqlite3.Row
    return db


def ro_bridge(path):
    """A database that may not be there (the bridge's, or the iPhone's), or None."""
    return ro(path) if path and os.path.exists(path) else None


def has_table(db, name):
    return db.execute("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", (name,)).fetchone() is not None


def bridge_members(archive, bridge, person, own):
    """The groups' members as the bridge last read them, added to their conversations (one who left
    stays: a member once)."""
    if not has_table(bridge, "group_members"):
        return
    for group, jid in bridge.execute("SELECT group_jid, jid FROM group_members"):
        conv = archive.find_conversation("whatsapp", group)
        p = person(jid)
        if conv and p and p not in own:
            archive.db.execute("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", (conv, archive.address(*p)))


def bridge_mentions(archive, bridge, person):
    """Whom each message names with @, also on the messages the archive has from the iPhone."""
    if "mentions" not in {r[1] for r in bridge.execute("PRAGMA table_info(messages)")}:
        return
    for mid_key, mentions in bridge.execute("SELECT id, mentions FROM messages WHERE coalesce(mentions, '') != ''"):
        mid = archive.message_by_key("whatsapp", mid_key)
        if not mid:
            continue
        for jid in mentions.split(","):
            p = person(jid)
            if p:
                archive.db.execute("INSERT OR IGNORE INTO mention VALUES (?, ?, ?)",
                                   (mid, archive.address(*p), "@" + jid.split("@")[0]))


def ms(stamp):
    """A bridge timestamp as Unix ms (0 for one not known)."""
    return int(datetime.fromisoformat(stamp).timestamp() * 1000) if stamp else 0


def bridge_receipts(archive, bridge, person, own):
    """Who got, read and played the owner's messages, and when (the first time each)."""
    if not has_table(bridge, "receipts"):
        return
    for key, jid, kind, stamp in bridge.execute("SELECT message_id, jid, type, timestamp FROM receipts"):
        if kind not in ("delivered", "read", "played"):
            continue
        mid = archive.message_by_key("whatsapp", key)
        p = person(jid)
        if not mid or not p or p in own:
            continue
        aid = archive.address(*p)
        archive.db.execute("INSERT OR IGNORE INTO receipt (message_id, address_id) "
                           "SELECT id, ? FROM message WHERE id = ? AND outgoing", (aid, mid))
        archive.db.execute(f"UPDATE receipt SET {kind}_at = ? WHERE message_id = ? AND address_id = ? "
                           f"AND (coalesce({kind}_at, 0) = 0 OR ({kind}_at > ? AND ? > 0))", (ms(stamp), mid, aid, ms(stamp), ms(stamp)))


def bridge_read(bridge):
    """chat jid -> (the newest message the owner read there, when), Unix ms, from the bridge's read_at."""
    if "read_at" not in {r[1] for r in bridge.execute("PRAGMA table_info(messages)")}:
        return {}
    out = {}
    for jid, stamp, read in bridge.execute("SELECT chat_jid, timestamp, read_at FROM messages WHERE read_at IS NOT NULL"):
        newest, when = out.get(jid, (0, 0))
        out[jid] = (max(newest, ms(stamp)), max(when, ms(read)))
    return out


def bridge_changes(archive, bridge, person, own, reactions):
    """What the bridge saw happen to messages the archive already has: edits and deletions by their
    sender (marked; the text stays as the archive first had it), and reactions as they are now
    (one per person: changed, added, or removed once taken back). Returns the counts."""
    db, out = archive.db, defaultdict(int)
    cols = {r[1] for r in bridge.execute("PRAGMA table_info(messages)")}
    if {"edited", "deleted"} <= cols:
        for r in bridge.execute("SELECT id, edited, deleted FROM messages WHERE edited OR deleted"):
            mid = archive.message_by_key("whatsapp", r["id"])
            if mid:
                for flag in ("edited", "deleted"):
                    if r[flag] and db.execute(f"UPDATE message SET {flag} = 1 WHERE id = ? AND NOT {flag}", (mid,)).rowcount:
                        out[flag] += 1
    for (_, message_id), rows in reactions.items():
        mid = archive.message_by_key("whatsapp", message_id)
        if not mid:
            continue
        for jid, mine, emoji in rows:
            p = None if mine else person(jid)
            mine = mine or p in own
            who = None if mine or not p else archive.address(*p)
            if not mine and who is None:
                continue
            old = db.execute("SELECT rowid, emoji FROM reaction WHERE message_id = ? AND " +
                             ("outgoing = 1" if mine else "address_id = ?"),
                             (mid,) if mine else (mid, who)).fetchone()
            if not emoji:
                if old:
                    db.execute("DELETE FROM reaction WHERE rowid = ?", (old[0],))
                    out["reactions"] += 1
            elif old is None:
                db.execute("INSERT INTO reaction (message_id, emoji, count, address_id, outgoing) VALUES (?, ?, 1, ?, ?)",
                           (mid, emoji, who, 1 if mine else None))
                out["reactions"] += 1
            elif old[1] != emoji:
                db.execute("UPDATE reaction SET emoji = ?, code = NULL WHERE rowid = ?", (emoji, old[0]))
                out["reactions"] += 1
    return dict(out)


def protobuf_strings(data, depth=0):
    """The readable strings in a protobuf message, nested ones included, in order."""
    out, i = [], 0
    try:
        while i < len(data):
            key, i = varint(data, i)
            wire = key & 7
            if wire == 0:
                _, i = varint(data, i)
            elif wire == 1:
                i += 8
            elif wire == 5:
                i += 4
            elif wire == 2:
                n, i = varint(data, i)
                chunk, i = data[i:i + n], i + n
                nested = protobuf_strings(chunk, depth + 1) if depth < 8 else None
                if nested:
                    out += nested
                else:
                    try:
                        text = chunk.decode("utf-8")
                    except UnicodeDecodeError:
                        continue
                    if text.isprintable() or "\n" in text:
                        out.append(text)
            else:
                return None
            if i > len(data):
                return None
    except IndexError:
        return None
    return out


def protobuf_fields(data):
    """field number -> its values, one level deep: ints, or bytes for nested messages and strings."""
    out, i = {}, 0
    try:
        while i < len(data):
            key, i = varint(data, i)
            field, wire = key >> 3, key & 7
            if wire == 0:
                value, i = varint(data, i)
            elif wire == 1:
                value, i = data[i:i + 8], i + 8
            elif wire == 5:
                value, i = data[i:i + 4], i + 4
            elif wire == 2:
                n, i = varint(data, i)
                value, i = data[i:i + n], i + n
            else:
                break
            out.setdefault(field, []).append(value)
    except IndexError:
        pass
    return out


def varint(data, i):
    n = shift = 0
    while True:
        b = data[i]
        n |= (b & 0x7F) << shift
        i += 1
        if b < 0x80:
            return n, i
        shift += 7


def metadata_text(blob):
    strings = protobuf_strings(blob or b"") or []
    # Sentences, not the ids, hashes, mime types and urls around them.
    lines = [s.strip() for s in strings if (" " in s.strip() or not s.isascii())
             and not s.startswith(("http", "/v/")) and not s.endswith(("@s.whatsapp.net", "@lid"))]
    return "\n".join(dict.fromkeys(lines)) or None


def user(jid):
    return jid.split("@")[0].split(":")[0] if jid else None


class People:
    """jid (or the bridge's bare user part) -> (kind, value) address."""

    def __init__(self, contacts, store, iphone, bridge):
        """Each may be None: the iPhone's contacts and messages, the bridge's store and messages."""
        self.lid_phone = {}
        for lid, jid in contacts.execute("SELECT ZLID, ZWHATSAPPID FROM ZWAADDRESSBOOKCONTACT "
                                         "WHERE ZLID IS NOT NULL AND ZWHATSAPPID IS NOT NULL") if contacts else ():
            self.lid_phone[user(lid)] = user(jid)
        for lid, pn in store.execute("SELECT lid, pn FROM whatsmeow_lid_map") if store else ():
            self.lid_phone.setdefault(lid, pn)
        if bridge and has_table(bridge, "group_members"):      # the groups' members, named both ways
            for lid, pn in bridge.execute("SELECT lid, phone FROM group_members WHERE lid != '' AND phone != ''"):
                self.lid_phone.setdefault(user(lid), user(pn))
        # Every LID seen anywhere, to tell them from phone numbers in the bridge's bare senders.
        self.lids = set(self.lid_phone)
        for (jid,) in iphone.execute("SELECT ZFROMJID FROM ZWAMESSAGE WHERE ZFROMJID LIKE '%@lid' UNION "
                                     "SELECT ZMEMBERJID FROM ZWAGROUPMEMBER WHERE ZMEMBERJID LIKE '%@lid' UNION "
                                     "SELECT ZCONTACTJID FROM ZWACHATSESSION WHERE ZCONTACTJID LIKE '%@lid'") if iphone else ():
            self.lids.add(user(jid))
        for (jid,) in bridge.execute("SELECT chat_jid FROM messages WHERE chat_jid LIKE '%@lid' UNION "
                                     "SELECT sender FROM messages WHERE sender LIKE '%@lid'") if bridge else ():
            self.lids.add(user(jid))
        # The names WhatsApp shows for people, by kind (book: its copy of the phone's address
        # book; chat: a chat's name; profile: the name they chose, a push name): one per handle
        # and kind, the first found.
        self.names = {}
        rows = []
        if contacts:
            rows += [(j, n, "book") for j, n in contacts.execute(
                "SELECT ZWHATSAPPID, ZFULLNAME FROM ZWAADDRESSBOOKCONTACT UNION ALL "
                "SELECT ZLID, ZFULLNAME FROM ZWAADDRESSBOOKCONTACT")]
        if store:
            rows += [(j, n, "book") for j, n in store.execute("SELECT their_jid, full_name FROM whatsmeow_contacts")]
        if iphone:
            rows += [(j, n, "chat") for j, n in iphone.execute(
                "SELECT ZCONTACTJID, ZPARTNERNAME FROM ZWACHATSESSION "
                "WHERE ZCONTACTJID LIKE '%@s.whatsapp.net' OR ZCONTACTJID LIKE '%@lid'")]
            rows += [(j, n, "profile") for j, n in iphone.execute("SELECT ZJID, ZPUSHNAME FROM ZWAPROFILEPUSHNAME")]
        if bridge:
            rows += [(j, n, "chat") for j, n in bridge.execute(
                "SELECT jid, name FROM chats WHERE jid LIKE '%@s.whatsapp.net' OR jid LIKE '%@lid'")]
        if store:
            rows += [(j, n, "profile") for j, n in store.execute("SELECT their_jid, push_name FROM whatsmeow_contacts")]
        for jid, name, kind in rows:
            handle = self(jid if jid and "@" in jid else f"{jid}@s.whatsapp.net") if jid else None
            if handle and name:
                self.names.setdefault((handle, kind), name)

    def conversation_key(self, jid):
        """The archive's key of a WhatsApp chat: a group's jid, a person's handle value."""
        if jid.endswith("@s.whatsapp.net") or jid.endswith("@lid"):
            p = self(jid)
            return p[1] if p else jid
        return jid

    def __call__(self, jid):
        if not jid:
            return None
        u = user(jid)
        if jid.endswith("@lid") or ("@" not in jid and u in self.lids):
            return address(self.lid_phone[u]) if u in self.lid_phone else ("id", f"{u}@lid", "whatsapp")
        if jid.endswith("@s.whatsapp.net") or ("@" not in jid and len(u) <= 15):    # longer: no phone (E.164)
            return address(u)
        return None


# channels, and status (all of it, and each contact's own: "<number>@status", "<lid>@lid.status"):
# not wanted, not imported
CHANNELS = ("@newsletter", "status@broadcast", "@status", ".status")


def conversation(archive, person, jid, title, members):
    if jid.endswith(CHANNELS):
        return None
    if jid.endswith("@s.whatsapp.net") or jid.endswith("@lid"):
        p = person(jid)
        return archive.conversation("whatsapp", [p] if p else [("id", jid, "whatsapp")])
    cid = archive.conversation("whatsapp", members, key=jid, title=title)
    archive.db.execute("UPDATE conversation SET is_group = 1 WHERE id = ?", (cid,))
    return cid


def run(archive, iphone_db=IPHONE_DB, contacts_db=CONTACTS_DB, bridge_db=BRIDGE_DB, store_db=BRIDGE_STORE):
    iphone, bridge = ro_bridge(iphone_db), ro_bridge(bridge_db)
    if not iphone and not bridge:
        print("καμία πηγή: ούτε", iphone_db, "ούτε", bridge_db or "[whatsapp] bridge")
        return
    person = People(ro_bridge(contacts_db), ro_bridge(store_db), iphone, bridge)
    own = archive.own()
    src = {}
    if iphone:
        src["iphone"] = archive.source(f"{IPHONE}/whatsapp", iphone_db, IPHONE)
    if bridge:
        src["bridge"] = archive.source("whatsapp-bridge", bridge_db, "whatsapp-bridge")
    added, skipped = defaultdict(int), defaultdict(int)

    def add(source, row_key, extra, conv, ts, outgoing, sender, kind, text, key):
        if conv is None:                # a channel
            return
        if archive.has_origin(src[source], row_key):
            return
        if archive.message_by_key("whatsapp", key):
            skipped[source] += 1
            return
        if extra and extra.get("reactions"):    # the reactors are jids: people, or the owner
            extra["reactions"] = [(e, code, n, None if who is None or person(who) in own else person(who),
                                   1 if who is None or person(who) in own else None)
                                  for e, code, n, who, _ in extra["reactions"]]
        archive.add_message(src[source], row_key, service="whatsapp", conversation_id=conv, ts=ts,
                            outgoing=outgoing, sender_id=archive.address(*sender) if sender else None,
                            kind=kind, text=text, key=key, extras=extra)
        added[source] += 1

    # iPhone
    if iphone:
        group_members = defaultdict(list)
        member_jid = {}
        for r in iphone.execute("SELECT Z_PK, ZCHATSESSION, ZMEMBERJID FROM ZWAGROUPMEMBER"):
            member_jid[r["Z_PK"]] = r["ZMEMBERJID"]
            p = person(r["ZMEMBERJID"])
            if p and p not in own and p not in group_members[r["ZCHATSESSION"]]:
                group_members[r["ZCHATSESSION"]].append(p)
        sessions = {}
        for s in iphone.execute("SELECT Z_PK, ZCONTACTJID, ZPARTNERNAME, ZSESSIONTYPE FROM ZWACHATSESSION"):
            jid = s["ZCONTACTJID"] or f"session:{s['Z_PK']}"
            sessions[s["Z_PK"]] = (s["ZCONTACTJID"], conversation(archive, person, jid, s["ZPARTNERNAME"],
                                                                  group_members[s["Z_PK"]]))
        metadata = dict(iphone.execute(
            f"SELECT m.Z_PK, i.ZMETADATA FROM ZWAMESSAGE m JOIN ZWAMEDIAITEM i ON i.Z_PK = m.ZMEDIAITEM "
            f"WHERE m.ZMESSAGETYPE IN {METADATA_TEXT}").fetchall())
        for r in iphone.execute("SELECT m.*, i.ZMETADATA AS meta_blob, mi.ZRECEIPTINFO AS receipt_blob, "
                                "i.ZLATITUDE, i.ZLONGITUDE, i.ZTITLE, i.ZVCARDNAME, i.ZVCARDSTRING FROM ZWAMESSAGE m "
                                "LEFT JOIN ZWAMEDIAITEM i ON i.Z_PK = m.ZMEDIAITEM "
                                "LEFT JOIN ZWAMESSAGEINFO mi ON mi.Z_PK = m.ZMESSAGEINFO ORDER BY m.Z_PK"):
            jid, conv = sessions[r["ZCHATSESSION"]]
            outgoing = bool(r["ZISFROMME"])
            sender = None if outgoing else person(member_jid.get(r["ZGROUPMEMBER"]) or r["ZFROMJID"])
            add("iphone", str(r["Z_PK"]), extras.whatsapp(dict(r), r["meta_blob"], r["receipt_blob"], dict(r)),
                conv, round((r["ZMESSAGEDATE"] + APPLE_EPOCH) * 1000),
                outgoing, sender, IPHONE_KINDS.get(r["ZMESSAGETYPE"], "system"),
                r["ZTEXT"] or metadata_text(metadata.get(r["Z_PK"])), r["ZSTANZAID"])

    # Bridge
    names = dict(bridge.execute("SELECT jid, name FROM chats").fetchall()) if bridge else {}
    reactions = defaultdict(list)       # (chat, message id) -> [(sender jid, is_from_me, emoji)]
    if bridge and has_table(bridge, "reactions"):
        for r in bridge.execute("SELECT chat_jid, message_id, sender, is_from_me, emoji FROM reactions"):
            reactions[(r["chat_jid"], r["message_id"])].append((r["sender"], bool(r["is_from_me"]), r["emoji"]))
    convs = {}
    for r in bridge.execute("SELECT * FROM messages ORDER BY timestamp") if bridge else ():
        jid = r["chat_jid"]
        if jid not in convs:
            convs[jid] = conversation(archive, person, jid, names.get(jid), [])
        outgoing = bool(r["is_from_me"])
        kind = (BRIDGE_KINDS.get(r["kind"], "file") if "kind" in r.keys() and r["kind"]
                else BRIDGE_MEDIA_KINDS.get(r["media_type"] or "", "file"))
        add("bridge", f"{jid}/{r['id']}", extras.whatsapp_bridge(r, reactions.get((jid, r["id"]), ())), convs[jid],
            int(datetime.fromisoformat(r["timestamp"]).timestamp() * 1000), outgoing,
            None if outgoing else person(r["sender"] or jid), kind, r["content"] or None, r["id"])
    updated = bridge_changes(archive, bridge, person, own, reactions) if bridge else {}
    if bridge:
        bridge_members(archive, bridge, person, own)
        bridge_mentions(archive, bridge, person)
        bridge_receipts(archive, bridge, person, own)

    # The names WhatsApp shows, for the handles in the archive (core/names.py orders them against an
    # address book and other services).
    seen = {}
    if iphone:
        seen["iphone"] = int(os.path.getmtime(iphone_db))
    if bridge:
        seen["bridge"] = int(os.path.getmtime(store_db if store_db and os.path.exists(store_db) else bridge_db))
    for (handle, kind), name in person.names.items():
        if handle not in own:
            archive.handle_name(handle, "whatsapp", name, kind, max(seen.values()))

    # The state of chats: archived (only the start of ours: see Archive.init_archived), muted, pinned,
    # as each source last saw it.
    def report(where, jid, field, value, at):
        conv = archive.find_conversation("whatsapp", person.conversation_key(jid))
        archive.report_state(src[where], conv, field, value, at * 1000)
    if iphone:
        for jid, archived in iphone.execute("SELECT ZCONTACTJID, ZARCHIVED FROM ZWACHATSESSION WHERE ZCONTACTJID IS NOT NULL"):
            report("iphone", jid, "archived", int(bool(archived)), seen["iphone"])
        muted = dict(iphone.execute("SELECT ZJID, ZMUTEDUNTIL FROM ZWACHATPUSHCONFIG WHERE ZJID IS NOT NULL"))
        for (jid,) in iphone.execute("SELECT ZCONTACTJID FROM ZWACHATSESSION WHERE ZCONTACTJID IS NOT NULL"):
            until = muted.get(jid) or 0
            until = 0 if until <= 0 else -1 if until > 32503680000 else int((until + APPLE_EPOCH) * 1000)  # past 3000: for ever
            report("iphone", jid, "muted", until, seen["iphone"])
    store = ro_bridge(store_db) if bridge else None
    if store:
        for jid, until, pinned, archived in store.execute(
                "SELECT chat_jid, muted_until, pinned, archived FROM whatsmeow_chat_settings"):
            report("bridge", jid, "archived", int(bool(archived)), seen["bridge"])
            report("bridge", jid, "pinned", int(bool(pinned)), seen["bridge"])
            report("bridge", jid, "muted", -1 if until == -1 else (until or 0) * 1000, seen["bridge"])
    if bridge:                          # read up to: what the owner read on any device, as the bridge saw
        for jid, (newest, when) in bridge_read(bridge).items():
            conv = archive.find_conversation("whatsapp", person.conversation_key(jid))
            archive.report_state(src["bridge"], conv, "read_until", newest, when, when)

    archive.resolve()
    for sid in src.values():
        archive.imported(sid)
    archive.db.commit()
    for source in src:
        print(f"νέα:     {added[source]:7} {source}"
              + (f" ({skipped[source]} υπήρχαν ήδη από άλλη πηγή)" if skipped[source] else ""))
    if updated:
        print("αλλαγές σε όσα υπήρχαν:", ", ".join(f"{k} {v}" for k, v in sorted(updated.items())))
    return updated
