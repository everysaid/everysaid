#!/usr/bin/env python3
"""Carry out the owner's decisions from the review page (`triage` in <data>/review.db).

    uv run --extra media python scripts/media-triage.py [--dry-run [--plan FILE]] [--only FILE]

The page (`vlm-review.py`) only records a decision per file; this does the work:

- delete: through `media-prune.py` with no library asset: every local copy is removed (archive and
  sources, checked to be the same content), the archive keeps its record, and its copy is refreshed;
  a look-alike removed on `vlm-review.py --similar` (label DUP) is linked instead to the closest
  file of its group that stays (`media_same`); one no longer in a group, or a group with none left,
  is not touched;
- aside: a hard link (or a copy) in `<aside>/<label>/<kind>/`, as `media-aside.py` makes
  them, the label being where the file came from (the person or the group), and a row in `aside`, so
  the pages leave it out;
- keep: nothing yet; these wait for the upload to the photo library.

A delete is carried out only when it is the newest decision about the file in every table of
review.db (`triage`, `aside_decision`, `aside`, `decision` and `date_from` of `immich-review.py`, by
way of match.db, and `restored`, a file brought back by `media-restore.py`); an older one is skipped
and listed. Files set aside (`aside`, or with a row in `aside_decision`) are decided apart: only
`--table aside_decision` deletes them, and their links in the aside folder go with them.

Only the archive's own files can be set aside or deleted here. Decisions already carried out (file
gone, or already aside) are skipped, so it can be run again after every session on the page.
`--plan FILE` (with --dry-run) writes what would be done, one `delete|aside<TAB>path` per line;
`--only FILE` then does no more than the lines of such a file (`vlm-review.py`'s two clicks).
"""
import argparse
import hashlib
import importlib.util
import os
import re
import sqlite3
import subprocess
import sys
import tempfile
import time
from datetime import datetime

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("media_aside", os.path.join(HERE, "media-aside.py"))
aside_mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(aside_mod)

ARCHIVE = aside_mod.ARCHIVE
ARCHIVE_DB = aside_mod.ARCHIVE_DB
DECISIONS = aside_mod.DECISIONS
DUP = "διπλότυπο"
SIMILAR = os.path.join(aside_mod.config.CACHE, "similar.tsv")
VECTORS = os.path.join(aside_mod.config.CACHE, "similar.npz")
MATCH = os.path.join(aside_mod.config.CACHE, "match.db")          # sha1 -> path, for immich-review.py's tables


def twins(rows):
    """For each look-alike to remove: (sha256 of the closest file of its group that stays, cosine), or None."""
    if not os.path.exists(SIMILAR):
        return {}
    import numpy as np
    group = dict(line.rstrip("\n").split("\t", 1)[::-1] for line in open(SIMILAR, encoding="utf-8"))
    v = np.load(VECTORS)
    vec = dict(zip(v["paths"].tolist(), v["vectors"]))
    gone = {p for p, a, _, _ in rows if a == "delete"}
    out = {}
    for p, a, label, _ in rows:
        if a != "delete" or label != DUP or p not in group:
            continue
        stay = [q for q, g in group.items() if g == group[p] and q not in gone and os.path.exists(q)
                and aside_mod.common.in_media(q)]
        if stay:
            best = max(stay, key=lambda q: float(vec[q] @ vec[p]))
            out[p] = (os.path.splitext(os.path.basename(best))[0], float(vec[best] @ vec[p]))
        else:
            out[p] = None
    return out


def same_file(copy, path, sha):
    """copy is path's link in the aside folder, or a copy of it (made where links cannot be)."""
    a, b = os.stat(copy), os.stat(path)
    if (a.st_dev, a.st_ino) == (b.st_dev, b.st_ino):
        return True
    if not copy.endswith(f"_{sha[:8]}{os.path.splitext(path)[1]}") or a.st_size != b.st_size:
        return False
    h = hashlib.sha256()
    with open(copy, "rb") as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
    return h.hexdigest() == sha


def folder(label):
    """'👥 Family · Viber' -> 'Family'; '👤 Name ·' -> 'Name'."""
    label = re.sub(r"^[👤👥]\s*", "", label or "χωρίς προέλευση")
    label = re.sub(r"\s·\s(Viber|WhatsApp|SMS|iMessage)$", "", label).rstrip(" ·").strip()
    return re.sub(r'[/\\:]', "-", label) or "χωρίς προέλευση"


