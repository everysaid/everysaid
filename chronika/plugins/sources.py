"""The source plugins: each one way of reaching a service.

Most wrap what the project already has (the extract scripts and the importers): an import first
brings the source up to date where it can (a backup over the cable, an export over adb, the
service's API), then runs the importers for what it brings. Records found by two plugins are kept
once, with both origins (the importers' own deduplication).
"""
import asyncio
import contextlib
import io
import json
import os
import queue
import subprocess
import sys
import threading
import time
import urllib.request

from .. import config
from ..errors import plugin_error
from .base import Plugin, Setting
from .i18n import tr
from .icons import ICONS

SCRIPTS = os.path.join(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))), "scripts")


class _Lines(io.TextIOBase):
    """stdout of an importer, line by line into the instance's log."""

    def __init__(self, ctx):
        self.ctx, self.buf = ctx, ""

    def write(self, s):
        self.buf += s
        while "\n" in self.buf:
            line, self.buf = self.buf.split("\n", 1)
            if line.strip():
                self.ctx.log(line)
        return len(s)


def run_script(ctx, name, *args, extra_env=None, stdin=None):
    """One of the project's scripts, its output into the log; raises if it fails. stdin: a line
    given to it (a password), through a pipe: never in its arguments or environment."""
    path = os.path.join(SCRIPTS, name)
    if not os.path.exists(path):
        raise RuntimeError(f"{name} not found (the scripts come with the project's code)")
    env = dict(os.environ, PYTHONUTF8="1", PYTHONUNBUFFERED="1", **(extra_env or {}))   # its lines as they come
    p = subprocess.Popen([sys.executable, path, *args], stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                         stdin=subprocess.PIPE if stdin else subprocess.DEVNULL, env=env)
    if stdin:
        p.stdin.write((stdin + "\n").encode())
        p.stdin.close()
    # As a terminal shows it: a line ends with \n; a \r draws the line again (a progress bar). The
    # bar is one line that changes in place: drawn as it changes (at most every 0.2 s, and its last
    # state always), and in the log as each drawing ends.
    chunks = queue.Queue()

    def read():
        while chunk := os.read(p.stdout.fileno(), 4096):
            chunks.put(chunk)
        chunks.put(None)
    threading.Thread(target=read, daemon=True).start()
    last, pending, shown, sent, drawn = "", b"", "", "", 0.0
    while True:
        try:
            chunk = chunks.get(timeout=0.2)
        except queue.Empty:
            chunk = b""
        if chunk is None:
            break
        pending += chunk
        while b"\n" in pending:
            raw, pending = pending.split(b"\n", 1)
            text = raw.decode("utf-8", "replace").rstrip("\r")
            line = (text.split("\r")[-1] or shown).rstrip()
            redrawn = bool(shown) or "\r" in text
            shown = sent = ""
            if line.strip():
                ctx.log(line, redrawn=redrawn)
                last = line.strip()
        if b"\r" in pending:
            parts = pending.decode("utf-8", "replace").split("\r")
            shown = next((x for x in reversed(parts) if x.strip()), shown)
            pending = parts[-1].encode()
        if shown and shown != sent and time.time() - drawn >= 0.2:
            ctx.progress(shown)
            sent, drawn = shown, time.time()
    if pending.strip():
        line = pending.decode("utf-8", "replace").split("\r")[-1].rstrip()
        ctx.log(line)
        last = line.strip()
    if p.wait():                        # its own last words say why (else its exit code)
        raise RuntimeError(last or f"{name}: exit code {p.returncode}")


def run_importers(ctx, steps):
    """steps: [(label, function(archive))]. In the archive, the sources these bring are then tied
    to this instance. Returns the ids of the new messages and calls."""
    from ..archive import Archive
    with ctx.host.import_lock:
        a = Archive(ctx.store.path)
        try:
            m0 = a.db.execute("SELECT ifnull(max(id), 0) FROM message").fetchone()[0]
            c0 = a.db.execute("SELECT ifnull(max(id), 0) FROM call").fetchone()[0]
            t0 = int(time.time()) - 1
            out = _Lines(ctx)
            for label, fn in steps:
                ctx.log("== {label}", label=tr(label, ctx.lang))
                with contextlib.redirect_stdout(out):
                    fn(a)
            a.db.execute("UPDATE source SET instance_id = ? WHERE instance_id IS NULL AND imported_at >= ?", (ctx.id, t0))
            a.db.commit()
            m1 = a.db.execute("SELECT ifnull(max(id), 0) FROM message").fetchone()[0]
            c1 = a.db.execute("SELECT ifnull(max(id), 0) FROM call").fetchone()[0]
        finally:
            a.db.close()
    if m1 > m0 or c1 > c0:
        ctx.emit({"type": "new", "messages": [m0, m1], "calls": [c0, c1]})
    ctx.log("new messages: {m}, new calls: {c}", m=m1 - m0, c=c1 - c0)
    return (m0, m1), (c0, c1)


