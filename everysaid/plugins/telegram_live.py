"""Telegram live: the user's account connected through Telethon while the app runs.

On connecting it brings what arrived while it was not connected, and each chat's state (archived,
pinned, muted; then live, as it changes on any of the user's devices); then new messages (incoming, and
outgoing from any of the user's devices) and edits, pushed by Telegram as they happen, are written
to `telegram.db` as the sync script writes them, then imported into the archive at once. Messages
sent from the app go through the same client and the same path. Channels and bots stay out.
"""
import asyncio
import json
import time

from .. import config, telegram, telegram_store
from ..errors import plugin_error

CLIENTS = {}        # instance id -> TelegramClient


async def _client(ctx):
    from telethon import TelegramClient
    from telethon.sessions import StringSession
    c = CLIENTS.get(ctx.id)
    if c and c.is_connected():
        return c
    api_id, api_hash = config.secret("telegram-api-id"), config.secret("telegram-api-hash")
    session = config.secret("telegram-session")
    if not (api_id and api_hash and session):
        raise plugin_error("Not signed in to Telegram yet (scripts/telegram-sync.py --save-credentials, --login)")
    c = TelegramClient(StringSession(session), int(api_id), api_hash, flood_sleep_threshold=60)
    await c.connect()
    if not await c.is_user_authorized():
        raise plugin_error("The Telegram sign-in has expired: scripts/telegram-sync.py --login")
    CLIENTS[ctx.id] = c
    return c


def _store(ctx, chat_entity, messages):
    """Write messages (Telethon objects) of one chat to telegram.db and import them; returns how many."""
    from telethon import utils
    kind = telegram_store.entity_kind(chat_entity)
    if kind in ("channel", "bot"):
        return 0
    chat_id = utils.get_peer_id(chat_entity)
    db = telegram_store.open_store()
    try:
        db.execute("INSERT INTO chat (id, kind, title, archived, json) VALUES (?, ?, ?, 0, ?) "
                   "ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, title = excluded.title, json = excluded.json",
                   (chat_id, kind, utils.get_display_name(chat_entity), telegram_store.dump(chat_entity)))
        keys = set()
        for m in messages:
            db.execute("INSERT INTO message (chat_id, id, date, json) VALUES (?, ?, ?, ?) "
                       "ON CONFLICT (chat_id, id) DO UPDATE SET json = excluded.json",
                       (chat_id, m.id, int(m.date.timestamp()), telegram_store.dump(m)))
            if m.sender is not None:
                db.execute("INSERT OR REPLACE INTO entity VALUES (?, ?)",
                           (utils.get_peer_id(m.sender), telegram_store.dump(m.sender)))
            keys.add((chat_id, m.id))
        db.execute("UPDATE chat SET synced_at = ? WHERE id = ?", (int(time.time()), chat_id))
        db.commit()
    finally:
        db.close()
    settings = ctx.host.ctx(ctx.id).settings         # as they are now: the choice may have changed
    if chat_id in {int(c) for c in settings.get("skip_chats") or []}:
        return 0                        # kept in telegram.db, not imported: the user left this chat out
    from .sources import run_importers
    run_importers(ctx, [("Telegram live", lambda a: telegram.run(a, only=keys))])
    return len(keys)


def _until_ms(dt):
    """Telegram's mute_until (a datetime, or None) as Unix ms: 0 not muted, -1 for ever (past 3000)."""
    if not dt:
        return 0
    t = int(dt.timestamp() * 1000)
    return -1 if dt.year >= 3000 else max(t, 0)


def _report_states(ctx, items):
    """items: [(chat id, field, value)] as Telegram says now: into the archive's state reports."""
    from ..archive import Archive
    if not items:
        return
    now = int(time.time() * 1000)
    with ctx.host.import_lock:
        a = Archive(ctx.store.path)
        try:
            src = a.source(telegram.SOURCE, telegram.DB, "telegram", telegram.MEDIA)
            a.db.execute("UPDATE source SET instance_id = ? WHERE id = ? AND instance_id IS NULL", (ctx.id, src))
            for chat_id, field, value in items:
                a.report_state(src, a.find_conversation("telegram", str(chat_id)), field, value, now)
            a.init_archived()
            a.db.commit()
        finally:
            a.db.close()
    ctx.emit({"type": "changed"})


