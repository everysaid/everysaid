#!/usr/bin/env python3
"""A local page to look at what the vision models said about the chat pictures.

    uv run --extra media python scripts/vlm-review.py [--port 8518]     then open the address it prints

Shows every picture that at least two of the models in `media-vlm.py` have answered about (and
that its cheap filters, table `filtered`, do not leave out), with each model's kind,
value and description under it, and whether they agree. Filters: agreement (all, majority, none),
the majority's kind, and the keep decision (value 3 or more). Clicking a model's line marks that
answer as wrong (again: right), stored in `vlm.db` (`verdict`) to measure each model. With
`--skipped` it shows instead the pictures `media-vlm.py` set aside (table `skipped`), with the reason
and the size. `--model M` shows every answer of that one model rather than the pilot, and the pictures it could
not answer about. Each picture
shows where it came from: the person, merged across services by number and named from the
Nextcloud address book (`~/.cache/everysaid/contact-names.tsv`, else the name Viber or WhatsApp
shows, marked with a dot), or the group; a filter picks one, and the [ and ] keys move to the
previous or next one. For each file the owner decides keep, aside or delete (on the
card, with K / A / D / U in the large view, or for everything shown of one person or group, where a
file also sent elsewhere is left to decide on its own); decisions go to `triage` in review.db, and
`media-triage.py` carries them out. Where `media-faces.py` has run, a filter by faces: with one of
the people it recognises, with others only, or with none; and with a dog (`media-tags.py` above
DOG, or the model's description saying so). What is kept (marked keep) is final and no
longer shown, except with `--kept`, which shows all of it (and where a decision can still be changed),
and `--similar`, only its look-alikes as `media-similar.py` grouped them, one group per kind. Pictures can be selected in the grid (click, shift+click, a drag box; ctrl+drag
takes away) and decided together with the same keys or the buttons; a button (two clicks) runs
`media-triage.py` from the page. Files set aside with
`media-aside.py` are left out. `--filtered` shows what the cheap filters left out, by reason. Double-click
opens a picture (a video plays); the arrow keys move on, Escape closes. Thumbnails are cached in a private folder
under /tmp, removed on exit.
"""
import argparse
import hashlib
import json
import os
import shutil
import signal
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))     # also when loaded by another script
import common  # noqa: E402
from common import config  # noqa: E402

VLM = os.path.join(config.DATA, "vlm.db")
REVIEW_DB = os.path.join(config.DATA, "review.db")
ROOTS = (common.MEDIA,)
VIDEO = common.VIDEO
SKIPPED = "στην άκρη"
DOG = config.DOG_THRESHOLD
ARCHIVE = config.MEDIA_STORE                                      # media/<ab>/<sha256><ext>
ARCHIVE_DB = os.path.join(config.DATA, "archive.db")
DATA = os.path.join(config.CACHE, "iphone")
DESKTOP = config.VIBER_DESKTOP                                    # optional, and may be gone
NAMES = os.path.join(config.CACHE, "contact-names.tsv")
SIMILAR = os.path.join(config.CACHE, "similar.tsv")
PILOT = config.OLLAMA_MODEL                                       # media-vlm.py's model ([ollama] model)
TRIAGE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "media-triage.py")
SERVICE = {"viber": "Viber", "whatsapp": "WhatsApp", "sms": "SMS", "mms": "SMS", "imessage": "iMessage"}
lock = threading.Lock()


def last10(number):
    """Numbers are matched on their last 10 digits, as everywhere in the project."""
    return "".join(c for c in number or "" if c.isdigit())[-10:]


def ro(path):
    return config.read_only(path)


def names():
    """last 10 digits -> name: the Nextcloud address book first (cached in NAMES, looked up through the
    nextcloud MCP server), then the name Viber or WhatsApp shows, unless that is just the number."""
    out = {}
    if os.path.exists(NAMES):
        for line in open(NAMES, encoding="utf-8"):
            number, name = line.rstrip("\n").split("\t")
            out[last10(number)] = name
    clean = lambda t: (t or "").strip("\u2068\u2069\u202a\u202c ").strip()
    found = []
    if os.path.exists(f"{DATA}/viber.sqlite"):          # each source only where it is
        found += ro(f"{DATA}/viber.sqlite").execute(
            "SELECT coalesce(p.ZCANONIZEDPHONENUM, p.ZPHONE), m.ZDISPLAYFULLNAME FROM ZMEMBER m "
            "JOIN ZPHONENUMBER p ON p.ZMEMBER = m.Z_PK").fetchall()
    if DESKTOP and os.path.exists(DESKTOP):
        found += ro(DESKTOP).execute("SELECT Number, coalesce(nullif(ClientName, ''), Name) FROM Contact").fetchall()
    if os.path.exists(f"{DATA}/whatsapp.sqlite"):
        found += ro(f"{DATA}/whatsapp.sqlite").execute(
            "SELECT ZCONTACTJID, ZPARTNERNAME FROM ZWACHATSESSION WHERE ZCONTACTJID LIKE '%@s.whatsapp.net'").fetchall()
    for number, name in found:
        name = clean(name)
        if number and name and any(c.isalpha() for c in name):
            out.setdefault(last10(number), name + " ·")      # the dot: not from the address book
    return out


def aside():
    """sha256 of the files set aside by media-aside.py (`aside` in review.db): kept off the pages."""
    db = sqlite3.connect(REVIEW_DB)
    if not db.execute("SELECT 1 FROM sqlite_master WHERE name = 'aside'").fetchone():
        return set()
    return {r[0] for r in db.execute("SELECT sha256 FROM aside")}


def sha_of(path):
    return os.path.splitext(os.path.basename(path))[0]


def triage_db():
    """The owner's decision per file: keep, aside or delete (`triage` in review.db). Recorded here,
    carried out by media-triage.py."""
    db = sqlite3.connect(REVIEW_DB)
    db.execute("""CREATE TABLE IF NOT EXISTS triage (path TEXT PRIMARY KEY, action TEXT NOT NULL
                  CHECK (action IN ('keep', 'aside', 'delete')), label TEXT, at INTEGER NOT NULL)""")
    # the files set aside are decided apart (`--aside`), never mixed with the rest: their own table,
    # carried out only by hand
    db.execute("""CREATE TABLE IF NOT EXISTS aside_decision (path TEXT PRIMARY KEY, action TEXT NOT NULL
                  CHECK (action IN ('keep', 'aside', 'delete')), label TEXT, at INTEGER NOT NULL)""")
    return db


