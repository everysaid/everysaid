# How the data is obtained: a technical reference

This document describes, source by source, how Chronika gets at each piece of personal data: the
first acquisition, how it is refreshed, what is incremental and what is not, and how each obstacle
that keeps a user from their own data is got round (encrypted backups, an encrypted desktop
database, providers without an export, text hidden in binary blobs). It is written from the code as
it stands (October 2026); where the code and the README disagree, the code is described and the
difference noted in section 13.

`README.md` is the overview. This file is the "how". Notes about one installation (its devices,
paths, history and numbers) belong in a local, untracked `LOCAL.md`, not here.

Placeholders: `<data>`, `<cache>` and `<config>` are Chronika's folders (README, "Folders and
configuration"); `<backup_root>` is `[iphone] backup_root`; `<export>` is `[android] export`, with
one `<device>` folder per Android phone.

Contents:

1. [Overview and data flow](#1-overview-and-data-flow)
2. [Principles shared by every step](#2-principles-shared-by-every-step)
3. [iPhone: the encrypted backup](#3-iphone-the-encrypted-backup)
4. [Android: content providers over adb](#4-android-content-providers-over-adb)
5. [Viber](#5-viber)
6. [WhatsApp](#6-whatsapp)
7. [SMS, MMS, iMessage, RCS](#7-sms-mms-imessage-rcs)
8. [Calls](#8-calls)
9. [The unified archive](#9-the-unified-archive)
10. [Media: from the archive to the photo library](#10-media-from-the-archive-to-the-photo-library)
11. [Refresh runbook](#11-refresh-runbook)
12. [Incremental behaviour, summarised](#12-incremental-behaviour-summarised)
13. [Known gaps, pitfalls and inconsistencies](#13-known-gaps-pitfalls-and-inconsistencies)
14. [Files, permissions, caches](#14-files-permissions-caches)

---

## 1. Overview and data flow

```
 iPhone ──USB/usbmuxd──► idevicebackup2 ──► <backup_root>/<UDID>/   (encrypted, Finder format)
                                                          │  iphone_backup_decrypt + password
                                                          ▼
                                  <cache>/iphone/  sms.db, CallHistory.storedata,
                                                   viber.sqlite, whatsapp.sqlite,
                                                   whatsapp-contacts.sqlite,
                                                   whatsapp-calls.sqlite,
                                                   viber-media/, whatsapp-media/
 Android phone ──adb── content query/read ─────► <export>/<device>/android.db (+ *.txt.gz, mms-parts/)
               ──adb pull Android/data/com.viber.voip/files ──► a folder of Viber media
 Viber Desktop (linked to the Android phone) ── LD_PRELOAD export ──► viber-desktop-<date>.sqlite
 whatsapp-mcp bridge (whatsmeow, live) ─────────► whatsapp-bridge/store/messages.db, whatsapp.db

                     uv run python -m chronika sms calls viber whatsapp telegram voip media
                                                          ▼
                     <data>/archive.db  +  <cache>/media/<ab>/<sha256><ext>
                                                          │
            immich (API or nightly dump) ─► immich-index ─► immich-match / immich-dupes / media-vlm
                                                          ▼
                     review pages (user approves) ─► media-prune: library_link, local copies removed
```

Three layers, each rebuildable from the one before it:

| Layer | Where | Written by | Rebuildable from |
|---|---|---|---|
| Raw acquisition | `<backup_root>/`, `<export>/<device>/` | `idevicebackup2`, `android-export.py`, `adb pull`, the Viber export | the devices only (an encrypted iPhone backup is the only copy of the phone's call history) |
| Decrypted extracts | `<cache>/iphone/` | `iphone-sync.py` | the encrypted backup |
| Unified archive | `<data>/archive.db`, `<cache>/media/` | `chronika` | the extracts; but see section 13: once media are pruned, the archive is the only record |

An Android export may be removed once everything in it is in the archive (9.1, "Both origins of a
pair"); the importers run without it.

---

## 2. Principles shared by every step

- **Read only towards devices.** iPhone: only `idevicebackup2 backup` (the phone sends, nothing is
  written to it). Android: only `adb exec-out content query` / `content read` and `adb pull`. No
  restore, no `adb push`, no provider writes, without the user's explicit agreement.
- **The encrypted backup is precious.** `<backup_root>/` is modified only by
  `idevicebackup2 backup`. Every other tool opens it read only. Pruning media never touches it.
- **Private on disk.** Every script that writes decrypted data sets `umask 077` (folders 700, files
  600). This covers `iphone-sync.py`, `Archive`, `immich-index.py`, `media-vlm.py` and the review
  pages; the Viber export log is created under umask 077 too.
- **Atomic replacement.** Files are written as `NAME.part` and then `os.replace`d over the old
  one, so an interrupted run never leaves a half-written database in place. This covers the
  extracts, the media files, the password file and the MMS parts.
- **Idempotent imports.** Every importer can be run again. A row is identified by `(source,
  row_key)`, its id in that source, and is skipped if already present. A message's service-wide id
  (`message.key`) stops the same message arriving twice from two sources.
- **One origin per record.** A record found on two devices is stored once, from the device that was
  in use at the time (`device.used_from`/`used_until` in the archive, `Archive.keeper()`): an
  Android phone during its period of use, the iPhone otherwise (the newest device carries the
  history copied from phone to phone). `message_origin` / `call_origin` record which source row
  became which record; the source row itself is not kept (9.1), so the extracts must stay.
- **No channels.** Viber channels and WhatsApp channels/status are never imported (5.7, 6.4).
- **Local processing only for private media.** Classification, similarity and hashing run on this
  machine. Sending anything to an outside model needs the user's consent each time.
- **The photo library gets nothing without approval.** Nothing goes to immich unless the user
  approves each item. A picture there carries its real capture date.
- **Secrets.** The backup password and the immich key live in the system's keyring (service
  `chronika`), or, where there is none, in `<config>/<name>` (600); see README,
  "Secrets". Only the scripts read them (`config.secret()`). They are never an argument and never
  printed.

---

## 3. iPhone: the encrypted backup

### 3.1 Transport and pairing

- `libimobiledevice`: `idevicepair`, `ideviceinfo`, `idevicebackup2`.
- `usbmuxd` is started by udev when the phone is plugged in. The scripts use the cable; Wi-Fi sync
  and iCloud backup play no part.
- Pairing creates a lockdown pairing record (host certificate and keys) on both sides; the phone
  shows "Trust this computer". To check it:
  - `idevicepair validate`
  - `ideviceinfo -q com.apple.mobile.backup` (must show `WillEncrypt: true`)
- Device: its UDID, set in `config.toml` (`[iphone] udid`); without it the
  scripts take the only backup in `[iphone] backup_root`, else the only phone on the cable.

### 3.2 Why encrypted, and what that protects

- iOS chooses the backup's content by whether it is encrypted. Only an encrypted backup contains
  **call history** (`CallHistory.storedata`), the keychain, Health and Safari history.
- So encryption must stay on. Turning it off would silently drop the calls from every later backup.
- The password is a setting of the phone, not of a single backup. It cannot be removed or changed
  without the old one.
- If the password is unknown, the only way out is
  `Settings > General > Transfer or Reset iPhone > Reset > Reset All Settings`.
  - This clears the backup password without deleting data.
  - It resets Wi-Fi networks, the home screen layout, permissions and notification settings.
  - A new password is then set from Finder or iTunes ("Encrypt local backup") or with
    `idevicebackup2 encryption on`.

### 3.3 Making the backup (first and incremental)

`iphone-sync.py` runs `idevicebackup2 -u <UDID> backup [--full] <backup_root>`.

**Format** (the one Finder uses):

- `Info.plist` and `Status.plist`.
- `Manifest.plist`, which holds the `BackupKeyBag`, the `ManifestKey` and the flag `IsEncrypted`.
- `Manifest.db`, an encrypted SQLite catalogue, table `Files`:
  - `fileID`, which is `sha1("<domain>-<relativePath>")`;
  - `domain` and `relativePath`;
  - `flags` (1 file, 2 folder, 4 symlink);
  - `file`, a binary plist with size, modes and the per-file wrapped key.
- The file contents, at `<UDID>/<fileID[:2]>/<fileID>`, each encrypted on its own.

**Runs:**

- **First run.** A full backup, by idevicebackup2 or by Finder; idevicebackup2 continues a Finder
  backup in place.
- **Later runs are incremental.** The phone sends only files whose content changed, and tells the
  host which to delete; `Manifest.db` is rewritten. An incremental run takes a few minutes.
- **`--full`** forces a complete backup.
- **Passcode.** The phone may ask for its own passcode to start.
- **Failure.** If `idevicebackup2` returns non-zero, the script stops before touching `Data/`:
  "Το backup απέτυχε· τα αρχεία στο ... δεν άλλαξαν."
- The backup is complete only once `Manifest.plist` exists. `--no-backup` and `--save-password`
  refuse to run without it.

### 3.4 Decrypting: how the encryption is opened

Decryption is done by the Python library `iphone_backup_decrypt` (the `iphone` extra:
`uv run --extra iphone ...`). This is the
standard iOS backup scheme, in outline:

1. **Unlocking the keybag.** The password unlocks the `BackupKeyBag` in `Manifest.plist`.
   - The key is derived with PBKDF2-SHA256, using the keybag's `DPSL` salt and `DPIC` iterations,
     then PBKDF2-SHA1 with `SALT` and `ITER`.
   - This is deliberately slow: a few seconds per attempt. That is why a wrong password takes a
     while to be rejected.
   - The derived key unwraps the class keys (AES key wrap, RFC 3394).
2. **Opening `Manifest.db`.** The `ManifestKey` (prefixed with its protection class) is unwrapped
   with that class key, and `Manifest.db` is decrypted with it (AES-256-CBC) into a temporary file.
   That is what `manifest_db_cursor()` queries.
3. **Opening each file.** Every file's `EncryptionKey`, found in its `file` plist, is unwrapped with
   the key of its protection class. The content is then decrypted with AES-256-CBC and the padding
   removed.

The scripts' usage:

- `EncryptedBackup(backup_directory=..., passphrase=pw)`, then `test_decryption()`. This raises
  `IncorrectPassphraseError` on a wrong password; any other exception is reported separately.
- `extract_file(relative_path=..., domain_like=..., output_filename=...)` gets one file;
  `RelativePath.TEXT_MESSAGES` and `RelativePath.CALL_HISTORY` are the library's constants for
  `Library/SMS/sms.db` and `Library/CallHistoryDB/CallHistory.storedata`.

### 3.5 The password

- **Saving it once.** `iphone-sync.py --save-password` asks with `getpass` (hidden input, any number
  of tries), checks the password against the existing backup, and writes it.
  - The folder `<config>` is created as 700.
  - The file is written through `os.open(..., 0o600)` to `.part` and then `os.replace`d into place.
- **Every later run.**
  - A password file readable by group or others (`st_mode & 0o077`) is refused: "chmod 600 και
    ξανά" (`config.exposed()`; not checked on Windows, where `st_mode` does not say).
  - A wrong stored password stops the run with a message to save it again.
  - Without a file, the password is asked for interactively before anything else, so the rest of
    the run is unattended.
- `iphone-verify.py` and `iphone-ls.py` use the file too, or ask.

### 3.6 Extracting the databases

**Databases in `FILES`** (`iphone-sync.py`):

| Output | Domain | Path in the backup |
|---|---|---|
| `sms.db` | HomeDomain | `Library/SMS/sms.db` |
| `CallHistory.storedata` | HomeDomain | `Library/CallHistoryDB/CallHistory.storedata` |
| `viber.sqlite` | `AppDomainGroup-group.viber.share.container` | `com.viber/database/Contacts.data` |
| `whatsapp.sqlite` | `AppDomainGroup-group.net.whatsapp.WhatsApp.shared` | `ChatStorage.sqlite` |
| `whatsapp-contacts.sqlite` | same | `ContactsV2.sqlite` |
| `whatsapp-calls.sqlite` | same | `CallHistory.sqlite` (WhatsApp's call log) |

`--only NAME...` decrypts only the named databases and stops before the media (e.g.
`iphone-sync.py --no-backup --only whatsapp-calls.sqlite`).

**Refresh:**

- Each database is decrypted in full on every run. They are small: a few MB to a few hundred MB
  (WhatsApp's is the largest).
- Each is written to `.part`, chmod 600, and renamed over the old copy.
- Stale `-wal` / `-shm` files next to the old copy are removed. A reader such as sqlite3 may have
  left them, and they belong to the old file, not the new one.

**WAL:** the backup carries no `-wal` files for these databases (check with
`iphone-ls.py '%' '%sms.db%'` and likewise for the others). iOS checkpoints them into the main file
before backing up, so the single file is complete.

**Found in the backup but not extracted yet:**

- `HomeDomain Library/CallHistoryDB/CallHistoryTemp.storedata` (0.1 MB);
- `MediaDomain Library/SMS/Attachments/...`, the SMS/iMessage attachments.

### 3.7 Extracting media (incremental)

`MEDIA` in `iphone-sync.py`:

| Output folder | Domain | Prefixes taken | Stripped |
|---|---|---|---|
| `viber-media/` | `AppDomain-com.viber` | `Documents/Attachments/`, `Documents/FileMessages/`, `Documents/VoiceMessages/` | `Documents/` |
| `whatsapp-media/` | `AppDomainGroup-group.net.whatsapp.WhatsApp.shared` | `Message/Media/` | `Message/Media/` |

How a refresh works:

1. List `Files WHERE flags = 1 AND domain = ?` from `Manifest.db` and keep the paths with those
   prefixes.
2. Skip a path whose output file already exists. Media files have unique names and never change
   once written, so existence is enough: no hashing, no dates.
3. Skip a path the archive has already taken (`archived_media()`).
   - It opens `archive.db` **read only** and collects `attachment.source_path` for the sources
     `iphone/whatsapp` and `iphone/viber`.
   - The archive keeps these rows after the file has gone to immich or been removed. So pruned
     media do not come back, while media that arrive late for old messages are still picked up.
   - This replaced an earlier date watermark, which would have lost late arrivals.
4. Otherwise decrypt to `.part`, chmod 600, rename. Folders are created with mode 700 at every
   level.

What this means for deletions:

- Files deleted on the phone disappear from the next backup. Copies already in `Data/` stay; the
  archive only ever adds.
- The encrypted backup itself keeps whatever the phone currently has. Pruning acts only on the
  decrypted copies.

### 3.8 Looking without extracting, and verifying

- **`iphone-ls.py DOMAIN_LIKE [PATH_LIKE] [--depth N]`** lists without extracting anything.
  - It groups `Files` rows by domain and the first N path components, with counts and sizes.
  - The size comes from the `Size` in each row's `file` plist (`$objects[1]`).
- **`iphone-verify.py`** checks the whole backup.
  - It decrypts every `flags = 1` file in memory (`_decrypt_inner_file`) and compares the length
    with the plist's size.
  - Empty files have no key and are counted separately.
  - Mismatches where both sizes are whole 4,096-byte pages are reported as "databases that changed
    during the backup". The library itself notes this happens routinely.
  - Anything else is a problem, and the exit status is 1.
  - Chromium `Web Data` databases (the Google app, Brave) use 2,048-byte pages, so they still show
    up as problems; they are harmless.

### 3.9 Restore (not tested; it overwrites a phone)

1. Turn off Find My on the phone.
2. Run `idevicebackup2 restore --system --settings --reboot -i <backup_root>`.

Alternatively, copy the backup folder into `~/Library/Application Support/MobileSync/Backup/` on
a Mac and use Finder.

---

## 4. Android: content providers over adb

### 4.1 Getting adb access without root

On the phone:

1. Turn on developer options and USB debugging.
2. Set the USB mode to "File transfer".
3. Reconnect the cable and accept the RSA authorisation prompt.

Before that, a phone exposes only MTP and mass storage. `adb devices -l` shows its serial.

- The `shell` user may query the telephony providers. `content query` runs as `shell`; on the
  phone this was written against it could read the call log, SMS and MMS providers (vendors may
  restrict this).
- It can also read `/sdcard/Android/data/<pkg>` with `adb pull`. That folder is hidden from apps
  since Android 11, but is still accessible to `shell`.
- No root and no backup app was needed.

**If adb hangs:** a stuck adb server is fixed by `adb kill-server` and `adb start-server`. Long
transfers must not be wrapped in a short timeout, which kills a run half way.

### 4.2 `scripts/android-export.py`

| Table | Provider URI |
|---|---|
| `calls` | `content://call_log/calls` |
| `sms` | `content://sms` |
| `mms` | `content://mms` |
| `mms_part` | `content://mms/part` |
| `blocked` | `content://com.android.blockednumber/blocked` |
| `mms_addr` | `content://mms/<id>/addr`, one query per MMS |

**How each table is read:**

1. **Columns.** An unrestricted `adb exec-out content query --uri URI`. The column names are parsed
   from the `Row: 0 ` line (`(?:^Row: 0 |, )([A-Za-z0-9_]+)=`); duplicate or missing names abort.
2. **Rows.** The same query with `--projection col1:col2:...`, so the column order is known and
   fixed.
3. **Parsing** (`parse`). The text format is `Row: N col=value, col=value, ...` with no escaping.
   Commas and newlines inside a message body are therefore ambiguous.
   - The parser relies on two exact markers. A row starts at `\nRow: <n+1> `, with n counted up.
   - A value ends at `, <next expected column>=`. Because the column sequence is known, a body
     containing ", " or newlines is kept intact.
   - The literal `NULL` becomes SQL NULL.
   - Any deviation aborts naming the row and column.
4. **Checks.** The row count is compared with a second `--projection _id` query, and `_id` must be
   unique. A mismatch aborts before saving.
5. **Saving.**
   - The raw output is kept, gzip-compressed, as `<table>.txt.gz` next to the database.
   - Each table gets exactly the provider's columns, every column `TEXT`. Values are stored as text
     and converted by the importers (`CAST(_id AS INTEGER)`, `int(date)`).
6. **`mms_addr`.** One query per `mms._id` (`content://mms/<id>/addr`); messages without addresses
   are skipped.
7. **MMS parts.** For each `mms_part` row with `_data` not NULL, the binary part is read with
   `adb exec-out content read --uri content://mms/part/<_id>` and written to `mms-parts/<_id>`
   (`.part`, then renamed).
   - A part may come back empty (a placeholder part, or a file the provider no longer returns).
     Such parts are skipped at import because their size is 0.

### 4.3 Incremental behaviour

- **Granularity is the table.** A table already in `android.db` is skipped whole ("υπάρχει ήδη").
  To refresh one, drop it (or write to a new database) and run again.
- **MMS parts are per file.** A part already in `mms-parts/` is not read again, nor one the
  archive has already taken from that export (`attachment.source_path` of the source
  `<folder>/mms`), so parts removed after review do not come back.
- **A retired phone** needs no refresh: its export is a fixed snapshot.

### 4.4 Times and codes (Android)

| Field | Meaning |
|---|---|
| `calls.date`, `sms.date` | Unix **milliseconds** |
| `mms.date` | Unix **seconds** |
| `calls.type` | 1 incoming, 2 outgoing, 3 missed, 5 rejected, 6 blocked, 7 answered elsewhere; 1 and 7 count as answered |
| `sms.type` | 1 inbox; anything else is outgoing |
| `mms.msg_box` | 2 sent |
| `mms_addr.type` | 137 is the sender (PDU `FROM`); 151 / 130 are recipients |

### 4.5 Viber media on an Android phone

- **Source folders.** `adb pull` of:
  - `Android/data/com.viber.voip/files` (the app's own copies);
  - the gallery folders where Viber saves pictures and videos.
- **Where they come from.** The media Viber restored from its Google Drive backup, plus what the
  phone kept.
- **Copied by hand**, once, while Viber is still installed: the app folder goes with the app.
- Linking the files to messages is in 5.5.

---

## 5. Viber

### 5.1 The obstacle

- Viber keeps no message content on its servers and offers no export.
- History does not transfer between Android and iOS.
- The Android backup on Google Drive can only be opened by the Android app, as a restore.
- An iPhone's backup therefore holds only the history since Viber moved to the iPhone. The older
  history from an Android phone exists only inside Viber on Android, which is unreadable without
  root, and in Google Drive in a format only the app reads.

### 5.2 The route

1. Activate Viber on the Android phone. This deactivates it on the iPhone: one primary device per
   number.
2. Restore the history from Google Drive on the Android phone.
3. Link **Viber Desktop** on Linux to the Android phone.
   - It needs a fresh profile. An existing one, linked to the iPhone, can be set aside (its history
     is the iPhone's `viber.sqlite`).
   - The desktop then syncs the whole history from the phone.
4. Read the desktop's database. It is encrypted; see 5.3.
5. Pull the media from the Android phone with `adb pull`; the desktop does not sync media.

Afterwards Viber can be moved back to the iPhone and the desktop linked to it again.

### 5.3 Viber Desktop's encrypted database: `scripts/viber-desktop-export.cpp`

**The lock.**

- `~/.ViberPC/<number>/viber.db` is a SQLCipher-style encrypted SQLite database.
- Viber opens it through Qt SQL with `PRAGMA hexkey = '...'`.
- The key passed there is transformed internally before use, so even with the pragma's value the
  file does not open in a stock SQLCipher.

**The way in: use Viber's own open connection.** An `LD_PRELOAD` library overrides
`QSqlQuery::exec(const QString&)`, mangled as `_ZN9QSqlQuery4execERK7QString`.

1. Every call is forwarded to the real function, found with `dlsym(RTLD_NEXT, ...)`.
2. When the SQL starts with `PRAGMA hexkey` (case-insensitive), the hook is armed.
3. On the **next** `exec` call, with the database now unlocked and before Viber's own statement
   runs, it does the export once, through that same `QSqlQuery` object:
   - `ATTACH DATABASE '<VIBER_PLAIN_OUT>' AS plain KEY ''`. An empty key gives a plain,
     unencrypted SQLite file.
   - `SELECT sqlcipher_export('plain')`. This copies everything if the function exists.
   - If it does not: list `main.sqlite_master` tables, excluding `sqlite_%`, and run
     `CREATE TABLE plain."<t>" AS SELECT * FROM main."<t>"` for each. Then
     `CREATE TABLE plain._schema AS SELECT type, name, tbl_name, sql FROM main.sqlite_master`, which
     keeps the original DDL, with types, keys and indexes, for reference. `CREATE TABLE AS` does
     not copy constraints.
   - `DETACH DATABASE plain`.
4. Every statement and its result go to `/tmp/vb/export.log`, opened under umask 077.

**Building it.** Compile against Viber's bundled Qt 6.10 libraries with the system Qt 6 headers.
`-DQT_NO_VERSION_TAGGING` avoids symbol-version mismatches between the headers and the bundled
libraries.

```
g++ -shared -fPIC -O1 -std=c++17 -DQT_NO_VERSION_TAGGING -o /tmp/viber-export.so \
    scripts/viber-desktop-export.cpp -I/usr/include/qt6 -I/usr/include/qt6/QtCore \
    -I/usr/include/qt6/QtSql -L/opt/viber/lib -Wl,-rpath,/opt/viber/lib -lQt6Core -lQt6Sql -ldl
mkdir -p /tmp/vb
VIBER_PLAIN_OUT=/path/to/viber-desktop-YYYY-MM-DD.sqlite \
    LD_PRELOAD=/tmp/viber-export.so /opt/viber/Viber
```

**Pitfalls:**

- Viber must not already be running. A second start hands over to the first instance, which was
  started without the library.
- Viber ignores SIGTERM, so it has to be quit from its menu or killed.
- The output file holds the whole history in clear: keep it 600.
- `VIBER_PLAIN_OUT` unset means the hook does nothing.

A later export of the same profile is a superset of an earlier one; the importer reads the one
named in `[viber] desktop_export`.

### 5.4 Desktop schema used

| Table | Columns used |
|---|---|
| `Events` | `EventID`, `TimeStamp` (Unix ms), `Direction` (0 in, 1 out), `ChatID`, `ContactID`, `Token`, `Type` (3 = system event) |
| `Messages` | by `EventID`: `Type` (1 text, 2 image, 3 video, 4 sticker, 5 location, 6 voice, 9 text, 10 contact, 11 file, 15 system), `Body`, `Info` (JSON: `fileInfo.FileSize`, `fileInfo.Duration`, `fileInfo.mediaInfo.Width/Height`), `PayloadPath`, `ThumbnailPath`, `StickerID`, `PttID`, `Duration` |
| `ChatInfo` | `ChatID`, `Name`, `Token` (non-empty for groups) |
| `ChatRelation` | `ChatID`, `ContactID` (members) |
| `Contact` | `ContactID`, `MID` (Viber member id), `Number` |
| `DownloadFile` | `EventID`, `DownloadID` (`0-02-05-<64 hex>`, the server's media id) |

`Calls` is empty: Viber calls are not on the desktop.

### 5.5 Android Viber media → messages: `scripts/viber-link-media.py` (retired)

How an Android phone's Viber media were linked to their messages. The script is retired; this is
the record of the method, for whoever writes the step again.

**The primary key.**

- The 32 hex digits in Android's file names are `md5(DownloadFile.DownloadID)`. The names look like
  `IMG-<hex>-V.jpg`, `video-<hex>-V.mp4` or `<hex>.vptt`.
- The iPhone's `ZATTACHMENT.ZID` holds the same DownloadID.
- A name therefore maps to its events with certainty. Method `downloadid`, confidence `certain`;
  the pictures' aspect ratio agrees with the recorded one in all but a handful of cases.

**Other matching steps:**

1. **Byte-identical copies** of a linked file, by content MD5 (mostly the gallery copies), inherit
   its events. Method `content-dup:<file>`, confidence `certain`.
2. **Fallback** for the rest: exact size plus aspect ratio against media events not yet linked.
   - The size is `fileInfo.FileSize`, or `fileInfo.Duration` when the size is missing.
   - The aspect ratio must agree within 0.01, either orientation, against `mediaInfo` Width/Height.
   - One candidate means `probable`, several mean `ambiguous`, none means `unmatched`.
   - The false-positive rate is measured by running the fallback on files whose true event is
     known (under 2% when it was measured).
   - Files left `unmatched` tend to be the phone's own sent originals.

**What does not work as a key:**

- **Size alone.** Android keeps downscaled copies (at most 1,600 px), so many certain links do not
  match the recorded size. Different pictures also share a size by chance.
- **`FileHash`** is the MD5 of the encrypted upload.
- **`EncParams`** is a random per-file key, so it cannot be checked against the files either.

**Output and review.** `viber-media/links.tsv` had the columns `file`, `method`, `confidence` and
`event_ids`. Only `certain` rows were imported. The uncertain ones were reviewed by hand with
`media-review.py` (see 10.6).

**Re-running.**

- The file could carry hand work (rows of deleted files removed, renamed files).
- So the script kept every existing row exactly as it was. It linked only files that had no row,
  and appended them.
- With nothing new it wrote nothing. Otherwise it wrote `links.tsv.part` and renamed it over the
  old file.
- It read the same desktop export that `chronika.viber` imported (`[viber] desktop_export`).
- Byte-identical copies inherited only from `certain` rows.

### 5.6 The iPhone's Viber database (`viber.sqlite`, Core Data)

| Table | Columns used |
|---|---|
| `ZVIBERMESSAGE` | `Z_PK`, `ZDATE` (s since 2001), `ZTOKEN`, `ZTEXT`, `ZSTATE` (`received` / `delivered` / `send`; outgoing = not `received`), `ZSYSTEMTYPE` (`''`/`url`/`formatted` = text, `customLocation`, `systemCallLog`, other = system), `ZATTACHMENT`, `ZCONVERSATION`, `ZPHONENUMINDEX` (sender) |
| `ZATTACHMENT` | `Z_PK`, `ZTYPE` (`picture`, `gif`, `video`, `audio`, `file`, `sticker`, `customLocation`), `ZNAME` (the file name in `Documents/...`), `ZID` (DownloadID) |
| `ZCONVERSATION` | `Z_PK`, `ZGROUPID` (groups), `ZNAME` |
| `ZMEMBER` | `Z_PK`, `ZMEMBERID`, `ZDISPLAYFULLNAME` |
| `ZPHONENUMBER` | `ZMEMBER`, `ZCANONIZEDPHONENUM`, `ZPHONE` |
| `Z_5PHONENUMINDEXES` | `Z_5CONVERSATIONS`, `Z_10PHONENUMINDEXES` (conversation members) |

### 5.7 Import (`chronika/viber.py`)

**Key.** The message token is the same id on every device. This was checked: every message matching
on text and time within 5 s also matches on token, and none matches on text with a different token.
`message.key = str(token)`. System events (`Events.Type` 3) get no key, because tokens can repeat
among them.

**Time.**

- iPhone: `(ZDATE + 978307200) * 1000`.
- Desktop: `TimeStamp`, already in ms.
- A token carries its send time: `(token >> 22) + 292057776050` is Unix ms. This is within 2 s for
  almost every message. `token_time()` uses it only when the date is missing or 0.

**People.**

- `phones()` builds Viber member id → number from the desktop's `Contact` and the iPhone's
  `ZMEMBER` + `ZPHONENUMBER`, with the desktop first.
- A person is stored as a normalised phone address where a number is known, so they meet their SMS
  and calls. Otherwise the person is stored as `('viber', MID)`.
- The user (`[owner] numbers`) is left out of member lists.

**Conversations.**

- Groups get the key `group:<token>` (desktop `ChatInfo.Token`, iPhone `ZGROUPID`) and
  `is_group = 1`.
- One-to-one chats are keyed by their member, with the fallbacks `conversation:<Z_PK>` / `chat:<ChatID>`.

**Channels are skipped:** iPhone `ZCONVERSATION.ZSUBTYPE` 3, desktop `ChatInfo.PGType` 3 (`CHANNEL`).
The notes-to-self chat (a nameless group of the user alone) is kept: it may matter a lot, so check
it before removing anything that belongs to it.

**Extras** (`extras.viber_desktop`, `extras.viber_iphone`): reactions (codes 1-5 as ❤️😂😮😢😡,
every code also in `reaction.code` as `viber:N`, the emoji NULL for 6 and later; one-to-one
reactions without a type `viber:?`, and the counterpart resolved as `"peer"`), replies, edits,
forwards, links and pins (`subtype_code` `viber:9`/`viber:url`, `viber:15`/`viber:systemPinnedMessageCreated`),
and the sender's position (`sender_lat`, `sender_lon`).

**Order and choice of origin.**

1. Read all iPhone rows into memory, indexed by token.
2. Stream the desktop events. A token also on the iPhone, at a time when the desktop's device (the
   Android phone, `[android] device`) was not the one in use, is skipped: the iPhone copy wins.
   Otherwise the desktop copy wins, and the iPhone row is marked as taken.
3. Add the iPhone rows not marked as taken.
4. `add()` skips a row in these cases (the last two are counted and printed as "υπήρχαν ήδη"):
   - its `(source, row_key)` exists;
   - its token is already in `message` for Viber, whatever source it came from;
   - it has no token (some system events) and the same conversation already has a keyless Viber
     message at the same millisecond with the same text. The desktop's `EventID` is local to one
     profile, so without this an export from another profile would bring them again. To test it,
     import into a copy of the archive with the desktop source renamed, so that every `EventID`
     looks new: no rows should be added.

### 5.8 Refreshing Viber

**iPhone.**

- Every `iphone-sync.py` brings a new `viber.sqlite` and the new media.
- `python -m chronika viber calls voip media` adds the new rows (`voip` for the calls in the recents,
  after `calls`); the row key is `Z_PK`, and the token
  dedupes against the desktop rows.

**Desktop (Android history).**

- This is a fixed snapshot. `[viber] desktop_export` (`DESKTOP_DB` in `viber.py`) names the dated
  file.
- Linking Viber Desktop to the iPhone and exporting again gives a new file. Then:
  - point `[viber] desktop_export` at it (media would need a new linking step: `viber-link-media.py`
    is retired);
  - keep the source name `<device>/viber` only if it is the same history, otherwise add a new
    source name;
  - tokens and the keyless-row rule (5.7) keep what is already there from coming in twice.

---

## 6. WhatsApp

### 6.1 Sources

- **iPhone backup** (`whatsapp.sqlite` = `ChatStorage.sqlite`, `whatsapp-contacts.sqlite` =
  `ContactsV2.sqlite`, media under `Message/Media`). The full message history, carried from phone
  to phone.
  - Media are there only as far as the phone still has them: older years are often mostly missing,
    since a move between phones does not carry old files.
- **The whatsapp-mcp bridge** (its `whatsapp-bridge/store/` folder, `[whatsapp] bridge` in
  `config.toml`; optional: without it only the iPhone is read).
  - It is a whatsmeow client linked as a companion device, running live.
  - `messages.db` has the tables `messages` (`id`, `chat_jid`, `sender`, `content`, `timestamp`
    ISO, `is_from_me`, `media_type`) and `chats` (`jid`, `name`).
  - `whatsapp.db`, whatsmeow's store, has `whatsmeow_lid_map` (`lid`, `pn`).
  - The bridge fills the gap between the last iPhone backup and now.
- **Not Android.** WhatsApp on Android keeps its database encrypted (`msgstore.db.crypt15`); it is
  not read yet. Its media folder goes with the app when it is uninstalled.

### 6.2 Keys and people

**Key.** `ZWAMESSAGE.ZSTANZAID` on the iPhone, and `messages.id` in the bridge. Both are the stanza
id WhatsApp sends, the same on every device, so most of the bridge's messages are already present
from the iPhone.

**LIDs** (`<n>@lid`) are WhatsApp's privacy ids; they are not numbers. `People` maps them with:

- the iPhone contacts: `ZWAADDRESSBOOKCONTACT.ZLID` → `ZWHATSAPPID`;
- the bridge: `whatsmeow_lid_map` (`lid` → `pn`). The iPhone takes precedence.

A set of every LID seen anywhere tells LIDs apart from numbers among the bridge's bare senders.
Unmapped LIDs are stored as `('whatsapp', '<n>@lid')`.

**Conversations.**

- `@s.whatsapp.net` and `@lid` chats are keyed by the person.
- Groups (`@g.us`) are keyed by jid, with `is_group = 1`. Members come from `ZWAGROUPMEMBER`, and
  the sender of a group message from `ZGROUPMEMBER` → `ZMEMBERJID`.

### 6.3 Message types and hidden text (iPhone)

**Types.** `ZMESSAGETYPE` was identified from the files each type carries (`file`, `ffprobe`) and
from the metadata:

| Kind | Types |
|---|---|
| text | 0, 7 |
| image | 1, 38 (view once); 11 GIF (silent mp4) |
| video | 2, 39 (view once); 54 round video note |
| voice | 3 |
| contact | 4 |
| location | 5 |
| file | 8 |
| sticker | 15 |
| business messages | 19, 20, 25, 30, 31, 41 |
| deleted | 14 |
| system | everything else (6 group event, 10 notice, 59 call, 66 poll, …) |

`message.subtype` names them (`extras.WHATSAPP_SUBTYPES`): 7 link, 11 gif, 14 deleted, 54 video
note, 6 group event, 10 notice, 59 call, 66 poll.

**Extras** (`extras.whatsapp`): from `ZWAMEDIAITEM.ZMETADATA` field 5 the quoted stanza id, 6 its
sender, 19 its text, 46 the forward score; from `ZWAMESSAGEINFO.ZRECEIPTINFO` field 7 the reactions
(1/2 the jid, 3 the emoji; no jid means the user). Edits and polls were looked for and not found.

**Business messages** (templates, buttons) have `ZTEXT` NULL. Their text is only in
`ZWAMEDIAITEM.ZMETADATA`, a protobuf blob.

- `protobuf_strings()` is a schema-less decoder. It walks varint keys and handles wire types 0, 1, 2
  and 5; any other wire type means "not a protobuf", and decoding stops.
- Length-delimited fields are tried first as nested messages, up to depth 8. If that fails they are
  read as UTF-8 and kept when printable.
- `metadata_text()` keeps sentences: strings with a space or non-ASCII. It drops URLs, `/v/` paths
  and jids, and removes repeats.

### 6.4 Import order (`chronika/whatsapp.py`)

Channels (`...@newsletter`) and status (`status@broadcast`) are skipped (`CHANNELS`).

1. iPhone first. Each row is skipped if `(source, Z_PK)` is present, or if its stanza id is already
   a WhatsApp `message.key`.
2. The bridge next, in `timestamp` order. The row key is `<chat_jid>/<id>`. Only stanza ids the
   archive does not have yet are added.
   - The kind comes from `media_type`: `''` text, `image`, `video`, `audio`, `document`.
     Anything else is file.

Because the iPhone is read first, a message present in both always gets the iPhone's richer row.
The bridge contributes only the tail.

### 6.5 Refresh

- iPhone: `iphone-sync.py`, then `python -m chronika whatsapp calls voip media` (`voip` for the calls, after `calls`).
- Bridge: nothing to do. It fills `messages.db` while running, and the next `whatsapp` import picks
  up the new rows.
- Bridge media are not in the archive yet. They can be downloaded through the `whatsapp` MCP
  server, `download_media`.

---

## 7. SMS, MMS, iMessage, RCS

### 7.1 iPhone `sms.db`

**Tables.**

- `message`: `ROWID`, `guid`, `date`, `is_from_me`, `service`, `text`, `attributedBody`, `handle_id`,
  `cache_has_attachments`, `associated_message_type`, `item_type`.
- `handle`: `id` is the number or address.
- `chat` (`style` 43 = group), `chat_message_join`, `chat_handle_join`.
- `attachment` and `message_attachment_join`, for the mime type of the first attachment.

**Time.** `date` is in nanoseconds since 2001 on modern iOS and in seconds on old rows. Values above
1e11 are treated as ns. Unix ms = `date // 1e6` (ns) or `date * 1000` (s), plus `978307200000`.

**Service.**

- `SMS`, `iMessage` or `RCS` as given.
- SMS with attachments, or in a group chat, is recorded as `mms`.

**Kind.**

- `associated_message_type` ≠ 0 means a reaction (tapback).
- `item_type` ≠ 0 means a system message (group renames and the like).
- Otherwise the kind comes from the attachment's mime (image, video, audio → voice, other → file),
  or is text.

**Hidden text: `attributedBody`.** On newer iOS many messages have `text` NULL. Their text is only
in `attributedBody`, an `NSAttributedString` archived with NeXTSTEP *typedstream* (`streamtyped`),
not a keyed archive. `attributed_text()`:

1. Find the class name `NSString`.
2. Find the next `\x84\x01+` (a string object marker followed by `+`, the C-string type tag).
3. Read the length. It is a single byte normally. `0x81` means a 2-byte little-endian length
   follows; `0x82` means a 4-byte one.
4. Decode that many bytes as UTF-8.

`clean()` then removes U+FFFC, the object replacement character that marks an attachment's place,
and trims.

**Keys.** `guid` is the row key. iMessages also get `message.key = guid`. SMS/MMS have no global id,
so `key` is NULL.

### 7.2 Android SMS and MMS (`android.db`)

- **SMS.**
  - `date` is in ms; `type` 1 means in, anything else out.
  - The address is the counterpart.
  - `body` is the text.
- **MMS.**
  - `date` is in seconds; `msg_box` 2 means sent.
  - Addresses come from `mms_addr`. The members are every address except the user's, and the sender
    is the one of type 137.
  - The text is the concatenation of `text/plain` parts, in `seq` order.
  - The kind comes from the first part that is neither `text/plain` nor `application/smil`.

### 7.3 Pairing the two phones (`chronika/sms.py`)

Phones carry the same history, copied from phone to phone at each change.

1. **`collapse()`** drops exact repeats within one source: same service, direction, sender, text
   and second. An iPhone's inherited history can hold such rows, with a different guid each.
2. **`pair()`** matches one to one, greedily, in iPhone time order.
   - It takes iPhone `sms`/`mms` rows only. iMessage and RCS have no Android twin.
   - The Android index is keyed by (direction, text). The nearest unused Android row within 2 s
     wins.
   - SMS and MMS are paired together, because Android stores many long or link-bearing texts as
     MMS that the iPhone has as SMS.
3. **Choice.** For a pair, the Android row is kept if the Android phone was the device in use at
   its time (`Archive.keeper()`), the iPhone row otherwise. Unpaired rows from both sides are added
   as they are.
4. **Conversations.** SMS and MMS share one conversation per counterpart (service `sms`); a group is
   keyed by its sorted member addresses.
5. **Sources.** `iphone/sms`, `<device>/sms` and `<device>/mms` are separate sources, with the row
   keys `guid`, `_id` and `_id`. Every Android export is read (`archive.android_exports()`): one
   folder per phone, `<export>/<device>/android.db`, the folder's name being the device; and the
   earlier single export, `<export>/<[android] device>.db`. Repeats are dropped within each phone;
   two Android phones are not paired with each other. Any of them may be missing.

### 7.4 Refresh

- `iphone-sync.py` brings a new `sms.db`. Then run `python -m chronika sms calls voip` (`voip` reads the
  carrier's missed-call notices from the new SMS, and must come after `calls`).
- The pairing is recomputed in memory on every run, and only rows whose `(source, row_key)` is new
  are added.
- New iPhone rows are after a retired Android phone's period of use, so they pair with nothing and
  simply come in.

---

## 8. Calls

### 8.1 iPhone `CallHistory.storedata` (Core Data)

- **`ZCALLRECORD`**:
  - `ZDATE` (s since 2001), `ZDURATION` (s), `ZORIGINATED` (1 out), `ZANSWERED`;
  - `ZADDRESS` (the number, in any format);
  - `ZSERVICE_PROVIDER`: `com.apple.Telephony` → phone, `com.apple.FaceTime` → facetime; an app's
    bundle id (with or without its team id in front) → its service (`calls.SERVICES`: WhatsApp,
    Viber, Telegram, Signal, Messenger, Teams, Skype, Zoom, Meet, Discord, Slack, LINE, WeChat); an
    unknown app keeps its bundle id as the service name;
  - `ZUNIQUE_ID`, used as the row key.
- **Missing `ZADDRESS`.** Some records, mostly app calls, have it NULL. The number is then taken
  from `Z_2REMOTEPARTICIPANTHANDLES` → `ZHANDLE.ZVALUE` (`_participant`).
- **"Answered".** For outgoing calls it is defined as `duration > 0`, since `ZANSWERED` is not
  meaningful for them. For incoming calls it is `ZANSWERED`.
- **Retention.** iOS does not seem to prune old calls. An iPhone's history may still start late,
  because a move from another phone brings only the months that phone still had.

### 8.2 Android call log

- `type` 2 is outgoing. Answered means duration > 0 when outgoing, `type` in (1, 7) when incoming.
- `date` is in ms; `number` is the counterpart.

### 8.3 Pairing and refresh

- **Pairing** (`chronika/calls.py`) works as for SMS. The key is (normalised address, direction),
  and the nearest call within 2 s wins.
  - It applies only to the iPhone's `phone` service.
  - Pairs occur only where the two phones' histories overlap (the months a move carried over).
- **Choice.** The device in use at the time (`Archive.keeper()`): the Android phone in its period,
  the iPhone otherwise.
- **Refresh.** `iphone-sync.py`, then `python -m chronika calls voip`. The row key is `ZUNIQUE_ID`.
- **Detail** (`extras.call`): Android `type` 3 missed, 5 rejected, 6 blocked (`detail_code`
  `android:3`...); iPhone `ZCALLTYPE` 8 (video) sets `video` (FaceTime video calls).

### 8.4 App calls and carrier notices (`chronika/voip.py`, importer `voip`)

| Source | Read from | Row key |
|---|---|---|
| `iphone/whatsapp-calls` | `whatsapp-calls.sqlite`, WhatsApp's call log | its call id |
| `iphone/whatsapp` | call bubbles in the chats (`ZMESSAGETYPE` 59; metadata field 87: 1.1 video, 1.2 outcome, 1.3 duration, 1.5 participants) | the bubble's `Z_PK` |
| `iphone/viber-calls` | `viber.sqlite` `ZRECENT` (recents) | its `Z_PK` |
| `sms-alerts` | the carrier's missed-call SMS in the archive itself, read by the parsers enabled in `[import] carrier_notices` (`chronika/carriers/`; `gr`: Greek carriers, Latin look-alike letters normalised, Athens time) | `<message_id>/<i>` |

- The WhatsApp log and the bubbles describe the same calls: they are matched by time window and
  direction, the person taken from the chat, the creator jid as fallback (the log uses LIDs).
- A call already in the archive from **another** source, for the same service, the same person and
  the same direction, within 60 s, is not added again; calls without a known person are never merged (it only fills in video, group, detail and key); calls of the
  same source are never merged with each other, except a carrier notice sent twice (same number,
  same minute). A call log that says answered keeps no "missed" or "busy" from a notice. The carrier notices thus give way to the phone's
  own call log where it has the call.
- **Video.** The log (`ZVIDEO`) and the bubbles (field 1.1) agree on whether a call was a video
  call wherever both describe it.
- Calls get `detail` (missed, unanswered, busy, failed; WhatsApp's outcome 4 is missed and 5
  failed) with the source's code in `detail_code` (`whatsapp:4`, `viber:missed`, `carrier:gr`),
  `video`, `attempts`, `conversation_id` for group calls, and `call_member` (who, outcome, code).
- **Not recoverable:** app calls older than the apps' own logs (WhatsApp's log and Viber's recents
  on the iPhone start roughly when the app came to that phone), and phone calls older than any
  phone's call log, except those the carrier notices record.
- **Participants.** In `whatsapp-calls.sqlite` the participants (`ZWACDCALLEVENTPARTICIPANT`)
  belong to the aggregate event (`Z1PARTICIPANTS` → `ZWAAGGREGATECALLEVENT`, `Z1CALLEVENTS` on each
  call), not to the call itself. Read that way, the log's person agrees with the chat bubble's
  almost always, and every call of the log has its person.

---

## 9. The unified archive

### 9.1 Schema (`chronika/archive.py`)

`PRAGMA user_version` is 1 until the first release; until then the schema changes in place, without
migrations.

**Lookup tables and vocabularies:**

- `service` (sms, mms, imessage, rcs, viber, whatsapp, phone, facetime, telegram, messenger,
  signal; a call app found in the iPhone's log adds its own), with `key_scope`: `service` where a
  message id is unique across the service, `conversation` where only within a chat (Telegram);
- `address_kind`: `phone`, `email`, `sender` (an SMS sender name), `uri` (sip:...), shared by every
  service; `id`, `username`, `name`, within one service (`address.service_id`): a Viber member id,
  a WhatsApp LID, a Telegram user id or username, a name where a service gives nothing else;
- `message_kind` (text, image, video, voice, file, sticker, location, contact, call, system,
  reaction);
- `vocabulary` (field, name): what `message.subtype` (link, gif, video note, deleted, notice, call,
  invalid, group event, poll, pin, location, tapback), `call.detail` (missed, unanswered, rejected,
  blocked, busy, failed) and `call_member.outcome` (joined and the call details) may say; triggers
  refuse anything else. The source's own code goes beside each (`subtype_code`, `detail_code`,
  `outcome_code`, `reaction.code`), e.g. `whatsapp:54`, `viber:systemCallLog`, `android:5`.

**People and devices:**

| Table | Contents |
|---|---|
| `address` | `kind_id`, `value`, `service_id` (NULL for the shared kinds); unique on (kind, value, service) |
| `person` | `name` (set by the user), `name_source` (where the user pinned their name to come from: a source of names, or `address:<id>`), `contact_uid` (vCard UID), `contact_url` (CardDAV href or any other), `note` |
| `handle_name` | every name a service has shown for a handle: `address_id`, `service_id`, `kind` (`book`: the service's copy of the user's address book; `chat`: a chat's name; `profile`: chosen by them), `name`, `first_seen`, `last_seen`, `current` (the latest of that handle, service and kind); many handles may share a name. WhatsApp gives all three kinds, Telegram profile names; `core/names.py` picks a person's name from them |
| `merge_dismissed` | pairs of people the user said are not one (`a` < `b`, `at`): that suggestion is not shown again |
| `person_address` | `address_id` PK, `person_id`, `how` (`auto`: one person per new address; `number`: a service id whose number is known; `manual`: merged by the user) |
| `account` | the user's own handles: `address_id`, `service_id` (NULL: every service), `label`; seeded from `[owner] numbers` |
| `device` | `name` (iphone, an Android device's name, whatsapp-bridge...), `kind`, `used_from`, `used_until` (Unix ms) |
| `source` | `name` (`<device>/sms`...), `path`, `imported_at`, `device_id`, `media_root` (the folder `attachment.source_path` is relative to; `{cache}` and `{data}` stand for those folders), `instance_id` (the plugin instance that reads it) |
| `plugin_instance` | `plugin` (its id), `kind` (source, library, contacts), `label`, `settings` and `state` (JSON), `enabled`, `device_id`, `is_default` (the library kept files go to), `created_at`, `last_run`, `last_status` |
| `contact`, `contact_address` | an address book's contacts (`instance_id`, `uid`, `url`, `name`, `organization`, `photo` in `<cache>/avatars/`) and the addresses they list, joined only to addresses the archive has |
| `state_report` | what a source says about a conversation: `conversation_id`, `instance_id`, `field` (`hidden`, `muted`, `pinned`, `read_until`), `value` (muted: until, Unix ms, -1 for ever), `observed_at` (when the source's data was so), `changed_at` (when it became so) |
| `chat_state` | what the user chose in the app per chat (`p<person>`, `c<conversation>`): `field`, `value`, `set_at`, `always`; `core/queries.py` combines it with the reports (see `docs/design.md`) |
| `setting` | the user's settings shared by every device (JSON values), e.g. `unread_since`, `push_preview` |

A number is one `address` whatever the service, so its SMS, calls, Viber and WhatsApp meet in one
person; the migration made one person per address, which is how the archive behaved before.

**History:**

| Table | Contents |
|---|---|
| `conversation` | `service_id`, `key`, `title`, `is_group`; (service, key) unique |
| `conversation_member` | conversation × address |
| `message` | `status` (a message sent from the app: sending, sent, failed), `id`, `service_id`, `conversation_id`, `ts` (Unix ms UTC), `outgoing`, `sender_id` (NULL when outgoing), `kind_id`, `text`, `key`, `key_scope` (the conversation, for services whose keys are per chat), `fingerprint` (messages without a key: time, direction, kind and text); unique on (service, key, key_scope); and the extras: `subtype`, `subtype_code`, `reply_to`, `reply_key`, `reply_text`, `edited`, `deleted`, `forwarded`, `starred`, `lat`, `lon`, `place` (a location shared), `sender_lat`, `sender_lon` (where the sender was, older Viber) |
| `reaction` | `message_id`, `emoji` (NULL where only a code is known), `code`, `count`, `address_id`, `outgoing` |
| `message_origin` | (`source_id`, `row_key`) PK, `message_id` (WITHOUT ROWID) |
| `call` | `id`, `service_id`, `address_id` (NULL for hidden numbers), `ts`, `outgoing`, `answered`, `duration`, `key`, `detail`, `detail_code`, `video`, `attempts`, `conversation_id` |
| `call_member` | `call_id`, `address_id`, `outcome`, `outcome_code` (group calls) |
| `call_origin` | as `message_origin` |
| `viber_member` | Viber member id → number, as the sources said |
| `blocked` | `address_id`, `phone` (the device), `original`: numbers blocked on a phone |
| `message_fts` | contentless FTS5 (`contentless_delete=1`) over the text folded by `chronika/text.py` (lower case, combining marks removed in every script, final sigma as sigma, NFKC), rowid = `message.id`; written by `Archive.add_message()` (a trigger cannot fold); a query is folded the same way (`text.query()`) |

**Media:**

| Table | Contents |
|---|---|
| `media` | `sha256` PK, `size`, `mime`, `path` (`media/<ab>/<sha256><ext>`, relative to the media root) |
| `attachment` | `message_id`, `sha256`, `source_id`, `source_path`; (source, path, message) unique |
| `library_link` | (`sha256`, `library`) PK, `asset_id` (for a folder: the path in it), `method` (checksum, phash, clip, upload), `score`, `linked_at`, `instance_id` (the library plugin instance): a file in a photo library; more than one library may hold it |
| `media_same` | `sha256` PK, `same_as`, `method`, `score`, `linked_at`: a file removed as the same picture as one the archive keeps |
| `media_decision` | `sha256` PK, `decision` (keep, remove, library), `date_ms` (a date the user gave), `at`: the user's sorting in the app; the newest decision is the one that counts. |

**Connection settings:** WAL journal, `foreign_keys = ON`, `umask 077`.

**Extras.** `chronika/extras.py` turns each source row into the extra columns and reactions
(`add_message(..., extras=)`); `Archive.resolve()` then links replies by `reply_key` (within the
conversation where keys are per chat), applies edit events and iMessage tapbacks. A position on a
message that is not a location is the sender's (Viber only, `sender_lat`). iMessage's
`reply_to_guid` is not a reply (it points to the previous message) and is not used.

**No raw rows.** The source rows are not kept in the archive: what matters from each is in the
columns above, and the extracted sources are the way back to a source row. (Early archives held
each row as JSON in `*_origin.raw`; a one-time migration moved what mattered into columns and
dropped it.)

**Both origins of a pair.** A record found on both phones is kept once, but both source rows are in
`message_origin` / `call_origin`, pointing to the same row (`Archive.record_pairs()`; for Viber, the
iPhone's copies of messages kept from the Android phone). Later imports skip the iPhone's copy by
its own row key, without reading the Android export again; that is what makes it possible to remove
an Android export once it is imported.
Which copy is kept: `Archive.keeper()`, the device in use at the time by its period in `device`,
else the one in use most recently, else the first named (the iPhone).

**Location.** `DB` = `<data>/archive.db`; `MEDIA_ROOT` = `[media] store`, `<data>` by default (the media, under `media/`);
`IPHONE_DATA` = `<cache>/iphone`. Scripts that read the archive use the same path (`ARCHIVE_DB`).

### 9.2 Address normalisation (`address()`)

| Input | Result |
|---|---|
| contains `@` | `email`, lower-cased |
| a URI (`sip:...`; `tel:` is read as a number) | `uri`, lower-cased |
| not digits after removing spaces, `-`, `(`, `)`, `.` | `sender` (sender names such as bank ids) |
| `00…` | `+…` |
| a valid number as written, a national one read in `[owner] region` (`phonenumbers`) | E.164, `+…` |
| a valid number once a `+` is put before it (`306912345678`, `4915112345678`) | E.164 |
| otherwise, fewer than 10 digits | the bare digits. Short codes: the iPhone adds a `+`, Android does not |
| otherwise | `+digits` |

Without `[owner] region` a national number cannot be read: it is kept as `+digits`.
`scripts/archive-phonenumbers.py DB` is a dry run of the rule against an existing archive: it lists
the addresses that would be renamed or merged in `<cache>/phonenumbers-dry-run.tsv`, and compares
the numbers as the iPhone's databases and `viber_member` write them.

**A spurious country code on an iPhone.** An SMS history inherited from older phones can have
short codes and foreign numbers with the home country code in front (e.g. `+3015551234567` for
`+15551234567`). No general rule can strip it: the code followed by a national-length number is a
valid number, and some short codes really start with those digits; `phonenumbers` does not either
(such a number is not valid, and is kept as it is). Where an Android copy of the same SMS has the
address without the prefix, the pair shows the right value; a one-time step used that once, and
the import does not need it.

Contact names are not taken from any phone. They are meant to come from the user's address book
(CardDAV), matched by number.

### 9.3 Running

```
uv run python -m chronika [--db PATH] [sms calls viber whatsapp telegram voip media]
```

- **Registry.** `IMPORTERS` in `chronika/__main__.py`, in the order they run. Without names, the
  ones `[import] importers` lists, else all. Every source is optional: an importer whose sources are
  missing says so and adds nothing. The carrier notices are read only by the parsers
  `[import] carrier_notices` enables (`chronika/carriers/`, one module per carrier or country).

- **Order matters.** The `media` importer resolves messages through `message.key` and
  `message_origin`, so the message importers must have run first; `voip` reads the carrier notices
  from the archive's SMS and the WhatsApp call bubbles, so it comes after `sms` and `whatsapp`; and
  after `calls`, which does not look at the calls `voip` added (they would come in twice).
- **Commits.** Each importer commits at its end; `media` commits after each step.
- **Interruptions.** An interrupted importer loses only its uncommitted batch, and a re-run redoes it.

### 9.4 Deduplication keys, summarised

| Service | Within a source | Across sources |
|---|---|---|
| SMS/MMS | `guid` / `_id` + exact-repeat collapse | direction + text + ≤ 2 s, one to one; the device in use decides |
| iMessage, RCS | `guid` | (iPhone only) |
| Calls | `ZUNIQUE_ID` / `_id` | address + direction + ≤ 2 s; the device in use decides |
| App calls, notices | call id / `Z_PK` / `<message>/<i>` | another source within 60 s |
| Viber | `Z_PK` / `EventID` | token (`message.key`); the device in use decides |
| WhatsApp | `Z_PK` / `<jid>/<id>` | stanza id (`message.key`); iPhone first |
| Media | `(source, source_path, message)` | content sha256 (`media`) |
| Services with ids per chat (Telegram) | `(source, row_key)` | (`message.key`, `key_scope`) |
| Sources without ids (Messenger's export) | `(source, row_key)` | `message.fingerprint` within the conversation |

---

## 10. Media: from the archive to the photo library

### 10.1 Into the archive (`chronika/media.py`)

**Storage.** Each file is stored once by content: `<cache>/media/<sha256[:2]>/<sha256><ext>`.

- It is a **hard link** (`os.link`) to the source file: same file system, no extra space. Across
  file systems it is a copy (`link_or_copy`).
- The `mime` is guessed from the extension.

**Links per source:**

| Step | Source file | Message found by |
|---|---|---|
| `whatsapp` | `whatsapp-media/<ZMEDIALOCALPATH minus "Media/">` | stanza id → `message.key` |
| `viber_iphone` | `viber-media/{Attachments,FileMessages,VoiceMessages}/<ZATTACHMENT.ZNAME>` | token → `message.key`, else `Z_PK` → `message_origin` |
| `mms_android` | `<export folder>/mms-parts/<part _id>` (empty ones skipped) | MMS `_id` → `message_origin`; if the iPhone's copy was kept instead, the single SMS/MMS message with the same direction within ±2 s |

Viber media pulled from an Android phone have no step at present: one existed for a `links.tsv`
of 5.5 (`certain` rows only: desktop `EventID` → `message_origin`, else its token → `message.key`)
and was removed with the files it served.

**Incremental.**

- An existing `(source, path, message)` link is skipped.
- A file already linked from the same source path reuses its recorded sha256 and is not hashed
  again.
- Files missing on disk, for example pruned ones, are counted as "χωρίς αρχείο" and skipped. This is
  harmless.

### 10.2 Pictures do not stay

The archive keeps the record, but not the file.

- Each `media` / `attachment` row stays.
- The file goes to immich, and `library_link` records the asset, or it is removed (the user's
  choice).
- Documents and PDFs are a second phase. Video calls are never kept.

### 10.3 Reading immich without touching it: `scripts/immich-index.py`

**Through the API (the default).** `[immich] url` and the key `immich-key`; only reads.

- **Assets.** `POST /search/metadata` (permission asset.read), 1,000 a page, with EXIF, for each
  visibility (timeline, archive, hidden); the trash is left out. The checksum comes as base64 and
  is stored as hex; times are written as the dump writes them (`2020-01-01 12:00:00.000+00`), so
  the readers are the same. An index made this way agrees in every field with one made from the
  dump, for the assets in both.
- **Previews.** `GET /assets/{id}/thumbnail?size=thumbnail` (asset.view; about 250 px, WebP) into
  `<cache>/immich-thumbs/<id>.webp`, only those not there yet. Without asset.view the
  index has no previews (a message says so), and the hash and look-alike checks have nothing to
  compare with.
- **Embeddings.** The API does not give immich's. `immich-match.py` makes them from the previews
  with the same model, and the index keeps them (and `phash`) from run to run for unchanged assets.

**From the nightly dump (`--dump [FILE]`).**

- **No API key, no API calls.** immich writes a nightly Postgres dump to
  `<data_folder>/backups/immich-db-backup-*.sql.gz` (`<data_folder>` is `[immich] data_folder` in
  `config.toml`). The newest one (or one given as an argument) is parsed directly.
  - Only the `COPY public.<table> (...) FROM stdin;` blocks of `asset`, `asset_exif`, `asset_file`,
    `smart_search` and `system_metadata` are read.
  - Fields are tab-separated. `\N` means NULL, and `\t \n \r \\` are unescaped.
- **Output.** `<cache>/immich.db`, rebuilt from scratch each time. Table `asset`: id,
  type, original name, SHA-1, `fileCreatedAt`, `dateTimeOriginal`, make, model, EXIF width and
  height, size, preview path, CLIP embedding, and later `phash`.
- **Trash.** Assets with `deletedAt` are left out.
- **Field conversions.**
  - The checksum is `bytea` (`\x<hex>`) and is stored as hex.
  - The preview path `/usr/src/app/upload/...` (inside the container; `[immich] container_prefix`)
    is rewritten to `<data_folder>/...`.
  - The embedding is the pgvector text `[f, f, ...]`, stored as float32 bytes. The model comes from
    `system-config` → `machineLearning.clip.modelName`, e.g. `ViT-B-16-SigLIP2__webli`, 768
    dimensions.
- **Freshness.** The index is as fresh as the last nightly dump.

### 10.4 Matching chat media against immich

**`immich-match.py`** runs with `uv run --extra ml --extra torch` (`--extra rocm` instead on an AMD
GPU). The device is a CUDA/ROCm GPU, else Apple's MPS, else the CPU.

- **Candidates:** archive `media` rows that are images or videos (or `.heic`), with the earliest
  message time.
- **Embeddings for immich.** Assets the index has none for (read through the API) get one first,
  from their preview, stored in the index.
- **Exact match.** The file's SHA-1 against immich's checksum.
- **Similarity.**
  - The embedding comes from `google/siglip2-base-patch16-224` (HuggingFace cache), the local
    equivalent of immich's model. The same picture scores 0.90–0.99 against immich's own embedding;
    different pictures score about 0.5.
  - It is compared by cosine with every immich embedding, and the best asset is kept.
  - Pictures are read with Pillow (pillow-heif for HEIC), turned as their EXIF says and shrunk to
    1440 px; videos are judged by an ffmpeg frame 1 s in (or 0 s).
- **Capture date.** exiftool `DateTimeOriginal` + `OffsetTimeOriginal`, in batches of 500.
  - With the offset, the time is taken in its own zone; without it, the configured one.
  - Years before 1990 are ignored.
  - Ignoring the offset puts photos taken abroad hours off.
- **Output.** `<cache>/match.db`, table `match`: path, origin, sha1, exact, best,
  similarity, taken, message, immich_date. Rebuilt from scratch each time.

**`immich-phash.py`** (`--extra media`) handles a CLIP weakness: CLIP squashes a tall
portrait into a square, so a copy can score only 0.89. For each candidate's best asset it adds:

- `phash`, the pHash Hamming distance between the picture as shown and the immich preview;
- `same_shot`, taken within ±2 s with the same pixel size in either orientation.

**`immich-dupes.py`** compares all against all, like Czkawka.

1. pHash every immich preview. Kept in `immich.db` `asset.phash` as a signed 64-bit int, so later
   runs hash only new assets.
2. pHash every chat picture, as shown, through Pillow at 512 px (JPEG decoded with `draft`; HEIC
   through pillow-heif). This uses all cores but two.
3. Find the nearest immich asset by Hamming distance, with vectorised XOR and popcount in blocks.
   Stored as `hash_asset`, `hash_dist`.
4. Group chat pictures within distance 6 of each other with union-find (`hash_group`).
5. **Confirm** every pair within 6:
   - the same aspect ratio as shown, within 6% (`hash_aspect`);
   - a second hash, dHash, within 10 (`hash_dhash`).
   - Reason: a pHash of 6 can be chance (a news clipping against a painting, a memoji against a
     person).
   - A pair counts as the same picture when `hash_dist ≤ 6`, `hash_aspect = 1` and `hash_dhash ≤ 10`.

### 10.5 Judging the rest with a local vision model: `scripts/media-vlm.py`

Answers go to `<data>/vlm.db`. It holds hours of work, so it lives in the data folder rather than
in the cache.

**Cheap filters first,** without a model, stored in `vlm.db` `filtered`:

- already in immich: an exact match, or CLIP ≥ 0.90 at the same time (±120 s from the capture date;
  or, without one, the message date between 1 h before and 30 days after immich's date);
- WhatsApp GIFs and round video notes (`message.subtype` gif, video note);
- `.gif` or `/GIF-` names; `.webp` stickers;
- a longest side under 480 px;
- files that are gone.

**The model.**

- Each remaining file becomes a JPEG of at most 896 px (videos: a frame at 1 s).
- It is sent to the local Ollama (`[ollama] url`, default `http://localhost:11434`, `/api/chat`)
  with `[ollama] model` (default `qwen2.5vl:7b`): temperature 0, `num_predict 300`,
  `keep_alive 10m`.
- The answer is forced to a JSON schema: `kind` (nine values), `value` 1–5 and `description` (Greek).

**Robustness.**

- Every answer is committed at once. The run resumes from what is not yet in `answer`, so Ctrl-C, a
  crash or sleep loses at most one picture.
- If Ollama is unreachable, it waits 15 s and retries, forever.
- An HTTP error for one picture is tried 3 times. Invalid JSON, a strip under 32 px or an
  unconvertible file is set aside in `skipped` with the reason.
- `--retry-skipped` retries the set-aside pictures; `--report` compares the answers with the
  reference.
- Keep the machine awake with `systemd-inhibit --what=sleep` (the screen can still turn off).

**Model choice.**

- Local models are measured on a pilot set of pictures against a reference model's answers
  (`[review] reference_model`, stored in `vlm.db` like any model's); `--report` gives the
  agreement. Sending pictures to an outside reference model needs the user's explicit consent.
- The default, `qwen2.5vl:7b`, was chosen this way over Gemma 3 12B and Llama 3.2 Vision.
- Next: larger local models as a second opinion.

### 10.6 Review pages and pruning

| Tool | Port | Shows | Writes |
|---|---|---|---|
| `immich-review.py` | 8519 | each candidate next to its immich match: similarity, pHash, dates, sizes; `--origin archive/kept/filtered`, `--only hash/false` | `<data>/review.db` `decision`, `date_from` (until acted on); a date or an approval turns a `triage` "delete" of the file into "keep" |
| `vlm-review.py` | 8518 | the models' answers per picture | `vlm.db` `verdict` (answers marked wrong), `review.db` `triage`; Apply: first the exact list (`media-triage.py --dry-run --plan`), then, on a second click, that list only (`--only`) |
| `media-triage.py [--table aside_decision] [--dry-run --plan FILE] [--only FILE]` | n/a | n/a | carries out the newest decision per file across `triage`, `aside_decision`, `aside`, `restored`, `decision` and `date_from`; a delete older than (or as old as) any other decision is not carried out and is listed |
| `media-prune.py LIST [--dry-run] [--done FILE]` | n/a | n/a | `library_link`, then removes every local copy |
| `media-aside.py LABEL CONV_ID...` | n/a | n/a | hard links (copies where the folder is on another file system) in `<aside>/<LABEL>/<kind>/` (`[media] aside`, default `<data>/aside`), and `aside` in `review.db`; the pages leave these files out, and only `media-triage.py --table aside_decision` deletes them, passing their aside links to `media-prune.py` as known copies |

Retired, with the Android Viber media they served: `media-review.py` (port
8517) showed the Android Viber files no `attachment` referred to (by `links.tsv` category, CLIP label,
duplicates, faces, date, shape) and moved those chosen to `viber-media/_deleted/` after checking
again that they were unreferenced and not hard-linked; `media-classify.py` gave it CLIP ViT-L/14
labels, an optimal-leaf order, duplicates (≥ 0.95) and an insightface face count
(`viber-media/_review/ai.json`).

All pages:

- listen on 127.0.0.1 only, and answer only requests whose Host (and Origin, if any) is
  127.0.0.1 or localhost on their port, so another web site cannot reach them by DNS rebinding;
- print their address with a key of the run (`/?k=KEY`); opening it sets a cookie
  (`chronika-<port>`, HttpOnly, SameSite=Strict) that every request needs, GETs included, so
  another local user cannot read or drive them;
- accept a change (POST) only with the run's random token, which the page itself sends with every
  request (`X-Token`) and another site cannot know;
- escape every text they show and send a Content-Security-Policy with a nonce per page, so a chat
  or person name, or a model's answer, cannot run as script;
- accept only paths under their own roots (`safe()`);
- keep thumbnails in a private `/tmp` folder that is removed on exit.

**`media-prune.py`, step by step.** LIST lines are `archive path \t asset id \t method \t score`,
optionally followed by the file's links in the aside folder (known copies; refused outside it).

1. Check that the path is the archive's own path for that sha256.
2. Collect the copies: the archive's hard link, plus every `attachment.source_path` under the
   source's root (`<cache>/iphone/whatsapp-media`, `…/viber-media`).
3. Group the copies by inode.
   - The first path of each inode must hash to that sha256. Separate files with the same content
     happen: the same picture received twice.
   - Each inode's link count must equal the number of copies found. Otherwise there is an unknown
     copy somewhere, and the file is skipped.
   - A file another one is linked to as the copy kept (`media_same.same_as`) is refused.
4. If the asset is not `-`, write `library_link` and **commit before removing anything**.
5. Remove the copies.

The encrypted backup is never touched, and the next `iphone-sync.py` does not bring the files back
(3.7).

### 10.7 Where the archive and its media live

- The database is `<data>/archive.db`. The data folder should be covered by the user's backups
  (best from a snapshot, so the copy is consistent); backing it up is not Chronika's job.
- The archive's media wait in `<cache>/media/` (`MEDIA_ROOT`) as hard links to the files
  `iphone-sync.py` copied into `<cache>/iphone/`; both must be on the same file system for the links
  to work (otherwise they are copies). The cache is meant to be left out of backups: everything
  there can be made again from the encrypted iPhone backup (`media-restore.py` for media already
  taken by the archive).

---

## 11. Refresh runbook

With the iPhone on the cable:

```
# 1. back up and extract (password from the keyring; the phone may ask for its passcode)
uv run --extra iphone python scripts/iphone-sync.py
# 2. bring everything new into the archive (bridge rows come in with `whatsapp`)
uv run python -m chronika sms calls viber whatsapp telegram voip media
```

Optional, for the photo-library work:

```
uv run python scripts/immich-index.py                               # through the API (--dump: the nightly dump)
uv run --extra ml --extra torch python scripts/immich-match.py      # rebuilds match.db (--extra rocm on AMD)
uv run --extra media python scripts/immich-dupes.py
systemd-inhibit --what=sleep uv run --extra media python scripts/media-vlm.py   # resumable
uv run --extra media python scripts/immich-review.py --origin archive          # user approves
uv run python scripts/media-prune.py LIST --dry-run                 # then without --dry-run
```

**Occasionally:**

- `scripts/iphone-verify.py` checks the whole backup (expect databases that changed during the
  backup, and browser "Web Data" files, to be reported; see 3.8).
- `scripts/iphone-ls.py` shows what else the backup holds.

**One-off sources (a retired Android phone), final once done:**

- `android-export.py`;
- `adb pull` of the Viber media;
- the Viber Desktop export;
- the linking of the Viber media (`viber-link-media.py`, retired: 5.5).

---

## 12. Incremental behaviour, summarised

| Step | First run | Later runs | Unit of change |
|---|---|---|---|
| `idevicebackup2 backup` | full (or `--full`) | phone sends changed files, deletes removed ones | file |
| database extraction | decrypt 6 DBs | decrypt them again in full, replace atomically (`--only` for some) | whole file |
| media extraction | all files | only names not on disk and not in the archive's `attachment` | file |
| `android-export.py` | all tables, all parts | skips existing tables, existing part files | table / part |
| Viber Desktop export | full copy | a new full copy into a new file | whole DB |
| `chronika` importers | everything | rows whose `(source, row_key)` is new and whose `key` is not yet present | row |
| `chronika voip` | everything | calls whose source row is new and that no other source has (same service, person, ±60 s) | call |
| `chronika media` | hash and link all | new `(source, path, message)` only; known paths not rehashed | link |
| `immich-index.py` | build | rebuild from the newest dump | whole DB |
| `immich-match.py` | build | rebuild | whole DB |
| `immich-dupes.py` | hash all | immich previews: only new; chat pictures: all again | asset |
| `media-vlm.py` | all | only files without an answer for the model | picture |

---

## 13. Known gaps, pitfalls and inconsistencies

Found while writing this, from the code:

1. **The desktop export is named once,** `[viber] desktop_export` in `config.toml`.
2. **SMS and call pairing is greedy and recomputed on every run.** As long as the Android data are
   fixed and new iPhone rows are after the Android phone's period of use, earlier choices stay
   stable. Changing `PAIR_MS`, `collapse()` or the devices' periods, then re-running on an existing
   archive, could add the other side of pairs already imported. Rebuild the archive instead.
3. **Source rows live only in the extracts.** No source row is kept in the archive (9.1), neither
   the chosen one nor the copy not chosen. An iPhone row is read again from what each sync
   extracts; a removed Android export's rows survive only as what the archive took from them (both
   origins of each pair).
4. **The acquired data need a copy of their own.** The encrypted iPhone backup is the only copy of
   the phone's call history, and a Viber Desktop export may be the only readable copy of an Android
   phone's Viber history. Keeping them safe is the user's job, with whatever backup they use.
5. **Not extracted yet from the iPhone backup:**
   - SMS/iMessage attachments (`MediaDomain Library/SMS/Attachments`);
   - `CallHistoryTemp.storedata`.
   Messenger keeps its messages out of the backup; Teams is on the server; Discord leaves only
   avatars.
6. **Bridge media are not in the archive.** Viber calls older than the iPhone's recents are lost
   (they are not in the desktop database).
7. **`immich.db` lags.** It reflects immich as of its last nightly dump. Something uploaded today is
   invisible to the matching until tomorrow.
8. **Short codes and alphanumeric senders** are stored without a country code by design. Two
   phones writing the same short code differently (`+1234` / `1234`) are normalised to the bare
   digits.
9. **A new iPhone** means a new UDID in `config.toml` (or none, to take the only backup or phone).
   A new backup is also needed (a new keybag, though the same password can be set). Paths and
   settings are in `chronika/config.py` (README, "Folders and configuration").

---

## 14. Files, permissions, caches

| Path | Contents | Mode | In backups? |
|---|---|---|---|
| `<backup_root>/<UDID>/` | encrypted iPhone backup | as written by idevicebackup2 | the user's to back up: the only copy of the call history |
| `<cache>/iphone/` | decrypted DBs and new media (`iphone-sync.py`) | 700/600 | no: made again from the backup |
| `<cache>/media/` | the archive's media until they go to immich | 700/600 | no: from the backup (`media-restore.py`) |
| `<data>/archive.db` | the archive | 600 | yes, with the data folder |
| `<config>/config.toml` | settings (README, "Folders and configuration") | 600 | yes |
| keyring, service `chronika` | `backup-password`, `immich-key` | the keyring's | the keyring's |
| `<config>/backup-password`, `immich-key` | the same secrets where there is no keyring (or until moved with `--move-to-keyring`) | 600, folder 700 | opened only by the scripts |
| `<cache>/immich.db` | immich index | 600 | rebuildable |
| `<cache>/immich-thumbs/` | immich's small previews, through the API | 700/600 | rebuildable |
| `<cache>/match.db` | match results | 600 | rebuildable |
| `<data>/vlm.db` | model answers, `filtered`, `skipped`, `verdict` | 600 | yes (answers take hours to remake) |
| `<data>/review.db` | the user's decisions: `triage`, `decision`, `date_from`, `aside`, `aside_decision`, `aside_date`, `restored` | 600 | yes (not rebuildable) |
| `<cache>/` `faces.db`, `neighbours.db`, `similar.tsv`/`.npz` | faces, tags, immich neighbours, look-alike groups | 600 | rebuildable (but `faces.db` holds names the user gave to face groups) |
| the models' own caches (Ollama, HuggingFace, insightface) | models | n/a | rebuildable by downloading again |
| `/tmp/vb/export.log` | Viber export log | 600 | scratch |

The rule is:

- `<cache>` holds only what can be rebuilt (the extracts, the immich index and the match results).
  Backups are expected to skip it.
- `<data>` holds what cannot be rebuilt, or would cost hours to remake: the archive, the model
  answers and the user's decisions. The user's backups should cover it.
