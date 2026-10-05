#!/usr/bin/env python3
"""List what the iPhone backup holds, by folder, without extracting anything.

    uv run --extra iphone python scripts/iphone-ls.py DOMAIN_LIKE [PATH_LIKE] [--depth N]

DOMAIN_LIKE and PATH_LIKE are SQL LIKE patterns ("%viber%", "%Attachments%"). Prints file count and
total size per domain and leading path components. The password comes from where iphone-sync.py
keeps it (keyring, else file), or is asked for; it is never printed.
"""
import argparse
import getpass
import plistlib
import sys
from collections import defaultdict

from iphone_backup_decrypt import EncryptedBackup
from iphone_backup_decrypt.exceptions import IncorrectPassphraseError

from common import config

ap = argparse.ArgumentParser(description="List the iPhone backup's files by folder.")
ap.add_argument("domain", help="SQL LIKE pattern for the domain")
ap.add_argument("path", nargs="?", default="%", help="SQL LIKE pattern for the relative path")
ap.add_argument("--depth", type=int, default=3, help="path components to group by (default 3)")
args = ap.parse_args()
BACKUP = config.iphone_backup()

pw = config.secret("backup-password") or getpass.getpass("Κωδικός backup: ")

backup = EncryptedBackup(backup_directory=BACKUP, passphrase=pw)
try:
    backup.test_decryption()
except IncorrectPassphraseError:
    sys.exit("Λάθος κωδικός.")

groups = defaultdict(lambda: [0, 0])
with backup.manifest_db_cursor() as cur:
    cur.execute("SELECT domain, relativePath, file FROM Files WHERE flags = 1 AND domain LIKE ? AND relativePath LIKE ?",
                (args.domain, args.path))
    for domain, path, blob in cur:
        size = plistlib.loads(blob)["$objects"][1].get("Size", 0)
        key = (domain, "/".join(path.split("/")[:args.depth]))
        groups[key][0] += 1
        groups[key][1] += size

for (domain, path), (n, size) in sorted(groups.items(), key=lambda kv: -kv[1][1]):
    print(f"{size / 1e6:10.1f} MB {n:7d}  {domain}  {path}")
print(f"{sum(s for _, s in groups.values()) / 1e6:10.1f} MB {sum(n for n, _ in groups.values()):7d}  σύνολο")