def table():
    return "aside_decision" if Handler.aside else "triage"


def faces():
    """path -> [faces found, people recognised, other faces], from media-faces.py, where it has run."""
    path = os.path.join(config.CACHE, "faces.db")
    if not os.path.exists(path):
        return {}
    db = ro(path)
    out = {p: [n, [w for w in (who or "").split(",") if w], o, False]
           for p, n, who, o in db.execute("SELECT path, faces, who, others FROM face")}
    if config.DOG_TAG and db.execute("SELECT 1 FROM sqlite_master WHERE name = 'tag'").fetchone():
        # a dog, by media-tags.py: SigLIP's sigmoid is small in absolute terms; at 0.0005 it
        # agreed with Qwen's descriptions on nearly all the pictures tried
        for p, score in db.execute("SELECT path, score FROM tag WHERE label = ?", (config.DOG_TAG,)):
            out.setdefault(p, [None, [], 0, False])[3] = score >= DOG
    return out


def triage():
    return dict(triage_db().execute(f"SELECT path, action FROM {table()}"))


def final(path, done):
    """Kept for good: what the owner marked keep."""
    return done.get(path) == "keep"


def provenance(paths):
    """path -> where it came from: the people (merged across services, by number) or the groups."""
    known = names()
    own = {last10(n) for n in config.OWN_NUMBERS}
    db = ro(ARCHIVE_DB)
    convs = {}
    for cid, title, group, service, kind, value in db.execute(
            "SELECT cv.id, cv.title, cv.is_group, s.name, k.name, ad.value FROM conversation cv "
            "JOIN service s ON s.id = cv.service_id LEFT JOIN conversation_member cm ON cm.conversation_id = cv.id "
            "LEFT JOIN address ad ON ad.id = cm.address_id LEFT JOIN address_kind k ON k.id = ad.kind_id"):
        if group:
            convs[cid] = f"👥 {title or '(χωρίς όνομα)'} · {SERVICE.get(service, service)}"
        elif value is None:
            convs.setdefault(cid, "👤 (άγνωστος)")
        elif kind == "phone":
            n = last10(value)
            convs[cid] = "👤 " + ("εγώ" if n in own else known.get(n, value))
        else:
            convs[cid] = f"👤 {value}"
    by_sha = {}
    for sha, cid in db.execute("SELECT DISTINCT a.sha256, m.conversation_id FROM attachment a "
                               "JOIN message m ON m.id = a.message_id"):
        by_sha.setdefault(sha, set()).add(convs[cid])
    out = {}
    for p in paths:
        out[p] = sorted(by_sha.get(os.path.splitext(os.path.basename(p))[0], {"(χωρίς μήνυμα)"}))
    return out


def set_aside(table="skipped"):
    """The pictures media-vlm.py set aside (`skipped`), or left out by its cheap filters (`filtered`:
    gifs, stickers, video notes, the very small; not those already in immich, which go with a link),
    each as one pseudo-answer: the reason and the size."""
    db = sqlite3.connect(VLM)
    hidden, done = aside(), triage()
    rows = [(p, r) for p, r in db.execute(f"SELECT path, reason FROM {table} ORDER BY reason, path")
            if os.path.exists(p) and sha_of(p) not in hidden and not final(p, done) and r != "immich"]
    size = {os.path.normpath(r["SourceFile"]): f"{r.get('ImageWidth', '?')}×{r.get('ImageHeight', '?')}"
            for r in common.exiftool_json(["-n", "-ImageWidth", "-ImageHeight"], [p for p, _ in rows])}
    src, who = provenance([p for p, _ in rows]), faces()
    return [SKIPPED], [[p, {SKIPPED: [reason, 1, size.get(p, ""), False]}, src[p], done.get(p), who.get(p)]
                       for p, reason in rows]


def put_aside():
    """The files set aside (`aside` in review.db, media-aside.py / media-triage.py), with their kind."""
    db = sqlite3.connect(REVIEW_DB)
    label = dict(db.execute("SELECT sha256, label FROM aside"))
    arch = ro(ARCHIVE_DB)
    rows = []
    for sha, path, mime in arch.execute("SELECT sha256, path, mime FROM media"):
        if sha in label and os.path.exists(common.media_file(path)):
            kind = (mime or "?").split("/")[0]
            rows.append((common.media_file(path), {"image": "φωτογραφία", "video": "βίντεο", "audio": "ήχος"}.get(kind, "άλλο")))
    rows.sort()
    # the kind: the pilot model's (meme, screenshot, ...) where it has looked, else why the cheap filters left it out
    vlm = sqlite3.connect(VLM)
    seen = {p: (k, v, d) for p, k, v, d in vlm.execute(
        "SELECT path, kind, value, description FROM answer WHERE model = ?", (PILOT,))}
    reason = dict(vlm.execute("SELECT path, reason FROM filtered"))
    done, src, who = triage(), provenance([p for p, _ in rows]), faces()
    rdb = sqlite3.connect(REVIEW_DB)
    dated = {}                              # the date chosen for it (`aside_date`), shown with it
    if rdb.execute("SELECT 1 FROM sqlite_master WHERE name = 'aside_date'").fetchone():
        dated = {p: (src, ms) for p, src, ms in rdb.execute("SELECT path, source, ms FROM aside_date")}
    def when(p):
        if p not in dated:
            return ""
        src, ms = dated[p]
        return (f"ημερομηνία: {time.strftime('%Y-%m-%d %H:%M', time.localtime(ms / 1000))} "
                f"({'λήψης' if src == 'exif' else 'μηνύματος'}). ")
    def answer(p, k):
        if p in seen:
            return [seen[p][0], seen[p][1], when(p) + (seen[p][2] or ""), False]
        return [reason.get(p, k), 1, when(p), False]
    return [SKIPPED], [[p, {SKIPPED: answer(p, k)}, src[p], done.get(p), who.get(p)] for p, k in rows]


def resolution(path):
    """(width, height) of a picture or a video, (0, 0) when it cannot be read."""
    return common.dimensions(path)


