#!/usr/bin/env python3
"""Tag chat pictures by what they show (a dog, ...), with a local SigLIP model, zero-shot.

    uv run --extra ml --extra torch python scripts/media-tags.py LABEL="TEXT"... [--paths FILE]   (--extra rocm on AMD)

For example `σκύλος="a photo of a dog"`. Each picture (videos: a frame one second in) is scored
against each text by google/siglip2-base-patch16-224 (the model immich-match.py uses, in the
HuggingFace cache), whose sigmoid output is a probability for that text alone, so the labels are
independent of each other. Scores go to `<cache>/faces.db` (`tag`: path, label, score);
the review page shows a picture under a label when its score is THRESHOLD or more. Candidates are
the files in `--paths`, else every picture and video the archive still has; pictures already
scored for a label are skipped. Runs on the GPU when there is one (CUDA or ROCm, Apple's MPS). Nothing leaves the machine.
"""
import argparse
import os
import sqlite3
import sys

import torch
from transformers import AutoModel, AutoProcessor

import common
from common import config

ARCHIVE_DB = os.path.join(config.DATA, "archive.db")
OUT = os.path.join(config.CACHE, "faces.db")
MODEL = "google/siglip2-base-patch16-224"
BATCH = 32


def main():
    ap = argparse.ArgumentParser(description="Tag chat pictures by what they show.")
    ap.add_argument("tags", nargs="+", help='LABEL="text"')
    ap.add_argument("--paths", help="file with the pictures to look at, one per line")
    args = ap.parse_args()
    tags = dict(t.split("=", 1) for t in args.tags)
    os.umask(0o077)
    os.makedirs(config.CACHE, exist_ok=True)

    if args.paths:
        paths = [l.strip() for l in open(args.paths) if l.strip()]
    else:
        db = sqlite3.connect(f"file:{ARCHIVE_DB}?mode=ro", uri=True)
        paths = [common.media_file(p) for (p,) in db.execute(
            "SELECT path FROM media WHERE mime LIKE 'image/%' OR mime LIKE 'video/%'")]
    paths = [p for p in paths if os.path.exists(p)]
    out = sqlite3.connect(OUT)
    out.execute("CREATE TABLE IF NOT EXISTS tag (path TEXT, label TEXT, score REAL, PRIMARY KEY (path, label))")
    done = {}
    for p, label in out.execute("SELECT path, label FROM tag"):
        done.setdefault(p, set()).add(label)
    todo = [p for p in paths if not set(tags) <= done.get(p, set())]
    device = common.device()
    print(f"{len(paths)} εικόνες, μένουν {len(todo)}, {device}", file=sys.stderr)
    if not todo:
        return

    model = AutoModel.from_pretrained(MODEL).to(device).eval()
    proc = AutoProcessor.from_pretrained(MODEL)
    labels = list(tags)
    with torch.no_grad():
        t = proc(text=[tags[l] for l in labels], padding="max_length", max_length=64, return_tensors="pt").to(device)
        text = torch.nn.functional.normalize(common.features(model.get_text_features(**t)), dim=-1)
    scale, bias = model.logit_scale.exp(), model.logit_bias

    def flush(batch):
        if not batch:
            return
        with torch.no_grad():
            x = proc(images=[im for _, im in batch], return_tensors="pt").to(device)
            img = torch.nn.functional.normalize(common.features(model.get_image_features(**x)), dim=-1)
            prob = torch.sigmoid(img @ text.T * scale + bias).cpu().tolist()
        for (p, _), row in zip(batch, prob):
            out.executemany("INSERT OR REPLACE INTO tag VALUES (?, ?, ?)",
                            [(p, l, round(s, 4)) for l, s in zip(labels, row)])
        batch.clear()

    batch = []
    for i, p in enumerate(todo, 1):
        im = common.picture(p, 640)
        if im is not None:
            batch.append((p, im))
        if len(batch) == BATCH:
            flush(batch)
            out.commit()
        if i % 500 == 0:
            print(f"  {i}/{len(todo)}", file=sys.stderr)
    flush(batch)
    out.commit()
    for l in labels:
        scores = sorted((s for (s,) in out.execute("SELECT score FROM tag WHERE label = ?", (l,))), reverse=True)
        print(f"{l}: {len(scores)} · ≥0.5: {sum(s >= .5 for s in scores)} · ≥0.1: {sum(s >= .1 for s in scores)} "
              f"· ≥0.01: {sum(s >= .01 for s in scores)}")


if __name__ == "__main__":
    main()
