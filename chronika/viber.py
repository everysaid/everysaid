"""Import Viber from the iPhone's viber.sqlite and a Viber Desktop export (the Android phone's history,
config `[viber] desktop_export`). Either may be missing.

Messages are matched by token, the same id on every device. A message in both sources is kept
once, from the desktop export where its device (the Android phone it was synced with) was the one
in use at the time, from the iPhone otherwise; a message whose token
is already in the archive is skipped. Rows without a token (system events) are skipped when the
same conversation already has one at the same moment with the same text. People are matched by
Viber member id and stored by phone number when either source knows it, so they meet their SMS and
calls; groups by group token. Channels (news, government broadcasts) are not wanted and not imported.
"""
from collections import defaultdict
import os
import sqlite3

from . import config, extras
from .archive import ANDROID, IPHONE, IPHONE_DATA, APPLE_EPOCH, address

IPHONE_DB = f"{IPHONE_DATA}/viber.sqlite"
DESKTOP_DB = config.VIBER_DESKTOP     # optional

DESKTOP_KINDS = {1: "text", 2: "image", 3: "video", 4: "sticker", 5: "location", 6: "voice",
                 9: "text", 10: "contact", 11: "file", 15: "system"}
DESKTOP_SYSTEM_EVENT = 3
IPHONE_ATTACHMENT_KINDS = {"picture": "image", "gif": "image", "video": "video", "audio": "voice",
                           "file": "file", "sticker": "sticker", "customLocation": "location"}
IPHONE_TEXT_TYPES = ("", "url", "formatted")
CHANNEL = 3     # ZCONVERSATION.ZSUBTYPE on the iPhone, ChatInfo.PGType in the desktop export: not wanted
# A token carries its send time: (token >> 22) + this offset is Unix ms (measured on the iPhone's
# messages: nearly all within 2 s). Used only where the date itself is missing.
TOKEN_EPOCH_MS = 292057776050


def token_time(date, token):
    ts = round((date + APPLE_EPOCH) * 1000) if date is not None else 0
    return ts if ts > 0 or not token else (token >> 22) + TOKEN_EPOCH_MS


def ro(path):
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    db.row_factory = sqlite3.Row
    return db


def phones(iphone, desktop, archive):
    """Viber member id -> phone number, from whichever source knows it. What is learnt is kept in the
    archive (`viber_member`), so it is not lost when a source (the Android phone's export) is gone."""
    known = dict(archive.db.execute("SELECT mid, number FROM viber_member"))
    if desktop:
        for mid, number in desktop.execute("SELECT MID, Number FROM Contact WHERE MID != '' AND Number != ''"):
            known[mid] = number
    for mid, number in iphone.execute(
            "SELECT m.ZMEMBERID, coalesce(p.ZCANONIZEDPHONENUM, p.ZPHONE) FROM ZMEMBER m "
            "JOIN ZPHONENUMBER p ON p.ZMEMBER = m.Z_PK WHERE m.ZMEMBERID IS NOT NULL"):
        if number:
            known.setdefault(mid, number)
    archive.db.executemany("INSERT OR REPLACE INTO viber_member VALUES (?, ?)", known.items())
    return known


class People:
    def __init__(self, known):
        self.known = known

    def __call__(self, mid, number=None):
        number = number or self.known.get(mid)
        if number:
            return address(number)
        return ("id", mid, "viber") if mid else None


class Empty:
    """A source that is not there: every query gives nothing."""

    def execute(self, *_):
        return iter(())


