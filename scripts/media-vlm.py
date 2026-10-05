#!/usr/bin/env python3
"""Ask a local vision-language model what each chat picture is and whether it is worth keeping.

    uv run --extra media python scripts/media-vlm.py                    # everything left after the cheap filters
    uv run --extra media python scripts/media-vlm.py --pilot 200        # a fixed random sample
    uv run --extra media python scripts/media-vlm.py --report           # how far the answers agree with Sonnet's
    uv run --extra media python scripts/media-vlm.py --retry-skipped    # only the pictures set aside earlier

Meant to run for hours in a terminal. Every answer is saved at once, so it can be stopped at any
time (Ctrl-C, closing the terminal, a crash) and simply started again: it carries on with what is
left. When the Ollama service does not answer (the computer slept, the container restarted), it
waits and tries again instead of stopping. A picture the model cannot take (a thin strip, an error
answer from Ollama, an answer that is not valid JSON) is set aside in `skipped` with the reason, and
the run moves on; `--retry-skipped` tries those again. To keep the computer awake meanwhile:
`systemd-inhibit --what=sleep uv run --extra media python scripts/media-vlm.py` on Linux.

Candidates are the files in `<cache>/match.db` (made by `immich-match.py`): the
archive's pictures and videos. Cheap filters come
first and need no model: what is already in immich (same checksum, or a very similar picture taken
at the same time), GIFs (also WhatsApp's, which are mp4: message type 11), WhatsApp's round video
notes (type 54), stickers (webp), pictures under 480 px, and files that are no longer there. Every
other one is shown, as a JPEG of at most 896 px (videos: a frame one second in), to the model
through the local Ollama service (config `[ollama] url`), which answers in a fixed JSON form: kind, value for a personal
library (1-5) and a short description. Answers go to `<data>/vlm.db` (`answer`, one
row per file and model): hours of work, so it lives in the home snapshots, not in `~/.cache`.
Nothing leaves the machine.

The model and prompt were chosen by comparing a few local models on a sample of pictures:
qwen2.5vl:7b with the nine kinds below agreed best; Gemma 3 and Llama 3.2 Vision (looped on the
JSON form) were dropped, and a simpler "personal or not" prompt did worse.
"""
import argparse
import base64
import json
import os
import io
import random
import sqlite3
import sys
import time
import urllib.error
import urllib.request

import common
from common import config

OLLAMA = config.OLLAMA_URL
MATCH = os.path.join(config.CACHE, "match.db")
OUT = os.path.join(config.DATA, "vlm.db")
ARCHIVE = config.MEDIA_STORE                                      # media/<ab>/<sha256><ext>
ARCHIVE_DB = os.path.join(config.DATA, "archive.db")
MODEL = config.OLLAMA_MODEL                                       # [ollama] model
REFERENCE = config.REVIEW_REFERENCE                               # [review] reference_model
# WhatsApp messages (message.subtype, from ZWAMESSAGE.ZMESSAGETYPE) whose files are of no interest here
WHATSAPP_SKIP = {"gif": "gif", "video note": "video note"}
VIDEO = common.VIDEO
MIN_SIDE = 480
SIDE = 896
RETRY = 15                  # seconds between tries when Ollama does not answer
HTTP_TRIES = 3              # an error answer for one picture: try this many times, then skip it
MIN_MODEL_SIDE = 32         # Qwen2.5-VL needs both sides of at least 28 px
KINDS = ["personal_photo", "screenshot", "meme", "greeting", "advert", "document", "news_or_graphic",
         "sticker_or_cartoon", "other"]
PROMPT = """This picture was sent or received in a personal chat (WhatsApp or Viber).
We are deciding which chat pictures deserve a place in his personal photo library.

kind, one of:
- personal_photo: a real photograph taken by someone in his life (people, family, friends, places, home, trips, events, pets, things)
- screenshot: a screenshot of a phone or computer screen
- meme: a joke picture, usually with a caption
- greeting: a greeting card picture (wishes, flowers, name days, holidays, religious images)
- advert: an advertisement, an offer or a product picture from a shop
- document: a photo or scan of a document, receipt, invoice, form, ID or note
- news_or_graphic: a news picture, infographic, poster or other forwarded graphic
- sticker_or_cartoon: a sticker, cartoon, drawing or emoji
- other: none of the above

value for his personal photo library:
5 = a personal moment worth keeping for ever (people he knows, family, gatherings, trips with people)
4 = a personal photo of his life without a clear moment (his home, places he went, pets, his things)
3 = a personal record useful to keep (work done at his house, a car, something he bought)
2 = practical and temporary (information, a receipt, a screenshot of something useful)
1 = no personal value (memes, greetings, adverts, forwarded news, stickers)

description: one short sentence in Greek saying what the picture shows."""
SCHEMA = {"type": "object", "properties": {
    "kind": {"type": "string", "enum": KINDS},
    "value": {"type": "integer", "minimum": 1, "maximum": 5},
    "description": {"type": "string"}}, "required": ["kind", "value", "description"]}


