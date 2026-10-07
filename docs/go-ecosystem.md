# Go ecosystem for porting Everysaid

Research for porting Everysaid (Python: `everysaid/`, scripts, FastAPI server, MCP server) to one Go
binary: static, cross-compiled for Linux, macOS and Windows on amd64/arm64, with as little cgo as
possible. Versions and dates were checked on GitHub and `proxy.golang.org` on 2026-10-07.

This is the research as it was written before the port. The port is done (`docs/go.md`,
`docs/go-port.md`) and the Python has since left the repository: the Python files, scripts and
tests named below are in its history, and the steps about running both side by side are past.

## 0. What the Python code uses today (the scope)

| Piece | Python today | Where |
|---|---|---|
| Archive | `sqlite3`, WAL, `busy_timeout`, FTS5 (`unicode61 remove_diacritics 2` and `trigram`, `contentless_delete=1`), text folded in Python before indexing (`text.fold`: casefold + NFKD + mark strip + NFC) | `archive.py`, `core/store.py`, `core/queries.py`, `text.py` |
| Server | FastAPI + uvicorn (`proxy_headers=True`), 87 routes, **one WebSocket** `/api/events` (JSON events, app-level ping every 25 s, own Origin + cookie check), SPA fallback from `web/dist` with `immutable` cache for `/assets/`. No SSE. | `server/app.py`, `server/host.py` |
| Auth | `webauthn` (resident key required, UV preferred, discoverable login, exclude credentials, several origins), scrypt passwords, hand-written TOTP, setup links, recovery codes | `server/auth.py`, `server/app.py` |
| Push | `pywebpush` + `py_vapid` (`Vapid02`, private key kept as PEM in the keyring as `vapid-private`) | `server/push.py` |
| MCP | `mcp` (`MCPServer`, tools by decorator), stdio | `mcp_server.py` |
| Telegram | Telethon: `StringSession` kept as a secret, `start()` (phone/code/2FA), `get_dialogs`, `iter_messages(min_id, reverse)`, `NewMessage`/`MessageEdited`/`Raw` handlers, `send_message`/`send_file` with `reply_to` and formatting entities, `send_read_acknowledge`, `download_media`, `get_messages(filter=…)` counts. Messages are stored in `telegram.db` **as Telethon's own JSON**. | `plugins/telegram_live.py`, `telegram.py`, `telegram_store.py`, `scripts/telegram-sync.py` |
| WhatsApp | Separate Go process (`bridges/whatsapp`, whatsmeow + **mattn/go-sqlite3, cgo**), REST on 127.0.0.1 | `bridges/whatsapp/` |
| Phone numbers | `phonenumbers` (parse, `is_valid_number`, E164/INTERNATIONAL), metadata 9.0.40 | `archive.py`, `core/names.py` |
| Secrets, dirs, time | `keyring`, `platformdirs` (data/cache/config/state, `appauthor=False`), `tzlocal` + `/etc/localtime` logic, `tzdata` on Windows, `tomllib` for `config.toml` | `config.py` |
| iPhone backup | `iphone_backup_decrypt` (`EncryptedBackup`, `extract_file`), `idevicebackup2` run as a process | `scripts/iphone-*.py`, `scripts/media-restore.py`, `plugins/sources.py` |
| Images | Pillow + pillow-heif for thumbnails only: open (JPEG/PNG/HEIC/…), `exif_transpose`, `thumbnail` (360/1600 px), save **WebP q80**; video frame via `ffmpeg` | `server/app.py:make_thumb`, `demo.py` draws demo pictures |
| External tools | `adb`, `idevicebackup2`/`idevice_id`, `ffmpeg`, `exiftool`, the C++ Viber Desktop exporter | `plugins/sources.py`, `plugins/libraries.py` |
| HTTP clients | `urllib`, `httpx` (CardDAV `REPORT`, immich, local model in `analysis.py`) | `plugins/*.py` |
| i18n | `plugins/i18n.py` dict `EL` with English keys and `{name}` placeholders; `tests/test_i18n.py` | |
| Tests | pytest on a demo archive built once (`demo.build(7)`), copied per test; Playwright e2e against `everysaid demo --dir /tmp/chr-demo --serve` (port 8530) | `tests/`, `web/e2e/` |

Not in the port's scope (stay Python or external): the ML/media scripts (torch, insightface,
transformers, imagehash, the VLM review pages), the C++ Viber Desktop exporter, `idevicebackup2`,
`adb`, `ffmpeg`, `exiftool`.

**Project licence:** the repository has no `LICENSE` file and `pyproject.toml` declares none, so
today the code is "all rights reserved". This matters for Signal (section 8).

---

## 1. SQLite without cgo

| | modernc.org/sqlite | github.com/ncruces/go-sqlite3 | github.com/mattn/go-sqlite3 |
|---|---|---|---|
| Repo | gitlab.com/cznic/sqlite (mirror github.com/modernc-org/sqlite) | github.com/ncruces/go-sqlite3 | github.com/mattn/go-sqlite3 |
| Latest | v1.61.0 (2026-09-30, changelog; proxy `@latest` still v1.60.1 of 2026-09-29) | v0.35.6 (2026-09-23) | v1.14.52 (2026-09-05) |
| SQLite | 3.53.4 | current 3.5x (wasm build) | 3.5x (amalgamation) |
| How | C transpiled to Go (ccgo) | SQLite compiled to Wasm, then **wasm2go** to Go (no wazero at run time any more) | cgo |
| cgo | no | no | **yes** |
| Licence | BSD-3-Clause | MIT | MIT |
| Activity | very active, frequent releases | very active | active |
| FTS5 / trigram / contentless_delete | built in (`SQLITE_ENABLE_FTS5`) | **opt-in**: import `github.com/ncruces/go-sqlite3/ext/fts5` | built in with tag `sqlite_fts5` (default now) |
| JSON | built in (JSON1 is core since 3.38) | built in | built in |
| Speed | CPU-bound queries 1.3–2.0x C (their own measurements, Sept 2026); I/O-bound ≈ C | similar order (wasm2go), higher memory per connection | C speed |
| Locks | POSIX advisory locks like C SQLite (default); opt-in OFD locks (`MODERNC_SQLITE_OFD_LOCK=1`) | OFD locks on Linux/macOS, `LockFileEx` on Windows; WAL shm via mmap / `MapViewOfFile` | C SQLite's |