# How the services these plugins bring look (each plugin declares those it brings).
LOOKS = {
    "sms": {"name": "SMS", "color": "#8e94a3", "short": "SMS"},
    "mms": {"name": "MMS", "color": "#8e94a3", "short": "MMS"},
    "rcs": {"name": "RCS", "color": "#6b8afd", "short": "RCS"},
    "imessage": {"name": "iMessage", "color": "#0a84ff", "short": "iM"},
    "phone": {"name": "Phone", "color": "#34c759", "short": "☎", "messages": False},
    "facetime": {"name": "FaceTime", "color": "#30d158", "short": "FT", "messages": False},
    "whatsapp": {"name": "WhatsApp", "color": "#25d366", "short": "WA"},
    "viber": {"name": "Viber", "color": "#7360f2", "short": "Vb"},
    "telegram": {"name": "Telegram", "color": "#2aabee", "short": "Tg"},
}


def looks(*services):
    return {s: LOOKS[s] | {"icon": ICONS[s]} for s in services}


class IphoneBackup(Plugin):
    id = "iphone-backup"
    name = "iPhone (encrypted backup)"
    services = ("sms", "imessage", "rcs", "phone", "facetime", "whatsapp", "viber")
    service_info = looks(*services)
    name_weights = {"whatsapp/book": 80, "whatsapp/chat": 50, "whatsapp/profile": 30}
    state_weights = {"muted": 60}
    description = ("Messages, iMessage, calls, WhatsApp and Viber from an encrypted iPhone backup, made over "
                   "the cable with libimobiledevice. The backup must be encrypted: only then does it hold calls.")
    platforms = ("linux", "darwin")
    needs = ("the phone on a USB cable", "libimobiledevice (idevicebackup2)", "the backup password")
    settings = (
        Setting("backup", "A new backup before importing", "bool", default=True,
                help="Off: only decrypt the backup already there"),
        Setting("backup_root", "Backup folder", "path", default=config.IPHONE_BACKUP_ROOT,
                help="Where the encrypted backup is kept (a folder for each phone inside)"),
        Setting("udid", "UDID", help="Only with more than one iPhone; otherwise it is found",
                pattern=r"[0-9A-Fa-f]{8}-?[0-9A-Fa-f]{16}|[0-9A-Fa-f]{40}"),
        Setting("password", "The backup password", "select", default="ask",
                options=[("ask", "Asked for at each import, kept nowhere"), ("keyring", "Kept in the system's keyring")],
                keeps={"keyring": ("backup_password", "The backup password")},
                help="Going back to asking takes it out of the keyring"),
    )

    def check(self, ctx):
        if ctx.settings.get("password") == "keyring" and not ctx.secret("backup_password"):
            return False, "missing: The backup password"
        return True, "ready"

    def asks(self, ctx):
        return [] if ctx.settings.get("password") == "keyring" else [("backup_password", "The backup password")]

    def _backup(self, ctx):
        """(the folder of this phone's backup, or None when it is not known yet)."""
        root = ctx.settings.get("backup_root") or config.IPHONE_BACKUP_ROOT
        udid = ctx.settings.get("udid")
        if not udid and os.path.isdir(root):
            found = [d for d in os.listdir(root) if os.path.exists(os.path.join(root, d, "Manifest.plist"))]
            udid = found[0] if len(found) == 1 else None
        return os.path.join(root, udid) if udid else None

    def info(self, ctx):
        where = self._backup(ctx)
        root = ctx.settings.get("backup_root") or config.IPHONE_BACKUP_ROOT
        manifest = where and os.path.join(where, "Manifest.db")
        if not manifest or not os.path.exists(manifest):
            return [("Backup", root), ("Last backup", "none yet")]
        return [("Backup", where), ("Last backup", time.strftime("%Y-%m-%d %H:%M", time.localtime(os.path.getmtime(manifest)))),
                ("Size", _size(where))]

    def _import(self, ctx):
        from .. import calls, media, sms, viber, voip, whatsapp
        return run_importers(ctx, [("SMS, iMessage", sms.run), ("calls", calls.run), ("Viber", viber.run),
                                   ("WhatsApp", whatsapp.run), ("WhatsApp and Viber calls", voip.run),
                                   ("files", media.run)])

    def run_import(self, ctx):
        # first a new backup over the cable (unless turned off), then the databases out of it, then the import
        args = ["--backup-root", ctx.settings.get("backup_root") or config.IPHONE_BACKUP_ROOT, "--password-stdin"]
        if ctx.settings.get("udid"):
            args += ["--udid", ctx.settings["udid"]]
        if not ctx.settings.get("backup", True):
            args.append("--no-backup")
        password = ctx.given.get("backup_password") or ctx.secret("backup_password")
        if not password:
            raise plugin_error("The backup password is needed")
        run_script(ctx, "iphone-sync.py", *args, stdin=password)
        return self._import(ctx)