def sha_of(path):
    return os.path.splitext(os.path.basename(path))[0]


def has(db, table):
    return db.execute("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", (table,)).fetchone()


def history(dec):
    """sha256 -> [(when, table, key, what)]: every decision about each archive file, in every table."""
    out = {}
    def add(path_or_sha, at, table, key, what):
        sha = sha_of(path_or_sha) if os.sep in path_or_sha or "/" in path_or_sha else path_or_sha
        out.setdefault(sha, []).append((int(at), table, key, what))
    for table in ("triage", "aside_decision"):
        if has(dec, table):
            for path, action, at in dec.execute(f"SELECT path, action, at FROM {table}"):
                add(path, at, table, path, action)
    if has(dec, "aside"):
        for sha, at in dec.execute("SELECT sha256, at FROM aside"):
            add(sha, at, "aside", sha, "aside")
    if has(dec, "restored"):
        for sha, at in dec.execute("SELECT sha256, at FROM restored"):
            add(sha, at, "restored", sha, "restored")
    # immich-review.py's, by SHA-1: the path it was recorded with, and the one match.db has now
    paths = {}
    if os.path.exists(MATCH):
        for sha1, path in aside_mod.config.read_only(MATCH).execute("SELECT sha1, path FROM match"):
            paths.setdefault(sha1, set()).add(path)
    for table, what, at in (("decision", "approved", "decided_at"), ("date_from", "source", "decided_at")):
        if has(dec, table):
            for sha1, path, value, when in dec.execute(f"SELECT sha1, path, {what}, {at} FROM {table}"):
                for p in {path} | paths.get(sha1, set()):
                    if aside_mod.common.in_media(p):
                        add(p, when, table, sha1, f"{what}={value}")
    return out


def aside_links(wanted):
    """sha256 -> its links (or copies) anywhere in the aside folder, for the archive files `wanted`
    (sha256 -> archive path)."""
    by_inode, by_name = {}, {}
    for sha, path in wanted.items():
        st = os.stat(path)
        by_inode[(st.st_dev, st.st_ino)] = sha
        by_name[f"_{sha[:8]}{os.path.splitext(path)[1]}"] = sha
    out = {}
    for d, _, names in os.walk(aside_mod.ASIDE):
        for n in names:
            f = os.path.join(d, n)
            st = os.stat(f)
            sha = by_inode.get((st.st_dev, st.st_ino)) or next((v for k, v in by_name.items() if n.endswith(k)), None)
            if sha and same_file(f, wanted[sha], sha):
                out.setdefault(sha, []).append(f)
    return out


