#!/usr/bin/env python3
"""Make a schema-v2 copy of an archive of schema v1, and check it.

    uv run python scripts/archive-v2.py SRC OUT

SRC is read only (copied with SQLite's backup, consistent even while open elsewhere); OUT, which
must not exist, gets the copy and the migration (`chronika/migrate.py`, one transaction). Then:
the counts of every table that holds history are compared with SRC's, and integrity, foreign
keys and the full-text index are checked, and OUT is vacuumed. Putting OUT in SRC's place (with
the archive closed everywhere) is left to the owner.
"""
import argparse
import os
import sqlite3
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
from chronika import migrate  # noqa: E402

KEPT = ("message", "call", "conversation", "conversation_member", "message_origin", "call_origin",
        "attachment", "media", "reaction", "call_member", "address", "source", "viber_member", "blocked")

ap = argparse.ArgumentParser(description="Make a schema-v2 copy of an archive and check it.")
ap.add_argument("src")
ap.add_argument("out")
args = ap.parse_args()
if os.path.exists(args.out):
    sys.exit(f"Το {args.out} υπάρχει ήδη.")

os.umask(0o077)
src = sqlite3.connect(f"file:{args.src}?mode=ro", uri=True)
out = sqlite3.connect(args.out, isolation_level=None)
src.backup(out)


def counts(db):
    have = {r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type = 'table'")}
    return {t: db.execute(f"SELECT count(*) FROM {t}").fetchone()[0] for t in KEPT if t in have}


def links(db):
    return db.execute("SELECT count(*) FROM library_link").fetchone()[0]


before, links_before = counts(src), links(src)
done = migrate.migrate(out)
after = counts(out)
same = links(out) + out.execute("SELECT count(*) FROM media_same").fetchone()[0]
bad = []
for t in KEPT:
    mark = "" if before.get(t) == after.get(t) else "  <-- διαφέρει"
    if t != "address" and mark:
        bad.append(t)
    print(f"{t:22} {before.get(t, '-'):>9} {after.get(t, '-'):>9}{mark}")
print(f"{'library_link + same':22} {links_before:>9} {same:>9}")
if same != links_before:
    bad.append("library_link")
integrity = out.execute("PRAGMA integrity_check").fetchone()[0]
keys = out.execute("PRAGMA foreign_key_check").fetchall()
out.execute("INSERT INTO message_fts (message_fts, rank) VALUES ('integrity-check', 1)")
print(f"integrity: {integrity}, foreign keys: {len(keys)} προβλήματα, fts: ok")
for k, v in done.items():
    print(f"{k}: {v}")
if bad or integrity != "ok" or keys:
    sys.exit(f"ΑΠΟΤΥΧΙΑ: {bad} {keys[:5]}")
out.execute("VACUUM")                   # the rebuilt tables leave their old pages free
print(f"έτοιμο: {args.out} (σχήμα v{out.execute('PRAGMA user_version').fetchone()[0]}, "
      f"{os.path.getsize(args.out) / 1e6:.0f} MB)")
