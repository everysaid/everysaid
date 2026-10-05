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
import subprocess
import sys
import time
import urllib.request

from .. import config
from ..errors import plugin_error
from .base import Plugin, Setting
from .i18n import tr

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


def run_script(ctx, name, *args, extra_env=None):
    """One of the project's scripts, its output into the log; raises if it fails."""
    path = os.path.join(SCRIPTS, name)
    if not os.path.exists(path):
        raise RuntimeError(f"{name} not found (the scripts come with the project's code)")
    env = dict(os.environ, PYTHONUTF8="1", **(extra_env or {}))
    p = subprocess.Popen([sys.executable, path, *args], stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                         stdin=subprocess.DEVNULL, text=True, encoding="utf-8", env=env)
    for line in p.stdout:
        line = line.rstrip("\r\n").split("\r")[-1]
        if line.strip():
            ctx.log(line)
    if p.wait():
        raise RuntimeError(f"{name}: exit code {p.returncode}")


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
    return {s: LOOKS[s] for s in services}


class IphoneBackup(Plugin):
    id = "iphone-backup"
    name = "iPhone (encrypted backup)"
    services = ("sms", "imessage", "rcs", "phone", "facetime", "whatsapp", "viber")
    service_info = looks(*services)
    name_weights = {"whatsapp/book": 80, "whatsapp/chat": 50, "whatsapp/profile": 30}
    state_weights = {"hidden": 70, "muted": 60}
    description = ("Messages, iMessage, calls, WhatsApp and Viber from an encrypted iPhone backup, made over "
                   "the cable with libimobiledevice. The backup must be encrypted: only then does it hold calls.")
    platforms = ("linux", "darwin")
    needs = ("the phone on a USB cable", "libimobiledevice (idevicebackup2)", "the backup password")
    settings = (
        Setting("udid", "UDID", help="Only with more than one iPhone; otherwise it is found"),
        Setting("backup", "A new backup before importing", "bool", default=True,
                help="Off: only decrypt the backup already there"),
    )
    actions = (("import_only", "Import only (no backup)"),)

    def check(self, ctx):
        if not config.secret("backup-password"):
            return False, "missing: the backup password (scripts/iphone-sync.py --save-password)"
        return True, "ready"

    def _import(self, ctx):
        from .. import calls, media, sms, viber, voip, whatsapp
        return run_importers(ctx, [("SMS, iMessage", sms.run), ("calls", calls.run), ("Viber", viber.run),
                                   ("WhatsApp", whatsapp.run), ("WhatsApp and Viber calls", voip.run),
                                   ("files", media.run)])

    def run_import(self, ctx):
        args = [] if ctx.settings.get("backup", True) else ["--no-backup"]
        run_script(ctx, "iphone-sync.py", *args)
        return self._import(ctx)

    def action(self, ctx, name):
        if name == "import_only":
            return self._import(ctx)
        return super().action(ctx, name)


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
    state_weights = {"hidden": 70, "muted": 60, "pinned": 0}
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
                help="A risk for the account; needs a bridge with /api/send"),
        Setting("interval", "Check every (seconds)", "number", default=10),
    )
    can_send = True

    def sending(self, ctx):
        return bool(ctx.settings.get("send")) and super().sending(ctx)

    def _paths(self, ctx):
        d = ctx.settings.get("store") or ""
        return os.path.join(d, "messages.db"), os.path.join(d, "whatsapp.db")

    def run_import(self, ctx):
        from .. import whatsapp
        bridge, store = self._paths(ctx)
        # only the bridge's databases: an iPhone's are another instance's
        return run_importers(ctx, [("WhatsApp (bridge)", lambda a: whatsapp.run(a, iphone_db=None, contacts_db=None,
                                                                                 bridge_db=bridge, store_db=store))])

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
            raise
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
    state_weights = {"hidden": 70, "muted": 60, "pinned": 0}
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