def candidates():
    """(path, reason it is filtered out, or None)"""
    db = config.read_only(MATCH)
    rows = db.execute("""SELECT path,
        exact IS NOT NULL
        OR (similarity >= 0.90 AND taken IS NOT NULL AND abs(taken / 1000 - strftime('%s', substr(immich_date, 1, 19))) <= 120)
        OR (similarity >= 0.90 AND taken IS NULL AND message / 1000 - strftime('%s', substr(immich_date, 1, 19)) BETWEEN -3600 AND 30 * 86400)
        FROM match ORDER BY path""").fetchall()
    archive = config.read_only(ARCHIVE_DB)
    skip = {}
    for path, kind in archive.execute(
            "SELECT DISTINCT 'media/' || substr(a.sha256, 1, 2) || '/' || a.sha256, m.subtype "
            "FROM attachment a JOIN message m ON m.id = a.message_id "
            "JOIN service sv ON sv.id = m.service_id WHERE sv.name = 'whatsapp'"):
        if kind in WHATSAPP_SKIP:
            skip[path] = WHATSAPP_SKIP[kind]
    rows = [(p, i) for p, i in rows if os.path.exists(p)]
    sizes = dims([p for p, _ in rows])
    out = []
    for path, in_immich in rows:
        ext = os.path.splitext(path)[1].lower()
        side = sizes.get(path, 0)
        stem = os.path.splitext(os.path.relpath(path, ARCHIVE))[0] if path.startswith(ARCHIVE) else None
        reason = ("immich" if in_immich else skip.get(stem) or (
            "gif" if ext == ".gif" or "/GIF-" in path else
            "sticker" if ext == ".webp" else "small" if side and side < MIN_SIDE else None))
        out.append((path, reason))
    return out


def dims(paths):
    """path -> longest side in pixels."""
    sizes = {}
    for r in common.exiftool_json(["-n", "-ImageWidth", "-ImageHeight"], paths):
        w, h = r.get("ImageWidth"), r.get("ImageHeight")
        if isinstance(w, (int, float)) and isinstance(h, (int, float)):
            sizes[os.path.normpath(r["SourceFile"])] = max(w, h)
    return sizes


def jpeg(src):
    im = common.picture(src, SIDE)
    if im is None:
        return None
    if min(im.size) < MIN_MODEL_SIDE:   # a thin strip (a banner): the model cannot take it
        return "strip"
    buf = io.BytesIO()
    im.save(buf, "JPEG", quality=85)
    return base64.b64encode(buf.getvalue()).decode()


