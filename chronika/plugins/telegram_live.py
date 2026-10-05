"""Telegram live: the user's account connected through Telethon while the app runs.

On connecting it brings what arrived while it was not connected, and each chat's state (archived,
pinned, muted; then live, as it changes on any of the user's devices); then new messages (incoming, and
outgoing from any of the user's devices) and edits, pushed by Telegram as they happen, are written
to `telegram.db` as the sync script writes them, then imported into the archive at once. Messages
sent from the app go through the same client and the same path. Channels and bots stay out.
"""
import asyncio
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
        except Exception as e:      # one bad update must not stop the connection
            ctx.log("error: {e}", e=repr(e))

    c.add_event_handler(on_message, events.NewMessage())
    c.add_event_handler(on_message, events.MessageEdited())
    c.add_event_handler(on_state, events.Raw())
    await asyncio.to_thread(_report_states, ctx, _dialog_states(dialogs))
    await catch_up(ctx, c, dialogs)     # after the handlers: nothing falls between the two
    try:
        await c.run_until_disconnected()
    finally:
        CLIENTS.pop(ctx.id, None)


async def send(ctx, conversation, text, reply_to=None):
    c = await _client(ctx)
    peer = await c.get_input_entity(int(conversation["key"]))
    msg = await c.send_message(peer, text, reply_to=int(reply_to["key"]) if reply_to else None)
    chat = await c.get_entity(peer)
    if msg.sender is None:
        await msg.get_sender()
    await asyncio.to_thread(_store, ctx, chat, [msg])
    return {"id": msg.id}
