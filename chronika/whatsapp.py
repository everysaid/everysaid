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
import sqlite3

from . import config, extras
from .archive import IPHONE, IPHONE_DATA, APPLE_EPOCH, address

IPHONE_DB = f"{IPHONE_DATA}/whatsapp.sqlite"
CONTACTS_DB = f"{IPHONE_DATA}/whatsapp-contacts.sqlite"
BRIDGE_DIR = config.WHATSAPP_BRIDGE     # optional
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
BRIDGE_KINDS = {"": "text", "image": "image", "video": "video", "audio": "voice", "document": "file"}


def ro(path):
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    db.row_factory = sqlite3.Row
    return db


def ro_bridge(path):
    """A database that may not be there (the bridge's, or the iPhone's), or None."""
    return ro(path) if path and os.path.exists(path) else None


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
        # Every LID seen anywhere, to tell them from phone numbers in the bridge's bare senders.
        self.lids = set(self.lid_phone)
        for (jid,) in iphone.execute("SELECT ZFROMJID FROM ZWAMESSAGE WHERE ZFROMJID LIKE '%@lid' UNION "
                                     "SELECT ZMEMBERJID FROM ZWAGROUPMEMBER WHERE ZMEMBERJID LIKE '%@lid' UNION "
                                     "SELECT ZCONTACTJID FROM ZWACHATSESSION WHERE ZCONTACTJID LIKE '%@lid'") if iphone else ():
            self.lids.add(user(jid))
        for (jid,) in bridge.execute("SELECT chat_jid FROM messages WHERE chat_jid LIKE '%@lid' UNION "
                                     "SELECT sender FROM messages WHERE sender LIKE '%@lid'") if bridge else ():
            self.lids.add(user(jid))

    def __call__(self, jid):
        if not jid:
            return None
        u = user(jid)
        if jid.endswith("@lid") or ("@" not in jid and u in self.lids):
            return address(self.lid_phone[u]) if u in self.lid_phone else ("id", f"{u}@lid", "whatsapp")
        if jid.endswith("@s.whatsapp.net") or "@" not in jid:
            return address(u)
        return None


CHANNELS = ("@newsletter", "status@broadcast")     # channels and status: not wanted, not imported


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
    convs = {}
    for r in bridge.execute("SELECT * FROM messages ORDER BY timestamp") if bridge else ():
        jid = r["chat_jid"]
        if jid not in convs:
            convs[jid] = conversation(archive, person, jid, names.get(jid), [])
        outgoing = bool(r["is_from_me"])
        add("bridge", f"{jid}/{r['id']}", None, convs[jid],
            int(datetime.fromisoformat(r["timestamp"]).timestamp() * 1000), outgoing,
            None if outgoing else person(r["sender"] or jid),
            BRIDGE_KINDS.get(r["media_type"] or "", "file"), r["content"] or None, r["id"])

    archive.resolve()
    for sid in src.values():
        archive.imported(sid)
    archive.db.commit()
    for source in src:
        print(f"νέα:     {added[source]:7} {source}"
              + (f" ({skipped[source]} υπήρχαν ήδη από άλλη πηγή)" if skipped[source] else ""))
