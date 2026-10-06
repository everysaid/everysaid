"""What every plugin is: a manifest (what it is, what it needs, what it can do) and a few methods.

Kinds: `source` (brings messages, calls, people, media), `library` (where kept pictures and videos
go), `contacts` (an address book). A plugin is code; a `plugin_instance` row is one use of it (one
phone, one account, one folder), with its own settings, secrets and state.

Source plugins implement `run_import(ctx)` (bring what is new, then stop) and, where the service
can be reached live, `live(ctx)` (an async task that stays connected) and `send(ctx, ...)`.
Library plugins: `find(ctx, sha256, path)`, `store(ctx, path, meta)`, `fetch(ctx, ref, size)`.
Contacts plugins: `sync(ctx)`.
"""
from contextlib import contextmanager
from dataclasses import dataclass, field
import json
import os
import sys
import threading
import time

from .. import config
from .i18n import tr


@dataclass
class Setting:
    key: str
    label: str
    type: str = "text"          # text, path, url, number, bool, secret, select
    required: bool = False
    default: object = None
    help: str = ""
    options: list = field(default_factory=list)     # for select: [(value, label)]
    pattern: str = ""                               # what a value must look like (a regex), if anything
    keeps: dict = field(default_factory=dict)       # select: {option: (secret key, label)}: choosing it asks
                                                    # for that secret, kept; leaving it takes the secret away

    def valid(self, value):
        import re
        return not self.pattern or value in (None, "") or bool(re.fullmatch(self.pattern, str(value)))

    def manifest(self, lang="en"):
        return {"key": self.key, "label": tr(self.label, lang), "type": self.type, "required": self.required,
                "default": self.default, "help": tr(self.help, lang),
                "options": [{"value": v, "label": tr(label, lang)} for v, label in self.options],
                "keeps": {o: {"key": k, "label": tr(label, lang)} for o, (k, label) in self.keeps.items()}}


class Plugin:
    id = ""
    name = ""
    kind = "source"
    services = ()
    description = ""
    modes = ("import",)         # import, live
    live_default = False        # with "live": connect on its own once set up (the user can turn it off)
    platforms = ("linux", "darwin", "win32")
    needs = ()                  # plain words for the user: "a cable", "libimobiledevice", "a login"
    settings = ()               # Setting(...)
    can_send = False
    can_reply = False           # can send an answer to a given message (quoting it)
    can_mention = False         # can name people of a group in what it sends (@)
    can_mark_read = False       # can tell the service a chat was read (read receipts), where the user allows
    can_send_files = False      # can send a file (a picture, a video, a document) with a caption
    actions = ()                # extra buttons: (id, label)
    # How each service it brings looks: {service: {"name", "color", "short", "icon", "messages"}}
    # ("icon": an SVG path on a 24x24 view, drawn in the colour, where the service is chosen or shown;
    # "messages": False for a service of calls only). Several plugins may bring a service; any may say.
    service_info = {}
    # The names it brings for people, and how much they are trusted by default: {"<service>/<kind>":
    # weight} (kind: book, its copy of the user's address book; chat, a chat's name; profile, chosen
    # by them), or {"contacts": weight} for an address book. The user's order (Settings) overrides.
    name_weights = {}
    # The state of chats it reports (muted, pinned, read_until; and archived, which only starts the
    # app's own: see Archive.init_archived), and how much it counts
    # against other services by default ({field: weight}; 0: shown, not applied).
    state_weights = {}

    @classmethod
    def manifest(cls, lang="en"):
        return {"id": cls.id, "name": tr(cls.name, lang), "kind": cls.kind, "services": list(cls.services),
                "description": tr(cls.description, lang), "modes": list(cls.modes), "platforms": list(cls.platforms),
                "available": sys.platform in cls.platforms, "needs": [tr(n, lang) for n in cls.needs],
                "settings": [s.manifest(lang) for s in cls.settings], "can_send": cls.can_send, "can_reply": cls.can_reply,
                "can_mention": cls.can_mention, "can_mark_read": cls.can_mark_read, "can_send_files": cls.can_send_files,
                "actions": [{"id": a, "label": tr(label, lang)} for a, label in cls.actions],
                "has_chats": cls.chats is not Plugin.chats, "live_default": cls.live_default, "name_weights": dict(cls.name_weights),
                "state_weights": dict(cls.state_weights)}

    # sources
    def run_import(self, ctx):
        raise NotImplementedError

    async def live(self, ctx):
        raise NotImplementedError

    async def send(self, ctx, conversation, text, reply_to=None, mentions=None, file=None):
        """conversation: {id, key, service}; reply_to (where can_reply): {key, id} of the message
        answered, in that conversation; mentions (where can_mention): [{start, length, address_id}],
        the people of a group the text names, each where it is in the text (in characters, e.g.
        "@name"), for the plugin to write as its service does; file (where can_send_files):
        {data: bytes, filename, mime_type}, with the text as its caption (which may be empty)."""
        raise NotImplementedError

    async def mark_read(self, ctx, conversation, until):
        """The user read the conversation ({id, key, service}) up to `until` (Unix ms) in the app:
        tell the service (read receipts), if the instance's settings allow. Returns how many messages
        were marked."""
        return 0

    def chats(self, ctx):
        return []

    def asks(self, ctx):
        """What it needs typed in for each run, kept nowhere: [(key, label)] (e.g. a password the
        user chose not to store). The run gets them in ctx.given."""
        return []

    def info(self, ctx):
        """A few facts to show on its card: [(label, value)] (e.g. where its backup is, of when)."""
        return []

    def sending(self, ctx):
        """Whether this instance may send now."""
        return self.can_send and not self.not_sending(ctx)

    def not_sending(self, ctx):
        """Why this instance may not send now, in a few words for the user ("" when it may): by default
        when it is not set up; a plugin may add its own reasons (a setting the user turns on)."""
        ok, why = self.check(ctx)
        return "" if ok else why

    def action(self, ctx, name):
        raise NotImplementedError(name)

    def check(self, ctx):
        """Whether the instance is ready: (ok, message)."""
        missing = [s.label for s in self.settings if s.required and s.type != "secret" and not ctx.settings.get(s.key)]
        missing += [s.label for s in self.settings if s.required and s.type == "secret" and not ctx.secret(s.key)]
        return (not missing, "missing: " + ", ".join(missing) if missing else "ready")


