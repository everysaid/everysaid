#!/usr/bin/env python3
"""A local page to approve, one by one, which chat pictures may go into immich.

    uv run --extra media python scripts/immich-review.py [--origin archive|kept|filtered] [--port 8519]
    then open the address it prints (http://127.0.0.1:8519/?k=..., a key of its own each run)

Nothing goes into immich without the owner's approval; this page records it. It shows the
candidates from `~/.cache/everysaid/match.db` (by default the archive's),
each next to the most similar picture already in immich (its preview, as `immich-index.py` found
it; opened large, the larger preview comes through the API, permission asset.view; read only), with the similarity, both dates and both sizes (the chat copy is highlighted when it is the same picture,
very similar, but larger than immich's), so a duplicate shows at a glance. Filters: how
close the immich match is (same file, same picture by perceptual hash or same second and size
(`immich-phash.py`), very similar from a threshold set with a slider, which can also hide everything above or below it; another shot of the same moment within 15
minutes, similar, not there; times shown in Greek time), capture date, and the
decision so far. Select as on the other pages (click, shift+click, drag; ctrl+drag takes away),
then approve or reject. Double-click opens the pair large: A approves and moves on, R or Delete
rejects and moves on, U clears the decision, arrows move, Escape closes. Decisions go to
`~/.local/share/everysaid/review.db` (`decision`: SHA-1, path, approved or rejected, the immich asset it
was compared with, when): working state only, until they are acted on (the archive then records
the outcome: a `library_link`, or the file gone). Files set aside with `media-aside.py` are left out. The decisions cannot be remade, so they live in the
home snapshots, not in `~/.cache` with the rebuildable indexes.
"""
import argparse
import bisect
import html
import importlib.util
import json
import os
import shutil
import signal
import sqlite3
import sys
import tempfile
import threading
import time
import re
import urllib.error
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import common
from common import config

MATCH = os.path.join(config.CACHE, "match.db")
INDEX = os.path.join(config.CACHE, "immich.db")
DECISIONS = os.path.join(config.DATA, "review.db")
THUMBS = os.path.join(config.CACHE, "immich-thumbs") + os.sep    # immich-index.py, through the API
ROOTS = (common.MEDIA, THUMBS) + ((os.path.join(config.IMMICH_DATA, "thumbs") + os.sep,) if config.IMMICH_DATA else ())
VIDEO = common.VIDEO
HASH_SAME = 6       # perceptual-hash distance up to which two pictures are the same
NEIGHBOURS = os.path.join(config.CACHE, "neighbours.db")
FACES = os.path.join(config.CACHE, "faces.db")
AROUND = 4          # immich pictures shown on each side of the date a chat picture would get
# the one person most pictures come from (config `[review] person`): their conversation and their face
# have their own filters on the page; ME is the owner's face label (media-faces.py)
PERSON, ME = config.REVIEW_PERSON, config.REVIEW_ME
OWNER = f"👤 {PERSON}" if PERSON else None
lock = threading.Lock()


def aside():
    """sha256 of the files set aside by media-aside.py (`aside` in review.db): kept off the pages."""
    db = decisions_db()
    if not db.execute("SELECT 1 FROM sqlite_master WHERE name = 'aside'").fetchone():
        return set()
    return {r[0] for r in db.execute("SELECT sha256 FROM aside")}


def sha_of(path):
    return os.path.splitext(os.path.basename(path))[0]


def decisions_db():
    os.makedirs(os.path.dirname(DECISIONS), exist_ok=True)
    db = sqlite3.connect(DECISIONS)
    db.execute("""CREATE TABLE IF NOT EXISTS decision (
        sha1 TEXT PRIMARY KEY, path TEXT NOT NULL, approved INTEGER NOT NULL, immich_asset TEXT,
        similarity REAL, decided_at INTEGER NOT NULL)""")
    # which date a picture without its own capture date gets in immich, the owner's choice by eye: that
    # of an immich picture of the same occasion (immich's dates are right), its message's (sent the
    # day it was taken), none known (then it stays off the timeline), or one set by hand (`ms`, e.g. a
    # video's own creation time where the message is not certain)
    db.execute("""CREATE TABLE IF NOT EXISTS date_from (
        sha1 TEXT PRIMARY KEY, path TEXT NOT NULL,
        source TEXT NOT NULL CHECK (source IN ('asset', 'message', 'unknown', 'set')),
        immich_asset TEXT, decided_at INTEGER NOT NULL, ms INTEGER)""")
    return db


_dims = {}


def dims(paths):
    """path -> [width, height] as stored (exiftool)."""
    todo = [p for p in paths if p not in _dims]
    for r in common.exiftool_json(["-n", "-ImageWidth", "-ImageHeight"], todo):
        w, h = r.get("ImageWidth"), r.get("ImageHeight")
        if isinstance(w, (int, float)) and isinstance(h, (int, float)):
            _dims[os.path.normpath(r["SourceFile"])] = [int(w), int(h)]
    return {p: _dims.get(p) for p in paths}


def clip_same(sim, taken, message, idate):
    """CLIP's verdict as in immich-match: very similar and taken (or sent) at the immich date."""
    if not sim or sim < 0.90 or not idate:
        return False
    t = datetime.fromisoformat(idate.replace("+00", "+00:00")).timestamp()
    if taken:
        return abs(taken / 1000 - t) <= 120
    return message is not None and -3600 <= message / 1000 - t <= 30 * 86400


def kept():
    """What the owner kept on the review pages (`vlm-review.py --kept`): the candidates for immich."""
    spec = importlib.util.spec_from_file_location("vlm_review", os.path.join(os.path.dirname(__file__), "vlm-review.py"))
    review = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(review)
    notes = {p for (p,) in sqlite3.connect(review.VLM).execute("SELECT path FROM filtered WHERE reason = 'video note'")}
    marked = {p for p, a in review.triage().items() if a == "delete" and os.path.exists(p)}   # still shown until carried out
    return ({x[0] for x in review.kept()[1]} | marked) - notes          # video notes: not decided for immich yet


def filtered():
    """What media-vlm.py's first filter found already in immich (`filtered`, reason "immich"), never looked at."""
    spec = importlib.util.spec_from_file_location("vlm_review", os.path.join(os.path.dirname(__file__), "vlm-review.py"))
    review = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(review)
    return {p for (p,) in sqlite3.connect(review.VLM).execute("SELECT path FROM filtered WHERE reason = 'immich'")
            if os.path.exists(p)}


def undelete(db, path):
    """A date or an approval given after "delete" is the newer decision: the delete (`triage`) becomes
    keep, in the caller's transaction."""
    if db.execute("SELECT 1 FROM sqlite_master WHERE name = 'triage'").fetchone():
        db.execute("UPDATE triage SET action = 'keep', label = NULL, at = ? WHERE path = ? AND action = 'delete'",
                   (int(time.time()), path))


def triage_db():
    """The decisions of the review pages (`triage`, see vlm-review.py), carried out by media-triage.py."""
    spec = importlib.util.spec_from_file_location("vlm_review", os.path.join(os.path.dirname(__file__), "vlm-review.py"))
    review = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(review)
    return review.triage_db()


