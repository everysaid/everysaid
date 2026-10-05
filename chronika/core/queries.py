"""Questions about the archive. Everything returns plain dicts and lists (JSON as it is).

Chats: the list a messenger shows. A person is one chat, whatever services they were reached on:
all their one-to-one conversations and their calls make one stream (`p<person id>`). A group, or a
conversation that is no one person's (a notes-to-self chat), is a chat of its own
(`c<conversation id>`).

Streams are read page by page with a cursor: `before` (older) or `after` (newer) an item, given as
the `cursor` of an item (`<ts>:<m|c>:<id>`), or `around` a time (Unix ms).
"""
from collections import defaultdict
import os
import re

from .. import archive, text as text_mod
from .names import people

PAGE = 60


# --- lookups -------------------------------------------------------------------------------------

def _lookups(store):
    def build():
        db = store.read()
        return {
            "service": dict(db.execute("SELECT id, name FROM service")),
            "kind": dict(db.execute("SELECT id, name FROM message_kind")),
        }
    return store.cached("lookups", build)


def unread_since(store):
    return store.setting("unread_since", 0) or 0


# --- the chat list -------------------------------------------------------------------------------

def _chat_index(store):
    """Which conversations and addresses make each chat, built once per archive version."""
    def build():
        db = store.read()
        ppl = people(store)
        lk = _lookups(store)
        members = defaultdict(list)
        for cid, aid in db.execute("SELECT conversation_id, address_id FROM conversation_member"):
            members[cid].append(aid)
        last = dict(db.execute("SELECT conversation_id, max(ts) FROM message GROUP BY conversation_id"))
        chats = {}              # chat id -> dict
        conv_chat = {}
        for cid, sid, is_group, title in db.execute("SELECT id, service_id, is_group, title FROM conversation"):
            others = {ppl.person_of.get(a) for a in members[cid] if a not in ppl.own_addresses}
            others.discard(None)
            others -= ppl.me
            if not is_group and len(others) == 1:
                pid = others.pop()
                key = f"p{pid}"
                chat = chats.setdefault(key, {"id": key, "type": "person", "person_id": pid,
                                              "conversations": [], "services": set(), "last_ts": 0})
            else:
                key = f"c{cid}"
                chat = chats.setdefault(key, {"id": key, "type": "group" if is_group else "conversation",
                                              "conversation_id": cid, "title": title,
                                              "conversations": [], "services": set(), "last_ts": 0})
            chat["conversations"].append(cid)
            chat["services"].add(lk["service"][sid])
            chat["last_ts"] = max(chat["last_ts"], last.get(cid) or 0)
            conv_chat[cid] = key
        for aid, sid, ts in db.execute(
                "SELECT address_id, service_id, max(ts) FROM call WHERE conversation_id IS NULL "
                "AND address_id IS NOT NULL GROUP BY address_id, service_id"):
            pid = ppl.person_of.get(aid)
            if pid is None or pid in ppl.me:
                continue
            key = f"p{pid}"
            chat = chats.setdefault(key, {"id": key, "type": "person", "person_id": pid,
                                          "conversations": [], "services": set(), "last_ts": 0})
            chat["services"].add(lk["service"][sid])
            chat["last_ts"] = max(chat["last_ts"], ts or 0)
            chat["has_calls"] = True
        return chats, conv_chat
    return store.cached("chat_index", build)


FIELDS = ("pinned", "muted", "hidden", "read_until")


