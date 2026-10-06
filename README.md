# Chronika

A personal archive of messages and calls: SMS and iMessage, phone and FaceTime calls, Viber,
WhatsApp, Telegram, with their media, in one SQLite database. It is built from local phone backups
(iPhone), Android phones over adb, and the services' own APIs and exports. Over it: a core, an app
for a person (a messenger for the whole history, on desktop and phone, `chronika serve`) and an MCP
server for an assistant (`chronika mcp`). `docs/app.md` tells how to run them, `docs/design.md`
how they are built.

The goal is a complete, permanent history that does not depend on what the phones keep: a message
stays in the archive after the phone that held it is gone, deleted or replaced. Nothing goes
through iCloud. Telegram, and WhatsApp through a bridge, can also be live: new messages arrive in
the app as they come, and can be answered from it.

Backups are not the project's job. Keeping the archive itself safe (and the phone backups it reads,
and the photo library) is the user's concern, with whatever backup they use. What Chronika owes is
not to lose anything by its own doing (writes in transactions, nothing removed because the source
dropped it) and to keep what cannot be made again in its data folder, never in the cache, which
backups skip by convention.

`docs/data-sources.md` is the technical reference for how each source is read, refreshed and
unlocked. A `LOCAL.md`, not tracked, may hold notes about one installation (devices, paths,
history); nothing in the tracked documentation depends on it.

## What works

| Part | State |
|---|---|
| Encrypted iPhone backup on Linux, incremental refresh | working (`scripts/iphone-sync.py`) |
| Extraction of `sms.db`, `CallHistory.storedata`, Viber and WhatsApp (databases and media) | working (same script) |
| Integrity check of an iPhone backup | working (`scripts/iphone-verify.py`) |
| Looking inside a backup without extracting | working (`scripts/iphone-ls.py`) |
| Android call log, SMS, MMS and blocked numbers over adb | working (`scripts/android-export.py`) |
| Old Viber history through Viber Desktop (Linux) | working (`scripts/viber-desktop-export.cpp`) |
| Telegram through its API | working (`scripts/telegram-sync.py`) |
| WhatsApp through a live bridge (whatsmeow) | read, for what came after the last backup |
| Unified archive, deduplicated across sources | working (`chronika`) |
| Replies, reactions, edits, locations, call outcomes | working (`chronika/extras.py`) |
| WhatsApp and Viber calls, carrier missed-call notices | working (`chronika/voip.py`) |
| Chat media matched against a photo library (immich) and uploaded with the right date | working (scripts, see "Media") |
| The core: chats, a person's stream across services, search ignoring accents, people, calls, media, statistics | working (`chronika/core/`) |
| Plugins: sources, photo libraries (folder, immich), contacts (CardDAV, .vcf), as instances | working (`chronika/plugins/`) |
| The app: passkeys, the messenger, search, media, people, sources, settings, push, PWA | working (`chronika serve`, `web/`) |
| Live Telegram and WhatsApp (bridge), sending where the plugin can | working |
| The MCP server | working (`chronika mcp`) |
| A demo archive of invented people | working (`chronika demo`) |

## Folders and configuration

`chronika/config.py` is the one place for paths and settings. The three folders follow the
platform (platformdirs; on Linux the XDG folders, so `XDG_DATA_HOME` and the like move them):

| Folder | Linux | Holds |
|---|---|---|
| data | `~/.local/share/chronika` | what cannot be made again: `archive.db`, the review state, the aside folder |
| cache | `~/.cache/chronika` | what can: the iPhone's extracts, the indexes, `telegram/` |
| config | `~/.config/chronika` | `config.toml`, and the secrets' files where there is no keyring |

The environment variables CHRONIKA_DATA, CHRONIKA_CACHE and CHRONIKA_CONFIG move each of them (the
demo and the tests use them to stay apart). The app adds `<data>/server.db` (users, passkeys,
sessions, push subscriptions, audit; mode 600), and `<cache>/thumbs/` and `<cache>/avatars/`.

```
<cache>/iphone/sms.db                     decrypted messages database
<cache>/iphone/CallHistory.storedata      decrypted call history database
<cache>/iphone/viber.sqlite               decrypted Viber database (the app's `Contacts.data`)
<cache>/iphone/whatsapp.sqlite            decrypted WhatsApp database (`ChatStorage.sqlite`)
<cache>/iphone/whatsapp-contacts.sqlite   WhatsApp's contacts (`ContactsV2.sqlite`)
<cache>/iphone/whatsapp-calls.sqlite      WhatsApp's call log (`CallHistory.sqlite`)
<cache>/iphone/viber-media/, whatsapp-media/   new media of messages, copied by each sync
<data>/media/<ab>/<sha256><ext>           the archive's media (config [media] store), until they go to the library
<cache>/telegram/telegram.db              Telegram's messages, as read by telegram-sync.py
```

