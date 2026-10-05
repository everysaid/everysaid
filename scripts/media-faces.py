#!/usr/bin/env python3
"""Find who is in the chat pictures, with the faces immich already knows by name.

    uv run --extra ml python scripts/media-faces.py PERSON=LABEL... [--paths FILE] [--dump]

Each PERSON=LABEL pair names a person of the immich library (by name, or by id where the name
repeats) and the label to give them here, e.g. `Name=Name 0a1b2c3d-...=εγώ`. Their faces come
from immich, read only. Through the API (default): the person (`GET /people`, permission
person.read), up to SAMPLE of their pictures (`POST /search/metadata`, asset.read), where immich saw
them in each (`GET /faces`, face.read) and the picture's preview (`GET /assets/{id}/thumbnail`,
asset.view); the face found here at that place gives the embedding. With --dump: immich's own
embeddings, from its nightly database backup (`asset_face`, `face_search`). immich's face model is
buffalo_l (ArcFace), the same one insightface keeps in ~/.insightface, so the two compare directly.

Candidates are the pictures the review page shows (`--paths`, one path per line), else every
picture and video the archive still has. For each: faces found (detection score of 0.7 or more,
as immich), and for each face the named person it matches: the cosine similarity to at least
MATCHES of that person's faces in immich must be MIN_SIM or more. Results go to
`~/.cache/chronika/faces.db` (`face`: path, faces, the labels found, other faces), and can be
redone: files already done are skipped. Runs on the CPU (onnxruntime's GPU providers are not
assumed).
Nothing leaves the machine.
"""
import argparse
import glob
import gzip
import io
import os
import sqlite3
import sys
import urllib.error

import numpy as np
from PIL import Image

import common
from common import config

IMMICH = config.IMMICH_DATA                  # immich's upload folder (config `[immich] data_folder`)
ARCHIVE_DB = os.path.join(config.DATA, "archive.db")
OUT = os.path.join(config.CACHE, "faces.db")
SAMPLE = 60         # pictures of each person read through the API
MIN_SCORE = 0.7     # immich's minimum detection score
MIN_SIM = 0.5       # cosine similarity for "same person" (immich: max distance 0.5)
MATCHES = 3         # ...to this many of that person's known faces, not one stray


def copy_block(f, header):
    """Rows of the COPY block that starts with `header`, as lists of fields."""
    for line in f:
        if line.startswith(header):
            break
    for line in f:
        if line == "\\.\n":
            return
        yield line.rstrip("\n").split("\t")


def references_dump(names):
    """label -> matrix of the normalised face embeddings immich has for that named person."""
    dump = max(glob.glob(os.path.join(config.require(IMMICH, "immich", "data_folder"), "backups",
                                      "immich-db-backup-*.sql.gz")))
    print("immich:", os.path.basename(dump), file=sys.stderr)
    with gzip.open(dump, "rt", encoding="utf-8") as f:
        faces = {}                                           # face id -> person group
        for r in copy_block(f, "COPY public.asset_face "):
            if r[10] == "\\N":                               # not deleted
                faces[r[8]] = r[1]
        vectors = {}
        for r in copy_block(f, "COPY public.face_search "):
            vectors[r[0]] = r[1]
        groups = {}
        for r in copy_block(f, "COPY public.person "):
            if r[3] in names:
                groups.setdefault(r[3], []).append(r[11])
            if r[11] in names:                               # given by id: names repeat in immich
                groups[r[11]] = [r[11]]
    out = {}
    for name, label in names.items():
        if name not in groups:
            sys.exit(f"στο immich δεν υπάρχει πρόσωπο με όνομα {name}")
        if len(groups[name]) > 1:
            sys.exit(f"στο immich υπάρχουν {len(groups[name])} πρόσωπα με όνομα {name}")
        ids = [fid for fid, g in faces.items() if g == groups[name][0] and fid in vectors]
        m = np.array([np.array(vectors[i].strip("[]").split(","), dtype=np.float32) for i in ids])
        out[label] = m / np.linalg.norm(m, axis=1, keepdims=True)
        print(f"  {name} -> {label}: {len(ids)} πρόσωπα", file=sys.stderr)
    return out


def bgr(im):
    return np.asarray(im.convert("RGB"))[:, :, ::-1].copy()      # insightface wants BGR


def picture(src):
    im = common.picture(src, 1600)
    return bgr(im) if im is not None else None


def people(names):
    """name or id -> immich person id, every person checked to be there once."""
    found, page = [], 1
    while page:
        r = common.immich("GET", f"/people?page={page}&size=1000&withHidden=true")
        found += r["people"]
        page = page + 1 if r.get("hasNextPage") else None
    out = {}
    for name in names:
        ids = [p["id"] for p in found if name in (p["name"], p["id"])]
        if not ids:
            sys.exit(f"στο immich δεν υπάρχει πρόσωπο με όνομα {name}")
        if len(ids) > 1:
            sys.exit(f"στο immich υπάρχουν {len(ids)} πρόσωπα με όνομα {name}")
        out[name] = ids[0]
    return out


