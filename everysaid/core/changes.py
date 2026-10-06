"""Changes the user makes in the app. Each is one transaction; nothing of the history is deleted
here: merging people moves their addresses to one person, splitting moves one address to a new
person, merging groups links their conversations into one chat, and messages, calls and media
stay as they are.
"""
import json
import time

from .queries import _chat_index
from ..errors import UserError

_UNSET = object()


def set_person(store, person_id, name=_UNSET, note=_UNSET, name_source=_UNSET):
    """name: the user's own name for them; name_source: where their name comes from (a source of
    names, 'address:<id>' for one of their handles, or None: by the order)."""
    with store.write() as db:
        if not db.execute("SELECT 1 FROM person WHERE id = ?", (person_id,)).fetchone():
            raise KeyError(person_id)
        if name_source is not _UNSET:
            if name_source and name_source.startswith("address:"):
                aid = int(name_source.split(":", 1)[1])
                if not db.execute("SELECT 1 FROM person_address WHERE address_id = ? AND person_id = ?",
                                  (aid, person_id)).fetchone():
                    raise UserError("people.not_their_handle")
            db.execute("UPDATE person SET name_source = ? WHERE id = ?", (name_source or None, person_id))
        if name is not _UNSET:
            db.execute("UPDATE person SET name = ? WHERE id = ?", ((name or "").strip() or None, person_id))
        if note is not _UNSET:
            db.execute("UPDATE person SET note = ? WHERE id = ?", ((note or "").strip() or None, person_id))


def merge_people(store, into, other):
    """`other` becomes part of `into`: its addresses move over (marked as merged by the user), its
    name and note are kept where `into` has none, and its own row goes."""
    if into == other:
        raise UserError("people.same")
    with store.write() as db:
        rows = {pid: r for pid, *r in db.execute(
            "SELECT id, name, note, contact_uid, contact_url, name_source FROM person WHERE id IN (?, ?)", (into, other))}
        if into not in rows or other not in rows:
            raise KeyError(other if into in rows else into)
        db.execute("UPDATE person_address SET person_id = ?, how = 'manual' WHERE person_id = ?", (into, other))
        name, note, uid, url, source = rows[into]
        oname, onote, ouid, ourl, osource = rows[other]
        db.execute("UPDATE person SET name = ?, note = ?, contact_uid = ?, contact_url = ?, name_source = ? WHERE id = ?",
                   (name or oname, "\n\n".join(n for n in (note, onote) if n) or None, uid or ouid, url or ourl,
                    source or osource, into))
        # archived only if both were: one of them in view keeps the whole person in view
        both_archived = len({c for (c,) in db.execute(
            "SELECT chat FROM chat_state WHERE field = 'archived' AND value = 1 AND chat IN (?, ?)", (f"p{into}", f"p{other}"))}) == 2
        # the user's choices for the other's chat: kept where theirs for `into` are older or missing
        db.execute("INSERT INTO chat_state SELECT ?, field, value, set_at, always FROM chat_state WHERE chat = ? "
                   "ON CONFLICT (chat, field) DO UPDATE SET value = excluded.value, set_at = excluded.set_at, "
                   "always = excluded.always WHERE excluded.set_at > chat_state.set_at", (f"p{into}", f"p{other}"))
        if not both_archived:
            db.execute("UPDATE chat_state SET value = 0 WHERE chat = ? AND field = 'archived'", (f"p{into}",))
        db.execute("DELETE FROM chat_state WHERE chat = ?", (f"p{other}",))
        db.execute("DELETE FROM merge_dismissed WHERE a = ? OR b = ?", (other, other))
        db.execute("DELETE FROM person WHERE id = ?", (other,))
    return into


