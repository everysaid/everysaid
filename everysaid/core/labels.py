"""Labels: the words people are described by, the tone of their chats (many to a person) and who
they are to the owner (one), and the names found for people no source names.

The lists are the user's: the app starts them with a few (`archive.LABELS`, their words in the
app's languages), and the user renames, adds, merges and removes them. A label's meaning is what
the local models read to judge by; one without a meaning ('') is only given by the user.

A person's label is the user's (yes, or no: never suggested again) or the local models' (suggested,
with their votes and a line of the chat that shows it). The models' never touch the user's.

A name for someone without one comes from the local models (`name_guess` 'models') or from their
handles, read here without a model ('handle': "first.last@..." is "First Last" where
the archive knows those names). Either is shown to the user, never applied by itself.
"""
from collections import Counter, defaultdict
import hashlib
import json
import re
import time

from .. import archive, text as text_mod
from ..errors import UserError
from .names import own_name, people
from .queries import _active, _chat_index, _skeleton, _unnamed

KINDS = ("tone", "relation")
DEFAULT_MEANING = {key: meaning for _, key, meaning, _ in archive.LABELS}


# --- the lists -----------------------------------------------------------------------------------

def labels(store, kind=None):
    """[{id, kind, key, name, meaning, default_meaning, sensitive, uses: {yes, suggested}}], in the
    user's order. name: the user's (None for one the app brings, said in the app's words by key);
    meaning: what the models read (None: the app's own, default_meaning)."""
    db = store.read()
    uses = defaultdict(Counter)
    for lid, state, n in db.execute("SELECT label_id, state, count(*) FROM person_label GROUP BY 1, 2"):
        uses[lid][state] = n
    return [{"id": i, "kind": k, "key": key, "name": name, "meaning": meaning,
             "default_meaning": DEFAULT_MEANING.get(key), "sensitive": bool(sens),
             "uses": {"yes": uses[i]["yes"], "suggested": uses[i]["suggested"]}}
            for i, k, key, name, meaning, sens in db.execute(
                "SELECT id, kind, key, name, meaning, sensitive FROM label" + (" WHERE kind = ?" if kind else "")
                + " ORDER BY kind DESC, position, id", (kind,) if kind else ())]


def for_models(store, kind):
    """The labels the models judge by: [(id, word, meaning, sensitive)], the word being the app's
    key or the user's name."""
    out = []
    for lb in labels(store, kind):
        meaning = lb["default_meaning"] if lb["meaning"] is None else lb["meaning"]
        if meaning:
            out.append((lb["id"], lb["key"] or lb["name"], meaning, lb["sensitive"]))
    return out


def digest(store):
    """What the lists the models judge by are now: another digest, another list."""
    words = [for_models(store, k) for k in KINDS]
    return hashlib.sha1(json.dumps(words, ensure_ascii=False).encode()).hexdigest()[:12]


def add_label(store, kind, name, meaning="", sensitive=False):
    if kind not in KINDS:
        raise ValueError(kind)
    name = (name or "").strip()
    if not name:
        raise UserError("labels.no_name")
    with store.write() as db:
        if any(n and text_mod.fold(n) == text_mod.fold(name) for (n,) in db.execute(
                "SELECT name FROM label WHERE kind = ?", (kind,))):
            raise UserError("labels.exists")
        last = db.execute("SELECT max(position) FROM label").fetchone()[0] or 0
        return db.execute("INSERT INTO label (kind, name, meaning, sensitive, position) VALUES (?, ?, ?, ?, ?)",
                          (kind, name, (meaning or "").strip(), int(bool(sensitive)), last + 1)).lastrowid


_UNSET = object()


def edit_label(store, label_id, name=_UNSET, meaning=_UNSET, sensitive=_UNSET):
    """name None or '': back to the app's words (for one it brings); meaning None: back to the app's."""
    with store.write() as db:
        row = db.execute("SELECT key FROM label WHERE id = ?", (label_id,)).fetchone()
        if not row:
            raise KeyError(label_id)
        if name is not _UNSET:
            name = (name or "").strip() or None
            if not name and not row[0]:
                raise UserError("labels.no_name")
            db.execute("UPDATE label SET name = ? WHERE id = ?", (name, label_id))
        if meaning is not _UNSET:
            db.execute("UPDATE label SET meaning = ? WHERE id = ?",
                       (None if meaning is None and row[0] else (meaning or "").strip(), label_id))
        if sensitive is not _UNSET:
            db.execute("UPDATE label SET sensitive = ? WHERE id = ?", (int(bool(sensitive)), label_id))


def order_labels(store, ids):
    with store.write() as db:
        db.executemany("UPDATE label SET position = ? WHERE id = ?", [(i, int(x)) for i, x in enumerate(ids)])


