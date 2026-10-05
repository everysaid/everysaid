"""One archive, as the core uses it: a reading connection per thread, one writer at a time.

SQLite in WAL mode lets many readers work while one writes. The core reads through `read()` (a
connection of the calling thread, `query_only`), and every change goes through `write()`, which
holds the store's lock and commits or rolls back as a whole. Importers keep using `Archive` (its
own connection); `busy_timeout` makes either wait for the other instead of failing.

`version()` changes whenever anything in the database changes (from this process or another), so
caches built from it (the chat list, the names) know when to rebuild.
"""
from contextlib import contextmanager
import json
import os
import sqlite3
import threading

from .. import archive


class Store:
    def __init__(self, path=archive.DB):
        self.path = os.path.abspath(path)
        if not os.path.exists(self.path):
            raise FileNotFoundError(self.path)
        self._local = threading.local()
        self._lock = threading.RLock()
        self._writer = None
        self._cache = {}
        self._own_writes = 0
        db = self.read()
        v = archive.version(db)
        if v != archive.VERSION:
            raise RuntimeError(f"{self.path}: σχήμα v{v}, γνωστό: v{archive.VERSION}")

    def _connect(self):
        db = sqlite3.connect(self.path, check_same_thread=False)
        db.execute("PRAGMA busy_timeout = 30000")
        db.execute("PRAGMA foreign_keys = ON")
        return db

    def read(self):
        db = getattr(self._local, "db", None)
        if db is None:
            db = self._connect()
            db.execute("PRAGMA query_only = 1")
            self._local.db = db
        return db

    @contextmanager
    def write(self):
        """A connection for changes, inside one transaction (committed at the end, rolled back on
        an error). One writer at a time within the process."""
        with self._lock:
            if self._writer is None:
                self._writer = self._connect()
            db = self._writer
            db.execute("BEGIN IMMEDIATE")
            try:
                yield db
                db.execute("COMMIT")
            except BaseException:
                db.execute("ROLLBACK")
                raise
            finally:
                self._own_writes += 1

    def version(self):
        """Changes with every write: SQLite's data_version, as one connection kept for asking it
        sees it (writes by any other connection or process), plus this store's own writes."""
        with self._lock:
            if getattr(self, "_probe", None) is None:
                self._probe = self._connect()
            return (self._probe.execute("PRAGMA data_version").fetchone()[0], self._own_writes)

    def cached(self, key, build):
        """build() once per version of the database."""
        v = self.version()
        hit = self._cache.get(key)
        if hit and hit[0] == v:
            return hit[1]
        value = build()
        self._cache[key] = (v, value)
        return value

    def setting(self, key, default=None):
        row = self.read().execute("SELECT value FROM setting WHERE key = ?", (key,)).fetchone()
        return json.loads(row[0]) if row else default

    def close(self):
        db = getattr(self._local, "db", None)
        if db is not None:
            db.close()
            self._local.db = None
        for name in ("_writer", "_probe"):
            if getattr(self, name, None) is not None:
                getattr(self, name).close()
                setattr(self, name, None)
