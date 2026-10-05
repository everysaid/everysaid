"""What a message carries beyond its text: the message it answers, reactions, edits and deletions,
forwarding, a star, a place, a shared contact or poll, and its kind as the service names it.

Each function reads one source's row (as the importers see it) and returns a dict for
`Archive.add_message(..., extras=...)`; keys are left out when there is nothing to say:

- reply_key, reply_text: the service key of the message answered, or reacted to by a tapback
  (resolved to `message.reply_to` by `Archive.resolve()`), and the quoted text, kept only where
  that message is not in the archive;
- subtype: the service's own kind where ours is coarser (link, gif, video note, poll, pin...; the
  archive's vocabulary), and subtype_code, the source's own code for it ('whatsapp:54', 'viber:url');
- edited, deleted, forwarded, starred: 1;
- lat, lon, place: a location sent; on any other message (older Viber), where the sender was when
  sending (`Archive.add_message` keeps that as sender_lat, sender_lon);
- text: the content of a shared contact or a poll, where the message has no text of its own;
- reactions: [(emoji, code, count, sender, outgoing)]: the emoji where known, the service's code
  ('viber:6'); the sender as (kind, value[, service]), "peer" for the other person of a one-to-one
  chat, or None; outgoing 1 for the owner's own reaction, else None;
- edits_key: for an edit event (Viber on the iPhone), the key of the message it edited;
- reacts_to: for a reaction sent as a message of its own (iMessage tapback), (key, emoji, code).

Found by reading every field of every source (October 2026); left out on purpose:
read and delivery times, the names apps show, link previews (the link is in the text), the
sender's time zone, language guesses, and iMessage's `reply_to_guid`, which iOS sets on ordinary
messages too (the previous one in the chat), so it is not a reply.
"""
import json

# Viber's reaction codes, as its apps show them; 6 and later ones are kept as codes only.
VIBER_REACTIONS = {1: "❤️", 2: "😂", 3: "😮", 4: "😢", 5: "😡"}
# iMessage tapbacks (associated_message_type); 2006 carries its own emoji.
TAPBACKS = {2000: "❤️", 2001: "👍", 2002: "👎", 2003: "😂", 2004: "‼️", 2005: "❓"}
VIBER_DESKTOP_SUBTYPES = {9: "link", 15: "pin"}
VIBER_MEDIA = (2, 3, 11)                # picture, video, file: a Subject on these is their caption
VIBER_IPHONE_SUBTYPES = {"url": "link", "systemGeneralMessageRemoved": "deleted",
                         "systemPinnedMessageCreated": "pin", "systemCallLog": "call",
                         "systemInvalidMessage": "invalid", "customLocation": "location"}
# ZWAMESSAGE.ZMESSAGETYPE (see whatsapp.py); only those our kind does not already say.
WHATSAPP_SUBTYPES = {7: "link", 11: "gif", 14: "deleted", 54: "video note", 6: "group event",
                     10: "notice", 66: "poll", 59: "call"}  # 10: security code, number changed...; 59: a call


def _json(value):
    if isinstance(value, dict):
        return value
    try:
        return json.loads(value) if value else {}
    except ValueError:
        return {}


def _reaction(code):
    """(emoji or None, code or None) of a Viber reaction: a number, or an emoji itself."""
    try:
        code = int(code)
    except (TypeError, ValueError):
        return str(code), None
    return VIBER_REACTIONS.get(code), f"viber:{code}"


def _str(value):
    return value.decode("utf-8", "replace") if isinstance(value, bytes) else value


def _contact(name, number):
    parts = [p for p in ((name or "").strip("⁨⁩ "), number) if p]
    return "📇 " + ", ".join(parts) if parts else None