_sizes = {}


def _size(folder):
    """A backup's size in words (worked out again only when its manifest changes)."""
    stamp = os.path.getmtime(os.path.join(folder, "Manifest.db"))
    if _sizes.get(folder, (None,))[0] != stamp:
        total = sum(e.stat().st_size for e in _walk(folder))
        _sizes[folder] = (stamp, f"{total / 1e9:.1f} GB" if total >= 1e9 else f"{total / 1e6:.0f} MB")
    return _sizes[folder][1]


def _walk(folder):
    for e in os.scandir(folder):
        if e.is_dir(follow_symlinks=False):
            yield from _walk(e.path)
        elif e.is_file(follow_symlinks=False):
            yield e


class AndroidAdb(Plugin):
    id = "android-adb"
    name = "Android (adb)"
    services = ("sms", "mms", "phone")
    service_info = looks(*services)
    description = "SMS, MMS, calls and blocked numbers of an Android phone, read over adb (USB debugging on)."
    needs = ("the phone on a USB cable", "adb", "USB debugging enabled")
    settings = (Setting("serial", "Serial", help="Only when adb sees more than one phone"),)

    def run_import(self, ctx):
        from .. import calls, media, sms
        serial = ctx.settings.get("serial")
        run_script(ctx, "android-export.py", *(["-s", serial] if serial else []))
        return run_importers(ctx, [("SMS, MMS", sms.run), ("calls", calls.run), ("files", media.run)])


class ViberDesktop(Plugin):
    id = "viber-desktop"
    name = "Viber Desktop export"
    services = ("viber",)
    service_info = looks(*services)
    description = ("The history Viber Desktop holds (synced from the phone it is linked to), decrypted with "
                   "scripts/viber-desktop-export.cpp. Linux only: there is no official way.")
    platforms = ("linux",)
    needs = ("Viber Desktop", "a decrypted export (viber-desktop-export.cpp)")
    settings = (Setting("export", "Decrypted database", "path", required=True, default=config.VIBER_DESKTOP),)

    def run_import(self, ctx):
        from .. import viber
        path = ctx.settings.get("export")
        return run_importers(ctx, [("Viber Desktop", lambda a: viber.run(a, desktop_db=path))])