def senders(paths):
    """path -> who sent it: 1 the owner (to anyone), 2 others, 3 OWNER's conversation."""
    spec = importlib.util.spec_from_file_location("vlm_review", os.path.join(os.path.dirname(__file__), "vlm-review.py"))
    review = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(review)
    src = review.provenance(sorted(paths))
    db = config.read_only(review.ARCHIVE_DB)
    out = {}
    for p in paths:
        sent = {o for (o,) in db.execute("SELECT DISTINCT m.outgoing FROM attachment a JOIN message m ON m.id = a.message_id "
                                          "WHERE a.sha256 = ?", (sha_of(p),))}
        out[p] = 1 if 1 in sent else 3 if src[p] == [OWNER] else 2
    return out


def context(items, preview):
    """For the kept pictures: who sent each, and to help date those without a capture date, the immich
    pictures most like it (immich-neighbours.py) and those around the date it would otherwise get."""
    near = {}
    if os.path.exists(NEIGHBOURS):
        for path, asset, sim in config.read_only(NEIGHBOURS).execute(
                "SELECT path, asset, similarity FROM neighbour ORDER BY path, rank"):
            near.setdefault(path, []).append((asset, sim))
    global TIMELINE
    timeline = sorted((ms(d), a) for a, (_, d, _, _) in preview.items() if d)
    stamps = [t for t, _ in timeline]
    card = lambda a, sim=None: {"id": a, "p": preview[a][0], "d": ms(preview[a][1]), "sim": sim}
    TIMELINE = (stamps, timeline, card)
    who = senders([x["path"] for x in items])
    faces = {}                          # who is in it (media-faces.py): PERSON, ME
    if os.path.exists(FACES):
        faces = {p: (w or "").split(",") for p, w in config.read_only(FACES).execute(
            "SELECT path, who FROM face")}
    for x in items:
        x["sender"] = who[x["path"]]
        f = faces.get(x["path"])
        x["people"] = None if f is None else ("both" if {PERSON, ME} <= set(f) else
                                              "him" if PERSON in f else "me" if ME in f else "none")
        x["near"] = [card(a, s) for a, s in near.get(x["path"], []) if a in preview]
        chosen = x["datefrom"][1] if x.get("datefrom") and x["datefrom"][0] == "asset" else None
        if chosen in preview and chosen not in {c["id"] for c in x["near"]}:
            x["near"].append(card(chosen))      # picked from a pasted link: shown with the others
        when = x["taken"] or x["message"]
        x["around"] = around(when) if when else []


TIMELINE = None     # (stamps, [(stamp, asset)], card): immich's timeline, for /around


def around(when):
    """The immich pictures on either side of a moment (Unix ms) in the timeline."""
    stamps, timeline, card = TIMELINE
    i = bisect.bisect(stamps, when)
    return [card(a) for _, a in timeline[max(0, i - AROUND):i + AROUND]]


def typed(text):
    """A date typed by the owner, in Greek time: 2019, 2019-08, 2019-08-15 or 2019-08-15 20:30 -> Unix ms."""
    m = re.fullmatch(r"\s*(\d{4})(?:-(\d{1,2})(?:-(\d{1,2})(?:[ T](\d{1,2}):(\d{2}))?)?)?\s*", text or "")
    if not m:
        return None
    y, mo, d, h, mi = (int(v) if v else None for v in m.groups())
    try:
        when = datetime(y, mo or 1, d or 1, 12 if h is None else h, mi or 0, tzinfo=config.TIMEZONE)
    except ValueError:
        return None
    return int(when.timestamp() * 1000) if 1900 <= y <= datetime.now().year else None


def who_sent(items):
    """For the first filter's "already in immich": did the owner send it, in which conversation, who is in it."""
    spec = importlib.util.spec_from_file_location("vlm_review", os.path.join(os.path.dirname(__file__), "vlm-review.py"))
    review = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(review)
    src = review.provenance(sorted(x["path"] for x in items))
    db = config.read_only(review.ARCHIVE_DB)
    faces = {}
    if os.path.exists(FACES):
        faces = {p: (w or "").split(",") for p, w in config.read_only(FACES).execute(
            "SELECT path, who FROM face")}
    for x in items:
        sent = {o for (o,) in db.execute("SELECT DISTINCT m.outgoing FROM attachment a JOIN message m ON m.id = a.message_id "
                                          "WHERE a.sha256 = ?", (sha_of(x["path"]),))}
        x["mine"] = 1 in sent
        x["conv"] = "him" if OWNER in src[x["path"]] else "others"
        x["chat"] = ", ".join(l.replace("👤 ", "") for l in src[x["path"]])
        f = faces.get(x["path"])
        x["people"] = None if f is None else ("both" if {PERSON, ME} <= set(f) else
                                              "him" if PERSON in f else "me" if ME in f else "none")


def ms(date):
    return int(datetime.fromisoformat(date.replace(" ", "T").replace("+00", "+00:00")).timestamp() * 1000) if date else None


def data(origin, only=None):
    match = config.read_only(MATCH)
    index = config.read_only(INDEX)
    preview = {r[0]: (r[1], r[2], r[3], [r[4], r[5]] if r[4] and r[5] else None)
               for r in index.execute("SELECT id, preview, taken, name, width, height FROM asset")}
    with lock:
        decided = {r[0]: r[1] for r in decisions_db().execute("SELECT sha1, approved FROM decision")}
        date_from = {r[0]: (r[1], r[2]) for r in decisions_db().execute("SELECT sha1, source, immich_asset FROM date_from")}
        date_set = dict(decisions_db().execute("SELECT sha1, ms FROM date_from WHERE source = 'set'"))
    items = []
    hidden = aside()
    cols = {r[1] for r in match.execute("PRAGMA table_info(match)")}
    extra = "phash, same_shot" if "phash" in cols else "NULL, NULL"
    extra += (", hash_asset, CASE WHEN hash_aspect = 1 AND hash_dhash <= 10 THEN hash_dist END, hash_dist"
              if "hash_dhash" in cols else ", NULL, NULL, NULL")     # only hash matches confirmed by immich-dupes.py
    wanted = kept() if origin == "kept" else filtered() if origin == "filtered" else None
    if wanted is not None and Handler.paths:         # --paths: just these
        wanted &= Handler.paths
    marked = dict(triage_db().execute("SELECT path, action FROM triage")) if wanted else {}
    for path, sha1, exact, best, sim, taken, message, ph, same, hasset, hdist, raw_dist in match.execute(
            f"SELECT path, sha1, exact, best, similarity, taken, message, {extra} FROM match "
            "WHERE origin = ? ORDER BY path", ("archive" if wanted else origin,)):
        if wanted is not None and path not in wanted:
            continue
        clip_found = exact or clip_same(sim, taken, message, preview.get(best, (None, None))[1])
        if hdist is not None and hdist <= HASH_SAME and not exact:
            best, ph = hasset, hdist        # the all-against-all hash found the same picture
        if only == "hash" and (clip_found or ph is None or ph > HASH_SAME):
            continue
        if only == "false" and not (raw_dist is not None and raw_dist <= HASH_SAME and not exact and not clip_found
                                    and (hdist is None or hdist > HASH_SAME)):
            continue
        if not os.path.exists(path) or sha_of(path) in hidden:     # removed since the match, or set aside
            continue
        asset = exact or best
        p, idate, iname, isize = preview.get(asset, (None, None, None, None))
        items.append({"path": path, "sha1": sha1, "exact": bool(exact), "sim": sim, "taken": taken,
                      "message": message, "asset": asset, "ipreview": p, "idate": idate, "iname": iname,
                      "isize": isize, "phash": ph, "same": same, "decision": decided.get(sha1),
                      "delete": marked.get(path) == "delete",
                      "datefrom": None if taken else date_from.get(sha1, ("unknown", None)), "dateset": date_set.get(sha1),
                      # the owner has decided on it: a date chosen (even "unknown"), or marked to go
                      "chosen": sha1 in date_from or marked.get(path) == "delete" or decided.get(sha1) in (0, 1)})
    if origin == "kept":
        context(items, preview)
    if origin == "filtered":
        who_sent(items)
    size = dims([x["path"] for x in items])
    for x in items:
        x["size"] = size[x["path"]]
    return items