def _states(store):
    """chat -> (pinned, muted, hidden, read_until, origin): what applies to each chat, from what its
    sources report (`state_report`) and what the user chose (`chat_state`).

    Between sources: of one service, the newest report; between services, the highest weight its
    plugin declares for the field (0: not applied), then the latest change; read_until, the latest
    (read anywhere is read). Between the user and the sources, the later change wins, unless the
    user chose `always`. A chat hidden by a service comes back on a newer message from a service
    that has not hidden it, if the user wants so (setting `hidden_returns`, off by default).
    origin: {field: "user" | service} of what applies."""
    import time
    from .. import plugins
    raw = store.cached("states", lambda: _state_parts(store))
    now = int(time.time() * 1000)
    returns = store.setting("hidden_returns", False)
    out = {}
    for chat, parts in raw.items():
        values, origin = {}, {}
        for f in FIELDS:
            reps, user = parts["reports"].get(f, []), parts["user"].get(f)
            svc = None
            if f == "read_until":
                svc = max(reps, key=lambda r: r["value"], default=None)
            else:
                newest = {}
                for r in reps:          # one service, several sources: the newest says
                    k = (r["conversation"], r["service"])
                    if k not in newest or r["observed"] > newest[k]["observed"]:
                        newest[k] = r
                weighed = [r for r in newest.values() if plugins.state_weight(r["plugin"], f) > 0]
                svc = max(weighed, key=lambda r: (plugins.state_weight(r["plugin"], f), r["changed"]), default=None)
            if user and (user["always"] or svc is None or user["set_at"] >= svc["changed"]):
                v, by = user["value"], "user"
                if f == "read_until" and svc:
                    v = max(v, svc["value"])
            elif svc:
                v, by = svc["value"], svc["service"]
            else:
                v, by = (None if f == "read_until" else 0), None
            if f == "hidden" and v and by not in (None, "user") and returns:
                hidden_on = {r["conversation"] for r in parts["reports"].get("hidden", []) if r["value"]}
                if any(ts > svc["changed"] and c not in hidden_on for c, ts in parts["last"].items()):
                    v = 0
            values[f], origin[f] = v, by
        m = values["muted"]       # the user's: 0/1; a service's: until (ms), -1 for ever
        muted = bool(m) if origin["muted"] == "user" else m == -1 or m > now
        out[chat] = (int(bool(values["pinned"])), int(muted), int(bool(values["hidden"])), values["read_until"], origin)
    return out


def _state_reports(store, chat_id):
    """What each service says about a chat, for showing: {field: [{service, value}]} (muted: whether
    muted now)."""
    import time
    parts = store.cached("states", lambda: _state_parts(store)).get(chat_id)
    out = {}
    for f, reps in (parts["reports"].items() if parts else ()):
        by = {}
        for r in sorted(reps, key=lambda r: r["observed"]):
            v = r["value"]
            by[r["service"]] = (v == -1 or v > time.time() * 1000) if f == "muted" else v
        out[f] = [{"service": k, "value": v} for k, v in by.items()]
    return out


def _state_parts(store):
    """What _states combines, per chat, built once per archive version."""
    db = store.read()
    index, conv_chat = _chat_index(store)
    parts = defaultdict(lambda: {"reports": defaultdict(list), "user": {}, "last": {}})
    for conv, plugin, service, field, value, observed, changed in db.execute(
            "SELECT r.conversation_id, i.plugin, s.name, r.field, r.value, r.observed_at, r.changed_at FROM state_report r "
            "JOIN plugin_instance i ON i.id = r.instance_id AND i.enabled JOIN conversation c ON c.id = r.conversation_id "
            "JOIN service s ON s.id = c.service_id"):
        chat = conv_chat.get(conv)
        if chat:
            parts[chat]["reports"][field].append({"conversation": conv, "plugin": plugin, "service": service,
                                                  "value": value, "observed": observed, "changed": changed})
    for chat, field, value, set_at, always in db.execute("SELECT chat, field, value, set_at, always FROM chat_state"):
        if chat in index:
            parts[chat]["user"][field] = {"value": value, "set_at": set_at, "always": always}
    hidden = [c for c, p in parts.items() if any(r["value"] for r in p["reports"].get("hidden", []))]
    for chat in hidden:
        for conv in index[chat]["conversations"]:
            ts = db.execute("SELECT max(ts) FROM message WHERE conversation_id = ?", (conv,)).fetchone()[0]
            if ts:
                parts[chat]["last"][conv] = ts
    return dict(parts)


def chat_title(store, chat):
    if chat["type"] == "person":
        return people(store).name(chat["person_id"])
    if chat.get("title"):
        return chat["title"]
    if chat["type"] == "conversation":
        from ..plugins.i18n import tr
        return tr("Notes", store.setting("language") or "en")       # notes to oneself
    db = store.read()
    names = [people(store).name_of_address(a) for (a,) in db.execute(
        "SELECT address_id FROM conversation_member WHERE conversation_id = ? LIMIT 4", (chat["conversation_id"],))]
    return ", ".join(n for n in names if n) or f"#{chat['conversation_id']}"


