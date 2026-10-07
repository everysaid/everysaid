"""Analysis plugins: local models read the archive and suggest what no source says.

The one here asks models served by Ollama on this computer (or its own network: nothing goes
further) to read a little of each chat of a person without a name, and say their name, with the
line that shows it, the chat's tone and who the person is to the owner, from the user's lists of
labels (core/labels.py). With two or three models they vote. Nothing is applied: what they say is a
suggestion the user accepts or turns down.

It works in the background while it is turned on, the largest chats first, and reads a chat again
when it has grown by half; "judge again" reads again those read by another list of labels.
"""
import asyncio
from collections import Counter
import ipaddress
import json
import random
import re
import socket
import urllib.parse
from datetime import datetime

from .. import text as text_mod
from ..core import labels as labels_mod
from ..core.names import own_name, people
from ..core.queries import _chat_index
from .base import Plugin, Setting

PROMPT = """You read part of a private chat archive. The archive's owner is {owner}; lines marked ME are
the owner's, lines marked THEM are the other person's.
{asks}
Their handles: {handles}
Names they have shown: {aka}

How others name them in groups: {mentions}

Messages:
{lines}"""
ASK_NAME = """Find the OTHER person's real name (first name, and surname if the texts give it): how the owner
calls them at the start of a message, how they sign or introduce themselves, how others name them in
groups, or a handle that is clearly a name. Give it in the nominative, as written in the chat's
language. The owner's own name is never the answer. If no line shows their name, name is null: do
not guess, do not invent, a common name that does not appear in the lines is wrong. evidence: the
exact short phrase, copied from the lines, that shows it.
"""
ASK_TONE = """Judge the chat: its tone (one or more of the list, the main one first) and who the person is to
the owner (relationship, one of the list). A tone marked sensitive only when lines clearly show it,
and then copy one such line, exactly, into sensitive_evidence (else null).
Tones:
{tones}
Relationships:
{relations}
"""


def local_address(url):
    """Whether the URL's host is this computer or on its own network (a private address)."""
    host = urllib.parse.urlparse(url).hostname
    if not host:
        return False
    if host == "localhost" or host.endswith(".local"):
        return True
    try:
        addrs = {ipaddress.ip_address(host)}
    except ValueError:
        try:
            addrs = {ipaddress.ip_address(a[4][0]) for a in socket.getaddrinfo(host, None)}
        except OSError:
            return False
    return all(a.is_loopback or a.is_private or a.is_link_local for a in addrs)


