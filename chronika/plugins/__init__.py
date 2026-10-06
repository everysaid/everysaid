"""The plugins Chronika knows, by id, and the instances of them in an archive.

    from chronika import plugins
    plugins.catalog()                  # every plugin's manifest, for the UI
    plugins.get("telegram")            # the class
    plugins.instances(store)           # the instances in this archive
"""
import json
import os
import time

from . import contacts, libraries, sources
from .i18n import tr

REGISTRY = {p.id: p() for p in (*sources.PLUGINS, *libraries.PLUGINS, *contacts.PLUGINS)}
if os.environ.get("CHRONIKA_DEMO"):            # the demo's own source, which "sends" into the demo archive
    from ..demo import DemoSender
    REGISTRY["demo-sender"] = DemoSender.plugin()


def get(plugin_id):
    return REGISTRY.get(plugin_id)


def catalog(lang="en"):
    return [p.manifest(lang) for p in REGISTRY.values()]


def services(lang="en"):
    """How each service looks, as the plugins that bring it declare: {service: {name, color, short, icon, messages}}."""
    out = {}
    for p in REGISTRY.values():
        for sid, info in p.service_info.items():
            out.setdefault(sid, {"messages": True, **info, "name": tr(info["name"], lang)})
    return out


def name_weights():
    """{source of names: weight}: "contacts" (address books) or a service, the highest any plugin
    declares; core/names.py orders names by these unless the user set an order."""
    out = {}
    for p in REGISTRY.values():
        for key, w in p.name_weights.items():
            out[key] = max(w, out.get(key, w))
    return out


NAME_KINDS = {"book": "address book copy", "chat": "chat name", "profile": "chosen by them"}


def name_label(source, lang="en"):
    """'contacts' or '<service>/<kind>' in words."""
    if source == "contacts":
        return tr("Address book", lang)
    service, _, kind = source.partition("/")
    name = services(lang).get(service, {}).get("name", service)
    return f"{name} ({tr(NAME_KINDS[kind], lang)})" if kind in NAME_KINDS else name


def name_sources(lang="en"):
    """The sources of names in the default order, for the UI: [{id, label, weight}]."""
    return [{"id": k, "weight": w, "label": name_label(k, lang)}
            for k, w in sorted(name_weights().items(), key=lambda kw: -kw[1])]


def state_weight(plugin_id, field):
    """How much a plugin's report of a chat's state counts (0: not applied)."""
    p = REGISTRY.get(plugin_id)
    return p.state_weights.get(field, 0) if p else 0


def _row(r):
    keys = ("id", "plugin", "kind", "label", "settings", "state", "enabled", "device_id", "is_default",
            "created_at", "last_run", "last_status")
    return dict(zip(keys, r))


def instances(store, kind=None):
    rows = store.read().execute(
        "SELECT id, plugin, kind, label, settings, state, enabled, device_id, is_default, created_at, last_run, last_status "
        "FROM plugin_instance" + (" WHERE kind = ?" if kind else "") + " ORDER BY kind, id", (kind,) if kind else ())
    return [_row(r) for r in rows]


def instance(store, iid):
    r = store.read().execute(
        "SELECT id, plugin, kind, label, settings, state, enabled, device_id, is_default, created_at, last_run, last_status "
        "FROM plugin_instance WHERE id = ?", (iid,)).fetchone()
    return _row(r) if r else None


def public(row, lang="en"):
    """An instance as the UI sees it: settings without secrets, with its plugin's manifest."""
    p = get(row["plugin"])
    secret_keys = {s.key for s in (p.settings if p else ()) if s.type == "secret"}
    defaults = {s.key: s.default for s in (p.settings if p else ()) if s.default is not None and s.type != "secret"}
    settings = {k: v for k, v in (defaults | json.loads(row["settings"] or "{}")).items() if k not in secret_keys}
    return {"id": row["id"], "plugin": row["plugin"], "kind": row["kind"], "label": row["label"],
            "settings": settings, "enabled": bool(row["enabled"]), "is_default": bool(row["is_default"]),
            "last_run": row["last_run"], "last_status": row["last_status"],
            "known": p is not None, "name": tr(p.name, lang) if p else row["plugin"]}


def create(store, plugin_id, label, settings=None, kind=None):
    p = get(plugin_id)
    if not p:
        raise KeyError(plugin_id)
    settings = {s.key: s.default for s in p.settings if s.default is not None and s.type != "secret"} | (settings or {})
    with store.write() as db:
        first_library = p.kind == "library" and not db.execute(
            "SELECT 1 FROM plugin_instance WHERE kind = 'library'").fetchone()
        return db.execute("INSERT INTO plugin_instance (plugin, kind, label, settings, is_default, created_at) "
                          "VALUES (?, ?, ?, ?, ?, ?)", (plugin_id, p.kind, label, json.dumps(settings),
                                                         int(first_library), int(time.time()))).lastrowid


def update(store, iid, label=None, settings=None, enabled=None, is_default=None):
    with store.write() as db:
        row = db.execute("SELECT settings, kind FROM plugin_instance WHERE id = ?", (iid,)).fetchone()
        if not row:
            raise KeyError(iid)
        if label is not None:
            db.execute("UPDATE plugin_instance SET label = ? WHERE id = ?", (label, iid))
        if settings is not None:
            merged = json.loads(row[0] or "{}") | settings
            db.execute("UPDATE plugin_instance SET settings = ? WHERE id = ?", (json.dumps(merged), iid))
        if enabled is not None:
            db.execute("UPDATE plugin_instance SET enabled = ? WHERE id = ?", (int(bool(enabled)), iid))
        if is_default:
            db.execute("UPDATE plugin_instance SET is_default = (id = ?) WHERE kind = ?", (iid, row[1]))


def remove(store, iid):
    """An instance goes; what it brought stays in the archive (its sources lose their instance)."""
    with store.write() as db:
        db.execute("UPDATE source SET instance_id = NULL WHERE instance_id = ?", (iid,))
        db.execute("UPDATE library_link SET instance_id = NULL WHERE instance_id = ?", (iid,))
        for (cid,) in db.execute("SELECT id FROM contact WHERE instance_id = ?", (iid,)).fetchall():
            db.execute("DELETE FROM contact_address WHERE contact_id = ?", (cid,))
        db.execute("DELETE FROM contact WHERE instance_id = ?", (iid,))
        db.execute("DELETE FROM plugin_instance WHERE id = ?", (iid,))