**Recommendation: modernc.org/sqlite.** It is the same C SQLite (same planner, same locking
protocol, same file format), FTS5 with `trigram` and `contentless_delete` built in, the largest
user base, and whatsmeow / mautrix `dbutil` work with it (driver name `sqlite`, see 7).
**Alternative:** ncruces/go-sqlite3 (good, but FTS5 must be registered explicitly and its VFS is
its own Go implementation).

Concurrent access with Python scripts writing the same file: both pure-Go drivers interoperate
with the C SQLite in Python: modernc uses exactly SQLite's own POSIX locks and `-shm` layout;
ncruces uses OFD locks, which on Linux conflict with classic POSIX locks (so they exclude each
other correctly) and the same `-shm` mmap protocol. Still: add a test where a Python process writes
in WAL mode while the Go binary reads and writes, on Linux and macOS.

Gotchas:
- modernc: **downstream `go.mod` must pin the exact `modernc.org/libc` version** that
  modernc.org/sqlite's `go.mod` pins (v1.77.1 for v1.60.x), else subtle breakage (cznic issue 177).
  Requires Go 1.26.
- DSN: `file:archive.db?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate`
  (writers). A plain path drops `?mode=ro`: use a `file:` URI for read-only opens (MCP stdio opens
  read-only). Enable `StrictPragmas` if the DSN is ever built from user input.
- POSIX-lock trap (C SQLite has it too): closing *any* descriptor of the DB file in the process
  drops SQLite's locks. Never `os.Open` the live DB for copying while connections are open; use
  `VACUUM INTO` or the backup API. Or enable modernc's OFD mode on Linux.
- Bound the pool (`SetMaxOpenConns`): each connection has its own page cache. Mirror the Python
  shape: one writer connection (queue), a small pool of readers.
- **Folding parity:** the index holds text folded by Python (`str.casefold` = full Unicode case
  folding, NFKD, strip marks, NFC). Go must use `golang.org/x/text/cases.Fold()` +
  `golang.org/x/text/unicode/norm`, not `strings.ToLower`, and a parity test over the archive's
  text (Python vs Go output identical) is mandatory, or searches silently miss rows indexed by the
  other side.

## 2. HTTP server, router, WebSocket, embedded UI

| Piece | Choice | Version / date | Licence | cgo | Notes |
|---|---|---|---|---|---|
| Router | **stdlib `net/http` ServeMux** (Go 1.22+ patterns: `GET /api/chats/{id}`, `{path...}`) | Go 1.26/1.27 | BSD | no | 87 routes fit fine; middleware by wrapping handlers |
| Router (alt.) | github.com/go-chi/chi/v5 | v5.3.2 (2026-08-20) | MIT | no | route groups/middleware stacks; compatible with `http.Handler` |
| WebSocket | **github.com/coder/websocket** | v1.8.15 (2026-06-15) | ISC | no | already a dependency of whatsmeow and signalmeow; context-based, `wsjson` |
| WebSocket (alt.) | github.com/gorilla/websocket | v1.5.3 (2024-06), repo quiet since 2025-03 | BSD-2 | no | |
| SSE | not used by the Python server | | | | |
| Static UI | stdlib `embed` + `io/fs` + `http.FileServerFS` | | | | |

Gotchas:
- `coder/websocket.Accept` rejects cross-origin requests by default; the Python code does its own
  Origin check against `origins()` + localhost: pass `OriginPatterns` (or check first, then
  `InsecureSkipVerify: true`), and close with code 4401 as today.
- `//go:embed` cannot reach `../`: put a tiny package in `web/` (`web/embed.go`:
  `//go:embed all:dist`) and import it. `all:` is needed for files starting with `.`/`_`. The
  release build must run `pnpm build` before `go build`; keep a `dev` build tag that serves
  `web/dist` from disk.
- SPA fallback and headers must be reproduced: `/assets/*` → `public, max-age=31536000,
  immutable`; everything else and the `index.html` fallback → `no-cache`; `api/*` unknown → 404 JSON.
- uvicorn ran with `proxy_headers=True`: implement `X-Forwarded-For`/`-Proto` handling for trusted
  proxies only (rate limiting uses the client IP).
- Errors: keep the `{"detail": …}` / `UserError` code shape exactly; `tests/test_api_routes.py`
  checks every UI call against the server's routes and must be ported to walk the Go mux.
- The MCP server can also be mounted in-process over streamable HTTP (design.md section 2).

## 3. WebAuthn / passkeys

**github.com/go-webauthn/webauthn** v0.18.2 (2026-09-19), BSD-3-Clause, no cgo, very active
(pushed 2026-10-07). Alternative: none comparable in Go (`duo-labs/webauthn` is its archived
ancestor); hand-rolling is not advisable.

Mapping of what `server/app.py` does:

| Python (`webauthn`) | go-webauthn |
|---|---|
| `generate_registration_options(resident_key=REQUIRED, user_verification=PREFERRED, exclude_credentials=…)` | `BeginRegistration(user, WithResidentKeyRequirement(Required), WithAuthenticatorSelection(…UVPreferred), WithExclusions(…))` |
| `verify_registration_response(expected_origin=[…])` | `Config.RPOrigins = []string{…}`; `protocol.ParseCredentialCreationResponseBytes` + `CreateCredential` |
| `generate_authentication_options` (no allow list) | `BeginDiscoverableLogin()` |
| `verify_authentication_response(credential_public_key, sign_count)` | `protocol.ParseCredentialRequestResponseBytes` + `ValidatePasskeyLogin(handler, …)` (handler looks the user up by `userHandle`/raw ID) |

