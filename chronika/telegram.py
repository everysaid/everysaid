"""Import Telegram from `<cache>/telegram/telegram.db`, which `scripts/telegram-sync.py` fills
through the Telegram API (each message whole, as Telethon's JSON).

Every chat is imported but channels and bots, which the sync does not read. Message ids are unique
only within a chat (the service's `key_scope`), so a message's key is its id and its row key in the
source `<chat>/<id>`. People are stored by phone number where Telegram shows it (so they meet the
same person on other services), else by their Telegram user id; their other handles join the same
person: the user id, the username and the name their profile shows (what the owner needs to tell
who is who, and to join them to their contacts later). The owner's own id is an account.
Calls, which Telegram keeps as service messages, go to `call` too, keyed by the call's id.
"""
from collections import defaultdict
import json
import os

from . import config
from .archive import CACHE, address

DB = os.path.join(CACHE, "telegram", "telegram.db")
MEDIA = os.path.join(CACHE, "telegram", "media")
SOURCE = "telegram"

# Service actions: those our vocabulary names; the rest are system messages with their code only.
ACTIONS = {"MessageActionPinMessage": "pin", "MessageActionPhoneCall": "call", "MessageActionGroupCall": "call",
           "MessageActionChatCreate": "group event", "MessageActionChatAddUser": "group event",
           "MessageActionChatDeleteUser": "group event", "MessageActionChatJoinedByLink": "group event",
           "MessageActionChatJoinedByRequest": "group event", "MessageActionChatEditTitle": "group event",
           "MessageActionChatEditPhoto": "group event", "MessageActionChatDeletePhoto": "group event",
           "MessageActionChatMigrateTo": "group event", "MessageActionChannelMigrateFrom": "group event"}
CALL_DETAIL = {"PhoneCallDiscardReasonBusy": "busy", "PhoneCallDiscardReasonDisconnect": "failed"}


def peer_id(peer):
    """Telethon's marked id of a Peer dict: users as they are, groups negative, -100... supergroups."""
    if not peer:
        return None
    if "user_id" in peer:
        return peer["user_id"]
    if "chat_id" in peer:
        return -peer["chat_id"]
    return -(10 ** 12 + peer["channel_id"])


def text_of(value):
    """Newer layers send some texts (poll questions and answers) as TextWithEntities."""
    return value.get("text") if isinstance(value, dict) else value


def attrs(doc):
    return {a["_"]: a for a in doc.get("attributes", [])}


def kind_of(m):
    """(kind, subtype, subtype_code, extra fields) of a message dict."""
    if m["_"] == "MessageService":
        name = m["action"]["_"]
        return "system", ACTIONS.get(name), f"telegram:{name}", {"text": m["action"].get("title")}
    media = m.get("media") or {}
    name = media.get("_")
    code = f"telegram:{name}" if name else None
    if not name:
        return "text", None, None, {}
    if name == "MessageMediaWebPage":
        return "text", "link", code, {}
    if name == "MessageMediaPhoto":
        return "image", None, None, {}
    if name in ("MessageMediaGeo", "MessageMediaGeoLive", "MessageMediaVenue"):
        geo = media.get("geo") or {}
        place = ", ".join(p for p in (media.get("title"), media.get("address")) if p) or None
        return "location", None, None, {"lat": geo.get("lat"), "lon": geo.get("long"), "place": place}
    if name == "MessageMediaContact":
        who = " ".join(p for p in (media.get("first_name"), media.get("last_name"), media.get("phone_number")) if p)
        return "contact", None, None, {"text": who or None}
    if name == "MessageMediaPoll":
        poll = media.get("poll") or {}
        lines = [text_of(poll.get("question"))] + [f"- {text_of(a.get('text'))}" for a in poll.get("answers", [])]
        return "text", "poll", code, {"text": "\n".join(l for l in lines if l) or None}
    if name != "MessageMediaDocument" or not media.get("document"):
        return "file", None, code, {}
    doc = media["document"]
    a = attrs(doc)
    mime = doc.get("mime_type") or ""
    if "DocumentAttributeSticker" in a:
        return "sticker", None, None, {}
    if "DocumentAttributeAnimated" in a:
        return "image", "gif", code, {}
    video = a.get("DocumentAttributeVideo")
    if video and video.get("round_message"):
        return "video", "video note", code, {}
    if video or mime.startswith("video/"):
        return "video", None, None, {}
    if a.get("DocumentAttributeAudio", {}).get("voice"):
        return "voice", None, None, {}
    if mime.startswith("image/"):
        return "image", None, None, {}
    return "file", None, None, {}