def viber_desktop(raw):
    """A row of the Viber Desktop export (Events joined with Messages)."""
    out, info = {}, _json(raw.get("Info"))
    if raw.get("MessageType") in VIBER_DESKTOP_SUBTYPES:
        out["subtype"], out["subtype_code"] = VIBER_DESKTOP_SUBTYPES[raw["MessageType"]], f"viber:{raw['MessageType']}"
    if info.get("ivmInfo"):
        out["subtype"], out["subtype_code"] = "video note", "viber:ivmInfo"
    if info.get("ClientInnerMessageType") == "EXPRESSION_PANEL_GIF":
        out["subtype"], out["subtype_code"] = "gif", "viber:EXPRESSION_PANEL_GIF"
    if raw.get("MessageType") in VIBER_MEDIA and raw.get("Subject") and not raw.get("Body"):
        out["text"] = raw["Subject"]
    quote = info.get("quote")
    if isinstance(quote, dict) and quote.get("token"):
        out["reply_key"] = str(quote["token"])
        if quote.get("text"):
            out["reply_text"] = quote["text"]
    if "edit" in info:
        out["edited"] = 1
    if info.get("generalFwdInfo"):
        out["forwarded"] = 1
    reactions = [(*_reaction(r.get("type")), r.get("count") or 1, None, None)
                 for r in info.get("messageReactions") or [] if r.get("count")]
    meta = info.get("reaction_meta_info") or {}
    if not reactions:                   # one-to-one chats: who reacted is known, the emoji is not kept
        if meta.get("talker_current_reaction_token"):
            reactions.append((None, "viber:?", 1, "peer", None))
        if meta.get("my_current_reaction_token"):
            reactions.append((None, "viber:?", 1, None, 1))
    if reactions:
        out["reactions"] = reactions
    if raw.get("ContactLatitude") or raw.get("ContactLongitude"):
        # a location sent, or on any other message where the sender was (older Viber)
        out["lat"], out["lon"] = raw["ContactLatitude"] / 1e7, raw["ContactLongitude"] / 1e7
        if (info.get("locationInfo") or {}).get("address"):
            out["place"] = info["locationInfo"]["address"]
    if raw.get("MessageType") == 10 and not raw.get("Body"):
        text = _contact(info.get("Name"), info.get("PhoneNumber") or info.get("ViberNumber"))
        if text:
            out["text"] = text
    return out


def viber_iphone(raw, locations=None):
    """A ZVIBERMESSAGE row of the iPhone's viber.sqlite; locations: ZLOCATION -> (lat, lon, place)."""
    out, md, cm = {}, _json(raw.get("ZMETADATA")), _json(raw.get("ZCLIENTMETADATA"))
    system = raw.get("ZSYSTEMTYPE")
    if system in VIBER_IPHONE_SUBTYPES:
        out["subtype"], out["subtype_code"] = VIBER_IPHONE_SUBTYPES[system], f"viber:{system}"
    if md.get("ClientInnerMessageType") == "EXPRESSION_PANEL_GIF":
        out["subtype"], out["subtype_code"] = "gif", "viber:EXPRESSION_PANEL_GIF"
    if system == "systemCallLog" and raw.get("ZCALLTYPE"):       # incoming, missed, outgoing_viber, ..._with_video
        count = raw.get("ZCALLSCOUNT") or 1
        out["subtype_code"] = f"viber:{raw['ZCALLTYPE']}" + (f" ×{count}" if count > 1 else "")
    if system == "systemGeneralMessageRemoved":
        out["deleted"] = 1
    quote = md.get("quote")
    if isinstance(quote, dict) and quote.get("token"):
        out["reply_key"] = str(quote["token"])
        if quote.get("text"):
            out["reply_text"] = quote["text"]
    edit = md.get("edit")
    if isinstance(edit, dict) and edit.get("token"):
        out["edits_key"] = str(edit["token"])      # this row is the edit event; the token is the message
    if cm.get("EditDate") or cm.get("EditToken"):
        out["edited"] = 1
    if md.get("generalFwdInfo") or raw.get("ZFORWARDTYPE"):
        out["forwarded"] = 1
    reactions = []
    for code, count in ((cm.get("Reactions") or {}).get("reactions") or {}).items():
        if count:
            reactions.append((*_reaction(code), count, None, None))
    one = cm.get("Reactions1on1OnMessageMetadata") or {}
    if one.get("interlocutorReaction") or one.get("stableReaction"):
        # one-to-one: whose reaction it is (the other person's, or the owner's), instead of a bare count
        reactions = []
        if (one.get("interlocutorReaction") or {}).get("type"):
            reactions.append((*_reaction(one["interlocutorReaction"]["type"]), 1, "peer", None))
        if (one.get("stableReaction") or {}).get("type"):
            reactions.append((*_reaction(one["stableReaction"]["type"]), 1, None, 1))
    if not reactions and raw.get("ZLIKESCOUNT"):
        reactions.append((*_reaction(raw.get("ZLIKESTYPE") or 1), raw["ZLIKESCOUNT"], None, None))
    if reactions:
        out["reactions"] = reactions
    if raw.get("ZLOCATION") and locations and raw["ZLOCATION"] in locations:
        out["lat"], out["lon"], place = locations[raw["ZLOCATION"]]
        if place:
            out["place"] = place
    poll = cm.get("Poll")
    if isinstance(poll, list) and poll:
        options = "\n".join(f"• {o.get('title', '')} ({o.get('count', 0)})" for o in poll)
        out["subtype"], out["subtype_code"] = "poll", "viber:Poll"
        out["text"] = ((raw.get("ZTEXT") or "") + "\n" + options).strip()
    return out


