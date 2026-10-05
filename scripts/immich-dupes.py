#!/usr/bin/env python3
"""Find, by perceptual hash, every chat picture that is already in immich, and chat pictures that
are copies of each other (the same picture sent twice, compressed differently).

    uv run --extra media python scripts/immich-dupes.py

All against all, like a duplicate finder (Czkawka), rather than only against CLIP's best match:
1. pHash (64 bits) of every immich asset's preview (read only, as `immich-index.py` found it),
   kept in `<cache>/immich.db` (`asset.phash`), so later runs only hash new assets;
2. pHash of every chat picture in `<cache>/match.db`, as shown (auto-oriented);
3. for each chat picture, the nearest immich asset by Hamming distance (`hash_asset`,
   `hash_dist`), and a group number shared by chat pictures within DUP of each other
   (`hash_group`). Videos are left out. Uses all cores.
4. a pHash this close can still be chance (a landscape news clipping and a portrait painting
   once scored 6), so each pair within SAME is confirmed: the same aspect ratio as shown
   (`hash_aspect`, within 6%) and a second, different hash, dHash (`hash_dhash`). The pair is the
   same picture when hash_dist <= SAME, hash_aspect = 1 and hash_dhash <= DHASH_SAME.
"""
import argparse
import os
import sqlite3
import sys
from concurrent.futures import ProcessPoolExecutor

import imagehash
import numpy as np

import common
from common import config

INDEX = os.path.join(config.CACHE, "immich.db")
MATCH = os.path.join(config.CACHE, "match.db")
VIDEO = common.VIDEO
DUP = 6          # chat pictures this close to each other are one group
SAME = 6         # a chat picture this close to an immich asset may be the same picture...
DHASH_SAME = 10  # ...if its dHash is this close too and the aspect ratio agrees


def signed(h):
    """imagehash -> signed 64-bit int, for SQLite."""
    n = int(str(h), 16)
    return n - (1 << 64) if n >= 1 << 63 else n


def hash_file(path):
    im = common.picture(path, 512)
    return path, signed(imagehash.phash(im)) if im is not None else None


def hash_all(paths, label):
    out = {}
    with ProcessPoolExecutor(max(1, os.cpu_count() - 2)) as pool:
        for i, (path, h) in enumerate(pool.map(hash_file, paths, chunksize=32), 1):
            out[path] = h
            if i % 2000 == 0:
                print(f"  {label}: {i}/{len(paths)}", file=sys.stderr)
    return out


def confirm(pair):
    """(path, immich preview) -> (path, same aspect ratio as shown, dHash distance)"""
    path, preview = pair
    pics = [common.picture(p, 512) for p in (path, preview)]
    if None in pics:
        return path, None, None
    ra, rb = (im.width / im.height for im in pics)
    return path, int(abs(ra - rb) / max(ra, rb) <= 0.06), int(imagehash.dhash(pics[0]) - imagehash.dhash(pics[1]))


def popcount(x):
    x = x - ((x >> 1) & 0x5555555555555555)
    x = (x & 0x3333333333333333) + ((x >> 2) & 0x3333333333333333)
    x = (x + (x >> 4)) & 0x0F0F0F0F0F0F0F0F
    return (x * 0x0101010101010101) >> 56