class People:
    """Telegram peer id -> (kind, value[, service]) address."""

    def __init__(self, db):
        self.entity = {}
        for pid, js in db.execute("SELECT id, json FROM entity UNION ALL SELECT id, json FROM chat"):
            self.entity[pid] = json.loads(js)
        self.me = next((pid for pid, e in self.entity.items() if e.get("is_self")), None)

    def __call__(self, pid):
        e = self.entity.get(pid, {})
        if e.get("phone"):
            return address("+" + e["phone"].lstrip("+"))
        return "id", str(pid), "telegram"

    def others(self, pid):
        """A user's handles besides the one __call__ gives: id, username."""
        e = self.entity.get(pid, {})
        if e.get("_") != "User":
            return []
        out = [("id", str(pid), "telegram")] if e.get("phone") else []
        out += [("username", u.lower(), "telegram") for u in [e.get("username")] if u]
        return out

    def name(self, pid):
        """The user's profile name, if they have one."""
        e = self.entity.get(pid, {})
        if e.get("_") != "User":
            return None
        return " ".join(p for p in (e.get("first_name"), e.get("last_name")) if p) or None


def reactions(m, person, own):
    r = m.get("reactions") or {}
    results = r.get("results") or []

    def emoji(x):
        x = x or {}
        if x.get("_") == "ReactionEmoji":
            return x.get("emoticon"), None
        if x.get("_") == "ReactionCustomEmoji":
            return None, f"telegram:custom:{x.get('document_id')}"
        return None, f"telegram:{x.get('_')}"

    recent = r.get("recent_reactions") or []
    if recent and len(recent) == sum(x.get("count", 0) for x in results):   # everyone who reacted, by name
        out = []
        for x in recent:
            who = person(peer_id(x.get("peer_id")))
            mine = x.get("my") or who in own
            out.append((*emoji(x.get("reaction")), 1, None if mine else who, 1 if mine else None))
        return out
    return [(*emoji(x.get("reaction")), x.get("count", 1), None, 1 if x.get("chosen_order") is not None else None)
            for x in results]


def call(archive, src, row_key, m, peer, ts):
    action = m["action"]
    if archive.has_origin(src, row_key, "call_origin"):
        return 0
    key = str(action.get("call_id"))
    if archive.db.execute("SELECT 1 FROM call WHERE service_id = ? AND key = ?",
                          (archive.service["telegram"], key)).fetchone():
        return 0
    reason = (action.get("reason") or {}).get("_")
    duration = action.get("duration") or 0
    outgoing = bool(m.get("out"))
    detail = CALL_DETAIL.get(reason)
    if reason == "PhoneCallDiscardReasonMissed" or (not duration and reason == "PhoneCallDiscardReasonHangup"):
        detail = "unanswered" if outgoing else "missed"
    archive.add_call(src, row_key, service="telegram", address_id=archive.address(*peer), ts=ts,
                     outgoing=outgoing, answered=duration > 0, duration=duration, key=key, detail=detail,
                     detail_code=f"telegram:{reason}" if reason else None, video=action.get("video"))
    return 1