ANDROID_CALL_DETAIL = {"3": "missed", "5": "rejected", "6": "blocked"}
FACETIME_VIDEO = 8                      # ZCALLRECORD.ZCALLTYPE: 8 video, 16 audio


def call(raw):
    """What a call record says beyond answered and duration: (detail: missed, rejected or blocked;
    its code; video)."""
    kind = str(raw.get("type"))
    detail = ANDROID_CALL_DETAIL.get(kind)
    return detail, f"android:{kind}" if detail else None, int(raw.get("ZCALLTYPE") == FACETIME_VIDEO)


def imessage(raw):
    """A message row of the iPhone's sms.db."""
    out, kind = {}, raw.get("associated_message_type")
    if kind and raw.get("associated_message_guid"):
        guid = raw["associated_message_guid"].split("/", 1)[-1]
        emoji = raw.get("associated_message_emoji") or TAPBACKS.get(int(kind))
        if emoji and int(kind) < 3000:              # 3000s take a tapback back
            out["reacts_to"] = (guid, emoji, f"imessage:{kind}")
            out["reply_key"] = guid                 # kept even where it cannot be resolved (SMS have no key)
            out["subtype"], out["subtype_code"] = "tapback", f"imessage:{kind}"
    return out


def whatsapp(row, meta=None, receipt=None, media=None):
    """A ZWAMESSAGE row with what the iPhone keeps beside it: its media item's protobuf metadata,
    its message info's receipt protobuf, and the media item (lat, lon, vcard)."""
    from .whatsapp import protobuf_fields
    out, mtype = {}, row.get("ZMESSAGETYPE")
    if mtype in WHATSAPP_SUBTYPES:
        out["subtype"], out["subtype_code"] = WHATSAPP_SUBTYPES[mtype], f"whatsapp:{mtype}"
    if mtype == 14:
        out["deleted"] = 1
    if row.get("ZSTARRED"):
        out["starred"] = 1
    fields = protobuf_fields(meta) if meta else {}
    if fields.get(5):
        out["reply_key"] = _str(fields[5][0])
        quoted = protobuf_fields(fields[19][0]) if isinstance((fields.get(19) or [None])[0], bytes) else {}
        if quoted.get(1) and isinstance(quoted[1][0], bytes):
            out["reply_text"] = _str(quoted[1][0])
    if isinstance((fields.get(46) or [0])[0], int) and (fields.get(46) or [0])[0] > 0:
        out["forwarded"] = 1
    if receipt:
        reactions = []
        for block in protobuf_fields(receipt).get(7, []):
            for item in protobuf_fields(block).get(1, []):
                r = protobuf_fields(item)
                emoji = _str((r.get(3) or [None])[0])
                if emoji:
                    jid = _str((r.get(2) or [None])[0])
                    reactions.append((emoji, None, 1, jid, None if jid else 1))
        if reactions:
            out["reactions"] = reactions        # senders as jids: the importer maps them to people
    if media:
        if mtype == 5 and (media.get("ZLATITUDE") or media.get("ZLONGITUDE")):
            out["lat"], out["lon"] = media["ZLATITUDE"], media["ZLONGITUDE"]
            if media.get("ZTITLE"):
                out["place"] = media["ZTITLE"]
        if mtype == 4 and not row.get("ZTEXT"):
            number = None
            for line in (media.get("ZVCARDSTRING") or "").splitlines():
                if line.upper().startswith("TEL") and ":" in line:
                    number = line.split(":", 1)[1].strip()
                    break
            text = _contact(media.get("ZVCARDNAME"), number)
            if text:
                out["text"] = text
    return out
