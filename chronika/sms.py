"""Import SMS, MMS, iMessage and RCS from the iPhone's sms.db and SMS and MMS from an Android
phone's export (`android-export.py`; the Android phone's android.db). Either may be missing.

Two phones may carry the same SMS history, copied from phone to phone. A pair (same
direction, same text, times at most 2 s apart; one to one) becomes one message with a single
origin: the device in use at the time (`device` in the archive; the Android phone for the years it
was in use), else the newer one. Unpaired rows come in from wherever they are. Exact repeats within one source (same
second, direction, sender and text; an iPhone may have hundreds) are taken once.

SMS and MMS are paired together: the Android phone kept many plain texts as MMS (long ones, or with a
link) that the iPhone has as SMS. Both live in one conversation per counterpart (service 'sms').
"""
from collections import defaultdict
from dataclasses import dataclass, field
import os
import sqlite3

from . import extras
from .archive import IPHONE, IPHONE_DATA, APPLE_EPOCH, address, android_exports

IPHONE_DB = f"{IPHONE_DATA}/sms.db"
PAIR_MS = 2000
GROUP_STYLE = 43
ANDROID_MMS_SENT = "2"
ANDROID_MMS_FROM = "137"
NOT_CONTENT = ("text/plain", "application/smil")


@dataclass
class Rec:
    source: str            # source name: 'iphone/sms', 'acme/sms', 'acme/mms' (<device>/...)
    row_key: str
    raw: dict
    service: str
    ts: int
    outgoing: bool
    sender: tuple | None   # (kind, value), None when outgoing
    members: list = field(default_factory=list)
    kind: str = "text"
    text: str | None = None
    key: str | None = None


def attributed_text(blob):
    """The plain text of an NSAttributedString typedstream (sms.db `attributedBody`)."""
    if not blob:
        return None
    i = blob.find(b"NSString")
    if i < 0:
        return None
    i = blob.find(b"\x84\x01+", i)
    if i < 0:
        return None
    i += 3
    n = blob[i]
    if n == 0x81:
        n, i = int.from_bytes(blob[i + 1:i + 3], "little"), i + 3
    elif n == 0x82:
        n, i = int.from_bytes(blob[i + 1:i + 5], "little"), i + 5
    else:
        i += 1
    return blob[i:i + n].decode("utf-8", "replace")


def clean(text):
    text = (text or "").replace("\ufffc", "").strip()
    return text or None


def kind_of_mime(mime):
    top = (mime or "").split("/")[0]
    return {"image": "image", "video": "video", "audio": "voice"}.get(top, "file")