_run = threading.local()        # the log file of the run going on in this thread, if any


@contextmanager
def run_log(instance_id, what):
    """While it lasts, the instance's log lines in this thread go to a file of this run of their own:
    <logs>/plugin-<id>/<date>-<time>-<what>.log (else to the day's file, as a live connection's do)."""
    folder = os.path.join(config.LOGS, f"plugin-{instance_id}")
    os.makedirs(folder, exist_ok=True)
    _run.path = os.path.join(folder, f"{time.strftime('%Y%m%d-%H%M%S')}-{what}.log")
    try:
        yield _run.path
    finally:
        _run.path = None


class Context:
    """One plugin instance at work: its settings, secrets and state, the archive, and a log."""

    def __init__(self, host, row):
        self.host = host
        self.id, self.plugin_id, self.kind, self.label = row["id"], row["plugin"], row["kind"], row["label"]
        from . import get
        p = get(self.plugin_id)
        defaults = {x.key: x.default for x in (p.settings if p else ()) if x.default is not None and x.type != "secret"}
        self.settings = defaults | json.loads(row["settings"] or "{}")     # what is not set: the plugin's default
        self.state = json.loads(row["state"] or "{}")
        self.device_id = row.get("device_id")
        self.lines = []
        self.bar = ""                   # the line a progress bar draws again and again, under the lines
        self.given = {}                 # what the user typed in for this run (asks()): never stored

    @property
    def store(self):
        return self.host.store

    @property
    def lang(self):
        """The language the user last chose (setting `language`), for words said without a request."""
        return self.store.setting("language") or "en"

    def log(self, text, redrawn=False, **params):
        """A line of the instance's log, in English with {params}: said in the user's language. The
        last lines are kept for the interface; every line goes to the log files (see run_log).
        redrawn: a progress bar's line as it ended, which stays the one bar line of the interface."""
        line = tr(str(text), self.lang)
        if params:
            line = line.format(**params)
        path = getattr(_run, "path", None) or os.path.join(config.LOGS, f"plugin-{self.id}", f"{time.strftime('%Y%m%d')}-live.log")
        try:
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "a", encoding="utf-8") as f:
                stamp = time.strftime("%Y-%m-%d %H:%M:%S")
                f.write("".join(f"{stamp} {part}\n" for part in line.splitlines() or [""]))
        except OSError:
            pass                        # a full disk must not stop an import
        if redrawn:
            self.progress(line)
            return
        self.lines.append((int(time.time()), line))
        del self.lines[:-500]
        self.host.emit({"type": "plugin_log", "instance": self.id, "line": line})

    def progress(self, line):
        """A line being drawn again (a progress bar): shown in place as it changes, not in the log."""
        self.bar = line
        self.host.emit({"type": "plugin_progress", "instance": self.id, "line": line})

    def secret_name(self, key):
        return f"plugin-{self.id}-{key}"

    def secret(self, key):
        return config.secret(self.secret_name(key))

    def save_secret(self, key, value):
        return config.save_secret(self.secret_name(key), value)

    def delete_secret(self, key):
        config.delete_secret(self.secret_name(key))

    def save_state(self, **values):
        self.state.update(values)
        with self.store.write() as db:
            db.execute("UPDATE plugin_instance SET state = ? WHERE id = ?", (json.dumps(self.state), self.id))

    def emit(self, event):
        self.host.emit(event)