def split_address(store, address_id):
    """One address leaves its person for a new person of its own (undoing a wrong merge)."""
    with store.write() as db:
        row = db.execute("SELECT person_id FROM person_address WHERE address_id = ?", (address_id,)).fetchone()
        if not row:
            raise KeyError(address_id)
        if db.execute("SELECT count(*) FROM person_address WHERE person_id = ?", (row[0],)).fetchone()[0] < 2:
            return row[0]
        pid = db.execute("INSERT INTO person DEFAULT VALUES").lastrowid
        db.execute("UPDATE person_address SET person_id = ?, how = 'manual' WHERE address_id = ?", (pid, address_id))
        # its chat is new, but not newly seen: archived as the chat it left
        db.execute("INSERT INTO chat_state SELECT ?, field, value, set_at, always FROM chat_state "
                   "WHERE chat = ? AND field = 'archived'", (f"p{pid}", f"p{row[0]}"))
        return pid


def set_chat_state(store, chat_id, always=False, **fields):
    """The user's choice for a chat: pinned, muted, archived (bools), read_until (Unix ms, or 'now'),
    or None to follow the services again. A service's later change wins over it, unless `always`."""
    allowed = {"pinned", "muted", "archived", "read_until"}
    if set(fields) - allowed:
        raise ValueError(set(fields) - allowed)
    index, _ = _chat_index(store)
    if chat_id not in index:
        raise KeyError(chat_id)
    now = int(time.time() * 1000)
    with store.write() as db:
        for f, v in fields.items():
            if v is None:
                db.execute("DELETE FROM chat_state WHERE chat = ? AND field = ?", (chat_id, f))
                continue
            v = now if v == "now" else int(v) if f == "read_until" else int(bool(v))
            db.execute("INSERT OR REPLACE INTO chat_state VALUES (?, ?, ?, ?, ?)", (chat_id, f, v, now, int(bool(always))))


def _group(index, chat_id):
    c = index.get(chat_id)
    if not c:
        raise KeyError(chat_id)
    if c["type"] != "group":
        raise UserError("chat.not_group")
    return c


def merge_groups(store, into, other):
    """The group chat `other` becomes part of `into` (chat ids): their conversations are shown as one
    chat, `into`'s id; the user's choices for the other are kept where `into` has none or older ones,
    and it is archived only if both were."""
    index, _ = _chat_index(store)
    a, b = _group(index, into), _group(index, other)
    if a is b:
        raise UserError("chat.same_group")
    head, gone = a["conversation_id"], b["conversation_id"]
    with store.write() as db:
        db.executemany("INSERT OR REPLACE INTO group_link VALUES (?, ?)", [(c, head) for c in b["conversations"]])
        both_archived = len({c for (c,) in db.execute(
            "SELECT chat FROM chat_state WHERE field = 'archived' AND value = 1 AND chat IN (?, ?)", (into, other))}) == 2
        db.execute("INSERT INTO chat_state SELECT ?, field, value, set_at, always FROM chat_state WHERE chat = ? "
                   "ON CONFLICT (chat, field) DO UPDATE SET value = excluded.value, set_at = excluded.set_at, "
                   "always = excluded.always WHERE excluded.set_at > chat_state.set_at", (into, other))
        if not both_archived:
            db.execute("UPDATE chat_state SET value = 0 WHERE chat = ? AND field = 'archived'", (into,))
        db.execute("DELETE FROM chat_state WHERE chat = ?", (other,))
        db.execute("DELETE FROM group_dismissed WHERE a = ? OR b = ?", (gone, gone))
    return into


