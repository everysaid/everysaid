#!/usr/bin/env python3
"""Check the immich match of each candidate with a perceptual hash, and by capture time and size.

    uv run --extra media python scripts/immich-phash.py [--origin archive]

CLIP squeezes a picture into a small square before comparing, so a tall portrait and its own copy
can score as low as 0.89. Two further checks, against the asset `immich-match.py` found:
the perceptual hash (pHash, 64 bits) of both pictures as shown (auto-oriented; the immich preview,
read only), and whether both were taken in the same second (±2 s) at the same size (either
orientation). Adds `phash` (Hamming distance, 0 = same) and `same_shot` (1/0) to
`<cache>/match.db`. Videos are left out.
"""
import argparse
import os
import sqlite3
import sys
from datetime import datetime

import imagehash

import common
from common import config

MATCH = os.path.join(config.CACHE, "match.db")
INDEX = os.path.join(config.CACHE, "immich.db")
VIDEO = common.VIDEO


def phash(path):
    im = common.picture(path, 512)
    return imagehash.phash(im) if im is not None else None


def size(path):
    w, h = common.dimensions(path)
    return tuple(sorted((w, h))) if w and h else None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--origin", default="archive")
    args = ap.parse_args()
    db = sqlite3.connect(MATCH)
    cols = {r[1] for r in db.execute("PRAGMA table_info(match)")}
    for col, kind in (("phash", "INTEGER"), ("same_shot", "INTEGER")):
        if col not in cols:
            db.execute(f"ALTER TABLE match ADD COLUMN {col} {kind}")
    index = config.read_only(INDEX)
    asset = {r[0]: r[1:] for r in index.execute("SELECT id, preview, taken, width, height FROM asset")}
    rows = db.execute("SELECT path, coalesce(exact, best), taken FROM match WHERE origin = ?", (args.origin,)).fetchall()
    done = 0
    for path, aid, taken in rows:
        if not os.path.exists(path) or path.lower().endswith(VIDEO) or aid not in asset:
            continue
        preview, itaken, w, h = asset[aid]
        a, b = phash(path), phash(preview) if preview and os.path.exists(preview) else None
        dist = int(a - b) if a is not None and b is not None else None
        same = None
        if taken and itaken and w and h:
            t = datetime.fromisoformat(itaken.replace("+00", "+00:00")).timestamp() * 1000
            same = int(abs(taken - t) <= 2000 and size(path) == tuple(sorted((int(w), int(h)))))
        db.execute("UPDATE match SET phash = ?, same_shot = ? WHERE path = ?", (dist, same, path))
        done += 1
    db.commit()
    n = db.execute("SELECT count(*), sum(phash <= 10), sum(same_shot) FROM match WHERE origin = ? AND phash IS NOT NULL",
                   (args.origin,)).fetchone()
    print(f"{done} έλεγχοι · ίδια κατά hash (≤ 10): {n[1]} · ίδια λήψη (ώρα και μέγεθος): {n[2]}", file=sys.stderr)


if __name__ == "__main__":
    main()
