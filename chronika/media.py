"""Link media files to their messages and store each file once, by content, in the archive.

Files are hard-linked (no extra space; copied where the source is on another file system) to
`<media store>/media/<ab>/<sha256><ext>` (config `[media] store`, the data folder by default). Each source records the folder its files are in
(`source.media_root`); `attachment.source_path` is relative to it. The links to messages, each
step skipped when its source is not there:

- iPhone WhatsApp: `ZWAMEDIAITEM.ZMEDIALOCALPATH`, message by stanza id;
- iPhone Viber: `ZATTACHMENT.ZNAME`, message by token;
- Android MMS (`android-export.py`): `mms-parts/<part _id>`, message by MMS id, or by time and
  direction where the iPhone's copy of the message was the one kept;
- Telegram (`telegram-sync.py --media`): `<chat>/<message><ext>`, message by its row key
  (`telegram.media`).

The Android phone's Viber media were linked once, through a `links.tsv` made by a script since retired
(docs/data-sources.md, 5.5); their files are gone and the links stay in the archive.

A file already linked from the same source path is not hashed again, unless its size has changed.
"""
from collections import defaultdict
import hashlib
import mimetypes
import os
import shutil

from . import config, telegram
from .archive import IPHONE, IPHONE_DATA, MEDIA_ROOT, android_exports


PAIR_MS = 2000


def ro(path):
    return config.read_only(path)


def link_or_copy(src, dest):
    """A hard link to src at dest, or a copy wherever a link cannot be made: another file system,
    or one without hard links (each system says so with its own error: EXDEV, EPERM, ENOTSUP,
    EINVAL on FAT and exFAT, ...). A missing source or an existing destination is still an error."""
    try:
        os.link(src, dest)
    except (FileNotFoundError, FileExistsError):
        raise
    except OSError:
        shutil.copy2(src, dest)


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
    return h.hexdigest()


class Store:
    def __init__(self, archive):
        self.archive = archive
        self.root = MEDIA_ROOT
        self.added = defaultdict(int)
        self.missing = defaultdict(int)

    def link(self, source, source_id, path, rel, message_id):
        db = self.archive.db
        if message_id is None:
            self.missing[(source, "χωρίς μήνυμα")] += 1
            return
        if not os.path.isfile(path):
            self.missing[(source, "χωρίς αρχείο")] += 1
            return
        if db.execute("SELECT 1 FROM attachment WHERE source_id = ? AND source_path = ? AND message_id = ?",
                      (source_id, rel, message_id)).fetchone():
            return
        # the file of this source path hashed before, unless it is another file now (a new export whose
        # part numbers start again): then its size tells
        row = db.execute("SELECT a.sha256 FROM attachment a JOIN media md ON md.sha256 = a.sha256 "
                         "WHERE a.source_id = ? AND a.source_path = ? AND md.size = ?",
                         (source_id, rel, os.path.getsize(path))).fetchone()
        digest = row[0] if row else sha256(path)
        if not db.execute("SELECT 1 FROM media WHERE sha256 = ?", (digest,)).fetchone():
            ext = os.path.splitext(path)[1].lower()
            stored = f"media/{digest[:2]}/{digest}{ext}"
            target = os.path.join(self.root, stored)
            os.makedirs(os.path.dirname(target), exist_ok=True)
            if not os.path.exists(target):
                link_or_copy(path, target)
            db.execute("INSERT INTO media VALUES (?, ?, ?, ?)",
                       (digest, os.path.getsize(path), mimetypes.guess_type(path)[0], stored))
        db.execute("INSERT INTO attachment (message_id, sha256, source_id, source_path) VALUES (?, ?, ?, ?)",
                   (message_id, digest, source_id, rel))
        self.added[source] += 1


def by_key(archive, service):
    sid = archive.service[service]
    return dict(archive.db.execute("SELECT key, id FROM message WHERE service_id = ? AND key IS NOT NULL", (sid,)))


def by_origin(archive, source_name):
    return dict(archive.db.execute(
        "SELECT o.row_key, o.message_id FROM message_origin o JOIN source s ON s.id = o.source_id "
        "WHERE s.name = ?", (source_name,)))


