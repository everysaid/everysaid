# everysaid-signal

Everysaid's Signal helper: a small program that links to a Signal account as a **secondary device**
(as Signal Desktop does, by scanning a QR code with the phone) and speaks to Everysaid in JSON lines
over its stdin and stdout. It never registers a primary device.

It is built on [presage](https://github.com/whisperfish/presage) and Signal's
[libsignal](https://github.com/signalapp/libsignal), both AGPL-3.0, so it is a program of its own,
under the **AGPL-3.0** (see `LICENSE`): Everysaid starts it and talks to it, and links none of its
code. Everysaid's `signal` source (`internal/signal`) is the only caller.

Versions are pinned: presage by commit (`33dd149`, September 2026; presage has no release on
crates.io), and through it libsignal `v0.99.0` and libsignal-service-rs `9e6c08b`, held by
`Cargo.lock`.

## Building

```
cd bridges/signal
cargo build --release          # target/release/everysaid-signal
cargo test                     # the protocol and the conversion of messages; never the network
```

It needs a recent stable Rust, a C compiler, `protoc` (the protobuf compiler, used by libsignal's
build) and OpenSSL's libcrypto with its headers (SQLCipher, which encrypts the store, uses it; the
program links `libcrypto.so.3`): on Arch the packages `protobuf` and `openssl`, on Debian and Ubuntu
`protobuf-compiler` and `libssl-dev`.

Everysaid finds the program next to its own binary, else on the `PATH`; the source's setting
"The Signal helper" names another. The release binary is about 18 MB.

## What it keeps

The folder Everysaid gives it (`<data>/signal/<instance>/`, mode 700) holds `presage.db`: the
device's keys, the contacts and groups, and what it received since it was linked, encrypted
(SQLCipher) with a passphrase Everysaid makes once and keeps in the system's keyring. Files of
messages go to the folder Everysaid names (`<cache>/signal/<instance>/media/`), as
`<sent ms>-<first 8 of the author's id>-<n>.<ext>`.

## The protocol

One JSON object per line, UTF-8, each way. A request has an `id` (a number) and a `cmd`; its
answer carries the same `id`:

```
→ {"id": 3, "cmd": "status"}
← {"id": 3, "ok": true, "result": {"open": true, "linked": true, ...}}
← {"id": 4, "ok": false, "code": "not_linked", "error": "not linked to a Signal account"}
```

Codes: `bad_request`, `not_open`, `not_linked`, `already_linked`, `locked` (wrong passphrase),
`unknown_group`, `failed` (Signal's own error, in `error`). Requests are answered as they finish,
not always in order; `open` is done before the next line is read.

Events come without being asked and have an `event` name instead (they may have an `id` of their
own: a group's, a call's). The helper's log is on stderr (`EVERYSAID_SIGNAL_LOG=debug` for more).
It ends on `quit` or when its stdin closes, after the requests under way (for half a minute at
most).

### Requests

| `cmd` | fields | result |
|---|---|---|
| `open` | `store`, `attachments` (folders), `passphrase` | status |
| `status` | | `{open, linked, receiving, aci, pni, phone, device_id, device_name}` |
| `link` | `device_name` (default "Everysaid") | a `link_url` event, then (once scanned) status |
| `sync` | | asks the phone for its contacts: `{requested: true}`; they come as a `contacts` event |
| `contacts` | | `{contacts: [{aci, phone, name, profile_name}]}` (`name`: the address book's, only with a `phone`) |
| `groups` | | `{groups: [{id, title, description, revision, members, pending}]}` |
| `receive` | `download` (default true: fetch attachments) | `{started}`; then events until the helper ends |
| `send` | `chat`, `text`, `quote`, `mentions`, `attachments` | `{ts}`; and a `message` event of what was sent |
| `react` | `chat`, `target: {author, ts}`, `emoji`, `remove` | the owner's reaction (in place of the one there was; `remove` with the emoji that was there takes it back): `{ts}`; and a `reaction` event of it |
| `edit` | `chat`, `target_ts` (the message's newest version), `original_ts` (its first), `text` | the owner's message with a new text, its files and quote kept from the store's copy: `{ts}`; and an `edit` event of it |
| `delete` | `chat`, `target_ts` (the message's newest version) | the owner's message deleted for everyone: `{ts}`; and a `delete` event of it |
| `mark_read` | `messages: [{author, ts}]`, `receipts` (default true) | a read sync to the phone always, read receipts to each author where `receipts`: `{marked}` |
| `fetch` | `messages: [{author, ts}]` | the files of these messages (in the store) fetched again: each comes again as its `message` event; `{found}` |
| `history` | `since` (Unix ms) | `{events: [...]}`: what the store holds, as events (files already fetched only) |
| `quit` | | `{}`, then it ends |

A `chat` is `{"kind": "contact", "id": "<ACI>"}` or `{"kind": "group", "id": "<group id>"}`. A
group's id is the base64 identifier Signal's apps show, never its master key (which stays in the
helper). A `quote` is `{ts, author, text}`. `mentions` are `[{start, length, aci}]` in UTF-16 units,
each over a U+FFFC in the text, as Signal's apps write them. `attachments` are `[{path,
content_type, filename, voice}]`: files the helper reads and uploads.

### Events

Every message-like event has `chat`, `sender` (an ACI; the account's own for what the owner sent
from any device), `sender_device`, `outgoing`, `ts` (when it was sent, Unix ms: with the author,
Signal's name for a message) and `server_ts`.

| `event` | fields |
|---|---|
| `link_url` | `url` (`sgnl://linkdevice?...`: draw it as a QR code for the phone) |
| `queue_empty` | what waited on the server has all come |
| `contacts` | `contacts`, as the `contacts` request (after the phone sent them) |
| `contact` | `aci, phone, name, profile_name`: someone whose message came, once presage fetched their profile |
| `group` | as one of `groups`, before the first message of a group at a new revision |
| `message` | `text`, `mentions`, `quote`, `attachments` (`content_type, filename, size, width, height, caption, voice, gif, borderless, sticker, file` or `error`), `contacts` (shared), `previews`, `poll`, `group_change`, `group_revision`, `group_call`, `expire_timer_update`, `expire_timer`, `forwarded`, `view_once`, `pin`, `unpin`, `payment`, `gift`, `story_reply` |
| `edit` | `target_ts` and the message's new fields |
| `delete` | `target_author`, `target_ts` (deleted for everyone); `admin` for a group admin's deletion, passed on only when the sender is an admin of the group (else only of their own message) |
| `reaction` | `emoji`, `remove`, `target_author`, `target_ts` |
| `receipt` | `sender` (who), `kind` (`delivery`, `read`, `viewed`), `timestamps` (the owner's messages) |
| `read` | `messages: [{author, ts}]`: the owner read them on another device |
| `call` | `id`; from a call message: `action` (`offer`, `answer`, `busy`, `hangup`), `video`, `hangup` (how); from the phone's call log (`source: "sync"`): `type`, `direction`, `result` (`accepted`, `not_accepted`, ...) |
| `decryption_error` | `sender`: a message that could not be read |
| `receive_ended` | `error` (null when it ended by itself), `code`: `unlinked` when Signal refused this device (it was removed from the phone's linked devices) |

Typing, stories and Signal's own housekeeping messages are not passed on.

## What it cannot do

- **History before the link.** Signal gives a new linked device only what arrives from then on
  (contacts and groups are synced). Signal Desktop's "transfer message history" (an encrypted
  backup sent at link time) is not implemented by presage: the key for it is ignored.
- **The chats' state** (archived, pinned, muted) lives in Signal's storage service, which presage
  does not read.
- **Calls** are seen as their signalling messages and the phone's call log entries, not taken: the
  helper never answers or places one.
- Contact discovery by phone number (presage's `cdsi` feature, which needs BoringSSL) is not built.
