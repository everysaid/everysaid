#!/usr/bin/env python3
"""Replace local copies of chat media that are in the photo library (immich) by a link to it.

    uv run python scripts/media-prune.py LIST [--dry-run] [--done FILE]

LIST has one line per file: the archive path (<cache>/media/...), the immich asset id, the method
(checksum, phash, clip) and the score, tab-separated, then any of its links in the aside folder
(config `[media] aside`), which go with it. An asset id of "-" means the file is not in
immich and is simply removed (the owner's choice): no link, the message only knows it had media. With
the method "similar" the id is instead the sha256 of a look-alike the archive keeps (the owner judged
them the same picture): the record is `media_same` (file -> the one kept), which must still be
there. Otherwise the archive gets a row in `library_link` (media -> immich asset). Then every local
copy is removed: the archive's hard link and the source files it was linked from (each source's
files are under its `source.media_root`). They are all hard links of one
file, or separate files with the same content (checked); a file is only removed when the copies
found are all of its links, so no unknown copy is left behind. The iPhone sync does not bring them back: it
asks the archive which files it has already taken (`attachment.source_path`, kept after the file is
gone). The encrypted iPhone backup is never touched. A file that a removed look-alike points to
(`media_same.same_as`) is not removed: it is the only copy left of both. `--done FILE` gets the
archive path of each file removed (with --dry-run: each that would be).

After this the archive is the only record of these files; it lives in the home folder, in the
home snapshots and backups.
"""
import argparse
import hashlib
import os
import sqlite3
import sys
import time

import common
from common import config
from chronika.archive import expand

ARCHIVE_DB = os.path.join(config.DATA, "archive.db")

def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
    return h.hexdigest()


ap = argparse.ArgumentParser(description="Replace local media that are in immich by links.")
ap.add_argument("list")
ap.add_argument("--dry-run", action="store_true")
ap.add_argument("--done", help="write here the archive path of each file removed (or, with --dry-run, to be)")
args = ap.parse_args()
ASIDE = os.path.join(os.path.normpath(config.ASIDE), "")
db = sqlite3.connect(ARCHIVE_DB)
db.execute("PRAGMA foreign_keys = ON")
done = skipped = 0
removed = open(args.done, "w") if args.done else None
for line in open(args.list):
    path, asset, method, score, *extra = line.rstrip("\n").split("\t")
    sha = os.path.splitext(os.path.basename(path))[0]
    row = db.execute("SELECT path FROM media WHERE sha256 = ?", (sha,)).fetchone()
    if not row or common.media_file(row[0]) != os.path.normpath(path):
        print("άγνωστο στη βάση:", path, file=sys.stderr); skipped += 1; continue
    if any(not os.path.normpath(e).startswith(ASIDE) for e in extra):
        print("αντίγραφο έξω από τον φάκελο aside:", path, extra, file=sys.stderr); skipped += 1; continue
    kept_for = [s for (s,) in db.execute("SELECT sha256 FROM media_same WHERE same_as = ?", (sha,))]
    if kept_for:
        print("κρατιέται για τις όμοιές του που σβήστηκαν (media_same):", path, kept_for, file=sys.stderr)
        skipped += 1; continue
    copies = {p for p in [path, *extra] if os.path.exists(p)}
    for root, rel in db.execute("SELECT s.media_root, a.source_path FROM attachment a JOIN source s ON s.id = a.source_id "
                                "WHERE a.sha256 = ? AND s.media_root IS NOT NULL", (sha,)):
        full = os.path.join(expand(root), *rel.split("/"))
        if os.path.exists(full):
            copies.add(full)
    # Copies may be separate files with the same content (the same picture received twice); each
    # must really have that content, and each file's links must all be among the copies found.
    by_inode = {}
    for c in copies:
        st = os.stat(c)
        by_inode.setdefault((st.st_dev, st.st_ino), []).append(c)
    bad = [c for paths in by_inode.values() for c in paths[:1] if sha256(c) != sha]
    unknown = [p[0] for p in by_inode.values() if os.stat(p[0]).st_nlink != len(p)]
    if bad or unknown:
        print("άλλο περιεχόμενο ή άγνωστα αντίγραφα:", path, bad + unknown, file=sys.stderr); skipped += 1; continue
    if method == "similar":
        twin = db.execute("SELECT path FROM media WHERE sha256 = ?", (asset,)).fetchone()
        if asset == sha or not twin or not os.path.exists(common.media_file(twin[0])):
            print("η όμοια που μένει δεν είναι στο archive:", path, asset, file=sys.stderr); skipped += 1; continue
    if args.dry_run:
        done += 1
        if removed:
            removed.write(path + "\n")
        continue
    if method == "similar":
        db.execute("INSERT OR REPLACE INTO media_same (sha256, same_as, method, score, linked_at) VALUES (?, ?, ?, ?, ?)",
                   (sha, asset, method, float(score), int(time.time())))
    elif asset != "-":
        db.execute("INSERT OR REPLACE INTO library_link VALUES (?, 'immich', ?, ?, ?, ?)",
                   (sha, asset, method, float(score), int(time.time())))
    db.commit()                         # the record first, then the files
    for c in copies:
        os.remove(c)
    done += 1
    if removed:
        removed.write(path + "\n"); removed.flush()
print(f"{'θα αφαιρεθούν' if args.dry_run else 'αφαιρέθηκαν'}: {done}, παραλείφθηκαν: {skipped}")
