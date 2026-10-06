#!/usr/bin/env python3
"""Read the owner's Telegram history through the Telegram API (Telethon), with their own account.

    uv run --extra telegram python scripts/telegram-sync.py --save-credentials   api_id and api_hash
    uv run --extra telegram python scripts/telegram-sync.py --login              makes the session
    uv run --extra telegram python scripts/telegram-sync.py --survey             survey of the chats
    uv run --extra telegram python scripts/telegram-sync.py                      the messages
    uv run --extra telegram python scripts/telegram-sync.py --media [--dry-run]  their pictures

--save-credentials asks once for the api_id and api_hash from my.telegram.org (hidden) and keeps
them as secrets (`telegram-api-id`, `telegram-api-hash`: the keyring, else files of those names in
the config folder, mode 600). --login asks for the phone, the code and the 2FA password and makes
the session, which gives full access to the account: kept as a secret too (`telegram-session`, a
Telethon StringSession), so in the keyring, and in a file of that name, mode 600, only where there
is no keyring. The survey lists every chat with its kind, size and dates, no content.

With no option, every chat but channels and bots is read into `<cache>/telegram/telegram.db`, each
message whole (Telethon's own fields, as JSON, without the binary ones: file references and inline
thumbnails), with the chats and the people seen. A later run brings only what came after the last
message read in each chat; edits and deletions of older messages are not followed. --media then
downloads the pictures, videos, GIFs, video notes and voice messages of those messages into
`<cache>/telegram/media/<chat>/<message><ext>` (stickers and other files are not downloaded; the
message keeps their name and size), except in the chats config `[telegram] no_media` lists (their
ids, as the survey gives them), and not at all with `[telegram] media = false`; --dry-run says only
how many and how big.

How far each chat was read, by the owner and by the others, is kept too (`chat_read`).

Read only: nothing is sent, nothing is marked read. Nothing secret is ever printed.
Secret chats live only on the devices and are not reachable through the API.
"""
import argparse
import asyncio
import csv
import getpass
import json
import os
import sqlite3
import sys
import time
from collections import Counter

from telethon import TelegramClient, utils
from telethon.sessions import StringSession
from telethon.tl.types import Channel, Chat, InputMessagesFilterPhotoVideo, InputMessagesFilterRoundVoice, User

from common import config
from everysaid import telegram_store

SESSION = "telegram-session"
OUT = os.path.join(config.CACHE, "telegram")
DB = os.path.join(OUT, "telegram.db")
MEDIA = os.path.join(OUT, "media")

os.umask(0o077)

SCHEMA = telegram_store.SCHEMA


def save_credentials():
    api_id = input("api_id: ").strip()
    api_hash = getpass.getpass("api_hash (hidden): ").strip()
    if not api_id.isdigit() or not api_hash:
        sys.exit("api_id must be a number and api_hash not empty")
    config.save_secret("telegram-api-id", api_id)
    print(f"saved in {config.save_secret('telegram-api-hash', api_hash)}")


def client():
    api_id, api_hash = config.secret("telegram-api-id"), config.secret("telegram-api-hash")
    if not api_id or not api_hash:
        sys.exit("no credentials: run with --save-credentials first")
    session = StringSession(config.secret(SESSION))
    return TelegramClient(session, int(api_id), api_hash, flood_sleep_threshold=300)


def keep_session(c):
    """Save the session if it is new or changed (another data centre after a migration)."""
    value = c.session.save()
    if value and value != config.secret(SESSION):
        return config.save_secret(SESSION, value)


def kind(d):
    e = d.entity
    if d.is_user:
        return "saved" if getattr(e, "is_self", False) else "bot" if getattr(e, "bot", False) else "user"
    if d.is_channel and getattr(e, "broadcast", False):
        return "channel"
    return "supergroup" if d.is_channel else "group"


plain, dump = telegram_store.plain, telegram_store.dump


async def count(c, entity, **kw):
    return (await c.get_messages(entity, limit=0, **kw)).total


async def survey(c):
    os.makedirs(OUT, exist_ok=True)
    rows = []
    async for d in c.iter_dialogs():
        first = await c.get_messages(d.entity, limit=1, reverse=True)
        rows.append({
            "kind": kind(d), "id": d.id, "title": d.name or "", "archived": int(d.archived),
            "messages": await count(c, d.entity),
            "photos_videos": await count(c, d.entity, filter=InputMessagesFilterPhotoVideo),
            "voice_round": await count(c, d.entity, filter=InputMessagesFilterRoundVoice),
            "first": first[0].date.date().isoformat() if first else "",
            "last": d.date.date().isoformat() if d.date else "",
        })
        print(f"\r{len(rows)} chats", end="", file=sys.stderr)
    print(file=sys.stderr)
    path = os.path.join(OUT, "survey.tsv")
    with open(path, "w", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, rows[0].keys() if rows else ["kind"], delimiter="\t")
        w.writeheader()
        w.writerows(rows)
    chats, msgs = Counter(), Counter()
    for r in rows:
        chats[r["kind"]] += 1
        msgs[r["kind"]] += r["messages"]
    for k in chats:
        print(f"{k:11} {chats[k]:5} chats {msgs[k]:9} messages")
    print(f"details: {path}")


def store():
    os.makedirs(OUT, exist_ok=True)
    db = sqlite3.connect(DB)
    db.execute("PRAGMA journal_mode = WAL")
    db.executescript(SCHEMA)
    return db