def split_group(store, chat_id, conversation_id):
    """One group leaves a merged group chat, a chat of its own again, archived as the chat it left. When
    it is the one whose id the chat has, the others keep the chat (and the user's choices) under the
    id of the latest of them."""
    index, conv_chat = _chat_index(store)
    c = _group(index, chat_id)
    conversation_id = int(conversation_id)
    if conv_chat.get(conversation_id) != chat_id:
        raise UserError("chat.not_in_chat")
    if len(c["conversations"]) < 2:
        return chat_id
    with store.write() as db:
        head = c["conversation_id"]
        if conversation_id == head:     # the rest move to a new head, the latest of them
            rest = [x for x in c["conversations"] if x != head]
            new = db.execute(f"SELECT c.id FROM conversation c LEFT JOIN message m ON m.conversation_id = c.id "
                             f"WHERE c.id IN ({','.join('?' * len(rest))}) GROUP BY c.id "
                             f"ORDER BY max(m.ts) DESC, c.id LIMIT 1", rest).fetchone()[0]
            db.execute("DELETE FROM group_link WHERE conversation_id IN (?, ?)", (new, head))
            db.execute("UPDATE group_link SET into_id = ? WHERE into_id = ?", (new, head))
            db.execute("INSERT OR REPLACE INTO chat_state SELECT ?, field, value, set_at, always FROM chat_state "
                       "WHERE chat = ?", (f"c{new}", chat_id))
            db.execute("DELETE FROM chat_state WHERE chat = ? AND field != 'archived'", (chat_id,))
            return f"c{new}"
        db.execute("DELETE FROM group_link WHERE conversation_id = ?", (conversation_id,))
        # its chat is new, but not newly seen: archived as the chat it left
        db.execute("INSERT OR REPLACE INTO chat_state SELECT ?, field, value, set_at, always FROM chat_state "
                   "WHERE chat = ? AND field = 'archived'", (f"c{conversation_id}", chat_id))
    return chat_id


def dismiss_group_merge(store, chat_ids):
    """The user says these groups are not one: the suggestion is not shown again."""
    index, _ = _chat_index(store)
    ids = sorted({_group(index, c)["conversation_id"] for c in chat_ids})
    with store.write() as db:
        db.executemany("INSERT OR REPLACE INTO group_dismissed VALUES (?, ?, ?)",
                       [(a, b, int(time.time())) for i, a in enumerate(ids) for b in ids[i + 1:]])


def dismiss_merge(store, person_ids):
    """The user says these people are not one: the suggestion is not shown again."""
    ids = sorted(set(int(p) for p in person_ids))
    with store.write() as db:
        db.executemany("INSERT OR REPLACE INTO merge_dismissed VALUES (?, ?, ?)",
                       [(a, b, int(time.time())) for i, a in enumerate(ids) for b in ids[i + 1:]])


def set_setting(store, key, value):
    with store.write() as db:
        db.execute("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
                   (key, json.dumps(value)))


def settings(store):
    return {k: json.loads(v) for k, v in store.read().execute("SELECT key, value FROM setting")}


def decide_media(store, sha256, decision, date_ms=None):
    """keep, remove or library (send to the default library); the newest decision about a file is the
    one that counts, and is what a library step or a removal later reads."""
    if decision not in ("keep", "remove", "library", None):
        raise ValueError(decision)
    with store.write() as db:
        if not db.execute("SELECT 1 FROM media WHERE sha256 = ?", (sha256,)).fetchone():
            raise KeyError(sha256)
        if decision is None:
            db.execute("DELETE FROM media_decision WHERE sha256 = ?", (sha256,))
            return
        db.execute("INSERT INTO media_decision (sha256, decision, date_ms, at) VALUES (?, ?, ?, ?) "
                   "ON CONFLICT (sha256) DO UPDATE SET decision = excluded.decision, "
                   "date_ms = coalesce(excluded.date_ms, media_decision.date_ms), at = excluded.at",
                   (sha256, decision, date_ms, int(time.time())))


def set_device_period(store, device_id, used_from=_UNSET, used_until=_UNSET):
    """When a phone was the one in use (Unix ms; None: not known): the copy of a record found on two
    devices that is kept is the one of the device in use at its time."""
    with store.write() as db:
        if not db.execute("SELECT 1 FROM device WHERE id = ?", (device_id,)).fetchone():
            raise KeyError(device_id)
        if used_from is not _UNSET:
            db.execute("UPDATE device SET used_from = ? WHERE id = ?", (used_from, device_id))
        if used_until is not _UNSET:
            db.execute("UPDATE device SET used_until = ? WHERE id = ?", (used_until, device_id))


def devices(store):
    return [{"id": i, "name": n, "kind": k, "used_from": f, "used_until": u}
            for i, n, k, f, u in store.read().execute("SELECT id, name, kind, used_from, used_until FROM device ORDER BY id")]
