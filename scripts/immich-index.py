#!/usr/bin/env python3
"""Build a local index of the immich library (read only).

    uv run python scripts/immich-index.py                 through the immich API
    uv run python scripts/immich-index.py --dump [FILE]   from immich's nightly database backup

Writes `<cache>/immich.db` (mode 600; outside the home snapshots, rebuilt whenever needed): one row
per asset not in the trash, with its type, original file name, SHA-1 checksum, dates, camera, size,
the path of a preview and a CLIP embedding. Nothing in immich is touched.

- Through the API (config `[immich] url`, secret `immich-key`): the assets come from
  `POST /search/metadata` (permission asset.read), their small previews (`thumbnail`, about 250
  pixels) from `GET /assets/{id}/thumbnail` (asset.view) into `<cache>/immich-thumbs/`, only those
  not there yet. immich does not give its embeddings: `immich-match.py` makes them from the
  previews, and they are kept here from one run to the next, as are `immich-dupes.py`'s hashes.
- From the dump (`[immich] data_folder`): the newest `backups/immich-db-backup-*.sql.gz` (or the one
  given), with immich's own previews in its data folder and its embeddings (`smart_search`, model in
  `system-config`, as float32).
"""
import argparse
import array
import base64
import glob
import gzip
import json
import os
import sqlite3
import sys
import urllib.error
from datetime import datetime

import common
from common import config

IMMICH = config.IMMICH_DATA                  # immich's upload folder (config `[immich] data_folder`)
OUT = os.path.join(config.CACHE, "immich.db")
THUMBS = os.path.join(config.CACHE, "immich-thumbs")
WANTED = {"asset", "asset_exif", "asset_file", "smart_search", "system_metadata"}
SCHEMA = """
    CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
    CREATE TABLE asset (
        id TEXT PRIMARY KEY, type TEXT, name TEXT, sha1 TEXT, created TEXT, taken TEXT,
        make TEXT, model TEXT, width INTEGER, height INTEGER, size INTEGER, preview TEXT,
        embedding BLOB, phash INTEGER);
    CREATE INDEX asset_sha1 ON asset (sha1);
"""


def unescape(field):
    if field == "\\N":
        return None
    if "\\" not in field:
        return field
    out, i = [], 0
    while i < len(field):
        c = field[i]
        if c == "\\" and i + 1 < len(field):
            n = field[i + 1]
            out.append({"t": "\t", "n": "\n", "r": "\r", "\\": "\\"}.get(n, n))
            i += 2
        else:
            out.append(c)
            i += 1
    return "".join(out)


def tables(path):
    """table -> (columns, rows) for the COPY blocks of the wanted tables."""
    found = {}
    with gzip.open(path, "rt", encoding="utf-8") as f:
        table = None
        for line in f:
            if table is None:
                if line.startswith("COPY public."):
                    name = line[len("COPY public."):].split(" ", 1)[0].strip('"')
                    if name in WANTED:
                        cols = [c.strip().strip('"') for c in line[line.index("(") + 1:line.index(")")].split(",")]
                        table = name
                        found[name] = (cols, [])
                continue
            if line == "\\.\n":
                table = None
                continue
            found[table][1].append([unescape(x) for x in line.rstrip("\n").split("\t")])
    return found


def new_index():
    """A fresh index beside the old one (OUT.part), put in place by done()."""
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    if os.path.exists(OUT + ".part"):
        os.remove(OUT + ".part")
    db = sqlite3.connect(OUT + ".part")
    db.executescript(SCHEMA)
    return db


def done(db, n, note):
    db.commit()
    db.close()
    os.replace(OUT + ".part", OUT)
    print(f"{n} assets{note} -> {OUT}", file=sys.stderr)


def from_dump(backup):
    config.require(IMMICH, "immich", "data_folder")
    backup = backup or max(glob.glob(os.path.join(IMMICH, "backups", "immich-db-backup-*.sql.gz")))
    print(f"backup: {os.path.basename(backup)}", file=sys.stderr)
    t = tables(backup)
    rows = {name: [dict(zip(cols, r)) for r in data] for name, (cols, data) in t.items()}

    system = next((json.loads(r["value"]) for r in rows["system_metadata"] if r["key"] == "system-config"), {})
    model = system.get("machineLearning", {}).get("clip", {}).get("modelName", "ViT-B-32__openai")
    exif = {r["assetId"]: r for r in rows["asset_exif"]}
    preview = {r["assetId"]: r["path"] for r in rows["asset_file"] if r["type"] == "preview"}
    embedding = {r["assetId"]: r["embedding"] for r in rows["smart_search"]}

    db = new_index()
    db.executemany("INSERT INTO meta VALUES (?, ?)", [("backup", os.path.basename(backup)), ("clip_model", model)])
    n = 0
    for a in rows["asset"]:
        if a.get("deletedAt"):
            continue
        e = exif.get(a["id"], {})
        vec = embedding.get(a["id"])
        blob = array.array("f", map(float, vec.strip("[]").split(","))).tobytes() if vec else None
        checksum = a["checksum"]
        sha1 = checksum[2:] if checksum and checksum.startswith("\\x") else checksum
        path = preview.get(a["id"])
        if path:
            path = os.path.join(IMMICH, *path.removeprefix(config.IMMICH_PREFIX).split("/"))
        db.execute("INSERT INTO asset VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)", (
            a["id"], a["type"], a["originalFileName"], sha1, a["fileCreatedAt"], e.get("dateTimeOriginal"),
            e.get("make"), e.get("model"), e.get("exifImageWidth"), e.get("exifImageHeight"),
            e.get("fileSizeInByte"), path, blob))
        n += 1
    done(db, n, f" ({sum(1 for a in rows['asset'] if a.get('deletedAt'))} in the trash left out), model {model}")