def _note_reads(ctx, items, seen_now=False):
    """items: [(chat id, inbox, outbox)] (None: not said) as Telegram says now: into telegram.db, then,
    for the chats where either moved on, into the archive (their read_until, and receipts of the owner's
    messages)."""
    from ..archive import Archive
    db = telegram_store.open_store()
    try:
        known = {i for (i,) in db.execute("SELECT id FROM chat")}     # channels and bots are not
        items = [x for x in items if x[0] in known and telegram_store.note_read(db, *x, seen_now)]
        db.commit()
    finally:
        db.close()
    if not items:
        return
    with ctx.host.import_lock:
        a = Archive(ctx.store.path)
        try:
            src = a.source(telegram.SOURCE, telegram.DB, "telegram", telegram.MEDIA)
            a.db.execute("UPDATE source SET instance_id = ? WHERE id = ? AND instance_id IS NULL", (ctx.id, src))
            telegram.reads(a, chats={chat_id for chat_id, _, _ in items})
            a.db.commit()
        finally:
            a.db.close()
    ctx.emit({"type": "changed"})


def _dialog_reads(dialogs):
    return [(d.id, getattr(d.dialog, "read_inbox_max_id", None), getattr(d.dialog, "read_outbox_max_id", None))
            for d in dialogs if telegram_store.entity_kind(d.entity) not in ("channel", "bot")]


def _dialog_states(dialogs):
    out = []
    for d in dialogs:
        if telegram_store.entity_kind(d.entity) in ("channel", "bot"):
            continue
        ns = getattr(d.dialog, "notify_settings", None)
        out += [(d.id, "archived", int(d.archived)), (d.id, "pinned", int(d.pinned)),
                (d.id, "muted", _until_ms(getattr(ns, "mute_until", None)))]
    return out


async def catch_up(ctx, c, dialogs):
    """What arrived while the app was not connected: each chat's messages after the newest that
    telegram.db has (a chat new since then, all of it)."""
    db = telegram_store.open_store()
    try:
        have = dict(db.execute("SELECT chat_id, max(id) FROM message GROUP BY chat_id").fetchall())
    finally:
        db.close()
    total = chats = 0
    for d in dialogs:
        if telegram_store.entity_kind(d.entity) in ("channel", "bot"):
            continue
        since = have.get(d.id, 0)
        if d.message is None or d.message.id <= since:
            continue
        batch = [m async for m in c.iter_messages(d.entity, min_id=since, reverse=True)]
        if batch:
            await asyncio.to_thread(_store, ctx, d.entity, batch)
            total += len(batch)
            chats += 1
    if total:
        ctx.log("caught up: {n} new messages in {chats} chats", n=total, chats=chats)
    else:
        ctx.log("caught up: nothing new")


async def run(ctx, plugin):
    from telethon import events
    c = await _client(ctx)
    dialogs = await c.get_dialogs()     # the access hashes of every chat (a StringSession keeps none)
    ctx.log("connected to Telegram")

    async def on_message(event):
        try:
            chat = await event.get_chat()
            msg = event.message
            if msg.sender is None:
                await msg.get_sender()
            n = await asyncio.to_thread(_store, ctx, chat, [msg])
            if n:
                ctx.log("new message in {chat}", chat=getattr(chat, "title", None) or getattr(chat, "first_name", "") or chat.id)
        except Exception as e:      # one bad update must not stop the connection
            ctx.log("error: {e}", e=repr(e))

    async def on_state(update):
        """Archived, pinned or muted on any of the user's devices."""
        from telethon import types, utils
        items = []
        try:
            if isinstance(update, types.UpdateFolderPeers):
                items = [(utils.get_peer_id(fp.peer), "archived", int(fp.folder_id == 1)) for fp in update.folder_peers]
            elif isinstance(update, types.UpdateDialogPinned) and isinstance(update.peer, types.DialogPeer):
                items = [(utils.get_peer_id(update.peer.peer), "pinned", int(bool(update.pinned)))]
            elif isinstance(update, types.UpdateNotifySettings) and isinstance(update.peer, types.NotifyPeer):
                items = [(utils.get_peer_id(update.peer.peer), "muted", _until_ms(update.notify_settings.mute_until))]
            if items:
                await asyncio.to_thread(_report_states, ctx, items)
            # read on any of the owner's devices, or by the others (seen as it happens: its time is now)
            if isinstance(update, (types.UpdateReadHistoryInbox, types.UpdateReadHistoryOutbox)):
                chat, inbox = utils.get_peer_id(update.peer), isinstance(update, types.UpdateReadHistoryInbox)
            elif isinstance(update, (types.UpdateReadChannelInbox, types.UpdateReadChannelOutbox)):
                chat = utils.get_peer_id(types.PeerChannel(update.channel_id))
                inbox = isinstance(update, types.UpdateReadChannelInbox)
            else:
                return
            await asyncio.to_thread(_note_reads, ctx, [(chat, update.max_id, None) if inbox else
                                                       (chat, None, update.max_id)], not inbox)
        except Exception as e:      # one bad update must not stop the connection
            ctx.log("error: {e}", e=repr(e))

    c.add_event_handler(on_message, events.NewMessage())
    c.add_event_handler(on_message, events.MessageEdited())
    c.add_event_handler(on_state, events.Raw())
    await asyncio.to_thread(_report_states, ctx, _dialog_states(dialogs))
    await asyncio.to_thread(_note_reads, ctx, _dialog_reads(dialogs))
    await catch_up(ctx, c, dialogs)     # after the handlers: nothing falls between the two
    try:
        await c.run_until_disconnected()
    finally:
        CLIENTS.pop(ctx.id, None)


