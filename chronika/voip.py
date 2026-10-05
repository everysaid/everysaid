"""Calls the phones' call logs do not have: WhatsApp's and Viber's, and missed calls known only from
the carrier's text messages.

- WhatsApp: its own call log (`CallHistory.sqlite` in the backup, extracted as
  whatsapp-calls.sqlite: start, duration, outcome, video, group, participants) and the call bubbles
  in the chats (ZWAMESSAGE type 59: the same facts in the media item's metadata,
  field 87: 1.1 video, 1.2 outcome, 1.3 duration in seconds, 1.5 participants). Both describe the same
  calls for the months they overlap; a call is kept once, the log's id as its key.
- Viber: the iPhone's recents (`ZRECENT`, with the number in `ZRECENTSLINE`).
  Viber Desktop's export of the Android phone has an empty `Calls` table: it knows only calls made on the
  desktop, so Viber calls of that phone's time are not anywhere.
- The carrier's missed-call notices, read from the archive's SMS by the parsers config
  `[import] carrier_notices` enables (`carriers/`; "gr" for the Greek ones): calls that
  came while the phone was off or busy, each with how many times the number called. Those a call
  log already has are left to it.

A call is skipped when the archive already has it: the same row (`call_origin`), or from another
source, for the same service, a call with the same other person in the same direction within a
minute (the iPhone's CallHistory shows some WhatsApp calls too). Calls without a known person are
never merged. Runs after `calls`, which does not look at the calls added here.
"""
from collections import defaultdict
from datetime import datetime
import os
import sqlite3

from .archive import APPLE_EPOCH, IPHONE, IPHONE_DATA, address
from . import carriers, config, whatsapp

DATA = IPHONE_DATA
WHATSAPP_CALLS = f"{DATA}/whatsapp-calls.sqlite"
VIBER = f"{DATA}/viber.sqlite"
SAME_CALL_MS = 60_000
# WhatsApp's outcome (ZWACDCALLEVENT.ZOUTCOME, field 87 / 1.2): 0 connected, 1 not answered; 4 and 5
# come only on incoming calls without duration (4 with the "missed" flag).
WHATSAPP_OUTCOMES = {4: "missed", 5: "failed"}
CARRIER_NOTICES = getattr(config, "CARRIER_NOTICES", None) or config.get("import", "carrier_notices", [])


def ro(path):
    db = config.read_only(path)
    db.row_factory = sqlite3.Row
    return db


class Calls:
    def __init__(self, archive):
        self.archive = archive
        self.added = defaultdict(int)

    def existing(self, source_id, service, address_id, ts, outgoing):
        """The same call from another source (calls of one source are all distinct, even a minute apart):
        same person and direction. Calls without a person (hidden numbers, groups) are never taken
        for one another."""
        if address_id is None:
            return None
        return self.archive.db.execute(
            "SELECT id, answered FROM call WHERE service_id = ? AND address_id = ? AND outgoing = ? "
            "AND abs(ts - ?) <= ? AND id NOT IN (SELECT call_id FROM call_origin WHERE source_id = ?) "
            "ORDER BY abs(ts - ?)",
            (self.archive.service[service], address_id, int(outgoing), ts, SAME_CALL_MS, source_id, ts)).fetchone()

    def add(self, source_id, row_keys, service, **call):
        """row_keys: every source row that describes this call; the call is added once."""
        db = self.archive.db
        for key in row_keys:
            if self.archive.has_origin(source_id, key, "call_origin"):
                return
        found = self.existing(source_id, service, call["address_id"], call["ts"], call["outgoing"])
        if found:
            call_id, answered = found
            # a call log that says answered is not overruled by "missed" or "busy" from elsewhere
            detail = None if answered else call.get("detail")
            code = None if answered else call.get("detail_code")
            db.execute("UPDATE call SET video = max(video, ?), conversation_id = coalesce(conversation_id, ?), "
                       "detail_code = CASE WHEN detail IS NULL THEN ? ELSE detail_code END, "
                       "detail = coalesce(detail, ?), key = coalesce(key, ?), attempts = max(attempts, ?) "
                       "WHERE id = ?",
                       (call.get("video", 0), call.get("conversation_id"), code, detail, call.get("key"),
                        call.get("attempts") or 1, call_id))
        else:
            call_id = self.archive.add_call(source_id, row_keys[0], service=service, **call)
            row_keys = row_keys[1:]
            self.added[service] += 1
        for key in row_keys:
            db.execute("INSERT OR IGNORE INTO call_origin VALUES (?, ?, ?)", (source_id, key, call_id))
        return call_id


def outcome_detail(outcome, outgoing):
    """(detail, its code) of a WhatsApp outcome."""
    if outcome == 1:
        return "unanswered" if outgoing else "missed", "whatsapp:1"
    return (WHATSAPP_OUTCOMES[outcome], f"whatsapp:{outcome}") if outcome in WHATSAPP_OUTCOMES else (None, None)


