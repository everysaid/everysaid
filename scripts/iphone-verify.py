#!/usr/bin/env python3
"""Verify an encrypted iPhone backup: decrypt every file and check it against Manifest.db.

    uv run --extra iphone python scripts/iphone-verify.py

The password comes from where iphone-sync.py keeps it (keyring, else file), or is asked for; it is
never printed.
"""
import argparse
import contextlib
import getpass
import io
import os
import sys

from iphone_backup_decrypt import EncryptedBackup
from iphone_backup_decrypt.exceptions import IncorrectPassphraseError
from iphone_backup_decrypt.utils import FilePlist, backup_file_path

from common import config

argparse.ArgumentParser(description="Decrypt every file of the iPhone backup and check it (read only).").parse_args()
BACKUP = config.iphone_backup()

if not os.path.exists(os.path.join(BACKUP, "Manifest.plist")):
    sys.exit("Δεν βρέθηκε ολοκληρωμένο backup (λείπει το Manifest.plist).")


def passwords():
    """The saved password (as iphone-sync.py keeps it) first, then as many tries as needed."""
    if saved := config.secret("backup-password"):
        yield saved
    while True:
        pw = getpass.getpass("Κωδικός backup (κενό για έξοδο): ")
        if not pw:
            sys.exit(1)
        yield pw


for pw in passwords():
    backup = EncryptedBackup(backup_directory=BACKUP, passphrase=pw)
    try:
        backup.test_decryption()
    except IncorrectPassphraseError:
        print("Λάθος κωδικός.")
        continue
    break

with backup.manifest_db_cursor() as cur:
    cur.execute("SELECT fileID, domain, relativePath, file FROM Files WHERE flags = 1")
    rows = cur.fetchall()

PAGE = 4096  # SQLite page size; live databases change by whole pages during a backup

ok = empty = 0
live = []
problems = []
for i, (file_id, domain, path, bplist) in enumerate(rows, 1):
    name = f"{domain}/{path}"
    meta = FilePlist(bplist)
    on_disk = backup_file_path(BACKUP, file_id)
    if not os.path.exists(on_disk):
        problems.append(f"λείπει: {name}")
    elif meta.encryption_key is None:
        empty += 1  # empty files are stored without a key
    else:
        try:
            with contextlib.redirect_stdout(io.StringIO()):  # silence the library's INFO lines
                data = backup._decrypt_inner_file(file_id=file_id, file_bplist=bplist)
            if len(data) == meta.filesize:
                ok += 1
            elif len(data) % PAGE == 0 and meta.filesize % PAGE == 0:
                live.append(name)
            else:
                problems.append(f"μέγεθος {len(data)} αντί {meta.filesize}: {name}")
        except Exception as e:
            problems.append(f"σφάλμα ({type(e).__name__}): {name}")
    if i % 2000 == 0:
        print(f"  {i}/{len(rows)}...", flush=True)

print(f"\nΑρχεία στον κατάλογο: {len(rows)}")
print(f"Αποκρυπτογραφήθηκαν σωστά: {ok}")
print(f"Κενά αρχεία (χωρίς κλειδί): {empty}")
print(f"Βάσεις που άλλαξαν κατά τη λήψη (αποκρυπτογραφήθηκαν κανονικά): {len(live)}")
print(f"Πραγματικά προβλήματα: {len(problems)}")
for p in problems:
    print("  " + p)
sys.exit(1 if problems else 0)