def _telegram_user(ctx, address_id, conversation_id):
    """A person's Telegram user id, from any of their addresses (their number, their id): the address
    itself where it is one, else one that is a member of the conversation."""
    row = ctx.store.read().execute(
        "SELECT a.value FROM person_address mine JOIN person_address theirs ON theirs.person_id = mine.person_id "
        "JOIN address a ON a.id = theirs.address_id JOIN address_kind k ON k.id = a.kind_id "
        "JOIN service s ON s.id = a.service_id WHERE mine.address_id = ? AND k.name = 'id' AND s.name = 'telegram' "
        "ORDER BY a.id = mine.address_id DESC, EXISTS (SELECT 1 FROM conversation_member cm WHERE "
        "cm.conversation_id = ? AND cm.address_id = a.id) DESC, a.id LIMIT 1",
        (int(address_id), conversation_id)).fetchone()
    if not row:
        raise plugin_error("Unknown person to mention")
    return int(row[0])


async def _input_user(c, uid):
    """The user as Telegram takes them: from the session's cache, else from telegram.db (a member who
    never came up in the dialogs this session read)."""
    from telethon import types, utils
    try:
        return utils.get_input_user(await c.get_input_entity(uid))
    except ValueError:
        db = telegram_store.open_store()
        try:
            row = db.execute("SELECT json FROM entity WHERE id = ?", (uid,)).fetchone()
        finally:
            db.close()
        known = json.loads(row[0]) if row else {}
        if known.get("access_hash") is None:
            raise plugin_error("Unknown person to mention")
        return types.InputUser(uid, known["access_hash"])


async def _entities(ctx, c, text, mentions, conversation_id):
    """The text with each mention ({start, length, address_id}, in characters, e.g. "@name") written as
    Telegram has it for a user named, not by username: the name (without "@"), linked to them, at its
    place in the text as sent, counted in UTF-16 units."""
    from telethon import types
    units = lambda s: len(s.encode("utf-16-le")) // 2
    out, entities, at = "", [], 0
    for m in sorted(mentions, key=lambda m: m["start"]):
        start, end = m["start"], m["start"] + m["length"]
        name = text[start:end].removeprefix("@")
        out += text[at:start]
        user = await _input_user(c, _telegram_user(ctx, m["address_id"], conversation_id))
        entities.append(types.InputMessageEntityMentionName(units(out), units(name), user))
        out += name
        at = end
    return out + text[at:], entities


async def send(ctx, conversation, text, reply_to=None, mentions=None, file=None):
    import io
    c = await _client(ctx)
    peer = await c.get_input_entity(int(conversation["key"]))
    entities = None
    if mentions:
        text, entities = await _entities(ctx, c, text, mentions, conversation["id"])
    reply = int(reply_to["key"]) if reply_to else None
    if file:
        data = io.BytesIO(file["data"])
        data.name = file.get("filename") or "file"
        mime = file.get("mime_type") or ""
        msg = await c.send_file(peer, data, caption=text, reply_to=reply, formatting_entities=entities,
                                force_document=not mime.startswith(("image/", "video/")))
    else:
        msg = await c.send_message(peer, text, reply_to=reply, formatting_entities=entities)
    chat = await c.get_entity(peer)
    if msg.sender is None:
        await msg.get_sender()
    await asyncio.to_thread(_store, ctx, chat, [msg])
    return {"id": msg.id}


async def mark_read(ctx, conversation, until):
    """Read receipts up to the newest message from the others at or before `until` (Unix ms)."""
    row = ctx.store.read().execute(
        "SELECT key FROM message WHERE conversation_id = ? AND NOT outgoing AND ts <= ? AND key IS NOT NULL "
        "ORDER BY ts DESC, id DESC LIMIT 1", (conversation["id"], until)).fetchone()
    if not row:
        return 0
    c = await _client(ctx)
    chat = int(conversation["key"])
    await c.send_read_acknowledge(await c.get_input_entity(chat), max_id=int(row[0]))
    await asyncio.to_thread(_note_reads, ctx, [(chat, int(row[0]), None)])
    return 1