def remove_label(store, label_id):
    """The label goes, and with it every person's (the user's too: the UI asks first)."""
    with store.write() as db:
        if not db.execute("DELETE FROM label WHERE id = ?", (label_id,)).rowcount:
            raise KeyError(label_id)


def merge_labels(store, into, other):
    """`other` becomes `into`: the people it was given to have `into` (the user's word over the
    models'), and it goes."""
    if into == other:
        raise UserError("labels.same")
    with store.write() as db:
        kinds = dict(db.execute("SELECT id, kind FROM label WHERE id IN (?, ?)", (into, other)))
        if len(kinds) != 2:
            raise KeyError(other if into in kinds else into)
        if kinds[into] != kinds[other]:
            raise UserError("labels.other_kind")
        _move_labels(db, "label_id", into, other)
        db.execute("DELETE FROM label WHERE id = ?", (other,))


def _move_labels(db, column, into, other):
    """person_label rows of `other` (a label or a person, by `column`) to `into`: where both have
    one, the user's word wins over the models', else the one there stays."""
    who = "person_id, ?" if column == "label_id" else "?, label_id"
    db.execute(f"INSERT INTO person_label (person_id, label_id, state, votes, models, evidence, at) "
               f"SELECT {who}, state, votes, models, evidence, at FROM person_label WHERE {column} = ? "
               "ON CONFLICT (person_id, label_id) DO UPDATE SET state = excluded.state, votes = excluded.votes, "
               "models = excluded.models, evidence = excluded.evidence, at = excluded.at "
               "WHERE person_label.state = 'suggested' AND excluded.state != 'suggested'", (into, other))
    db.execute(f"DELETE FROM person_label WHERE {column} = ?", (other,))


def moved_person(db, into, other):
    """When the person `other` becomes part of `into` (inside the merge's transaction): their
    labels go over, a name found for them too where `into` has none, and both are to be read again
    (their chat is now one)."""
    _move_labels(db, "person_id", into, other)
    db.execute("UPDATE OR IGNORE name_guess SET person_id = ? WHERE person_id = ?", (into, other))
    db.execute("DELETE FROM name_guess WHERE person_id = ?", (other,))
    db.execute("DELETE FROM analysis WHERE person_id IN (?, ?)", (into, other))


# --- a person's ----------------------------------------------------------------------------------

def person_labels(store, person_id, suggested=True):
    """[{id, kind, key, name, sensitive, state, votes, models, evidence}]: the user's, and the
    models' unless `suggested` is False; those the user said no to are left out."""
    return [{"id": i, "kind": k, "key": key, "name": name, "sensitive": bool(sens), "state": state,
             "votes": votes, "models": models, "evidence": ev}
            for i, k, key, name, sens, state, votes, models, ev in store.read().execute(
                "SELECT l.id, l.kind, l.key, l.name, l.sensitive, pl.state, pl.votes, pl.models, pl.evidence "
                "FROM person_label pl JOIN label l ON l.id = pl.label_id WHERE pl.person_id = ? AND pl.state != 'no' "
                + ("" if suggested else "AND pl.state = 'yes' ") + "ORDER BY l.kind DESC, l.position, l.id", (person_id,))]


def by_person(store, suggested=True):
    """Every person's labels at once, for lists: {person id: [{id, kind, key, name, state}]}, the
    user's and (unless `suggested` is False) the models'."""
    out = defaultdict(list)
    for pid, i, k, key, name, state in store.read().execute(
            "SELECT pl.person_id, l.id, l.kind, l.key, l.name, pl.state FROM person_label pl JOIN label l ON l.id = pl.label_id "
            "WHERE pl.state = 'yes'" + (" OR pl.state = 'suggested'" if suggested else "")
            + " ORDER BY l.kind DESC, l.position, l.id"):
        out[pid].append({"id": i, "kind": k, "key": key, "name": name, "state": state})
    return out


def set_person_label(store, person_id, label_id, state):
    """The user's word on a label of a person: 'yes' (it is so: one relation only), 'no' (it is not:
    the models do not suggest it again), or None (no word: gone, the models may suggest it)."""
    if state not in ("yes", "no", None):
        raise ValueError(state)
    with store.write() as db:
        if not db.execute("SELECT 1 FROM person WHERE id = ?", (person_id,)).fetchone():
            raise KeyError(person_id)
        row = db.execute("SELECT kind FROM label WHERE id = ?", (label_id,)).fetchone()
        if not row:
            raise KeyError(label_id)
        if state is None:
            db.execute("DELETE FROM person_label WHERE person_id = ? AND label_id = ?", (person_id, label_id))
            return
        if state == "yes" and row[0] == "relation":       # one relation: the others it was are no more
            db.execute("DELETE FROM person_label WHERE person_id = ? AND state = 'yes' AND label_id IN "
                       "(SELECT id FROM label WHERE kind = 'relation')", (person_id,))
        db.execute("INSERT INTO person_label (person_id, label_id, state, at) VALUES (?, ?, ?, ?) "
                   "ON CONFLICT (person_id, label_id) DO UPDATE SET state = excluded.state, at = excluded.at",
                   (person_id, label_id, state, int(time.time())))