(The archive's media are in the data folder, not the cache: some exist nowhere else once their source
is gone.)

### Running the scripts

Everything runs under uv, from the project folder. What a script needs beyond the archive is an
optional group (`pyproject.toml`), named with `--extra`:

| Extra | Brings | For |
|---|---|---|
| (none) | platformdirs, keyring, phonenumbers, tzlocal (and tzdata on Windows) | the importers, `immich-index.py`, `media-aside.py`, `media-prune.py`, `android-export.py` |
| `iphone` | iphone-backup-decrypt | `iphone-sync.py`, `iphone-ls.py`, `iphone-verify.py`, `media-restore.py` |
| `media` | Pillow, pillow-heif, imagehash, numpy | the review pages, `media-vlm.py`, `media-triage.py`, `immich-dupes.py`, `immich-phash.py`, `immich-upload.py` |
| `ml` | `media`, transformers, scipy, insightface, onnxruntime | `media-faces.py`, `media-face-groups.py`; with a torch extra, the embedding scripts |
| `torch` | torch from PyPI (CUDA on Linux, MPS on Apple, else the CPU) | `immich-match.py`, `immich-neighbours.py`, `media-tags.py`, `media-similar.py` |
| `rocm` | torch for AMD GPUs on Linux (PyTorch's ROCm 7.2 index) | the same, instead of `torch` |
| `telegram` | Telethon | `telegram-sync.py` |

For example `uv run --extra iphone python scripts/iphone-sync.py`, or
`uv run --extra ml --extra rocm python scripts/immich-match.py` on an AMD GPU. `torch` and `rocm`
exclude each other. The project is on Python 3.14 (`.python-version`): every package has wheels for
it, torch 2.14 included (PyPI and ROCm). Pictures are read with Pillow (pillow-heif for HEIC).
External programs, looked up on the PATH, with a clear message when one is missing: exiftool,
ffmpeg/ffprobe, libimobiledevice (`idevicebackup2`, `idevice_id`), adb.

### Secrets

The iPhone backup password (`backup-password`), the immich API key (`immich-key`), Telegram's
`telegram-api-id`, `telegram-api-hash` and `telegram-session`, and any later source's token are kept
in the system's keyring (service `chronika`): Secret Service on Linux (KWallet or GNOME Keyring),
the Keychain on macOS, the Credential Manager on Windows. Where there is none (a headless Linux
without a Secret Service, or a session without D-Bus, such as plain ssh), the file of that name in
the config folder (mode 600) is used instead. `config.secret(name)` reads either;
`config.save_secret()` writes to the keyring when it can.

- `iphone-sync.py --save-password`, `immich-upload.py --save-key` and
  `telegram-sync.py --save-credentials` ask for the secret and store it.
- `iphone-sync.py --move-to-keyring` and `immich-upload.py --move-to-keyring` copy an existing file
  into the keyring, check that it reads back the same, and then offer to remove the file (only on
  "y"). The keyring is read first, so a file left behind is no longer used.
- A secret file that others can read is refused (not checked on Windows, where the mode means
  nothing). Secrets are never taken as arguments and never printed.

### `config.toml`

TOML, optional: every key has a general default.

| Key | Default | Used for |
|---|---|---|
| `[owner] numbers` | none | the user's own numbers, left out of conversation members |
| `[owner] region` | none | the country of numbers written without a country code (ISO code, e.g. `GR`) |
| `[owner] timezone` | the system's (TZ, also as `:/path`; the `/etc/localtime` link; `/etc/timezone`; tzlocal, which also reads Windows' setting; else today's offset) | dates in file names, typed dates, carrier notices, calls, the pages; a name that is not a zone is reported and the next is tried |
| `[iphone] udid` | the only backup, else the only phone on the cable | `iphone-sync.py`, `iphone-ls.py`, `iphone-verify.py`, `media-restore.py` |
| `[iphone] device` | `iphone` | the device name in the iPhone's source names (`iphone/sms`) |
| `[iphone] backup_root` | `<data>/iphone-backup` | where `idevicebackup2` writes |
| `[android] export` | `<data>/android` | `android-export.py`'s folder: `<device>/android.db` and `mms-parts/` per phone |
| `[android] device` | `android` | the name of a legacy export `<export>/<device>.db` and of its sources |
| `[import] importers` | all, in the registry's order | which importers `python -m chronika` runs by default |
| `[import] carrier_notices` | none | the parsers of carriers' missed-call SMS to use (`gr`: the Greek one) |
| `[viber] desktop_export` | none | a decrypted Viber Desktop database |
| `[whatsapp] bridge` | none | the WhatsApp bridge's store folder (`messages.db`, `whatsapp.db`) |
| `[telegram] media`, `no_media` | true, none | `telegram-sync.py --media`: false downloads nothing; `no_media` lists chat ids whose media are passed over |
| `[immich] url` | none | the immich API: `immich-index.py`, `media-faces.py`, `immich-review.py`'s large previews, `immich-upload.py` |
| `[immich] data_folder` | none | immich's upload folder, only for `--dump` (its nightly backups and previews) |
| `[immich] container_prefix` | `/usr/src/app/upload/` | the same folder as immich's own paths name it |
| `[immich] make` | `chronika` | the camera make written into uploaded files that have none |
| `[ollama] url`, `model` | `http://localhost:11434`, `qwen2.5vl:7b` | `media-vlm.py`'s local vision model, also the one the pages show first |
| `[media] store` | `<data>` | where the archive's media files are (`media/<ab>/...`) |
| `[server] origin`, `host`, `port` | `http://localhost:8520`, `127.0.0.1`, 8520 | the app's address (passkeys are tied to it) and where it listens |
| `[media] aside` | `<data>/aside` | `media-aside.py` and `media-triage.py` (hard links, or copies on another file system) |
| `[review] person`, `me` | none, `εγώ` | the one person with a filter of their own on `immich-review.py`, and the user's own face label |
| `[review] dog_tag`, `dog_threshold` | none, 0.0005 | `media-tags.py`'s label for a dog on `vlm-review.py` |
| `[review] reference_model` | `claude-sonnet` | the model in `vlm.db` whose answers `vlm-review.py` compares the others against |

Contact names are meant to come from the user's address book (CardDAV), matched by number.

## Sources

### iPhone: the encrypted backup

Tools: `libimobiledevice` (`idevicebackup2`, `idevicepair`, `ideviceinfo`), with `usbmuxd`, which
starts when the phone is plugged in. Backups are made over the cable.

```
idevicepair validate                                   # pairing with this machine
ideviceinfo -q com.apple.mobile.backup                 # WillEncrypt must be true
idevicebackup2 backup <backup_root>                    # incremental: only what changed
```

`--full` forces a complete backup; without it only changed files are sent. The phone may ask for
its passcode to start.

**Why the backup must be encrypted.** iOS decides what goes into a backup by whether it is
encrypted. Only encrypted backups contain the **call history**, the keychain (saved passwords,
Wi-Fi), Health data and Safari history; SMS are in both. An unencrypted backup would lose the
calls, and they cannot be added afterwards. So encryption stays on, with a password the user keeps.

The backup password is a setting of the phone, not of each backup: every new backup uses it, and
it cannot be changed or turned off without the old one. If it is forgotten, the only way out is
`Settings > General > Transfer or Reset iPhone > Reset > Reset All Settings`, which clears it
without deleting data (apps and their data, messages, photos stay; Wi-Fi passwords, home screen
layout, app permissions and notification settings go back to defaults); a new one is then set in
Finder or iTunes ("Encrypt local backup") or by `idevicebackup2 encryption on`.

**Backing up and extracting.**

```
uv run --extra iphone python scripts/iphone-sync.py [-o DIR] [--no-backup] [--full]
```

`scripts/iphone-sync.py` does both steps in one run. It takes the backup password from the keyring
(or the secret file), or, if neither has it, asks for it first (hidden input, any number of tries;
each try takes a few seconds by design). Either way it checks it against the existing backup before
anything else, so the rest runs unattended. Then it runs the incremental `idevicebackup2 backup` and
writes `sms.db`, `CallHistory.storedata`, `viber.sqlite`, `whatsapp.sqlite` and the WhatsApp
contacts and call log into `DIR` (default `<cache>/iphone`, mode 700, files 600). It also copies
media incrementally: Viber's `Documents/Attachments`, `FileMessages` and `VoiceMessages` into
`DIR/viber-media/` (files named by attachment id, `ZATTACHMENT.ZNAME`), and WhatsApp's
`Message/Media` into `DIR/whatsapp-media/` (the path in `ZWAMEDIAITEM.ZMEDIALOCALPATH` without its
leading `Media/`). These files never change once written, so only new ones are decrypted; files the
archive has already taken (`attachment.source_path`, asked read only) are not copied again, so
nothing removed comes back, while media that arrive late for old messages are still picked up.

- `--save-password` asks for the password, checks it against the backup and stores it.
- `--no-backup` only decrypts the backup already on disk; `--full` forces a complete backup.
- If the backup fails, nothing in `DIR` is touched. Each file is written to `NAME.part` and then
  renamed over the old one, and stale `-wal`/`-shm` files of the old copy are removed.
- A wrong password gives `IncorrectPassphraseError` and is reported as such; any other error is
  reported separately.

**Looking inside.** `scripts/iphone-ls.py DOMAIN_LIKE [PATH_LIKE] [--depth N]` lists what the
backup holds (file count and size per domain and folder) without extracting anything, e.g.
`iphone-ls.py '%viber%' --depth 2`.

**Verifying.** `scripts/iphone-verify.py` decrypts every file listed in the backup's `Manifest.db`
and compares its size with the size recorded there. It only reads. SQLite databases (and WebKit
storage, caches) that changed while iOS copied them routinely differ in whole pages (4,096 bytes,
2,048 for Chromium's `Web Data`); the script reports mismatches of whole 4,096-byte pages as
"databases that changed during the backup".

**Restore** (not tested; it can only be done by overwriting a phone). From Linux, over the cable:

1. On the iPhone turn off `Settings > [name] > Find My > Find My iPhone` (required by iOS).
2. Connect and trust the computer.
3. `idevicebackup2 restore --system --settings --reboot -i <backup_root>` (`-i` asks for the
   backup password).
4. The phone restarts and restores data and settings; apps download again from the App Store.

For a new or erased phone, choose "From Mac or PC" on the "Apps & Data" screen during setup, then
run the command. The phone needs the same or a newer iOS than the backup. The backup is in the
format Finder uses, so Finder's "Restore Backup…" works too, from
`~/Library/Application Support/MobileSync/Backup/`.

What an iPhone backup holds and what it does not: messages and calls carried over from earlier
phones (iOS keeps old calls, but a move brings only the months the old phone still had); Viber
only since it was activated on the iPhone (Viber does not transfer between Android and iOS);
WhatsApp's whole message history, but its media only as far as the phone still has them.
Telegram and Messenger are not in it.

### Android: over adb

`scripts/android-export.py` reads the call log, SMS, MMS (with their parts' files) and blocked
numbers through `adb shell content query`, read only, into `<export>/<device>/android.db` and
`mms-parts/`. adb needs developer options and USB debugging on, USB mode "File transfer", and the
authorisation prompt accepted on the phone.

```
adb devices -l
adb shell content query --uri content://call_log/calls
adb shell content query --uri content://sms
```

WhatsApp on Android keeps its database encrypted (`msgstore.db.crypt15`); it is not read yet.

### Viber: through Viber Desktop (Linux)

Viber keeps no message content on its servers and offers no export; its Google Drive backup can
only be opened by the Android app. The history of an Android phone can be had by linking Viber
Desktop to it (a fresh profile) and letting it sync.

Viber Desktop's `~/.ViberPC/<number>/viber.db` is encrypted (`PRAGMA hexkey`, with a key that is
transformed internally, so it does not open with SQLCipher). `scripts/viber-desktop-export.cpp` is
an `LD_PRELOAD` library that waits for Viber's own `PRAGMA hexkey` and then, through Viber's open
connection, attaches a plain database and copies every table into it (`CREATE TABLE ... AS SELECT`,
with the original schema in `_schema`). Linux only. Build against Viber's bundled Qt (6.10) with the
system Qt headers, and start Viber with it:

```
g++ -shared -fPIC -O1 -std=c++17 -DQT_NO_VERSION_TAGGING -o /tmp/viber-export.so \
    scripts/viber-desktop-export.cpp -I/usr/include/qt6 -I/usr/include/qt6/QtCore \
    -I/usr/include/qt6/QtSql -L/opt/viber/lib -Wl,-rpath,/opt/viber/lib -lQt6Core -lQt6Sql -ldl
VIBER_PLAIN_OUT=/path/plain.db LD_PRELOAD=/tmp/viber-export.so /opt/viber/Viber
```

(Viber must not already be running; it ignores SIGTERM, and a second start hands over to the first.)
It writes a log to `/tmp/vb/export.log`. Tables `Events` (`TimeStamp` in ms, `Direction` 0 in / 1
out, `ChatID`, `ContactID`), `Messages` (by `EventID`), `ChatInfo`, `Contact`. Media and calls are
not synced to the desktop (`Calls` is empty); on Android the media are in
`Android/data/com.viber.voip` (readable with `adb pull`).

The message token, `Events.Token` on the desktop and `ZVIBERMESSAGE.ZTOKEN` on the iPhone, is the
same id on both, and the deduplication key. A token carries its send time
(`(token >> 22) + 292057776050` is Unix ms, within about 2 s). Media files from Android
(`IMG-<hex>-V.jpg`, `video-<hex>-V.mp4`, `<hex>.vptt`) are named by `md5(DownloadID)`, the server's
media id, kept in `DownloadFile.DownloadID` on the desktop and `ZATTACHMENT.ZID` on the iPhone.
Size alone is not a safe key: Android keeps downscaled copies, and different pictures often share
a size by chance; `FileHash` and `EncParams` concern the encrypted upload and cannot be checked
against the files.

### Telegram: through its API

`scripts/telegram-sync.py` (`--extra telegram`) reads the user's own account through the Telegram
API (Telethon). The API was chosen over Telegram Desktop's JSON export, which takes the same history
but by hand each time; the API works for any user and later brings only what is new.

- `--save-credentials`: api_id and api_hash from my.telegram.org, as secrets.
- `--login`: the code arrives inside Telegram, not by SMS; the session, full access to the account,
  is a Telethon `StringSession` kept as a secret.
- `--survey`: every chat with its kind, size and dates, no content, in `<cache>/telegram/survey.tsv`.
- With no option: every chat but channels and bots into `<cache>/telegram/telegram.db`, each message
  whole (Telethon's fields as JSON); later runs bring only what is new.
- `--media [--dry-run]`: pictures, videos, GIFs, video notes and voice messages (`[telegram] media`,
  `no_media`).

Read only: nothing is sent, nothing is marked read. Secret chats are on the devices only and cannot
be had.

### WhatsApp: through a live bridge

Chronika's whatsmeow bridge (`bridges/whatsapp/`, begun from whatsapp-mcp's) keeps what arrives
after it is linked, files included, in its store folder (`messages.db`, `media/`). The importer takes from it what came after the last iPhone backup, matched by
stanza id; LIDs (`...@lid`) are mapped to numbers through the iPhone's WhatsApp contacts and the
bridge's `whatsmeow_lid_map`.

### Facebook Messenger

Not in the iPhone backup (the app excludes its messages). Meta's "Download your information" export
(JSON); since late 2023 personal chats are end-to-end encrypted and stay on the server only with
"secure storage" (a PIN), and are believed to be downloadable from Messenger's own settings. Not
verified yet.

## The archive

`<data>/archive.db` (mode 600) holds every message and call once, whatever its source, and is only
ever added to. Filled by the `chronika` package:

```
uv run python -m chronika sms calls viber whatsapp telegram voip media   # what is already there is skipped
```

Schema (`chronika/archive.py`):

- `message` and `call`: our own `id`; `service_id`, `ts` (Unix ms, UTC), `outgoing`; for messages
  `conversation_id`, `sender_id` (an `address`, NULL when outgoing), `kind_id`, `text`, and `key`,
  the service's own id (Viber token, WhatsApp stanza id, iMessage guid, Telegram message id; NULL
  for SMS/MMS), unique per service or, where a service's ids are unique only per chat
  (`key_scope`), per conversation; for calls `address_id`, `answered`, `duration` (s).
- What the services add to a message (`chronika/extras.py`, per source): `subtype` (link, pin,
  gif, video note, deleted, notice, poll, call, group event...), `reply_to` (the quoted message,
  resolved after import from `reply_key`; `reply_text` keeps the quoted text), `edited`, `deleted`,
  `forwarded`, `starred`, `lat`/`lon`/`place` (a shared location, or on Viber messages of other
  kinds the sender's position). `reaction`: emoji (Viber codes 1-5 as ❤️😂😮😢😡, others `viber:N`),
  count, who, and whether it was the user's own; iMessage tapbacks become reactions.
- Calls also carry `detail` (missed, unanswered, rejected, blocked, busy, and WhatsApp's outcomes 4
  and 5 by code), `video`, `attempts`, `conversation_id` (group calls) and, in `call_member`, who
  took part and how.
- `message_origin` / `call_origin`: where each row came from: `source_id` and `row_key` (the row's
  id in that source, unique per source). The source row itself is not kept: what matters is in
  columns. The extracted source databases are the way back to a source row.
- `service`, `address_kind`, `message_kind`, `source`: lookup tables; `address` (normalised: E.164
  numbers, lower-case email, a service's id, username or profile name); `person` joins addresses
  (`Archive.alias` adds a handle to an existing person); `conversation` and `conversation_member`.
- `message_fts`: FTS5 over the text folded (`chronika/text.py`: lower case, no accents of any script,
  final sigma as sigma), so `καλημερα` finds `Καλημέρα`; written by the archive as it adds a message.
- The app's tables: `plugin_instance` (every source, library and address book in use, with its
  settings and state; `source.instance_id`, `library_link.instance_id` point to it), `contact` and
  `contact_address` (an address book's people, joined to the addresses they list), `handle_name`
  (every name a service has shown for a handle, of a kind, with when it was seen), `state_report`
  (what each source says about a chat: hidden, muted, pinned, read up to) and `chat_state` (what the
  user chose), `setting` (the user's, shared by every device), `media_decision` (keep, remove, to the
  library; the newest counts), `message.status` (messages sent from the app). Until the first
  release the schema changes in place, without migrations.

Deduplication. Rows found in more than one source are paired one to one and kept once, with every
origin recorded; where the sources differ, the copy of the device in use at the time wins
(`device.used_from`/`used_until`).

- SMS and MMS (`chronika/sms.py`): phones carry the same history, copied from phone to phone.
  Pairs: same direction and text, at most 2 s apart. SMS and MMS are paired together, because
  Android keeps many plain texts as MMS (long ones, or with a link) that the iPhone has as SMS; they
  share one conversation per counterpart. Exact repeats (same second and text, different guid) are
  taken once. On newer iOS many messages have `text` NULL and the text only in `attributedBody`,
  decoded in `chronika/sms.py`.
- Calls (`chronika/calls.py`): same number, direction and second.
- Viber (`chronika/viber.py`): the iPhone's `viber.sqlite` and a Viber Desktop export, matched by
  token. People are matched by member id and stored by phone number where either source knows it,
  so they meet their SMS and calls; groups by group token. The token's time fills in a missing
  date.
- WhatsApp (`chronika/whatsapp.py`): the iPhone's `ChatStorage.sqlite`, then the bridge, matched by
  stanza id. Message types are identified from their files (`file`, `ffprobe`) and metadata (GIFs
  are silent mp4s; round video notes; deleted messages); some business messages have their text
  only in the media item's protobuf metadata, which is extracted.
- Telegram (`chronika/telegram.py`): ids unique per chat (`key_scope`), the row key `<chat>/<id>`.
  People by phone where Telegram shows it, else by user id, with the id, username and profile name
  as further handles of the same person; deleted accounts have neither name nor number. Calls,
  which Telegram keeps as service messages, go to `call` too.
- Calls of the apps and the carrier (`chronika/voip.py`, importer `voip`): WhatsApp's call log
  merged with the call bubbles in its chats (type 59), Viber's recents (`ZRECENT`), and the
  carriers' missed-call SMS notices read from the archive's own SMS (`chronika/carriers/`, one module
  per carrier or country, chosen in `[import] carrier_notices`). A call already there from another
  source (same service, person and direction) within 60 s is not added again; calls of the same
  source are never merged.
- Media (`chronika/media.py`): each file is stored once by content, hard-linked (no extra space)
  as `media/<ab>/<sha256><ext>`; `media` (sha256, size, mime, path) and `attachment` (message,
  sha256, source, the file's path in that source). Links: iPhone WhatsApp by stanza id
  (`ZWAMEDIAITEM.ZMEDIALOCALPATH`), iPhone Viber by token (`ZATTACHMENT.ZNAME`), Android Viber by
  event id, Android MMS by MMS id, Telegram by message.
- Channels are never imported: Viber chats of type 3 (`ZCONVERSATION.ZSUBTYPE`, desktop
  `ChatInfo.PGType`), WhatsApp `...@newsletter` and `status@broadcast`, Telegram channels; Telegram
  bots neither. A service's notes-to-self chat (Viber's, Telegram's Saved Messages) is imported like
  any other.

Apple timestamps count from 2001-01-01 UTC (add 978307200 for Unix time): seconds for calls,
nanoseconds for messages. The iPhone databases' tables are described in `docs/data-sources.md`.

## Media and the photo library

The archive does not keep pictures. Each `media` row stays as the record of what a message
carried; the file itself either goes to the photo library, and `library_link` records where
(`library`, `asset_id`, how it was matched), or is removed. In the library a picture carries its
real capture date, never the (later) date of the message. Nothing goes into the library without
the user's approval.

Dates, in this order of authority: the file's own EXIF date (always right; such files are not even
shown for dating); the date of a library picture of the same occasion; the message date; a date
the user typed. Video notes take the message date. Exact copies (same picture, other resolution)
are handled silently, as links to the copy kept; only real choices are put to the user. Deletion
happens only on the user's word: review pages only mark, and the scripts carry it out afterwards.

The tools below work with immich and local models (nothing leaves the machine). They were written
for one archive's backlog and are not the core's design (see "Plans").

**immich permissions.** One API key serves all the scripts. It needs: `asset.read` (the index,
`POST /search/metadata`, and reading back an upload), `asset.view` (previews:
`GET /assets/{id}/thumbnail`), `person.read` (`GET /people`) and `face.read` (`GET /faces`) for
`media-faces.py`, and `asset.upload` for `immich-upload.py`. Nothing else: nothing here changes
or deletes in immich. Without `asset.view` the index has no previews; without the face permissions
`media-faces.py` works only with `--dump`.

- `scripts/immich-index.py`: an index of the immich library (read only) in `<cache>/immich.db`:
  checksum, dates, camera, size, a preview and a CLIP embedding. Through the API by default
  (assets, and small previews into `<cache>/immich-thumbs/`, skipping those immich has none of;
  the embeddings are then made by `immich-match.py`); `--dump` reads immich's nightly database
  backup instead, with immich's own previews and embeddings (model ViT-B-16-SigLIP2).
- `scripts/immich-match.py`: each chat picture against immich: SHA-1, and the most similar asset
  with the same model (google/siglip2-base-patch16-224; the same picture scores 0.90-0.99,
  different ones about 0.5); EXIF capture dates in their own time zone.
- `scripts/immich-dupes.py`: all against all by perceptual hash, like a duplicate finder: pHash of
  all immich previews and chat pictures, nearest match, and groups of chat pictures that are copies
  of each other. A pHash match is confirmed by the same aspect ratio and a second hash (dHash),
  since a pHash of 6 can be chance.
- `scripts/immich-review.py`: a page (port 8519) with each candidate next to its immich match,
  similarity, dates and sizes; approve or reject (decisions in `<data>/review.db`). A date given, or
  an approval, turns an earlier "delete" of the same file into "keep". The large view closes only
  with ✕ or Esc.
- `scripts/media-vlm.py` and `scripts/vlm-review.py` (port 8518): local vision models through
  Ollama (e.g. qwen2.5vl:7b, gemma3:12b, llama3.2-vision:11b; the first is config `[ollama]
  model`, compared on the page against `[review] reference_model`) say what each picture is and
  its value for a personal library. `vlm-review.py` records keep, aside or delete per file; modes:
  default (by kind), `--kept`, `--similar` (look-alike groups), `--filtered` (what the cheap
  filters took out), `--aside` (a set-aside label; its decisions go to their own table and the page
  cannot apply them).
- Apply on `vlm-review.py`: the first click shows the exact list from a dry run of
  `media-triage.py` (what would be deleted and set aside, from every view); the second click,
  within 30 seconds and with nothing changed, carries out that list and no more.
- `scripts/media-triage.py`: carries out the decisions (`triage`, or `--table aside_decision`):
  delete through `media-prune.py`, set aside, keep. The newest decision about a file wins, across
  every table: a delete is carried out only when nothing about the file came after it (a tie also
  stops it), and those stopped are listed. `--dry-run --plan FILE` writes the exact list; `--only
  FILE` carries out no more than such a list, every check made again.
- `scripts/media-aside.py LABEL CONVERSATION_ID...`: sets a conversation's media aside for later,
  as hard links (copies across file systems) in the aside folder by kind and date, and off the
  review pages.
- `scripts/media-prune.py`: for files in immich, writes `library_link` and then removes every
  local copy (archive and sources, all checked to be the same content); with asset `-` it only
  removes. Extra columns of a line are its links in the aside folder, removed only with the file;
  it refuses a file another one is linked to as the copy kept (`media_same.same_as`); `--done FILE`
  lists what it removed.
- `scripts/media-similar.py`: look-alike groups among the kept files, for `--similar`; a removed
  look-alike is linked to the closest one kept (`media_same`).
- `scripts/immich-neighbours.py`: the five immich pictures most like each undated kept picture, so
  the user can give it the date of the same occasion.
- `scripts/media-faces.py`, `scripts/media-face-groups.py`, `scripts/media-tags.py`: who is in a
  picture (faces immich knows by name; groups of unnamed faces the user names) and what it shows
  (zero-shot tags such as a dog), for the pages' filters (`<cache>/faces.db`).
- `scripts/media-restore.py`: brings back a removed file from the encrypted iPhone backup, and
  records the restore (`restored` in `review.db`), so that an earlier "delete" no longer applies.
- `scripts/immich-upload.py`: uploads the kept files. Each goes up as a copy carrying its chosen
  date (EXIF left as it is), the camera make `[immich] make` with the service as the model (a file
  with a camera of its own keeps it: the make is there because something has to be, and it is how
  chat media are told apart in the library), the name `<service> <date> <time><ext>`, and the
  original's sha256 in XMP `dc:identifier`. A file whose date is unknown or not chosen stops the
  plan, nothing uploaded; a date the script wrote is read back from immich and must agree (within a
  second), else it stops before writing `library_link` (method `upload`), which is also what a
  re-run skips. immich names files by capture time; a second file of the same second gets "+1".

The local pages listen on 127.0.0.1 only and answer only requests addressed to 127.0.0.1 or
localhost (no DNS rebinding). Each run prints its own address, `http://127.0.0.1:PORT/?k=KEY`:
open that one, not the bare port. The key becomes a cookie (HttpOnly, SameSite=Strict, one per
port) that every request needs, GETs included, so another local user cannot use the pages; a change
also needs the token the page carries. The pages escape every text they show and send a
Content-Security-Policy that runs only their own scripts.

## Principles

- **The archive is read from the phones, never written back.** Reads only: `adb shell content
  query`, `idevicebackup2 backup`. Putting old calls into an iPhone's own history would mean editing
  its backup and restoring the whole phone: unsupported and risky.
- **The backup stays encrypted**: the call history exists only in encrypted backups.
- **Decrypted data stays private on disk**: extracts are 700/600, and scratch extractions are
  deleted once they have served.
- **Only what matters, in columns.** Anything worth keeping from a source goes into a column, not a
  blob.
- **No channels.** Channels and status updates are not the user's conversations.
- **EXIF dates are authoritative**, and exact copies are not put to the user.
- **Deletion only on the user's word**, with the exact list.
- **Private pictures are judged with local models.**
- **General.** Nothing tied to one user's data or setup: any Linux, and macOS and Windows where the
  tools and libraries exist; values of one installation go in configuration.

## Plans

Built (October 2026), as `docs/design.md` describes: the core, plugins as instances (sources,
libraries, contacts), the app (passkeys, the messenger across services, search, media,
people and their merging, sources with per-chat choice, devices, settings, push, PWA for desktop
and phone, light and dark, Greek and English), live Telegram and WhatsApp with sending, the MCP
server with media on demand, the demo archive, tests (core, server, MCP; end to end on desktop and
mobile). Next:

1. **Sources still missing**: the iPhone's SMS/iMessage attachments (`MediaDomain`
   `Library/SMS/Attachments`), the WhatsApp bridge's media (`/api/download`), Android WhatsApp (its
   encrypted backup), Messenger (Meta's export), a native Android companion for live SMS and calls.
2. **Writes through the core**: today's importers become sources that yield records, the core's
   pipeline storing and deduplicating them (now they write through `Archive`, wrapped as plugins).
3. **The importers' and the command line's words** in both languages (they are Greek; the app, the server's
   messages and the plugins are bilingual, and `tests/test_i18n.py` lists what is left).
4. **Native wrappers** (Capacitor, Tauri) if notification replies or sharing into the app on an
   iPhone are wanted.
5. **Several users** on one server: the auth database and the core already take the archive per
   user; what is left is the UI to add a user and the per-user plugin host.
