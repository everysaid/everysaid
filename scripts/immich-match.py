#!/usr/bin/env python3
"""Find which chat media are already in immich: exactly (SHA-1) or as a similar picture.

    uv run --extra ml --extra torch python scripts/immich-match.py      (--extra rocm on AMD)

Needs the index made by `immich-index.py`. The candidates are the archive's pictures and videos
(`media`). Each gets an embedding with the model immich itself uses (ViT-B-16-SigLIP2, here as
google/siglip2-base-patch16-224 in the HuggingFace cache; same picture: cosine 0.90-0.99 against
immich's own embedding, different pictures about 0.5), and is compared with every immich asset.
Assets without immich's embedding (an index read through the API, which does not give them) get
one here first, from their preview, kept in the index for the next runs. Writes
`<cache>/match.db`: per candidate file its SHA-1, an exact match if any, the most
similar immich asset with the cosine similarity, its own date (EXIF capture date in its recorded time zone, else the
earliest message it is attached to) and immich's date for that asset. Videos are judged by a frame
one second in.
"""
from datetime import datetime, timedelta, timezone
import argparse
import hashlib
import os
import re
import sqlite3
import sys

import numpy as np
import torch
from transformers import AutoModel, AutoProcessor

import common
from common import config

ARCHIVE = config.CACHE                                            # media/<ab>/<sha256><ext>
ARCHIVE_DB = os.path.join(config.DATA, "archive.db")
INDEX = os.path.join(config.CACHE, "immich.db")
OUT = os.path.join(config.CACHE, "match.db")
MODEL = "google/siglip2-base-patch16-224"
TZ = config.TIMEZONE


def sha1(path):
    h = hashlib.sha1()
    with open(path, "rb") as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
    return h.hexdigest()


def picture(src):
    return common.picture(src, 1440)


def exif_dates(paths):
    """path -> capture date (Unix ms) from EXIF DateTimeOriginal, in its own time zone
    (OffsetTimeOriginal) where recorded, else the configured time zone."""
    dates = {}
    for r in common.exiftool_json(["-DateTimeOriginal", "-OffsetTimeOriginal"], paths):
        ms = capture_ms(r.get("DateTimeOriginal"), r.get("OffsetTimeOriginal"))
        if ms:
            dates[os.path.normpath(r["SourceFile"])] = ms
    return dates


def embed_missing(model, proc, device):
    """Embeddings for the assets the index has none for, from their previews, into the index."""
    index = sqlite3.connect(INDEX)
    todo = [(a, p) for a, p in index.execute("SELECT id, preview FROM asset WHERE embedding IS NULL "
                                             "AND preview IS NOT NULL") if os.path.exists(p)]
    if todo:
        print(f"immich: {len(todo)} προεπισκοπήσεις χωρίς embedding", file=sys.stderr)
    for i in range(0, len(todo), 32):
        batch = [(a, im) for a, p in todo[i:i + 32] if (im := picture(p)) is not None]
        if not batch:
            continue
        with torch.no_grad():
            x = proc(images=[im for _, im in batch], return_tensors="pt").to(device)
            emb = torch.nn.functional.normalize(common.features(model.get_image_features(**x)), dim=-1).cpu().numpy()
        index.executemany("UPDATE asset SET embedding = ? WHERE id = ?",
                          [(e.astype(np.float32).tobytes(), a) for (a, _), e in zip(batch, emb)])
        index.commit()


def capture_ms(value, offset):
    try:
        when = datetime.strptime(str(value)[:19], "%Y:%m:%d %H:%M:%S")
    except ValueError:
        return None
    if when.year < 1990:
        return None
    if offset and re.fullmatch(r"[+-]\d\d:\d\d", str(offset)):
        sign = 1 if offset[0] == "+" else -1
        tz = timezone(sign * timedelta(hours=int(offset[1:3]), minutes=int(offset[4:6])))
    else:
        tz = TZ
    return int(when.replace(tzinfo=tz).timestamp() * 1000)


def candidates():
    """(path, origin, message date in Unix ms or None)"""
    db = sqlite3.connect(f"file:{ARCHIVE_DB}?mode=ro", uri=True)
    rows = [(common.media_file(path), "archive", ts) for path, ts in db.execute(
        "SELECT m.path, min(msg.ts) FROM media m JOIN attachment a ON a.sha256 = m.sha256 "
        "JOIN message msg ON msg.id = a.message_id "
        "WHERE m.mime LIKE 'image/%' OR m.mime LIKE 'video/%' OR m.path LIKE '%.heic' GROUP BY m.sha256")]
    return rows