def ask(image):
    body = json.dumps({"model": MODEL, "stream": False, "format": SCHEMA,
                       "options": {"temperature": 0, "num_predict": 300}, "keep_alive": "10m",
                       "messages": [{"role": "user", "content": PROMPT, "images": [image]}]}).encode()
    req = urllib.request.Request(f"{OLLAMA}/api/chat", body, {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=300) as r:
        return json.loads(json.load(r)["message"]["content"])


def ask_patiently(image):
    """Ask; while Ollama is unreachable (sleep, restart), wait and try again. If Ollama answers with
    an error for this picture a few times, or the answer is not valid JSON, give the reason (a string)
    so that the picture is set aside."""
    errors = 0
    while True:
        try:
            return ask(image)
        except json.JSONDecodeError:
            return "bad answer"
        except urllib.error.HTTPError as e:
            errors += 1
            if errors >= HTTP_TRIES:
                return f"error {e.code}"
            time.sleep(RETRY)
        except (urllib.error.URLError, ConnectionError, TimeoutError, OSError) as e:
            status(f"το Ollama δεν απαντά ({type(e).__name__}), ξαναδοκιμάζω σε {RETRY} s…", final=True)
            time.sleep(RETRY)


def status(text, final=False):
    sys.stderr.write("\r\033[K" + text + ("\n" if final else ""))
    sys.stderr.flush()


def report(db):
    rows = db.execute("SELECT path, model, kind, value FROM answer WHERE model IN (?, ?)", (MODEL, REFERENCE)).fetchall()
    by = {}
    for path, model, kind, value in rows:
        by.setdefault(path, {})[model] = (kind, value)
    both = [a for a in by.values() if len(a) == 2]
    print(f"{db.execute('SELECT count(*) FROM answer WHERE model = ?', (MODEL,)).fetchone()[0]} απαντήσεις του {MODEL}")
    if both:
        same = sum((a[MODEL][1] >= 3) == (a[REFERENCE][1] >= 3) for a in both)
        miss = sum(a[REFERENCE][1] >= 3 and a[MODEL][1] < 3 for a in both)
        print(f"απέναντι στο {REFERENCE} ({len(both)} κοινές): ίδια απόφαση {same}, χάνει {miss}")
    aside = db.execute("SELECT reason, count(*) FROM skipped GROUP BY 1").fetchall()
    if aside:
        print("στην άκρη:", ", ".join(f"{r}: {n}" for r, n in aside), "(--retry-skipped για να ξαναδοκιμαστούν)")


def main():
    ap = argparse.ArgumentParser(description="Ask a local vision model about the chat pictures (resumable).")
    ap.add_argument("--pilot", type=int, help="only a fixed random sample of this many")
    ap.add_argument("--report", action="store_true", help="how far the answers agree with Sonnet's")
    ap.add_argument("--retry-skipped", action="store_true", help="only the pictures set aside earlier")
    args = ap.parse_args()
    os.umask(0o077)
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    db = sqlite3.connect(OUT)
    db.execute("""CREATE TABLE IF NOT EXISTS answer (path TEXT, model TEXT, kind TEXT, value INTEGER,
                  description TEXT, seconds REAL, PRIMARY KEY (path, model))""")
    db.execute("CREATE TABLE IF NOT EXISTS filtered (path TEXT PRIMARY KEY, reason TEXT)")
    db.execute("CREATE TABLE IF NOT EXISTS skipped (path TEXT PRIMARY KEY, reason TEXT, at INTEGER)")
    if args.report:
        return report(db)

    status("ψάχνω τα αρχεία και εφαρμόζω τα φθηνά φίλτρα…")
    cands = candidates()
    db.execute("DELETE FROM filtered")
    db.executemany("INSERT INTO filtered VALUES (?, ?)", [c for c in cands if c[1]])
    db.commit()
    todo = [p for p, reason in cands if not reason]
    if args.pilot:
        todo = sorted(random.Random(42).sample(todo, min(args.pilot, len(todo))))
    done = {r[0] for r in db.execute("SELECT path FROM answer WHERE model = ?", (MODEL,))}
    aside = {r[0] for r in db.execute("SELECT path FROM skipped")}
    if args.retry_skipped:
        left = [p for p in todo if p in aside and p not in done]
    else:
        left = [p for p in todo if p not in done and p not in aside]
    filtered = {}
    for _, reason in cands:
        if reason:
            filtered[reason] = filtered.get(reason, 0) + 1
    status(f"{len(cands)} αρχεία · εκτός από φίλτρα: {filtered} · για το μοντέλο {len(todo)}, "
           f"έγιναν ήδη {len(done)}, στην άκρη {len(aside)}, μένουν {len(left)}", final=True)

    kept = db.execute("SELECT count(*) FROM answer WHERE model = ? AND value >= 3", (MODEL,)).fetchone()[0]
    started, n = time.time(), 0
    try:
        for path in left:
            image = jpeg(path)
            a = None
            if image is None:
                a = "no picture"
            elif image == "strip":
                a = "strip"
            else:
                t = time.time()
                a = ask_patiently(image)
            if isinstance(a, str):          # set aside, with the reason, and move on
                db.execute("INSERT OR REPLACE INTO skipped VALUES (?, ?, ?)", (path, a, int(time.time())))
                db.commit()
                continue
            db.execute("DELETE FROM skipped WHERE path = ?", (path,))
            db.execute("INSERT OR REPLACE INTO answer VALUES (?, ?, ?, ?, ?, ?)",
                       (path, MODEL, a.get("kind"), a.get("value"), a.get("description"), time.time() - t))
            db.commit()
            n += 1
            kept += a.get("value", 0) >= 3
            rate = (time.time() - started) / n
            eta = rate * (len(left) - n)
            status(f"{len(done) + n}/{len(todo)} · {rate:.1f} s/εικόνα · μένουν ~{eta / 3600:.1f} ώρες · "
                   f"κρατάει μέχρι τώρα {kept}")
    except KeyboardInterrupt:
        status(f"σταμάτησε· έγιναν {n} σε αυτή την εκτέλεση. Ξανατρέξε το για να συνεχίσει.", final=True)
        return
    status(f"τέλος· έγιναν {n} σε αυτή την εκτέλεση, κρατάει συνολικά {kept}.", final=True)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:           # before the work started: nothing to save
        status("σταμάτησε πριν ξεκινήσει.", final=True)
