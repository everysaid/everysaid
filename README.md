# Everysaid

A personal archive of messages and calls: SMS and iMessage, phone and FaceTime calls, Viber,
WhatsApp, Telegram, Signal, the logs of older messengers, with their media, in one SQLite database.
It is built from local phone backups (iPhone), Android phones over adb, and the services' own APIs
and exports. Over it: a core, an app for a person (a messenger for the whole history, on desktop
and phone, `everysaid serve`) and an MCP server for an assistant (`everysaid mcp`). `docs/app.md`
tells how to run them, `docs/design.md` how they are built.

The goal is a complete, permanent history that does not depend on what the phones keep: a message
stays in the archive after the phone that held it is gone, deleted or replaced. Nothing goes
through iCloud. Telegram, WhatsApp, Signal and Viber (through Viber Desktop) can also be live: new
messages arrive in the app as they come, and can be answered from it.

Backups are not the project's job. Keeping the archive itself safe (and the phone backups it reads,
and the photo library) is the user's concern, with whatever backup they use. What Everysaid owes is
not to lose anything by its own doing (writes in transactions, nothing removed because the source
dropped it) and to keep what cannot be made again in its data folder, never in the cache, which
backups skip by convention.

`docs/data-sources.md` is the technical reference for how each source is read, refreshed and
unlocked. A `LOCAL.md`, not tracked, may hold notes about one installation (devices, paths,
history); nothing in the tracked documentation depends on it.

## What works

| Part | State |
|---|---|
| Encrypted iPhone backup on Linux, incremental refresh | working (`everysaid iphone-sync`) |
| Extraction of `sms.db`, `CallHistory.storedata`, Viber and WhatsApp (databases and media) | working (same command) |
| Integrity check of an iPhone backup | working (`everysaid iphone-verify`) |
| Looking inside a backup without extracting | working (`everysaid iphone-ls`) |
| Android call log, SMS, MMS and blocked numbers over adb | working (`everysaid android-export`) |
| Live Viber through Viber Desktop (Linux): history, arriving, sending | working (`bridges/viber/`, `internal/viber`) |
| Telegram through its API | working (`everysaid telegram-sync`) |
| WhatsApp through a whatsmeow client linked as a device (the `whatsapp-bridge` source) | working (`internal/whatsapp`) |
| Signal through a helper linked as a device | working (`bridges/signal/`, `internal/signal`) |
| Unified archive, deduplicated across sources | working (`everysaid import`, `internal/importers`) |
| Replies, reactions, edits, locations, call outcomes | working (`internal/importers/extras.go`) |
| WhatsApp and Viber calls, carrier missed-call notices | working (`internal/importers/voip.go`) |
| Chat media matched against a photo library (immich) and uploaded with the right date | separate tools, not part of the app (see "Media") |
| The core: chats, a person's stream across services, search ignoring accents, people, calls, media, statistics | working (`internal/core/`) |
| Plugins: sources, photo libraries (folder, immich), contacts (CardDAV, .vcf), as instances | working (`internal/plugins/`) |
| The app: passkeys, the messenger, search, media, people, sources, settings, push, PWA | working (`everysaid serve`, `internal/server`, `web/`) |
| Live Telegram, WhatsApp (bridge), Signal and Viber Desktop, sending where the plugin can | working |
| Reactions, edits and deletions for everyone from the app (WhatsApp, Telegram, Signal, Viber) | working |
| Strangers removed as spam (reported and blocked on Telegram, blocked on WhatsApp); those blocked on a phone or a service suggested | working (`internal/core/spam.go`) |
| The MCP server | working (`everysaid mcp`) |
| A demo archive of invented people | working (`everysaid demo`) |
| The logs of Adium and Pidgin (Gaim): MSN, ICQ, AIM, Yahoo, Jabber/Google Talk, Skype, IRC, Facebook chat | working (`internal/importers/imlogs.go`, the "Adium and Pidgin logs" source) |
| Labels (the tone of a chat, who someone is), the user's own lists; names read from emails and user names | working (`internal/core/labels.go`) |
| Local analysis: models on this computer (Ollama) suggest names and labels for people without a name | working (`internal/plugins/analysis`) |