def kept():
    """Everything kept for good: what is marked keep on the page."""
    db = sqlite3.connect(VLM)
    answers = {}
    for path, model, kind, value, desc in db.execute("SELECT path, model, kind, value, description FROM answer"):
        if model == PILOT or path not in answers:
            answers[path] = [kind, value, desc, False]
    done = triage()
    NONE = ["χωρίς κατηγορία", 0, "", False]
    for path, reason in db.execute("SELECT path, reason FROM filtered"):
        answers.setdefault(path, [reason, 0, "", False])        # kept from the cheap filters: their reason is the kind
    paths = [p for p, a in done.items() if a == "keep"]
    hidden = aside()                        # set aside: never among what is kept for the photo library
    paths = sorted(p for p in set(paths) if os.path.exists(p) and sha_of(p) not in hidden)
    group = {}
    if Handler.similar:                     # only the look-alikes media-similar.py found, group by group
        for line in open(SIMILAR, encoding="utf-8"):
            n, p = line.rstrip("\n").split("\t", 1)
            group[p] = int(n)
        paths = [p for p in set(paths) | {p for p, a in done.items() if a == "delete"}    # marked to go: still shown
                 if p in group and os.path.exists(p)]
        size = {n: list(group.values()).count(n) for n in set(group.values())}
        res = {p: resolution(p) for p in paths}
        paths.sort(key=lambda p: (group[p], -res[p][0] * res[p][1], p))       # the largest first
        top = {}
        for p in paths:
            top[group[p]] = max(top.get(group[p], 0), res[p][0] * res[p][1])
    src, who = provenance(paths), faces()
    def answer(p):
        if p in group:                      # the group is the kind, so the kind filter shows one group
            return [f"ομάδα {group[p]:02} ({size[group[p]]})"] + answers.get(p, NONE)[1:]
        return answers.get(p, NONE)
    if group:                               # and its group, width × height, whether it is the largest
        return ["κράτα"], [[p, {"κράτα": answer(p)}, src[p], done.get(p, "keep"), who.get(p),
                            [group[p], *res[p], res[p][0] * res[p][1] == top[group[p]]]] for p in paths]
    return ["κράτα"], [[p, {"κράτα": answer(p)}, src[p], "keep", who.get(p)] for p in paths]


def data():
    if Handler.skipped:
        return set_aside()
    if Handler.filtered:
        return set_aside("filtered")
    if Handler.aside:
        return put_aside()
    if Handler.kept:
        return kept()
    db = sqlite3.connect(VLM)
    db.execute("CREATE TABLE IF NOT EXISTS verdict (path TEXT, model TEXT, wrong INTEGER, PRIMARY KEY (path, model))")
    models = [r[0] for r in db.execute("SELECT DISTINCT model FROM answer ORDER BY model")]
    wrong = {(p, m) for p, m, w in db.execute("SELECT path, model, wrong FROM verdict") if w}
    by = {}
    for path, model, kind, value, desc in db.execute("SELECT path, model, kind, value, description FROM answer"):
        by.setdefault(path, {})[model] = [kind, value, desc, (path, model) in wrong]
    # at least two models answered; not what the cheap filters now leave out, nor what is gone
    filtered = {p for (p,) in db.execute("SELECT path FROM filtered")} if db.execute(
        "SELECT 1 FROM sqlite_master WHERE name = 'filtered'").fetchone() else set()
    if Handler.model:                       # one model's answers, all of them
        models = [Handler.model]
        chosen = [(p, {Handler.model: a[Handler.model]}) for p, a in sorted(by.items()) if Handler.model in a]
        # and the pictures it could not answer about (set aside by media-vlm.py), so all are in one place
        answered = {p for p, _ in chosen}
        chosen += [(p, {Handler.model: [f"χωρίς απάντηση ({reason})", 0, "", False]})
                   for p, reason in db.execute("SELECT path, reason FROM skipped ORDER BY path") if p not in answered]
    else:                                   # the pilot: at least two models answered
        chosen = [(p, a) for p, a in sorted(by.items()) if len(a) >= 2]
    hidden, done = aside(), triage()
    chosen = [(p, a) for p, a in chosen
              if p not in filtered and os.path.exists(p) and sha_of(p) not in hidden and not final(p, done)]
    src, who = provenance([p for p, _ in chosen]), faces()
    return models, [[p, a, src[p], done.get(p), who.get(p)] for p, a in chosen]


def safe(path):
    path = os.path.normpath(path)
    if not path.startswith(ROOTS) or not os.path.isfile(path):
        raise ValueError(path)
    return path


def thumb(cache, path, size):
    out = os.path.join(cache, f"{size}-" + common.flat(path) + ".jpg")
    if os.path.exists(out) or common.jpeg(path, out, size):
        return out
    return None


