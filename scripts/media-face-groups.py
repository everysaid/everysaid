#!/usr/bin/env python3
"""Group the faces in a set of pictures by person, for someone immich does not know by name.

    uv run --extra ml python scripts/media-face-groups.py --paths FILE [--min 0.5]

media-faces.py recognises people by the faces immich has named; someone who is not named there
shows up only as "others". Here every face (detection score 0.7 or more,
buffalo_l as in media-faces.py) in the pictures listed in FILE is compared with every other: two
faces whose cosine similarity is MIN or more are the same person, and people are the groups joined
that way (immich's own clustering works on the same embeddings). The owner looks at the largest
groups and says which one is whom; `--name GROUP=LABEL` then adds LABEL to those pictures in
`faces.db` (`face.who`), as media-faces.py would have.

Writes `<cache>/face-groups/`: `faces.npz` (embeddings, path, box), and `groups.html`, a
local page with a few faces of each of the largest groups (cropped, inline). Nothing leaves the
machine.
"""
import argparse
import base64
import importlib.util
import io
import os
import sqlite3
import sys

import numpy as np
from PIL import Image

from common import config

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(config.CACHE, "face-groups")
FACES = os.path.join(config.CACHE, "faces.db")
SHOW, PER = 12, 16       # groups shown, faces per group


def groups(e, min_sim):
    sim = e @ e.T
    parent = list(range(len(e)))
    def root(i):
        while parent[i] != i:
            parent[i] = parent[parent[i]]
            i = parent[i]
        return i
    for i, j in zip(*np.where(np.triu(sim >= min_sim, 1))):
        parent[root(i)] = root(j)
    out = {}
    for i in range(len(e)):
        out.setdefault(root(i), []).append(i)
    return sorted(out.values(), key=len, reverse=True)


def main():
    ap = argparse.ArgumentParser(description="Group faces by person.")
    ap.add_argument("--paths", required=True)
    ap.add_argument("--min", type=float, default=0.5)
    ap.add_argument("--name", action="append", default=[], help="GROUP=LABEL, after looking at groups.html")
    args = ap.parse_args()
    os.umask(0o077)
    os.makedirs(OUT, exist_ok=True)
    store = os.path.join(OUT, "faces.npz")

    if args.name:                       # second step: label the pictures of the chosen groups
        d = np.load(store)
        gs = groups(d["emb"], args.min)
        db = sqlite3.connect(FACES)
        for pair in args.name:
            g, label = pair.split("=", 1)
            paths = sorted({str(d["path"][i]) for i in gs[int(g) - 1]})
            for p in paths:
                row = db.execute("SELECT who, others FROM face WHERE path = ?", (p,)).fetchone()
                who = set(filter(None, (row[0] or "").split(","))) if row else set()
                if label not in who:
                    db.execute("UPDATE face SET who = ?, others = max(others - 1, 0) WHERE path = ?",
                               (",".join(sorted(who | {label})), p))
            print(f"ομάδα {g} -> {label}: {len(paths)} εικόνες")
        db.commit()
        return

    spec = importlib.util.spec_from_file_location("media_faces", os.path.join(HERE, "media-faces.py"))
    mf = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mf)
    from insightface.app import FaceAnalysis
    app = FaceAnalysis(name="buffalo_l", allowed_modules=["detection", "recognition"], providers=["CPUExecutionProvider"])
    app.prepare(ctx_id=-1, det_thresh=mf.MIN_SCORE, det_size=(640, 640))
    paths = [l.strip() for l in open(args.paths, encoding="utf-8") if l.strip() and os.path.exists(l.strip())]
    emb, where, crops = [], [], []
    for k, p in enumerate(paths, 1):
        img = mf.picture(p)
        if img is None:
            continue
        for f in app.get(img):
            x0, y0, x1, y1 = [int(v) for v in f.bbox]
            pad = int(0.25 * max(x1 - x0, y1 - y0))
            crop = Image.fromarray(img[max(0, y0 - pad):y1 + pad, max(0, x0 - pad):x1 + pad, ::-1]).convert("RGB")
            crop.thumbnail((112, 112))
            buf = io.BytesIO()
            crop.save(buf, "JPEG", quality=80)
            emb.append(f.normed_embedding)
            where.append(p)
            crops.append(base64.b64encode(buf.getvalue()).decode())
        if k % 50 == 0:
            print(f"  {k}/{len(paths)}", file=sys.stderr)
    emb = np.array(emb)
    np.savez(store, emb=emb, path=np.array(where))
    gs = groups(emb, args.min)
    html = ['<!doctype html><meta charset="utf-8"><title>Everysaid · Ομάδες προσώπων</title>'
            '<body style="background:#1e1e1e;color:#ddd;font:14px system-ui">']
    for n, g in enumerate(gs[:SHOW], 1):
        pics = len({where[i] for i in g})
        html.append(f'<h3>ομάδα {n}: {len(g)} πρόσωπα σε {pics} εικόνες</h3><div>')
        html += [f'<img src="data:image/jpeg;base64,{crops[i]}" style="margin:2px;height:112px">' for i in g[:PER]]
        html.append('</div>')
    with open(os.path.join(OUT, "groups.html"), "w", encoding="utf-8") as f:
        f.write("\n".join(html))
    print(f"{len(emb)} πρόσωπα σε {len(set(where))} εικόνες · ομάδες με 3+: {sum(len(g) >= 3 for g in gs)} · "
          f"μεγαλύτερες: {[len(g) for g in gs[:SHOW]]}")
    print(os.path.join(OUT, "groups.html"))


if __name__ == "__main__":
    main()