def whatsapp(archive, store):
    path = f"{IPHONE_DATA}/whatsapp.sqlite"
    if not os.path.exists(path):
        return
    src = archive.source(f"{IPHONE}/whatsapp", path, IPHONE, f"{IPHONE_DATA}/whatsapp-media")
    messages = by_key(archive, "whatsapp")
    db = ro(path)
    for stanza, path in db.execute(
            "SELECT m.ZSTANZAID, i.ZMEDIALOCALPATH FROM ZWAMEDIAITEM i JOIN ZWAMESSAGE m ON m.Z_PK = i.ZMESSAGE "
            "WHERE i.ZMEDIALOCALPATH IS NOT NULL ORDER BY i.Z_PK"):
        rel = path.removeprefix("Media/")
        store.link(f"{IPHONE}/whatsapp", src, f"{IPHONE_DATA}/whatsapp-media/{rel}", rel, messages.get(stanza))


def viber_iphone(archive, store):
    database = f"{IPHONE_DATA}/viber.sqlite"
    if not os.path.exists(database):
        return
    src = archive.source(f"{IPHONE}/viber", database, IPHONE, f"{IPHONE_DATA}/viber-media")
    messages, origins = by_key(archive, "viber"), by_origin(archive, f"{IPHONE}/viber")
    files = {}
    for folder in ("Attachments", "FileMessages", "VoiceMessages"):
        path = f"{IPHONE_DATA}/viber-media/{folder}"
        for name in os.listdir(path) if os.path.isdir(path) else ():     # none yet, or all taken and removed
            files[name] = f"{folder}/{name}"
    db = ro(database)
    for pk, token, name in db.execute(
            "SELECT m.Z_PK, m.ZTOKEN, a.ZNAME FROM ZVIBERMESSAGE m JOIN ZATTACHMENT a ON a.Z_PK = m.ZATTACHMENT "
            "WHERE a.ZNAME IS NOT NULL ORDER BY m.Z_PK"):
        if name not in files:
            continue                    # never downloaded on the phone
        message = messages.get(str(token)) if token else origins.get(str(pk))
        store.link(f"{IPHONE}/viber", src, f"{IPHONE_DATA}/viber-media/{files[name]}", files[name], message)


def mms_android(archive, store):
    for device, path, folder in android_exports():
        mms_export(archive, store, device, path, folder)


def mms_export(archive, store, device, database, folder):
    name = f"{device}/mms"
    src = archive.source(name, database, device, folder)
    origins = by_origin(archive, name)
    db = ro(database)
    for pid, mid, date, box in db.execute(
            "SELECT p._id, p.mid, m.date, m.msg_box FROM mms_part p JOIN mms m ON m._id = p.mid "
            "WHERE p._data IS NOT NULL ORDER BY CAST(p._id AS INTEGER)"):
        path = f"{folder}/mms-parts/{pid}"
        if os.path.isfile(path) and os.path.getsize(path) == 0:
            continue                    # parts the provider no longer returns
        message = origins.get(mid)
        if message is None:             # the iPhone's copy was kept: same time and direction
            ts = int(date) * 1000
            rows = archive.db.execute(
                "SELECT id FROM message WHERE service_id IN (?, ?) AND outgoing = ? AND ts BETWEEN ? AND ?",
                (archive.service["sms"], archive.service["mms"], int(box == "2"), ts - PAIR_MS, ts + PAIR_MS)).fetchall()
            message = rows[0][0] if len(rows) == 1 else None
        store.link(name, src, path, f"mms-parts/{pid}", message)


def run(archive):
    store = Store(archive)
    for step in (whatsapp, viber_iphone, mms_android, telegram.media):
        step(archive, store)
        archive.db.commit()
    for source, n in sorted(store.added.items()):
        print(f"νέα:    {n:6} {source}")
    for (source, why), n in sorted(store.missing.items()):
        print(f"χωρίς:  {n:6} {source} ({why})")
    db = archive.db
    count, size = db.execute("SELECT count(*), coalesce(sum(size), 0) FROM media").fetchone()
    print(f"σύνολο: {count} αρχεία, {size / 1e9:.1f} GB, "
          f"{db.execute('SELECT count(*) FROM attachment').fetchone()[0]} συνδέσεις με μηνύματα")
