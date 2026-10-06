"""Library plugins: where kept pictures and videos go. In the first version, a folder on disk and
immich. Each answers whether a file is already there, stores one with its date, and gives a stored
file back (for showing it in the app).

A file's date is its own EXIF date where it has one (never changed); else the date given (the
message's, or one the user typed) is written into the copy that is stored, with the camera make
where the file has none (`make`, by default "Everysaid": how chat media are told apart in a library).
"""
import hashlib
import json
import mimetypes
import os
import shutil
import sqlite3
import subprocess
import tempfile
import time
from datetime import datetime
import urllib.request
import uuid

from .. import config
from .base import Plugin, Setting


def _sha(path, algo="sha256"):
    h = hashlib.new(algo)
    with open(path, "rb") as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
    return h.hexdigest()


def _exiftool():
    return shutil.which("exiftool")


def prepared_copy(src, date_ms, make, service):
    """A temporary copy of src carrying the date (where it has no capture date of its own) and the
    make and model (where it has no make). Without exiftool, the copy as it is."""
    fd, tmp = tempfile.mkstemp(suffix=os.path.splitext(src)[1].lower())
    os.close(fd)
    shutil.copy2(src, tmp)
    tool = _exiftool()
    if tool and date_ms:
        when = datetime.fromtimestamp(date_ms / 1000, config.TIMEZONE)
        stamp = when.strftime("%Y:%m:%d %H:%M:%S")
        offset = when.strftime("%z")
        offset = offset[:3] + ":" + offset[3:] if offset else ""
        # only where the file has no capture date of its own (-if): an EXIF date is never changed
        subprocess.run([tool, "-q", "-q", "-overwrite_original", "-P", "-m",
                        "-if", "not $DateTimeOriginal and not $CreateDate",
                        f"-AllDates={stamp}", f"-OffsetTimeOriginal={offset}", tmp],
                       capture_output=True, text=True, encoding="utf-8")
        if make:
            subprocess.run([tool, "-q", "-q", "-overwrite_original", "-P", "-m", "-if", "not $Make",
                            f"-Make={make}", f"-Model={service or ''}", tmp], capture_output=True, text=True,
                           encoding="utf-8")
    if date_ms:
        os.utime(tmp, (date_ms / 1000, date_ms / 1000))
    return tmp


class Folder(Plugin):
    id = "folder"
    name = "Folder"
    kind = "library"
    description = "A folder on disk: kept files go into year/month subfolders, named by date and service."
    settings = (
        Setting("path", "Folder", "path", required=True),
        Setting("make", "Camera make (where missing)", "text", default=config.IMMICH_MAKE),
    )

    def _root(self, ctx):
        return os.path.expanduser(ctx.settings.get("path") or "")

    def _index(self, ctx):
        """sha256 of every file in the folder, refreshed for files new or changed since last time."""
        db = sqlite3.connect(os.path.join(config.CACHE, f"library-{ctx.id}.db"))
        db.execute("CREATE TABLE IF NOT EXISTS file (path TEXT PRIMARY KEY, size INTEGER, mtime INTEGER, sha256 TEXT)")
        root = self._root(ctx)
        seen = set()
        for d, _, names in os.walk(root):
            for n in names:
                p = os.path.join(d, n)
                rel = os.path.relpath(p, root)
                st = os.stat(p)
                seen.add(rel)
                row = db.execute("SELECT size, mtime FROM file WHERE path = ?", (rel,)).fetchone()
                if row != (st.st_size, int(st.st_mtime)):
                    db.execute("INSERT OR REPLACE INTO file VALUES (?, ?, ?, ?)", (rel, st.st_size, int(st.st_mtime), _sha(p)))
        for (rel,) in db.execute("SELECT path FROM file").fetchall():
            if rel not in seen:
                db.execute("DELETE FROM file WHERE path = ?", (rel,))
        db.commit()
        return db

    def check(self, ctx):
        root = self._root(ctx)
        if not root:
            return False, "missing: the folder"
        if not os.path.isdir(root):
            return False, f"not found: {root}"
        return True, "ready"

    def find(self, ctx, sha256, path=None):
        db = self._index(ctx)
        row = db.execute("SELECT path FROM file WHERE sha256 = ?", (sha256,)).fetchone()
        return row[0] if row else None

    def store(self, ctx, path, meta):
        root = self._root(ctx)
        date_ms = meta.get("date_ms") or int(os.stat(path).st_mtime * 1000)
        when = datetime.fromtimestamp(date_ms / 1000, config.TIMEZONE)
        ext = os.path.splitext(path)[1].lower() or mimetypes.guess_extension(meta.get("mime") or "") or ""
        folder = os.path.join(root, f"{when:%Y}", f"{when:%m}")
        os.makedirs(folder, exist_ok=True)
        base = f"{when:%Y-%m-%d %H%M%S} {meta.get('service') or 'chat'}"
        dest, n = os.path.join(folder, base + ext), 2
        while os.path.exists(dest):
            dest, n = os.path.join(folder, f"{base} {n}{ext}"), n + 1
        tmp = prepared_copy(path, date_ms, ctx.settings.get("make") or config.IMMICH_MAKE, meta.get("service"))
        shutil.move(tmp, dest)
        return os.path.relpath(dest, root)

    def fetch(self, ctx, ref, size="original"):
        p = os.path.join(self._root(ctx), ref)
        return ("path", p) if os.path.exists(p) else None


