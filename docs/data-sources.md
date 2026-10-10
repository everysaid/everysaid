# How the data is obtained: a technical reference

This document describes, source by source, how Everysaid gets at each piece of personal data: the
first acquisition, how it is refreshed, what is incremental and what is not, and how each obstacle
that keeps a user from their own data is got round (encrypted backups, providers without an
export, text hidden in binary blobs). It describes the code as it
stands.

`README.md` is the overview (it also covers Telegram, Signal and the Adium and Pidgin logs, which
are not repeated here); `docs/go.md` tells how to build, run and develop. This file is the "how".
Notes about one installation (its devices, paths, history and numbers) belong in a local, untracked
`LOCAL.md`, not here.

Placeholders: `<data>`, `<cache>` and `<config>` are Everysaid's folders (README, "Folders and
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
15. [Signal: the client's commands and events](#15-signal-the-clients-commands-and-events)

---

## 1. Overview and data flow

```
 iPhone ──USB/usbmuxd──► idevicebackup2 ──► <backup_root>/<UDID>/   (encrypted, Finder format)
                                                          │  everysaid iphone-sync + password
                                                          ▼
                                  <cache>/iphone/  sms.db, CallHistory.storedata,
                                                   viber.sqlite, whatsapp.sqlite,
                                                   whatsapp-contacts.sqlite,
                                                   whatsapp-calls.sqlite,
                                                   viber-media/, whatsapp-media/, sms-media/
 Android phone ──adb── content query/read ─────► <export>/<device>/android.db (+ *.txt.gz, mms-parts/)
               ──adb pull Android/data/com.viber.voip/files ──► a folder of Viber media
 WhatsApp source (internal/whatsapp, live) ──────► the archive directly (state: <data>/keys.db, whatsapp_*)
 Telegram API ── everysaid telegram-sync ────────► <data>/keys.db (telegram_*), <cache>/telegram/media/
 Messenger, Instagram (internal/meta, live) ─────► the archive directly (sessions: <data>/keys.db, meta_*)
 Meta's export (Download your information) ──────► the archive (everysaid import messenger instagram)

                     everysaid import sms calls viber whatsapp telegram voip media   (or the sources in the app)
                                                          ▼
                     <data>/archive.db  +  <data>/media/<ab>/<sha256><ext>   ([media] store)
                                                          │
                     the user's decisions (the app; separate picture tools for the backlog)
                                                          ▼
                     the photo library: library_link, local copies removed
```

Three layers, each rebuildable from the one before it:

| Layer | Where | Written by | Rebuildable from |
|---|---|---|---|
| Raw acquisition | `<backup_root>/`, `<export>/<device>/`, the live sources' stores | `idevicebackup2`, `everysaid android-export`, `adb pull`, the WhatsApp source | the devices only (an encrypted iPhone backup is the only copy of the phone's call history) |
| Decrypted extracts | `<cache>/iphone/` | `everysaid iphone-sync` | the encrypted backup |
| Unified archive | `<data>/archive.db`, `<data>/media/` | `everysaid import`, the sources | the extracts; but see section 13: once media are pruned, the archive is the only record |

An Android export may be removed once everything in it is in the archive (9.1, "Both origins of a
pair"); the importers run without it.

---

## 2. Principles shared by every step

- **Read only towards devices.** iPhone: only `idevicebackup2 backup` (the phone sends, nothing is
  written to it). Android: only `adb exec-out content query` / `content read` and `adb pull`. No
  restore, no `adb push`, no provider writes, without the user's explicit agreement.
- **The encrypted backup is precious.** `<backup_root>/` is modified only by
  `idevicebackup2 backup`. Every other tool opens it read only. Pruning media never touches it.
- **Private on disk.** Everything that writes decrypted data does so as folders 700 and files 600
  (`iphone-sync`, `android-export`, the archive, the demo); the binary runs with umask 077.
- **Atomic replacement.** Files are written as `NAME.part` and then renamed over the old
  one, so an interrupted run never leaves a half-written database in place. This covers the
  extracts, the media files, the password file and the MMS parts.
- **Idempotent imports.** Every importer can be run again. A row is identified by `(source,
  row_key)`, its id in that source, and is skipped if already present (the Adium and Pidgin logs,
  without ids, by their time, direction, kind and text). A message's service-wide id
  (`message.key`) stops the same message arriving twice from two sources.
- **One origin per record.** A record found on two devices is stored once, from the device that was
  in use at the time (`device.used_from`/`used_until` in the archive, `Archive.Keeper`): an
  Android phone during its period of use, the iPhone otherwise (the newest device carries the
  history copied from phone to phone). `message_origin` / `call_origin` record which source row
  became which record; the source row itself is not kept (9.1).
- **No channels.** Viber channels and WhatsApp channels/status are never imported (5.3, 6.4).
- **Local processing only for private media.** Classification, similarity and hashing run on this
  machine. Sending anything to an outside model needs the user's consent each time.
- **The photo library gets nothing without approval.** Nothing goes to immich unless the user
  approves each item. A picture there carries its real capture date.
- **Secrets.** The backup password and the immich key live in the system's keyring (service
  `everysaid`), or, where there is none, in `<config>/<name>` (600); see README,
  "Secrets". Only the code reads them (`config.Secret`). They are never an argument and never
  printed.

---

## 3. iPhone: the encrypted backup

### 3.1 Transport and pairing

- `libimobiledevice`: `idevicepair`, `ideviceinfo`, `idevicebackup2`, `idevice_id`.
- `usbmuxd` is started by udev when the phone is plugged in. Everysaid uses the cable; Wi-Fi sync
  and iCloud backup play no part.
- Pairing creates a lockdown pairing record (host certificate and keys) on both sides; the phone
  shows "Trust this computer". To check it:
  - `idevicepair validate`
  - `ideviceinfo -q com.apple.mobile.backup` (must show `WillEncrypt: true`)
- Device: its UDID, set in `config.toml` (`[iphone] udid`) or with `--udid`; without it the
  commands take the only backup in `[iphone] backup_root` (or `--backup-root`), else the only
  phone on the cable (`idevice_id -l`).

### 3.2 Why encrypted, and what that protects

- iOS chooses the backup's content by whether it is encrypted. Only an encrypted backup contains
  **call history** (`CallHistory.storedata`), the keychain, Health and Safari history.
- So encryption must stay on. Turning it off would silently drop the calls from every later backup.
  A backup that is not encrypted is refused, with a message to turn encryption on
  (`idevicebackup2 encryption on`) and back up again.
- The password is a setting of the phone, not of a single backup. It cannot be removed or changed
  without the old one.
- If the password is unknown, the only way out is
  `Settings > General > Transfer or Reset iPhone > Reset > Reset All Settings`.
  - This clears the backup password without deleting data.
  - It resets Wi-Fi networks, the home screen layout, permissions and notification settings.
  - A new password is then set from Finder or iTunes ("Encrypt local backup") or with
    `idevicebackup2 encryption on`.

### 3.3 Making the backup (first and incremental)

`everysaid iphone-sync` runs `idevicebackup2 -u <UDID> backup [--full] <backup_root>`.

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
- **`--full`** forces a complete backup; **`--no-backup`** only decrypts the backup already there.
- **Passcode.** The phone may ask for its own passcode to start.
- **Failure.** If `idevicebackup2` returns non-zero, the command stops before touching the
  extracts, and says why where it can tell (the phone locked, no phone found).
- The backup is complete only once `Manifest.plist` exists. `--no-backup` and `--save-password`
  refuse to run without it.

### 3.4 Decrypting: how the encryption is opened

Decryption is done by `internal/iphone`, in Go, with no outside tool. This is the standard iOS
backup scheme, in outline:

1. **Unlocking the keybag.** The password unlocks the `BackupKeyBag` in `Manifest.plist`.
   - The key is derived with PBKDF2-SHA256, using the keybag's `DPSL` salt and `DPIC` iterations,
     then PBKDF2-SHA1 with `SALT` and `ITER`. Iteration counts far above what iOS uses are refused
     as unsafe.
   - This is deliberately slow: a few seconds per attempt. That is why a wrong password takes a
     while to be rejected.
   - The derived key unwraps the class keys (AES key wrap, RFC 3394).
2. **Opening `Manifest.db`.** The `ManifestKey` (prefixed with its protection class) is unwrapped
   with that class key, and `Manifest.db` is decrypted with it (AES-256-CBC) into a temporary
   folder, removed when the backup is closed.
3. **Opening each file.** Every file's `EncryptionKey`, found in its `file` plist, is unwrapped with
   the key of its protection class. The content is then decrypted with AES-256-CBC and the padding
   removed.

The password is checked first, by opening the keybag and `Manifest.db`: a wrong one is reported as
such, any other failure separately. Then each file is found in `Manifest.db` by domain and path and
decrypted on its own.

### 3.5 The password

- **Saving it once.** `everysaid iphone-sync --save-password` asks for it (hidden input, any number
  of tries), checks the password against the existing backup, and stores it: in the keyring, else
  in a file.
  - The folder `<config>` is created as 700.
  - The file is written with mode 600 to `.part` and then renamed into place.
  - `--move-to-keyring` moves an existing file into the keyring.