def read_iphone(path):
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    db.row_factory = sqlite3.Row
    handles = {r["ROWID"]: r["id"] for r in db.execute("SELECT ROWID, id FROM handle")}
    chats = {r["ROWID"]: r for r in db.execute("SELECT ROWID, guid, style, display_name FROM chat")}
    chat_of = dict(db.execute("SELECT message_id, chat_id FROM chat_message_join").fetchall())
    members = defaultdict(list)
    for chat_id, handle_id in db.execute("SELECT chat_id, handle_id FROM chat_handle_join"):
        members[chat_id].append(address(handles[handle_id]))
    mime = {}
    for mid, m in db.execute("SELECT j.message_id, a.mime_type FROM message_attachment_join j "
                             "JOIN attachment a ON a.ROWID = j.attachment_id ORDER BY a.ROWID"):
        mime.setdefault(mid, m)

    recs = []
    for r in db.execute("SELECT * FROM message ORDER BY ROWID"):
        raw = dict(r)
        chat = chats.get(chat_of.get(r["ROWID"]))
        sender = None if r["is_from_me"] or not r["handle_id"] else address(handles[r["handle_id"]])
        mem = (members.get(chat["ROWID"]) if chat else None) or ([sender] if sender else [])
        service = {"SMS": "sms", "iMessage": "imessage", "RCS": "rcs"}[r["service"]]
        if service == "sms" and (r["cache_has_attachments"] or (chat and chat["style"] == GROUP_STYLE)):
            service = "mms"
        date = r["date"]
        ts = (date // 1_000_000 if date > 1e11 else date * 1000) + APPLE_EPOCH * 1000
        kind = ("reaction" if r["associated_message_type"] else "system" if r["item_type"]
                else kind_of_mime(mime[r["ROWID"]]) if r["ROWID"] in mime else "text")
        recs.append(Rec(f"{IPHONE}/sms", r["guid"], raw, service, ts, bool(r["is_from_me"]), sender, mem, kind,
                        clean(r["text"] if r["text"] is not None else attributed_text(r["attributedBody"])),
                        r["guid"] if service == "imessage" else None))
    return recs


def read_android(path, own, device):
    """One Android export's SMS and MMS; own: the owner's addresses, left out of MMS members."""
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    db.row_factory = sqlite3.Row
    recs = []
    for r in db.execute("SELECT * FROM sms ORDER BY CAST(_id AS INTEGER)"):
        outgoing = r["type"] != "1"
        addr = address(r["address"])
        recs.append(Rec(f"{device}/sms", r["_id"], dict(r), "sms", int(r["date"]), outgoing,
                        None if outgoing else addr, [addr], "text", clean(r["body"])))

    parts, addrs = defaultdict(list), defaultdict(list)
    for p in db.execute("SELECT * FROM mms_part ORDER BY CAST(seq AS INTEGER), CAST(_id AS INTEGER)"):
        parts[p["mid"]].append(dict(p))
    for a in db.execute("SELECT * FROM mms_addr ORDER BY CAST(_id AS INTEGER)"):
        addrs[a["msg_id"]].append(dict(a))
    for r in db.execute("SELECT * FROM mms ORDER BY CAST(_id AS INTEGER)"):
        raw = dict(r, parts=parts[r["_id"]], addr=addrs[r["_id"]])
        outgoing = r["msg_box"] == ANDROID_MMS_SENT
        members = list(dict.fromkeys(m for a in raw["addr"] if (m := address(a["address"])) not in own))
        sender = next((address(a["address"]) for a in raw["addr"] if a["type"] == ANDROID_MMS_FROM), None)
        media = [p["ct"] for p in raw["parts"] if p["ct"] not in NOT_CONTENT]
        text = "".join(p["text"] or "" for p in raw["parts"] if p["ct"] == "text/plain")
        recs.append(Rec(f"{device}/mms", r["_id"], raw, "mms", int(r["date"]) * 1000, outgoing,
                        None if outgoing else sender, members,
                        kind_of_mime(media[0]) if media else "text", clean(text)))
    return recs


def collapse(recs):
    """Drop exact repeats within one source; returns (kept, dropped)."""
    seen, kept = set(), []
    for r in recs:
        k = (r.service, r.outgoing, r.sender, r.text, r.ts // 1000)
        if k not in seen:
            seen.add(k)
            kept.append(r)
    return kept, len(recs) - len(kept)


def pair(iphone, android):
    """One-to-one pairs of iPhone and Android SMS: same direction and text, nearest time within 2 s."""
    index = defaultdict(list)
    for h in android:
        index[(h.outgoing, h.text)].append(h)
    used, pairs = set(), {}
    for i in sorted(iphone, key=lambda r: r.ts):
        if i.service not in ("sms", "mms"):
            continue
        best = min((h for h in index.get((i.outgoing, i.text), ())
                    if id(h) not in used and abs(h.ts - i.ts) <= PAIR_MS),
                   key=lambda h: abs(h.ts - i.ts), default=None)
        if best:
            used.add(id(best))
            pairs[id(i)] = best
    return pairs, used


def run(archive, iphone_db=IPHONE_DB, exports=None):
    """exports: (device, database, folder) of the Android exports; default: every one there is."""
    exports = android_exports() if exports is None else exports
    iphone = read_iphone(iphone_db) if os.path.exists(iphone_db) else []
    if not iphone and not exports:
        print("καμία πηγή: ούτε", iphone_db, "ούτε export Android")
        return
    iphone, i_dup = collapse(iphone)
    android, a_dup = [], 0
    for device, path, _ in exports:         # repeats are dropped within each phone
        recs, dup = collapse(read_android(path, archive.own(), device))
        android, a_dup = android + recs, a_dup + dup
    pairs, used = pair(iphone, android)

    def from_android(h):
        device = h.source.split("/")[0]
        return archive.keeper([IPHONE, device], h.ts) == device

    chosen = []
    for i in iphone:
        h = pairs.get(id(i))
        chosen.append(h if h and from_android(h) else i)
    chosen += [h for h in android if id(h) not in used]

    sources = {f"{IPHONE}/sms": archive.source(f"{IPHONE}/sms", iphone_db, IPHONE)}
    for device, path, _ in exports:
        for kind in ("sms", "mms"):
            sources[f"{device}/{kind}"] = archive.source(f"{device}/{kind}", path, device)
    added = defaultdict(int)
    for r in sorted(chosen, key=lambda r: r.ts):
        sid = sources[r.source]
        if archive.has_origin(sid, r.row_key):
            continue
        conv = archive.conversation("sms" if r.service == "mms" else r.service, r.members)
        archive.add_message(sid, r.row_key, service=r.service, conversation_id=conv, ts=r.ts,
                            outgoing=r.outgoing, sender_id=archive.address(*r.sender) if r.sender else None,
                            kind=r.kind, text=r.text, key=r.key,
                            extras=extras.imessage(r.raw) if r.source == f"{IPHONE}/sms" else None)
        added[(r.source, r.service)] += 1
    # the copy not kept, as a second origin: later imports skip it without the other phone
    archive.record_pairs(sources, [(h, i) if from_android(h) else (i, h)
                                   for i in iphone if (h := pairs.get(id(i)))])
    archive.resolve()
    for sid in sources.values():
        archive.imported(sid)
    archive.db.commit()

    print(f"iPhone: {len(iphone) + i_dup} γραμμές ({i_dup} επαναλήψεις)")
    print(f"Android ({', '.join(d for d, _, _ in exports) or '—'}): {len(android) + a_dup} γραμμές SMS και MMS "
          f"({a_dup} επαναλήψεις)")
    print(f"ζεύγη:  {len(pairs)} (από το Android στην εποχή του: "
          f"{sum(1 for i in iphone if (h := pairs.get(id(i))) and from_android(h))})")
    for (src, service), n in sorted(added.items()):
        print(f"νέα:    {n:6} {src} {service}")
    if not added:
        print("νέα:    κανένα")