def main():
    argparse.ArgumentParser(description="Find which chat media are already in immich.").parse_args()
    os.umask(0o077)
    os.makedirs(config.CACHE, exist_ok=True)
    device = common.device()
    model = AutoModel.from_pretrained(MODEL).to(device).eval()
    proc = AutoProcessor.from_pretrained(MODEL)
    embed_missing(model, proc, device)
    index = sqlite3.connect(f"file:{INDEX}?mode=ro", uri=True)
    assets = index.execute("SELECT id, sha1, taken, created, embedding FROM asset").fetchall()
    by_sha1 = {a[1]: a[0] for a in assets if a[1]}
    with_emb = [a for a in assets if a[4]]
    if not with_emb:
        sys.exit("Το ευρετήριο του immich δεν έχει embeddings ούτε προεπισκοπήσεις για να γίνουν: "
                 "immich-index.py με την άδεια asset.view, ή --dump.")
    ids = [a[0] for a in with_emb]
    when = {a[0]: a[2] or a[3] for a in assets}
    ref = torch.tensor(np.stack([np.frombuffer(a[4], dtype=np.float32) for a in with_emb])).to(device)
    ref = torch.nn.functional.normalize(ref, dim=-1)

    cands = candidates()
    # files gone since (to the photo library, or removed) keep what the last run found for them
    gone = {c[0] for c in cands if not os.path.exists(c[0])}
    cands = [c for c in cands if c[0] not in gone]
    kept = []
    if os.path.exists(OUT):
        old = sqlite3.connect(f"file:{OUT}?mode=ro", uri=True)
        kept = [r for r in old.execute("SELECT path, origin, sha1, exact, best, similarity, taken, message, "
                                       "immich_date FROM match") if r[0] in gone]
        old.close()
    print(f"{len(cands)} υποψήφια ({len(kept)} κρατήθηκαν από πριν), {len(assets)} στο immich, {device}",
          file=sys.stderr)
    dates = exif_dates([c[0] for c in cands])

    # built beside the old one and put in its place at the end: an interrupted run leaves the old one
    # (whose rows for files since gone cannot be made again)
    if os.path.exists(OUT + ".part"):
        os.remove(OUT + ".part")
    out = sqlite3.connect(OUT + ".part")
    out.execute("""CREATE TABLE match (
        path TEXT PRIMARY KEY, origin TEXT, sha1 TEXT, exact TEXT, best TEXT, similarity REAL,
        taken INTEGER, message INTEGER, immich_date TEXT)""")
    out.executemany("INSERT INTO match VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", kept)

    def flush(batch):
        if not batch:
            return
        with torch.no_grad():
            x = proc(images=[b[1] for b in batch], return_tensors="pt").to(device)
            emb = torch.nn.functional.normalize(common.features(model.get_image_features(**x)), dim=-1)
            sim, idx = (emb @ ref.T).max(dim=1)
        for (c, _), s, j in zip(batch, sim.tolist(), idx.tolist()):
            path, origin, ts = c
            digest = sha1(path)
            best = ids[j]
            out.execute("INSERT INTO match VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                        (path, origin, digest, by_sha1.get(digest), best, round(s, 4), dates.get(path), ts, when[best]))
        batch.clear()

    batch = []
    for i, c in enumerate(cands, 1):
        im = picture(c[0])
        if im is None:
            out.execute("INSERT INTO match (path, origin, sha1, taken, message) VALUES (?, ?, ?, ?, ?)",
                        (c[0], c[1], sha1(c[0]), dates.get(c[0]), c[2]))
        else:
            batch.append((c, im))
        if len(batch) == 32:
            flush(batch)
        if i % 1000 == 0:
            out.commit()
            print(f"  {i}/{len(cands)}", file=sys.stderr)
    flush(batch)
    out.commit()
    n, exact = out.execute("SELECT count(*), count(exact) FROM match").fetchone()
    out.close()
    os.replace(OUT + ".part", OUT)
    print(f"{n} -> {OUT}; ακριβώς ίδια στο immich: {exact}", file=sys.stderr)


if __name__ == "__main__":
    main()