def excerpt(store, pid, known, n_even=25, n_named=30, n_first=10):
    """What the models read of a chat: its first lines, lines spread over all of it, and lines that
    name someone (a first name the archive knows, or the owner's openings: "Hey" and a name). [(ts, outgoing, text)]"""
    c = _chat_index(store)[0].get(f"p{pid}")
    if not c or not c["conversations"]:
        return []
    q = ",".join("?" * len(c["conversations"]))
    rows = store.read().execute(f"SELECT ts, outgoing, text FROM message WHERE conversation_id IN ({q}) "
                                f"AND text IS NOT NULL AND text != '' ORDER BY ts", c["conversations"]).fetchall()
    if not rows:
        return []
    chosen = set(range(min(n_first, len(rows))))
    chosen |= set(range(0, len(rows), max(1, len(rows) // n_even)))
    named = [i for i, r in enumerate(rows)
             if any(labels_mod._key(w) in known for w in re.findall(r"[^\W\d_]{3,}", r[2][:300]))]
    opening = [i for i, r in enumerate(rows) if r[1] and re.match(r"^\W*\w+[ ,!]+[^\W\d_]", r[2]) and
               (re.match(r"^\W*\w+[ ,!]+(\w)", r[2]).group(1).isupper())]
    rnd = random.Random(pid)
    rnd.shuffle(named)
    rnd.shuffle(opening)
    chosen |= set(named[:n_named]) | set(opening[:15])
    return [rows[i] for i in sorted(chosen)]


def mentions(store, pid, n=10):
    """How others name them in groups: texts that @mention one of their handles."""
    addrs = people(store).addresses(pid)
    if not addrs:
        return []
    q = ",".join("?" * len(addrs))
    return [t for (t,) in store.read().execute(
        f"SELECT m.text FROM mention x JOIN message m ON m.id = x.message_id WHERE x.address_id IN ({q}) "
        f"AND m.text IS NOT NULL LIMIT ?", (*addrs, n))]


def shown(name, text):
    """Whether every word of the name (less its ending, for its other cases) is in the text."""
    t = text_mod.fold(text)
    return all(text_mod.fold(w)[:max(3, len(w) - 2)] in t for w in name.split())


def copied(line, text):
    """Whether a line the model says it copied is in the text (a few words at least)."""
    line = " ".join(text_mod.fold(line or "").split())
    return len(line) >= 8 and line in " ".join(text_mod.fold(text).split())


def a_name(name, handles, own):
    """A model's name, if it can be one: not an email, a handle or a number, not the owner's."""
    if not isinstance(name, str):
        return None
    name = " ".join(name.split()).strip(" .,:;\"'")
    if not name or text_mod.fold(name) in ("null", "none", "unknown") or len(name) > 60 or len(name.split()) > 4:
        return None
    if re.search(r"[@\d_/\\]", name) or any(text_mod.fold(name) == text_mod.fold(h) for h in handles):
        return None
    if all(labels_mod._key(w) in own for w in name.split()):
        return None
    return name


def vote(answers, text, handles, own, tones, relations):
    """The models' answers made one: (name, tones, relation) as core.labels.save_analysis takes them.
    A name counts where its words are in what they read, the same name in another case,
    and with a surname or without as one; the most said wins.
    A tone needs most of the models (a sensitive one, two at least, each with a line it copied); a
    relation, most of them."""
    n = len(answers)
    most = n // 2 + 1
    names = {}
    for a in answers:
        name = a_name(a.get("name"), handles, own)
        if name and shown(name, text):
            names.setdefault(labels_mod._key(name.split()[0]), []).append((name, a.get("evidence")))
    name = None
    if names:
        group = max(names.values(), key=len)
        forms = Counter(nm for nm, _ in group)
        best = max(forms, key=lambda f: (forms[f], len(f.split()), f.endswith(("s", chr(0x3c2)))))
        evidence = next((e for nm, e in group if nm == best and isinstance(e, str)), None)
        name = (best, len(group), n, (evidence or "")[:200] or None)
    by_word = {w: (i, sens) for i, w, _, sens in tones}
    counted, lines = Counter(), {}
    for a in answers:
        for w in dict.fromkeys(a.get("tone") or []):
            if w not in by_word:
                continue
            i, sens = by_word[w]
            if sens:
                line = a.get("sensitive_evidence")
                if not (isinstance(line, str) and copied(line, text)):
                    continue
                lines.setdefault(i, line[:200])
            counted[i] += 1
    sensitive = {i for i, _, _, s in tones if s}
    chosen = [(i, v, n, lines.get(i)) for i, v in counted.most_common() if v >= (max(2, most) if i in sensitive else most)]
    rel_ids = {w: i for i, w, _, _ in relations}
    rel = Counter(rel_ids[a["relationship"]] for a in answers if a.get("relationship") in rel_ids)
    relation = None
    if rel:
        i, v = rel.most_common(1)[0]
        if v >= most:
            relation = (i, v, n, None)
    return name, chosen, relation


class Ollama(Plugin):
    id = "ollama"
    name = "Local analysis (Ollama)"
    kind = "analysis"
    description = ("Local models read the chats of people without a name and suggest who they are, and the "
                   "chats' tone. Nothing leaves this computer and its network; nothing is applied without you.")
    modes = ("live",)
    needs = ("Ollama, with a model",)
    settings = (
        Setting("url", "Ollama's address", "url", required=True, default="http://localhost:11434",
                help="Only on this computer or its own network"),
        Setting("models", "Models", "text", required=True, default="qwen3:14b",
                help="One, or two or three separated by commas, to vote: e.g. qwen3:14b, gemma3:12b"),
        Setting("what", "What they look for", "select", default="both",
                options=[("both", "A name and the tone"), ("name", "A name"), ("tone", "The tone")]),
        Setting("who", "Whose chats", "select", default="unnamed",
                options=[("unnamed", "Those without a name"), ("all", "Everyone's")]),
        Setting("min_messages", "Messages at least", "number", default=20),
    )
    actions = (("again", "Judge again by today's labels"), ("forget", "Forget all the analysis"))

    def check(self, ctx):
        ok, why = super().check(ctx)
        if ok and not local_address(ctx.settings["url"]):
            return False, "Ollama's address is not on this computer or its network"
        return ok, why

    def models(self, ctx):
        return [m.strip() for m in str(ctx.settings.get("models") or "").split(",") if m.strip()][:3]

    def todo(self, ctx):
        return labels_mod.to_analyse(ctx.store, ctx.settings.get("who", "unnamed") != "all",
                                     int(ctx.settings.get("min_messages") or 1))

    def info(self, ctx):
        db = ctx.store.read()
        read = db.execute("SELECT count(*) FROM analysis").fetchone()[0]
        names = db.execute("SELECT count(*) FROM name_guess WHERE how = 'models' AND NOT dismissed").fetchone()[0]
        out = [("Read", str(read)), ("To read", str(len(self.todo(ctx)))), ("Names found", str(names))]
        if labels_mod.stale(ctx.store):
            out.append(("Read by an older list of labels", str(labels_mod.stale(ctx.store))))
        return out

    def idle_actions(self, ctx):
        db = ctx.store.read()
        out = [] if labels_mod.stale(ctx.store) else ["again"]
        if not (db.execute("SELECT 1 FROM analysis").fetchone() or db.execute(
                "SELECT 1 FROM person_label WHERE state = 'suggested'").fetchone() or db.execute(
                "SELECT 1 FROM name_guess WHERE how = 'models' AND NOT dismissed").fetchone()):
            out.append("forget")
        return out

    def action(self, ctx, name):
        if name == "forget":
            n = labels_mod.forget_analysis(ctx.store)
            ctx.log("the analysis is forgotten: {n} suggestions; your own labels stay", n=n)
        elif name.startswith("person:"):            # one person, now (asked from their chat)
            pid = int(name.split(":", 1)[1])
            n = labels_mod.messages_of(ctx.store, pid)
            ctx.log("reading {name} ({n} messages), {left} to go", name=people(ctx.store).name(pid), n=n, left=0)
            self.analyse(ctx, pid, n)
        elif name == "again":
            n = labels_mod.judge_again(ctx.store)
            ctx.log("{n} people to read again; they are read while the analysis runs", n=n)
        else:
            raise NotImplementedError(name)

    async def live(self, ctx):
        while True:
            if not await asyncio.to_thread(self.step, ctx):
                await asyncio.sleep(300)        # nothing to read: a look again later

    def step(self, ctx):
        """One person read, the largest chat first: whether there was one."""
        todo = self.todo(ctx)
        if not todo:
            ctx.log("all read", redrawn=True)
            return False
        pid, n = todo[0]
        name = people(ctx.store).name(pid)
        ctx.log("reading {name} ({n} messages), {left} to go", redrawn=True, name=name, n=n, left=len(todo))
        self.analyse(ctx, pid, n)
        return True

    def analyse(self, ctx, pid, n):
        store = ctx.store
        what = ctx.settings.get("what", "both")
        ppl = people(store)
        known, _ = labels_mod._known_names(store)
        rows = excerpt(store, pid, known)
        handles = [v for _, v, _, _ in ppl.handles.get(pid, ())]
        own = {labels_mod._key(w) for w in re.split(r"\W+", own_name() or "") if w}
        own |= {labels_mod._key(w) for me in ppl.me for _, v, _, _ in ppl.handles.get(me, ())
                for w in re.split(r"[\W_]+", v.split("@")[0]) if w}      # the owner's own handles are the owner too
        tones = labels_mod.for_models(store, "tone") if what != "name" else []
        relations = labels_mod.for_models(store, "relation") if what != "name" else []
        models = self.models(ctx)
        if not rows:
            labels_mod.save_analysis(store, pid, n, models)
            return
        lines = "\n".join(f"[{datetime.fromtimestamp(ts / 1000):%Y-%m-%d}] {'ME' if o else 'THEM'}: "
                          f"{t.replace(chr(10), ' ')[:220]}" for ts, o, t in rows)
        named = " | ".join(t.replace("\n", " ")[:200] for t in mentions(store, pid)) or "none"
        aka = ", ".join(a["name"] for a in ppl.aka(pid)) or "none"
        asks = (ASK_NAME if what != "tone" else "") + (ASK_TONE.format(
            tones="\n".join(f"- {w}: {m}" + (" (sensitive)" if s else "") for _, w, m, s in tones),
            relations="\n".join(f"- {w}: {m}" for _, w, m, _ in relations)) if what != "name" else "")
        prompt = PROMPT.format(owner=own_name() or "the owner", asks=asks, handles=", ".join(handles),
                               aka=aka, mentions=named, lines=lines)
        schema = self.schema(what, tones, relations)
        answers = []
        for m in models:
            try:
                answers.append(self.ask(ctx, m, prompt, schema))
            except Exception as e:          # one model failing: the others still vote
                ctx.log("{model}: {e}", model=m, e=e)
        if not answers:
            raise RuntimeError("no model answered")
        text = prompt.split("Their handles:", 1)[1]
        name, chosen, relation = vote(answers, text, handles, own, tones, relations)
        labels_mod.save_analysis(store, pid, n, models, name=name if what != "tone" else None,
                                 tones=chosen, relation=relation)

    @staticmethod
    def schema(what, tones, relations):
        props, req = {}, []
        if what != "tone":
            props |= {"name": {"type": ["string", "null"]}, "evidence": {"type": ["string", "null"]}}
            req += ["name", "evidence"]
        if what != "name":
            if tones:
                props["tone"] = {"type": "array", "items": {"type": "string", "enum": [w for _, w, _, _ in tones]}}
                props["sensitive_evidence"] = {"type": ["string", "null"]}
                req += ["tone", "sensitive_evidence"]
            if relations:
                props["relationship"] = {"type": "string", "enum": [w for _, w, _, _ in relations]}
                req.append("relationship")
        return {"type": "object", "properties": props, "required": req}

    def ask(self, ctx, model, prompt, schema):
        import httpx
        r = httpx.post(ctx.settings["url"].rstrip("/") + "/api/chat", timeout=600, json={
            "model": model, "stream": False, "format": schema, "think": False,
            "options": {"temperature": 0, "num_ctx": 8192, "num_predict": 400},
            "messages": [{"role": "user", "content": prompt}]})
        if r.status_code == 404:
            raise RuntimeError(f"Ollama has no model {model}")
        r.raise_for_status()
        return json.loads(r.json()["message"]["content"])


PLUGINS = (Ollama,)
