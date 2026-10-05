#!/usr/bin/env python3
"""Upload the kept chat pictures and videos to immich, each with its date written in it.

    uv run --extra media python scripts/immich-upload.py                 plan only: writes the plan, uploads nothing
    uv run --extra media python scripts/immich-upload.py --upload [--limit N]
    uv run python scripts/immich-upload.py --save-key | --move-to-keyring

The files are those kept on the review pages (`vlm-review.py --kept`, video notes included), less
those marked "only immich's" on `immich-review.py` (`decision` approved 0: immich has it already).
A file whose date the owner left unknown ("άγνωστη", `date_from` unknown), or has not chosen yet,
stops the plan: they are listed, and nothing is uploaded until each has a date (or is left out).
Each one is copied to STAGE and the copy gets, with exiftool:

- its date, unless it has its own capture date (EXIF is authoritative, left as it is): the one the
  owner chose (`review.db` `date_from`: an immich picture of the same occasion, the message, or one
  set by hand); video notes and the rest take the date of the message that carried them. Written in
  the configured time zone with its offset (pictures: DateTimeOriginal, CreateDate, OffsetTime*; videos: the
  QuickTime dates in UTC and Keys:CreationDate with the offset);
- camera make MAKE (config `[immich] make`) and the service (WhatsApp, Viber) as the model, so immich can filter on
  them; a file with a camera of its own keeps it. No description: immich's own pictures have none
  (the conversation and how the date was found stay in the archive and review.db);
- the archive's sha256 of the original file as XMP dc:identifier, so the link survives any later
  change of metadata, even a new upload.

The copy is uploaded (`POST /assets`, permission asset.upload), read back (`GET /assets/{id}`,
asset.read) to check the date written (a different one stops the run, the asset
recorded nowhere: its id is printed to look at), under the name "<service> <date> <time><ext>", and recorded: in the archive's `library_link` (sha256 -> asset id,
method "upload"). The archive's own file and the
sources are not changed here; removing them is `media-prune.py`'s job, when the owner says so.
Re-running skips what `library_link` already has in immich. The API key (secret `immich-key`) is read from the
system's keyring, else from its file in the config folder (600), and never printed.
"""
import argparse
import getpass
import importlib.util
import os
import shutil
import sqlite3
import sys
import time
import uuid
from datetime import datetime, timezone

import common
from common import config

SECRET = "immich-key"
ARCHIVE_DB = os.path.join(config.DATA, "archive.db")
REVIEW_DB = os.path.join(config.DATA, "review.db")
MATCH = os.path.join(config.CACHE, "match.db")
INDEX = os.path.join(config.CACHE, "immich.db")
STAGE = os.path.join(config.CACHE, "upload")
PLAN = os.path.join(config.DATA, "upload-plan.tsv")
TZ = config.TIMEZONE
VIDEO = common.VIDEO
MAKE = config.IMMICH_MAKE


