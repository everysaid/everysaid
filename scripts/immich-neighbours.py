#!/usr/bin/env python3
"""The few immich pictures most like each kept chat picture, to date the ones that lost their date.

    uv run --extra ml --extra torch python scripts/immich-neighbours.py [--top 5]   (--extra rocm on AMD)

Chat apps strip the capture date, and a picture is often sent long after it was taken, so the
message date can be years off. immich's dates are right; when one of the immich pictures most like
a chat picture is from the same occasion (the owner judges, on `immich-review.py --origin kept`), the
chat picture takes its date. The nearest one alone is not enough: the same people in the same pose
on another day can score higher than another shot of the same evening. Candidates are what the
review pages keep (video notes aside) that have no capture date of their own; embeddings as in
immich-match.py. Writes `<cache>/neighbours.db` (`neighbour`: path, rank, asset,
similarity). Nothing leaves the machine.
"""
import argparse
import importlib.util
import os
import sqlite3
import sys

import numpy as np
import torch
from transformers import AutoModel, AutoProcessor

import common
from common import config

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(config.CACHE, "neighbours.db")


def load(name, file):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, file))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def main():
    ap = argparse.ArgumentParser(description="The immich pictures most like each kept chat picture.")
    ap.add_argument("--top", type=int, default=5)
    args = ap.parse_args()
    os.umask(0o077)
    os.makedirs(config.CACHE, exist_ok=True)
    match_mod, review = load("immich_match", "immich-match.py"), load("immich_review", "immich-review.py")
    match = config.read_only(review.MATCH)
    undated = {p for (p,) in match.execute("SELECT path FROM match WHERE taken IS NULL")}
    paths = sorted(p for p in review.kept() if p in undated)

    index = config.read_only(match_mod.INDEX)
    assets = [a for a in index.execute("SELECT id, embedding FROM asset") if a[1]]
    ids = [a[0] for a in assets]
    device = common.device()
    ref = torch.tensor(np.stack([np.frombuffer(a[1], dtype=np.float32) for a in assets])).to(device)
    ref = torch.nn.functional.normalize(ref, dim=-1)
    model = AutoModel.from_pretrained(match_mod.MODEL).to(device).eval()
    proc = AutoProcessor.from_pretrained(match_mod.MODEL)
    print(f"{len(paths)} χωρίς ημερομηνία λήψης, {len(ids)} στο immich, {device}", file=sys.stderr)

    out = sqlite3.connect(OUT + ".part")
    out.execute("DROP TABLE IF EXISTS neighbour")
    out.execute("CREATE TABLE neighbour (path TEXT, rank INTEGER, asset TEXT, similarity REAL, PRIMARY KEY (path, rank))")
    for path in paths:
        im = match_mod.picture(path)
        if im is None:
            continue
        with torch.no_grad():
            emb = common.features(model.get_image_features(**proc(images=[im], return_tensors="pt").to(device)))
            sim, idx = (torch.nn.functional.normalize(emb, dim=-1) @ ref.T)[0].topk(args.top)
        out.executemany("INSERT INTO neighbour VALUES (?, ?, ?, ?)",
                        [(path, r, ids[j], round(s, 4)) for r, (s, j) in enumerate(zip(sim.tolist(), idx.tolist()))])
    out.commit()
    out.close()
    os.replace(OUT + ".part", OUT)
    print(f"{len(paths)} -> {OUT}", file=sys.stderr)


if __name__ == "__main__":
    main()
