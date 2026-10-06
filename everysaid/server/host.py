"""The plugin host: runs plugin instances for one archive, and tells the open apps what happened.

Imports run in a thread each (one import at a time: they write a lot); live connections are asyncio
tasks that stay up while the server runs, restarted after an error with a growing pause. Every
event (a plugin's log line, its state, new messages) goes to the apps listening on the WebSocket;
new incoming messages also go out as push notifications, except for muted and archived chats.
"""
import asyncio
import json
import logging
import os
import threading
import time
import traceback

from .. import archive as archive_mod, plugins
from ..core import queries
from ..plugins.base import Context
from ..plugins.base import run_log
from ..plugins.i18n import tr
from ..errors import UserError


class Host:
    def __init__(self, store, push=None):
        self.store = store
        self.push = push
        self.loop = None
        self.listeners = set()
        self.import_lock = threading.Lock()
        self.contexts = {}
        self.running = {}           # instance id -> "import" | "live"
        self.live_tasks = {}
        self.marking = set()            # (instance, conversation) being told it was read now

    # events
    def listen(self):
        q = asyncio.Queue(maxsize=1000)
        self.listeners.add(q)
        return q

    def unlisten(self, q):
        self.listeners.discard(q)

    def emit(self, event):
        if self.loop is None:
            return
        self.loop.call_soon_threadsafe(self._dispatch, event)

    def _dispatch(self, event):
        if event.get("type") == "new":
            event = self._describe_new(event)
        for q in list(self.listeners):
            try:
                q.put_nowait(event)
            except asyncio.QueueFull:
                pass

    def _describe_new(self, event):
        """Which chats got what: the apps refresh those; push for incoming ones, except in archived chats
        (which stay archived: it is decided once, when the chat is first seen, then only by the user)."""
        db = self.store.read()
        (m0, m1), (c0, c1) = event.get("messages", (0, 0)), event.get("calls", (0, 0))
        states = queries._states(self.store)
        chats, incoming = {}, []
        for mid, conv, outgoing, txt, kind in db.execute(
                "SELECT m.id, m.conversation_id, m.outgoing, m.text, k.name FROM message m "
                "JOIN message_kind k ON k.id = m.kind_id WHERE m.id > ? AND m.id <= ? ORDER BY m.id", (m0, m1)):
            cid = queries.chat_of_conversation(self.store, conv)
            if cid:
                chats[cid] = chats.get(cid, 0) + 1
                if not outgoing and not states.get(cid, (0, 0, 0))[2]:
                    incoming.append((cid, mid, txt, kind))
        if self.push and incoming:
            self.push.notify(self.store, incoming)
        return {"type": "new", "chats": chats, "calls": c1 - c0}

    def alert(self, title, body):
        """Tell the user something about the app (a plugin's warning): on the open apps, and as a push."""
        self.emit({"type": "alert", "title": title, "body": body})
        if self.push:
            self.push.alert(self.store, title, body)

    # instances
    def ctx(self, iid):
        row = plugins.instance(self.store, iid)
        if not row:
            raise KeyError(iid)
        old = self.contexts.get(iid)
        c = Context(self, row)
        if old:
            c.lines, c.bar = old.lines, old.bar
        self.contexts[iid] = c
        return c

    def status(self, iid, lang="en"):
        row = plugins.instance(self.store, iid)
        p = plugins.get(row["plugin"])
        c = self.contexts.get(iid)
        ctx = self.ctx(iid)
        ready = p.check(ctx) if p else (False, "unknown plugin")
        out = plugins.public(row, lang)
        out.update({"running": self.running.get(iid), "ready": ready[0], "ready_text": tr(ready[1], lang),
                    "live_capable": bool(p and "live" in p.modes), "live": iid in self.live_tasks,
                    "can_send": bool(p and p.can_send), "log": [line for _, line in (c.lines[-30:] if c else [])], "bar": c.bar if c else "",
                    "asks": [{"key": k, "label": tr(label, lang)} for k, label in (p.asks(ctx) if p else [])],
                    "info": [{"label": tr(label, lang), "value": tr(value, lang)} for label, value in (p.info(ctx) if p else [])]})
        return out

    def _set_status(self, iid, status):
        with self.store.write() as db:
            db.execute("UPDATE plugin_instance SET last_run = ?, last_status = ? WHERE id = ?",
                       (int(time.time()), status[:500], iid))

    async def run(self, iid, action=None, given=None):
        """An import (or a plugin's own action) of one instance, in a thread. given: what the user
        typed in for this run (the plugin's asks()): kept only by this run, in memory."""
        if self.running.get(iid):
            raise UserError("host.running", 409)
        ctx = self.ctx(iid)
        ctx.lines.clear()
        ctx.bar = ""                        # the panel shows this run (the earlier ones are in their files)
        p = plugins.get(ctx.plugin_id)
        wanted = {k for k, _ in p.asks(ctx)} if p else set()
        ctx.given = {k: v for k, v in (given or {}).items() if k in wanted and v}
        self.running[iid] = "import"
        self.emit({"type": "plugin", "instance": iid, "running": "import"})

        def work():
            try:
                with run_log(iid, action or "import"):
                    self._work(ctx, p, iid, action)
            finally:
                ctx.given = {}          # gone with the run

        threading.Thread(target=work, name=f"plugin-{iid}", daemon=True).start()

    def _work(self, ctx, p, iid, action):
        """The run itself (in its thread, its lines in a log file of its own)."""
        try:
            ctx.log("— {what} —", what=tr(action or "import", ctx.lang))
            (p.action(ctx, action) if action else (p.sync(ctx) if hasattr(p, "sync") else p.run_import(ctx)))
            self._set_status(iid, "ok")
            if self.loop:
                self.loop.call_soon_threadsafe(self.auto_live, iid)     # now set up, perhaps
        except Exception as e:
            ctx.log("error: {e}", e=e)
            ctx.log(traceback.format_exc().strip())          # whole, in the log file and the panel
            self._set_status(iid, str(e))           # the reason ("ok" when it worked): the interface says the rest
        finally:
            self.running.pop(iid, None)
            self.emit({"type": "plugin", "instance": iid, "running": None})
            self.emit({"type": "changed"})

    def auto_live(self, iid):
        """Start a live connection the plugin wants by default, unless the user turned it off or it
        is not set up yet."""
        row = plugins.instance(self.store, iid)
        p = row and plugins.get(row["plugin"])
        if (not p or iid in self.live_tasks or not row["enabled"] or "live" not in p.modes
                or not json.loads(row["settings"] or "{}").get("_live", p.live_default) or not p.check(self.ctx(iid))[0]):
            return
        try:
            self.start_live(iid)
        except Exception as e:
            self.ctx(iid).log(f"live: {e}")

    def start_live(self, iid):
        if iid in self.live_tasks:
            return
        ctx = self.ctx(iid)
        p = plugins.get(ctx.plugin_id)
        if not p or "live" not in p.modes:
            raise UserError("host.no_live")

        async def keep():
            pause = 5
            while True:
                try:
                    ctx = self.ctx(iid)
                    ok, why = p.check(ctx)
                    if not ok:
                        ctx.log(f"live: {why}")
                        await asyncio.sleep(60)
                        continue
                    self.emit({"type": "plugin", "instance": iid, "live": True})
                    await p.live(ctx)
                    pause = 5
                except asyncio.CancelledError:
                    raise
                except Exception as e:
                    self.ctx(iid).log("live: error {e}; again in {pause}s", e=repr(e), pause=pause)
                await asyncio.sleep(pause)
                pause = min(pause * 2, 600)

        self.live_tasks[iid] = asyncio.get_running_loop().create_task(keep())
        plugins.update(self.store, iid, settings={"_live": True})

    def stop_live(self, iid, remember=True):
        t = self.live_tasks.pop(iid, None)
        if t:
            t.cancel()
        if remember:
            plugins.update(self.store, iid, settings={"_live": False})
        self.emit({"type": "plugin", "instance": iid, "live": False})

    async def startup(self):
        self.loop = asyncio.get_running_loop()
        for row in plugins.instances(self.store, "source"):
            self.auto_live(row["id"])       # if the user turned it on, or by the plugin's default once set up

    async def shutdown(self):
        for iid in list(self.live_tasks):
            self.stop_live(iid, remember=False)

    # sending
    def senders(self):
        """[(instance id, plugin)] of the sources that may send now, as each plugin says."""
        out = []
        for row in plugins.instances(self.store, "source"):
            p = plugins.get(row["plugin"])
            if row["enabled"] and p and p.can_send and p.sending(self.ctx(row["id"])):
                out.append((row["id"], p))
        return out

    def able(self, flag):
        """The services something can send to now with `flag` (can_reply, can_mention, can_send_files)."""
        return self.store.cached(f"{flag}:{int(time.time() // 60)}",
                                 lambda: {s for _, p in self.senders() if getattr(p, flag) for s in p.services})

    def replyable(self):
        """The services something can send an answer to a given message to now."""
        return self.able("can_reply")

    def sendable(self):
        """The services something can send to now (kept until the archive, its plugins included, changes:
        a plugin's check may read the keyring, too slow for every chat opened)."""
        # and at most a minute: a login outside the app changes only the keyring
        return self.store.cached(f"sendable:{int(time.time() // 60)}",
                                 lambda: {s for _, p in self.senders() for s in p.services})

    def unsendable(self):
        """{service: why}: the services an enabled source reaches but may not send to now, each with what
        its source says is missing (English, as plugins word it)."""
        def why():
            out = {}
            for row in plugins.instances(self.store, "source"):
                p = plugins.get(row["plugin"])
                if row["enabled"] and p and p.can_send:
                    reason = p.not_sending(self.ctx(row["id"]))
                    for s in p.services if reason else ():
                        out.setdefault(s, reason)
            return out
        return self.store.cached(f"unsendable:{int(time.time() // 60)}", why)

    async def send(self, chat_id, text, conversation_id=None, service=None, reply_to=None, mentions=None, file=None):
        """Send text in a chat through the plugin that reaches its service; returns what it said.
        reply_to: the id of a message of the chat it answers: then through its conversation, by a
        plugin that can reply. mentions: [{start, length, address_id}], members of the group the text
        names (left out where the plugin cannot mention: the text says them anyway). file: {data,
        filename, mime_type}, the text its caption: only through a plugin that can send files."""
        c = queries.chat(self.store, chat_id)
        if not c:
            raise KeyError(chat_id)
        convs = c["conversations"]
        answered = None
        if reply_to:
            row = self.store.read().execute("SELECT conversation_id, key FROM message WHERE id = ?", (int(reply_to),)).fetchone()
            if not row or row[0] not in convs:
                raise UserError("chat.not_in_chat")
            if not row[1]:
                raise UserError("chat.cannot_reply", 409)
            conversation_id, answered = row[0], {"id": int(reply_to), "key": row[1]}
        if conversation_id:
            if conversation_id not in convs:
                raise UserError("chat.not_in_chat")
            convs = [conversation_id]
        db = self.store.read()
        options = []
        senders = self.senders()
        for conv in convs:
            key, svc = db.execute("SELECT c.key, s.name FROM conversation c JOIN service s ON s.id = c.service_id "
                                      "WHERE c.id = ?", (conv,)).fetchone()
            last = db.execute("SELECT max(ts) FROM message WHERE conversation_id = ?", (conv,)).fetchone()[0] or 0
            for iid, p in senders:
                if svc in p.services and (answered is None or p.can_reply) and (file is None or p.can_send_files):
                    options.append((last, conv, key, svc, iid, p))
        if service:
            options = [o for o in options if o[3] == service]
        if not options:
            raise UserError("chat.cannot_reply" if answered else "chat.cannot_send_files" if file else "chat.no_sender", 409)
        options.sort(key=lambda o: o[0], reverse=True)          # where the chat was last active
        _, conv, key, service, iid, p = options[0]
        if mentions:
            members = {a for (a,) in db.execute("SELECT address_id FROM conversation_member WHERE conversation_id = ?",
                                                (conv,))}
            if any(int(m["address_id"]) not in members for m in mentions):
                raise UserError("chat.not_a_member")
        ctx = self.ctx(iid)
        extra = {}
        if answered:
            extra["reply_to"] = answered
        if mentions and p.can_mention:
            extra["mentions"] = mentions
        if file:
            extra["file"] = file
        result = await p.send(ctx, {"id": conv, "key": key, "service": service}, text, **extra)
        self.emit({"type": "changed"})
        return {"service": service, "conversation_id": conv, "result": result}

    async def mark_read(self, chat_id, until):
        """The user read the chat up to `until` (Unix ms) here: each of its conversations with something
        newer from the others than the service last said was read is told so, through the plugins that
        can and are connected (a live one only while its connection runs; each sends only where its user
        allowed), one at a time per conversation. Their errors go to their logs."""
        c = queries.chat(self.store, chat_id)
        if not c:
            return
        db = self.store.read()
        for row in plugins.instances(self.store, "source"):
            p = plugins.get(row["plugin"])
            if not (row["enabled"] and p and p.can_mark_read) or ("live" in p.modes and row["id"] not in self.live_tasks):
                continue
            ctx = self.ctx(row["id"])
            for conv in c["conversations"]:
                key, svc, newest, told = db.execute(
                    "SELECT c.key, s.name, (SELECT max(ts) FROM message WHERE conversation_id = c.id AND NOT outgoing "
                    "AND ts <= ?), (SELECT max(value) FROM state_report WHERE conversation_id = c.id AND field = 'read_until') "
                    "FROM conversation c JOIN service s ON s.id = c.service_id WHERE c.id = ?", (until, conv)).fetchone()
                if svc not in p.services or not newest or (told or 0) >= newest or (row["id"], conv) in self.marking:
                    continue
                self.marking.add((row["id"], conv))
                try:
                    await p.mark_read(ctx, {"id": conv, "key": key, "service": svc}, until)
                except Exception as e:      # reading here must not fail on a service's error
                    ctx.log("read receipts: {e}", e=str(e) or repr(e))
                finally:
                    self.marking.discard((row["id"], conv))

    def mark_read_soon(self, chat_id, until):
        """mark_read on the server's loop, from a request's thread, not waited for (what fails outside
        a plugin goes to the server's log, not lost)."""
        if not self.loop:
            return

        def done(f):
            if not f.cancelled() and f.exception():
                logging.getLogger("everysaid.server").error("read receipts", exc_info=f.exception())
        asyncio.run_coroutine_threadsafe(self.mark_read(chat_id, until), self.loop).add_done_callback(done)

    # libraries
    def libraries(self):
        return [r for r in plugins.instances(self.store, "library") if r["enabled"]]

    def default_library(self):
        libs = self.libraries()
        return next((r for r in libs if r["is_default"]), libs[0] if libs else None)

    def local_file(self, sha256):
        row = self.store.read().execute("SELECT path FROM media WHERE sha256 = ?", (sha256,)).fetchone()
        if not row:
            return None
        p = os.path.join(archive_mod.MEDIA_ROOT, row[0])
        return p if os.path.exists(p) else None

    def to_library(self, sha256, iid=None, date_ms=None):
        """Store a file in a library (the default one unless named), unless it is there already;
        either way the archive records the link. Runs in the caller's thread."""
        from ..plugins.libraries import link
        row = plugins.instance(self.store, iid) if iid else self.default_library()
        if not row:
            raise UserError("library.none", 409)
        path = self.local_file(sha256)
        p = plugins.get(row["plugin"])
        ctx = self.ctx(row["id"])
        known = self.store.read().execute("SELECT asset_id FROM library_link WHERE sha256 = ? AND instance_id = ?",
                                          (sha256, row["id"])).fetchone()
        if known:       # stored there before (the stored copy may differ: a date or a make written in)
            return {"already": True, "ref": known[0], "library": row["label"]}
        if not path:
            raise UserError("file_gone", 409)
        found = p.find(ctx, sha256, path)
        if found:
            link(self.store, row["id"], row["label"], sha256, found, "checksum")
            return {"already": True, "ref": found, "library": row["label"]}
        db = self.store.read()
        mime, msg_ts, service = db.execute(
            "SELECT md.mime, min(m.ts), s.name FROM media md JOIN attachment a ON a.sha256 = md.sha256 "
            "JOIN message m ON m.id = a.message_id JOIN service s ON s.id = m.service_id WHERE md.sha256 = ?",
            (sha256,)).fetchone()
        decided = db.execute("SELECT date_ms FROM media_decision WHERE sha256 = ?", (sha256,)).fetchone()
        when = date_ms or (decided[0] if decided and decided[0] else None) or msg_ts
        ref = p.store(ctx, path, {"sha256": sha256, "mime": mime, "date_ms": when, "service": service})
        link(self.store, row["id"], row["label"], sha256, ref, "upload")
        return {"already": False, "ref": ref, "library": row["label"]}

    def fetch(self, sha256, size):
        """A file from the library that holds it: ("path", p) or ("bytes", data, type), or None."""
        for iid, ref in self.store.read().execute(
                "SELECT instance_id, asset_id FROM library_link WHERE sha256 = ? AND instance_id IS NOT NULL", (sha256,)):
            row = plugins.instance(self.store, iid)
            p = plugins.get(row["plugin"]) if row else None
            if not p or not row["enabled"]:
                continue
            try:
                got = p.fetch(self.ctx(iid), ref, size)
                if got:
                    return got
            except Exception:
                continue
        return None