def main():
    argparse.ArgumentParser(description="Find chat pictures already in immich, and copies, by perceptual hash.").parse_args()
    index = sqlite3.connect(INDEX)
    if "phash" not in {r[1] for r in index.execute("PRAGMA table_info(asset)")}:
        index.execute("ALTER TABLE asset ADD COLUMN phash INTEGER")
    todo = index.execute("SELECT id, preview FROM asset WHERE phash IS NULL AND preview IS NOT NULL").fetchall()
    print(f"immich: {len(todo)} previews to hash", file=sys.stderr)
    hashes = hash_all([p for _, p in todo], "immich")
    index.executemany("UPDATE asset SET phash = ? WHERE id = ?", [(hashes[p], i) for i, p in todo])
    index.commit()
    ids, ih = zip(*index.execute("SELECT id, phash FROM asset WHERE phash IS NOT NULL"))
    ih = np.array(ih, dtype=np.int64).view(np.uint64)

    match = sqlite3.connect(MATCH)
    cols = {r[1] for r in match.execute("PRAGMA table_info(match)")}
    for col, kind in (("hash_asset", "TEXT"), ("hash_dist", "INTEGER"), ("hash_group", "INTEGER"), ("own_phash", "INTEGER"),
                      ("hash_aspect", "INTEGER"), ("hash_dhash", "INTEGER")):
        if col not in cols:
            match.execute(f"ALTER TABLE match ADD COLUMN {col} {kind}")
    paths = [p for (p,) in match.execute("SELECT path FROM match")
             if os.path.exists(p) and not p.lower().endswith(VIDEO)]
    print(f"chats: {len(paths)} pictures to hash", file=sys.stderr)
    ch = hash_all(paths, "chats")
    paths = [p for p in paths if ch[p] is not None]
    cv = np.array([ch[p] for p in paths], dtype=np.int64).view(np.uint64)

    best_i, best_d = [], []
    for k in range(0, len(cv), 256):
        d = popcount(cv[k:k + 256, None] ^ ih[None, :]).astype(np.int16)
        best_i += d.argmin(axis=1).tolist()
        best_d += d.min(axis=1).tolist()

    # chat pictures that are copies of each other: union-find over pairs within DUP
    parent = list(range(len(cv)))

    def find(a):
        while parent[a] != a:
            parent[a] = parent[parent[a]]
            a = parent[a]
        return a

    for k in range(0, len(cv), 512):
        d = popcount(cv[k:k + 512, None] ^ cv[None, :])
        for a, b in zip(*np.nonzero(d <= DUP)):
            if k + a < b:
                parent[find(k + a)] = find(int(b))
    groups = {}
    for a in range(len(cv)):
        groups.setdefault(find(a), []).append(a)
    group = {}
    for n, members in enumerate((m for m in groups.values() if len(m) > 1), 1):
        for a in members:
            group[a] = n

    match.executemany("UPDATE match SET hash_asset = ?, hash_dist = ?, hash_group = ?, own_phash = ? WHERE path = ?",
                      [(ids[best_i[a]], int(best_d[a]), group.get(a), ch[p], p) for a, p in enumerate(paths)])
    preview = dict(index.execute("SELECT id, preview FROM asset"))
    pairs = [(p, preview[ids[best_i[a]]]) for a, p in enumerate(paths) if best_d[a] <= SAME]
    with ProcessPoolExecutor(max(1, os.cpu_count() - 2)) as pool:
        checked = list(pool.map(confirm, pairs, chunksize=16))
    match.executemany("UPDATE match SET hash_aspect = ?, hash_dhash = ? WHERE path = ?",
                      [(aspect, dh, p) for p, aspect, dh in checked])
    match.commit()
    confirmed = sum(1 for _, aspect, dh in checked if aspect == 1 and dh is not None and dh <= DHASH_SAME)
    print(f"ζεύγη με pHash ≤ {SAME}: {len(checked)}, επιβεβαιωμένα (αναλογία και dHash): {confirmed}", file=sys.stderr)
    dist = np.array(best_d)
    print(f"{len(paths)} εικόνες συνομιλιών · ίδιες με immich (απόσταση ≤ 4): {int((dist <= 4).sum())}, "
          f"≤ 8: {int((dist <= 8).sum())}, ≤ 12: {int((dist <= 12).sum())} · ομάδες αντιγράφων μεταξύ τους: "
          f"{len(set(group.values()))} ({len(group)} εικόνες)", file=sys.stderr)


if __name__ == "__main__":
    main()
