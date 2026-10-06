"""Who is who: each person's display name, handles and avatar.

Names come from sources: `contacts` (an address book plugin: the contacts that list one of the
person's addresses) and, for each service, the names it has shown for their handles (`handle_name`),
by kind: `<service>/book` (the service's copy of the user's address book), `<service>/chat` (a
chat's name), `<service>/profile` (a name they chose themselves).

A person's name is the one the user gave (`person.name`); else, if the user pinned where it comes
from (`person.name_source`: a source, or one handle), from there; else from the first source that
has one, in the user's order (setting `name_order`), else by the weight the plugins declare
(`Plugin.name_weights`); else any name seen; else their best handle, formatted (a phone number in
international form, an email, @username). Within one source, the name most of their handles share,
then the latest. The user's own addresses (`account`) make up "me".
"""
import json
from collections import defaultdict

import phonenumbers

from .. import config

HANDLE_ORDER = ("phone", "email", "username", "sender", "uri", "name", "id")   # a name where a service gives nothing else, before an id


def name_order(db):
    """The sources of names, most trusted first: the user's order (setting `name_order`), then any
    source they did not place, by the weight its plugins declare."""
    from ..plugins import name_weights
    weights = name_weights()
    row = db.execute("SELECT value FROM setting WHERE key = 'name_order'").fetchone()
    mine = [x for x in (json.loads(row[0]) or [] if row else []) if x in weights]
    return mine + sorted((x for x in weights if x not in mine), key=lambda x: -weights[x])


def pretty_phone(value):
    if not value.startswith("+"):
        return value
    try:
        n = phonenumbers.parse(value, None)
        return phonenumbers.format_number(n, phonenumbers.PhoneNumberFormat.INTERNATIONAL)
    except phonenumbers.NumberParseException:
        return value


def pretty(kind, value):
    if kind == "phone":
        return pretty_phone(value)
    if kind == "username":
        return "@" + value
    return value


def pick(cands):
    """One name of one source: the one most of the person's handles share, then the latest seen.
    cands: [(name, address_id, seen)]."""
    by = defaultdict(lambda: [set(), 0])
    for name, aid, seen in cands:
        by[name][0].add(aid)
        by[name][1] = max(by[name][1], seen or 0)
    return max(by, key=lambda n: (len(by[n][0]), by[n][1])) if by else None