def main():
    ap = argparse.ArgumentParser(description="Carry out the keep / aside / delete decisions.")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--table", default="triage", choices=("triage", "aside_decision"),
                    help="aside_decision: the decisions on the files set aside, kept apart from the rest")
    ap.add_argument("--plan", help="with --dry-run: write what would be done here (delete|aside TAB path)")
    ap.add_argument("--only", help="do no more than the lines of a --plan file")
    args = ap.parse_args()
    os.umask(0o077)
    dec = sqlite3.connect(DECISIONS)
    dec.execute("CREATE TABLE IF NOT EXISTS aside (sha256 TEXT PRIMARY KEY, label TEXT NOT NULL, at INTEGER NOT NULL)")
    rows = dec.execute(f"SELECT path, action, label, at FROM {args.table}").fetchall() if has(dec, args.table) else []
    if args.only:
        with open(args.only, encoding="utf-8") as f:
            planned = {tuple(l.rstrip("\n").split("\t", 1)) for l in f if l.strip()}
        rows = [r for r in rows if (r[1], r[0]) in planned]
    already = {r[0] for r in dec.execute("SELECT sha256 FROM aside")}
    # set aside: decided apart, never deleted by the main decisions
    apart = already | ({sha_of(p) for (p,) in dec.execute("SELECT path FROM aside_decision")}
                       if has(dec, "aside_decision") else set())
    decided = history(dec)
    arch = aside_mod.config.read_only(ARCHIVE_DB)
    info = {sha: (path, mime, ts) for sha, path, mime, ts in arch.execute(
        "SELECT md.sha256, md.path, md.mime, min(m.ts) FROM media md JOIN attachment a ON a.sha256 = md.sha256 "
        "JOIN message m ON m.id = a.message_id GROUP BY md.sha256")}

    keep = sum(1 for r in rows if r[1] == "keep" and os.path.exists(r[0]))      # still here: not uploaded yet
    twin = twins(rows)
    prune, put_aside, skipped, older, unaside, plan = [], 0, [], [], {}, []
    for path, action, label, at in rows:
        if action == "keep":
            continue
        sha = sha_of(path)
        if not aside_mod.common.in_media(path) or sha not in info:
            if os.path.exists(path):        # not there: gone before
                skipped.append((path, "όχι αρχείο του archive"))
            continue
        if not os.path.exists(path) or (sha in already and action != "delete"):
            continue                    # done before
        if action == "delete":
            if args.table == "triage" and sha in apart:
                skipped.append((path, "στην άκρη: αποφασίζεται μόνο με --table aside_decision"))
                continue
            newer = [h for h in decided.get(sha, ()) if h[0] >= int(at) and (h[1], h[2]) != (args.table, path)]
            if newer:
                when, table, _, what = max(newer)
                older.append((path, f"νεότερη απόφαση: {table} {what}, {datetime.fromtimestamp(when):%Y-%m-%d %H:%M}"))
                continue
            if sha in already:
                unaside[sha] = path     # set aside before, now to go: its links in the aside folder go with it
            if label != DUP:
                prune.append([path, "-", "deleted", "0"])
            elif path not in twin:
                skipped.append((path, "δεν είναι πια σε ομάδα όμοιων (similar.tsv)"))
            elif twin[path]:
                prune.append([path, twin[path][0], "similar", f"{twin[path][1]:.4f}"])
            else:
                skipped.append((path, "όλη η ομάδα όμοιων σβήνεται, καμία δεν μένει"))
            continue
        rel, mime, ts = info[sha]
        when = datetime.fromtimestamp(ts / 1000, aside_mod.TZ).strftime("%Y-%m-%d_%H%M%S")
        dest = os.path.join(aside_mod.ASIDE, folder(label), aside_mod.kind(mime), f"{when}_{sha[:8]}{os.path.splitext(rel)[1]}")
        if not args.dry_run:
            if not os.path.exists(dest):
                os.makedirs(os.path.dirname(dest), exist_ok=True)
                aside_mod.link_or_copy(path, dest)
            dec.execute("INSERT OR REPLACE INTO aside VALUES (?, ?, ?)", (sha, folder(label), int(time.time())))
        plan.append(f"aside\t{path}\n")
        put_aside += 1
    dec.commit()

    # Files set aside and now to be deleted: their links in the aside folder (the same inode, or a
    # copy) are handed to media-prune.py with the other copies, so they go only when it removes the
    # file, after its checks; a dry run sees them the same way.
    if unaside:
        links = aside_links(unaside)
        for line in prune:
            line += links.get(sha_of(line[0]), [])
    print(f"κράτα: {keep} (περιμένουν το immich) · στην άκρη: {put_aside} · σβήσιμο: {len(prune)}"
          + (" (δοκιμή)" if args.dry_run else ""), flush=True)
    for path, why in older:
        print(f"  δεν σβήνεται ({why}): {path}")
    for path, why in skipped[:10]:
        print(f"  παραλείφθηκε ({why}): {path}")
    sys.stdout.flush()
    result = 0
    if prune:
        with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False) as f:
            f.writelines("\t".join(line) + "\n" for line in prune)
        done = f.name + ".done"
        cmd = [sys.executable, os.path.join(HERE, "media-prune.py"), f.name, "--done", done] + (["--dry-run"] if args.dry_run else [])
        result = subprocess.run(cmd).returncode
        os.remove(f.name)
        if os.path.exists(done):
            with open(done, encoding="utf-8") as d:
                plan += [f"delete\t{p}" for p in d]
            os.remove(done)
    if args.plan:
        with open(args.plan, "w", encoding="utf-8") as f:
            f.writelines(plan)
    sys.exit(result)


if __name__ == "__main__":
    main()