Everysaid is one program, `everysaid`, written in Go; `docs/go.md` tells how to build and run it.

## Folders and configuration

`internal/config` is the one place for paths and settings. The folders follow the platform (on
Linux the XDG folders, so `XDG_DATA_HOME` and the like move them):

| Folder | Linux | Holds |
|---|---|---|
| data | `~/.local/share/everysaid` | what cannot be made again: `archive.db`, `server.db`, the archive's media, the iPhone backups, the Android exports, the WhatsApp and Signal stores |
| cache | `~/.cache/everysaid` | what can: the iPhone's extracts, `telegram/`, thumbnails, avatars |
| config | `~/.config/everysaid` | `config.toml`, and the secrets' files where there is no keyring |
| state | `~/.local/state/everysaid` | `logs/`: the server's and each plugin's |

The environment variables EVERYSAID_DATA, EVERYSAID_CACHE, EVERYSAID_CONFIG and EVERYSAID_STATE
move each of them (the demo and the tests use them to stay apart). `<data>/server.db` is the app's
own (users, passkeys, sessions, push subscriptions, audit; mode 600).

```
<cache>/iphone/sms.db                     decrypted messages database
<cache>/iphone/CallHistory.storedata      decrypted call history database
<cache>/iphone/viber.sqlite               decrypted Viber database (the app's `Contacts.data`)
<cache>/iphone/whatsapp.sqlite            decrypted WhatsApp database (`ChatStorage.sqlite`)
<cache>/iphone/whatsapp-contacts.sqlite   WhatsApp's contacts (`ContactsV2.sqlite`)
<cache>/iphone/whatsapp-calls.sqlite      WhatsApp's call log (`CallHistory.sqlite`)
<cache>/iphone/viber-media/, whatsapp-media/   new media of messages, copied by each sync
<data>/media/<ab>/<sha256><ext>           the archive's media (config [media] store), until they go to the library
<cache>/telegram/telegram.db              Telegram's messages, as read by telegram-sync
<cache>/telegram/media/                   their files (telegram-sync --media)
<data>/whatsapp-bridge/                   the WhatsApp source's store (default; [whatsapp] bridge)
```

(The archive's media are in the data folder, not the cache: some exist nowhere else once their source
is gone.)

### Running

Everything is a subcommand of the one binary (`everysaid -h` lists them): `serve`, `mcp`, `import`,
`demo`, `user`, and the extraction the sources run, which can also be run by hand:
`iphone-sync`, `iphone-ls`, `iphone-verify`, `android-export`, `telegram-sync`. External programs,
looked up on the PATH, with a clear message when one is missing: exiftool, ffmpeg,
libimobiledevice (`idevicebackup2`, `idevice_id`), adb.

### Secrets

The iPhone backup password (`backup-password`), the immich API key (`immich-key`), Telegram's
`telegram-api-id`, `telegram-api-hash` and `telegram-session-go`, and any other source's token are kept
in the system's keyring (service `everysaid`): Secret Service on Linux (KWallet or GNOME Keyring),
the Keychain on macOS, the Credential Manager on Windows. Where there is none (a headless Linux
without a Secret Service, or a session without D-Bus, such as plain ssh), the file of that name in
the config folder (mode 600) is used instead. `config.Secret(name)` reads either;
`config.SaveSecret` writes to the keyring when it can.

- `everysaid iphone-sync --save-password` and `everysaid telegram-sync --save-credentials` ask for
  the secret and store it; an immich library takes its API key in its settings, else the secret
  `immich-key`.
- `everysaid iphone-sync --move-to-keyring` copies an existing file into the keyring, checks that it
  reads back the same, and then offers to remove the file (only on "y"). The keyring is read first,
  so a file left behind is no longer used.
- A secret file that others can read is refused (not checked on Windows, where the mode means
  nothing). Secrets are never taken as arguments and never printed.

### `config.toml`

TOML, optional: every key has a general default.