def whatsapp_calls(archive, calls):
    if not os.path.exists(WHATSAPP_CALLS) or not os.path.exists(whatsapp.IPHONE_DB):
        return
    iphone = ro(whatsapp.IPHONE_DB)
    person = whatsapp.People(whatsapp.ro_bridge(whatsapp.CONTACTS_DB), whatsapp.ro_bridge(whatsapp.BRIDGE_STORE),
                             iphone, whatsapp.ro_bridge(whatsapp.BRIDGE_DB))
    own = archive.own()
    log_src = archive.source(f"{IPHONE}/whatsapp-calls", WHATSAPP_CALLS, IPHONE)
    chat_src = archive.source(f"{IPHONE}/whatsapp", whatsapp.IPHONE_DB, IPHONE)

    def addr(jid):
        p = person(jid) if jid else None
        return archive.address(*p) if p and p not in own else None

    def group(jid):
        row = archive.db.execute("SELECT id FROM conversation WHERE key = ?", (jid,)).fetchone() if jid else None
        return row[0] if row else None

    # the call bubbles in the chats
    bubbles = []
    for r in iphone.execute("SELECT m.Z_PK, m.ZMESSAGEDATE, m.ZISFROMME, s.ZCONTACTJID, s.ZSESSIONTYPE, i.ZMETADATA "
                            "FROM ZWAMESSAGE m JOIN ZWACHATSESSION s ON s.Z_PK = m.ZCHATSESSION "
                            "LEFT JOIN ZWAMEDIAITEM i ON i.Z_PK = m.ZMEDIAITEM WHERE m.ZMESSAGETYPE = 59"):
        info = whatsapp.protobuf_fields(r["ZMETADATA"] or b"").get(87)
        fields = whatsapp.protobuf_fields(whatsapp.protobuf_fields(info[0]).get(1, [b""])[0]) if info else {}
        members = [whatsapp.protobuf_fields(p).get(1, [b""])[0].decode() for p in fields.get(5, [])]
        bubbles.append(dict(pk=str(r["Z_PK"]), ts=round((r["ZMESSAGEDATE"] + APPLE_EPOCH) * 1000),
                            outgoing=bool(r["ZISFROMME"]), jid=r["ZCONTACTJID"], is_group=r["ZSESSIONTYPE"] == 1,
                            video=(fields.get(1) or [0])[0], outcome=(fields.get(2) or [None])[0],
                            duration=(fields.get(3) or [0])[0], members=members))

    # the call log
    log = []
    db = ro(WHATSAPP_CALLS)
    parts = defaultdict(list)
    for r in db.execute("SELECT Z1PARTICIPANTS, ZJIDSTRING, ZOUTCOME FROM ZWACDCALLEVENTPARTICIPANT"):
        parts[r[0]].append((r[1], r[2]))
    for r in db.execute("SELECT e.*, a.ZINCOMING, a.ZMISSED, a.ZVIDEO FROM ZWACDCALLEVENT e "
                        "LEFT JOIN ZWAAGGREGATECALLEVENT a ON a.Z_PK = e.Z1CALLEVENTS"):
        log.append(dict(pk=str(r["Z_PK"]), ts=round((r["ZDATE"] + APPLE_EPOCH) * 1000), outgoing=not r["ZINCOMING"],
                        video=r["ZVIDEO"] or 0, outcome=r["ZOUTCOME"], duration=round(r["ZDURATION"] or 0),
                        # participants belong to the aggregate event (Z1PARTICIPANTS -> entity 1), not to each call
                        group=r["ZGROUPJIDSTRING"], key=r["ZCALLIDSTRING"], members=parts[r["Z1CALLEVENTS"]],
                        creator=r["ZGROUPCALLCREATORUSERJIDSTRING"]))

    # One call in both: the bubble falls between the call's start (a minute's leeway) and its end
    # plus a minute, in the same direction; the nearest such. People are taken from the bubble's
    # chat, which names them by number (the log uses WhatsApp's internal ids).
    pairs, free = {}, sorted(bubbles, key=lambda b: b["ts"])
    for c in sorted(log, key=lambda c: c["ts"]):
        window = [b for b in free if b["outgoing"] == c["outgoing"]
                  and c["ts"] - SAME_CALL_MS <= b["ts"] <= c["ts"] + c["duration"] * 1000 + SAME_CALL_MS]
        if window:
            b = min(window, key=lambda b: abs(b["ts"] - c["ts"]))
            pairs[c["pk"]] = b
            free.remove(b)
    for c in log:
        b = pairs.get(c["pk"])
        group_jid = c["group"] or (b["jid"] if b and b["is_group"] else None)
        # who: the bubble's chat; else the log's participant; else who started it (the other
        # person on an incoming call; on an outgoing one that is the owner, and stays unknown)
        peer = (addr(b["jid"]) if b and not b["is_group"] else None if group_jid else
                next((addr(j) for j, _ in c["members"]), None) or addr(c["creator"]))
        detail, code = outcome_detail(c["outcome"], c["outgoing"])
        call_id = calls.add(log_src, [c["pk"]], "whatsapp", address_id=peer, ts=c["ts"], outgoing=c["outgoing"],
                            answered=c["outcome"] == 0, duration=c["duration"] or (b["duration"] if b else 0),
                            key=c["key"], detail=detail, detail_code=code,
                            video=c["video"] or (b["video"] if b else 0), conversation_id=group(group_jid))
        if call_id and b:
            archive.db.execute("INSERT OR IGNORE INTO call_origin VALUES (?, ?, ?)", (chat_src, b["pk"], call_id))
        if call_id and group_jid:
            for jid, outcome in c["members"] or [(j, None) for j in (b["members"] if b else [])]:
                said, code = ("joined", "whatsapp:0") if outcome == 0 else outcome_detail(outcome, False)
                archive.db.execute("INSERT OR IGNORE INTO call_member VALUES (?, ?, ?, ?)",
                                   (call_id, addr(jid), said, code))
    for b in free:
        detail, code = outcome_detail(b["outcome"], b["outgoing"])
        call_id = calls.add(chat_src, [b["pk"]], "whatsapp",
                            address_id=None if b["is_group"] else addr(b["jid"]), ts=b["ts"], outgoing=b["outgoing"],
                            answered=b["outcome"] == 0, duration=b["duration"], detail=detail, detail_code=code,
                            video=b["video"], conversation_id=group(b["jid"]) if b["is_group"] else None)
        if call_id and b["is_group"]:
            for jid in b["members"]:
                archive.db.execute("INSERT OR IGNORE INTO call_member (call_id, address_id) VALUES (?, ?)",
                                   (call_id, addr(jid)))