def run(archive, db_path=DB, only=None, skip=()):
    """only: {(chat id, message id)}: just these (the live connection's new messages), else all.
    skip: chats not to import (the user's choice); what is already in the archive stays."""
    if not os.path.exists(db_path):
        print("καμία πηγή:", db_path, "(scripts/telegram-sync.py)")
        return
    db = config.read_only(db_path)
    chats_wanted = {c for c, _ in only} if only is not None else None
    person = People(db)
    if person.me is not None:
        archive.account(("id", str(person.me), "telegram"), "telegram")
    own = archive.own() | {("id", str(person.me), "telegram")}
    src = archive.source(SOURCE, db_path, "telegram", MEDIA)
    for pid in person.entity:
        for handle in person.others(pid):
            archive.alias(handle, person(pid))
    added, calls = defaultdict(int), 0
    for chat_id, kind, title in db.execute("SELECT id, kind, title FROM chat ORDER BY id").fetchall():
        if (chats_wanted is not None and chat_id not in chats_wanted) or chat_id in skip:
            continue
        if kind in ("user", "saved"):
            conv = archive.conversation("telegram", [person(chat_id)], key=str(chat_id), title=title)
        else:
            conv = archive.conversation("telegram", [], key=str(chat_id), title=title)
            archive.db.execute("UPDATE conversation SET is_group = 1 WHERE id = ?", (conv,))
        ids = sorted(m for c, m in only if c == chat_id) if only is not None else None
        rows = (db.execute(f"SELECT id, date, json FROM message WHERE chat_id = ? AND id IN ({','.join('?' * len(ids))}) "
                           f"ORDER BY id", (chat_id, *ids)) if ids is not None else
                db.execute("SELECT id, date, json FROM message WHERE chat_id = ? ORDER BY id", (chat_id,)))
        for mid, date, js in rows:
            row_key = f"{chat_id}/{mid}"
            m = json.loads(js)
            ts = date * 1000
            outgoing = bool(m.get("out"))
            if m["_"] == "MessageService" and m["action"]["_"] == "MessageActionPhoneCall" and kind == "user":
                calls += call(archive, src, row_key, m, person(chat_id), ts)
            if archive.has_origin(src, row_key):
                continue
            k, subtype, code, x = kind_of(m)
            x = {key: v for key, v in x.items() if v is not None}
            if subtype:
                x["subtype"], x["subtype_code"] = subtype, code
            reply = m.get("reply_to") or {}
            if reply.get("reply_to_msg_id") and not reply.get("reply_to_peer_id"):
                x["reply_key"] = str(reply["reply_to_msg_id"])
                if reply.get("quote_text"):
                    x["reply_text"] = reply["quote_text"]
            if m.get("edit_date") and not m.get("edit_hide"):
                x["edited"] = 1
            if m.get("fwd_from"):
                x["forwarded"] = 1
            if m.get("reactions"):
                x["reactions"] = reactions(m, person, own)
            sender = None
            if not outgoing:
                sender = person(peer_id(m.get("from_id")) or chat_id)
            archive.add_message(src, row_key, service="telegram", conversation_id=conv, ts=ts,
                                outgoing=outgoing, sender_id=archive.address(*sender) if sender else None,
                                kind=k, text=m.get("message") or None, key=str(mid), extras=x)
            added[kind] += 1
        archive.db.commit()
    for pid in person.entity:            # profile names, for the people the archive has
        if pid != person.me and person.name(pid):
            archive.handle_name(person(pid), "telegram", person.name(pid), "profile")
    # archived chats, as the last sync saw them (only the start of ours: see Archive.init_archived)
    for chat_id, archived, synced in db.execute("SELECT id, archived, synced_at FROM chat WHERE synced_at IS NOT NULL").fetchall():
        archive.report_state(src, archive.find_conversation("telegram", str(chat_id)), "archived", int(bool(archived)),
                             synced * 1000)
    archive.resolve()
    archive.imported(src)
    archive.db.commit()
    for kind, n in sorted(added.items()):
        print(f"νέα:     {n:7} {kind}")
    print(f"κλήσεις: {calls:7}")


def media(archive, store):
    """The downloaded files (telegram-sync.py --media), each to its message (media.py's step)."""
    if not os.path.exists(DB):
        return
    src = archive.source(SOURCE, DB, "telegram", MEDIA)
    origins = dict(archive.db.execute("SELECT row_key, message_id FROM message_origin WHERE source_id = ?", (src,)))
    db = config.read_only(DB)
    for chat_id, mid, rel in db.execute("SELECT chat_id, id, file FROM message WHERE file IS NOT NULL ORDER BY chat_id, id"):
        store.link(SOURCE, src, os.path.join(MEDIA, rel), rel, origins.get(f"{chat_id}/{mid}"))