def review_module():
    spec = importlib.util.spec_from_file_location("vlm_review", os.path.join(os.path.dirname(__file__), "vlm-review.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def ro(path):
    return sqlite3.connect(f"file:{path}?mode=ro", uri=True)


def to_ms(text):
    return int(datetime.fromisoformat(text.replace(" ", "T").replace("+00", "+00:00")).timestamp() * 1000)


def plan():
    """[{path, sha256, sha1, ext, when (ms, or None: own capture date), how, service, name}]"""
    for path, maker in ((MATCH, "immich-match.py"), (INDEX, "immich-index.py")):
        if not os.path.exists(path):
            sys.exit(f"Λείπει το {path}: τρέξε πρώτα το {maker}.")
    review = review_module()
    paths = sorted(x[0] for x in review.kept()[1])
    notes = {p for (p,) in sqlite3.connect(review.VLM).execute("SELECT path FROM filtered WHERE reason = 'video note'")}
    match = {p: (sha1, taken, message) for p, sha1, taken, message in
             ro(MATCH).execute("SELECT path, sha1, taken, message FROM match")}
    date_from = {s: (src, asset, ms) for s, src, asset, ms in
                 ro(REVIEW_DB).execute("SELECT sha1, source, immich_asset, ms FROM date_from")}
    only_immich = {s for (s,) in ro(REVIEW_DB).execute("SELECT sha1 FROM decision WHERE approved = 0")}
    asset_date = dict(ro(INDEX).execute("SELECT id, taken FROM asset"))
    archive = ro(ARCHIVE_DB)
    out, undated, left = [], [], 0
    for p in paths:
        sha256 = os.path.splitext(os.path.basename(p))[0]
        service = archive.execute("SELECT s.name FROM attachment a JOIN message m ON m.id = a.message_id "
                                  "JOIN service s ON s.id = m.service_id WHERE a.sha256 = ? "
                                  "ORDER BY m.ts LIMIT 1", (sha256,)).fetchone()
        service = {"whatsapp": "WhatsApp", "viber": "Viber"}.get(service[0], service[0]) if service else "?"
        sha1, taken, message = match[p]
        if sha1 in only_immich:         # the owner: immich has it already
            left += 1
            continue
        if taken:
            when, how, name_ms = None, "EXIF", taken
        elif p in notes:
            when, how, name_ms = message, "note", message
        elif sha1 not in date_from or date_from[sha1][0] == "unknown" or (date_from[sha1][0] == "message" and not message):
            undated.append((p, date_from.get(sha1, ("δεν διαλέχτηκε",))[0]))
            continue
        else:
            src, asset, ms = date_from[sha1]
            when = to_ms(asset_date[asset]) if src == "asset" else ms if src == "set" else message
            how, name_ms = src, when
        if not taken and not when:
            undated.append((p, how))
            continue
        ext = os.path.splitext(p)[1].lower()
        name = f"{service} {datetime.fromtimestamp(name_ms / 1000, TZ):%Y-%m-%d %H.%M.%S}{ext}"
        out.append({"path": p, "sha256": sha256, "sha1": sha1, "ext": ext, "when": when, "how": how,
                    "service": service, "name": name})
    if left:
        print(f"μόνο του immich (δεν ανεβαίνουν): {left}")
    if undated:
        for p, why in undated:
            print(f"  χωρίς ημερομηνία ({why}): {p}", file=sys.stderr)
        sys.exit(f"{len(undated)} αρχεία χωρίς ημερομηνία: διάλεξε μία (immich-review.py --origin kept) "
                 "ή άφησέ τα έξω· δεν ανέβηκε τίποτα.")
    return out


def local(ms):
    t = datetime.fromtimestamp(ms / 1000, TZ)
    off = t.strftime("%z")
    return t.strftime("%Y:%m:%d %H:%M:%S"), f"{off[:3]}:{off[3:]}"


def tag(item, dest):
    """Write the date, camera and identifier into the staged copy."""
    args = ["exiftool", "-q", "-overwrite_original", "-P", f"-XMP-dc:Identifier={item['sha256']}"]
    has_make = common.run(["exiftool", "-s3", "-Make", dest], capture_output=True, text=True).stdout.strip()
    if not has_make:
        args += [f"-Make={MAKE}", f"-Model={item['service']}"]
        if item["ext"] in VIDEO:
            args += [f"-Keys:Make={MAKE}", f"-Keys:Model={item['service']}"]
    if item["when"]:
        stamp, off = local(item["when"])
        if item["ext"] in VIDEO:
            utc = datetime.fromtimestamp(item["when"] / 1000, timezone.utc).strftime("%Y:%m:%d %H:%M:%S")
            args += [f"-QuickTime:CreateDate={utc}", f"-QuickTime:ModifyDate={utc}",
                     f"-QuickTime:TrackCreateDate={utc}", f"-QuickTime:MediaCreateDate={utc}",
                     f"-Keys:CreationDate={stamp}{off}"]
        else:
            args += [f"-DateTimeOriginal={stamp}", f"-CreateDate={stamp}", f"-OffsetTimeOriginal={off}",
                     f"-OffsetTimeDigitized={off}", f"-OffsetTime={off}"]
    r = common.run(args + [dest], capture_output=True, text=True)
    if r.returncode != 0:
        raise RuntimeError(f"exiftool: {r.stderr.strip()}")


def upload(item, dest):
    when = item["when"] or int(os.path.getmtime(item["path"]) * 1000)
    iso = datetime.fromtimestamp(when / 1000, timezone.utc).isoformat()
    boundary = uuid.uuid4().hex
    parts = []
    for name, value in (("fileCreatedAt", iso), ("fileModifiedAt", iso), ("filename", item["name"])):
        parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
    with open(dest, "rb") as f:
        data = f.read()
    parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="assetData"; filename="{item["name"]}"\r\n'
                 f'Content-Type: application/octet-stream\r\n\r\n'.encode() + data + b"\r\n")
    parts.append(f"--{boundary}--\r\n".encode())
    return common.immich("POST", "/assets", b"".join(parts),
                         headers={"Content-Type": f"multipart/form-data; boundary={boundary}"})


def main():
    ap = argparse.ArgumentParser(description="Upload the kept chat media to immich, dated.")
    ap.add_argument("--upload", action="store_true", help="really upload (otherwise only the plan)")
    ap.add_argument("--limit", type=int, help="at most this many files (for a first test)")
    ap.add_argument("--only", nargs="+", metavar="PATH", help="just these files")
    ap.add_argument("--save-key", action="store_true", help="ask for the API key and store it in the keyring")
    ap.add_argument("--move-to-keyring", action="store_true",
                    help=f"move the key from {config.secret_file(SECRET)} into the keyring")
    args = ap.parse_args()
    os.umask(0o077)
    if args.move_to_keyring:
        return config.move_to_keyring(SECRET)
    if args.save_key:
        if key := getpass.getpass("Κλειδί API του immich: ").strip():
            print(f"Αποθηκεύτηκε στο {config.save_secret(SECRET, key)}.")
        return

    items = plan()
    if args.only:
        items = [x for x in items if x["path"] in set(args.only)]
    with open(PLAN if not args.only else os.devnull, "w") as f:     # the full plan only
        for x in items:
            stamp = "EXIF" if not x["when"] else " ".join(local(x["when"]))
            f.write(f"{x['path']}\t{x['how']}\t{stamp}\t{x['service']}\t{x['name']}\n")
    from collections import Counter
    print(f"σχέδιο: {len(items)} αρχεία, {PLAN}")
    for (how, service), n in sorted(Counter((x["how"], x["service"]) for x in items).items()):
        print(f"  {n:5}  {service:9} {how}")
    if not args.upload:
        return

    archive = sqlite3.connect(ARCHIVE_DB)
    done = {sha for (sha,) in archive.execute("SELECT sha256 FROM library_link WHERE library = 'immich'")}
    todo = [x for x in items if x["sha256"] not in done][:args.limit]
    os.makedirs(STAGE, exist_ok=True)
    for n, item in enumerate(todo, 1):
        dest = os.path.join(STAGE, os.path.basename(item["path"]))
        shutil.copyfile(item["path"], dest)
        tag(item, dest)
        res = upload(item, dest)
        info = common.immich("GET", f"/assets/{res['id']}")
        got = (info.get("exifInfo") or {}).get("dateTimeOriginal") or info.get("fileCreatedAt")
        # the date written here must be the one immich took (a file's own EXIF date is left to it)
        if item["when"] and (not got or abs(to_ms(got.replace("Z", "+00:00")) - item["when"]) > 1000):
            sys.exit(f"Το immich έδωσε άλλη ημερομηνία ({got}) από αυτή που γράφτηκε "
                     f"({datetime.fromtimestamp(item['when'] / 1000, TZ).isoformat()}) στο {item['path']}: "
                     f"asset {res['id']}, δεν καταγράφηκε· σταμάτησε εδώ.")
        archive.execute("INSERT OR REPLACE INTO library_link VALUES (?, 'immich', ?, 'upload', NULL, ?)",
                        (item["sha256"], res["id"], int(time.time())))
        archive.commit()
        os.remove(dest)
        print(f"{n}/{len(todo)} {res['status']:9} {got}  {os.path.basename(item['path'])}")


if __name__ == "__main__":
    main()