def safe(path):
    path = os.path.normpath(path)
    if not path.startswith(ROOTS) or not os.path.isfile(path):
        raise ValueError(path)
    return path


def thumb(cache, path, size):
    out = os.path.join(cache, f"{size}-" + common.flat(path) + ".jpg")
    if os.path.exists(out):
        return out
    if size > 360 and path.startswith(THUMBS):      # opened large: immich's larger preview, if it gives it
        try:
            data = common.immich("GET", f"/assets/{os.path.splitext(os.path.basename(path))[0]}/thumbnail?size=preview",
                                 raw=True)
            big = os.path.join(cache, "preview-" + os.path.basename(path))
            with open(big, "wb") as f:
                f.write(data)
            path = big
        except (urllib.error.URLError, SystemExit):
            pass                        # no permission, no key, or immich away: the small one
    return out if common.jpeg(path, out, size) else None


PAGE = """<!doctype html><html lang="el"><meta charset="utf-8"><title>Everysaid · Έγκριση για immich</title>
<style>
body{margin:0;font:13px system-ui;background:#1e1e1e;color:#ddd;user-select:none}
header{position:sticky;top:0;z-index:5;background:#2b2b2b;padding:8px 12px;display:flex;gap:8px;align-items:center;flex-wrap:wrap;box-shadow:0 2px 6px #0008}
select,button{background:#3a3a3a;color:#eee;border:1px solid #555;border-radius:4px;padding:5px 8px;font:inherit;cursor:pointer}
button.ok{background:#245a2e;border-color:#3a8} button.no{background:#7a2323;border-color:#a33}
#status{margin-left:auto;opacity:.8}
#grid{display:flex;flex-wrap:wrap;gap:8px;padding:10px}
.t{width:340px;background:#2a2a2a;border:3px solid transparent;border-radius:6px;overflow:hidden;position:relative;cursor:pointer}
.t.sel{border-color:#3b8eea;background:#203a55}
.t.d1{box-shadow:inset 0 0 0 3px #3a8} .t.d0{box-shadow:inset 0 0 0 3px #a33;opacity:.6}
.pair{display:flex;gap:2px;background:#111}
.pair div{flex:1;text-align:center}
.pair img{width:100%;height:160px;object-fit:contain;pointer-events:none;display:block}
.cap{font-size:10px;opacity:.6;padding:1px}
.info{padding:4px 6px;line-height:1.4}
.s{display:inline-block;padding:0 5px;border-radius:3px;color:#000;font-weight:700}
.s-exact{background:#4c8}.s-hash{background:#4c8}.s-high{background:#8c6}.s-moment{background:#6bd}.s-mid{background:#cc6}.s-low{background:#777}
.dec{position:absolute;top:6px;right:6px;padding:1px 6px;border-radius:3px;font-size:11px;font-weight:700}
.dec.d1{background:#3a8;color:#000}.dec.d0{background:#a33;color:#fff}.dec.both{background:#3a8;color:#000;opacity:.8}
.dsw{margin-top:3px;padding:2px 6px;border-radius:3px;cursor:pointer;display:inline-block}
.dsw.message{background:#5a4a1a}.dsw.set{background:#2a4d7a;color:#fff}
.fname{user-select:text;cursor:text;font-size:11px;opacity:.75;word-break:break-all}
.zoom{position:absolute;top:4px;left:6px;font-size:16px;cursor:zoom-in}
#box{position:fixed;border:1px dashed #3b8eea;background:#3b8eea22;display:none;z-index:10;pointer-events:none}
#view{position:fixed;inset:0;background:#000e;display:none;z-index:20;flex-direction:column;align-items:center;justify-content:center}
#vpair{display:flex;gap:12px;align-items:center}
#vpair img{max-width:47vw;max-height:78vh}
#vinfo{margin-top:8px;text-align:center;max-width:98vw}
.strip{display:flex;gap:6px;justify-content:center;align-items:flex-end;margin-top:6px;flex-wrap:wrap}
.sl{writing-mode:vertical-rl;transform:rotate(180deg);opacity:.6;font-size:11px}
.nb{cursor:pointer;border:3px solid transparent;border-radius:4px;font-size:11px}
.nb img{height:110px;max-width:170px;object-fit:contain;display:block;background:#111}
.nb:hover{border-color:#666}.nb.on{border-color:#3b8eea;background:#203a55}
.nb.me{border-color:#fc6;background:#4a3a10;cursor:default}.nb.me div{color:#fc6;font-weight:700}
#vinfo button.on{background:#2a4d7a;border-color:#3b8eea}
.dsw.unknown{background:#444}.dsw.asset{background:#2a4d7a;color:#fff;font-weight:700}
#vdec{position:fixed;top:12px;left:50%;transform:translateX(-50%);padding:4px 14px;border-radius:4px;font-weight:bold}
</style>
<header>
 <select id="match"></select>
 <label title="όριο για το «πολύ όμοιο»">πολύ όμοιο ≥ <input type="range" id="thr" min="0.70" max="1.00" step="0.01" value="0.90" style="vertical-align:middle"> <b id="thrv">0.90</b></label>
 <select id="side"><option value="">όλα</option><option value="above">μόνο ≥ όριο</option><option value="below">μόνο &lt; όριο</option></select>
 <select id="dated"><option value="">Ημερομηνία: όλα</option><option value="yes">με ημερομηνία λήψης</option><option value="no">χωρίς</option></select>
 <select id="dchoice" style="display:none"><option value="">Ημερομηνία: όλες</option><option value="unknown">άγνωστη (album)</option><option value="asset">από φωτογραφία του immich</option><option value="message">του μηνύματος</option><option value="set">ορισμένη</option><option value="pending">δεν έχω επιλέξει ακόμα</option></select>
 <select id="mine" style="display:none"><option value="">Αποστολέας: όλοι</option><option value="1">έστειλα εγώ</option><option value="0">έστειλε ο άλλος</option></select>
 <select id="conv" style="display:none"><option value="">Συνομιλία: όλες</option><option value="him">με: __PERSON__</option><option value="others">με άλλους</option></select>
 <select id="gap" style="display:none"><option value="">Μήνυμα ↔ immich: όλα</option><option value="before">μήνυμα πριν</option><option value="same">ίδια ώρα (≤10′)</option><option value="day">έως 1 μέρα μετά</option><option value="week">1–7 μέρες μετά</option><option value="more">πάνω από εβδομάδα</option></select>
 <select id="vid" style="display:none"><option value="">Είδος: όλα</option><option value="photo">φωτογραφίες</option><option value="video">βίντεο</option></select>
 <select id="people" style="display:none"><option value="">Πρόσωπα: όλα</option><option value="him">μόνο: __PERSON__</option><option value="me">μόνο εγώ</option><option value="both">και οι δύο</option><option value="none">κανένας μας</option><option value="?">δεν ελέγχθηκαν</option></select>
 <select id="sender" style="display:none"><option value="">Αποστολέας: όλοι</option><option value="1">έστειλα εγώ</option><option value="2">άλλοι</option><option value="3">__PERSON__</option></select>
 <select id="dec"><option value="">Απόφαση: όλα</option><option value="none">χωρίς απόφαση</option><option value="1">εγκρίθηκαν</option><option value="0">απορρίφθηκαν</option></select>
 <button id="all">Επιλογή όλων</button><button id="nonesel">Καμία επιλογή</button>
 <button id="approve" class="ok">Έγκριση επιλεγμένων</button><button id="reject" class="no">Απόρριψη επιλεγμένων</button><button id="clear">Αναίρεση απόφασης</button>
 <span id="status"></span>
</header>
<div id="grid"></div><div id="box"></div>
<div id="view"><div id="vdec"></div><div id="vpair"></div><div id="vinfo"></div></div>
<script>
const items = __DATA__;
// the kept pictures: both stay unless marked; a click marks one as "only immich's" (rejected), again clears it
const TOGGLE = __TOGGLE__;
// the first filter's "already in immich": the pair side by side; a click: only immich's (the chat copy goes) / both stay
const PAIR = __PAIR__;
const grid = document.getElementById('grid'), box = document.getElementById('box');
const val = id => document.getElementById(id).value;
const imms = x => x.idate ? Date.parse(x.idate.replace(' ', 'T').replace(/\\+00$/, 'Z')) : null;
// another shot of a moment immich already has: similar scene, taken within 15 minutes of it
const moment = x => x.taken && imms(x) && Math.abs(x.taken - imms(x)) <= 15 * 60000 && (x.sim ?? 0) >= 0.7;
const HIGH = () => parseFloat(document.getElementById('thr').value), MID = 0.85;
// the same picture by perceptual hash (distance up to 8 of 64) or same second and same size
const hashSame = x => (x.phash != null && x.phash <= 6) || x.same === 1;
const band = x => x.exact ? 'exact' : hashSame(x) ? 'hash' : x.sim >= HIGH() ? 'high' : moment(x) ? 'moment' : x.sim >= Math.min(MID, HIGH()) ? 'mid' : 'low';
function bands() {
  const sel = document.getElementById('match'), cur = sel.value, c = {}, h = HIGH().toFixed(2);
  for (const x of items) c[band(x)] = (c[band(x)] || 0) + 1;
  const label = {exact: 'ίδιο αρχείο στο immich', hash: 'ίδια εικόνα (hash ή ίδια λήψη)', high: `πολύ όμοιο (≥ ${h})`, moment: 'ίδια στιγμή, άλλη λήψη (±15′)',
                 mid: `όμοιο (${MID.toFixed(2)}–${h})`, low: `δεν υπάρχει (< ${Math.min(MID, HIGH()).toFixed(2)})`};
  sel.innerHTML = `<option value="">Immich: όλα (${items.length})</option>` +
    (PAIR ? `<option value="alike">όμοια, όχι ίδια (${items.length - (c.exact || 0) - (c.hash || 0)})</option>` : '') +
    Object.keys(label).map(k => `<option value="${k}">${label[k]} (${c[k] || 0})</option>`).join('');
  sel.value = cur;
  const side = document.getElementById('side'), sc = side.value, up = items.filter(above).length;
  side.innerHTML = `<option value="">όλα</option><option value="above">μόνο ≥ ${h} (${up})</option><option value="below">μόνο < ${h} (${items.length - up})</option>`;
  side.value = sc;
}
const BAND = {exact: 'ίδιο αρχείο', hash: 'ίδια (hash)', high: 'πολύ όμοιο', moment: 'ίδια στιγμή', mid: 'όμοιο', low: 'δεν υπάρχει'};
// the date the picture would get in immich (its EXIF capture date, else the message's) and how far it is from immich's
const gap = (a, b) => { const h = Math.abs(a - b) / 3600000; return h < 1 ? `${Math.round(h * 60)}′` : h < 48 ? `${Math.round(h)} ώρες` : h < 60 * 24 ? `${Math.round(h / 24)} μέρες` : `${(h / 24 / 365).toFixed(1)} χρόνια`; };
const when = x => { const d = x.taken || x.message, i = imms(x);
  const line = `${x.taken ? 'λήψη' : '<span style="color:#fc6">μήνυμα</span>'} ${greek(d)} · immich ${greek(i)}` + (d && i ? ` · <b>Δ ${gap(d, i)}</b>` : '');
  if (!x.datefrom || PAIR) return line;
  return line + `<div class="dsw ${esc(x.datefrom[0])}" title="κλικ: διάλεξε">${dateText(x)}</div>`; };
// no capture date of its own: the date it gets, chosen in the large view
const pictures = x => [...(x.near || []), ...(x.around || [])];
const dateText = x => { const [src, a] = x.datefrom;
  if (src === 'asset') { const c = pictures(x).find(c => c.id === a); return `θα μπει: ${c ? greek(c.d) : 'όπως η φωτογραφία του immich'} (immich)`; }
  if (src === 'set') return `θα μπει: ${greek(x.dateset)} (ορισμένη)`;
  return src === 'message' ? `θα μπει: ${greek(x.message)} (μήνυμα)` : 'άγνωστη ημερομηνία: album, εκτός χρονολογίου'; };
// every choice is stored at once; if the server did not keep it, say so and stay
async function post(url, body) {
  try { if ((await fetch(url, {method: 'POST', body: JSON.stringify(body)})).ok) return true; } catch (e) {}
  document.getElementById('vinfo').insertAdjacentHTML('afterbegin', '<div style="background:#a33;color:#fff;padding:4px">ΔΕΝ αποθηκεύτηκε, ξαναδοκίμασε</div>');
  return false;
}
async function setDate(x, src, asset, text) {
  let r;
  try { r = await fetch('/date', {method: 'POST', body: JSON.stringify({key: x.sha1, source: src, asset: asset || null, text})}); } catch (e) {}
  if (!r || !r.ok) {
    document.getElementById('vinfo').insertAdjacentHTML('afterbegin', `<div style="background:#a33;color:#fff;padding:4px">${src === 'set' ?
      'Δεν την κατάλαβα: γράψε 2019, 2019-08, 2019-08-15 ή 2019-08-15 20:30' : 'ΔΕΝ αποθηκεύτηκε, ξαναδοκίμασε'}</div>`);
    return false;
  }
  if (src === 'set') x.dateset = (await r.json()).ms;
  x.datefrom = [src, asset || null]; x.chosen = true; x.delete = false; return true;    // a date cancels "delete"
}
// after a choice, on to the next one (the last one: stays, to see it was taken)
const next = () => show(at + 1 < shown.length ? at + 1 : at);
// how long after the most similar immich picture the message came: the groups of the gap filter
const gapKind = x => { const i = imms(x); if (!x.message || !i) return ''; const h = (x.message - i) / 3600000;
  return h < 0 ? 'before' : h <= 1 / 6 ? 'same' : h <= 24 ? 'day' : h <= 168 ? 'week' : 'more'; };
const greek = ms => ms ? new Date(ms).toLocaleString('sv-SE', {timeZone: __TZ__}).slice(0, 16) : '—';
const px = s => s ? `${s[0]}×${s[1]}` : '?';
const area = s => s ? s[0] * s[1] : 0;
// the same picture (very similar), but the chat copy is larger than immich's: maybe worth replacing
const bigger = x => !x.exact && x.sim >= HIGH() && x.isize && area(x.size) > area(x.isize) * 1.1;
const sizes = x => `<span${bigger(x) ? ' style="color:#fc6;font-weight:700" title="μεγαλύτερη από του immich"' : ''}>συνομιλία ${px(x.size)}</span> · immich ${px(x.isize)}`;
const day = ms => ms ? new Date(ms).toISOString().slice(0, 16).replace('T', ' ') : '—';
const above = x => x.exact || hashSame(x) || (x.sim ?? 0) >= HIGH();
const ok = x => (!val('side') || (val('side') === 'above') === above(x)) && (!val('match') || band(x) === val('match') || (val('match') === 'alike' && !['exact', 'hash'].includes(band(x)))) && (!val('dated') || (val('dated') === 'yes') === !!x.taken) &&
  (!val('dec') || (val('dec') === 'none' ? x.decision == null : String(x.decision) === val('dec'))) &&
  !(TOGGLE && x.taken) &&       // a capture date of its own is right by definition: nothing to look at
  (!val('sender') || String(x.sender) === val('sender')) &&
  (!val('people') || (x.people || '?') === val('people')) && (!val('gap') || gapKind(x) === val('gap')) &&
  (!val('mine') || String(+!!x.mine) === val('mine')) && (!val('conv') || x.conv === val('conv')) &&
  (!val('vid') || (/\\.(mp4|mov|3gp|webm|m4v)$/i.test(x.path) ? 'video' : 'photo') === val('vid')) &&
  (!val('dchoice') || (val('dchoice') === 'pending' ? !x.chosen : (x.datefrom ? x.datefrom[0] : 'exif') === val('dchoice')));
const enc = encodeURIComponent;
// every text that comes with the data (names, file names, ids) goes into the page escaped
const esc = t => String(t ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
let shown = [], chosen = new Set();
const SENDER = {1: 'έστειλα εγώ', 2: 'άλλοι', 3: __PERSON_JS__};
function tile(x) {
  // the kept pictures: only when each goes in matters now
  if (TOGGLE) return `<div class="pair"><div><img loading="lazy" src="/thumb?p=${enc(x.path)}"></div></div>` +
    `<div class="info">${esc(SENDER[x.sender])} · στάλθηκε ${greek(x.message)}<br><span class="fname">${esc(x.path.split('/').pop())}</span><br>` +
    (x.datefrom ? `<div class="dsw ${esc(x.datefrom[0])}">${dateText(x)}</div>` : `<div class="dsw asset">λήψη ${greek(x.taken)} (EXIF)</div>`) + '</div>' +
    (x.delete ? '<span class="dec d0">θα σβηστεί</span>' : x.decision === 0 ? '<span class="dec d0">μόνο του immich</span>' :
     x.chosen ? '<span class="dec d1">✓</span>' : '');
  const b = band(x);
  return `<div class="pair"><div><img loading="lazy" src="/thumb?p=${enc(x.path)}"><div class="cap">συνομιλία</div></div>` +
    `<div>${x.ipreview ? `<img loading="lazy" src="/thumb?p=${enc(x.ipreview)}">` : ''}<div class="cap">πιο όμοιο στο immich</div></div></div>` +
    `<div class="info"><span class="s s-${b}">${BAND[b]}${x.exact ? '' : ' ' + (x.sim ?? 0).toFixed(2)}</span> ` +
    when(x) + `<br>${sizes(x)}<br>` + (PAIR ? `<b>${x.mine ? 'έστειλα εγώ' : 'έστειλε ο άλλος'}</b> · ${esc(x.chat)}<br>` : '') +
    `<span style="opacity:.6">${esc(x.path.split('/').pop())}</span></div>` +
    (TOGGLE || PAIR ? `<span class="zoom" title="μεγάλη προβολή">🔍</span>` : '') +
    (x.decision != null ? `<span class="dec d${x.decision}">${TOGGLE || PAIR ? (x.decision ? 'εγκρίθηκε' : 'μόνο του immich') : x.decision ? 'εγκρίθηκε' : 'απορρίφθηκε'}</span>` :
     TOGGLE || PAIR ? '<span class="dec both">μένουν και οι δύο</span>' : '');
}
function render() {
  grid.innerHTML = ''; shown = items.filter(ok);
  for (const x of shown) {
    const d = document.createElement('div');
    d.className = 't' + (chosen.has(x.sha1) ? ' sel' : '') + (x.decision != null ? ' d' + x.decision : '') + (x.delete ? ' d0' : '');
    d.dataset.k = x.sha1; d.innerHTML = tile(x); grid.appendChild(d);
  }
  status();
}
const tiles = () => [...grid.querySelectorAll('.t')];
function sync() { for (const t of tiles()) t.classList.contains('sel') ? chosen.add(t.dataset.k) : chosen.delete(t.dataset.k); }
function status(msg) {
  sync();
  const del = items.filter(x => x.delete).length;   // only marked here: deleted later, with media-triage.py
  if (TOGGLE && !msg) msg = `${shown.length} στην οθόνη · για σβήσιμο: ${del} · χωρίς ημερομηνία: ` +
    `${items.filter(x => x.datefrom && x.datefrom[0] === 'unknown').length} · δεν έχεις επιλέξει ακόμα: ${items.filter(x => !x.chosen && !x.taken).length}`;
  const a = items.filter(x => x.decision === 1).length, r = items.filter(x => x.decision === 0).length;
  document.getElementById('status').textContent = msg ||
    `${shown.length} στην οθόνη, ${chosen.size} επιλεγμένα · εγκρίθηκαν ${a}, απορρίφθηκαν ${r}, χωρίς απόφαση ${items.length - a - r}`;
}
async function decide(keys, approved) {
  if (!await post('/decide', {keys, approved})) return false;
  for (const x of items) if (keys.includes(x.sha1)) { x.decision = approved; if (approved === 1) x.delete = false; }
  return true;
}
let last = null, dragged = false, start = null, base = [], removing = false;
async function toggle(x) { return decide([x.sha1], x.decision === 0 ? null : 0); }
grid.addEventListener('click', async e => {
  const t = e.target.closest('.t'); if (!t || dragged) return;
  if (TOGGLE) {
    const i = shown.findIndex(x => x.sha1 === t.dataset.k);
    if (e.target.closest('.fname')) return;     // the name: there to be selected and copied
    return show(i);           // a click opens it to choose its date
  }
  if (PAIR) {
    const i = shown.findIndex(x => x.sha1 === t.dataset.k);
    if (e.target.closest('.zoom')) return show(i);
    if (await toggle(shown[i])) render(); return;
  }
  if (e.shiftKey && last) { const all = tiles(), a = all.indexOf(last), b = all.indexOf(t);
    for (const x of all.slice(Math.min(a, b), Math.max(a, b) + 1)) x.classList.add('sel'); }
  else t.classList.toggle('sel');
  last = t; status();
});
grid.addEventListener('mousedown', e => { if (e.button || TOGGLE || PAIR) return; start = [e.clientX, e.clientY + scrollY]; dragged = false;
  base = tiles().filter(t => t.classList.contains('sel')); removing = e.ctrlKey; });
document.addEventListener('mousemove', e => {
  if (!start) return; const x = e.clientX, y = e.clientY + scrollY;
  if (!dragged && Math.hypot(x - start[0], y - start[1]) < 6) return; dragged = true;
  const l = Math.min(x, start[0]), r = Math.max(x, start[0]), t = Math.min(y, start[1]), b = Math.max(y, start[1]);
  Object.assign(box.style, {display: 'block', left: l + 'px', top: (t - scrollY) + 'px', width: (r - l) + 'px', height: (b - t) + 'px'});
  for (const el of tiles()) { const q = el.getBoundingClientRect(), qt = q.top + scrollY, qb = q.bottom + scrollY;
    const hit = q.left < r && q.right > l && qt < b && qb > t; el.classList.toggle('sel', hit ? !removing : base.includes(el)); }
  if (e.clientY > innerHeight - 30) scrollBy(0, 20); else if (e.clientY < 80) scrollBy(0, -20);
  status();
});
document.addEventListener('mouseup', () => { start = null; box.style.display = 'none'; setTimeout(() => dragged = false, 0); });
document.getElementById('all').onclick = () => { tiles().forEach(t => t.classList.add('sel')); status(); };
document.getElementById('nonesel').onclick = () => { chosen.clear(); tiles().forEach(t => t.classList.remove('sel')); status(); };
for (const [id, v] of [['approve', 1], ['reject', 0], ['clear', null]])
  document.getElementById(id).onclick = async () => { sync(); const keys = [...chosen]; if (!keys.length) return status('Δεν έχεις επιλέξει τίποτα');
    await decide(keys, v); chosen.clear(); render(); };
['match', 'dated', 'dec', 'side', 'sender', 'dchoice', 'people', 'vid', 'mine', 'conv', 'gap'].forEach(id => document.getElementById(id).onchange = render);
document.getElementById('thr').oninput = () => { document.getElementById('thrv').textContent = HIGH().toFixed(2); bands(); render(); };
let at = -1;
function strip(x, list, label) {
  if (!list.length) return '';
  return `<div class="strip"><div class="sl">${label}</div>` + list.map(c => `<div class="nb${x.datefrom && x.datefrom[1] === c.id ? ' on' : ''}" data-a="${esc(c.id)}">` +
    `<img loading="lazy" src="/thumb?p=${enc(c.p)}"><div>${greek(c.d)}${c.sim != null ? ' · ' + c.sim.toFixed(2) : ''}</div></div>`).join('') + '</div>';
}
// where it would sit in immich's timeline: its own capture date, the chosen immich picture's, else the message's
function placed(x) {
  if (!x.datefrom) return [x.taken, 'λήψη'];
  const [src, a] = x.datefrom, c = pictures(x).find(c => c.id === a);
  if (src === 'set') return [x.dateset, 'ορισμένη'];
  return src === 'asset' && c ? [c.d, 'από immich'] : [x.message, src === 'message' ? 'μήνυμα' : 'αν έμπαινε με του μηνύματος'];
}
async function timeline(x, typedAt) {
  const [t, why] = typedAt ? [typedAt, 'αυτή που έγραψες, δεν αποθηκεύτηκε ακόμα'] : placed(x), el = document.getElementById('tl');
  if (!t || !el) return;
  const list = t === (x.taken || x.message) ? x.around : await (await fetch('/around?t=' + t)).json();
  if (shown[at] !== x) return;          // moved on meanwhile
  const k = list.findIndex(c => c.d > t), at_ = k < 0 ? list.length : k;
  const me = `<div class="nb me"><img src="/thumb?p=${enc(x.path)}"><div>▲ εδώ · ${greek(t)}</div></div>`;
  const cards = list.map(c => `<div class="nb${x.datefrom && x.datefrom[1] === c.id ? ' on' : ''}" data-a="${esc(c.id)}">` +
    `<img loading="lazy" src="/thumb?p=${enc(c.p)}"><div>${greek(c.d)}</div></div>`);
  cards.splice(at_, 0, me);
  el.innerHTML = `<div class="strip"><div class="sl">χρονολόγιο immich (${why})</div>${cards.join('')}</div>`;
}
function showDating(x, i) {
  if (document.activeElement) document.activeElement.blur();   // so that Space never presses the last button clicked
  const d = x.datefrom || ['exif'], b = (k, t) => `<button data-src="${k}" class="${d[0] === k ? 'on' : ''}">${t}</button>`;
  const video = /\\.(mp4|mov|3gp|webm|m4v)$/i.test(x.path);
  document.getElementById('vpair').innerHTML = `<div>${video ? `<video src="/video?p=${enc(x.path)}" controls autoplay style="max-height:52vh;max-width:90vw"></video>` :
    `<img src="/big?p=${enc(x.path)}" style="max-height:52vh">`}<div class="cap fname">${esc(x.path.split('/').pop())}</div></div>`;
  document.getElementById('vinfo').innerHTML = `${i + 1} / ${shown.length} · ${esc(SENDER[x.sender])} · στάλθηκε ${greek(x.message)} · <b>${x.datefrom ? dateText(x) : 'λήψη ' + greek(x.taken) + ' (EXIF)'}</b><br>` +
    strip(x, x.near || [], 'πιο όμοιες στο immich') + '<div id="tl"></div>' +
    `<div style="margin-top:6px"><button data-move="-1">‹ προηγούμενη</button> ` + (x.datefrom ? b('unknown', 'άγνωστη (album)') + (x.message ? b('message', 'ημερομηνία μηνύματος') : '') +
    `<input id="typed" placeholder="2019-08-15 20:30 ή σύνδεσμος του immich" size="30" value="${x.datefrom && x.datefrom[0] === 'set' ? greek(x.dateset) : ''}"> ` +
    `<button data-peek>δες στο χρονολόγιο</button> ` +
    ` <span style="opacity:.6">(Space: του μηνύματος) ή κλικ σε φωτογραφία του immich της ίδιας περίστασης</span> ` :
     `<button data-ok class="${x.decision === 1 ? 'on' : ''}">${x.decision === 1 ? '✓ σωστά εκεί' : 'σωστά εκεί'}</button> `) +
    `<button data-del class="${x.delete ? 'on' : ''}">${x.delete ? 'θα σβηστεί ✓' : 'σβήσε'}</button> ` +
    `<button data-only class="${x.decision === 0 ? 'on' : ''}">${x.decision === 0 ? 'μόνο του immich ✓' : 'μόνο του immich'}</button> <button data-move="1">επόμενη ›</button> <button data-close>✕ κλείσιμο</button></div>`;
  const v = document.getElementById('vdec'); v.style.display = 'none';
  document.getElementById('view').style.display = 'flex';
  timeline(x);
}
function show(i) {
  if (i < 0 || i >= shown.length) return; at = i; const x = shown[i];
  if (TOGGLE) return showDating(x, i);
  document.getElementById('vpair').innerHTML = `<div><img src="/big?p=${enc(x.path)}"><div class="cap">συνομιλία</div></div>` +
    (x.ipreview ? `<div><img src="/big?p=${enc(x.ipreview)}"><div class="cap">immich: ${esc(x.iname)}</div></div>` : '');
  document.getElementById('vinfo').innerHTML = `${i + 1} / ${shown.length} · <b>${BAND[band(x)]}</b> ${x.exact ? '' : (x.sim ?? 0).toFixed(2)} · ${when(x)} · ${sizes(x)}<br>` +
    (PAIR ? `<button data-move="-1">‹ προηγούμενη</button> <span style="opacity:.6">κλικ στις εικόνες: μόνο του immich / και οι δύο</span> <button data-move="1">επόμενη ›</button> <button data-close>✕ κλείσιμο</button>` :
    `<span style="opacity:.6">A έγκριση · R/Delete απόρριψη · U αναίρεση · ← → · Esc</span>`);
  const v = document.getElementById('vdec');
  v.textContent = x.decision == null ? (PAIR ? 'μένουν και οι δύο' : '') : x.decision ? 'εγκρίθηκε' : TOGGLE || PAIR ? 'μόνο του immich' : 'απορρίφθηκε';
  if (PAIR && x.decision == null) { v.style.display = ''; v.style.background = '#3a8'; document.getElementById('view').style.display = 'flex'; return; }
  v.style.display = x.decision == null ? 'none' : ''; v.style.background = x.decision ? '#3a8' : '#a33';
  document.getElementById('view').style.display = 'flex';
}
grid.addEventListener('dblclick', e => { if (TOGGLE || PAIR) return; const t = e.target.closest('.t'); if (t) show(shown.findIndex(x => x.sha1 === t.dataset.k)); });
document.getElementById('view').onclick = async e => {
  if (PAIR && e) {               // the pair: toggle; arrows move; only the close button (or Escape) leaves
    if (document.activeElement) document.activeElement.blur();
    const m = e.target.closest('[data-move]'); if (m) return show(at + +m.dataset.move);
    if (e.target.closest('#vpair')) { if (await toggle(shown[at])) show(at); return; }
    if (!e.target.closest('[data-close]')) return;
  }
  if (TOGGLE && e) {             // the pair: toggle; the arrows: move; elsewhere: close
    const x = shown[at];
    {                             // dating: a picture of the same occasion, or message / unknown; nothing else closes
      const nb = e.target.closest('.nb:not(.me)'), sb = e.target.closest('[data-src]');
      // a choice moves on; taking one back (the same picture again, delete again) stays
      if (nb && x.datefrom) { const again = x.datefrom[0] === 'asset' && x.datefrom[1] === nb.dataset.a;
        if (await setDate(x, again ? 'unknown' : 'asset', again ? null : nb.dataset.a)) again ? show(at) : next(); return; }
      if (sb && x.datefrom) { if (await setDate(x, sb.dataset.src)) next(); return; }
      if (e.target.closest('[data-peek]')) {     // where the typed date would put it; nothing is kept
        const v = document.getElementById('typed').value, id = (v.match(UUID) || [])[0];
        if (id) { const a = await fetch('/asset?id=' + id); if (a.ok) return timeline(x, (await a.json()).d); }
        const r = await fetch('/typed?text=' + enc(v));
        if (r.ok) timeline(x, (await r.json()).ms);
        else document.getElementById('tl').innerHTML = '<div style="background:#a33;color:#fff;padding:4px">Δεν την κατάλαβα: γράψε 2019, 2019-08, 2019-08-15 ή 2019-08-15 20:30</div>';
        return;
      }
      if (e.target.closest('[data-ok]')) { const on = x.decision !== 1;    // its own date: confirmed as it is
        if (await decide([x.sha1], on ? 1 : null)) { x.chosen = on; on ? next() : show(at); } return; }
      if (e.target.closest('[data-only]')) { const on = x.decision !== 0;
        if (await toggle(x)) { if (on) x.chosen = true; on ? next() : show(at); } return; }
      if (e.target.closest('[data-del]')) { const on = !x.delete;
        if (await post('/delete', {path: x.path, delete: on})) { x.delete = on; if (on) x.chosen = true; on ? next() : show(at); } return; }
    }
    const m = e.target.closest('[data-move]'); if (m) return show(at + +m.dataset.move);
    if (!e.target.closest('[data-close]')) return;      // only the close button (or Escape) leaves
  } document.getElementById('vpair').innerHTML = '';     // stops a playing video
  document.getElementById('view').style.display = 'none'; at = -1; render(); };
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;
// clicking into the field starts from the date it would get now, to be edited
document.addEventListener('focusin', e => {
  if (e.target.id !== 'typed' || e.target.value || at < 0) return;
  const t = placed(shown[at])[0];
  if (t) { e.target.value = greek(t); e.target.setSelectionRange(e.target.value.length, e.target.value.length); }
});
document.addEventListener('keydown', async e => {
  if (at < 0) return; const x = shown[at], k = e.key.toLowerCase();
  if (e.target.id === 'typed') {        // typing a date: Enter keeps it and moves on, Escape leaves the field
    if (k === 'enter') { e.preventDefault();
      const id = (e.target.value.match(UUID) || [])[0];
      if (id) {                         // a link to an immich picture (or its id): take that picture's date
        const r = await fetch('/asset?id=' + id);
        if (!r.ok) { document.getElementById('vinfo').insertAdjacentHTML('afterbegin', '<div style="background:#a33;color:#fff;padding:4px">Δεν τη βρίσκω στο immich (ίσως ανέβηκε μετά το τελευταίο ευρετήριο)</div>'); return; }
        const c = await r.json(); x.near = [...(x.near || []).filter(n => n.id !== c.id), c];
        if (await setDate(x, 'asset', c.id)) next(); return;
      }
      if (await setDate(x, 'set', null, e.target.value)) next(); }
    else if (k === 'escape') e.target.blur();
    return;
  }
  if (k === 'escape') return document.getElementById('view').onclick();
  if (k === 'arrowright') show(at + 1); else if (k === 'arrowleft') show(at - 1);
  else if (TOGGLE && k === ' ' && x.datefrom && x.message) {     // Space: the message's date, and on
    e.preventDefault(); if (await setDate(x, 'message')) next(); }
  else if (TOGGLE || PAIR) return;            // these pages: otherwise choices only by mouse
  else if (k === 'a') { await decide([x.sha1], 1); at + 1 < shown.length ? show(at + 1) : show(at); }
  else if (k === 'r' || k === 'delete') { await decide([x.sha1], 0); at + 1 < shown.length ? show(at + 1) : show(at); }
  else if (k === 'u') { await decide([x.sha1], null); show(at); }
  else return;
  e.preventDefault();
});
if (PAIR) { ['all', 'nonesel', 'approve', 'clear'].forEach(id => document.getElementById(id).style.display = 'none');
  for (const id of ['side', 'dated']) document.getElementById(id).style.display = 'none';
  // one button for everything the filters show (e.g. all of "same file"): only immich's
  const rj = document.getElementById('reject'); rj.textContent = 'όσα φαίνονται: μόνο του immich';
  rj.onclick = async () => { const keys = shown.filter(x => x.decision !== 0).map(x => x.sha1);
    if (keys.length && await decide(keys, 0)) render(); };
  for (const id of ['mine', 'conv', 'people', 'vid']) document.getElementById(id).style.display = '';
  bands(); document.getElementById('match').value = 'alike';     // the ones to look at; the same ones go with a link
  const dc = document.getElementById('dec');
  dc.innerHTML = '<option value="">Απόφαση: όλα</option><option value="none">μένουν και οι δύο</option><option value="0">μόνο του immich</option>'; }
if (TOGGLE) { ['all', 'nonesel', 'approve', 'reject', 'clear'].forEach(id => document.getElementById(id).style.display = 'none');
  document.getElementById('sender').style.display = document.getElementById('dchoice').style.display =
    document.getElementById('people').style.display = document.getElementById('vid').style.display =
    document.getElementById('gap').style.display = '';
  for (const id of ['match', 'side', 'dated', 'dec']) document.getElementById(id).style.display = 'none';
  document.getElementById('thr').parentNode.style.display = 'none';
  // who sent it first (the owner, others, then PERSON's conversation), then by date
  items.sort((a, b) => a.sender - b.sender || (a.taken || a.message || 0) - (b.taken || b.message || 0)); }
bands(); render();
</script></html>"""