class WhatsappBridge(Plugin):
    id = "whatsapp-bridge"
    name = "WhatsApp (live bridge)"
    services = ("whatsapp",)
    service_info = looks(*services)
    name_weights = {"whatsapp/book": 80, "whatsapp/chat": 50, "whatsapp/profile": 30}
    state_weights = {"muted": 60, "pinned": 0}
    description = ("WhatsApp as it arrives, through a whatsmeow bridge linked as a device "
                   "(whatsapp-mcp's whatsapp-bridge). Unofficial: WhatsApp may block accounts that use one; "
                   "sending raises that risk.")
    modes = ("import", "live")
    live_default = True
    needs = ("a running whatsmeow bridge", "the bridge's store folder")
    settings = (
        Setting("store", "The bridge's store folder", "path", required=True, default=config.WHATSAPP_BRIDGE),
        Setting("api", "The bridge's REST API", "url", default="http://127.0.0.1:8080"),
        Setting("send", "Sending messages", "bool", default=False,
                help="A risk for the account; needs the bridge started with -send. Turned off by itself "
                     "when WhatsApp warns the account"),
        Setting("interval", "Check every (seconds)", "number", default=10),
    )
    can_send = True

    def sending(self, ctx):
        state = self.bridge_state(ctx)
        return (bool(ctx.settings.get("send")) and state.get("send_enabled") == "1" and not state.get("send_blocked")
                and super().sending(ctx))

    def check(self, ctx):
        ok, why = super().check(ctx)
        blocked = self.bridge_state(ctx).get("send_blocked")
        return (ok, f"sending blocked by the bridge: {blocked}") if ok and blocked else (ok, why)

    def _paths(self, ctx):
        d = ctx.settings.get("store") or ""
        return os.path.join(d, "messages.db"), os.path.join(d, "whatsapp.db")

    def bridge_state(self, ctx):
        """What the bridge last recorded about its connection (bridge_state: connection, send_enabled,
        send_blocked, ban_until); empty for a bridge from before that table."""
        path = self._paths(ctx)[0]
        if not os.path.exists(path):
            return {}
        try:
            db = config.read_only(path)
            try:
                return dict(db.execute("SELECT key, value FROM bridge_state").fetchall())
            finally:
                db.close()
        except Exception:
            return {}

    def watch_state(self, ctx):
        """Once the bridge blocks sending (WhatsApp warned the account), turn this instance's sending
        off too, say so in its log and to the user's devices. Turning it on again is the user's."""
        blocked = self.bridge_state(ctx).get("send_blocked") or ""
        if blocked == ctx.state.get("send_blocked", ""):
            return
        ctx.save_state(send_blocked=blocked)
        if not blocked:
            return
        ctx.log("WhatsApp warned the account, sending is off: {why}", why=blocked)
        if ctx.settings.get("send"):
            from . import update
            update(ctx.store, ctx.id, settings={"send": False})
        ctx.host.alert(tr("WhatsApp warned the account", ctx.lang), blocked)
        ctx.emit({"type": "changed"})

    def run_import(self, ctx):
        from .. import voip, whatsapp
        bridge, store = self._paths(ctx)
        changed = {}
        # only the bridge's databases: an iPhone's are another instance's
        out = run_importers(ctx, [
            ("WhatsApp (bridge)", lambda a: changed.update(whatsapp.run(a, iphone_db=None, contacts_db=None,
                                                                        bridge_db=bridge, store_db=store) or {})),
            ("WhatsApp calls (bridge)", lambda a: voip.bridge_calls(a, voip.Calls(a), bridge, store))])
        if changed:     # edits, deletions, reactions on messages already shown
            ctx.emit({"type": "changed"})
        return out

    async def live(self, ctx):
        # the messages (messages.db) and the chats' state, archived, pinned, muted (the store, whatsapp.db)
        paths = self._paths(ctx)
        last = None
        while True:
            stamps = [os.stat(f).st_mtime_ns for p in paths for f in (p, p + "-wal") if os.path.exists(f)]
            m = max(stamps) if stamps else None
            if m and m != last:
                if last is not None:
                    await asyncio.to_thread(self.run_import, ctx)
                await asyncio.to_thread(self.watch_state, ctx)
                last = m
            await asyncio.sleep(max(2, int(ctx.settings.get("interval") or 10)))

    async def send(self, ctx, conversation, text, reply_to=None):
        if not ctx.settings.get("send"):
            raise plugin_error("Sending is off in this source's settings")
        # a person's chat is keyed by their number (+E.164) or LID; the bridge takes a number's digits or a jid
        key = conversation["key"]
        body = json.dumps({"recipient": key.lstrip("+") if key.startswith("+") else key, "message": text}).encode()
        req = urllib.request.Request((ctx.settings.get("api") or "http://127.0.0.1:8080").rstrip("/") + "/api/send",
                                     data=body, headers={"Content-Type": "application/json"}, method="POST")
        try:
            with await asyncio.to_thread(urllib.request.urlopen, req, timeout=30) as r:
                answer = json.loads(r.read() or b"{}")
        except urllib.error.HTTPError as e:
            if e.code == 404:
                raise plugin_error("The bridge does not offer sending (/api/send)") from e
            try:                        # the bridge's refusals: off, blocked, a limit, not a chat they wrote in
                answer = json.loads(e.read() or b"{}")
            except ValueError:
                raise e from None
            await asyncio.to_thread(self.watch_state, ctx)
            raise plugin_error(answer.get("message") or "Sending failed") from e
        if not answer.get("success", True):
            raise plugin_error(answer.get("message") or "Sending failed")
        await asyncio.to_thread(self.run_import, ctx)      # the bridge stores what it sent
        return answer


