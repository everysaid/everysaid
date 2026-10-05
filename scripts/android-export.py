#!/usr/bin/env python3
"""Export the Android call log, SMS, MMS and blocked numbers over adb (read only) into SQLite.

    uv run python scripts/android-export.py [-s SERIAL]

The phone is the only one adb sees, or the one with that serial (`adb devices`). Everything goes to
`<export>/<device>/` (config `[android] export`, default `<data>/android`), the device named by its
maker and model and the end of its serial, e.g. `acme-phone1-1a2b`: `android.db`, the raw query
outputs and `mms-parts/`. Each content provider becomes a table with exactly the provider's columns, all as text
(NULL stays NULL). The raw `content query` output is kept next to it, compressed.
Tables already in the database are left alone, so a later run only adds what is missing.
MMS addresses (one query per message) go to `mms_addr`; the binary MMS parts (pictures,
audio, ...) are saved under `mms-parts/<part _id>`, except those the archive has already taken
(its record stays after the file has gone to the photo library or been removed).
"""
import argparse
import gzip
import os
import re
import sqlite3
import sys

import common
from common import config

ap = argparse.ArgumentParser(description="Export an Android phone's calls, SMS, MMS and blocked numbers over adb.")
ap.add_argument("-s", "--serial", help="the phone's serial (adb devices), when adb sees more than one")
args = ap.parse_args()
os.umask(0o077)   # everything written here is private: folders 700, files 600
ADB = ["adb"] + (["-s", args.serial] if args.serial else [])


def adb(*cmd):
    r = common.run([*ADB, *cmd], capture_output=True)
    if r.returncode != 0:
        sys.exit(f"adb {' '.join(cmd[:3])}: {r.stderr.decode(errors='replace').strip()}")
    return r.stdout


def device():
    """A folder name for the phone: maker, model and the last 4 characters of its serial."""
    if not args.serial:
        listed = [l.split()[0] for l in adb("devices").decode().splitlines()[1:] if l.strip().endswith("device")]
        if len(listed) != 1:
            sys.exit(f"adb βλέπει {len(listed)} συσκευές: διάλεξε μία με -s ({', '.join(listed) or 'καμία'}).")
    props = [adb("shell", "getprop", p).decode().strip() for p in
             ("ro.product.manufacturer", "ro.product.model", "ro.serialno")]
    name = "-".join(filter(None, [props[0], props[1], props[2][-4:]])).lower()
    return re.sub(r"[^a-z0-9.-]+", "-", name).strip("-") or "android"


OUT = os.path.join(config.ANDROID_EXPORT, device())
DB = os.path.join(OUT, "android.db")
ARCHIVE_DB = os.path.join(config.DATA, "archive.db")

PROVIDERS = {
    "calls": "content://call_log/calls",
    "sms": "content://sms",
    "mms": "content://mms",
    "mms_part": "content://mms/part",
    "blocked": "content://com.android.blockednumber/blocked",
}


def query(uri, projection=None):
    cmd = ["exec-out", "content", "query", "--uri", uri]
    if projection:
        cmd += ["--projection", ":".join(projection)]
    return adb(*cmd).decode("utf-8")


def columns(uri):
    """Column names, read from the first row of an unrestricted query."""
    first = query(uri).split("\nRow: 1 ", 1)[0]
    names = re.findall(r"(?:^Row: 0 |, )([A-Za-z0-9_]+)=", first)
    if not names or len(names) != len(set(names)):
        sys.exit(f"Δεν βρέθηκαν σωστά οι στήλες του {uri}")
    return names


def parse(text, cols):
    """Split rows on 'Row: N ' with N counted up, and values on ', <next column>='.

    Both markers are exact, so commas and newlines inside values (message bodies) are kept.
    """
    rows = []
    pos = 0
    n = 0
    while True:
        head = f"Row: {n} "
        if not text.startswith(head, pos):
            break
        nxt = text.find(f"\nRow: {n + 1} ", pos)
        block = text[pos + len(head):nxt if nxt >= 0 else len(text)].rstrip("\n")
        values = []
        p = 0
        for i, col in enumerate(cols):
            if not block.startswith(col + "=", p):
                sys.exit(f"Γραμμή {n}: περίμενα τη στήλη {col}")
            p += len(col) + 1
            if i + 1 < len(cols):
                end = block.find(f", {cols[i + 1]}=", p)
                if end < 0:
                    sys.exit(f"Γραμμή {n}: δεν βρέθηκε η στήλη {cols[i + 1]}")
            else:
                end = len(block)
            v = block[p:end]
            values.append(None if v == "NULL" else v)
            p = end + 2
        rows.append(values)
        if nxt < 0:
            break
        pos = nxt + 1
        n += 1
    return rows


def count(uri):
    return query(uri, ["_id"]).count("Row: ")


def exists(db, table):
    return db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (table,)).fetchone()


def save(db, table, cols, rows):
    db.execute(f"CREATE TABLE {table} ({', '.join(f'"{c}" TEXT' for c in cols)})")
    db.executemany(f"INSERT INTO {table} VALUES ({', '.join('?' * len(cols))})", rows)
    db.commit()
    print(f"OK {table}: {len(rows)} γραμμές, {len(cols)} στήλες")


os.makedirs(OUT, exist_ok=True)
db = sqlite3.connect(DB)
for table, uri in PROVIDERS.items():
    if exists(db, table):
        print(f"-- {table}: υπάρχει ήδη")
        continue
    cols = columns(uri)
    text = query(uri, cols)
    with gzip.open(os.path.join(OUT, f"{table}.txt.gz"), "wt", encoding="utf-8") as f:
        f.write(text)
    rows = parse(text, cols)
    expected = count(uri)
    ids = {r[cols.index("_id")] for r in rows}
    if len(rows) != expected or len(ids) != len(rows):
        sys.exit(f"{table}: {len(rows)} γραμμές, αναμένονταν {expected} ({len(ids)} μοναδικά _id)")
    save(db, table, cols, rows)

if not exists(db, "mms_addr"):
    cols, rows = None, []
    for (mid,) in db.execute("SELECT _id FROM mms").fetchall():
        uri = f"content://mms/{mid}/addr"
        text = query(uri)
        if not text.startswith("Row: 0 "):
            continue
        if cols is None:
            cols = columns(uri)
        rows += parse(query(uri, cols), cols)
    save(db, "mms_addr", cols, rows)

parts = os.path.join(OUT, "mms-parts")
os.makedirs(parts, exist_ok=True)
# parts the archive has already taken (`attachment.source_path`, kept after the file went to the photo
# library or was removed) are not fetched again
taken = set()
if os.path.exists(ARCHIVE_DB):
    taken = {p for (p,) in config.read_only(ARCHIVE_DB).execute(
        "SELECT a.source_path FROM attachment a JOIN source s ON s.id = a.source_id WHERE s.name = ?",
        (f"{os.path.basename(OUT)}/mms",))}
saved = 0
for pid, ct in db.execute("SELECT _id, ct FROM mms_part WHERE _data IS NOT NULL").fetchall():
    path = os.path.join(parts, pid)
    if os.path.exists(path) or f"mms-parts/{pid}" in taken:
        continue
    data = adb("exec-out", "content", "read", "--uri", f"content://mms/part/{pid}")
    with open(path + ".part", "wb") as f:
        f.write(data)
    os.replace(path + ".part", path)
    saved += 1
print(f"OK mms-parts: {saved} νέα αρχεία")
db.close()
print("Έτοιμο:", DB)
