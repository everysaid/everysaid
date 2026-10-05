#!/usr/bin/env python3
"""Set a conversation's media aside: a folder to look at later, and off the review pages.

    uv run python scripts/media-aside.py LABEL CONVERSATION_ID... [--dry-run]

For every file attached to a message of those conversations (archive `conversation.id`), a hard
link (no extra space; a copy where the folder is on another file system; the archive and the sources
are not touched) is made in `<aside>/<LABEL>/<kind>/<date>_<time>_<sha256[:8]><ext>`, the folder being
config `[media] aside` (default `<data>/aside`), the kind photos, videos, audio, documents or other,
and the date that of the earliest message carrying it, in the configured time zone. Each file is
recorded in the `aside` table of `<data>/review.db`
(sha256, label, when), which the review pages read to leave these files out. Files that are no
longer here (already in the photo library, or removed) are only counted. Running it again adds
what is missing.
"""
import argparse
import os
import sqlite3
import sys
import time
from datetime import datetime

import common
from common import config, link_or_copy

ARCHIVE = config.MEDIA_STORE                                      # media/<ab>/<sha256><ext>
ARCHIVE_DB = os.path.join(config.DATA, "archive.db")
ASIDE = config.ASIDE
DECISIONS = os.path.join(config.DATA, "review.db")
TZ = config.TIMEZONE
KINDS = {"image/webp": "other", "application/pdf": "documents"}


def kind(mime):
    mime = mime or ""
    if mime in KINDS:
        return KINDS[mime]
    return {"image": "photos", "video": "videos", "audio": "audio"}.get(mime.split("/")[0], "other")


def main():
    ap = argparse.ArgumentParser(description="Set a conversation's media aside.")
    ap.add_argument("label")
    ap.add_argument("conversations", nargs="+", type=int)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    if any(c in args.label for c in '/\\:') or args.label in ("", ".", ".."):
        sys.exit("μη έγκυρη ετικέτα")
    os.umask(0o077)
    db = config.read_only(ARCHIVE_DB)
    marks = ",".join("?" * len(args.conversations))
    rows = db.execute(
        f"SELECT md.sha256, md.path, md.mime, min(m.ts) FROM attachment a JOIN message m ON m.id = a.message_id "
        f"JOIN media md ON md.sha256 = a.sha256 WHERE m.conversation_id IN ({marks}) GROUP BY md.sha256",
        args.conversations).fetchall()
    os.makedirs(os.path.dirname(DECISIONS), exist_ok=True)
    dec = sqlite3.connect(DECISIONS)
    dec.execute("CREATE TABLE IF NOT EXISTS aside (sha256 TEXT PRIMARY KEY, label TEXT NOT NULL, at INTEGER NOT NULL)")
    made = present = gone = 0
    counts = {}
    for sha, path, mime, ts in rows:
        src = common.media_file(path)
        if not os.path.exists(src):
            gone += 1
            continue
        k = kind(mime)
        when = datetime.fromtimestamp(ts / 1000, TZ).strftime("%Y-%m-%d_%H%M%S")
        dest = os.path.join(ASIDE, args.label, k, f"{when}_{sha[:8]}{os.path.splitext(path)[1]}")
        counts[k] = counts.get(k, 0) + 1
        if os.path.exists(dest):
            present += 1
        else:
            if not args.dry_run:
                os.makedirs(os.path.dirname(dest), exist_ok=True)
                link_or_copy(src, dest)
            made += 1
        if not args.dry_run:
            # `at` is when it was set aside (a newer decision than any before it): kept on a run again
            dec.execute("INSERT INTO aside VALUES (?, ?, ?) ON CONFLICT (sha256) DO UPDATE SET label = excluded.label, "
                        "at = excluded.at WHERE label != excluded.label", (sha, args.label, int(time.time())))
    dec.commit()
    print(f"{args.label}: {len(rows)} αρχεία · {'θα γίνουν' if args.dry_run else 'έγιναν'} {made} "
          f"σύνδεσμοι, υπήρχαν {present}, δεν υπάρχουν πια {gone} · {counts} -> {os.path.join(ASIDE, args.label)}")


if __name__ == "__main__":
    main()