def run(archive, iphone_db=IPHONE_DB, desktop_db=DESKTOP_DB):
    iphone = ro(iphone_db) if os.path.exists(iphone_db) else Empty()
    desktop = ro(desktop_db) if desktop_db and os.path.exists(desktop_db) else None   # the Android phone's: may be gone
    if isinstance(iphone, Empty) and not desktop:
        print("καμία πηγή: ούτε", iphone_db, "ούτε", desktop_db or "[viber] desktop_export")
        return
    person = People(phones(iphone, desktop, archive))
    own = archive.own()
    src = {"iphone": archive.source(f"{IPHONE}/viber", iphone_db, IPHONE)}
    if desktop_db:
        src["desktop"] = archive.source(f"{ANDROID}/viber", desktop_db, ANDROID)
    added, skipped = defaultdict(int), defaultdict(int)

    def add(source, row_key, extra, conv, ts, outgoing, sender, kind, text, key):
        if conv is None:                # a channel
            return
        if archive.has_origin(src[source], row_key):
            return
        if key is not None and archive.message_by_key("viber", key):
            skipped[source] += 1
            return
        # Without a token (system events) the row key is all there is, and the desktop's EventID is
        # local to one profile: a later export from another profile would bring them again.
        if key is None and archive.db.execute(
                "SELECT 1 FROM message WHERE conversation_id = ? AND ts = ? AND key IS NULL AND text IS ? "
                "AND service_id = ?", (conv, ts, text, archive.service["viber"])).fetchone():
            skipped[source] += 1
            return
        archive.add_message(src[source], row_key, service="viber", conversation_id=conv, ts=ts,
                            outgoing=outgoing, sender_id=archive.address(*sender) if sender else None,
                            kind=kind, text=text, key=key, extras=extra)
        added[source] += 1

    # iPhone: read first (small), so that its tokens are known while the desktop rows stream by.
    members = {r["Z_PK"]: person(r["ZMEMBERID"]) for r in iphone.execute("SELECT Z_PK, ZMEMBERID FROM ZMEMBER")}
    conv_members = defaultdict(list)
    for c, m in iphone.execute("SELECT Z_5CONVERSATIONS, Z_10PHONENUMINDEXES FROM Z_5PHONENUMINDEXES"):
        if members.get(m) and members[m] not in own:
            conv_members[c].append(members[m])
    convs = {}
    for c in iphone.execute("SELECT Z_PK, ZGROUPID, ZNAME, ZSUBTYPE FROM ZCONVERSATION"):
        mem = conv_members[c["Z_PK"]]
        if c["ZSUBTYPE"] == CHANNEL:
            convs[c["Z_PK"]] = None
        elif c["ZGROUPID"]:
            convs[c["Z_PK"]] = archive.conversation("viber", mem, key=f"group:{c['ZGROUPID']}", title=c["ZNAME"])
            archive.db.execute("UPDATE conversation SET is_group = 1 WHERE id = ?", (convs[c["Z_PK"]],))
        else:
            convs[c["Z_PK"]] = archive.conversation("viber", mem or [("id", f"conversation:{c['Z_PK']}", "viber")])
    attachment = dict(iphone.execute("SELECT Z_PK, ZTYPE FROM ZATTACHMENT").fetchall())
    locations = {r[0]: (r[1], r[2], r[3]) for r in iphone.execute(
        "SELECT Z_PK, ZLATITUDE, ZLONGITUDE, ZADDRESS FROM ZVIBERLOCATION")}
    iphone_recs = {}
    for r in iphone.execute("SELECT * FROM ZVIBERMESSAGE ORDER BY Z_PK"):
        outgoing = r["ZSTATE"] != "received"
        system = r["ZSYSTEMTYPE"] or ""
        kind = (IPHONE_ATTACHMENT_KINDS.get(attachment.get(r["ZATTACHMENT"]), "file") if r["ZATTACHMENT"]
                else "text" if system in IPHONE_TEXT_TYPES else "location" if system == "customLocation"
                else "call" if system == "systemCallLog" else "system")
        iphone_recs[r["Z_PK"]] = dict(
            extra=extras.viber_iphone(dict(r), locations), conv=convs[r["ZCONVERSATION"]],
            ts=token_time(r["ZDATE"], r["ZTOKEN"]), outgoing=outgoing,
            sender=None if outgoing else members.get(r["ZPHONENUMINDEX"]), kind=kind,
            text=r["ZTEXT"] or None, key=str(r["ZTOKEN"]) if r["ZTOKEN"] else None)
    iphone_keys = {rec["key"]: pk for pk, rec in iphone_recs.items() if rec["key"]}

    taken_from_iphone = set()
    if desktop:
        # Desktop export of the Android phone.
        contacts = {r["ContactID"]: r for r in desktop.execute("SELECT ContactID, MID, Number FROM Contact")}
        contact = {cid: person(r["MID"], r["Number"]) for cid, r in contacts.items()}
        chat_members = defaultdict(list)
        for chat, cid in desktop.execute("SELECT ChatID, ContactID FROM ChatRelation"):
            if contact.get(cid) and contact[cid] not in own:
                chat_members[chat].append(contact[cid])
        chats = {}
        for c in desktop.execute("SELECT ChatID, Name, Token, PGType FROM ChatInfo"):
            mem = chat_members[c["ChatID"]]
            if c["PGType"] == CHANNEL:
                chats[c["ChatID"]] = None
            elif c["Token"]:
                chats[c["ChatID"]] = archive.conversation("viber", mem, key=f"group:{c['Token']}", title=c["Name"])
                archive.db.execute("UPDATE conversation SET is_group = 1 WHERE id = ?", (chats[c["ChatID"]],))
            else:
                chats[c["ChatID"]] = archive.conversation("viber", mem or [("id", f"chat:{c['ChatID']}", "viber")])
        for r in desktop.execute("SELECT e.*, m.Type AS MessageType, m.Body, m.Info, m.Status, m.Subject, "
                                 "m.Flag, m.PayloadPath, m.ThumbnailPath, m.StickerID, m.PttID, m.Duration "
                                 "FROM Events e LEFT JOIN Messages m USING (EventID) ORDER BY e.EventID"):
            key = str(r["Token"]) if r["Token"] and r["Type"] != DESKTOP_SYSTEM_EVENT else None
            if key in iphone_keys and archive.keeper([IPHONE, ANDROID], r["TimeStamp"]) != ANDROID:
                continue                    # the iPhone's copy is the one kept
            if key in iphone_keys:
                taken_from_iphone.add(iphone_keys[key])
            outgoing = r["Direction"] == 1
            kind = "system" if r["Type"] == DESKTOP_SYSTEM_EVENT else DESKTOP_KINDS.get(r["MessageType"], "text")
            add("desktop", str(r["EventID"]), extras.viber_desktop(dict(r)), chats[r["ChatID"]], r["TimeStamp"], outgoing,
                None if outgoing else contact.get(r["ContactID"]), kind, r["Body"] or None, key)

    for pk, rec in iphone_recs.items():
        if pk not in taken_from_iphone:
            add("iphone", str(pk), rec["extra"], rec["conv"], rec["ts"], rec["outgoing"], rec["sender"],
                rec["kind"], rec["text"], rec["key"])
    # the iPhone's copies of messages kept from the Android phone, as second origins: later imports skip them
    # without the Android phone's export
    for pk in taken_from_iphone:
        mid = archive.message_by_key("viber", iphone_recs[pk]["key"])
        if mid:
            archive.db.execute("INSERT OR IGNORE INTO message_origin VALUES (?, ?, ?)", (src["iphone"], str(pk), mid))
    archive.resolve()
    for sid in src.values():
        archive.imported(sid)
    archive.db.commit()

    events = desktop.execute("SELECT count(*) FROM Events").fetchone()[0] if desktop else "—"
    print(f"iPhone:  {len(iphone_recs)} μηνύματα, Desktop (Android): {events}")
    for source in src:
        print(f"νέα:     {added[source]:7} {source}"
              + (f" ({skipped[source]} υπήρχαν ήδη από άλλη πηγή)" if skipped[source] else ""))