class People:
    """A snapshot of every person's name and handles; build with `people(store)`, which caches it
    until the archive changes."""

    def __init__(self, db):
        self.handles = defaultdict(list)         # person -> [(kind, value, service, address_id)]
        self.person_of = {}                      # address_id -> person
        for aid, pid, kind, value, service in db.execute(
                "SELECT a.id, pa.person_id, k.name, a.value, s.name FROM address a "
                "JOIN person_address pa ON pa.address_id = a.id JOIN address_kind k ON k.id = a.kind_id "
                "LEFT JOIN service s ON s.id = a.service_id"):
            self.handles[pid].append((kind, value, service, aid))
            self.person_of[aid] = pid
        self.own_addresses = {r[0] for r in db.execute("SELECT address_id FROM account")}
        self.me = {self.person_of[a] for a in self.own_addresses if a in self.person_of}
        self.given, self.pinned, self.notes = {}, {}, {}
        for pid, name, source, note in db.execute("SELECT id, name, name_source, note FROM person "
                                                  "WHERE name != '' OR name_source IS NOT NULL OR note IS NOT NULL"):
            if name:
                self.given[pid] = name
            if source:
                self.pinned[pid] = source
            if note is not None:
                self.notes[pid] = note
        # contacts: person -> [(contact id, name, photo, organization, address id, updated)]
        self.contacts = defaultdict(list)
        for pid, cid, name, photo, org, aid, updated in db.execute(
                "SELECT pa.person_id, c.id, c.name, c.photo, c.organization, ca.address_id, c.updated_at "
                "FROM contact_address ca JOIN contact c ON c.id = ca.contact_id "
                "JOIN person_address pa ON pa.address_id = ca.address_id "
                "JOIN plugin_instance i ON i.id = c.instance_id WHERE i.enabled"):
            self.contacts[pid].append((cid, name, photo, org, aid, updated))
        # names services showed: person -> [(source, name, address id, first seen, last seen, current)]
        self.seen = defaultdict(list)
        for pid, service, kind, name, aid, first, last, current in db.execute(
                "SELECT pa.person_id, s.name, h.kind, h.name, h.address_id, h.first_seen, h.last_seen, h.current "
                "FROM handle_name h JOIN person_address pa ON pa.address_id = h.address_id "
                "JOIN service s ON s.id = h.service_id"):
            self.seen[pid].append((f"{service}/{kind}", name, aid, first, last, current))
        self.order = name_order(db)
        self._info = {}

    def _candidates(self, pid, source, address=None):
        if source == "contacts":
            return [(n, a, u) for _, n, _, _, a, u in self.contacts.get(pid, ()) if n and (address is None or a == address)]
        return [(n, a, last) for s, n, a, _, last, cur in self.seen.get(pid, ())
                if cur and s == source and (address is None or a == address)]

    def info(self, pid):
        """(name, source): source is 'user', 'contacts', '<service>/<kind>', or 'handle'."""
        if pid is None:
            return None, None
        if pid in self._info:
            return self._info[pid]
        out = None
        if pid in self.given:
            out = self.given[pid], "user"
        pin = self.pinned.get(pid)
        address = int(pin.split(":", 1)[1]) if pin and pin.startswith("address:") else None
        sources = [pin] if pin and address is None else self.order
        for where in sources if out is None else ():
            name = pick(self._candidates(pid, where, address))
            if name:
                out = name, where
                break
        if out is None:
            name = pick([(n, a, last) for _, n, a, _, last, cur in self.seen.get(pid, ()) if cur])
            if name:
                out = name, next(s for s, n, _, _, _, cur in self.seen[pid] if cur and n == name)
        if out is None:
            hs = self.handles.get(pid, [])
            if hs:
                k, v, _, _ = min(hs, key=lambda h: HANDLE_ORDER.index(h[0]) if h[0] in HANDLE_ORDER else 99)
                out = pretty(k, v), "handle"
            else:
                out = f"#{pid}", "handle"
        self._info[pid] = out
        return out

    def name(self, pid):
        return self.info(pid)[0]

    def self_named(self, pid):
        """Whether the person's name is one they chose themselves (shown marked in groups)."""
        source = self.info(pid)[1]
        return bool(source) and source.endswith("/profile")

    def name_of_address(self, address_id):
        if address_id is None:
            return None
        return self.name(self.person_of.get(address_id))

    def aka(self, pid):
        """Every other name the person has had: [{name, source, first_seen, last_seen, current}],
        the latest first."""
        shown = self.name(pid)
        out = {}
        for s, n, _, first, last, cur in self.seen.get(pid, ()):
            if n != shown:
                o = out.setdefault((n, s), {"name": n, "source": s, "first_seen": first, "last_seen": last, "current": False})
                o["first_seen"], o["last_seen"] = min(o["first_seen"], first), max(o["last_seen"], last)
                o["current"] = o["current"] or bool(cur)
        for _, n, _, _, _, u in self.contacts.get(pid, ()):
            if n and n != shown:
                out.setdefault((n, "contacts"), {"name": n, "source": "contacts", "first_seen": u, "last_seen": u, "current": True})
        return sorted(out.values(), key=lambda o: -o["last_seen"])

    def contact(self, pid):
        """(contact id, name, photo, organization) of the person's contact: the one most of their
        handles are in, then one with a photo, then the latest."""
        cs = self.contacts.get(pid)
        if not cs:
            return None
        count = defaultdict(set)
        for cid, _, _, _, aid, _ in cs:
            count[cid].add(aid)
        best = max(cs, key=lambda c: (len(count[c[0]]), c[2] is not None, c[5]))
        return best[:4]

    def avatar(self, pid):
        """The contact photo's file name in the cache, if the person has one."""
        c = self.contact(pid)
        return c[2] if c and c[2] else None

    def addresses(self, pid):
        return [aid for _, _, _, aid in self.handles.get(pid, [])]

    def describe(self, pid):
        """The person's handles for showing: [{kind, value, service, label}]."""
        return [{"kind": k, "value": v, "service": s, "label": pretty(k, v), "address_id": a}
                for k, v, s, a in sorted(self.handles.get(pid, []),
                                         key=lambda h: HANDLE_ORDER.index(h[0]) if h[0] in HANDLE_ORDER else 99)]


def people(store):
    return store.cached("people", lambda: People(store.read()))


def own_name():
    return config.get("owner", "name")


def name_sources_present(db):
    """The sources of names this archive has: `contacts` with an address book enabled, and each
    service and kind it has names of."""
    out = set()
    if db.execute("SELECT 1 FROM plugin_instance WHERE kind = 'contacts' AND enabled").fetchone():
        out.add("contacts")
    out |= {f"{s}/{k}" for s, k in db.execute(
        "SELECT DISTINCT s.name, h.kind FROM handle_name h JOIN service s ON s.id = h.service_id")}
    return out