def analysed(store, person_id):
    """When the local models last read the person's chat, and how many messages it had: {at, messages} or None."""
    row = store.read().execute("SELECT at, messages FROM analysis WHERE person_id = ?", (person_id,)).fetchone()
    return {"at": row[0], "messages": row[1]} if row else None


def analyse_again(store, person_id):
    """The person's chat to be read again by the local analysis, next time it runs."""
    with store.write() as db:
        db.execute("DELETE FROM analysis WHERE person_id = ?", (person_id,))


def forget_analysis(store):
    """Everything the models said goes (their labels, their names, what they read); the user's
    word stays (their labels, and the names they turned down)."""
    with store.write() as db:
        n = db.execute("DELETE FROM person_label WHERE state = 'suggested'").rowcount
        n += db.execute("DELETE FROM name_guess WHERE how = 'models' AND NOT dismissed").rowcount
        db.execute("DELETE FROM analysis")
    return n


# --- names for people without one ----------------------------------------------------------------

def _key(word):
    """One word as it sounds, the ending's s off: a Greek first name, its Latin spelling and its other case are one."""
    sk = _skeleton(word)
    return sk[:-1] if len(sk) > 3 and sk.endswith("s") else sk


def _known_names(store):
    """The names of the people the archive names: ({sound of a first name: its usual spelling},
    {sound of a surname: its usual spelling}), the owner's left out."""
    def build():
        ppl = people(store)
        own = {_key(w) for w in (own_name() or "").split()}
        firsts, lasts = defaultdict(Counter), defaultdict(Counter)
        for pid in ppl.handles:
            name, source = ppl.info(pid)
            if pid in ppl.me or source == "handle":
                continue
            words = [w for w in re.split(r"[\s,]+", name) if w.isalpha() and len(w) > 2]
            if not words:
                continue
            firsts[_key(words[0])][words[0]] += 1
            if len(words) > 1:
                lasts[_key(words[-1])][words[-1]] += 1
        pick = lambda d: {k: c.most_common(1)[0][0] for k, c in d.items() if k not in own}   # noqa: E731
        return pick(firsts), pick(lasts)
    return store.cached("known_names", build)


def _words(handle):
    """The words of a handle: first.last, FirstL_82, last-first."""
    out = []
    for part in re.split(r"[^A-Za-z]+", handle):
        out += re.findall(r"[A-Z]?[a-z]+|[A-Z]+(?![a-z])", part)
    return [w.lower() for w in out if len(w) > 2]


def from_handles(store, person_id):
    """A name read from the person's handles (an email's or a username's words) where one is a
    first name the archive knows: (name, the handle it is read from) or None."""
    firsts, lasts = _known_names(store)
    for kind, value, _, _ in people(store).handles.get(person_id, ()):
        if kind not in ("email", "username"):
            continue
        words = _words(value.split("@")[0])
        first = next((w for w in words if _key(w) in firsts), None)
        if not first:
            continue
        rest = [w for w in words if w != first and w.isalpha() and len(w) > 3]
        last = next((w for w in rest if _key(w) in lasts), None)
        if last:
            return f"{firsts[_key(first)]} {lasts[_key(last)]}", value
        if rest:            # a surname the archive does not know: both as written
            return f"{first.title()} {rest[0].title()}", value
        return firsts[_key(first)], value
    return None


def guess(store, person_id):
    """The name suggested for a person without one, or None: the models' (when they agree), else
    one read from their handles; one the user turned down is not suggested again.
    {name, how, votes, models, evidence}"""
    if person_id not in _unnamed(store)[0]:
        return None
    rows = {how: (name, votes, models, ev, dismissed) for how, name, votes, models, ev, dismissed in store.read().execute(
        "SELECT how, name, votes, models, evidence, dismissed FROM name_guess WHERE person_id = ?", (person_id,))}
    m = rows.get("models")
    if m and not m[4]:
        return {"name": m[0], "how": "models", "votes": m[1], "models": m[2], "evidence": m[3]}
    h = from_handles(store, person_id)
    if h and not (rows.get("handle") and rows["handle"][4] and rows["handle"][0] == h[0]):
        return {"name": h[0], "how": "handle", "votes": None, "models": None, "evidence": h[1]}
    return None