PAGE = """<!doctype html><html lang="el"><meta charset="utf-8"><title>Everysaid · Τι είπαν τα μοντέλα</title>
<style>
body{margin:0;font:13px system-ui;background:#1e1e1e;color:#ddd}
header{position:sticky;top:0;z-index:5;background:#2b2b2b;padding:8px 12px;display:flex;gap:8px;align-items:center;flex-wrap:wrap;box-shadow:0 2px 6px #0008}
select{background:#3a3a3a;color:#eee;border:1px solid #555;border-radius:4px;padding:5px 8px;font:inherit}
#status{margin-left:auto;opacity:.8}
#grid{display:flex;flex-wrap:wrap;gap:8px;padding:10px}
.grp{display:flex;flex-wrap:wrap;gap:8px;flex-basis:100%;padding:6px 0 10px;border-bottom:1px solid #444}
.gh{flex-basis:100%;opacity:.7}
.res{font-size:12px;opacity:.8}.zoom{cursor:zoom-in;padding:0 6px}
.grp .t{cursor:pointer}.grp .t img{cursor:pointer}
.best .res{color:#fff;font-weight:700;opacity:1}
.t{width:300px;background:#2a2a2a;border-radius:6px;overflow:hidden;position:relative}
.t img{width:100%;height:220px;object-fit:contain;background:#111;cursor:zoom-in;display:block}
.a{padding:4px 6px;border-top:1px solid #333;cursor:pointer;line-height:1.35}
.a:hover{background:#333}
.a.wrong{background:#4a1f1f;text-decoration:line-through;text-decoration-color:#f66}
.m{font-size:11px;opacity:.6}
.k{font-weight:600}
.v{display:inline-block;min-width:16px;text-align:center;border-radius:3px;padding:0 4px;margin-left:4px;color:#000;font-weight:700}
.v1{background:#777}.v2{background:#a98}.v3{background:#cc6}.v4{background:#8c6}.v5{background:#4c8}
.t.keep{outline:3px solid #4c8}.t.aside{outline:3px solid #db4}.t.delete{outline:3px solid #e55;opacity:.55}
.t.best{outline:4px solid #fff}.t.best.delete{outline-color:#e55}.t.best.aside{outline-color:#db4}
.acts{display:flex;border-top:1px solid #333}.acts b{flex:1;text-align:center;padding:3px 0;cursor:pointer;font-weight:400;opacity:.6}
.acts b:hover{background:#333;opacity:1}.acts b.on{opacity:1;font-weight:700}
.acts b.on[data-x=keep],.acts b[data-s=keep]{background:#285}.acts b[data-s=delete]{background:#833}.acts b.on[data-x=aside]{background:#764}.acts b.on[data-x=delete]{background:#833}
#bulk button{background:#3a3a3a;color:#eee;border:1px solid #555;border-radius:4px;padding:4px 8px;font:inherit;cursor:pointer}
#bulk button:disabled{opacity:.4;cursor:default}
#grid{user-select:none}
.t.sel{background:#203a55}.t.sel img{opacity:.8}.t.sel::after{content:"✓";position:absolute;top:6px;left:8px;color:#fff;background:#3b8eea;border-radius:3px;padding:0 5px;font-weight:700}
#box{position:fixed;border:1px dashed #3b8eea;background:#3b8eea22;display:none;z-index:10;pointer-events:none}
#apply{background:#5a2a2a;color:#fff;border:1px solid #844;border-radius:4px;padding:4px 10px;font:inherit;cursor:pointer}
#plan{display:none;white-space:pre-line;max-height:40vh;overflow:auto;background:#3a1f1f;padding:6px 12px;font-size:12px}
.from{padding:3px 6px;font-size:12px;color:#9cf;border-top:1px solid #333}
.badge{position:absolute;top:6px;right:6px;padding:1px 6px;border-radius:3px;font-size:11px;background:#000b}
.all{color:#7fd17f}.major{color:#e0c060}.none{color:#e07070}
#view{position:fixed;inset:0;background:#000e;display:none;z-index:20;align-items:center;justify-content:center;flex-direction:column}
#view img,#view video{max-width:95vw;max-height:80vh}
#vinfo{max-width:90vw;margin-top:8px}
</style>
<header>
 <select id="kind"></select>
 <select id="src"></select>
 <select id="faces"></select>
 <select id="tri"><option value="">Απόφαση σου: όλα</option><option value="none">χωρίς απόφαση</option><option value="keep">κράτα</option><option value="aside">στην άκρη</option><option value="delete">σβήσε</option></select>
 <span id="bulk"><span id="target">όσες φαίνονται</span>: <button data-x="keep">κράτα</button><button data-x="aside">άκρη</button><button data-x="delete">σβήσε</button><button data-x="">καθάρισε</button></span>
 <button id="apply"></button>
 <select id="agree"><option value="">Συμφωνία: όλα</option><option value="all">όλα τα μοντέλα ίδιο είδος</option><option value="major">πλειοψηφία</option><option value="none">διαφωνούν</option></select>
 <select id="keep"><option value="">Απόφαση: όλα</option><option value="yes">κρατάνε (αξία ≥ 3, πλειοψηφία)</option><option value="no">πετάνε</option><option value="split">διχασμένα</option></select>
 <select id="cmp"><option value="">Σύγκριση: όλα</option><option value="v2ref">v2 και Sonnet διαφωνούν (κράτα/όχι)</option><option value="v2doc">v2: έγγραφο</option><option value="v2keep">v2 κρατάει</option></select>
 <span id="status"></span> <span style="opacity:.5">[ ] προηγούμενο / επόμενο · επιλογή: κλικ, shift+κλικ, σύρσιμο (ctrl: αφαιρεί), Esc καθαρίζει · K κράτα, A άκρη, D/Delete σβήσε, U καμία</span>
</header>
<div id="plan"></div>
<div id="grid"></div>
<div id="box"></div>
<div id="view"><img id="vimg"><video id="vvid" controls autoplay loop></video><div id="vinfo"></div></div>
<script>
const [models, items] = __DATA__;
const KEPT = __KEPT__;
const ASIDE = __ASIDE__;   // the set-aside files: decisions kept apart, carried out by hand
const SIMILAR = __SIMILAR__;   // look-alikes: a file stays or goes, and the archive points it to the one kept
const grid = document.getElementById('grid');
// every text that comes with the data (names, group titles, the models' words) goes in escaped
const esc = t => String(t ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
const val = id => document.getElementById(id).value;
const short = m => m.replace(/:[^#]*/, '').replace('#', ' ');      // qwen2.5vl:7b#v2 -> qwen2.5vl v2
const V2 = models.find(m => m.endsWith('#v2')), REF = __REF__;
const keepOf = (a, m) => a[m] && (m.endsWith('#v2') ? a[m][0] === 'personal_photo' && a[m][1] >= 3 : a[m][1] >= 3);
const have = a => models.filter(m => a[m]);
function summary([p, a]) {
  const ms = have(a), kinds = ms.map(m => a[m][0]), keep = ms.map(m => a[m][1] >= 3);
  const counts = {}; kinds.forEach(k => counts[k] = (counts[k] || 0) + 1);
  const [top, n] = Object.entries(counts).sort((x, y) => y[1] - x[1])[0];
  const yes = keep.filter(Boolean).length;
  return {agree: n === ms.length ? 'all' : n >= 2 ? 'major' : 'none', kind: n >= 2 || ms.length === 1 ? top : '',
          keep: yes * 2 > ms.length ? 'yes' : 'no', split: yes > 0 && yes < ms.length};
}
function ok(x, skip) {
  if (skip !== 'src' && val('src') && !x[2].includes(val('src'))) return false;
  if (x[3] === 'keep' && !KEPT && !ASIDE) return false;  // kept is final: off the page at once (except on the kept page)
  if (skip !== 'faces' && val('faces') && !faceTags(x).includes(val('faces'))) return false;
  if (val('tri') && (x[3] || 'none') !== val('tri')) return false;
  const s = summary(x), a = x[1], c = val('cmp');
  if (c === 'v2ref' && !(V2 && a[V2] && a[REF] && keepOf(a, V2) !== keepOf(a, REF))) return false;
  if (c === 'v2doc' && !(V2 && a[V2] && a[V2][0] === 'document')) return false;
  if (c === 'v2keep' && !(V2 && keepOf(a, V2))) return false;
  return (!val('agree') || s.agree === val('agree')) && (skip === 'kind' || !val('kind') || s.kind === val('kind')) &&
    (!val('keep') || (val('keep') === 'split' ? s.split : s.keep === val('keep')));
}
function kinds() {
  // counted among what the other filters let through
  const c = {}; for (const x of items) if (ok(x, 'kind')) { const k = summary(x).kind || '(καμία πλειοψηφία)'; c[k] = (c[k] || 0) + 1; }
  const sel = document.getElementById('kind'), cur = sel.value;
  if (cur && !c[cur]) c[cur] = 0;
  sel.innerHTML = `<option value="">Είδος${models.length > 1 ? ' (πλειοψηφία)' : ''}: όλα</option>` +
    Object.entries(c).sort((a, b) => b[1] - a[1]).filter(([k]) => !k.startsWith('(')).map(([k, n]) => `<option value="${esc(k)}">${esc(k)} (${n})</option>`).join('');
  sel.value = cur;
}
function answers(p, a) {
  return have(a).map(m => `<div class="a ${a[m][3] ? 'wrong' : ''}" data-p="${encodeURIComponent(p)}" data-m="${esc(m)}" title="click: σωστό / λάθος">
    <span class="m">${esc(short(m))}</span> <span class="k">${esc(a[m][0])}</span><span class="v v${esc(a[m][1])}">${esc(a[m][1])}</span><br>${esc(a[m][2])}</div>`).join('');
}
let shown = [];
const chosen = new Set();       // the selection, by file, kept across filters
const DOG_WORDS = /σκύλ|σκυλ|κουτάβ|\bdog/i;
const hasDog = x => (x[4] && x[4][3]) || Object.values(x[1]).some(a => DOG_WORDS.test(a[2] || ''));
const faceTags = x => {
  // what media-faces.py found: each person recognised, "us" for any of them, others only, no faces;
  // and a dog, by media-tags.py or in the model's description
  const f = x[4], dog = hasDog(x) ? ['dog'] : [];
  if (!f || f[0] === null) return ['?', ...dog];
  const [n, who, others] = f;
  if (!n) return ['0', ...dog];
  return [...(who.length ? [...who.map(w => 'w:' + w), 'us'] : ['others']), ...dog];
};
const faceText = x => { const f = x[4], dog = hasDog(x) ? ' · 🐕' : ''; if (!f || f[0] === null) return dog;
  const [n, who, others] = f;
  return (n ? ` · 🙂 ${[...who, ...(others ? [`+${others}`] : [])].join(', ')}` : ' · χωρίς πρόσωπα') + dog; };
const from = x => `<div class="from">${esc(x[2].join(' · ') + faceText(x))}</div>`;
function faceOptions() {
  const c = {}; for (const x of items) if (ok(x, 'faces')) for (const t of faceTags(x)) c[t] = (c[t] || 0) + 1;
  const sel = document.getElementById('faces'), cur = sel.value;
  const people = Object.keys(c).filter(t => t.startsWith('w:')).sort();
  const opt = (v, label) => c[v] || v === cur ? `<option value="${esc(v)}">${esc(label)} (${c[v] || 0})</option>` : '';
  sel.innerHTML = '<option value="">Πρόσωπα: όλα</option>' + opt('us', 'με κάποιον από εμάς') +
    people.map(t => opt(t, 'με ' + t.slice(2))).join('') + opt('others', 'μόνο άλλα πρόσωπα') +
    opt('0', 'χωρίς πρόσωπα') + opt('dog', '🐕 με σκύλο') + opt('?', 'δεν ελέγχθηκαν');
  sel.value = cur;
}
const NAMES = {keep: 'κράτα', aside: 'άκρη', delete: 'σβήσε'}, DUP = 'διπλότυπο';
const acts = x => SIMILAR ? `<div class="acts" data-p="${encodeURIComponent(x[0])}">` +
  `<b data-x="toggle" class="on" data-s="${x[3] === 'delete' ? 'delete' : 'keep'}">${x[3] === 'delete' ? 'σβήνεται · ίδια με άλλη' : 'μένει'}</b></div>` :
  `<div class="acts" data-p="${encodeURIComponent(x[0])}">` +
  Object.entries(NAMES).map(([k, n]) => `<b data-x="${k}" class="${x[3] === k ? 'on' : ''}">${n}</b>`).join('') + '</div>';
async function decide(list, action, label) {
  // the label is where the file came from (the folder name when set aside): the chosen filter, else its own
  if (!list.length) return;
  const byLabel = {};
  for (const x of list) { x[3] = action || null; const l = SIMILAR ? DUP : label || x[2][0]; (byLabel[l] = byLabel[l] || []).push(x[0]); }
  for (const [l, paths] of Object.entries(byLabel))
    await fetch('/triage', {method: 'POST', body: JSON.stringify({paths, action, label: l})});
}
function decisions() {      // how many of each decision, on the filter itself
  const c = {}; for (const x of items) { const k = x[3] || 'none'; c[k] = (c[k] || 0) + 1; }
  const names = {'': 'Απόφαση σου: όλα', none: 'χωρίς απόφαση', keep: 'κράτα', aside: 'στην άκρη', delete: 'σβήσε'};
  for (const o of document.getElementById('tri').options) o.textContent = names[o.value] + (o.value ? ` (${c[o.value] || 0})` : '');
}
function render() {
  sources(); faceOptions(); kinds(); decisions(); grid.innerHTML = '';
  // where they came from matters only while there is more than one source
  document.getElementById('src').style.display = new Set(items.flatMap(x => x[2])).size > 1 || val('src') ? '' : 'none'; shown = items.filter(x => ok(x));
  let row = null;
  for (const x of shown) {
    const [p, a] = x, s = summary(x), d = document.createElement('div');
    if (x[5] && (!row || row.dataset.g != x[5][0])) {        // look-alikes: each group on its own row
      row = document.createElement('div'); row.className = 'grp'; row.dataset.g = x[5][0];
      row.innerHTML = `<div class="gh">ομάδα ${x[5][0]}</div>`; grid.appendChild(row);
    }
    d.className = 't ' + (x[3] || '') + (chosen.has(p) ? ' sel' : '') + (x[5] && x[5][3] ? ' best' : ''); d.dataset.p = p;
    d.innerHTML = `<img draggable="false" loading="lazy" src="/thumb?p=${encodeURIComponent(p)}" data-p="${encodeURIComponent(p)}">` +
      (models.length > 1 ? `<span class="badge ${s.agree}">${s.agree === 'all' ? 'συμφωνούν' : s.agree === 'major' ? 'πλειοψηφία' : 'διαφωνούν'}</span>` : '') +
      (x[5] ? `<div class="res"><span class="zoom" title="μεγάλη προβολή">🔍</span> ${x[5][1]}×${x[5][2]}${x[5][3] ? ' · μεγαλύτερη' : ''}</div>` : '') +
      from(x) + acts(x) + answers(p, a);
    (row || grid).appendChild(d);
  }
  const wrong = models.map(m => `${short(m)}: ${items.filter(([p, a]) => a[m] && a[m][3]).length}`).join(', ');
  const tally = Object.entries(NAMES).map(([k, n]) => `${n} ${items.filter(x => x[3] === k).length}`).join(', ');
  document.getElementById('status').textContent = `${shown.length} από ${items.length} · ${tally} · λάθη που σημείωσες: ${wrong}`;
  const picked = shown.filter(x => chosen.has(x[0])).length;
  document.getElementById('target').textContent = picked ? `${picked} επιλεγμένες` : 'όσες φαίνονται';
  document.querySelectorAll('#bulk button').forEach(b => b.disabled = !picked && !val('src'));
  if (SIMILAR) document.querySelectorAll('#bulk button').forEach(b => {     // only stays or goes here
    if (b.dataset.x === 'keep') b.textContent = 'μένουν'; else if (b.dataset.x === 'delete') b.textContent = 'σβήνονται';
    else b.style.display = 'none'; });
  const del = items.filter(x => x[3] === 'delete').length, asd = items.filter(x => x[3] === 'aside').length;
  const ap = document.getElementById('apply');
  ap.style.display = del + asd && !ASIDE ? '' : 'none'; plan = null; document.getElementById('plan').style.display = 'none';
  ap.textContent = `Εκτέλεση: σβήσιμο ${del}, στην άκρη ${asd}`;
}
document.addEventListener('click', async e => {
  const el = e.target.closest('.a'); if (!el) return;
  const p = decodeURIComponent(el.dataset.p), m = el.dataset.m, item = items.find(x => x[0] === p);
  item[1][m][3] = !item[1][m][3];
  document.querySelectorAll(`.a[data-p="${CSS.escape(el.dataset.p)}"][data-m="${CSS.escape(m)}"]`).forEach(x => x.classList.toggle('wrong', item[1][m][3]));
  await fetch('/verdict', {method: 'POST', body: JSON.stringify({path: p, model: m, wrong: item[1][m][3]})});
  document.getElementById('status').textContent = document.getElementById('status').textContent.replace(/λάθη.*/, 'λάθη που σημείωσες: ' + models.map(mm => `${short(mm)}: ${items.filter(([pp, a]) => a[mm] && a[mm][3]).length}`).join(', '));
});
let at = -1;
function closeView() { const v = document.getElementById('vvid'); v.pause(); v.removeAttribute('src'); document.getElementById('view').style.display = 'none'; }
function show(i) {
  if (i < 0 || i >= shown.length) return;
  at = i; const [p, a] = shown[i];
  const vid = /\\.(mp4|mov|3gp|webm|m4v)$/i.test(p), img = document.getElementById('vimg'), v = document.getElementById('vvid');
  img.style.display = vid ? 'none' : ''; v.style.display = vid ? '' : 'none';     // videos play, with their sound
  if (vid) { img.removeAttribute('src'); v.src = '/video?p=' + encodeURIComponent(p); }
  else { v.pause(); v.removeAttribute('src'); img.src = '/big?p=' + encodeURIComponent(p); }
  document.getElementById('vinfo').innerHTML = `<div style="opacity:.6">${i + 1} / ${shown.length} · ${esc(p)}</div>` + from(shown[i]) + acts(shown[i]) + answers(p, a);
  document.getElementById('view').style.display = 'flex';
}
grid.addEventListener('dblclick', e => { if (SIMILAR) return; const t = e.target.closest('.t'); if (t) show(shown.findIndex(x => x[0] === t.dataset.p)); });
// Selection, as on the other pages: click selects, shift+click a range, a drag draws a box that adds
// to the selection (ctrl+drag takes away). K / A / D (or Delete) / U then decide for all selected.
let last = null, start = null, dragged = false, base = null, removing = false;
const tiles = () => [...grid.querySelectorAll('.t')];
const box = document.getElementById('box');
function pick(t, on) { t.classList.toggle('sel', on); on ? chosen.add(t.dataset.p) : chosen.delete(t.dataset.p); }
function counts() { const k = shown.filter(x => chosen.has(x[0])).length;
  document.getElementById('target').textContent = k ? `${k} επιλεγμένες` : 'όσες φαίνονται';
  document.querySelectorAll('#bulk button').forEach(b => b.disabled = !k && !val('src')); }
grid.addEventListener('click', async e => {
  const t = e.target.closest('.t');
  if (SIMILAR && t && e.target.closest('.zoom')) return show(shown.findIndex(x => x[0] === t.dataset.p));
  if (SIMILAR && t && !e.target.closest('.acts')) {     // look-alikes: a click on the card is the toggle
    const x = items.find(y => y[0] === t.dataset.p);
    await decide([x], x[3] === 'delete' ? 'keep' : 'delete'); render(); return;
  }
  if (!t || dragged || e.target.closest('.acts, .a')) return;
  if (e.shiftKey && last) {
    const all = tiles(), a = all.indexOf(last), b = all.indexOf(t);
    for (const x of all.slice(Math.min(a, b), Math.max(a, b) + 1)) pick(x, true);
  } else pick(t, !t.classList.contains('sel'));
  last = t; counts();
});
grid.addEventListener('mousedown', e => {
  if (SIMILAR || e.button !== 0 || e.target.closest('.acts, .a')) return;     // no selection box there
  start = [e.clientX, e.clientY + window.scrollY]; dragged = false; removing = e.ctrlKey; base = new Set(chosen);
});
document.addEventListener('mousemove', e => {
  if (!start) return;
  const x = e.clientX, y = e.clientY + window.scrollY;
  if (!dragged && Math.hypot(x - start[0], y - start[1]) < 6) return;
  dragged = true;
  const l = Math.min(x, start[0]), r = Math.max(x, start[0]), t = Math.min(y, start[1]), b = Math.max(y, start[1]);
  Object.assign(box.style, {display: 'block', left: l + 'px', top: (t - window.scrollY) + 'px', width: (r - l) + 'px', height: (b - t) + 'px'});
  for (const el of tiles()) {
    const q = el.getBoundingClientRect(), qt = q.top + window.scrollY, qb = q.bottom + window.scrollY;
    const hit = q.left < r && q.right > l && qt < b && qb > t;
    pick(el, hit ? !removing : base.has(el.dataset.p));
  }
  if (e.clientY > innerHeight - 30) scrollBy(0, 20); else if (e.clientY < 80) scrollBy(0, -20);
  counts();
});
document.addEventListener('mouseup', () => { start = null; box.style.display = 'none'; setTimeout(() => dragged = false, 0); });
async function decideChosen(action) {
  const list = shown.filter(x => chosen.has(x[0]));
  if (!list.length) return;
  await decide(list, action, val('src'));
  chosen.clear(); render();
}
// Two clicks: the first asks for the exact list a dry run of media-triage.py would carry out and
// shows it; the second carries out that list and nothing else (by its id).
let plan = null;
document.getElementById('apply').onclick = async () => {
  const ap = document.getElementById('apply'), list = document.getElementById('plan');
  const say = t => document.getElementById('status').textContent = t;
  if (!plan) {
    ap.disabled = true; ap.textContent = 'ελέγχεται…';
    const r = await (await fetch('/apply', {method: 'POST', body: '{}'})).json();
    ap.disabled = false;
    if (!r.ok || !r.delete.length && !r.aside.length) { render(); say((r.ok ? 'τίποτα για εκτέλεση: ' : 'ΣΦΑΛΜΑ: ') + r.text); return; }
    plan = r.id;
    ap.textContent = `Σίγουρα; σβήσιμο ${r.delete.length}, στην άκρη ${r.aside.length}: κλικ ξανά`;
    list.textContent = [...r.delete.map(p => 'σβήσιμο: ' + p), ...r.aside.map(p => 'στην άκρη: ' + p), '', r.text].join('\\n');
    list.style.display = 'block'; say(r.text);
    setTimeout(() => { if (plan === r.id) render(); }, 30000);
    return;
  }
  const id = plan; plan = null; ap.disabled = true; ap.textContent = 'εκτελείται…';
  const r = await (await fetch('/apply', {method: 'POST', body: JSON.stringify({id})})).json();
  say((r.ok ? 'έγινε: ' : 'ΣΦΑΛΜΑ: ') + r.text);
  if (r.ok) setTimeout(() => location.reload(), 2500); else { ap.disabled = false; render(); }
};
document.getElementById('vimg').onclick = () => { closeView(); at = -1; };
function step(d) {
  // the previous or next person or group, in the order of the list
  const sel = document.getElementById('src'), opts = [...sel.options].filter(o => o.value);
  const i = opts.findIndex(o => o.value === sel.value);
  const next = opts[i < 0 ? (d > 0 ? 0 : opts.length - 1) : i + d];
  if (!next) return;
  sel.value = next.value; closeView(); at = -1;
  render(); window.scrollTo(0, 0);
}
document.addEventListener('keydown', e => {
  if (e.target.tagName === 'SELECT' || e.ctrlKey || e.altKey || e.metaKey) return;
  if (e.key === ']' || e.code === 'BracketRight') { e.preventDefault(); return step(1); }
  if (e.key === '[' || e.code === 'BracketLeft') { e.preventDefault(); return step(-1); }
  const key = (SIMILAR ? {KeyK: 'keep', KeyD: 'delete', Delete: 'delete', KeyU: 'keep'}
                       : {KeyK: 'keep', KeyA: 'aside', KeyD: 'delete', Delete: 'delete', KeyU: ''})[e.code];
  if (at < 0) {                   // in the grid: decide for the selection
    if (key !== undefined && chosen.size) { e.preventDefault(); decideChosen(key || null); }
    else if (e.key === 'Escape') { chosen.clear(); render(); }
    return;
  }
  if (key !== undefined) {
    const next = (shown[at + 1] || shown[at])[0];
    decide([shown[at]], key || null, val('src')).then(() => {
      render(); const i = shown.findIndex(x => x[0] === next);
      if (i >= 0) show(i); else { closeView(); at = -1; }
    });
    return;
  }
  if (e.key === 'Escape') { closeView(); at = -1; }
  else if (e.key === 'ArrowRight') show(at + 1); else if (e.key === 'ArrowLeft') show(at - 1);
});
function sources() {
  // where the pictures came from, counted among those the other filters let through: people, then groups
  const c = {}; for (const x of items) if (ok(x, 'src')) for (const s of x[2]) c[s] = (c[s] || 0) + 1;
  const sel = document.getElementById('src'), cur = sel.value;
  const opt = ([k, n]) => `<option value="${esc(k)}">${esc(k)} (${n})</option>`;
  const all = Object.entries(c).sort((a, b) => b[1] - a[1]);
  if (cur && !c[cur]) all.push([cur, 0]);
  sel.innerHTML = '<option value="">Προέλευση: όλες</option>' +
    '<optgroup label="Πρόσωπα">' + all.filter(([k]) => k.startsWith('👤')).map(opt).join('') + '</optgroup>' +
    '<optgroup label="Ομάδες">' + all.filter(([k]) => k.startsWith('👥')).map(opt).join('') + '</optgroup>' +
    '<optgroup label="Άλλο">' + all.filter(([k]) => !/^👤|^👥/.test(k)).map(opt).join('') + '</optgroup>';
  sel.value = cur;
}
document.addEventListener('click', async e => {
  const b = e.target.closest('.acts b');
  if (b) {
    const x = items.find(y => encodeURIComponent(y[0]) === b.parentNode.dataset.p);
    if (b.dataset.x === 'toggle') await decide([x], x[3] === 'delete' ? 'keep' : 'delete');
    else await decide([x], x[3] === b.dataset.x ? null : b.dataset.x, val('src'));
    const v = at; render(); if (v >= 0) show(Math.min(v, shown.length - 1));
    return;
  }
  const bb = e.target.closest('#bulk button');
  if (bb && shown.some(x => chosen.has(x[0]))) return decideChosen(bb.dataset.x || null);
  if (bb && val('src')) {
    // aside and delete only for the files of this source alone: a file also sent elsewhere is decided one by one
    const own = bb.dataset.x === 'keep' || !bb.dataset.x ? shown : shown.filter(x => x.length && x[2].length === 1);
    await decide(own, bb.dataset.x || null, val('src'));
    render();
    if (own.length < shown.length) document.getElementById('status').textContent +=
      ` · ${shown.length - own.length} στάλθηκαν και αλλού: απόφαση μία μία`;
  }
});
['src', 'faces', 'tri', 'agree', 'kind', 'keep', 'cmp'].forEach(id => document.getElementById(id).onchange = render);
render();
</script></html>"""