async def sync(c):
    db = store()
    total = 0
    async for d in c.iter_dialogs():
        k = kind(d)
        if k in ("channel", "bot"):
            continue
        db.execute("INSERT INTO chat (id, kind, title, archived, json) VALUES (?, ?, ?, ?, ?) "
                   "ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, title = excluded.title, "
                   "archived = excluded.archived, json = excluded.json",
                   (d.id, k, d.name, int(d.archived), dump(d.entity)))
        telegram_store.note_read(db, d.id, getattr(d.dialog, "read_inbox_max_id", None),
                                 getattr(d.dialog, "read_outbox_max_id", None))
        last = db.execute("SELECT max(id) FROM message WHERE chat_id = ?", (d.id,)).fetchone()[0] or 0
        n = 0
        async for m in c.iter_messages(d.entity, min_id=last, reverse=True):
            db.execute("INSERT OR IGNORE INTO message (chat_id, id, date, json) VALUES (?, ?, ?, ?)",
                       (d.id, m.id, int(m.date.timestamp()), dump(m)))
            sender = m.sender
            if isinstance(sender, (User, Chat, Channel)):
                db.execute("INSERT OR REPLACE INTO entity VALUES (?, ?)", (utils.get_peer_id(sender), dump(sender)))
            n += 1
            if n % 1000 == 0:
                db.commit()
                print(f"\r{d.name or d.id}: {n}", end="", file=sys.stderr)
        db.execute("UPDATE chat SET synced_at = ? WHERE id = ?", (int(time.time()), d.id))
        db.commit()
        total += n
        print(f"\r{k:10} {n:7} new  {d.name or d.id}", file=sys.stderr)
    print(f"{total} new messages in {DB}")


def wanted(m):
    """Whether a message's media is one --media downloads, and its size."""
    media = m.get("media") or {}
    if media.get("_") == "MessageMediaPhoto" and media.get("photo"):
        sizes = [s.get("size") or max(s.get("sizes") or [0]) for s in media["photo"].get("sizes", [])]
        return True, max(sizes or [0])
    doc = media.get("document") if media.get("_") == "MessageMediaDocument" else None
    if not doc:
        return False, 0
    attrs = {a["_"] for a in doc.get("attributes", [])}
    if "DocumentAttributeSticker" in attrs:
        return False, 0
    mime = doc.get("mime_type") or ""
    voice = any(a["_"] == "DocumentAttributeAudio" and a.get("voice") for a in doc.get("attributes", []))
    if mime.startswith(("image/", "video/")) or voice or "DocumentAttributeAnimated" in attrs:
        return True, doc.get("size") or 0
    return False, 0


async def media(c, dry_run, only=None):
    """only: the chats whose media are wanted (the app's choice per chat); then config's
    `media = false` and `no_media` do not apply, the choice being explicit."""
    if only is None and not config.get("telegram", "media", True):
        sys.exit("[telegram] media = false in config: no media are downloaded")
    db = store()
    todo = {}
    size = 0
    skip = {int(i) for i in config.get("telegram", "no_media", [])} if only is None else set()
    for chat_id, mid, js in db.execute("SELECT chat_id, id, json FROM message WHERE file IS NULL"):
        if chat_id in skip or (only is not None and chat_id not in only):
            continue
        ok, n = wanted(json.loads(js))
        if ok:
            todo.setdefault(chat_id, []).append(mid)
            size += n
    count_ = sum(len(v) for v in todo.values())
    print(f"{count_} files, {size / 1e9:.2f} GB, in {len(todo)} chats")
    if dry_run or not count_:
        return
    done = 0
    await c.get_dialogs()       # the chats' access hashes: a StringSession does not keep them
    for chat_id, ids in todo.items():
        entity = await c.get_input_entity(chat_id)
        folder = os.path.join(MEDIA, str(chat_id))
        os.makedirs(folder, exist_ok=True)
        for i in range(0, len(ids), 100):     # fetched again: the file references expire
            for m in await c.get_messages(entity, ids=ids[i:i + 100]):
                if m is None or not m.file:
                    continue                    # deleted since, or no longer carries it
                rel = f"{chat_id}/{m.id}{m.file.ext or ''}"
                dest = os.path.join(MEDIA, rel)
                await c.download_media(m, file=dest + ".part")
                os.replace(dest + ".part", dest)
                db.execute("UPDATE message SET file = ? WHERE chat_id = ? AND id = ?", (rel, chat_id, m.id))
                db.commit()
                done += 1
                print(f"\r{done}/{count_}", end="", file=sys.stderr)
    print(file=sys.stderr)
    print(f"{done} downloaded into {MEDIA}")


async def main():
    p = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    p.add_argument("--save-credentials", action="store_true")
    p.add_argument("--login", action="store_true")
    p.add_argument("--survey", action="store_true")
    p.add_argument("--media", action="store_true")
    p.add_argument("--dry-run", action="store_true", help="with --media: only how many and how big")
    p.add_argument("--chats", type=int, nargs="*", help="with --media: only these chats (ids)")
    a = p.parse_args()
    if a.save_credentials:
        return save_credentials()
    only = set(a.chats) if a.chats is not None else None
    if a.media and a.dry_run:
        return await media(None, True, only)
    c = client()
    if a.login:
        await c.start()                 # asks for the phone, the code and the 2FA password
        where = keep_session(c) or "unchanged"
        me = await c.get_me()
        print(f"logged in as {me.first_name or ''} (session: {where})")
    else:
        await c.connect()
        if not await c.is_user_authorized():
            sys.exit("not logged in: run with --login first")
        await (survey(c) if a.survey else media(c, False, only) if a.media else sync(c))
        keep_session(c)
    await c.disconnect()


if __name__ == "__main__":
    asyncio.run(main())