Gotchas:
- The API wraps the credential (`{"nonce", "credential", "label"}`), so use the `Parse…Bytes` +
  `CreateCredential` / `ValidatePasskeyLogin` functions, not `FinishRegistration(r *http.Request)`.
- Challenges: Python keeps the challenge server-side under a nonce; go-webauthn returns
  `SessionData`; store it (JSON) under the same nonce in `auth.db`.
- **Existing passkeys keep working**: both libraries store the COSE-encoded public key and the raw
  credential ID; keep `rp_id` and origins unchanged. Add a test with a passkey registered by the
  Python server.
- `options_to_json` output shape differs slightly (go-webauthn wraps in `{"publicKey": …}`): the
  UI's `navigator.credentials` call may need `options.publicKey` unwrapped, or emit the same JSON.
- scrypt passwords: `golang.org/x/crypto/scrypt.Key` with the stored N/r/p/salt reproduces the
  hashes. TOTP is ~20 lines of `crypto/hmac` (or github.com/pquerna/otp v1.5.0, Apache-2.0, quiet).

## 4. Web Push (VAPID)

| | github.com/SherClockHolmes/webpush-go | github.com/marknefedov/go-webpush/v2 |
|---|---|---|
| Latest | v1.4.0 (2025-01-02); master has later fixes (2025-11 race fix, 2026-04 AuthScheme) | v2.0.0 (2026-03-17) |
| Licence / cgo | MIT / no | MIT / no |
| Maintenance | slow but alive; the de-facto standard (≈450 stars) | small, newer (RFC 8291/8292/8030, batch send) |

**Recommendation:** SherClockHolmes/webpush-go, pinned to a recent master pseudo-version (for the
2025-11 race fix); alternative marknefedov/go-webpush/v2. Either is ~300 lines on top of stdlib
`crypto/ecdh` + HKDF, so vendoring/own code is a fallback if both stall.

Gotcha: the existing VAPID key is a **PEM** (py_vapid) in the keyring; webpush-go wants the raw
base64url private scalar and public point. Convert once at load time (`x509.ParseECPrivateKey` /
`ParsePKCS8PrivateKey` → `ecdh` bytes) so existing push subscriptions stay valid (subscriptions are
bound to the VAPID public key).

## 5. MCP server

| | github.com/modelcontextprotocol/go-sdk | github.com/mark3labs/mcp-go |
|---|---|---|
| Latest | v1.8.0 (2026-09-04, proxy) | v1.1.1 (2026-09-23) |
| Licence | Apache-2.0 (transition from MIT; older contributions MIT) | MIT |
| Spec | 2026-07-28 (v1.7.0+) and older | recent spec |
| Transports | stdio, streamable HTTP, SSE (legacy) | stdio, streamable HTTP, SSE |
| cgo | no | no |

**Recommendation: the official go-sdk** (Google + MCP maintainers, typed tools via
`mcp.AddTool[In, Out]` with JSON-schema from structs, `mcp.StdioTransport`). mcp-go is the mature
alternative. Gotcha: the Python tools have optional args with defaults (`limit: int = 30`); in Go
use pointer fields or `omitempty` + defaults applied in the handler, and keep the tool names and
descriptions identical (`tests/test_mcp.py` is the spec). In stdio mode never write logs to
stdout.

## 6. Telegram (MTProto user client)

**github.com/gotd/td** v0.162.0 (2026-09-18), MIT, pure Go, very active, "stable" status but
pre-1.0 (breaking changes still happen, e.g. `feat(markup)!` in Sept 2026; pin and read the
changelog). Companion github.com/gotd/contrib v0.25.0 (2026-07-15, MIT: session/peer storages,
flood-wait middleware). Alternative: github.com/amarnathcjd/gogram v1.7.3 (2026-04), Telethon-like
API, but **GPL-3.0**.

| Telethon use | gotd/td |
|---|---|
| `TelegramClient(StringSession, api_id, api_hash)` | `telegram.NewClient(appID, appHash, telegram.Options{SessionStorage: …})` |
| `start()`: phone, code, 2FA | `telegram/auth`: `auth.NewFlow(UserAuthenticator, …)`; the UI supplies phone/code/password through your own `UserAuthenticator` implementation; `auth.ErrPasswordAuthNeeded` → `client.Auth().Password()` |
| `flood_sleep_threshold` | `contrib/middleware/floodwait` (waiter) |
| `get_dialogs` (access hashes) | `telegram/query/dialogs` iterator; `telegram/peers` manager keeps users/chats with access hashes (persistent storage possible, unlike StringSession) |
| `iter_messages(min_id, reverse=True)` | `telegram/query/messages` (`GetHistory` iterator with offsets) or raw `MessagesGetHistory` |
| `get_messages(limit=0, filter=…)` counts | `MessagesSearch` with `InputMessagesFilterPhotoVideo` etc., read `.Count` |
| `NewMessage`, `MessageEdited`, `Raw` | `tg.UpdateDispatcher` (`OnNewMessage`, `OnEditMessage`, `OnReadHistoryInbox/Outbox`, …) |
| catch-up after a gap | `telegram/updates` gap manager (`GetDifference`/`GetChannelDifference`, needs a `StateStorage` for pts/qts/seq) — or keep the current "iterate from last id per chat" catch-up |
| `send_message(reply_to, formatting_entities)` | `telegram/message` sender: `.Reply(id)`, `.StyledText(…)`/entities |
| `send_file(data, caption, reply_to)` | `telegram/uploader` + `message.UploadedDocument/Photo` |
| `send_read_acknowledge(max_id)` | `MessagesReadHistory` / `ChannelsReadHistory` |
| `download_media` | `telegram/downloader` (handles CDN redirects, file parts) |