class Handler(BaseHTTPRequestHandler):
    cache = None
    origin = "archive"
    only = None
    paths = set()

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
                payload = json.dumps(data(self.origin, self.only), ensure_ascii=False).replace("</", "<\\/")
                page, csp = common.page(PAGE)
                return self.send(200, page.replace("__TOGGLE__", "true" if self.origin == "kept" else "false").replace(
                    "__PAIR__", "true" if self.origin == "filtered" else "false").replace(
                    "__PERSON__", html.escape(PERSON or "—")).replace("__PERSON_JS__", json.dumps(PERSON or "—", ensure_ascii=False).replace("</", "<\\/")).replace(
                    "__TZ__", json.dumps(config.TIMEZONE_NAME) if config.TIMEZONE_NAME else "undefined").replace(
                    "__DATA__", payload).encode(), "text/html; charset=utf-8", [("Content-Security-Policy", csp)])
            if url.path == "/video":        # the file itself, for the browser to play
                path = safe(parse_qs(url.query)["p"][0])
                if path.lower().endswith(VIDEO):
                    with open(path, "rb") as f:
                        return self.send(200, f.read(), "video/mp4")
            if url.path == "/asset" and TIMELINE:     # one immich picture, by the id in a pasted link
                aid = parse_qs(url.query).get("id", [""])[0]
                try:
                    return self.send(200, json.dumps(TIMELINE[2](aid)).encode(), "application/json")
                except KeyError:
                    return self.send(404, b"{}", "application/json")
            if url.path == "/typed":        # a typed date, to preview its place: its moment, or 400
                when = typed(parse_qs(url.query).get("text", [""])[0])
                return self.send(200 if when else 400, json.dumps({"ms": when}).encode(), "application/json")
            if url.path == "/around" and TIMELINE:
                return self.send(200, json.dumps(around(int(parse_qs(url.query)["t"][0]))).encode(), "application/json")
            if url.path in ("/thumb", "/big"):
                out = thumb(self.cache, safe(parse_qs(url.query)["p"][0]), 360 if url.path == "/thumb" else 1600)
                if out:
                    with open(out, "rb") as f:
                        return self.send(200, f.read(), "image/jpeg")
        except (KeyError, ValueError):
            pass
        self.send(404, b"not found", "text/plain")

    def do_POST(self):
        if not common.allowed(self):
            return self.send(403, b"forbidden", "text/plain")
        if self.path == "/delete":      # only marks (or unmarks) a kept picture; deleting is done apart, media-triage.py
            req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            path = safe(req["path"])
            with lock:
                db = triage_db()
                if req["delete"]:
                    db.execute("INSERT OR REPLACE INTO triage VALUES (?, 'delete', ?, ?)", (path, "χωρίς ημερομηνία", int(time.time())))
                else:
                    db.execute("INSERT OR REPLACE INTO triage VALUES (?, 'keep', NULL, ?)", (path, int(time.time())))
                db.commit()
            return self.send(200, b"{}", "application/json")
        if self.path == "/date":
            req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            match = config.read_only(MATCH)
            row = match.execute("SELECT path, message FROM match WHERE sha1 = ?", (req["key"],)).fetchone()
            when = typed(req.get("text")) if req["source"] == "set" else None
            if not row or req["source"] not in ("asset", "message", "unknown", "set") or (req["source"] == "asset") != bool(req.get("asset")) \
                    or (req["source"] == "message" and not row[1]) or (req["source"] == "set" and not when):
                return self.send(400, b"{}", "application/json")      # the page says it was not kept
            with lock:
                db = decisions_db()
                db.execute("INSERT OR REPLACE INTO date_from VALUES (?, ?, ?, ?, ?, ?)",
                           (req["key"], row[0], req["source"], req.get("asset"), int(time.time()), when))
                undelete(db, row[0])
                db.commit()
            return self.send(200, json.dumps({"ms": when}).encode(), "application/json")
        if self.path != "/decide":
            return self.send(404, b"not found", "text/plain")
        req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        keys, approved = req["keys"], req["approved"]
        match = config.read_only(MATCH)
        with lock:
            db = decisions_db()
            for key in keys:
                if approved is None:
                    db.execute("DELETE FROM decision WHERE sha1 = ?", (key,))
                    continue
                row = match.execute("SELECT path, coalesce(exact, best), similarity FROM match WHERE sha1 = ?",
                                    (key,)).fetchone()
                if row:
                    db.execute("INSERT OR REPLACE INTO decision VALUES (?, ?, ?, ?, ?, ?)",
                               (key, row[0], int(approved), row[1], row[2], int(time.time())))
                    if approved == 1:
                        undelete(db, row[0])
            db.commit()
        self.send(200, b"{}", "application/json")


def main():
    ap = argparse.ArgumentParser(description="Approve which chat pictures may go into immich.")
    ap.add_argument("--origin", default="archive", choices=("archive", "kept", "filtered"),
                    help="archive: the archive's; kept: what was kept on the vlm-review pages; "
                         "filtered: what the first filter found already in immich")
    ap.add_argument("--port", type=int, default=8519)
    ap.add_argument("--paths", help="show only the files listed here, one path per line")
    ap.add_argument("--only", choices=("hash", "false"),
                    help="hash: only pictures that only the perceptual hash finds in immich; "
                         "false: those whose hash match was not confirmed (chance)")
    args = ap.parse_args()
    os.umask(0o077)
    Handler.origin = args.origin
    Handler.paths = {l.strip() for l in open(args.paths, encoding="utf-8") if l.strip()} if args.paths else set()
    Handler.only = args.only
    Handler.cache = tempfile.mkdtemp(prefix="immich-review-")
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