def _last_item(store, chat):
    db = store.read()
    convs = chat["conversations"]
    row = None
    if convs:
        q = ",".join("?" * len(convs))
        row = db.execute(f"SELECT id, ts, outgoing, kind_id, text, service_id, sender_id, subtype, deleted FROM message "
                         f"WHERE conversation_id IN ({q}) ORDER BY ts DESC, id DESC LIMIT 1", convs).fetchone()
    call = None
    if chat.get("has_calls"):
        addrs = people(store).addresses(chat["person_id"])
        q = ",".join("?" * len(addrs))
        call = db.execute(f"SELECT id, ts, outgoing, answered, video, service_id, detail FROM call "
                          f"WHERE address_id IN ({q}) AND conversation_id IS NULL ORDER BY ts DESC LIMIT 1", addrs).fetchone()
    lk = _lookups(store)
    if call and (not row or call[1] >= row[1]):
        return {"type": "call", "ts": call[1], "outgoing": bool(call[2]), "answered": bool(call[3]),
                "video": bool(call[4]), "service": lk["service"][call[5]], "detail": call[6]}
    if row:
        mid, ts, outgoing, kind_id, txt, sid, sender, subtype, deleted = row
        return {"type": "message", "id": mid, "ts": ts, "outgoing": bool(outgoing), "kind": lk["kind"][kind_id],
                "text": (txt or "")[:160], "service": lk["service"][sid], "subtype": subtype, "deleted": bool(deleted),
                "sender": people(store).name_of_address(sender) if chat["type"] == "group" and sender else None}
    return None


def _unread(store, chat, since):
    if not chat["conversations"]:
        return 0
    q = ",".join("?" * len(chat["conversations"]))
    return store.read().execute(
        f"SELECT count(*) FROM message WHERE conversation_id IN ({q}) AND outgoing = 0 AND ts > ?",
        (*chat["conversations"], since)).fetchone()[0]


def chats(store, include_hidden=False, kind=None, q=None, limit=None, offset=0):
    """The chat list, newest first, pinned ones on top: [{id, type, title, services, last, unread,
    pinned, muted, avatar}]. kind: person, group or conversation; q: a part of the title."""
    index, _ = _chat_index(store)
    states = _states(store)
    base = unread_since(store)
    ppl = people(store)
    items = []
    qf = text_mod.fold(q) if q else None
    for chat in index.values():
        pinned, muted, hidden, read, _ = states.get(chat["id"], (0, 0, 0, None, {}))
        if hidden and not include_hidden:
            continue
        if kind and chat["type"] != kind:
            continue
        title = chat_title(store, chat)
        if qf and qf not in text_mod.fold(title):
            continue
        items.append((bool(pinned), chat["last_ts"], chat, title, muted, hidden, read))
    items.sort(key=lambda x: (x[0], x[1]), reverse=True)
    if limit:
        items = items[offset:offset + limit]
    out = []
    for pinned, last_ts, chat, title, muted, hidden, read in items:
        since = max(read or 0, base)
        out.append({
            "id": chat["id"], "type": chat["type"], "title": title,
            "person_id": chat.get("person_id"), "conversation_id": chat.get("conversation_id"),
            "services": sorted(chat["services"]), "last_ts": last_ts,
            "last": _last_item(store, chat) if last_ts else None,
            "unread": _unread(store, chat, since) if last_ts > since else 0,
            "pinned": pinned, "muted": bool(muted), "hidden": bool(hidden),
            "avatar": bool(ppl.avatar(chat["person_id"])) if chat["type"] == "person" else False,
        })
    return out


def chat(store, chat_id):
    index, _ = _chat_index(store)
    c = index.get(chat_id)
    if not c:
        return None
    states = _states(store)
    pinned, muted, hidden, read, origin = states.get(chat_id, (0, 0, 0, None, {}))
    out = {"id": c["id"], "type": c["type"], "title": chat_title(store, c), "services": sorted(c["services"]),
           "person_id": c.get("person_id"), "conversation_id": c.get("conversation_id"),
           "conversations": c["conversations"], "last_ts": c["last_ts"], "pinned": bool(pinned),
           "muted": bool(muted), "hidden": bool(hidden), "read_until": read, "state_from": origin,
           "state_reports": _state_reports(store, chat_id),
           "state_user": (store.cached("states", lambda: _state_parts(store)).get(chat_id) or {"user": {}})["user"]}
    if c["type"] == "person":
        out["person"] = person(store, c["person_id"])
    else:
        db = store.read()
        ppl = people(store)
        out["members"] = [{"person_id": ppl.person_of.get(a), "name": ppl.name_of_address(a)}
                          for (a,) in db.execute("SELECT address_id FROM conversation_member WHERE conversation_id = ?",
                                                 (c["conversation_id"],))]
    return out


def chat_of_conversation(store, conversation_id):
    return _chat_index(store)[1].get(conversation_id)


# --- streams -------------------------------------------------------------------------------------

def _cursor(item):
    return f"{item['ts']}:{'m' if item['type'] == 'message' else 'c'}:{item['id']}"


def _parse_cursor(c):
    ts, t, i = c.split(":")
    return int(ts), (1 if t == "m" else 0), int(i)