def decide_guess(store, person_id, how, accept):
    """The user's word on a suggested name: accepted, it is their name; turned down, it is not
    suggested again (until another name is found)."""
    g = guess(store, person_id)
    if not g or g["how"] != how:
        raise UserError("people.no_guess")
    with store.write() as db:
        if accept:
            db.execute("UPDATE person SET name = ? WHERE id = ?", (g["name"], person_id))
        db.execute("INSERT INTO name_guess (person_id, how, name, votes, models, evidence, dismissed, at) "
                   "VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (person_id, how) DO UPDATE SET "
                   "name = excluded.name, dismissed = excluded.dismissed, at = excluded.at",
                   (person_id, how, g["name"], g["votes"], g["models"], g["evidence"], int(not accept), int(time.time())))
    return g["name"]


def guessed(store):
    """The people without a name who have a suggested one."""
    def build():
        return {pid for pid in _unnamed(store)[0] & _active(store) if guess(store, pid)}
    return store.cached("guessed", build)


# --- what the local analysis reads ---------------------------------------------------------------

def to_analyse(store, only_unnamed=True, min_messages=20):
    """The people whose chats the local analysis has yet to read, the largest first: those it has
    not read, and those whose chat has grown by half since. [(person id, messages)]"""
    index, _ = _chat_index(store)
    per_conv = store.cached("conversation_sizes", lambda: dict(store.read().execute(
        "SELECT conversation_id, count(*) FROM message GROUP BY conversation_id")))
    done = dict(store.read().execute("SELECT person_id, messages FROM analysis"))
    ppl = people(store)
    who = _unnamed(store)[0] if only_unnamed else set(ppl.handles) - ppl.me
    out = []
    for pid in who & _active(store):
        c = index.get(f"p{pid}")
        n = sum(per_conv.get(cid, 0) for cid in c["conversations"]) if c else 0
        if n >= min_messages and (pid not in done or n >= done[pid] * 1.5):
            out.append((pid, n))
    out.sort(key=lambda x: -x[1])
    return out


def messages_of(store, person_id):
    """How many messages a person's chat has."""
    c = _chat_index(store)[0].get(f"p{person_id}")
    if not c or not c["conversations"]:
        return 0
    q = ",".join("?" * len(c["conversations"]))
    return store.read().execute(f"SELECT count(*) FROM message WHERE conversation_id IN ({q})", c["conversations"]).fetchone()[0]


def save_analysis(store, person_id, messages, models, name=None, tones=(), relation=None):
    """What the models made of a person's chat: name (name, votes, of, evidence) or None; tones and
    relation [(label id, votes, of, evidence)]. Their earlier suggestions for the person go; the
    user's word stays, and a label they said no to is not suggested."""
    now = int(time.time())
    with store.write() as db:
        if not db.execute("SELECT 1 FROM person WHERE id = ?", (person_id,)).fetchone():
            return                      # merged away while it was read
        db.execute("DELETE FROM person_label WHERE person_id = ? AND state = 'suggested'", (person_id,))
        if relation and db.execute("SELECT 1 FROM person_label pl JOIN label l ON l.id = pl.label_id "
                                   "WHERE pl.person_id = ? AND pl.state = 'yes' AND l.kind = 'relation'",
                                   (person_id,)).fetchone():
            relation = None             # the user has said who they are
        for lid, votes, of, ev in [*tones, *([relation] if relation else [])]:
            db.execute("INSERT OR IGNORE INTO person_label (person_id, label_id, state, votes, models, evidence, at) "
                       "VALUES (?, ?, 'suggested', ?, ?, ?, ?)", (person_id, lid, votes, of, ev, now))
        old = db.execute("SELECT name, dismissed FROM name_guess WHERE person_id = ? AND how = 'models'", (person_id,)).fetchone()
        if name and not (old and old[1] and _key(old[0]) == _key(name[0])):     # not one turned down again
            db.execute("INSERT OR REPLACE INTO name_guess (person_id, how, name, votes, models, evidence, dismissed, at) "
                       "VALUES (?, 'models', ?, ?, ?, ?, 0, ?)", (person_id, *name, now))
        elif not name and old and not old[1]:
            db.execute("DELETE FROM name_guess WHERE person_id = ? AND how = 'models'", (person_id,))
        db.execute("INSERT OR REPLACE INTO analysis VALUES (?, ?, ?, ?, ?)",
                   (person_id, messages, digest(store), ",".join(models), now))


def stale(store):
    """How many people were read by other lists of labels than today's."""
    return store.read().execute("SELECT count(*) FROM analysis WHERE labels != ?", (digest(store),)).fetchone()[0]


def judge_again(store):
    """The people read by other lists of labels, to be read again."""
    with store.write() as db:
        return db.execute("DELETE FROM analysis WHERE labels != ?", (digest(store),)).rowcount