class Handler(BaseHTTPRequestHandler):
    cache = None
    plans = {}                          # id (sha256 of the list) -> the dry run's list, for the second click
    skipped = False
    filtered = False
    aside = False
    kept = False
    similar = False
    model = None

    def log_message(self, *args):
        pass

    def send(self, code, body, ctype, headers=()):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        for name, value in headers:
            self.send_header(name, value)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if common.login(self):
            return
        if not common.allowed(self):
            return self.send(403, "Άνοιξε τη διεύθυνση που έγραψε το τερματικό (με το ?k=...).".encode(),
                             "text/plain; charset=utf-8")
        url = urlparse(self.path)
        try:
            if url.path == "/":
                with lock:
                    payload = json.dumps(data(), ensure_ascii=False).replace("</", "<\\/")
                page, csp = common.page(PAGE)
                page = page.replace("__REF__", json.dumps(config.REVIEW_REFERENCE).replace("</", "<\\/")).replace("__KEPT__", "true" if Handler.kept else "false").replace(
                    "__ASIDE__", "true" if Handler.aside else "false").replace(
                    "__SIMILAR__", "true" if Handler.similar else "false").replace("__DATA__", payload)
                return self.send(200, page.encode(), "text/html; charset=utf-8", [("Content-Security-Policy", csp)])
            if url.path == "/video":        # the file itself, for the browser to play
                path = safe(parse_qs(url.query)["p"][0])
                if path.lower().endswith(VIDEO):
                    with open(path, "rb") as f:
                        return self.send(200, f.read(), "video/mp4")
            if url.path in ("/thumb", "/big"):
                path = safe(parse_qs(url.query)["p"][0])
                out = thumb(self.cache, path, 440 if url.path == "/thumb" else 1600)
                if out:
                    with open(out, "rb") as f:
                        return self.send(200, f.read(), "image/jpeg")
        except (KeyError, ValueError):
            pass
        self.send(404, b"not found", "text/plain")

    def do_POST(self):
        if not common.allowed(self):
            return self.send(403, b"forbidden", "text/plain")
        if self.path == "/apply" and Handler.aside:     # the set-aside files: nothing is carried out from here
            return self.send(403, b"{}", "application/json")
        if self.path == "/apply":       # carry out the decisions (media-triage.py: its checks, then removal)
            req = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
            with lock:
                if not req.get("id"):   # first click: what would be done, exactly (a dry run)
                    plan = os.path.join(self.cache, f"plan-{time.time_ns()}.tsv")
                    r = subprocess.run([sys.executable, TRIAGE, "--dry-run", "--plan", plan], capture_output=True, text=True,
                                       encoding="utf-8", env={**os.environ, "PYTHONUTF8": "1"})
                    lines = open(plan, encoding="utf-8").read() if os.path.exists(plan) else ""
                    pid = hashlib.sha256(lines.encode()).hexdigest()
                    Handler.plans[pid] = plan
                    todo = [l.split("\t", 1) for l in lines.splitlines()]
                    out = (r.stdout + r.stderr).strip().splitlines()
                    return self.send(200, json.dumps({
                        "ok": r.returncode == 0, "id": pid, "text": " · ".join(out),
                        "delete": [p for a, p in todo if a == "delete"], "aside": [p for a, p in todo if a == "aside"]},
                        ensure_ascii=False).encode(), "application/json")
                plan = Handler.plans.pop(req["id"], None)
                if not plan or not os.path.exists(plan):
                    return self.send(200, json.dumps({"ok": False, "text": "άγνωστη ή παλιά λίστα· ξανά από την αρχή"},
                                                     ensure_ascii=False).encode(), "application/json")
                r = subprocess.run([sys.executable, TRIAGE, "--only", plan], capture_output=True, text=True,
                                   encoding="utf-8", env={**os.environ, "PYTHONUTF8": "1"})
                os.remove(plan)
            out = (r.stdout + r.stderr).strip().splitlines()
            return self.send(200, json.dumps({"ok": r.returncode == 0, "text": " · ".join(out[-3:])},
                                             ensure_ascii=False).encode(), "application/json")
        if self.path == "/triage":
            t = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            with lock:
                db = triage_db()
                for path in t["paths"]:
                    safe(path)
                    if t["action"]:
                        db.execute(f"INSERT OR REPLACE INTO {table()} VALUES (?, ?, ?, ?)",
                                   (path, t["action"], t.get("label"), int(time.time())))
                    else:
                        db.execute(f"DELETE FROM {table()} WHERE path = ?", (path,))
                db.commit()
            return self.send(200, b"{}", "application/json")
        if self.path != "/verdict":
            return self.send(404, b"not found", "text/plain")
        v = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if v["model"] == SKIPPED:           # not a model's answer: nothing to record
            return self.send(200, b"{}", "application/json")
        with lock:
            db = sqlite3.connect(VLM)
            db.execute("CREATE TABLE IF NOT EXISTS verdict (path TEXT, model TEXT, wrong INTEGER, PRIMARY KEY (path, model))")
            db.execute("INSERT OR REPLACE INTO verdict VALUES (?, ?, ?)", (v["path"], v["model"], int(bool(v["wrong"]))))
            db.commit()
        self.send(200, b"{}", "application/json")