def pg_time(iso):
    """The API's ISO time as the dump writes it ('2017-07-09 12:40:52.212+00'), for the same readers."""
    if not iso:
        return None
    t = datetime.fromisoformat(iso.replace("Z", "+00:00"))
    stamp = t.strftime("%Y-%m-%d %H:%M:%S") + (f".{t.microsecond // 1000:03d}".rstrip("0").rstrip(".")
                                              if t.microsecond else "")
    off = t.strftime("%z")
    return stamp + (off[:3] if off.endswith("00") else f"{off[:3]}:{off[3:]}")


def assets():
    """Every asset immich shows its owner (all visibilities), not in the trash."""
    seen = set()
    for visibility in ("timeline", "archive", "hidden"):
        page = 1
        while page:
            r = common.immich("POST", "/search/metadata", {"page": page, "size": 1000, "withExif": True,
                                                           "visibility": visibility})["assets"]
            for a in r["items"]:
                if a["id"] not in seen and not a.get("isTrashed"):
                    seen.add(a["id"])
                    yield a
            page = int(r["nextPage"]) if r.get("nextPage") else None


def thumbnail(asset_id):
    """The asset's small preview in THUMBS, fetched if not there yet; None where immich refuses it."""
    path = os.path.join(THUMBS, f"{asset_id}.webp")
    if not os.path.exists(path):
        data = common.immich("GET", f"/assets/{asset_id}/thumbnail?size=thumbnail", raw=True)
        with open(path + ".part", "wb") as f:
            f.write(data)
        os.replace(path + ".part", path)
    return path


def from_api():
    kept = {}                               # what earlier runs made: embeddings, hashes
    if os.path.exists(OUT):
        old = sqlite3.connect(f"file:{OUT}?mode=ro", uri=True)
        cols = {r[1] for r in old.execute("PRAGMA table_info(asset)")}
        if {"embedding", "phash"} <= cols:
            kept = {r[0]: r[1:] for r in old.execute("SELECT id, sha1, embedding, phash FROM asset")}
        old.close()
    os.makedirs(THUMBS, exist_ok=True)
    db = new_index()
    db.executemany("INSERT INTO meta VALUES (?, ?)", [("source", "api"), ("clip_model", "local")])
    n, previews, missing = 0, True, 0
    for a in assets():
        e = a.get("exifInfo") or {}
        sha1 = base64.b64decode(a["checksum"]).hex() if a.get("checksum") else None
        path = None
        if previews:
            try:
                path = thumbnail(a["id"])
            except urllib.error.HTTPError as err:
                if err.code == 404:         # immich has no preview of it (one it could not make)
                    missing += 1
                elif err.code == 403:
                    previews = False
                    print("immich: χωρίς άδεια asset.view, χωρίς προεπισκοπήσεις", file=sys.stderr)
                else:
                    raise
        emb, ph = kept[a["id"]][1:] if a["id"] in kept and kept[a["id"]][0] == sha1 else (None, None)
        db.execute("INSERT INTO asset VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", (
            a["id"], a["type"], a["originalFileName"], sha1, pg_time(a.get("fileCreatedAt")),
            pg_time(e.get("dateTimeOriginal")), e.get("make"), e.get("model"), e.get("exifImageWidth"),
            e.get("exifImageHeight"), e.get("fileSizeInByte"), path, emb, ph))
        n += 1
        if n % 2000 == 0:
            print(f"  {n}", file=sys.stderr)
    if missing:
        print(f"immich: {missing} χωρίς προεπισκόπηση (404)", file=sys.stderr)
    done(db, n, " (API)")


def main():
    ap = argparse.ArgumentParser(description="Index the immich library, read only.")
    ap.add_argument("--dump", nargs="?", const="", metavar="FILE",
                    help="read immich's nightly database backup (the newest, or FILE) instead of the API")
    args = ap.parse_args()
    os.umask(0o077)
    if args.dump is not None:
        from_dump(args.dump or None)
    else:
        from_api()


if __name__ == "__main__":
    main()