- **Every later run.**
  - A password file readable by group or others is refused ("chmod 600 and again"; not checked on
    Windows, where the mode does not say).
  - A wrong stored password stops the run with a message to save it again.
  - Without a stored password, it is asked for interactively before anything else, so the rest of
    the run is unattended.
  - `--password-stdin` reads it from the standard input for one run, without storing it.
- `iphone-verify` and `iphone-ls` read it the same way, or ask.

### 3.6 Extracting the databases

**Databases** (`internal/iphone`, `iphone-sync`), into `<cache>/iphone/` (`-o` for another folder):

| Output | Domain | Path in the backup |
|---|---|---|
| `sms.db` | HomeDomain | `Library/SMS/sms.db` |
| `CallHistory.storedata` | HomeDomain | `Library/CallHistoryDB/CallHistory.storedata` |
| `viber.sqlite` | `AppDomainGroup-group.viber.share.container` | `com.viber/database/Contacts.data` |
| `whatsapp.sqlite` | `AppDomainGroup-group.net.whatsapp.WhatsApp.shared` | `ChatStorage.sqlite` |
| `whatsapp-contacts.sqlite` | same | `ContactsV2.sqlite` |
| `whatsapp-calls.sqlite` | same | `CallHistory.sqlite` (WhatsApp's call log) |
| `acrobits.sqlite` | `AppDomain-cz.acrobits.%` | `Library/Application Support/eventHistory.db` (an Acrobits softphone's call log; left out where the phone has none) |

`--only NAME...` decrypts only the named databases and stops before the media (e.g.
`everysaid iphone-sync --no-backup --only whatsapp-calls.sqlite`); a name not in the table is
refused.

**Refresh:**

- Each database is decrypted in full on every run. They are small: a few MB to a few hundred MB
  (WhatsApp's is the largest).
- Each is written to `.part`, chmod 600, and renamed over the old copy.
- Stale `-wal` / `-shm` files next to the old copy are removed. A reader such as sqlite3 may have
  left them, and they belong to the old file, not the new one.

**WAL:** the backup carries no `-wal` files for these databases (check with
`everysaid iphone-ls '%' '%sms.db%'` and likewise for the others). iOS checkpoints them into the main file
before backing up, so the single file is complete.

**Found in the backup but not extracted:**

- `HomeDomain Library/CallHistoryDB/CallHistoryTemp.storedata`;
- `MediaDomain Library/SMS/Attachments/...`, the SMS/iMessage attachments.

### 3.7 Extracting media (incremental)

The media `iphone-sync` copies:

| Output folder | Domain | Prefixes taken | Stripped |
|---|---|---|---|
| `viber-media/` | `AppDomain-com.viber` | `Documents/Attachments/`, `Documents/FileMessages/`, `Documents/VoiceMessages/` | `Documents/` |
| `whatsapp-media/` | `AppDomainGroup-group.net.whatsapp.WhatsApp.shared` | `Message/Media/` | `Message/Media/` |
| `sms-media/` | `MediaDomain` | `Library/SMS/Attachments/` (the files of SMS, MMS and iMessage) | `Library/SMS/Attachments/` |
| `whatsapp-previews/` | `AppDomainGroup-group.net.whatsapp.WhatsApp.shared` | `Message/Media/`: only the `.thumb` of a link's preview (`ZWAMESSAGEDATAITEM.ZTHUMBNAILPATH`), not media | `Message/Media/` |

How a refresh works:

1. List `Files WHERE flags = 1 AND domain = ?` from `Manifest.db` and keep the paths with those
   prefixes.
2. Keep only files that belong to a message, as the databases just extracted name them
   (`ZWAMEDIAITEM.ZMEDIALOCALPATH`, `ZATTACHMENT.ZNAME`): thumbnails, favicons and link previews
   beside them are not copied.
3. Skip a path whose output file already exists. Media files have unique names and never change
   once written, so existence is enough: no hashing, no dates.
4. Skip a path the archive has already taken.
   - It opens `archive.db` **read only** and collects `attachment.source_path` for the sources
     `iphone/whatsapp` and `iphone/viber`.
   - The archive keeps these rows after the file has gone to immich or been removed. So pruned
     media do not come back, while media that arrive late for old messages are still picked up.
5. Otherwise decrypt to `.part`, chmod 600, rename. Folders are created with mode 700 at every
   level. A manifest path that would lead out of the output folder is ignored.

What this means for deletions:

- Files deleted on the phone disappear from the next backup. Copies already in `<cache>/iphone/`
  stay; the archive only ever adds.
- The encrypted backup itself keeps whatever the phone currently has. Pruning acts only on the
  decrypted copies.

### 3.8 Looking without extracting, and verifying

- **`everysaid iphone-ls DOMAIN_LIKE [PATH_LIKE] [--depth N]`** lists without extracting anything.
  - It groups `Files` rows by domain and the first N path components (3 by default), with counts
    and sizes, largest first.
  - The size comes from the `Size` in each row's `file` plist (`$objects[1]`).
- **`everysaid iphone-verify`** checks the whole backup.
  - It decrypts every `flags = 1` file in memory and compares the length
    with the plist's size.
  - Empty files have no key and are counted separately.
  - Mismatches where both sizes are whole 4,096-byte pages are reported as "databases that changed
    during the backup". This happens routinely.
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

- The `shell` user may query the telephony providers. `content query` runs as `shell`, and can
  usually read the call log, SMS and MMS providers (vendors may restrict this). The blocked
  numbers often belong to the default dialer and may be refused.
- It can also read `/sdcard/Android/data/<pkg>` with `adb pull`. That folder is hidden from apps
  since Android 11, but is still accessible to `shell`.
- No root and no backup app are needed.

**If adb hangs:** a stuck adb server is fixed by `adb kill-server` and `adb start-server`. Long
transfers must not be wrapped in a short timeout, which kills a run half way.

### 4.2 `everysaid android-export`

The phone is the only one adb sees, or the one given with `-s SERIAL`. Everything goes to
`<export>/<device>/`, the device named by its maker, model and the last 4 characters of its serial
(e.g. `acme-phone1-1a2b`).

| Table | Provider URI |
|---|---|
| `calls` | `content://call_log/calls` |
| `sms` | `content://sms` |
| `mms` | `content://mms` |
| `mms_part` | `content://mms/part` |
| `blocked` | `content://com.android.blockednumber/blocked` (optional: a refusal leaves it for a later run) |
| `mms_addr` | `content://mms/<id>/addr`, one query per MMS |

**How each table is read:**

1. **Columns.** An unrestricted `adb exec-out content query --uri URI`. The column names are parsed
   from the `Row: 0 ` line (`(?:^Row: 0 |, )([A-Za-z0-9_]+)=`); duplicate or missing names abort.
   A provider with no rows makes no table; a later run looks again.
2. **Rows.** The same query with `--projection col1:col2:...`, so the column order is known and
   fixed.
3. **Parsing.** The text format is `Row: N col=value, col=value, ...` with no escaping.
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
     and converted by the importers (`CAST(_id AS INTEGER)`).
6. **`mms_addr`.** One query per `mms._id` (`content://mms/<id>/addr`); messages without addresses
   are skipped.
7. **MMS parts.** For each `mms_part` row with `_data` not NULL, the binary part is read with
   `adb exec-out content read --uri content://mms/part/<_id>` and written to `mms-parts/<_id>`
   (`.part`, then renamed). A part the phone cannot read is not saved; a later run tries it again.
   - A part may come back empty (a placeholder part, or a file the provider no longer returns).
     Such parts are skipped at import because their size is 0.

### 4.3 Incremental behaviour

- **Granularity is the table.** A table already in `android.db` is skipped whole.
  To refresh one, drop it (or write to a new database) and run again.
- **MMS parts are per file.** A part already in `mms-parts/` is not read again, nor one the
  archive has already taken from that export (`attachment.source_path` of the source
  `<device>/mms`), so parts removed after review do not come back.
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
- **Copied by hand** while Viber is still installed: the app folder goes with the app.
- No importer links them to messages yet.

---

## 5. Viber

### 5.1 The obstacle

- Viber keeps no message content on its servers and offers no export.
- History does not transfer between Android and iOS.
- The Android backup on Google Drive can only be opened by the Android app, as a restore.
- An iPhone's backup therefore holds only the history since Viber moved to the iPhone. The older
  history from an Android phone exists only inside Viber on Android, which is unreadable without
  root, and in Google Drive in a format only the app reads.

### 5.2 The iPhone's Viber database (`viber.sqlite`, Core Data)

| Table | Columns used |
|---|---|
| `ZVIBERMESSAGE` | `Z_PK`, `ZDATE` (s since 2001), `ZTOKEN`, `ZTEXT`, `ZSTATE` (`received` / `delivered` / `send`; outgoing = not `received`; "delivered" is on every message sent, its `ZSTATEDATE` the sending time: it says nothing of delivery), `ZMETADATA` (JSON; `textMetaInfo` type 0: a mention, `memberId` and where the text names them, `start`/`end` in UTF-16 units, the text holding `‪@Name‬`), `ZSYSTEMTYPE` (`''`/`url`/`formatted` = text, `customLocation`, `systemCallLog`, other = system), `ZATTACHMENT`, `ZCONVERSATION`, `ZPHONENUMINDEX` (sender) |
| `ZATTACHMENT` | `Z_PK`, `ZTYPE` (`picture`, `gif`, `video`, `audio`, `file`, `sticker`, `customLocation`), `ZNAME` (the file name in `Documents/...`), `ZID` (DownloadID) |
| `ZCONVERSATION` | `Z_PK`, `ZGROUPID` (groups), `ZNAME`, `ZSUBTYPE` (3 channel, 5 "My Notes"), `ZLASTREADTOKEN` (the owner read up to this message), `ZSEENSTATUSLASTTOKEN` (in a person's chat, they saw the owner's messages up to this one; where they let it be seen) |
| `ZMEMBER` | `Z_PK`, `ZMEMBERID`, `ZDISPLAYFULLNAME` |
| `ZPHONENUMBER` | `ZMEMBER`, `ZCANONIZEDPHONENUM`, `ZPHONE` |
| `Z_5PHONENUMINDEXES` | `Z_5CONVERSATIONS`, `Z_10PHONENUMINDEXES` (conversation members) |
| `ZRECENT`, `ZRECENTSLINE` | the recents: Viber calls (8.4) |

### 5.3 Import (`internal/importers/viber.go`)

**Key.** The message token is the same id on every device: every message that matches on text and
time within 5 s also matches on token, and none matches on text with a different token.
`message.key` is the token as text. System events (`Events.Type` 3) get no key, because tokens can
repeat among them.

**Time.**

- iPhone: `(ZDATE + 978307200) * 1000`.
- A token carries its send time: `(token >> 22) + 292057776050` is Unix ms. This is within 2 s for
  almost every message. The importer uses it only when the date is missing or 0.

**People.**

- The importer builds Viber member id → number from the archive's `viber_member` (what earlier
  imports learnt), then the iPhone's `ZMEMBER` + `ZPHONENUMBER` for members it does not know, and
  writes what it knows back to `viber_member`, so a number outlives its source.
- A person is stored as a normalised phone address where a number is known, so they meet their SMS
  and calls. Otherwise the person is stored as a Viber `id` address, the member id.
- The user (`[owner] numbers`) is left out of member lists.

**Conversations.**

- Groups get the key `group:<token>` (iPhone `ZGROUPID`) and `is_group = 1`.
- One-to-one chats are keyed by their member, with the fallback `conversation:<Z_PK>`.

**Channels are skipped:** iPhone `ZCONVERSATION.ZSUBTYPE` 3 (`CHANNEL`). The notes-to-self chat is
kept, as a chat of the user alone (not a group): it may matter a lot, so check it before removing
anything that belongs to it. On the iPhone it is the conversation with `ZSUBTYPE` 5 (its `ZMETADATA`
has `myNotesCheckboxCounter`).

**Extras** (`internal/importers/extras.go`): reactions (codes 1-5 as ❤️😂😮😢😡, every code also in
`reaction.code` as `viber:N`, the emoji NULL for 6 and later; one-to-one reactions without a type
`viber:?`, and the counterpart resolved as `"peer"`), replies, edits, deletions, forwards, links and
pins (`subtype_code` `viber:url`, `viber:systemPinnedMessageCreated`), and the sender's position
(`sender_lat`, `sender_lon`).

**Marks**: mentions into `mention` (the token as the text has it); `ZLASTREADTOKEN` as the chat's
`read_until`; `ZSEENSTATUSLASTTOKEN`, in a person's chat, as receipts of the owner's messages up to
it (`read_at` 0: read, when not known).

**Order.** Read all iPhone rows into memory, indexed by token, and add them. A row is skipped if its
`(source, row_key)` exists; if its token is already in `message` for Viber; or, with no token (some
system events), if the same conversation already has a keyless Viber message at the same millisecond
with the same text.

### 5.4 Refreshing Viber

- Every `iphone-sync` brings a new `viber.sqlite` and the new media.
- `everysaid import viber calls voip media` adds the new rows (`voip` for the calls in the recents,
  after `calls`); the row key is `Z_PK`, and the token dedupes against rows already there.

---

## 6. WhatsApp

### 6.1 Sources

- **iPhone backup** (`whatsapp.sqlite` = `ChatStorage.sqlite`, `whatsapp-contacts.sqlite` =
  `ContactsV2.sqlite`, media under `Message/Media`). The full message history, carried from phone
  to phone.
  - Media are there only as far as the phone still has them: older years are often mostly missing,
    since a move between phones does not carry old files.
- **The WhatsApp source** (`whatsapp-bridge`, `internal/whatsapp`): a whatsmeow client inside the
  app, linked as a companion device, running live. It writes what arrives straight into the archive,
  as the Meta sources do: each event (a message, an edit, a reaction, a receipt, a call, a group's
  change), or each history the phone sends, is a batch (`importers.WABatch`) that
  `importers.WhatsAppWriter` writes under the import lock, before WhatsApp is told it arrived (one
  the archive could not take, WhatsApp gives again). Nothing of a message's content is kept
  anywhere else.
  - What the connection needs to go on is in `<data>/keys.db` (encrypted, `internal/keys`), in the
    tables of `importers.WhatsAppSchema`, each row a store folder's (`store`): `whatsapp_state` and
    `whatsapp_event` (the connection, sending blocked, its last events), `whatsapp_sent` and
    `whatsapp_started` (for the limits of sending: a day), `whatsapp_history` (history the phone
    sent, kept until it is downloaded and written, tried again up to a day apart), `whatsapp_chat`
    (a chat's name, and whether the other side wrote there: it sends only there),
    `whatsapp_group` and `whatsapp_group_member` (`jid` as the group names them, `phone`, `lid`:
    read at each start and when they change; they also map LIDs to numbers), `whatsapp_blocked`,
    `whatsapp_photo` and `whatsapp_photo_check` (the profile photos seen of each jid),
    `whatsapp_recent` (the last week's messages by their ids: where each is, who wrote it, when the
    owner read it: what it answers, edits, deletes and marks read), `whatsapp_media` (what downloads
    a file, or a link preview's high-quality picture, until the archive has it), `whatsapp_vote`
    (each voter's newest vote in a poll: the SHA-256 of each option chosen, as WhatsApp names
    them), `whatsapp_call` (a call's signalling while it goes on), `whatsapp_unreadable` (a message
    this device could not read: a notice a day on, unless the phone sends it), and
    `whatsapp_pending` (a reaction, an edit, a deletion, a receipt or the owner's read of a message
    the archive does not have yet: applied when it comes, from the phone's history or the iPhone).
  - whatsmeow's own tables there have `whatsmeow_lid_map` (`lid`, `pn`), `whatsmeow_contacts`
    (names) and `whatsmeow_chat_settings`; `whatsapp_device` names the device of each store folder.
  - The store folder (`<data>/whatsapp-bridge/` unless `[whatsapp] bridge` in `config.toml` names
    another) holds only files on their way into the archive: downloads (`media/<chat>/<id><ext>`)
    and profile photos (`photos/<jid>/<picture id>.jpg`).
  - The source fills the gap between the last iPhone backup and now.
- **Not Android.** WhatsApp on Android keeps its database encrypted (`msgstore.db.crypt15`); it is
  not read. Its media folder goes with the app when it is uninstalled.

### 6.2 Keys and people

**Key.** `ZWAMESSAGE.ZSTANZAID` on the iPhone, and the message's id live. Both are the stanza id
WhatsApp sends, the same on every device: the iPhone's copy of a message and the live one are one.

**LIDs** (`<n>@lid`) are WhatsApp's privacy ids; they are not numbers. They are mapped with:

- the iPhone contacts: `ZWAADDRESSBOOKCONTACT.ZLID` → `ZWHATSAPPID`;
- the live connection: `whatsmeow_lid_map` (`lid` → `pn`), and the groups' members
  (`whatsapp_group_member`: `lid` → `phone`). The iPhone takes precedence.

A set of every LID seen anywhere tells LIDs apart from numbers among bare senders.
Unmapped LIDs are stored as WhatsApp `id` addresses, `<n>@lid`.

**Names** go to `handle_name` by kind: `book` (`ZWAADDRESSBOOKCONTACT.ZFULLNAME`,
`whatsmeow_contacts.full_name`), `chat` (`ZWACHATSESSION.ZPARTNERNAME`, `whatsapp_chat.name`),
`profile` (`ZWAPROFILEPUSHNAME`, `whatsmeow_contacts.push_name`).

**Conversations.**

- `@s.whatsapp.net` and `@lid` chats are keyed by the person.
- Groups (`@g.us`) are keyed by jid, with `is_group = 1`. Members come from `ZWAGROUPMEMBER`, and
  the sender of a group message from `ZGROUPMEMBER` → `ZMEMBERJID`.

### 6.3 Message types and hidden text (iPhone)

**Types.** `ZMESSAGETYPE`, as told by the files each type carries (`file`, `ffprobe`) and by the
metadata:

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
| text (business messages) | 19, 20, 25, 30, 31, 41 |
| system | everything else (6 group event, 10 notice, 14 deleted, 59 call, 66 poll, …) |

`message.subtype` names them (`whatsappSubtypes` in `extras.go`): 7 link, 11 gif, 14 deleted (also
`deleted = 1`), 54 video note, 6 group event, 10 notice, 59 call, 66 poll.

**Extras** (`whatsappExtras`): from `ZWAMEDIAITEM.ZMETADATA` field 5 the quoted stanza id, 19.1 its
text, 46 the forward score; from `ZWAMESSAGEINFO.ZRECEIPTINFO` field 7 the reactions (2 the jid,
3 the emoji; no jid means the user); `ZSTARRED`; a location's `ZLATITUDE`, `ZLONGITUDE`, `ZTITLE`;
a shared contact's `ZVCARDNAME` and number. Edits and polls are not found in the iPhone's database.
`ZRECEIPTINFO` field 2 also holds one entry per recipient of the owner's messages (1 to 2 in a
person's chat, a few in groups), with second counts after the sending (4, 5, sub-messages 9 and 10)
and field 3 the sending time (Unix s); the recipient (2.1, 8 or 9 bytes starting 0x8C) is not
decoded, so these receipts are not read.

**Business messages** (templates, buttons) have `ZTEXT` NULL. Their text is only in
`ZWAMEDIAITEM.ZMETADATA`, a protobuf blob.

- `protobufStrings` is a schema-less decoder. It walks varint keys and handles wire types 0, 1, 2
  and 5; any other wire type means "not a protobuf", and decoding stops.
- Length-delimited fields are tried first as nested messages, up to depth 8. If that fails they are
  read as UTF-8 and kept when printable.
- `metadataTextOf` keeps sentences: strings with a space or non-ASCII. It drops URLs, `/v/` paths
  and jids, and removes repeats.

### 6.4 Import order (`internal/importers/whatsapp.go`)

Channels (`...@newsletter`) and status (`status@broadcast`, and each contact's own, `...@status` and
`...@lid.status`) are skipped (`waChannels`). A bare id longer than 15 digits is no phone number (a
channel's id without its domain) and makes no handle.

1. iPhone (`importers.WhatsApp`). Each row is skipped if `(source, Z_PK)` is present, or if its
   stanza id is already a WhatsApp `message.key` (the live connection's copy).
2. The live connection, as it comes (`importers.WhatsAppWriter`, `whatsapp_live.go`), as the source
   `whatsapp-bridge`. The row key is `<chat_jid>/<id>`. A message the archive has (from the iPhone,
   or seen again in the phone's history) gets only what it lacked: its preview, where it was
   forwarded from, its album, its poll's votes, whom it names.
   - The kind comes from `WAMessage.Kind` (`bridgeKinds`), or, with none, from its file's:
     `image`, `video`, `audio`, `document`, `sticker`. Anything else is file.
   - The rest: subtype (`whatsmeow:<subtype>` as its code), the quoted message, forwarded, place, a
     contact's text, whom it names with @ (`mention`).
   - Changes of messages already in the archive: edits and deletions applied where their author
     made them (a deletion in a group: also an admin; one for the user alone, any message);
     reactions one per person (changed, added, removed once taken back; the phone's history does
     not undo what came live); who got and read the owner's messages (`receipt`, the first time
     each); how far the owner read each chat (`state_report` read_until); polls with their votes;
     pins, timers and groups' changes as notices.
   - Calls: the log's, and the signalling's once a call ended (an hour on, one whose end was never
     seen); one call in both, or already in from the iPhone, is kept once (same person and direction
     within a minute).
3. From time to time (the source's import, `importers.WhatsAppState`): the names WhatsApp shows,
   the chats' state (archived, pinned, muted), the groups' members (added to their conversations,
   never removed), the people blocked; and what waited in `keys.db` for its message.

### 6.5 Refresh

- iPhone: `everysaid iphone-sync`, then `everysaid import whatsapp calls voip media` (`voip` for the calls, after `calls`).
- Live: nothing to do. The WhatsApp source writes into the archive while it runs.
- Its files: downloaded as messages arrive ([whatsapp] download) or when one is opened, and taken
  into the archive's store at once (`WhatsAppWriter.Files`, 10.1).

---

## 7. SMS, MMS, iMessage, RCS

### 7.1 iPhone `sms.db`

**Tables.**

- `message`: `ROWID`, `guid`, `date`, `is_from_me`, `service`, `text`, `attributedBody`, `handle_id`,
  `cache_has_attachments`, `associated_message_type`, `associated_message_guid`, `item_type`.
- `handle`: `id` is the number or address.
- `chat` (`style` 43 = group), `chat_message_join`, `chat_handle_join`.
- `attachment` and `message_attachment_join`, for the mime type of the first attachment.

**Time.** `date` is in nanoseconds since 2001 on modern iOS and in seconds on old rows. Values above
1e11 are treated as ns. Unix ms = `date // 1e6` (ns) or `date * 1000` (s), plus `978307200000`.

**Service.**

- `SMS`, `iMessage` or `RCS` as given. Any other service name is taken as SMS, with its name kept
  as `subtype_code` (`sms.db:<service>`).
- SMS with attachments, or in a group chat, is recorded as `mms`.

**Kind.**

- `associated_message_type` ≠ 0 means a reaction (tapback). 2000-2006 are tapbacks (2006 with its
  own emoji), 3000-3006 take them back; a tapback taken back is removed from its message.
- `item_type` ≠ 0 means a system message (group renames and the like).
- Otherwise the kind comes from the attachment's mime (image, video, audio → voice, other → file),
  or is text.

**Hidden text: `attributedBody`.** On newer iOS many messages have `text` NULL. Their text is only
in `attributedBody`, an `NSAttributedString` archived with NeXTSTEP *typedstream* (`streamtyped`),
not a keyed archive. `attributedText`:

1. Find the class name `NSString`.
2. Find the next `\x84\x01+` (a string object marker followed by `+`, the C-string type tag).
3. Read the length. It is a single byte normally. `0x81` means a 2-byte little-endian length
   follows; `0x82` means a 4-byte one.
4. Decode that many bytes as UTF-8.

`cleanText` then removes U+FFFC, the object replacement character that marks an attachment's place,
and trims.

**Keys.** `guid` is the row key. iMessages also get `message.key = guid`. SMS/MMS have no global id,
so `key` is NULL.

**Reading.** `is_read` on a message from others: the newest one read is the conversation's
`read_until` (`date_read`, its change). On the owner's iMessage and RCS in a chat with one person,
`is_delivered`/`date_delivered` and `is_read`/`date_read` are receipts (an SMS's say nothing of
the other).

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
- **Reading.** `read` 1 on a message from others: the newest one read is the conversation's
  `read_until`.

### 7.3 Pairing the two phones (`internal/importers/sms.go`)

Phones carry the same history, copied from phone to phone at each change.

1. **`collapse`** drops exact repeats within one source: same service, direction, sender, text
   and second. An iPhone's inherited history can hold such rows, with a different guid each.
2. **`pairSMS`** matches one to one, greedily, in iPhone time order.
   - It takes iPhone `sms`/`mms` rows only. iMessage and RCS have no Android twin.
   - The Android index is keyed by (direction, text). The nearest unused Android row within 2 s
     (`pairMS`) wins.
   - SMS and MMS are paired together, because Android stores many long or link-bearing texts as
     MMS that the iPhone has as SMS.
3. **Choice.** For a pair, the Android row is kept if the Android phone was the device in use at
   its time (`Archive.Keeper`), the iPhone row otherwise. Unpaired rows from both sides are added
   as they are.
4. **Conversations.** SMS and MMS share one conversation per counterpart (service `sms`); a group is
   keyed by its sorted member addresses.
5. **Sources.** `iphone/sms`, `<device>/sms` and `<device>/mms` are separate sources, with the row
   keys `guid`, `_id` and `_id`. Every Android export is read (`archive.AndroidExports`): one
   folder per phone, `<export>/<device>/android.db`, the folder's name being the device; and an
   export kept as one file, `<export>/<[android] device>.db`. Repeats are dropped within each
   phone; two Android phones are not paired with each other. Any of them may be missing.

### 7.4 Refresh

- `everysaid iphone-sync` brings a new `sms.db`. Then run `everysaid import sms calls voip` (`voip` reads the
  carrier's missed-call notices from the new SMS, and must come after `calls`).
- The pairing is recomputed in memory on every run, and only rows whose `(source, row_key)` is new
  are added.
- New iPhone rows are after a retired Android phone's period of use, so they pair with nothing and
  simply come in.

---

## 8. Calls

### 8.1 iPhone `CallHistory.storedata` (Core Data)

- **`ZCALLRECORD`**:
  - `ZDATE` (s since 2001), `ZDURATION` (s), `ZORIGINATED` (1 out), `ZANSWERED`, `ZCALLTYPE`;
  - `ZADDRESS` (the number, in any format);
  - `ZSERVICE_PROVIDER`: `com.apple.Telephony` → phone, `com.apple.FaceTime` → facetime; an app's
    bundle id (with or without its team id in front) → its service (`callServices` in
    `calls.go`: WhatsApp, Viber, Telegram, Signal, Messenger, Teams, Skype, Zoom, Meet, Discord,
    Slack, LINE, WeChat); an unknown app keeps its bundle id as the service name;
  - `ZUNIQUE_ID`, used as the row key.
- **Missing `ZADDRESS`.** Some records, mostly app calls, have it NULL. The number is then taken
  from `Z_2REMOTEPARTICIPANTHANDLES` → `ZHANDLE.ZVALUE`.
- **"Answered".** For outgoing calls it is defined as `duration > 0`, since `ZANSWERED` is not
  meaningful for them. For incoming calls it is `ZANSWERED`.
- **Retention.** iOS does not seem to prune old calls. An iPhone's history may still start late,
  because a move from another phone brings only the months that phone still had.

### 8.2 Android call log

- `type` 2 is outgoing. Answered means duration > 0 when outgoing, `type` in (1, 7) when incoming.
- `date` is in ms; `number` is the counterpart.

### 8.3 Pairing and refresh

- **Pairing** (`pairCalls` in `internal/importers/calls.go`) works as for SMS. The key is
  (normalised address, direction), and the nearest call within 2 s wins.
  - It applies only to the iPhone's `phone` service.
  - Pairs occur only where the two phones' histories overlap (the months a move carried over).
- **Choice.** The device in use at the time (`Archive.Keeper`): the Android phone in its period,
  the iPhone otherwise.
- **Sources.** `iphone/calls` and `<device>/calls`.
- **Refresh.** `everysaid iphone-sync`, then `everysaid import calls voip`. The row key is `ZUNIQUE_ID`.
- **Detail** (`callExtras`): Android `type` 3 missed, 5 rejected, 6 blocked (`detail_code`
  `android:3`...); iPhone `ZCALLTYPE` 8 (video) sets `video` (FaceTime video calls).

### 8.4 App calls and carrier notices (`internal/importers/voip.go`, importer `voip`)

| Source | Read from | Row key |
|---|---|---|
| `iphone/whatsapp-calls` | `whatsapp-calls.sqlite`, WhatsApp's call log | its call id |
| `iphone/whatsapp` | call bubbles in the chats (`ZMESSAGETYPE` 59; metadata field 87: 1.1 video, 1.2 outcome, 1.3 duration, 1.5 participants) | the bubble's `Z_PK` |
| `whatsapp-bridge/calls`, `whatsapp-bridge/call-events` | the WhatsApp store's `calls` (`source` 'log' and 'event') and `call_participants` | its call id |
| `iphone/viber-calls` | `viber.sqlite` `ZRECENT` (recents), the number in `ZRECENTSLINE` | its `Z_PK` |
| `iphone/sip-calls` | `acrobits.sqlite`, an Acrobits softphone's (SIP) call log: `Events` of `legacy:calls` (direction 1 in, 2 out), the number in `RemoteUsers.transportUri` (`anonymous` hidden), the rest in `EventAttributes` (`callResult`, `callDuration`, `callId`) | its `eventId` |
| `sms-alerts` | the carrier's missed-call SMS in the archive itself, read by the parsers enabled in `[import] carrier_notices` (`internal/importers/carriers.go`; `gr`: Greek carriers, Latin look-alike letters normalised, Athens time) | `<message_id>/<i>` |

- The WhatsApp log and the bubbles describe the same calls: they are matched by time window and
  direction, the person taken from the chat, the creator jid as fallback (the log uses LIDs).
- A call already in the archive from **another** source, for the same service, the same person and
  the same direction, within 60 s, is not added again (it only fills in video, group, detail and
  key); calls without a known person are never merged; calls of the same source are never merged
  with each other, except a carrier notice sent twice (same number, same minute). A call log that
  says answered keeps no "missed" or "busy" from a notice. The carrier notices thus give way to the
  phone's own call log where it has the call.
- **Video.** The log (`ZVIDEO`) and the bubbles (field 1.1) agree on whether a call was a video
  call wherever both describe it.
- Calls get `detail` (missed, unanswered, busy, failed; WhatsApp's outcome 4 is missed and 5
  failed; Acrobits' `callResult` 1 and 16 answered, 2 missed, 4 rejected, 64 unanswered) with the
  source's code in `detail_code` (`whatsapp:4`, `viber:missed`, `acrobits:2`, `carrier:gr`),
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

### 9.1 Schema (`internal/archive`, `schema.sql`)

`PRAGMA user_version` is 1 until the first release; until then the schema changes in place, without
migrations.

**Lookup tables and vocabularies:**

- `service` (sms, mms, imessage, rcs, viber, whatsapp, phone, facetime, telegram, messenger,
  signal, instagram; a call app found in the iPhone's log adds its own), with `key_scope`: `service` where a
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
| `person` | `name` (set by the user), `name_source` (where the user pinned their name to come from: a source of names, or `address:<id>`), `contact_uid` (vCard UID), `contact_url` (CardDAV href or any other), `note`, `photo_source` (where the user pinned their photo to come from: `contacts`, a service, `address:<id>`, `none`) |
| `handle_name` | every name a service has shown for a handle: `address_id`, `service_id`, `kind` (`book`: the service's copy of the user's address book; `chat`: a chat's name; `profile`: chosen by them), `name`, `first_seen`, `last_seen`, `current` (the latest of that handle, service and kind); many handles may share a name. WhatsApp gives all three kinds, Telegram profile names; `internal/core` (names) picks a person's name from them |
| `handle_photo`, `chat_photo` | every photo a service has shown for a handle (the owner's too) or a group: `address_id` / `conversation_id`, `service_id`, `sha256` (in `media`; NULL: none), `first_seen`, `last_seen`, `removed_at` (the service said there was none any more); a row per photo, never overwritten. Brought by the iPhone's WhatsApp (`Media/Profile`, into `whatsapp-photos/`) and Viber (files named by `ZICONID`/`ZPREVICONID`, into `viber-photos/`), the live WhatsApp (`whatsapp_photo` in `keys.db`) and Telegram (`telegram_photo`), and the groups' photo notices; `internal/core` (photos) picks a person's or group's photo from them |
| `merge_dismissed` | pairs of people the user said are not one (`a` < `b`, `at`): that suggestion is not shown again |
| `group_link` | groups the user merged: each conversation shown as part of the chat of `into_id` (`c<into_id>`, itself never linked) |
| `group_dismissed` | pairs of groups (their chats' conversations, `a` < `b`, `at`) the user said are not one |
| `person_address` | `address_id` PK, `person_id`, `how` (`auto`: one person per new address; `manual`: merged by the user, or by an import from the user's own groupings) |
| `account` | the user's own handles: `address_id`, `service_id` (NULL: every service), `label`; seeded from `[owner] numbers` |
| `device` | `name` (iphone, an Android device's name, whatsapp-bridge...), `kind`, `used_from`, `used_until` (Unix ms) |
| `source` | `name` (`<device>/sms`...), `path`, `imported_at`, `device_id`, `media_root` (the folder `attachment.source_path` is relative to; `{cache}` and `{data}` stand for those folders), `instance_id` (the plugin instance that reads it) |
| `plugin_instance` | `plugin` (its id), `kind` (source, library, contacts, analysis), `label`, `settings` and `state` (JSON), `enabled`, `device_id`, `is_default` (the library kept files go to), `created_at`, `last_run`, `last_status` |
| `contact`, `contact_address` | an address book's contacts (`instance_id`, `uid`, `url`, `name`, `organization`, `photo` in `<cache>/avatars/`) and the addresses they list, joined only to addresses the archive has |
| `contact_gone` | a contact the address book no longer has (`contact_id`, `gone_at`): kept with its addresses, its name and photo still its person's where no contact still there lists the number; gone from here when it comes back |
| `state_report` | what a source says about a conversation: `conversation_id`, `instance_id`, `field` (`archived`, which only starts the app's own in `chat_state`; `muted`, `pinned`, `read_until`), `value` (muted: until, Unix ms, -1 for ever), `observed_at` (when the source's data was so), `changed_at` (when it became so) |
| `chat_state` | what the user chose in the app per chat (`p<person>`, `c<conversation>`): `field`, `value`, `set_at`, `always`; `internal/core` combines it with the reports (see `docs/design.md`) |
| `setting` | the user's settings shared by every device (JSON values), e.g. `unread_since`, `push_preview` |
| `label` | the words people are described by: `kind` (`tone`, many to a person; `relation`, one), `key` (one the app brings, its words in the app's languages; NULL: the user's), `name` (the user's), `meaning` (what the local models read; NULL: the app's own; '': never theirs), `sensitive`, `position`; the app's are put in once (`archive.Labels`, setting `labels_seeded`) |
| `person_label` | a person's labels: `state` (`yes`, `no`: the user's; `suggested`: the models'), `votes` of `models`, `evidence` (a line of the chat), `at` |
| `name_guess` | a name found for someone without one: `how` (`models`, `handle`), `name`, `votes` of `models`, `evidence`, `dismissed` (the user said it is wrong) |
| `analysis` | the people the local analysis read: `messages` then, `labels` (a digest of the lists it judged by), `models`, `at` |

A number is one `address` whatever the service, so its SMS, calls, Viber and WhatsApp meet in one
person; a new address starts as a person of its own, until the user merges it.

**History:**

| Table | Contents |
|---|---|
| `conversation` | `service_id`, `key`, `title`, `is_group`; (service, key) unique |
| `conversation_member` | conversation × address |
| `message` | `status` (a message sent from the app: sending, sent, failed), `id`, `service_id`, `conversation_id`, `ts` (Unix ms UTC), `outgoing`, `sender_id` (NULL when outgoing), `kind_id`, `text`, `key`, `key_scope` (the conversation, for services whose keys are per chat); unique on (service, key, key_scope) where there is a key; and the extras: `subtype`, `subtype_code`, `reply_to`, `reply_key`, `reply_text`, `edited`, `deleted`, `forwarded`, `starred`, `lat`, `lon`, `place` (a location shared), `sender_lat`, `sender_lon` (where the sender was, older Viber), `forward_from` (who a forwarded message first came from), `album` (the service's key of files sent together), `pinned` (pinned until, Unix ms; -1 for ever) |
| `reaction` | `message_id`, `emoji` (NULL where only a code is known), `code`, `count`, `address_id`, `outgoing` |
| `link_preview` | a link's preview as its service sent it: `message_id` PK, `url`, `site`, `title`, `description`, `image` (in `link_preview_image`: `sha256` of a small JPEG, at most 480 px, `jpeg`, `width`, `height`; not `media`). Brought by Telegram (`media.webpage` of the stored message; its picture downloaded by the live connection into `telegram_webpage_photo` in `keys.db`), WhatsApp (the iPhone's `ZWAMESSAGEDATAITEM` and `whatsapp-previews/`; the live store's `preview_*`), Signal (the message's previews, the picture downloaded with its files), iMessage (`payload_data`, its payload file), Viber (`ZMETADATA`; the picture fetched from the linked site through `internal/netsafe`, failures in `<cache>/link-preview-failed.tsv`), Messenger and Instagram (shared links, the picture through `meta_file`) and their data export (shared text) |
| `receipt` | message × address: `delivered_at`, `read_at`, `played_at` (Unix ms; 0 so but when not known; NULL not yet), for the owner's messages |
| `mention` | message × address: whom its text names with @, and `token`, how the text names them (WhatsApp `@<number or LID's user part>`, Telegram `@username` or the name itself, Viber `@Name`) |
| `message_origin` | (`source_id`, `row_key`) PK, `message_id` (WITHOUT ROWID) |
| `call` | `id`, `service_id`, `address_id` (NULL for hidden numbers), `ts`, `outgoing`, `answered`, `duration`, `key`, `detail`, `detail_code`, `video`, `attempts`, `conversation_id` |
| `call_member` | `call_id`, `address_id`, `outcome`, `outcome_code` (group calls) |
| `call_origin` | as `message_origin` |
| `viber_member` | Viber member id → number, as the sources said |
| `blocked` | `address_id`, `phone` (the device, or `telegram`, `whatsapp`), `original`: handles blocked on a phone (its export) or a service (its blocklist, kept as it changes) |
| `spam` | `address_id` PK, `decision` (`removed`: removed as spam, and removed again from what each import brings; `kept`: not spam, not suggested again), `name` (the name it had), `at` |
| `message_fts` | contentless FTS5 table (`contentless_delete=1`, `detail=none`: words without their places), by words (`unicode61`), over the text folded by `text.Fold` (Unicode case folding, which also makes a final sigma σ; combining marks removed in every script; compatibility forms brought to one), rowid = `message.id`; written by `Archive.AddMessage` (a trigger cannot fold); a query is folded the same way (`core.WordFilters`) |
| `term` | every word `message_fts` holds (`term`), for parts of words: written with the index (`Archive.IndexText`), checked against it (`archive.SyncTerms`, fts5vocab) when the server starts and after an import from the command line |

**Media:**

| Table | Contents |
|---|---|
| `media` | `sha256` PK, `size`, `mime`, `path` (`media/<ab>/<sha256><ext>`, relative to the media root) |
| `attachment` | `message_id`, `sha256`, `source_id`, `source_path`, `name` (the file's name as sent); (source, path, message) unique |
| `library_link` | (`sha256`, `library`) PK, `asset_id` (for a folder: the path in it), `method` (checksum, phash, clip, upload), `score`, `linked_at`, `instance_id` (the library plugin instance): a file in a photo library; more than one library may hold it |
| `media_same` | `sha256` PK, `same_as`, `method`, `score`, `linked_at`: a file removed as the same picture as one the archive keeps |
| `media_decision` | `sha256` PK, `decision` (keep, remove, library), `date_ms` (a date the user gave), `at`: the user's sorting in the app; the newest decision is the one that counts. |

**Connection settings:** WAL journal, `foreign_keys = ON`, a 30 s busy timeout; the binary runs with
umask 077.

**Extras.** `internal/importers/extras.go` turns each source row into the extra columns and reactions
(`archive.Extras`, given to `Archive.AddMessage`); `Archive.Resolve` then links replies by
`reply_key` (within the conversation where keys are per chat), applies edit events and iMessage
tapbacks. A position on a message that is not a location is the sender's (Viber only,
`sender_lat`). iMessage's `reply_to_guid` is not a reply (it points to the previous message) and is
not used.

**No raw rows.** The source rows are not kept in the archive: what matters from each is in the
columns above, and the extracted sources are the way back to a source row. Anything new worth
keeping from a source goes into a column.

**Both origins of a pair.** A record found on both phones is kept once, but both source rows are in
`message_origin` / `call_origin`, pointing to the same row (`Archive.RecordPairs` for SMS and calls).
Which copy is kept: `Archive.Keeper`, the device in use at the time by its period in `device`,
else the one in use most recently, else the first named (the iPhone).

**Location.** `archive.DB()` = `<data>/archive.db`; `archive.MediaRoot()` = `[media] store`,
`<data>` by default (the media, under `media/`); `archive.IphoneData()` = `<cache>/iphone`.
`everysaid import --db PATH` reads another archive.

### 9.2 Address normalisation (`archive.Address`)

| Input | Result |
|---|---|
| contains `@` | `email`, lower-cased |
| a URI (`sip:...`; `tel:` is read as a number) | `uri`, lower-cased |
| not digits after removing spaces, `-`, `(`, `)`, `.` | `sender` (sender names such as bank ids) |
| `00…` | `+…` |
| a valid number as written, a national one read in `[owner] region` (libphonenumber's rules, `nyaruka/phonenumbers`) | E.164, `+…` |
| a valid number once a `+` is put before it (`306912345678`, `4915112345678`) | E.164 |
| otherwise, fewer than 10 digits | the bare digits. Short codes: the iPhone adds a `+`, Android does not |
| otherwise | `+digits` |

Without `[owner] region` a national number cannot be read: it is kept as `+digits`.

**A spurious country code on an iPhone.** An SMS history inherited from older phones can have
short codes and foreign numbers with the home country code in front (e.g. `+3015551234567` for
`+15551234567`). No general rule can strip it: the code followed by a national-length number is a
valid number, and some short codes really start with those digits; libphonenumber does not either
(such a number is not valid, and is kept as it is). Where an Android copy of the same SMS has the
address without the prefix, the pair shows the right value.

Contact names are not taken from any phone. They come from the user's address books (a contacts
source, CardDAV or .vcf), matched by number and email.

### 9.3 Running

```
everysaid import [--db PATH] [sms calls viber whatsapp telegram voip media]
```

- **Registry.** `importers.Names` in `internal/importers/importers.go`, in the order they run. Without names, the
  ones `[import] importers` lists, else all; an unknown name is refused. Every source is optional:
  an importer whose sources are missing says so and adds nothing. The carrier notices are read only
  by the parsers `[import] carrier_notices` enables (`internal/importers/carriers.go`, one parser
  per carrier or country). The Adium and Pidgin logs have no importer name: their source in the app
  imports them.

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
| WhatsApp | `Z_PK` / `<jid>/<id>` | stanza id (`message.key`); iPhone first within one run |
| Media | `(source, source_path, message)` | content sha256 (`media`) |
| Services with ids per chat (Telegram) | `(source, row_key)` | (`message.key`, `key_scope`) |
| Messenger, Instagram | live: `<thread>/<message id>`; export: `<thread>/<ms>/<n>` | the message id (`message.key`); the export's: time (ms), direction and text, or an Adium/Pidgin log's within 2 s |
| Adium and Pidgin logs (no ids) | time, direction, kind and text within the conversation (no key), no `message_origin` | the same (README, "Adium and Pidgin") |

---

## 10. Media: from the archive to the photo library

### 10.1 Into the archive (`internal/importers/media.go`)

**Storage.** Each file is stored once by content: `<media root>/media/<sha256[:2]>/<sha256><ext>`,
the media root being `[media] store`, `<data>` by default (some files exist nowhere else once their
source is gone).

- It is a **hard link** to the source file: same file system, no extra space. Across file systems,
  or where hard links cannot be made, it is a copy.
- The `mime` is guessed from the extension.

**Links per source:**

| Step | Source file | Message found by |
|---|---|---|
| `MediaWhatsApp` | `whatsapp-media/<ZMEDIALOCALPATH minus "Media/">` | stanza id → `message.key` |
| `MediaViberIphone` | `viber-media/{Attachments,FileMessages,VoiceMessages}/<ZATTACHMENT.ZNAME>` | token → `message.key`, else `Z_PK` → `message_origin` |
| `MediaSMSIphone` | `sms-media/<attachment.filename minus "~/Library/SMS/Attachments/">` (not `*.pluginPayloadAttachment`, a link's preview: a URL balloon's `payload_data`, an NSKeyedArchiver `LPLinkMetadata`, gives `link_preview` its link, site, title and summary; its `image` names one of these files by its place among the message's, which the sync copies and the SMS importer reads into the preview, never a file of the message) | `message.guid` → `message_origin` (`iphone/sms`) |
| `MediaMMSAndroid` | `<export folder>/mms-parts/<part _id>` (empty ones skipped) | MMS `_id` → `message_origin`; if the iPhone's copy was kept instead, the single SMS/MMS message with the same direction within ±2 s |
| `TelegramMedia` | `<cache>/telegram/media/<chat>/<message><ext>` | `<chat>/<id>` → `message_origin` |
| `WhatsAppWriter.Files` (the live WhatsApp source, as each file is downloaded) | `<store>/<whatsapp_media.path>` (only files within the store) | `<chat_jid>/<id>` → `message_origin`, else the id → `message.key` (the iPhone's copy) |

`everysaid import media` runs the steps of the phones and Telegram; each source in the app runs its
own. Viber media pulled from an Android phone have no step yet.

**Incremental.**

- An existing `(source, path, message)` link is skipped.
- A file already linked from the same source path reuses its recorded sha256 and is not hashed
  again, as long as it is still the stored file (same size and time).
- Files missing on disk, for example pruned ones, are counted as without a file and skipped. This is
  harmless.

### 10.2 Pictures do not stay

The archive keeps the record, but not the file.

- Each `media` / `attachment` row stays.
- The file goes to immich, and `library_link` records the asset, or it is removed (the user's
  choice).
- Documents and PDFs are a second phase. Video calls are never kept.

### 10.3 The separate picture tools

A backlog of chat pictures can be worked through with tools kept separately, outside this
repository; they are not part of the app. They index the immich library (read only), match the chat
pictures against it (checksum, perceptual hashes, image embeddings, capture dates), have local
vision models say what the rest show, offer review pages that only mark the user's decisions, and
then carry them out: `library_link` written and committed before any local copy is removed, every
copy checked to be the same content, the encrypted backup never touched. Their own files are listed
in section 14. The app's own way is its media view and the library plugins (folder, immich).

### 10.4 Where the archive and its media live

- The database is `<data>/archive.db`. The data folder should be covered by the user's backups
  (best from a snapshot, so the copy is consistent); backing it up is not Everysaid's job.
- The archive's media wait in `<data>/media/` (`[media] store`) until they go to the photo library
  or are removed. They are hard links to the files `iphone-sync` copied into `<cache>/iphone/` (and
  to the other sources' files) where both are on the same file system, copies otherwise. The cache
  is meant to be left out of backups: everything there can be made again from the encrypted iPhone
  backup and the sources.

---

## 11. Refresh runbook

With the iPhone on the cable:

```
# 1. back up and extract (password from the keyring; the phone may ask for its passcode)
everysaid iphone-sync
# 2. bring everything new into the archive (store rows come in with `whatsapp` where [whatsapp] bridge is set)
everysaid import sms calls viber whatsapp telegram voip media
```

(The iPhone source in the app does both; the WhatsApp and Telegram sources import on
their own.) The photo-library work is done in the app's media view, or with the separate tools
(10.3).

**Occasionally:**

- `everysaid iphone-verify` checks the whole backup (expect databases that changed during the
  backup, and browser "Web Data" files, to be reported; see 3.8).
- `everysaid iphone-ls` shows what else the backup holds.

**One-off sources (a retired Android phone), final once done:**

- `everysaid android-export` (or the Android source in the app);
- `adb pull` of the Viber media (not linked to messages yet);

---

## 12. Incremental behaviour, summarised

| Step | First run | Later runs | Unit of change |
|---|---|---|---|
| `idevicebackup2 backup` | full (or `--full`) | phone sends changed files, deletes removed ones | file |
| database extraction | decrypt 6 DBs | decrypt them again in full, replace atomically (`--only` for some) | whole file |
| media extraction | all files of messages | only names not on disk and not in the archive's `attachment` | file |
| `everysaid android-export` | all tables, all parts | skips existing tables, existing part files and parts the archive took | table / part |
| `everysaid import` | everything | rows whose `(source, row_key)` is new and whose `key` is not yet present | row |
| `everysaid import voip` | everything | calls whose source row is new and that no other source has (same service, person, ±60 s) | call |
| `everysaid import media` | hash and link all | new `(source, path, message)` only; known paths not rehashed | link |

---

## 13. Known gaps, pitfalls and inconsistencies

1. **SMS and call pairing is greedy and recomputed on every run.** As long as the Android data are
   fixed and new iPhone rows are after the Android phone's period of use, earlier choices stay
   stable. Changing `pairMS`, `collapse` or the devices' periods, then re-running on an existing
   archive, could add the other side of pairs already imported. Rebuild the archive instead.
2. **Source rows live only in the extracts.** No source row is kept in the archive (9.1), neither
   the chosen one nor the copy not chosen. An iPhone row is read again from what each sync
   extracts; a removed Android export's rows survive only as what the archive took from them (both
   origins of each pair).
3. **The acquired data need a copy of their own.** The encrypted iPhone backup is the only copy of
   the phone's call history. Keeping it safe is the user's job, with whatever backup they use.
4. **Not extracted from the iPhone backup:**
   - SMS/iMessage attachments (`MediaDomain Library/SMS/Attachments`);
   - `CallHistoryTemp.storedata`.
   Messenger keeps its messages out of the backup; Teams is on the server; Discord leaves only
   avatars.
5. **Viber calls older than the iPhone's recents are lost.**
6. **The separate tools' immich index lags** when it is made from immich's nightly dump: something
   uploaded today is invisible to their matching until tomorrow.
7. **Short codes and alphanumeric senders** are stored without a country code by design. Two
   phones writing the same short code differently (`+1234` / `1234`) are normalised to the bare
   digits.
8. **A new iPhone** means a new UDID in `config.toml` (or none, to take the only backup or phone).
   A new backup is also needed (a new keybag, though the same password can be set). Paths and
   settings are in `internal/config` (README, "Folders and configuration").

---

## 14. Files, permissions, caches

| Path | Contents | Mode | In backups? |
|---|---|---|---|
| `<backup_root>/<UDID>/` | encrypted iPhone backup (`<data>/iphone-backup` by default) | as written by idevicebackup2 | the user's to back up: the only copy of the call history |
| `<cache>/iphone/` | decrypted DBs and new media (`iphone-sync`) | 700/600 | no: made again from the backup |
| `<export>/<device>/` | Android exports (`<data>/android` by default) | 700/600 | yes, while they are not all in the archive |
| `<data>/whatsapp-bridge/` | the live WhatsApp's files on their way into the archive: `media/`, `photos/` (its state and the session's keys are in `<data>/keys.db`) | 700/600 | no: in the archive within moments |
| `<cache>/telegram/` | `media/` (`telegram-sync`); its store is in `<data>/keys.db` (`telegram_*`) | 700/600 | no: read again from the API |
| `<data>/keys.db` (`signalmeow_*`, `signal_device`, `signal_*`) | Signal's devices (their keys, contacts, groups) and what each received until imported | 600 | yes, with the data folder (encrypted) |
| `<cache>/signal/<instance>/media/` | the files Signal's client fetched, until the import takes them into the archive's media | 700/600 | no: in the archive once imported |
| `<data>/media/` | the archive's media until they go to the photo library (`[media] store`) | 700/600 | yes, with the data folder: some exist nowhere else |
| `<data>/archive.db` | the archive | 600 | yes, with the data folder |
| `<config>/config.toml` | settings (README, "Folders and configuration") | the user's | yes |
| keyring, service `everysaid` | `backup-password`, `immich-key` and the other secrets | the keyring's | the keyring's |
| `<config>/backup-password`, `immich-key` | the same secrets where there is no keyring (or until moved with `--move-to-keyring`) | 600, folder 700 | opened only by Everysaid's code |
| `<cache>/immich.db` | immich index (the separate tools, as the rows below) | 600 | rebuildable |
| `<cache>/immich-thumbs/` | immich's small previews, through the API | 700/600 | rebuildable |
| `<cache>/match.db` | match results | 600 | rebuildable |
| `<data>/vlm.db` | model answers, `filtered`, `skipped`, `verdict` | 600 | yes (answers take hours to remake) |
| `<data>/review.db` | the user's decisions: `triage`, `decision`, `date_from`, `aside`, `aside_decision`, `aside_date`, `restored` | 600 | yes (not rebuildable) |
| `<cache>/` `faces.db`, `neighbours.db`, `similar.tsv`/`.npz` | faces, tags, immich neighbours, look-alike groups | 600 | rebuildable (but `faces.db` holds names the user gave to face groups) |
| the models' own caches (Ollama, HuggingFace, insightface) | models | n/a | rebuildable by downloading again |

The rule is:

- `<cache>` holds only what can be rebuilt (the extracts, the snapshots, the immich index and the
  match results). Backups are expected to skip it.
- `<data>` holds what cannot be rebuilt, or would cost hours to remake: the archive and its media,
  the backups and exports, the live sources' stores, the model answers and the user's decisions.
  The user's backups should cover it.

## 15. Signal: the client's commands and events

The `signal` source (`internal/signal`) speaks to its client (`client.go`: signalmeow in the
process, built with the tag `signal`) in the words of the protocol its earlier helper program spoke
over a pipe (presage, in Rust; gone): commands with their fields, answered in JSON, and events,
JSON objects named by `event`, handed to the plugin one at a time and in order. The plugin keeps
each event in the store (`store.go`), and the importer (`import.go`) reads the store into the
archive. What Signal sends is acknowledged to its server only once the event is kept: signalmeow
leaves an envelope whose handler failed unacknowledged, the server sends it again, and signalmeow's
buffer of decrypted envelopes gives it again without decrypting twice.

**The device.** Linking (`link`) is Signal's provisioning as a secondary device, with the phone
offered the transfer of its history. signalmeow keeps the device in `keys.db` (`signalmeow_device`
and the tables of its keys, sessions, contacts and groups, `signalmeow_*`, upgraded by signalmeow
itself); `signal_device` (instance, ACI, name) names each plugin instance's. Unlinking from the app
imports the store, then removes the device's rows (cascading) and the instance's store rows; the
phone may still list the device until it is removed there.

**The store** (`keys.db`, tables `signal_*`, each row of an instance): `signal_account`,
`signal_contact`, `signal_pni` (a PNI's number and ACI, as Signal told them), `signal_group` and
`signal_group_member`, `signal_message` (each message's event whole, by author and sent time),
`signal_edit`, `signal_deletion`, `signal_reaction` (the latest of each person), `signal_receipt`,
`signal_read`, `signal_call`, `signal_poll_vote`, `signal_unreadable`, `signal_chat_state` (a
chat's archived, pinned, muted), and the bookkeeping of files fetched again (`signal_late_file`,
`signal_fetch_try`, `signal_fetch_at`). After each import, a message the archive has that came over
30 days ago (by when it came, not when it was sent: the history transferred at link time is old;
its files are asked again for as long) leaves the store with what was about it alone (its deletion,
votes, its edits' content: their times stay, so that what is aimed at an edit still finds the
message); reactions, receipts and reads as old whose message left go too, and calls and notes as
old. A chat the user left out of the import stays whole.

**The history transferred at link time.** Once connected, the client waits (up to an hour, as
signalmeow does) for the archive the phone uploads when its user agrees to transfer the history;
signalmeow downloads, checks and decrypts it into `signalmeow_backup_*`, and the client says it as
events (`transfer.go`): the people (their ids and names) and groups, each chat's state, and each
chat item as a message (its earlier versions as edits), with its reactions, the others' receipts,
the owner's reading, a poll's votes, a deletion for everyone, and the calls of the call log. Files
come where Signal's transit CDN still has them. Once the events are kept the tables are cleared; a
transfer a stopped client had not finished is finished at the next connection.

**The chats' state.** At each connection, and when the phone says its storage changed, the client
reads Signal's storage service (the account's master key comes at link time): each contact's and
group's archived and muted state, the pinned chats, and each contact's ids (ACI, PNI, number) and
names.

**Ids.** A person's chat is keyed by their ACI, or by their PNI (`PNI:<uuid>`, the id of a phone
number) for someone written to only by number. What ties a PNI to its person comes only from
Signal: the phone's copy of a message it sent to a number (`destinationE164`), a reply signed with
the PNI (`PniSignatureMessage`, signalmeow's `ACIFound`), the storage service's contact records,
the transfer archive's recipients, and contact discovery (`discover`).

### Commands

| `cmd` | fields | result |
|---|---|---|
| `open`, `status` | | `{open, linked, receiving, aci, pni, phone, device_id, device_name}` |
| `link` | `device_name` (default "Everysaid") | a `link_url` event, then (once scanned) status; `already_linked` where linked |
| `sync` | | asks the phone for its contacts: `{requested: true}`; they come as a `contacts` event |
| `contacts` | | `{contacts: [{aci, phone, name, profile_name}]}` |
| `groups` | | `{groups: [{id, title, description, revision, members, pending}]}` |
| `receive` | `download` (default true: fetch files) | `{started}`; then events until the client ends |
| `send` | `chat`, `text`, `quote`, `mentions`, `attachments` | `{ts}`; and a `message` event of what was sent |
| `react` | `chat`, `target: {author, ts}`, `emoji`, `remove` | the owner's reaction (`remove` with the emoji that was there takes it back): `{ts}`; and a `reaction` event |
| `edit` | `chat`, `target_ts` (the newest version), `original_ts` (the first), `text` | a new text, the files and quote kept from the store's copy: `{ts}`; and an `edit` event |
| `delete` | `chat`, `target_ts` | deleted for everyone: `{ts}`; and a `delete` event |
| `delete_me` | `chat`, `target: {author, ts}` (its first time) | deleted for the owner alone, said to their other devices (`SyncMessage.DeleteForMe`): `{ts}`; and a `delete` event with `for_me` (one from the phone comes as such an event too) |
| `mark_read` | `messages: [{author, ts}]`, `receipts` (default true) | a read sync to the phone always, read receipts to each author where `receipts`: `{marked}` |
| `fetch` | `messages: [{author, ts}]` | the files of these messages (in the store) fetched again, each message said again: `{found}` |
| `discover` | `numbers` (E.164) | `{found: [{number, aci, pni}]}` (Signal's contact discovery); each also an `ids` event |
| `history` | `since` (Unix ms) | `{events: [...]}`: the transfer archive's events not yet taken in |
| `quit` | | `{}`, then it ends |

A `chat` is `{"kind": "contact", "id": "<ACI or PNI:...>"}` or `{"kind": "group", "id": "<group
id>"}`, the base64 identifier Signal's apps show, never the master key. A `quote` is `{ts, author,
text}`; `mentions` are `[{start, length, aci}]` in UTF-16 units, each over a U+FFFC in the text;
`attachments` are `[{path, content_type, filename, voice}]`. Refusals have a code: `bad_request`,
`not_linked`, `already_linked`, `unknown_group`, `failed` (Signal's own error).

### Events

Every message-like event has `chat`, `sender` (an ACI; the account's own for what the owner sent
from any device), `sender_device`, `outgoing`, `ts` (when it was sent, Unix ms: with the author,
Signal's name for a message) and `server_ts`.

| `event` | fields |
|---|---|
| `link_url` | `url` (`sgnl://linkdevice?...`: drawn as a QR code for the phone) |
| `queue_empty` | what waited on the server has all come |
| `contacts` | `contacts`, as the `contacts` command (after the phone sent them) |
| `contact` | `aci, phone, name, profile_name`: someone seen in a message (their profile), or in the storage service or the transfer |
| `ids` | `aci`, `pni`, `phone` (each may be null): one person's ids as Signal told them (see Ids above) |
| `group` | as one of `groups`, before the first message of a group at a new revision |
| `message` | `text` (a long message's whole text, `long_text`), `mentions`, `quote`, `attachments` (`content_type, filename, size, width, height, caption, voice, gif, borderless, sticker, emoji, file` or `error`, and `pointer`: where Signal keeps the file and its key), `contacts`, `previews` (`url, title, description, date`, `image` as an attachment: the first's picture fetched whatever the setting as `preview-<name>`, taken into the archive's `link_preview` and removed, never a file of the message), `poll`, `group_change`, `group_changes` (`{editor, actions}`), `group_revision`, `group_call`, `expire_timer_update`, `expire_timer`, `forwarded`, `view_once`, `pin`, `unpin`, `poll_end`, `payment`, `gift`, `story_reply`, `story_reaction`, `unsupported` |
| `poll_vote` | `target_author`, `target_ts`, `options`, `vote_count` |
| `edit` | `target_ts` and the message's new fields |
| `delete` | `target_author`, `target_ts`; `admin` for a group admin's deletion, kept only when the sender is an admin of the group |
| `reaction` | `emoji`, `remove`, `target_author`, `target_ts` |
| `receipt` | `sender`, `kind` (`delivery`, `read`, `viewed`), `timestamps` (the owner's messages) |
| `read` | `messages: [{author, ts}]`: the owner read them on another device |
| `call` | `id`; from a call message: `action` (`offer`, `answer`, `busy`, `hangup`), `video`, `hangup` (how); from the phone's call log or the transfer (`source: "sync"`): `type`, `direction`, `result` |
| `chat_state` | `chat`, `state` (`archived`, `pinned`: 0/1; `muted`: until, Unix ms, -1 for ever, 0 not), `ts` |
| `transfer` | `messages` (how many came), or `error`: the history the phone transferred |
| `decryption_error` | `sender`: they could not decrypt a message sent from here (signalmeow sends it again) |
| `unreadable` | `sender`, `device`, `ts`, `chat`, `retry`, `resendable`: a message from them could not be decrypted here (the sender was asked to send it again); not said of what was not to be shown |
| `number_changed` | `phone`: the account's number (its PNI) is no longer the one this device was linked with; checked once a connection |
| `receive_ended` | `error`, `code`: `unlinked` (Signal refused this device: removed from the phone), `outdated` (HTTP 499: this version is too old), `stopped` (the client ended) |

Typing, stories and Signal's own housekeeping messages are not passed on. Calls are seen as their
signalling and the phone's call log, never answered or placed.