def _stream_sources(store, chat_id):
    index, _ = _chat_index(store)
    c = index.get(chat_id)
    if not c:
        raise KeyError(chat_id)
    addrs = people(store).addresses(c["person_id"]) if c["type"] == "person" else []
    return c, c["conversations"], addrs


def _fetch(store, convs, addrs, where, args, order, limit):
    """Raw message and call rows of a stream, within `where` on (ts, id)."""
    db = store.read()
    rows = []
    if convs:
        q = ",".join("?" * len(convs))
        rows += [("m", r) for r in db.execute(
            f"SELECT id, ts FROM message WHERE conversation_id IN ({q}) AND {where} ORDER BY ts {order}, id {order} LIMIT ?",
            (*convs, *args, limit))]
    if addrs:
        q = ",".join("?" * len(addrs))
        rows += [("c", r) for r in db.execute(
            f"SELECT id, ts FROM call WHERE address_id IN ({q}) AND conversation_id IS NULL AND {where} "
            f"ORDER BY ts {order}, id {order} LIMIT ?", (*addrs, *args, limit))]
    return rows


def stream(store, chat_id, before=None, after=None, around=None, limit=PAGE):
    """A page of a chat's stream, oldest first: {items, has_older, has_newer}."""
    c, convs, addrs = _stream_sources(store, chat_id)
    key = lambda r: (r[1][1], 0 if r[0] == "c" else 1, r[1][0])
    if around is not None:
        older = stream(store, chat_id, before=f"{around}:m:0", limit=limit // 2)
        newer = stream(store, chat_id, after=f"{around - 1}:m:{2 ** 62}", limit=limit - limit // 2)
        return {"items": older["items"] + newer["items"], "has_older": older["has_older"],
                "has_newer": newer["has_newer"]}
    if after:
        ts, t, i = _parse_cursor(after)
        rows = _fetch(store, convs, addrs, "(ts > ? OR (ts = ? AND id > ?))", (ts, ts, i if t == 1 else -1), "ASC", limit + 1)
        rows.sort(key=key)
        rows = [r for r in rows if key(r) > (ts, t, i)]
        page, more = rows[:limit], len(rows) > limit
        items = hydrate(store, page, chat=c)
        return {"items": items, "has_older": True, "has_newer": more}
    if before:
        ts, t, i = _parse_cursor(before)
        rows = _fetch(store, convs, addrs, "(ts < ? OR (ts = ? AND id < ?))", (ts, ts, i if t == 1 else 2 ** 62), "DESC", limit + 1)
    else:
        rows = _fetch(store, convs, addrs, "1", (), "DESC", limit + 1)
        ts = t = i = None
    rows.sort(key=key, reverse=True)
    if before:
        rows = [r for r in rows if key(r) < (ts, t, i)]
    page, more = rows[:limit], len(rows) > limit
    page.reverse()
    return {"items": hydrate(store, page, chat=c), "has_older": more, "has_newer": bool(before)}


def hydrate(store, rows, chat=None):
    """Full items for (type, (id, ts)) rows, in the order given."""
    db = store.read()
    lk = _lookups(store)
    ppl = people(store)
    mids = [r[1][0] for r in rows if r[0] == "m"]
    cids = [r[1][0] for r in rows if r[0] == "c"]
    msgs, calls = {}, {}
    if mids:
        q = ",".join("?" * len(mids))
        for r in db.execute(
                f"SELECT id, ts, service_id, conversation_id, outgoing, sender_id, kind_id, text, subtype, reply_to, "
                f"reply_text, edited, deleted, forwarded, starred, lat, lon, place, status, key IS NOT NULL "
                f"FROM message WHERE id IN ({q})", mids):
            (mid, ts, sid, conv, outgoing, sender, kind_id, txt, subtype, reply_to, reply_text, edited, deleted,
             forwarded, starred, lat, lon, place, status, keyed) = r
            msgs[mid] = {
                "type": "message", "id": mid, "ts": ts, "service": lk["service"][sid], "conversation_id": conv,
                "outgoing": bool(outgoing), "kind": lk["kind"][kind_id], "subtype": subtype, "text": txt,
                "sender_id": ppl.person_of.get(sender) if sender else None,
                "sender": ppl.name_of_address(sender) if sender else None,
                "sender_self_named": bool(sender) and ppl.self_named(ppl.person_of.get(sender)),
                "reply_to": reply_to, "reply_text": reply_text, "edited": bool(edited), "deleted": bool(deleted),
                "forwarded": bool(forwarded), "starred": bool(starred), "status": status,
                "keyed": bool(keyed),       # the service's own id is known: an answer to it can be sent
                "location": {"lat": lat, "lon": lon, "place": place} if lat is not None or place else None,
                "reactions": [], "attachments": [],
            }
        replies = {m["reply_to"] for m in msgs.values() if m["reply_to"]}
        if replies:
            q2 = ",".join("?" * len(replies))
            quoted = {mid: (txt, outgoing, sender, kind_id) for mid, txt, outgoing, sender, kind_id in db.execute(
                f"SELECT id, text, outgoing, sender_id, kind_id FROM message WHERE id IN ({q2})", list(replies))}
            for m in msgs.values():
                if m["reply_to"] in quoted:
                    txt, outgoing, sender, kind_id = quoted[m["reply_to"]]
                    m["reply"] = {"id": m["reply_to"], "text": (txt or "")[:200], "outgoing": bool(outgoing),
                                  "sender": None if outgoing else ppl.name_of_address(sender), "kind": lk["kind"][kind_id]}
        for mid, emoji, code, count, who, outgoing in db.execute(
                f"SELECT message_id, emoji, code, count, address_id, outgoing FROM reaction WHERE message_id IN ({q})", mids):
            msgs[mid]["reactions"].append({"emoji": emoji, "code": code, "count": count, "mine": bool(outgoing),
                                           "who": None if outgoing else ppl.name_of_address(who)})
        for mid, sha, mime, size, path, linked in db.execute(
                f"SELECT a.message_id, m.sha256, m.mime, m.size, m.path, "
                f"(SELECT count(*) FROM library_link l WHERE l.sha256 = m.sha256) "
                f"FROM attachment a JOIN media m ON m.sha256 = a.sha256 WHERE a.message_id IN ({q})", mids):
            att = msgs[mid]["attachments"]
            if any(x["sha256"] == sha for x in att):
                continue
            local = os.path.exists(os.path.join(archive.MEDIA_ROOT, path))
            att.append({"sha256": sha, "mime": mime, "size": size,
                        "available": "local" if local else "library" if linked else "gone"})
    if cids:
        q = ",".join("?" * len(cids))
        for cid, ts, sid, aid, outgoing, answered, duration, detail, video, attempts in db.execute(
                f"SELECT id, ts, service_id, address_id, outgoing, answered, duration, detail, video, attempts "
                f"FROM call WHERE id IN ({q})", cids):
            calls[cid] = {"type": "call", "id": cid, "ts": ts, "service": lk["service"][sid], "outgoing": bool(outgoing),
                          "answered": bool(answered), "duration": duration, "detail": detail, "video": bool(video),
                          "attempts": attempts, "with": ppl.name_of_address(aid)}
    out = []
    for t, (i, _) in rows:
        item = msgs.get(i) if t == "m" else calls.get(i)
        if item:
            item["cursor"] = _cursor(item)
            out.append(item)
    return out


def message(store, message_id):
    items = hydrate(store, [("m", (message_id, 0))])
    if not items:
        return None
    m = items[0]
    m["chat_id"] = chat_of_conversation(store, m["conversation_id"])
    return m


def context(store, message_id, n=10):
    """A message with the n before and after it in its chat."""
    m = message(store, message_id)
    if not m:
        return None
    page = stream(store, m["chat_id"], around=m["ts"] + 1, limit=2 * n + 1)
    return {"chat_id": m["chat_id"], "message": m, "items": page["items"]}


# --- search --------------------------------------------------------------------------------------

def _highlight(txt, matcher, width=180):
    """The text around the first match, as [[piece, is_match], ...]."""
    if not txt:
        return []
    spans = matcher.spans(txt)
    if not spans:
        return [[txt[:width], False]]
    start = max(0, spans[0][0] - width // 3)
    end = min(len(txt), start + width)
    out, pos = [], start
    if start > 0:
        out.append(["…", False])
    for a, b in spans:
        if a < max(start, pos) or b > end:      # outside, or inside one already marked
            continue
        if a > pos:
            out.append([txt[pos:a], False])
        out.append([txt[a:b], True])
        pos = b
    if pos < end:
        out.append([txt[pos:end], False])
    if end < len(txt):
        out.append(["…", False])
    return out


def search(store, q, chat_id=None, service=None, kind=None, since=None, until=None, outgoing=None,
           limit=50, offset=0, case=False, whole=False):
    """Messages whose text has every word of q, newest first, with the chat they are in and the
    matches marked. case: as typed (case and accents); else both ignored. whole: whole words only
    (a word ending in * a prefix); else anywhere, inside words too (a word of one or two letters:
    at the start of words, which is what an index of trigrams cannot do)."""
    m = text_mod.Matcher(q, case=case, whole=whole)
    if not m.words:
        return {"items": [], "total": 0}
    where, args = [], []
    for w in m.words:
        folded = text_mod.fold(w.rstrip("*"))
        if not folded:
            continue
        if whole or len(folded) < 3:
            where.append("m.id IN (SELECT rowid FROM message_fts WHERE message_fts MATCH ?)")
            args.append(text_mod.query(w) if whole else '"%s"*' % folded.replace('"', '""'))
        else:
            where.append("m.id IN (SELECT rowid FROM message_tri WHERE message_tri MATCH ?)")
            args.append('"%s"' % folded.replace('"', '""'))
    if not where:
        return {"items": [], "total": 0}
    db = store.read()
    lk = _lookups(store)
    if chat_id:
        _, convs, _ = _stream_sources(store, chat_id)
        where.append(f"m.conversation_id IN ({','.join('?' * len(convs))})")
        args += convs
    if service:
        sid = {v: k for k, v in lk["service"].items()}.get(service)
        where.append("m.service_id = ?")
        args.append(sid)
    if kind:
        kid = {v: k for k, v in lk["kind"].items()}.get(kind)
        where.append("m.kind_id = ?")
        args.append(kid)
    if since is not None:
        where.append("m.ts >= ?")
        args.append(since)
    if until is not None:
        where.append("m.ts < ?")
        args.append(until)
    if outgoing is not None:
        where.append("m.outgoing = ?")
        args.append(int(outgoing))
    w = " AND ".join(where)
    if case:        # the index is folded: of what it finds, those written as typed
        found = [(i, ts) for i, ts, txt in db.execute(f"SELECT m.id, m.ts, m.text FROM message m WHERE {w} ORDER BY m.ts DESC", args)
                 if m.matches(txt)]
        total, rows = len(found), found[offset:offset + limit]
    else:
        total = db.execute(f"SELECT count(*) FROM message m WHERE {w}", args).fetchone()[0]
        rows = db.execute(f"SELECT m.id, m.ts FROM message m WHERE {w} ORDER BY m.ts DESC LIMIT ? OFFSET ?",
                          (*args, limit, offset)).fetchall()
    items = hydrate(store, [("m", r) for r in rows])
    index, conv_chat = _chat_index(store)
    for it in items:
        cid = conv_chat.get(it["conversation_id"])
        it["chat_id"] = cid
        it["chat_title"] = chat_title(store, index[cid]) if cid in index else None
        it["highlight"] = _highlight(it["text"], m)
    return {"items": items, "total": total}


# --- people --------------------------------------------------------------------------------------

def person(store, person_id):
    ppl = people(store)
    db = store.read()
    if person_id not in ppl.handles and not db.execute("SELECT 1 FROM person WHERE id = ?", (person_id,)).fetchone():
        return None
    addrs = ppl.addresses(person_id)
    stats = {"messages": 0, "calls": 0, "first": None, "last": None, "by_service": {}}
    index, _ = _chat_index(store)
    c = index.get(f"p{person_id}")
    lk = _lookups(store)
    if c and c["conversations"]:
        q = ",".join("?" * len(c["conversations"]))
        for sid, n, first, last in db.execute(
                f"SELECT service_id, count(*), min(ts), max(ts) FROM message WHERE conversation_id IN ({q}) GROUP BY service_id",
                c["conversations"]):
            stats["by_service"][lk["service"][sid]] = n
            stats["messages"] += n
            stats["first"] = min(x for x in (stats["first"], first) if x is not None)
            stats["last"] = max(x for x in (stats["last"], last) if x is not None)
    if addrs:
        q = ",".join("?" * len(addrs))
        n, first, last = db.execute(f"SELECT count(*), min(ts), max(ts) FROM call WHERE address_id IN ({q})", addrs).fetchone()
        stats["calls"] = n
        if first is not None:
            stats["first"] = min(x for x in (stats["first"], first) if x is not None)
            stats["last"] = max(x for x in (stats["last"], last) if x is not None)
    groups = []
    if addrs:
        q = ",".join("?" * len(addrs))
        for cid, title in db.execute(
                f"SELECT DISTINCT c.id, c.title FROM conversation_member cm JOIN conversation c ON c.id = cm.conversation_id "
                f"WHERE c.is_group AND cm.address_id IN ({q})", addrs):
            groups.append({"chat_id": f"c{cid}", "title": title})
    contact = ppl.contact(person_id)
    contacts = {c[0]: c[1] for c in ppl.contacts.get(person_id, ())}
    return {"id": person_id, "name": ppl.name(person_id), "given_name": ppl.given.get(person_id),
            "name_from": ppl.info(person_id)[1], "name_source": ppl.pinned.get(person_id),
            "self_named": ppl.self_named(person_id), "aka": ppl.aka(person_id),
            "note": ppl.notes.get(person_id), "handles": ppl.describe(person_id), "me": person_id in ppl.me,
            "contact": {"id": contact[0], "name": contact[1], "organization": contact[3]} if contact else None,
            "contacts": [{"id": i, "name": n} for i, n in contacts.items()] if len(contacts) > 1 else [],
            "avatar": bool(ppl.avatar(person_id)), "stats": stats, "groups": groups}


def people_list(store, q=None, limit=100, offset=0):
    ppl = people(store)
    qf = text_mod.fold(q) if q else None
    out = []
    for pid in ppl.handles:
        if pid in ppl.me:
            continue
        name = ppl.name(pid)
        if qf and qf not in text_mod.fold(name) and not any(qf in text_mod.fold(h[1]) for h in ppl.handles[pid]):
            continue
        out.append({"id": pid, "name": name, "handles": len(ppl.handles[pid])})
    out.sort(key=lambda p: text_mod.fold(p["name"]))
    return {"items": out[offset:offset + limit], "total": len(out)}


def merge_suggestions(store, limit=50):
    """People who are likely one, with why, the strongest first; shown, never applied:
    - contact: one address-book contact lists handles of each (the user's own word);
    - book: a service's copy of the user's address book gives them the same name;
    - name: the same name they chose or a chat shows, only when it is rare here (no more than 3
      people) and has at least two words.
    Groups the user turned down (every pair of them) are left out."""
    ppl = people(store)
    db = store.read()
    dismissed = {(a, b) for a, b in db.execute("SELECT a, b FROM merge_dismissed")}
    groups = {}                     # frozenset of people -> {"why": [...], "name": ...}

    def add(pids, why, name):
        pids = frozenset(p for p in pids if p not in ppl.me)
        if len(pids) < 2:
            return
        pairs = {(a, b) for a in pids for b in pids if a < b}
        if pairs <= dismissed:
            return
        g = groups.setdefault(pids, {"why": [], "name": name})
        if why not in g["why"]:
            g["why"].append(why)

    by_contact = defaultdict(set)
    for pid, cs in ppl.contacts.items():
        for cid, name, *_ in cs:
            by_contact[(cid, name)].add(pid)
    for (_, name), pids in by_contact.items():
        add(pids, "contact", name)
    by_name = {"book": defaultdict(set), "name": defaultdict(set)}
    spelled = {}
    for pid, rows in ppl.seen.items():
        for source, name, _, _, _, cur in rows:
            if cur:
                fold = text_mod.fold(name)
                by_name["book" if source.endswith("/book") else "name"][fold].add(pid)
                spelled.setdefault(fold, name)
    for fold, pids in by_name["book"].items():
        add(pids, "book", spelled[fold])
    for fold, pids in by_name["name"].items():
        if len(pids) <= 3 and len(fold.split()) >= 2:
            add(pids, "name", spelled[fold])
    strength = {"contact": 0, "book": 1, "name": 2}
    ranked = sorted(groups.items(), key=lambda g: (min(strength[w] for w in g[1]["why"]), -len(g[1]["why"])))
    return [{"name": g["name"], "why": g["why"], "people": [person(store, p) for p in sorted(pids)[:5]]}
            for pids, g in ranked[:limit]]


# --- calls, media, timeline, statistics ----------------------------------------------------------

def calls(store, chat_id=None, missed=None, service=None, before=None, limit=PAGE):
    db = store.read()
    where, args = ["1"], []
    if chat_id:
        _, _, addrs = _stream_sources(store, chat_id)
        where.append(f"address_id IN ({','.join('?' * len(addrs))})")
        args += addrs
    if missed:
        where.append("outgoing = 0 AND answered = 0")
    if service:
        where.append("service_id = (SELECT id FROM service WHERE name = ?)")
        args.append(service)
    if before:
        where.append("ts < ?")
        args.append(int(before))
    rows = db.execute(f"SELECT id, ts FROM call WHERE {' AND '.join(where)} ORDER BY ts DESC LIMIT ?",
                      (*args, limit + 1)).fetchall()
    items = hydrate(store, [("c", r) for r in rows[:limit]])
    ppl = people(store)
    for it, (cid, _) in zip(items, rows):
        aid = db.execute("SELECT address_id FROM call WHERE id = ?", (cid,)).fetchone()[0]
        pid = ppl.person_of.get(aid)
        it["chat_id"] = f"p{pid}" if pid is not None else None
    return {"items": items, "has_more": len(rows) > limit}


MEDIA_KINDS = {"image": ("image",), "video": ("video",), "voice": ("voice",), "file": ("file",),
               "all": ("image", "video", "voice", "file", "sticker")}


def media(store, chat_id=None, kind="all", before=None, limit=PAGE, available_only=False):
    """Files of messages, newest first: [{sha256, mime, size, available, message_id, ts, chat_id, decision}]."""
    db = store.read()
    lk = _lookups(store)
    kinds = [k for k, v in lk["kind"].items() if v in MEDIA_KINDS.get(kind, MEDIA_KINDS["all"])]
    where = [f"m.kind_id IN ({','.join('?' * len(kinds))})"]
    args = list(kinds)
    if chat_id:
        _, convs, _ = _stream_sources(store, chat_id)
        where.append(f"m.conversation_id IN ({','.join('?' * len(convs))})")
        args += convs
    if before:
        where.append("m.ts < ?")
        args.append(int(before))
    rows = db.execute(
        f"SELECT m.id, m.ts, m.conversation_id, md.sha256, md.mime, md.size, md.path, "
        f"(SELECT count(*) FROM library_link l WHERE l.sha256 = md.sha256), d.decision "
        f"FROM message m JOIN attachment a ON a.message_id = m.id JOIN media md ON md.sha256 = a.sha256 "
        f"LEFT JOIN media_decision d ON d.sha256 = md.sha256 "
        f"WHERE {' AND '.join(where)} ORDER BY m.ts DESC LIMIT ?", (*args, (limit + 1) * 3)).fetchall()
    _, conv_chat = _chat_index(store)
    out, seen = [], set()
    for mid, ts, conv, sha, mime, size, path, linked, decision in rows:
        if sha in seen:
            continue
        seen.add(sha)
        local = os.path.exists(os.path.join(archive.MEDIA_ROOT, path))
        available = "local" if local else "library" if linked else "gone"
        if available_only and available == "gone":
            continue
        out.append({"sha256": sha, "mime": mime, "size": size, "available": available, "message_id": mid,
                    "ts": ts, "chat_id": conv_chat.get(conv), "decision": decision})
        if len(out) > limit:
            break
    return {"items": out[:limit], "has_more": len(out) > limit}


def timeline(store, day_start, day_end):
    """Everything between two instants (Unix ms), across chats, oldest first."""
    db = store.read()
    rows = [("m", r) for r in db.execute("SELECT id, ts FROM message WHERE ts >= ? AND ts < ? ORDER BY ts, id",
                                         (day_start, day_end))]
    rows += [("c", r) for r in db.execute("SELECT id, ts FROM call WHERE ts >= ? AND ts < ? ORDER BY ts, id",
                                          (day_start, day_end))]
    rows.sort(key=lambda r: (r[1][1], r[0], r[1][0]))
    items = hydrate(store, rows[:2000])
    _, conv_chat = _chat_index(store)
    for it in items:
        if it["type"] == "message":
            it["chat_id"] = conv_chat.get(it["conversation_id"])
    return {"items": items, "truncated": len(rows) > 2000}


def stats(store):
    def build():
        db = store.read()
        lk = _lookups(store)
        by_service = {lk["service"][s]: n for s, n in db.execute("SELECT service_id, count(*) FROM message GROUP BY 1")}
        calls_by_service = {lk["service"][s]: n for s, n in db.execute("SELECT service_id, count(*) FROM call GROUP BY 1")}
        by_year = {y: n for y, n in db.execute(
            "SELECT strftime('%Y', ts / 1000, 'unixepoch') y, count(*) FROM message GROUP BY y ORDER BY y")}
        first, last = db.execute("SELECT min(ts), max(ts) FROM message").fetchone()
        index, _ = _chat_index(store)
        top = []
        for c in index.values():
            if c["type"] != "person" or not c["conversations"]:
                continue
            q = ",".join("?" * len(c["conversations"]))
            n = db.execute(f"SELECT count(*) FROM message WHERE conversation_id IN ({q})", c["conversations"]).fetchone()[0]
            top.append((n, c))
        top.sort(key=lambda x: x[0], reverse=True)
        return {
            "messages": sum(by_service.values()), "calls": sum(calls_by_service.values()),
            "people": sum(1 for c in index.values() if c["type"] == "person"),
            "groups": sum(1 for c in index.values() if c["type"] == "group"),
            "by_service": by_service, "calls_by_service": calls_by_service, "by_year": by_year,
            "first": first, "last": last,
            "top_people": [{"chat_id": c["id"], "title": chat_title(store, c), "messages": n} for n, c in top[:20]],
        }
    return store.cached("stats", build)
