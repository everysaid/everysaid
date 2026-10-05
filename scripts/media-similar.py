#!/usr/bin/env python3
"""Group the kept pictures that look alike (same shot taken twice, the same picture resent smaller).

    uv run --extra ml --extra torch python scripts/media-similar.py [--min 0.95]   (--extra rocm on AMD)

The pictures are those the review page keeps (`vlm-review.py --kept`). Each (videos: a frame one
second in) gets an image embedding from google/siglip2-base-patch16-224, as in media-tags.py; two
pictures belong together when their cosine similarity is MIN or more, and groups are joined
through shared members. Same bytes always group. Groups go to `<cache>/similar.tsv`
(group, path), largest first, for `vlm-review.py --similar`, and their embeddings to `similar.npz`,
for `media-triage.py` to point each removed look-alike to the closest one kept. Nothing leaves the
machine.
"""
import argparse
import hashlib
import importlib.util
import os
import sys

import numpy as np
import torch
from transformers import AutoModel, AutoProcessor

import common
from common import config

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(config.CACHE, "similar.tsv")
VECTORS = os.path.join(config.CACHE, "similar.npz")
MODEL = "google/siglip2-base-patch16-224"


def main():
    ap = argparse.ArgumentParser(description="Group the kept pictures that look alike.")
    ap.add_argument("--min", type=float, default=0.95, help="cosine similarity for 'alike'")
    args = ap.parse_args()
    os.umask(0o077)
    os.makedirs(config.CACHE, exist_ok=True)
    spec = importlib.util.spec_from_file_location("vlm_review", os.path.join(HERE, "vlm-review.py"))
    review = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(review)
    paths = [x[0] for x in review.kept()[1]]

    device = common.device()
    model = AutoModel.from_pretrained(MODEL).to(device).eval()
    proc = AutoProcessor.from_pretrained(MODEL)
    ok, vectors, digest = [], [], {}
    for p in paths:
        im = common.picture(p, 640)
        if im is None:
            print("δεν διαβάζεται:", p, file=sys.stderr)
            continue
        with torch.no_grad():
            e = common.features(model.get_image_features(**proc(images=[im], return_tensors="pt").to(device)))
        vectors.append(torch.nn.functional.normalize(e, dim=-1)[0].cpu().numpy())
        with open(p, "rb") as f:
            digest[len(ok)] = hashlib.sha256(f.read()).hexdigest()
        ok.append(p)
    sim = np.array(vectors) @ np.array(vectors).T

    parent = list(range(len(ok)))
    def root(i):
        while parent[i] != i:
            parent[i] = parent[parent[i]]
            i = parent[i]
        return i
    for i in range(len(ok)):
        for j in range(i + 1, len(ok)):
            if sim[i, j] >= args.min or digest[i] == digest[j]:
                parent[root(i)] = root(j)
    groups = {}
    for i in range(len(ok)):
        groups.setdefault(root(i), []).append(ok[i])
    groups = sorted((g for g in groups.values() if len(g) > 1), key=len, reverse=True)
    with open(OUT + ".part", "w", encoding="utf-8") as f:
        for n, g in enumerate(groups, 1):
            f.writelines(f"{n}\t{p}\n" for p in sorted(g))
    os.replace(OUT + ".part", OUT)
    grouped = [i for i, p in enumerate(ok) if any(p in g for g in groups)]
    np.savez(VECTORS, paths=np.array([ok[i] for i in grouped]), vectors=np.array([vectors[i] for i in grouped]))
    print(f"{len(ok)} εικόνες · {len(groups)} ομάδες με {sum(map(len, groups))} εικόνες · {OUT}")


if __name__ == "__main__":
    main()