**Session conversion (no re-login needed):** gotd ships `session.TelethonSession(str)` which decodes
a Telethon `StringSession` (`'1'` + base64(DC id, IPv4/IPv6, port, 256-byte auth key)) into
gotd's `session.Data` (DC, address, auth key + key ID). Telethon keeps no server salt or config;
gotd fetches those on connect. Then store the result through a `session.Storage` implementation
that reads/writes the same keyring secret (gotd's own format is JSON: `{"Version":1,"Data":{…}}`).
Keep the same `api_id`/`api_hash`. A Telethon *SQLite* `.session` file (not used here) holds the
same fields in its `sessions` table and converts the same way.

Gotchas:
- **`telegram.db` stores Telethon's JSON** (Telethon class names, snake_case fields) and
  `telegram.py` parses that. gotd's `tg.*` types serialise differently. Either write gotd JSON and
  port the importer to read both, or map gotd messages directly to the archive and keep the
  Telethon-JSON reader only for old rows. Decide before porting.
- Marked peer ids (`-100…` for channels, negative for groups) are a Telethon convention used as
  `chat.id`/`conversation.key`; reproduce it exactly (`constant.TDLibPeerID` helpers in gotd).
- gotd's dependency tree and generated TL schema add ~15–25 MB to the binary.

## 7. WhatsApp in-process (whatsmeow)

**go.mau.fi/whatsmeow** — no tags; latest pseudo-version `v0.0.0-20261007111105-c386243a72ba`
(2026-10-07); MPL-2.0; pure Go (its Signal protocol is `go.mau.fi/libsignal`, pure Go); daily
activity. The bridge uses `…20260929…`. Requires Go 1.26 (toolchain 1.27.1).

Embedding instead of the bridge process: move `bridges/whatsapp/*.go` into a package (e.g.
`internal/plugins/whatsapp`), replace `main()`'s flags and REST server with a plugin type that the
plugin host starts/stops, call the send/read/download functions directly instead of
`127.0.0.1:8080/api/…`, and push events into the host's event bus instead of `messages.db` polling
(or keep `messages.db` as the plugin's staging store at first, so the importer is unchanged).
QR linking: render `qrChan` codes in the UI (the bridge prints with `mdp/qrterminal`).

sqlstore with a pure-Go driver: `sqlstore.New(ctx, "sqlite", "file:whatsapp.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", log)`
with `_ "modernc.org/sqlite"`. `go.mau.fi/util/dbutil.ParseDialect` maps any name starting with
`sqlite` to the SQLite dialect, so `"sqlite"` (modernc) and `"sqlite3"` (ncruces) both work. The
existing `whatsapp.db`/`messages.db` files open unchanged (same SQLite format); no re-linking.
Note mattn's `_foreign_keys=on` becomes modernc's `_pragma=foreign_keys(1)`.

Gotchas: WhatsApp forces client updates (old builds get "client outdated" / 405), so the Go
binary must be rebuilt with a fresh whatsmeow regularly: a release cadence concern for a single
binary. Keep the bridge's guards (rate limits, never "online").

## 8. Signal

**Building blocks**
- **libsignal** (github.com/signalapp/libsignal) v0.105.0 (2026-10-07), Rust, **AGPL-3.0-only**.
  The C ABI is crate `libsignal-ffi` (`crate-type = ["staticlib"]` → `libsignal_ffi.a`), MSRV
  Rust 1.93.1, depends on Signal's **BoringSSL fork** (`boring-signal`) → building needs cmake,
  clang/libclang, a C/C++ toolchain per target.
- **mautrix-signal** (github.com/mautrix/signal, module `go.mau.fi/mautrix-signal`) v0.2609.0
  (2026-09-16), **AGPL-3.0** (+ exceptions only for Beeper and Element), monthly releases:
  - `pkg/libsignalgo`: cgo bindings; `libsignal` is a git submodule pinned to a tag (now
    `v0.103.1`, not the latest), `libsignal-ffi.h` is regenerated from it with cbindgen
    (`update-ffi.sh`). Link flags are hard-coded in `cflags.go`:
    `#cgo LDFLAGS: -lsignal_ffi -ldl -lm -lz -lstdc++`; the `.a` is found via `LIBRARY_PATH` or
    `/usr/lib`. `build-rust.sh`: `RUSTFLAGS="-Ctarget-feature=-crt-static" cargo build -p libsignal-ffi --profile=release`.
    Platform files exist for darwin and windows/arm64 (`fixedarray_clang.go`), so macOS/Windows
    are anticipated; CI (mau.dev, gomuks-build-docker) publishes prebuilt `libsignal_ffi.a` for
    Linux amd64, Linux arm64, macOS arm64 only.
  - `pkg/signalmeow`: the linked-device client, importable as a library. Depends on
    `libsignalgo`, `go.mau.fi/util` (dbutil, works with modernc as in 7), coder/websocket,
    protobuf, gRPC (Signal's newer chat APIs), but **not** on the Matrix SDK. Its API is not
    stable (it follows the bridge's needs).

**What a linked-device client needs (all present in signalmeow):** provisioning (websocket to
`/v1/websocket/provisioning/`, a `sgnl://linkdevice?uuid=…&pub_key=…` URL shown as QR, provisioning
envelope decrypted with the device's ephemeral key: `provisioning.go`, `provisioning_cipher.go`);
device registration and prekeys (`keys.go`, Kyber prekeys); authenticated websocket to
`chat.signal.org` (`web/`, `wspb/`); receiving and decrypting (sealed sender, Signal sessions,
sender keys for groups: `receiving*.go`, `senderkey.go`); contacts/groups sync and storage service
(`contact.go`, `groups.go`, `storageservice.go`); attachments (AES-CBC+HMAC download/upload:
`attachments*.go`); sending, receipts, profiles; message history is **not** in the protocol beyond
what the primary sends at link time (backup transfer `backup.go` exists for the "transfer history"
flow).

**Cross-compiling difficulty:** high. Per target you need a Rust build of `libsignal-ffi`
(BoringSSL inside) and a cgo link: Linux amd64/arm64 are proven (Docker / `cargo-zigbuild`);
macOS amd64/arm64 are best built natively on a Mac and `lipo`-ed; Windows needs
`x86_64-pc-windows-gnu` (BoringSSL with mingw) so Go's mingw-based cgo can link it, which is
untested territory. `-lstdc++`/`-ldl` are hard-coded: with zig cc or on macOS (libc++ only) a shim
or a patched `cflags.go` (fork, build tag) is likely needed. A fully static Linux binary needs the
musl Rust target and `-extldflags -static`.

**Licence implications:** linking `signalmeow`/`libsignalgo`/`libsignal` into the binary makes the
whole binary a work under **AGPL-3.0**: distributing it requires offering the complete source under
AGPL-3.0, and because Everysaid is a network server, §13 requires offering the source to every
user who interacts with it over the network. Today the repository has no licence at all; choosing
AGPL-3.0-or-later for Everysaid would make this clean (whatsmeow MPL-2.0, gotd MIT, the rest
MIT/BSD/Apache are all compatible). If the project is to stay non-AGPL, Signal must live in a
**separate helper binary** (its own AGPL source, talking over a local socket/stdio), which is also
what keeps the main binary cgo-free. The Signal Foundation offers no exception.

**Recommendation:** Signal last, behind a build tag (`signal`) or as a separate
`everysaid-signal` helper binary built from signalmeow; the main binary stays `CGO_ENABLED=0` for
all six targets.

## 9. Phone numbers

**github.com/nyaruka/phonenumbers/v2** v2.0.14 (2026-10-02), MIT, no cgo, active; v2.0.13 carries
**metadata 9.0.40, the same as the Python `phonenumbers` 9.0.40** pinned here. (v1 line: v1.8.1,
2026-07-15.) `Parse`, `IsValidNumber`, `Format(E164|INTERNATIONAL)` match the calls used.
Alternative: none serious (other ports are stale). Gotchas: keep the metadata version in step with
Python while both run; a parity test over the archive's raw numbers (like
`scripts/archive-phonenumbers.py`) guards normalisation; don't import the carrier/geocoding
packages (several MB each).

## 10. Keyring and platform folders

| | Choice | Version / date | Licence | cgo | Notes |
|---|---|---|---|---|---|
| Keyring | **github.com/zalando/go-keyring** | v0.2.8 (2026-03-23), active | MIT | no | Linux: Secret Service over D-Bus (godbus); macOS: runs `/usr/bin/security`; Windows: Credential Manager (wincred) |
| Keyring (alt.) | github.com/99designs/keyring | v1.2.2 (2022-12), stale | MIT | **yes on macOS** (keybase/go-keychain) | avoid |
| Folders | own ~30-line function, or github.com/adrg/xdg | xdg v0.5.3 (2024-10; repo active 2026-09) | MIT | no | |

Keyring compatibility with the Python `keyring` entries (service `everysaid`, user = secret name):
- Linux: compatible (both use attributes `service` + `username`).
- macOS: compatible lookup (generic password, service/account), but items written by Python are
  ACL-bound to the Python executable: the first read by the Go binary may show a Keychain prompt
  ("Always allow" once).
- **Windows: not compatible.** Python stores target name `everysaid` (or `name@everysaid`);
  go-keyring uses `everysaid:name`. Needs a one-time migration (read with `wincred` directly under
  the Python names, write under the new ones) or re-entry.
- Headless Linux without a Secret Service: go-keyring returns an error; keep the existing
  file-in-CONFIG fallback (mode 600).

Folders: match `platformdirs` exactly, or existing users lose their data paths. platformdirs
(`appauthor=False`): Linux XDG (`~/.local/share`, `~/.cache`, `~/.config`, `~/.local/state`);
macOS data/config/state `~/Library/Application Support/everysaid`, cache `~/Library/Caches/everysaid`;
Windows data/config/state `%LOCALAPPDATA%\everysaid`, cache `%LOCALAPPDATA%\everysaid\Cache`.
Go's `os.UserConfigDir` returns `%AppData%` (Roaming) on Windows and adrg/xdg puts the Windows cache
in `%LOCALAPPDATA%\cache\everysaid`: both differ. Write the small function, honour
`EVERYSAID_DATA/CACHE/CONFIG/STATE` first as today. TOML config: github.com/BurntSushi/toml v1.6.0
(2025-12, MIT) or github.com/pelletier/go-toml/v2 v2.4.3 (2026-07, MIT).

## 11. Time zones

- Embed the database: `import _ "time/tzdata"` (or `-tags timetzdata`), +~450 KB; required on
  Windows and minimal containers. `time.LoadLocation("Europe/Athens")` then works everywhere.
- IANA name of the local zone (tzlocal's job): Go's `time.Local.String()` is just `"Local"`.
  Port `config.py`'s order: `$TZ`, the target of the `/etc/localtime` symlink (Linux and macOS:
  `/var/db/timezone/zoneinfo/…`), `/etc/timezone`; on Windows read the registry
  `TimeZoneKeyName` and map with CLDR `windowsZones.xml`. Ready-made: github.com/thlib/go-timezone-local
  v0.0.8 (2026-07-10, Unlicense, small project) does exactly this; vendoring its ~200 lines is
  reasonable.

## 12. iPhone encrypted backup

No maintained Go library. github.com/dunhamsteve/ios (`irestore`, MIT for its own code, last push
2023-04, uses mattn sqlite + its own plist) implements the full chain, including the iOS 10.2+
double PBKDF2: a good reference to port (~400 lines), not a dependency. github.com/danielpaulus/go-ios
(MIT, active) talks to devices but has no mobilebackup2, so `idevicebackup2` stays external.

The crypto, step by step, and the Go for each:

| Step | Detail | Go |
|---|---|---|
| Read `Manifest.plist` | binary plist: `BackupKeyBag` (data), `ManifestKey` (data), `IsEncrypted` | **howett.net/plist** v1.0.1 (2023-10, BSD-2/3; stable, no cgo) |
| Parse the keybag | TLV: 4-byte tag + 4-byte big-endian length + value; header `VERS`, `TYPE`, `UUID`, `HMCK`, `WRAP`, `SALT`, `ITER`, `DPWT`, `DPIC`, `DPSL`; then per class `UUID`, `CLAS`, `WRAP`, `KTYP`, `WPKY` | `encoding/binary` (own code) |
| Derive the passcode key | `k1 = PBKDF2-HMAC-SHA256(password, DPSL, DPIC, 32)`, then `key = PBKDF2-HMAC-SHA1(k1, SALT, ITER, 32)` | **`crypto/pbkdf2`** (stdlib since Go 1.24) with `crypto/sha256`, `crypto/sha1` |
| Unwrap class keys | for classes with `WRAP & 2`: AES key unwrap (RFC 3394) of `WPKY` with the derived key; a failed IV check = wrong password | RFC 3394 is not in stdlib: ~40 lines over `crypto/aes` (or github.com/NickBall/go-aes-key-wrap, MIT, stale since 2017; or copy dunhamsteve's `aeswrap`) |
| Decrypt `Manifest.db` | `ManifestKey` = 4-byte little-endian protection class + wrapped key → unwrap with that class key → AES-256-CBC, IV = 16 zero bytes, PKCS#7 padding | `crypto/aes` + `crypto/cipher.NewCBCDecrypter` |
| Find a file | `Manifest.db` `Files` table (`fileID`, `domain`, `relativePath`, `flags`, `file` blob); file at `<backup>/<fileID[:2]>/<fileID>` | SQLite (section 1) on the decrypted temp copy |
| Per-file key | `file` blob is an **NSKeyedArchiver** plist (`MBFile`): `ProtectionClass`, `EncryptionKey` (NSData: 4-byte class + wrapped key), `Size` | howett.net/plist decodes the archive; resolve `$objects`/`CF$UID` references by hand (~50 lines) |
| Decrypt a file | unwrap `EncryptionKey` with its class key → AES-256-CBC, zero IV, then truncate to `Size` (padding) | `crypto/cipher`, streaming in chunks for large videos |

Gotcha: the password must still come from the keyring/file/prompt (`golang.org/x/term.ReadPassword`)
and never from argv; decrypted databases go to the cache folder as today.

## 13. Images (thumbnails)

Server-side Pillow is used only by `make_thumb` (and `demo.py` drawing invented pictures):
decode JPEG/PNG/GIF/WebP/HEIC, apply EXIF orientation, fit to 360 or 1600 px, encode WebP q80;
video frames come from `ffmpeg` (keep `os/exec`).

| Need | Choice | Version / date | Licence | cgo | Notes |
|---|---|---|---|---|---|
| JPEG/PNG/GIF decode | stdlib `image/*` | | BSD | no | |
| WebP decode, scaling | **golang.org/x/image** (`webp`, `draw.CatmullRom`/`ApproxBiLinear`) | v0.46.0 (2026-09-08) | BSD-3 | no | `disintegration/imaging` v1.6.2 is from 2019 (works, unmaintained) |
| HEIC decode | **github.com/gen2brain/heic** | v0.7.2 (2026-09-15) | MIT | no | Rust `heic` decoder as Wasm run by wazero, or transpiled with tag `wasm2go`; tries a system libheif via purego first (`nodynamic` tag disables). Pure-Go alt.: github.com/gen2brain/h265 v0.2.3 (2026-09-15, MIT, young) |
| EXIF orientation/date | **github.com/evanoberholster/imagemeta** | v1.1.0 (2026-09-20) | MIT | no | JPEG, HEIC, TIFF, raw. Alts: dsoprea/go-exif/v3 (2022), rwcarlsen/goexif (2019) |
| WebP **encode** (lossy) | github.com/deepteams/webp | v1.2.8 (2026-09-22) | MIT per README (GitHub shows NOASSERTION) | no | young project (38 stars). Alt.: github.com/gen2brain/webp v0.6.4 (libwebp in Wasm). HugoSmits86/nativewebp is lossless only |

Recommendation: simplest is to switch thumbnails to **JPEG (stdlib)** for opaque images and PNG
when alpha: zero risky dependencies; the cache file names (`{sha}-{size}.webp`) and content type
change, and old cached thumbs are simply regenerated. If WebP must stay, use deepteams/webp
(pure Go) with gen2brain/webp as fallback. HEIC: gen2brain/heic with `nodynamic` for reproducible
behaviour (or `ffmpeg`, already required for videos, as a fallback decoder). Orientation must be
applied by hand (rotate/flip per EXIF tag 1–8); Go's decoders ignore it. Note HEVC patent
questions apply to any HEIC decoder shipped in a binary.

## 14. Android and external tools

`adb shell content query …`, `idevicebackup2`, `idevice_id`, `ffmpeg`, `ffprobe`, `exiftool`:
`os/exec` with `exec.LookPath`, context timeouts, streaming stdout into the plugin log. Viber and
WhatsApp databases (and iOS `sms.db`, `CallHistory.storedata`) are SQLite: read with the same
driver, opened read-only (`file:…?mode=ro&immutable=1` for copies). Nothing new is needed.
Windows: `adb.exe` etc. on `PATH`; `exec` handles `.exe` lookup.

## 15. CLI and i18n

| | Version / date | Licence | Notes |
|---|---|---|---|
| **stdlib `flag`** (a `FlagSet` per command) | Go | BSD | 5 commands (`import`, `serve`, `mcp`, `demo`, `user …`) plus the bare-importer-names form; full control of the (translatable) help text |
| github.com/alecthomas/kong | v1.16.1 (2026-08-09), active | MIT | struct-tag CLI, good nested subcommands; help text harder to translate |
| github.com/spf13/cobra | v1.10.2 (2025-12-04) | Apache-2.0 | heavy, completion generation; overkill here |

Recommendation: stdlib `flag` (alternative kong).

i18n: keep the current model, no framework. One generated Go file per language:
`var EL = map[string]string{"error: {e}": "σφάλμα: {e}", …}` converted mechanically from
`plugins/i18n.py`, and `func Tr(s, lang string, args map[string]any) string` that looks up the
translation (English when missing) and replaces `{name}` with a `strings.Replacer`. Port
`tests/test_i18n.py` as a Go test using `go/ast` to collect every `Tr("…")` literal and fail on
missing or unused keys, Greek outside the dictionaries, and error codes without words. The UI's
`web/src/lib/i18n.ts` is untouched. (github.com/nicksnyder/go-i18n/v2 v2.6.1, MIT, adds CLDR
plurals and message files: not needed.)

## 16. Testing

- **Playwright e2e stays the acceptance spec**: the Go binary must provide the same
  `everysaid demo --dir … --serve` (port 8530), the same API and the same `everysaid user link
  --user 1` output. `web/e2e/demo.ts` calls `uv run everysaid user link`: make the command
  configurable (`EVERYSAID_BIN`) so the same specs run against Python and Go.
- **Demo archive**: `demo.py` uses Python's `random.Random(7)`; Go's `math/rand` gives other data,
  and the specs assert on invented names/texts (≈50 text assertions). Either port the generator
  with a fixed list of choices in data (no RNG dependence), or have Python export the demo's
  content once to a JSON/SQL seed that the Go `demo` command embeds.
- **Go equivalent of the pytest fixtures**: `TestMain` sets `EVERYSAID_DATA/CACHE/CONFIG/STATE`
  and `EVERYSAID_KEYRING=everysaid-test` to a temp root, builds the demo archive once
  ("pristine"), and a helper `newStore(t)` copies it into `t.TempDir()` per test.
  `httptest.NewServer(handler)` for API tests; the MCP server tested in-memory
  (`mcp.NewInMemoryTransports`).
- **Parity harness during the port**: run the Python and Go servers on copies of the same demo
  archive and diff JSON for every GET route (and selected POSTs), plus parity tests for text
  folding, phone normalisation and FTS query building.
- Run Go tests in CI with `-race`; the e2e suite against the Go binary in CI on Linux.

## 17. Build and release

- **github.com/goreleaser/goreleaser** v2.18.2 (2026-09-17), MIT: one config, `CGO_ENABLED=0`,
  targets linux/darwin/windows × amd64/arm64, `-trimpath -ldflags "-s -w -X main.version=…"`,
  `before.hooks: pnpm -C web install && pnpm -C web build`, archives, checksums, macOS signing and
  notarization (goreleaser supports `notarize` with `quill`, which runs on Linux), SBOMs.
- **Pure-Go build (everything except Signal):** trivial cross-compile from one Linux machine.
- **With Signal (cgo):**
  - Linux amd64/arm64: `CC="zig cc -target x86_64-linux-musl"` + Rust
    `libsignal_ffi.a` built with `cargo zigbuild --target x86_64-unknown-linux-musl`; static link.
    Or the `ghcr.io/goreleaser/goreleaser-cross` image (osxcross + mingw + arm toolchains).
  - macOS: build natively on a Mac (both arches, `lipo`); cross from Linux with osxcross is
    possible but fragile with BoringSSL.
  - Windows: `x86_64-pc-windows-gnu` Rust target + mingw/zig; unproven, biggest risk; ship
    Windows without Signal first.
  - Prebuilt `libsignal_ffi.a` exist (mau.dev gomuks-build-docker) only for linux amd64/arm64 and
    macOS arm64, and must match the libsignal tag pinned by the signalmeow version used.
  - Use goreleaser's split/merge (or separate builds per OS) since cgo targets need per-target
    toolchains.
- **Binary size (estimates, stripped `-s -w`):**

| Part | ≈ size |
|---|---|
| Go runtime, net/http, crypto, server, core | 8–10 MB |
| modernc.org/sqlite (+libc) | 6–9 MB |
| whatsmeow + protobufs (the current bridge, with mattn, is 24 MB unstripped) | 12–16 MB |
| gotd/td (TL schema) | 15–25 MB |
| phonenumbers metadata | 3–5 MB |
| HEIC decoder (Wasm/wasm2go) + image libs | 3–6 MB |
| webauthn, MCP SDK, push, misc | 3–5 MB |
| web/dist (1.1 MB) + tzdata (0.45 MB) | ~1.6 MB |
| **Total without Signal** | **≈ 50–75 MB** |
| libsignal_ffi + signalmeow + gRPC | +20–35 MB |

UPX is not advisable (macOS signing, AV false positives on Windows).

---

## Recommended stack

| Piece | Library | Alternative | cgo |
|---|---|---|---|
| SQLite | modernc.org/sqlite v1.60.1/v1.61.0 (pin modernc.org/libc) | ncruces/go-sqlite3 v0.35.6 (+ `ext/fts5`) | no |
| Text folding | golang.org/x/text (`cases.Fold`, `unicode/norm`) | — | no |
| HTTP | stdlib `net/http` ServeMux | go-chi/chi/v5 v5.3.2 | no |
| WebSocket | coder/websocket v1.8.15 | gorilla/websocket v1.5.3 | no |
| UI files | `embed.FS` in a `web` package | serve from disk (`dev` tag) | no |
| Passkeys | go-webauthn/webauthn v0.18.2 | — | no |
| Passwords, TOTP | x/crypto/scrypt, own TOTP | pquerna/otp v1.5.0 | no |
| Web Push | SherClockHolmes/webpush-go (master pseudo-version) | marknefedov/go-webpush/v2 v2.0.0 | no |
| MCP | modelcontextprotocol/go-sdk v1.8.0 | mark3labs/mcp-go v1.1.1 | no |
| Telegram | gotd/td v0.162.0 + gotd/contrib v0.25.0 | amarnathcjd/gogram (GPL-3.0) | no |
| WhatsApp | go.mau.fi/whatsmeow (pseudo-version, in-process) on modernc | — | no |
| Signal | mautrix-signal `pkg/signalmeow` + libsignal_ffi, separate helper or `signal` build tag | — | **yes** |
| Phone numbers | nyaruka/phonenumbers/v2 v2.0.14 (metadata 9.0.40) | v1.8.1 | no |
| Keyring | zalando/go-keyring v0.2.8 + file fallback | 99designs/keyring (stale, cgo on macOS) | no |
| Folders | own platformdirs-compatible function | adrg/xdg v0.5.3 | no |
| Config | BurntSushi/toml v1.6.0 | pelletier/go-toml/v2 v2.4.3 | no |
| Time zones | `time/tzdata` + port of config.py logic | thlib/go-timezone-local v0.0.8 | no |
| iPhone backup | own package: `crypto/pbkdf2`, own RFC 3394, `crypto/cipher`, howett.net/plist v1.0.1 | port of dunhamsteve/ios | no |
| Images | stdlib + x/image v0.46.0, gen2brain/heic v0.7.2, evanoberholster/imagemeta v1.1.0; JPEG thumbs | deepteams/webp v1.2.8 for WebP | no |
| External tools | `os/exec` (adb, idevicebackup2, ffmpeg, exiftool) | — | no |
| CLI | stdlib `flag` | alecthomas/kong v1.16.1 | no |
| i18n | generated `map[string]string` + `Tr()` + ast-based test | nicksnyder/go-i18n/v2 | no |
| Tests | `go test` with demo-archive `TestMain`, httptest; Playwright e2e unchanged | — | — |
| Release | goreleaser v2.18.2, `CGO_ENABLED=0`; zig cc / goreleaser-cross / a Mac for Signal | — | — |

## Risks / open questions

1. **Signal and the licence.** Linking libsignal/signalmeow makes the binary AGPL-3.0 (with the
   network clause). The repository has no licence yet: decide (AGPL-3.0-or-later, or keep Signal
   in a separate AGPL helper). This also decides whether the main binary can stay cgo-free.
2. **Signal cross-builds.** Rust + BoringSSL + cgo per target; Windows unproven; hard-coded
   `-lstdc++ -ldl` in `libsignalgo`; libsignal pin must track mautrix-signal; signalmeow's API is
   unstable and Signal's servers change often (monthly upstream releases to follow).
3. **Search parity.** Go folding (`cases.Fold` + norm) must equal Python's `casefold`/NFKD/mark
   strip byte for byte while both write the index; also Unicode version differences between Python
   and x/text. Needs a corpus parity test.
4. **SQLite shared with Python.** Lock interop is expected to work (same protocol / OFD vs POSIX
   conflict on Linux) but must be tested on Linux and macOS with a Python writer and Go
   readers/writers; avoid the POSIX-lock "close any fd" trap; pin `modernc.org/libc`.
5. **Telegram data format.** `telegram.db` holds Telethon JSON; gotd's types differ. Decide: dual
   reader, a converter, or store gotd JSON from now on. Session conversion itself is solved
   (`session.TelethonSession`), but access hashes must be re-fetched (as today).
6. **Keyring on Windows/macOS.** Windows target names differ from Python keyring (migration
   needed); macOS may prompt once per secret for the new binary.
7. **Folder paths** must equal platformdirs' on all three OSes, or existing installs lose their
   archive location (Windows Roaming vs Local trap).
8. **Passkeys, push, sessions continuity.** Verify a passkey registered by Python logs in on Go;
   convert the VAPID PEM so push subscriptions survive; scrypt/TOTP formats unchanged; keep the
   cookie name and session table.
9. **Demo determinism.** e2e specs assert on demo content generated with Python's RNG; the Go demo
   must produce the same data (seed file or data-driven generator).
10. **WhatsApp/Telegram churn in a single binary.** whatsmeow (no tags) and gotd (pre-1.0) need
    frequent updates; WhatsApp blocks outdated clients, so releases must be regular, not occasional.
11. **Thumbnails.** No battle-tested pure-Go lossy WebP encoder (deepteams/webp is young); JPEG
    thumbs avoid it. HEIC decoding via Wasm costs size and CPU; HEVC patents.
12. **Binary size** ≈ 50–75 MB without Signal, ≈ 80–110 MB with it (estimates; gotd and whatsmeow
    dominate). Acceptable for a desktop/server app, but measure early.
13. **What stays outside the binary**: `idevicebackup2` (libimobiledevice), `adb`, `ffmpeg`,
    `exiftool`, the Linux-only C++ Viber Desktop exporter, and the Python ML scripts; they keep
    writing the same `archive.db`/`review.db`, so the schema must stay identical during the
    transition.
