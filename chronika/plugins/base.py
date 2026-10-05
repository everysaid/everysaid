"""What every plugin is: a manifest (what it is, what it needs, what it can do) and a few methods.

Kinds: `source` (brings messages, calls, people, media), `library` (where kept pictures and videos
go), `contacts` (an address book). A plugin is code; a `plugin_instance` row is one use of it (one
phone, one account, one folder), with its own settings, secrets and state.

Source plugins implement `run_import(ctx)` (bring what is new, then stop) and, where the service
can be reached live, `live(ctx)` (an async task that stays connected) and `send(ctx, ...)`.
Library plugins: `find(ctx, sha256, path)`, `store(ctx, path, meta)`, `fetch(ctx, ref, size)`.
Contacts plugins: `sync(ctx)`.
"""
from dataclasses import dataclass, field
import json
import sys
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
    options: list = field(default_factory=list)

    def manifest(self, lang="en"):
        return {"key": self.key, "label": tr(self.label, lang), "type": self.type, "required": self.required,
                "default": self.default, "help": tr(self.help, lang), "options": self.options}


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
    actions = ()                # extra buttons: (id, label)
    # How each service it brings looks: {service: {"name", "color", "short", "messages"}} ("messages":
    # False for a service of calls only). Several plugins may bring a service; any of them may say.
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
                "actions": [{"id": a, "label": tr(label, lang)} for a, label in cls.actions],
                "has_chats": cls.chats is not Plugin.chats, "live_default": cls.live_default, "name_weights": dict(cls.name_weights),
                "state_weights": dict(cls.state_weights)}

    # sources
    def run_import(self, ctx):
        raise NotImplementedError

    async def live(self, ctx):
        raise NotImplementedError

    async def send(self, ctx, conversation, text, reply_to=None):
        """conversation: {id, key, service}; reply_to (where can_reply): {key, id} of the message
        answered, in that conversation."""
        raise NotImplementedError

    def chats(self, ctx):
        return []

    def sending(self, ctx):
        """Whether this instance may send now: by default when it can send and is set up; a plugin may
        also make it a setting the user turns on."""
        return self.can_send and self.check(ctx)[0]

    def action(self, ctx, name):
        raise NotImplementedError(name)

    def check(self, ctx):
        """Whether the instance is ready: (ok, message)."""
        missing = [s.label for s in self.settings if s.required and s.type != "secret" and not ctx.settings.get(s.key)]
        missing += [s.label for s in self.settings if s.required and s.type == "secret" and not ctx.secret(s.key)]
        return (not missing, "missing: " + ", ".join(missing) if missing else "ready")


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

    @property
    def store(self):
        return self.host.store

    @property
    def lang(self):
        """The language the user last chose (setting `language`), for words said without a request."""
        return self.store.setting("language") or "en"

    def log(self, text, **params):
        """A line of the instance's log, in English with {params}: said in the user's language."""
        line = tr(str(text), self.lang)
        if params:
            line = line.format(**params)
        self.lines.append((int(time.time()), line))
        del self.lines[:-500]
        self.host.emit({"type": "plugin_log", "instance": self.id, "line": line})

    def secret_name(self, key):
        return f"plugin-{self.id}-{key}"

    def secret(self, key):
        return config.secret(self.secret_name(key))

    def save_secret(self, key, value):
        return config.save_secret(self.secret_name(key), value)

    def save_state(self, **values):
        self.state.update(values)
        with self.store.write() as db:
            db.execute("UPDATE plugin_instance SET state = ? WHERE id = ?", (json.dumps(self.state), self.id))

    def emit(self, event):
        self.host.emit(event)
