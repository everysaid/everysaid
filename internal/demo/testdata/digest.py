"""Writes digest.json, what demo_test.go checks the Go demo against: the Python demo (seed 7) built
at a fixed hour in UTC, each table's rows and a digest of them, and the files' digests.

    TZ=UTC uv run python internal/demo/testdata/digest.py > internal/demo/testdata/digest.json
"""
from datetime import datetime, timezone
import contextlib
import hashlib
import json
import os
import shutil
import sqlite3
import sys
import tempfile

ROOT = tempfile.mkdtemp(prefix="everysaid-digest-")
for name in ("DATA", "CACHE", "CONFIG", "STATE"):
    os.environ[f"EVERYSAID_{name}"] = os.path.join(ROOT, name.lower())
    os.makedirs(os.environ[f"EVERYSAID_{name}"])
os.environ["EVERYSAID_KEYRING"] = "everysaid-test"
with open(os.path.join(os.environ["EVERYSAID_CONFIG"], "config.toml"), "w", encoding="utf-8") as f:
    f.write('[owner]\nnumbers = ["+15550000000"]\nregion = "US"\nname = "Demo"\n\n[server]\nport = 8530\n')

from everysaid import demo  # noqa: E402

NOW = datetime(2026, 10, 7, 12, 34, 56, tzinfo=timezone.utc)


class Fixed(datetime):
    @classmethod
    def now(cls, tz=None):
        return NOW if tz else NOW.astimezone().replace(tzinfo=None)


demo.datetime = Fixed
with contextlib.redirect_stdout(sys.stderr):
    path = demo.build(7)
# the build's own clock (Unix time when it ran), and the folder, are not the demo's
SKIP = {("handle_name", "first_seen"), ("handle_name", "last_seen"), ("chat_state", "set_at"),
        ("plugin_instance", "settings")}


def value(v):
    if v is None:
        return "N"
    if isinstance(v, float):
        return "f" + repr(v)
    if isinstance(v, int):
        return "i" + str(v)
    if isinstance(v, bytes):
        return "b" + v.hex()
    return "s" + v


db = sqlite3.connect(path)
out = {"now": int(NOW.timestamp()), "tables": {}, "files": {}}
for (t,) in db.execute("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name"):
    cols = [r[1] for r in db.execute(f'PRAGMA table_info("{t}")')]
    h = hashlib.sha256()
    n = 0
    for row in db.execute(f'SELECT * FROM "{t}"'):
        h.update(("\x1f".join(value(v) for c, v in zip(cols, row) if (t, c) not in SKIP) + "\x1e").encode())
        n += 1
    out["tables"][t] = {"rows": n, "sha256": h.hexdigest()}
db.close()
for base in (os.environ["EVERYSAID_DATA"], os.environ["EVERYSAID_CACHE"]):
    for folder, _, files in os.walk(base):
        for name in files:
            p = os.path.join(folder, name)
            if not name.startswith("archive.db"):
                with open(p, "rb") as f:
                    out["files"][os.path.relpath(p, ROOT)] = hashlib.sha256(f.read()).hexdigest()
out["files"] = dict(sorted(out["files"].items()))
shutil.rmtree(ROOT)
json.dump(out, sys.stdout, indent=1)