def viber_calls(archive, calls):
    if not os.path.exists(VIBER):
        return
    db = ro(VIBER)
    src = archive.source(f"{IPHONE}/viber-calls", VIBER, IPHONE)
    for r in db.execute("SELECT r.Z_PK, r.ZDATE, r.ZDURATION, r.ZCALLTYPE, r.ZCALLTOKEN, l.ZPHONENUMBER "
                        "FROM ZRECENT r LEFT JOIN ZRECENTSLINE l ON l.Z_PK = r.ZRECENTSLINE"):
        kind = r["ZCALLTYPE"] or ""
        outgoing = kind.startswith("outgoing")
        calls.add(src, [str(r["Z_PK"])], "viber",
                  address_id=archive.address(*address(r["ZPHONENUMBER"])) if r["ZPHONENUMBER"] else None,
                  ts=round((r["ZDATE"] + APPLE_EPOCH) * 1000), outgoing=outgoing,
                  answered=(r["ZDURATION"] or 0) > 0, duration=r["ZDURATION"] or 0,
                  key=str(r["ZCALLTOKEN"]) if r["ZCALLTOKEN"] else None,
                  detail="missed" if kind == "missed" else "unanswered" if outgoing and not r["ZDURATION"] else None,
                  detail_code=f"viber:{kind}" if kind == "missed" else None, video=int("video" in kind))


def carrier_alerts(archive, calls, carrier):
    """The calls a carrier's notices tell of (carrier: a module of `carriers`)."""
    src = archive.source(carrier.SOURCE, "the archive's SMS")
    sms = archive.service["sms"]
    name = carrier.__name__.rsplit(".", 1)[-1]
    for mid, ts, text in archive.db.execute(
            "SELECT id, ts, text FROM message WHERE service_id = ? AND NOT outgoing AND text IS NOT NULL", (sms,)).fetchall():
        sent = datetime.fromtimestamp(ts / 1000, config.TIMEZONE)     # the owner's time zone ([owner] timezone)
        for i, (number, when, attempts, busy) in enumerate(carrier.alerts(text, sent)):
            kind, value = address(number)
            address_id, ts = archive.address(kind, value), int(when.timestamp() * 1000)
            # the carrier sometimes sends the same notice twice: the same number at the same minute
            # is one call, whichever message told of it
            same = archive.db.execute(
                "SELECT c.id FROM call c JOIN call_origin o ON o.call_id = c.id WHERE o.source_id = ? "
                "AND c.address_id = ? AND c.ts = ?", (src, address_id, ts)).fetchone()
            if same:
                archive.db.execute("INSERT OR IGNORE INTO call_origin VALUES (?, ?, ?)", (src, f"{mid}/{i}", same[0]))
                continue
            calls.add(src, [f"{mid}/{i}"], "phone", address_id=address_id, ts=ts, outgoing=False, answered=False,
                      duration=0, detail="busy" if busy else "missed", detail_code=f"carrier:{name}",
                      attempts=attempts)


def run(archive):
    calls = Calls(archive)
    whatsapp_calls(archive, calls)
    viber_calls(archive, calls)
    for carrier in carriers.enabled(CARRIER_NOTICES):
        carrier_alerts(archive, calls, carrier)
    archive.db.commit()
    for service, n in sorted(calls.added.items()):
        print(f"νέες:   {n:6} {service}")
    if not calls.added:
        print("νέες:   καμία")