class Telegram(Plugin):
    id = "telegram"
    name = "Telegram"
    services = ("telegram",)
    service_info = looks(*services)
    name_weights = {"telegram/profile": 40}     # chosen by each person
    state_weights = {"muted": 60, "pinned": 0}
    description = ("Every chat but channels and bots, through Telegram's API with the user's own account "
                   "(Telethon): the whole history, then live.")
    modes = ("import", "live")
    live_default = True
    needs = ("api_id and api_hash from my.telegram.org", "a login (a code that arrives in Telegram)")
    settings = (Setting("media", "Download pictures and videos", "bool", default=False),)
    can_send = True
    can_reply = True

    def check(self, ctx):
        if not (config.secret("telegram-api-id") and config.secret("telegram-api-hash")):
            return False, "missing: api_id and api_hash (scripts/telegram-sync.py --save-credentials)"
        if not config.secret("telegram-session"):
            return False, "missing: a login (scripts/telegram-sync.py --login)"
        return True, "ready"

    def run_import(self, ctx):
        from .. import media, telegram
        run_script(ctx, "telegram-sync.py")
        skip = {int(c) for c in ctx.settings.get("skip_chats") or []}
        steps = [("Telegram", lambda a: telegram.run(a, skip=skip))]
        media_chats = [str(c) for c in ctx.settings.get("media_chats") or []]
        if ctx.settings.get("media") or media_chats:
            run_script(ctx, "telegram-sync.py", "--media", *(["--chats", *media_chats] if media_chats else []))
            steps.append(("files", media.run))
        return run_importers(ctx, steps)

    def chats(self, ctx):
        """The chats telegram-sync.py has seen, with the user's choice for each."""
        from .. import config as cfg, telegram
        import os
        if not os.path.exists(telegram.DB):
            return []
        skip = {int(c) for c in ctx.settings.get("skip_chats") or []}
        media = {int(c) for c in ctx.settings.get("media_chats") or []}
        db = cfg.read_only(telegram.DB)
        out = []
        for cid, kind, title, archived, n, first, last in db.execute(
                "SELECT c.id, c.kind, c.title, c.archived, count(m.id), min(m.date), max(m.date) FROM chat c "
                "LEFT JOIN message m ON m.chat_id = c.id GROUP BY c.id ORDER BY max(m.date) DESC"):
            out.append({"id": cid, "kind": kind, "title": title, "archived": bool(archived), "messages": n,
                        "first": (first or 0) * 1000, "last": (last or 0) * 1000,
                        "import": cid not in skip, "media": cid in media})
        return out

    async def live(self, ctx):
        from . import telegram_live
        await telegram_live.run(ctx, self)

    async def send(self, ctx, conversation, text, reply_to=None):
        from . import telegram_live
        return await telegram_live.send(ctx, conversation, text, reply_to)


class CarrierNotices(Plugin):
    id = "carrier-notices"
    name = "Carrier missed-call notices"
    services = ("phone",)
    service_info = looks(*services)
    description = ("Calls known only from the carrier's SMS notices (\"you have a missed call from ...\"), "
                   "read from the archive's own SMS, by a parser per carrier or country.")
    settings = (Setting("carriers", "Carriers", "text", default=",".join(config.get("import", "carrier_notices", []) or []),
                        help="e.g. gr"),)

    def run_import(self, ctx):
        from .. import voip
        carriers = [c.strip() for c in (ctx.settings.get("carriers") or "").split(",") if c.strip()]
        if carriers:
            voip.CARRIER_NOTICES = carriers
        return run_importers(ctx, [("app and carrier calls", voip.run)])


PLUGINS = (IphoneBackup, AndroidAdb, ViberDesktop, WhatsappBridge, Telegram, CarrierNotices)