| Key | Default | Used for |
|---|---|---|
| `[owner] numbers` | none | the user's own numbers, left out of conversation members |
| `[owner] region` | none | the country of numbers written without a country code (ISO code, e.g. `GR`) |
| `[owner] name` | none | the user's own name, left out of the names found for others |
| `[owner] timezone` | the system's (TZ, also as `:/path`; the `/etc/localtime` link; `/etc/timezone`; else the process's local time) | dates in file names, typed dates, carrier notices, calls; a name that is not a zone is reported and the next is tried |
| `[iphone] udid` | the only backup, else the only phone on the cable | `iphone-sync`, `iphone-ls`, `iphone-verify` |
| `[iphone] device` | `iphone` | the device name in the iPhone's source names (`iphone/sms`) |
| `[iphone] backup_root` | `<data>/iphone-backup` | where `idevicebackup2` writes |
| `[android] export` | `<data>/android` | `android-export`'s folder: `<device>/android.db` and `mms-parts/` per phone |
| `[android] device` | `android` | the device name of an export kept as one file, `<export>/<device>.db`, and of its sources |
| `[import] importers` | all, in the registry's order | which importers `everysaid import` runs by default |
| `[import] carrier_notices` | none | the parsers of carriers' missed-call SMS to use (`gr`: the Greek one) |
| `[viber] desktop_export` | none | a decrypted copy of Viber Desktop's database, imported where there is one |
| `[whatsapp] bridge` | none (the source uses `<data>/whatsapp-bridge`) | the WhatsApp store folder (`messages.db`, `whatsapp.db`, `media/`); `everysaid import whatsapp` reads it only when set |
| `[whatsapp] send`, `download` | false, true | whether the WhatsApp source may send at all (its own setting is a second key), and whether it downloads the files of messages as they arrive |
| `[whatsapp] send_per_minute`, `send_per_hour`, `send_per_day`, `send_same_text` | 6, 60, 300, 3 | the limits on sending |
| `[imlogs] adium`, `pidgin` | none | the folders of Adium (`Adium 2.0`, `Users/Default` or `Logs`) and of Pidgin (`.purple` or its `logs`), for the "Adium and Pidgin logs" source |
| `[telegram] media`, `no_media` | true, none | `telegram-sync --media`: false downloads nothing; `no_media` lists chat ids whose media are passed over |
| `[immich] url` | none | the address an immich library is offered with |
| `[immich] make` | `Everysaid` | the camera make written into files sent to a library that have none |
| `[media] store` | `<data>` | where the archive's media files are (`media/<ab>/...`) |
| `[server] origin`, `host`, `port` | `http://localhost:8520`, `127.0.0.1`, 8520 | the app's address (passkeys are tied to it) and where it listens |
| `[server] contact` | `everysaid@localhost` | the contact address given to push services |

The separate media tools (see "Media") read further keys of their own from the same file.

