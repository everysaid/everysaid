"""Import calls from the iPhone's CallHistory.storedata and an Android phone's call log (the
Android's, in its export). Either may be missing.

An iPhone's calls of some months may be copies of the Android phone's (same number, direction
and second). As with SMS, a pair becomes one call with a single origin: the device in use at the
time, else the newer one. The iPhone's log also has the calls of apps that use CallKit; each goes
to its own service.
"""
from collections import defaultdict
from dataclasses import dataclass
import os
import sqlite3

from . import config, extras
from .archive import IPHONE, IPHONE_DATA, APPLE_EPOCH, address, android_exports

IPHONE_DB = f"{IPHONE_DATA}/CallHistory.storedata"
PAIR_MS = 2000
# ZSERVICE_PROVIDER: Apple's own, or the bundle id of an app that uses CallKit (with or without its
# team id in front, e.g. 'UKFA9XBX6K.net.whatsapp.WhatsApp'). An app not known here keeps its bundle id.
SERVICES = {"com.apple.Telephony": "phone", "com.apple.FaceTime": "facetime",
            "net.whatsapp.WhatsApp": "whatsapp", "net.whatsapp.WhatsAppSMB": "whatsapp",
            "com.viber": "viber", "ph.telegra.Telegraph": "telegram", "org.telegram.Telegram": "telegram",
            "org.whispersystems.signal": "signal", "com.facebook.Messenger": "messenger",
            "com.microsoft.skype.teams": "teams", "com.skype.skype": "skype", "com.skype.SkypeForiPhone": "skype",
            "us.zoom.videomeetings": "zoom", "com.google.Duo": "meet", "com.google.meetings": "meet",
            "com.hammerandchisel.discord": "discord", "com.tinyspeck.chatlyio": "slack",
            "jp.naver.line": "line", "com.tencent.xin": "wechat"}
ANDROID_OUTGOING = "2"
ANDROID_ANSWERED = ("1", "7")       # incoming, answered on another device


@dataclass
class Rec:
    source: str
    row_key: str
    raw: dict
    service: str
    ts: int
    outgoing: bool
    answered: bool
    duration: int
    address: tuple | None


def service_of(provider):
    """The service of a ZSERVICE_PROVIDER: by bundle id, with or without the team id before it."""
    if not provider:
        return "phone"
    for p in (provider, provider.split(".", 1)[-1]):
        if p in SERVICES:
            return SERVICES[p]
    for bundle, service in SERVICES.items():     # a variant of a known app (e.g. com.viber.voip)
        if provider.lower().startswith(bundle.lower() + ".") or ("." + bundle.lower()) in provider.lower():
            return service
    return provider


def read_iphone(path):
    db = config.read_only(path)
    db.row_factory = sqlite3.Row
    handle = {}
    for call, value in db.execute(
            "SELECT j.Z_2REMOTEPARTICIPANTCALLS, h.ZVALUE FROM Z_2REMOTEPARTICIPANTHANDLES j "
            "JOIN ZHANDLE h ON h.Z_PK = j.Z_4REMOTEPARTICIPANTHANDLES ORDER BY h.Z_PK"):
        handle.setdefault(call, value)
    recs = []
    for r in db.execute("SELECT * FROM ZCALLRECORD ORDER BY Z_PK"):
        raw = dict(r)
        number = r["ZADDRESS"] or handle.get(r["Z_PK"])
        if r["ZADDRESS"] is None and number:
            raw["_participant"] = number
        duration = round(r["ZDURATION"] or 0)
        outgoing = bool(r["ZORIGINATED"])
        recs.append(Rec(IPHONE, r["ZUNIQUE_ID"], raw, service_of(r["ZSERVICE_PROVIDER"]),
                        round((r["ZDATE"] + APPLE_EPOCH) * 1000), outgoing,
                        duration > 0 if outgoing else bool(r["ZANSWERED"]), duration,
                        address(number) if number else None))
    return recs


def read_android(path, device):
    db = config.read_only(path)
    db.row_factory = sqlite3.Row
    recs = []
    for r in db.execute("SELECT * FROM calls ORDER BY CAST(_id AS INTEGER)"):
        duration = int(r["duration"] or 0)
        outgoing = r["type"] == ANDROID_OUTGOING
        recs.append(Rec(device, r["_id"], dict(r), "phone", int(r["date"]), outgoing,
                        duration > 0 if outgoing else r["type"] in ANDROID_ANSWERED, duration,
                        address(r["number"]) if r["number"] else None))
    return recs


def pair(iphone, android):
    index = defaultdict(list)
    for h in android:
        index[(h.address, h.outgoing)].append(h)
    used, pairs = set(), {}
    for i in sorted(iphone, key=lambda r: r.ts):
        if i.service != "phone":
            continue
        best = min((h for h in index.get((i.address, i.outgoing), ())
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
    android = [r for device, path, _ in exports for r in read_android(path, device)]
    pairs, used = pair(iphone, android)

    def from_android(h):
        return archive.keeper([IPHONE, h.source], h.ts) == h.source

    chosen = [h if (h := pairs.get(id(i))) and from_android(h) else i for i in iphone]
    chosen += [h for h in android if id(h) not in used]

    sources = {IPHONE: archive.source(f"{IPHONE}/calls", iphone_db, IPHONE)}
    for device, path, _ in exports:
        sources[device] = archive.source(f"{device}/calls", path, device)
    added = defaultdict(int)
    for r in sorted(chosen, key=lambda r: r.ts):
        sid = sources[r.source]
        if archive.has_origin(sid, r.row_key, "call_origin"):
            continue
        detail, code, video = extras.call(r.raw)
        archive.add_call(sid, r.row_key, service=r.service,
                         address_id=archive.address(*r.address) if r.address else None, ts=r.ts,
                         outgoing=r.outgoing, answered=r.answered, duration=r.duration,
                         detail=detail, detail_code=code, video=video)
        added[(r.source, r.service)] += 1
    # the copy not kept, as a second origin: later imports skip it without the other phone
    archive.record_pairs(sources, [(h, i) if from_android(h) else (i, h)
                                   for i in iphone if (h := pairs.get(id(i)))], "call_origin")
    for sid in sources.values():
        archive.imported(sid)
    archive.db.commit()

    print(f"iPhone: {len(iphone)} κλήσεις")
    print(f"Android ({', '.join(d for d, _, _ in exports) or '—'}): {len(android)} κλήσεις")
    print(f"ζεύγη:  {len(pairs)} (από το Android στην εποχή του: "
          f"{sum(1 for i in iphone if (h := pairs.get(id(i))) and from_android(h))})")
    for (src, service), n in sorted(added.items()):
        print(f"νέες:   {n:6} {src} {service}")
    if not added:
        print("νέες:   καμία")