def main():
    ap = argparse.ArgumentParser(description="Look at what the vision models said.")
    ap.add_argument("--port", type=int, default=8518)
    ap.add_argument("--skipped", action="store_true", help="the pictures media-vlm.py set aside, with the reason")
    ap.add_argument("--filtered", action="store_true", help="what the cheap filters left out: gifs, stickers, video notes, small")
    ap.add_argument("--aside", action="store_true", help="what was set aside (Aside/), by person and kind")
    ap.add_argument("--model", help="all the answers of this one model (e.g. qwen2.5vl:7b), not only the pilot")
    ap.add_argument("--kept", action="store_true", help="everything kept: marked keep, and _keep/")
    ap.add_argument("--similar", action="store_true", help="of those kept, the look-alikes (media-similar.py)")
    args = ap.parse_args()
    os.umask(0o077)
    Handler.skipped = args.skipped
    Handler.filtered = args.filtered
    Handler.aside = args.aside
    Handler.model = args.model
    Handler.kept = args.kept or args.similar
    Handler.similar = args.similar
    Handler.cache = tempfile.mkdtemp(prefix="vlm-review-")
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))     # so that kill also removes the cache
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"{common.address(args.port)}   (Ctrl+C για τέλος)", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        shutil.rmtree(Handler.cache, ignore_errors=True)


if __name__ == "__main__":
    main()