Address books (CardDAV, .vcf) are one provider of names, matched to the archive's people by their
numbers and emails; the names themselves live in the archive.

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
everysaid iphone-sync [-o DIR] [--no-backup] [--full] [--udid UDID] [--backup-root DIR]
```

`everysaid iphone-sync` does both steps in one run (the iPhone source runs the same). It takes the backup password from the keyring
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

- `--save-password` asks for the password, checks it against the backup and stores it;
  `--password-stdin` reads it from the standard input instead of asking.
- `--no-backup` only decrypts the backup already on disk; `--full` forces a complete backup.
- If the backup fails, nothing in `DIR` is touched. Each file is written to `NAME.part` and then
  renamed over the old one, and stale `-wal`/`-shm` files of the old copy are removed.
- A wrong password is reported as such; any other error is reported separately.

**Looking inside.** `everysaid iphone-ls DOMAIN_LIKE [PATH_LIKE] [--depth N]` lists what the
backup holds (file count and size per domain and folder) without extracting anything, e.g.
`everysaid iphone-ls '%viber%' --depth 2`.

**Verifying.** `everysaid iphone-verify` decrypts every file listed in the backup's `Manifest.db`
and compares its size with the size recorded there. It only reads. SQLite databases (and WebKit
storage, caches) that changed while iOS copied them routinely differ in whole pages (4,096 bytes,
2,048 for Chromium's `Web Data`); the command reports mismatches of whole 4,096-byte pages as
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

`everysaid android-export` (or the Android source) reads the call log, SMS, MMS (with their parts' files) and blocked
numbers through `adb shell content query`, read only, into `<export>/<device>/android.db` and
`mms-parts/`. adb needs developer options and USB debugging on, USB mode "File transfer", and the
authorisation prompt accepted on the phone.

```
adb devices -l
adb shell content query --uri content://call_log/calls
adb shell content query --uri content://sms
```

WhatsApp on Android keeps its database encrypted (`msgstore.db.crypt15`); it is not read.

### Viber: through Viber Desktop (Linux)

Viber keeps no message content on its servers and offers no export; its Google Drive backup can
only be opened by the Android app. The history of an Android phone can be had by linking Viber
Desktop to it (a fresh profile) and letting it sync.

Viber Desktop's `~/.ViberPC/<number>/viber.db` is encrypted (`PRAGMA hexkey`, with a key that is
transformed internally, so it does not open with SQLCipher). Everysaid's Viber bridge
(`bridges/viber/`, `docs/viber-bridge.md`) is an `LD_PRELOAD` library inside the running Viber
Desktop: it reads that history through Viber's own connection, follows what arrives and sends.
Linux only. A decrypted copy of the database, where there is one (`[viber] desktop_export`), is
imported too. Tables `Events` (`TimeStamp` in ms, `Direction` 0 in / 1
out, `ChatID`, `ContactID`), `Messages` (by `EventID`), `ChatInfo`, `Contact`. Calls are not synced
to the desktop (`Calls` is empty), nor the media of the synced history: only the files Viber
Desktop downloaded or sent itself (`Messages.PayloadPath`) are linked to their messages. On Android
the media are in `Android/data/com.viber.voip` (readable with `adb pull`).

The message token, `Events.Token` on the desktop and `ZVIBERMESSAGE.ZTOKEN` on the iPhone, is the
same id on both, and the deduplication key. A token carries its send time
(`(token >> 22) + 292057776050` is Unix ms, within about 2 s). Media files from Android
(`IMG-<hex>-V.jpg`, `video-<hex>-V.mp4`, `<hex>.vptt`) are named by `md5(DownloadID)`, the server's
media id, kept in `DownloadFile.DownloadID` on the desktop and `ZATTACHMENT.ZID` on the iPhone.
Size alone is not a safe key: Android keeps downscaled copies, and different pictures often share
a size by chance; `FileHash` and `EncParams` concern the encrypted upload and cannot be checked
against the files.

### Telegram: through its API

`everysaid telegram-sync` (and the Telegram source) reads the user's own account through the
Telegram API (gotd). Unlike Telegram Desktop's JSON export, which takes the same history by hand
each time, the API works for any user and brings only what is new.

- `--save-credentials`: api_id and api_hash from my.telegram.org, as secrets.
- `--login`: the code arrives inside Telegram, not by SMS; the session, full access to the account,
  is kept as a secret (`telegram-session-go`).
- `--survey`: every chat with its kind, size and dates, no content, in `<cache>/telegram/survey.tsv`.
- With no option: every chat but channels and bots into `<cache>/telegram/telegram.db`, each message
  whole (as JSON); later runs bring only what is new.
- `--media [--dry-run]`: pictures, videos, GIFs, video notes and voice messages, into
  `<cache>/telegram/media/` (`[telegram] media`, `no_media`).

`telegram-sync` only reads: nothing is sent, nothing is marked read (the live source, in the app,
can send). Secret chats are on the devices only and cannot be had.

### WhatsApp: linked as a device

The `whatsapp-bridge` source (`internal/whatsapp`) is a whatsmeow client inside the app, linked to
the account as a device (like WhatsApp Web). It keeps what arrives after it is linked, files
included, in its store folder (`messages.db`, `whatsapp.db`, `media/`), and sends where
`[whatsapp] send` allows. The importer takes from it what the iPhone backup does not have, matched
by stanza id; LIDs (`...@lid`) are mapped to numbers through the iPhone's WhatsApp contacts and
the store's `whatsmeow_lid_map`. `bridges/whatsapp/` is the same client as a separate program with
a REST API; the two must never run on the same store at once. Unofficial clients may get an
account blocked by WhatsApp; sending raises that risk.

### Signal: linked as a device

The `signal` source (`internal/signal`) runs a helper, `everysaid-signal` (`bridges/signal/`, Rust
on presage and libsignal, AGPL-3.0, a program of its own spoken to in JSON lines), linked to the
account as a secondary device. Its keys are kept encrypted in `<data>/signal/<instance>/`; what it
receives goes to `<cache>/signal/<instance>/` and from there into the archive. Only what arrives
after the link can be had.

### Facebook Messenger

Not in the iPhone backup (the app excludes its messages). Meta's "Download your information" export
(JSON); since late 2023 personal chats are end-to-end encrypted and stay on the server only with
"secure storage" (a PIN), and are believed to be downloadable from Messenger's own settings. Not
verified yet.

### Adium and Pidgin: the messengers of the 2000s

The logs of the multi-protocol clients, Adium (macOS) and Pidgin or Gaim (libpurple), one file per
session under the account and the contact, give MSN, ICQ, AIM, Yahoo, Jabber and Google Talk,
Skype, IRC and Facebook chat (`internal/importers/imlogs.go`, the "Adium and Pidgin logs" source).
The folders are read as they are: unpacking an archive of them is the user's job.

- Google Talk is `jabber` (the same people, by the same addresses); Facebook chat, over XMPP or
  Adium's plugin, is `messenger`, by Facebook's ids. An MSN or Jabber handle is an email, shared
  with every service and the address book; the others are ids within their service.
- The owner's account a chat was on (the log folder's) is a member of its conversation, so that one
  of several accounts can be hidden; a chat with someone may have been on several.
- Messages have no ids: the fingerprint (second, direction, kind, text) within the conversation
  tells one seen before, from either program. Status lines are left out; the pictures Adium kept
  beside its logs come in as the messages' files.
- Pidgin logs names, not handles: the owner's messages are told by the account's names (the
  account, its alias in `accounts.xml`, any name speaking in three or more of its conversations).
  Adium's `alias` and Pidgin's `blist.xml` give people their names (`handle_name`, `chat` and
  `book`); the owner's own groupings of handles into one person (Adium's metacontacts, Pidgin's
  contacts of several buddies) merge the people of those handles.

## The archive

`<data>/archive.db` (mode 600) holds every message and call once, whatever its source, and is only
ever added to. Filled by the sources in the app, or by hand:

```
everysaid import sms calls viber whatsapp telegram voip media   # what is already there is skipped
```

Schema (`internal/archive`):

- `message` and `call`: our own `id`; `service_id`, `ts` (Unix ms, UTC), `outgoing`; for messages
  `conversation_id`, `sender_id` (an `address`, NULL when outgoing), `kind_id`, `text`, and `key`,
  the service's own id (Viber token, WhatsApp stanza id, iMessage guid, Telegram message id; NULL
  for SMS/MMS), unique per service or, where a service's ids are unique only per chat
  (`key_scope`), per conversation; for calls `address_id`, `answered`, `duration` (s).
- What the services add to a message (`internal/importers/extras.go`, per source): `subtype` (link, pin,
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
- `message_fts`: FTS5 over the text folded (`internal/text`: lower case, no accents of any script,
  final sigma as sigma), so `καλημερα` finds `Καλημέρα`; written by the archive as it adds a message.
- The app's tables: `plugin_instance` (every source, library and address book in use, with its
  settings and state; `source.instance_id`, `library_link.instance_id` point to it), `contact` and
  `contact_address` (an address book's people, joined to the addresses they list), `handle_name`
  (every name a service has shown for a handle, of a kind, with when it was seen), `state_report`
  (what each source says about a chat: hidden, muted, pinned, read up to) and `chat_state` (what the
  user chose), `setting` (the user's, shared by every device), `media_decision` (keep, remove, to the
  library; the newest counts), `message.status` (messages sent from the app). Also: mentions,
  receipts, blocked handles and those removed as spam (`spam`), the user's merges of people and groups, labels and name guesses
  (`label`, `person_label`, `name_guess`), and a trigram index (`message_tri`) for parts of words.
  Until the first
  release the schema changes in place, without migrations.

Deduplication. Rows found in more than one source are paired one to one and kept once, with every
origin recorded; where the sources differ, the copy of the device in use at the time wins
(`device.used_from`/`used_until`).

- SMS and MMS (`internal/importers/sms.go`): phones carry the same history, copied from phone to phone.
  Pairs: same direction and text, at most 2 s apart. SMS and MMS are paired together, because
  Android keeps many plain texts as MMS (long ones, or with a link) that the iPhone has as SMS; they
  share one conversation per counterpart. Exact repeats (same second and text, different guid) are
  taken once. On newer iOS many messages have `text` NULL and the text only in `attributedBody`,
  decoded by the importer.
- Calls (`internal/importers/calls.go`): same number, direction and second.
- Viber (`internal/importers/viber.go`): the iPhone's `viber.sqlite` and a Viber Desktop export, matched by
  token. People are matched by member id and stored by phone number where either source knows it,
  so they meet their SMS and calls; groups by group token. The token's time fills in a missing
  date.
- WhatsApp (`internal/importers/whatsapp.go`): the iPhone's `ChatStorage.sqlite`, then the
  WhatsApp store, matched by stanza id. Message types come from `ZMESSAGETYPE` (GIFs are silent
  mp4s; round video notes; deleted messages); some business messages have their text only in the
  media item's protobuf metadata, which is extracted.
- Telegram (`internal/importers/telegram.go`): ids unique per chat (`key_scope`), the row key `<chat>/<id>`.
  People by phone where Telegram shows it, else by user id, with the id, username and profile name
  as further handles of the same person; deleted accounts have neither name nor number. Calls,
  which Telegram keeps as service messages, go to `call` too.
- Calls of the apps and the carrier (`internal/importers/voip.go`, importer `voip`): WhatsApp's call log
  merged with the call bubbles in its chats (type 59), Viber's recents (`ZRECENT`), and the
  carriers' missed-call SMS notices read from the archive's own SMS (`internal/importers/carriers.go`,
  one parser per carrier or country, chosen in `[import] carrier_notices`). A call already there from another
  source (same service, person and direction) within 60 s is not added again; calls of the same
  source are never merged.
- Media (`internal/importers/media.go`): each file is stored once by content, hard-linked (no extra space)
  as `media/<ab>/<sha256><ext>`; `media` (sha256, size, mime, path) and `attachment` (message,
  sha256, source, the file's path in that source). Links: iPhone WhatsApp by stanza id
  (`ZWAMEDIAITEM.ZMEDIALOCALPATH`), iPhone Viber by token (`ZATTACHMENT.ZNAME`), Viber Desktop
  by token (`Messages.PayloadPath`), Android MMS by MMS id, Telegram by message, the WhatsApp store
  by message.
- Channels are never imported: Viber chats of type 3 (`ZCONVERSATION.ZSUBTYPE`, desktop
  `ChatInfo.PGType`), WhatsApp `...@newsletter`, `status@broadcast` and each contact's status (`...@status`, `...@lid.status`), Telegram channels; Telegram
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
happens only on the user's word: decisions are marked first, and the removal is carried out
afterwards.

The app's way to the library is its library plugins (folder, immich). Tools for sorting a large
backlog of chat pictures (matching them against immich, judging them with local models, review
pages) are separate, outside this repository and not part of the app.

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

1. **Sources still missing**: the iPhone's SMS/iMessage attachments (`MediaDomain`
   `Library/SMS/Attachments`), Android WhatsApp (its encrypted backup), Messenger (Meta's export),
   a native Android companion for live SMS and calls.
2. **Writes through the core**: the importers become sources that yield records, the core's
   pipeline storing and deduplicating them (they write through `Archive`, wrapped as plugins).
3. **Native wrappers** (Capacitor, Tauri) if notification replies or sharing into the app on an
   iPhone are wanted.
4. **Several users** on one server: the auth database and the core already take the archive per
   user; what is left is the UI to add a user and the per-user plugin host.
5. **Names written back to the address book**: from the people without a name, a new contact, or a
   handle added to an existing one, in the user's CardDAV address book (today contacts are only
   read), so that a name lives there and not only in the app.
## License

Everysaid is free software under the GNU Affero General Public License, version 3 (`LICENSE`).