def iou(a, b):
    w = max(0, min(a[2], b[2]) - max(a[0], b[0]))
    h = max(0, min(a[3], b[3]) - max(a[1], b[1]))
    inter = w * h
    return inter / ((a[2] - a[0]) * (a[3] - a[1]) + (b[2] - b[0]) * (b[3] - b[1]) - inter or 1)


def references_api(names, app):
    """As references_dump, through the API: the faces found here where immich placed that person."""
    out = {}
    for name, pid in people(names).items():
        hits = common.immich("POST", "/search/metadata", {"personIds": [pid], "type": "IMAGE",
                                                          "size": SAMPLE})["assets"]["items"]
        vectors = []
        for a in hits:
            boxes = [f for f in common.immich("GET", f"/faces?id={a['id']}") if (f.get("person") or {}).get("id") == pid]
            if not boxes:
                continue
            im = Image.open(io.BytesIO(common.immich("GET", f"/assets/{a['id']}/thumbnail?size=preview", raw=True)))
            found = app.get(bgr(im))
            for b in boxes:
                sx, sy = im.width / b["imageWidth"], im.height / b["imageHeight"]
                box = (b["boundingBoxX1"] * sx, b["boundingBoxY1"] * sy, b["boundingBoxX2"] * sx, b["boundingBoxY2"] * sy)
                best = max(found, key=lambda f: iou(f.bbox, box), default=None)
                if best is not None and iou(best.bbox, box) >= 0.5:
                    vectors.append(best.normed_embedding)
        if not vectors:
            sys.exit(f"δεν βρέθηκε κανένα πρόσωπο του {name} στις φωτογραφίες του immich")
        out[names[name]] = np.array(vectors)
        print(f"  {name} -> {names[name]}: {len(vectors)} πρόσωπα", file=sys.stderr)
    return out


def main():
    ap = argparse.ArgumentParser(description="Who is in the chat pictures, by the faces immich knows.")
    ap.add_argument("people", nargs="+", help="NAME_IN_IMMICH=LABEL")
    ap.add_argument("--paths", help="file with the pictures to look at, one per line")
    ap.add_argument("--dump", action="store_true", help="immich's embeddings from its nightly database backup")
    args = ap.parse_args()
    names = dict(p.split("=", 1) for p in args.people)
    os.umask(0o077)
    os.makedirs(config.CACHE, exist_ok=True)

    if args.paths:
        paths = [l.strip() for l in open(args.paths, encoding="utf-8") if l.strip()]
    else:
        db = config.read_only(ARCHIVE_DB)
        paths = [common.media_file(p) for (p,) in db.execute(
            "SELECT path FROM media WHERE mime LIKE 'image/%' OR mime LIKE 'video/%'")]
    paths = [p for p in paths if os.path.exists(p)]
    out = sqlite3.connect(OUT)
    out.execute("""CREATE TABLE IF NOT EXISTS face (path TEXT PRIMARY KEY, faces INTEGER, who TEXT,
                   others INTEGER, best TEXT)""")
    done = {p for (p,) in out.execute("SELECT path FROM face")}
    todo = [p for p in paths if p not in done]
    print(f"{len(paths)} εικόνες, μένουν {len(todo)}", file=sys.stderr)
    if not todo:
        return

    from insightface.app import FaceAnalysis
    app = FaceAnalysis(name="buffalo_l", allowed_modules=["detection", "recognition"],
                       providers=["CPUExecutionProvider"])
    app.prepare(ctx_id=-1, det_thresh=MIN_SCORE, det_size=(640, 640))
    try:
        refs = references_dump(names) if args.dump else references_api(names, app)
    except urllib.error.HTTPError as e:
        sys.exit(f"immich: {e.code} {e.read().decode(errors='replace')[:200]} · χρειάζονται οι άδειες "
                 "person.read, face.read, asset.read, asset.view (ή --dump)")

    for i, path in enumerate(todo, 1):
        img = picture(path)
        faces = app.get(img) if img is not None else []
        who, others, best = set(), 0, {}
        for face in faces:
            e = face.normed_embedding
            hit = None
            for label, m in refs.items():
                sims = m @ e
                best[label] = max(best.get(label, 0), float(sims.max()))
                if (sims >= MIN_SIM).sum() >= MATCHES and (hit is None or sims.max() > hit[1]):
                    hit = (label, float(sims.max()))
            if hit:
                who.add(hit[0])
            else:
                others += 1
        out.execute("INSERT OR REPLACE INTO face VALUES (?, ?, ?, ?, ?)",
                    (path, len(faces), ",".join(sorted(who)), others,
                     ",".join(f"{k}:{v:.2f}" for k, v in sorted(best.items()))))
        if i % 50 == 0:
            out.commit()
            print(f"  {i}/{len(todo)}", file=sys.stderr)
    out.commit()
    rows = out.execute("SELECT who, others > 0, faces > 0, count(*) FROM face GROUP BY 1, 2, 3").fetchall()
    for who, other, any_face, n in rows:
        print(f"{n:6}  {who or '-'}  {'+ άλλοι' if other else ''}  {'' if any_face else '(χωρίς πρόσωπα)'}")


if __name__ == "__main__":
    main()