class Immich(Plugin):
    id = "immich"
    name = "immich"
    kind = "library"
    description = ("An immich server, through its API. The API key needs asset.read, asset.view (previews), "
                   "asset.download (originals) and asset.upload.")
    settings = (
        Setting("url", "Address", "url", required=True, default=config.IMMICH_URL),
        Setting("key", "API key", "secret", help="If empty, the scripts' immich-key is used"),
        Setting("make", "Camera make (where missing)", "text", default=config.IMMICH_MAKE),
    )

    def _key(self, ctx):
        return ctx.secret("key") or config.secret("immich-key")

    def _req(self, ctx, method, path, body=None, headers=None, raw=False, timeout=60):
        url = (ctx.settings.get("url") or config.IMMICH_URL or "").rstrip("/") + "/api" + path
        h = {"x-api-key": self._key(ctx) or "", "Accept": "application/json", **(headers or {})}
        data = body
        if isinstance(body, (dict, list)):
            data = json.dumps(body).encode()
            h["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=data, headers=h, method=method)
        with urllib.request.urlopen(req, timeout=timeout) as r:
            content = r.read()
            return (content, r.headers.get("Content-Type")) if raw else json.loads(content or b"null")

    def check(self, ctx):
        if not (ctx.settings.get("url") or config.IMMICH_URL):
            return False, "missing: the address"
        if not self._key(ctx):
            return False, "missing: the API key"
        try:
            self._req(ctx, "GET", "/server/ping", timeout=10)
        except Exception as e:
            return False, f"no answer: {e}"
        return True, "ready"

    def find(self, ctx, sha256, path=None):
        if not path or not os.path.exists(path):
            return None
        sha1 = _sha(path, "sha1")
        r = self._req(ctx, "POST", "/assets/bulk-upload-check", {"assets": [{"id": sha256, "checksum": sha1}]})
        for x in r.get("results", []):
            if x.get("action") == "reject" and x.get("assetId"):
                return x["assetId"]
        return None

    def store(self, ctx, path, meta):
        date_ms = meta.get("date_ms") or int(os.stat(path).st_mtime * 1000)
        tmp = prepared_copy(path, date_ms, ctx.settings.get("make") or config.IMMICH_MAKE, meta.get("service"))
        try:
            when = datetime.fromtimestamp(date_ms / 1000, config.TIMEZONE).isoformat()
            boundary = uuid.uuid4().hex
            name = f"{meta.get('service') or 'chat'} {datetime.fromtimestamp(date_ms / 1000, config.TIMEZONE):%Y-%m-%d %H%M%S}{os.path.splitext(path)[1].lower()}"
            fields = {"deviceAssetId": meta.get("sha256") or _sha(path), "deviceId": "everysaid",
                      "fileCreatedAt": when, "fileModifiedAt": when}
            parts = []
            for k, v in fields.items():
                parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode())
            with open(tmp, "rb") as f:
                parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="assetData"; filename="{name}"\r\n'
                             f'Content-Type: {meta.get("mime") or "application/octet-stream"}\r\n\r\n'.encode() + f.read() + b"\r\n")
            parts.append(f"--{boundary}--\r\n".encode())
            r = self._req(ctx, "POST", "/assets", b"".join(parts),
                          {"Content-Type": f"multipart/form-data; boundary={boundary}"}, timeout=600)
            return r["id"]
        finally:
            os.remove(tmp)

    def fetch(self, ctx, ref, size="preview"):
        path = f"/assets/{ref}/original" if size == "original" else f"/assets/{ref}/thumbnail?size={'preview' if size == 'preview' else 'thumbnail'}"
        content, ctype = self._req(ctx, "GET", path, raw=True)
        return ("bytes", content, ctype)


PLUGINS = (Folder, Immich)


def link(store, instance_id, label, sha256, ref, method):
    with store.write() as db:
        db.execute("INSERT INTO library_link (sha256, library, asset_id, method, linked_at, instance_id) "
                   "VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (sha256, library) DO UPDATE SET asset_id = excluded.asset_id, "
                   "method = excluded.method, instance_id = excluded.instance_id",
                   (sha256, label, ref, method, int(time.time()), instance_id))
