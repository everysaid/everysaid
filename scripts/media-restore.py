#!/usr/bin/env python3
"""Bring back media removed by media-prune.py, from the encrypted iPhone backup.

    uv run --extra iphone python scripts/media-restore.py SHA256... [--dry-run]

The archive keeps the record of every removed file (`media`, `attachment.source_path`); for those
that came from the iPhone (WhatsApp, Viber), the encrypted backup still holds them as long as the
phone does. Each is decrypted again (the backup is only read), checked to have the recorded
sha256, written to its place in the cache (`<cache>/iphone`, `.part`, then renamed) and hard-linked back into the
archive. Each file brought back is recorded in `restored` in review.db (sha256, when): a decision
to delete it from before then no longer applies (media-triage.py). Files from the Android phone cannot be
brought back this way. The password comes from where
iphone-sync.py keeps it (keyring, else file), or is asked for; it is never printed.
"""
import argparse
import getpass
import hashlib
import os
import sqlite3
import sys
import time

from iphone_backup_decrypt import EncryptedBackup
from iphone_backup_decrypt.exceptions import IncorrectPassphraseError

import common
from common import config

DATA = os.path.join(config.CACHE, "iphone")
ARCHIVE_DB = os.path.join(config.DATA, "archive.db")
REVIEW_DB = os.path.join(config.DATA, "review.db")
# archive source -> (folder in DATA, backup domain, prefix of the path in the backup)
SOURCES = {"iphone/viber": ("viber-media", "AppDomain-com.viber", "Documents/"),
           "iphone/whatsapp": ("whatsapp-media", "AppDomainGroup-group.net.whatsapp.WhatsApp.shared", "Message/Media/")}


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
    return h.hexdigest()


def main():
    ap = argparse.ArgumentParser(description="Bring back removed media from the iPhone backup.")
    ap.add_argument("sha256", nargs="+")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    os.umask(0o077)
    db = sqlite3.connect(f"file:{ARCHIVE_DB}?mode=ro", uri=True)
    todo = []
    for sha in args.sha256:
        row = db.execute("SELECT path FROM media WHERE sha256 = ?", (sha,)).fetchone()
        if not row:
            print("άγνωστο στο archive:", sha); continue
        target = common.media_file(row[0])
        if os.path.exists(target):
            print("υπάρχει ήδη:", target); continue
        src = db.execute("SELECT s.name, a.source_path FROM attachment a JOIN source s ON s.id = a.source_id "
                         "WHERE a.sha256 = ? AND s.name IN (?, ?) LIMIT 1", (sha, *SOURCES)).fetchone()
        if not src:
            print("δεν ήρθε από το iPhone, δεν επανέρχεται έτσι:", sha); continue
        todo.append((sha, target, *src))
    print(f"{len(todo)} για επαναφορά")
    if args.dry_run or not todo:
        return

    pw = config.secret("backup-password") or getpass.getpass("Κωδικός backup: ")
    backup = EncryptedBackup(backup_directory=config.iphone_backup(), passphrase=pw)
    try:
        backup.test_decryption()
    except IncorrectPassphraseError:
        sys.exit("Λάθος κωδικός.")

    review = sqlite3.connect(REVIEW_DB)
    review.execute("CREATE TABLE IF NOT EXISTS restored (sha256 TEXT NOT NULL, at INTEGER NOT NULL)")
    done = 0
    for sha, target, source, rel in todo:
        folder, domain, prefix = SOURCES[source]
        dest = os.path.join(DATA, folder, *rel.split("/"))
        if not os.path.exists(dest):
            os.makedirs(os.path.dirname(dest), mode=0o700, exist_ok=True)
            try:
                backup.extract_file(relative_path=prefix + rel, domain_like=domain, output_filename=dest + ".part")
            except Exception as e:
                print(f"δεν βρέθηκε στο backup ({type(e).__name__}): {rel}"); continue
            if sha256(dest + ".part") != sha:
                os.remove(dest + ".part")
                print("άλλο περιεχόμενο στο backup:", rel); continue
            os.replace(dest + ".part", dest)
        elif sha256(dest) != sha:
            print("άλλο αρχείο στη θέση του:", dest); continue
        os.makedirs(os.path.dirname(target), exist_ok=True)
        common.link_or_copy(dest, target)
        review.execute("INSERT INTO restored VALUES (?, ?)", (sha, int(time.time())))
        review.commit()
        done += 1
    print(f"επανήλθαν: {done} από {len(todo)}")


if __name__ == "__main__":
    main()
