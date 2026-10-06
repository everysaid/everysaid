# Chronika: design of the core, the UI and the MCP server

Agreed on 5 October 2026 and built the same day; section 12 has the owner's decisions, section 13
where the build differs from this text. It builds on what existed (the archive, the importers, the
scripts; see `README.md` and `docs/data-sources.md`).

## 1. What it is for

One messenger for a person's whole history: each person shows the whole conversation across every
service (SMS, iMessage, calls, Viber, WhatsApp, Telegram, Messenger...), old and new, in one
stream. Like WhatsApp or Telegram in look and speed, like Pidgin in reach. Online, on desktop and
mobile; light and dark; in the user's language; secure without getting in the way. An assistant
reaches the same history through an MCP server.

What it is not: a backup tool (keeping the data safe is the user's concern), nor a replacement for
the services themselves (it reads them; where a service allows, it may also send).

## 2. Shape

```
                ┌──────────────── one process: chronika serve ────────────────┐
  browser /     │  HTTP API (REST + WebSocket)  ←→  core  ←→  archive.db       │
  installed PWA ┤                                   ↑   ↑                      │
                │  static PWA files                 │   └── media store         │
  assistant ────┤  MCP server (same core)           │       + library plugin   │
                │                         plugin host: plugin instances       │
                └──────────────────────────────────┬──────────────────────────┘
                                                    │
        iPhone backup · adb · Viber Desktop export · Telegram API · WhatsApp bridge · Meta export
```

- **The core** (Python package `chronika`): the only code that reads or writes the archive. Queries
  (streams, search, people, calls, timelines, statistics), changes (merge people, names, notes,
  decisions on media), and the import pipeline (normalise, deduplicate, store). Plain functions and
  classes, no web framework inside, so the API, the MCP server, the CLI and tests all call the same
  thing.
- **The API**: a thin layer over the core. REST for queries and changes, one WebSocket per open
  client for live events (new messages, sync progress, plugin status; typing indicators are out of scope).
- **The plugin host**: runs plugin instances: imports on demand or on a schedule, live connectors
  as long-running tasks; restarts them, reports their state.
- **The UI**: a PWA served by the same process; it talks only to the API.
- **The MCP server**: tools that call the core; runs in the same process (streamable HTTP) or as a
  separate stdio process opening the archive read only.
- **The CLI** stays: `python -m chronika ...` for imports and maintenance, also calling the core.

One process by default, so a single user runs `chronika serve` (or one container) and nothing
else. SQLite in WAL mode serves many readers and one writer, which fits: writes come from plugins
and the user's edits, serialised through one writer queue in the core.

## 3. Plugins

Plugins are of two kinds, under one system (manifest, instances, settings, secrets, setup):

- **Source plugins** bring messages, calls, people and media. A source plugin is one **way of
  reaching a service**, not a service: "WhatsApp from an iPhone backup", "WhatsApp live through a
  bridge", "WhatsApp from an Android backup" are three plugins.
- **Library plugins** are where kept pictures and videos go (5). In the first version: a folder on
  disk, and immich.

What follows describes source plugins; library plugins share the manifest and the instances, with
the interface of 5.

Each plugin declares a manifest:

| Field | Example |
|---|---|
| `id`, `name`, `service(s)` | `iphone-backup`, "iPhone backup", sms, imessage, calls, viber, whatsapp |
| `mode` | `import` (runs, brings what is new, stops) or `live` (stays connected) |
| `platforms` | linux, macos, windows (where its tools exist) |
| `needs` | a cable and libimobiledevice; a backup password (secret); an API login; a file |
| `settings` | a schema (JSON Schema) the UI turns into a form |
| `can_send` | whether messages can be sent through it |

and implements a small interface:

- `setup(ctx)`: guided steps for what it needs (a login with a code, a password, pairing); secrets
  go to the keyring through the core, never into settings.
- `sync(ctx, since)` (import): yields **normalised records**: messages, calls, people/handles,
  conversations, media references, each with its origin (`source`, `row_key`). Incremental: the
  plugin keeps its own cursor in `ctx.state`.
- `run(ctx)` (live): connects, yields the same records as they arrive; `send(ctx, conversation,
  content)` where `can_send`.
- `chats(ctx)`: the list of chats it can see, with kind and size, for the user's choice of what to
  import and whose media to fetch (select all, deselect all, filters).

A plugin knows nothing about the archive's tables; the core knows nothing about backups, adb or
Telethon. Today's importers (`chronika/sms.py`, `viber.py`, `whatsapp.py`, `telegram.py`, ...) and
extract scripts (`iphone-sync.py`, `android-export.py`, `telegram-sync.py`) become plugins: their
reading code moves, their writing code becomes the core's import pipeline.

**Instances.** A plugin can be added many times: two iPhones, four Android phones, two Telegram
accounts. Each instance has its own settings, secrets, state and device record. The user chooses
which instances feed the same archive.

**Third-party plugins.** At first, plugins are Python modules inside the project (entry points
later, so others can be installed as packages). They run in the same process; plugins from others
would need isolation (a subprocess with a narrow protocol) and are out of the first version.

## 4. Data model

The archive stays: it is general already. Changes:

- **Archive = bucket.** One user per installation in the first version, one archive; every plugin
  instance writes into it. Built so that users can be added later without a rewrite (12): the
  core takes the archive it works on as a parameter, never a global; sessions, plugin instances,
  secrets and settings carry the user they belong to; each user's archive is a file of its own.
- **`plugin_instance`** (new): id, plugin id, label, settings (JSON), state (JSON: cursors),
  enabled, `device_id`. Replaces the fixed source names (`iphone/sms`): `source` rows hang from an
  instance.
- **Devices and periods**: `device.used_from`/`used_until` editable in the UI ("this phone was in
  use from ... to ..."), and used by deduplication to choose which copy wins. Defaults: unknown,
  newest source wins.
- **Deduplication** in the core, per record kind, with the rules that exist now made general:
  service key where there is one (Viber token, WhatsApp stanza id, iMessage guid, Telegram
  chat/id); else a fingerprint (direction, counterpart, text, time within a tolerance); every
  origin kept; the winning copy chosen by device period, then by richness (the copy with more
  fields), then by first seen.
- **People**: `person`, `person_address` (auto/manual), `account` stay. Suggested merges (same
  number on two services, same contact) are shown to the user, never applied silently except where
  certain (the same phone number). Contacts from a CardDAV address book (any provider) are linked
  by `contact_uid`/`contact_url`; avatars from the contact, else from the service, else initials.
- **Media**: see 5. A library is a plugin instance (kind `library`); `library_link` points to that
  instance instead of a free-text name.
- **Removed**: `review`, `media_date`, `media_judgement` (never used; the old review state stays in
  its own files until the owner drops them), `viber_member` folded into person handles.
- **Text search**: FTS5 over a normalised copy of the text (lower case, accents removed, final
  sigma folded, Unicode NFKC), so `καλημερα` finds `καλημέρα`; a trigram index for CJK and for
  substrings, if measured worth its size.
- **Settings and UI state** that belong to the user (theme, language, pinned chats, muted chats)
  in the archive too, so every device sees them.

Scale: millions of messages, thousands of calls and people, a few GB. SQLite with the right
indexes answers a person's stream page or a search in milliseconds at that size; nothing larger is
needed.

## 5. Media

- **The media store** holds the files the archive has, by content (`media/<ab>/<sha256><ext>`, as
  now), in the **data** folder (not the cache: some files exist nowhere else once their source is
  gone). Thumbnails and previews are made on demand into the cache.
- **The library** is where kept pictures and videos go, and it is a **plugin** like the sources.
  In the first version two: `folder` (a folder on disk, files named by date, the date written into
  the file where it has none) and `immich` (API upload, as `immich-upload.py` does now). Others
  (Nextcloud, PhotoPrism, ...) are later plugins. A library plugin answers "is it already there?"
  (checksum; perceptual hash where the library allows; what it has from that day), stores a file
  with its date and the camera make where the file has none, and returns a reference for
  `library_link`. More than one library may be set up; the user chooses where each file goes, with
  a default.
- **Sorting**: some mechanism to choose what is kept, in the UI: a media view per person, chat or
  date, with keep / remove / send to library, multi-select, filters by kind and size. The newest
  decision on a file always wins. Local models (vision, faces) may help sort, as optional plugins;
  private pictures never leave the machine without consent.
- **Dates**: the file's own EXIF date, else the message date, else one the user gives.
- **Media on demand**: "the last two pictures X sent on WhatsApp": the core finds them, marks them
  wanted, checks the library, shows what is already there, stores only what the user approves.
- **Media of live services** are fetched only where the user enabled them per chat.

## 6. The UI

A PWA: one code base for desktop and mobile browsers, installable to the home screen, with push
notifications. Recommended stack:

| Need | Choice | Why |
|---|---|---|
| Language | TypeScript | types shared with the API (generated from its OpenAPI schema) |
| Framework | React with Vite | the largest ecosystem for what a messenger needs: virtualised lists, rich components, accessibility; long-term safety |
| Routing and server state | TanStack Router and TanStack Query | typed routes; caching, pagination and live updates of API data |
| Components | shadcn/ui (Radix primitives) with Tailwind CSS | accessible, themeable (light/dark), owned code rather than a dependency |
| Long lists | TanStack Virtual | chats of hundreds of thousands of messages, scrolled both ways |
| i18n | i18next (or Lingui) | plurals, dates and numbers per locale; Greek and English first |
| PWA | vite-plugin-pwa (Workbox) | service worker, offline shell, install, push |
| Tests | Vitest, Playwright | units and end-to-end in real browsers |

(React was chosen over SvelteKit, Vue and Solid; see 12.)

Screens: the people list (search, unread, pinned) and a person's unified stream (every service
interleaved, each message marked by its service, calls inline, replies, reactions, media,
jump to date); global search with filters (with dates alone, everything of those days, calls too; like the chat list, in the archived chats or in the others, as its archive button says); media views; people (merge, link to contact, names,
notes); sources (add a plugin instance, its setup, its state, its chat list); settings. Keyboard
shortcuts on desktop, gestures on mobile.

What a PWA can and cannot do (checked for iOS 27 / Safari 27 and Android Chrome, October 2026):

| | iPhone (installed to the Home Screen) | Android Chrome |
|---|---|---|
| Push notifications | yes, only once installed (since iOS 16.4) | yes, installed or not |
| Actions or inline reply in a notification | no: the user opens the app to reply | yes |
| Badge with the unread count | yes (Badging API) | no programmatic badge (Android's own dot) |
| Background sync or fetch | none; only a push wakes the service worker briefly | yes, limited |
| Receiving shares from other apps | no | yes, installed |
| Passkeys (Face ID, fingerprint) | yes | yes |
| Local storage | durable once installed; a Safari tab is wiped after 7 days unused | durable |
| Installing | since iOS 26 any site added to the Home Screen opens as an app, manifest or not | install prompt |

So the design holds: the server keeps every live connection and pushes; the app is a window onto
it, with no need for background work of its own; users are led to install it (on an iPhone, push
works only then). What is lost on an iPhone: reply from the notification and sharing into the app.
Apple has shown no sign of adding them; the UK's ruling on browser engines (due by 1 January 2027)
and the EU's DMA have not changed it so far. If they matter, the same UI is wrapped with Capacitor
(a native app with notification actions and a share extension), or Tauri on desktop, without
rewriting it. Live SMS and calls of an Android phone need a small native Android app, which is a
**plugin**, not the UI; on an iPhone that is not possible at all, and the backup stays.

## 7. The API

- Python, **FastAPI** (or Litestar) with Pydantic models; OpenAPI schema → generated TypeScript
  client; uvicorn.
- REST, cursor-paginated: `/people`, `/people/{id}/stream?before=&after=`, `/conversations`,
  `/search`, `/calls`, `/media`, `/plugins`, `/instances`, `/settings`.
- WebSocket `/events`: new records, plugin status, import progress.
- Server-side push (Web Push, VAPID) for new messages while the app is closed.

## 8. Security

Online, for one owner, with the strongest protection that does not get in the way:

- **Login with passkeys** (WebAuthn: fingerprint, face, security key), more than one per user;
  recovery codes printed once. Where a passkey cannot be made (some browsers or setups refuse it),
  a password together with a TOTP code from any authenticator app, both required (built on
  5 October 2026: the app must not depend on any one tool working).
- **Sessions**: HttpOnly, Secure, SameSite=Strict cookies; short-lived with silent renewal; every
  device listed and revocable.
- **Transport**: HTTPS only. Recommended exposure: behind a reverse proxy with automatic
  certificates (Caddy), or reachable only over a private network (WireGuard/Tailscale) for those
  who prefer not to expose it at all.
- **Hardening**: strict Content-Security-Policy, no inline scripts, CSRF protection on changes,
  rate limits on login, an audit log of logins and deletions.
- **Secrets** (backup passwords, service sessions, API keys) in the system keyring, as now; never
  in the archive, never sent to the browser.
- **Data at rest**: the archive and media are files of the user's; encryption at rest is the
  disk's (LUKS, FileVault, BitLocker). SQLCipher is possible but costs speed and tooling (open
  question 4).
- **The MCP server** has its own token and a read-mostly tool set; the few actions it can take are
  listed and confirmed.
- **Deletion** only on the user's explicit confirmation, with the exact list; the archive's
  records stay when files go.

## 9. The MCP server

Tools as calls into the core: search messages; a message with its context; a person (handles,
services, counts, first and last contact); a person's stream for a period; calls; a day's timeline
across services; statistics; media of a person. Harmless actions only (a note, a name), each
confirmed. Built with the official MCP Python SDK.

## 10. Platforms and installation

Linux first; macOS and Windows where the plugins' tools exist (each plugin says where it runs).
`uv` for development; for users a package (`pipx`/`uv tool install chronika`) and a container
image. The generality fixes found by the audit of October 2026 come first: time zones on Windows,
safe SQLite paths, UTF-8 file I/O, the media store in the data folder, phone numbers without a
region, messages through i18n.

## 11. Order of work

1. Generality fixes that do not depend on this design: done on 5 October 2026 (time zones on
   Windows with `tzdata` and `tzlocal`, read-only SQLite paths through `config.read_only()`, UTF-8
   for every text file and the output of child programs).
2. The core as a library: queries and changes over the current archive, with tests; the plugin
   interface; today's importers turned into plugins without changing what they store.
3. The schema: `plugin_instance` (sources and libraries), device periods editable, the unused tables removed,
   and search that folds accents and final sigma (the text normalised by the core as it writes,
   not by a trigger: a trigger would need a Python function in every connection that writes); the
   owner's archive brought to it once, with a check that nothing is lost.
4. The API and the UI's first version: people, a person's stream, search, sources; passkey login.
5. Live: Telegram, then WhatsApp; WebSocket events and push.
6. Media: the store in the data folder, the folder and immich library plugins, the media views, media on
   demand.
7. The MCP server over the core (it can come earlier, being small).
8. Sending, where plugins can; the Android companion app; Messenger.

## 12. Decisions (5 October 2026)

1. **Users**: one per installation in the first version; the core, the API and the stored state are
   built so that several users (each with an archive of their own, fully apart) can be added later
   without a rewrite.
2. **Sending**: yes, through the plugins that can (`can_send`); it comes after reading, late in the
   order of work.
3. **Exposure**: both. The application is made safe to put on the internet (passkeys, HTTPS, strict
   CSP, rate limits); each user chooses to expose it behind a reverse proxy or to keep it on a
   private network (WireGuard, Tailscale). The documentation describes both.
4. **Encryption at rest**: the disk's (LUKS, FileVault, BitLocker); the archive stays plain SQLite.
5. **WhatsApp live** (whatsmeow): reading on; sending is a setting of the plugin instance, off by
   default, with the risk to the account stated where it is turned on.

6. **UI framework**: React (compared with SvelteKit, Vue and Solid: the chat list over hundreds of
   thousands of messages, the components and the ecosystem decided it).

## 13. As built (October 2026)

Everything above is built, with these differences from the draft:

- **Importers as plugins**: the source plugins wrap the existing extract scripts and importers
  (which write through `Archive`); turning them into sources that yield records for a core
  pipeline is still to do. Deduplication is the importers' own (keys, fingerprints, device
  periods), now with device periods editable in the app.
- **Chat streams** use react-virtuoso (reverse scrolling with prepended pages, follow-output) rather
  than TanStack Virtual; the API's TypeScript types are written by hand (`web/src/lib/api.ts`)
  rather than generated.
- **Search** is a contentless FTS5 index of folded text (`chronika/text.py`), written by the archive
  as it adds a message; the snippet is made from the original text.
- **Users, passkeys, sessions** live in `<data>/server.db`, apart from the archives; each user row
  names its archive, so a second user is a second row and a second archive.
- **Plugins' words** are English in the code, translated per language (`chronika/plugins/i18n.py`).
- **Choosing chats** is a plugin's `chats()`; Telegram has it (import and media per chat).
- **Media on demand** is in the MCP server (`find_media`, `send_media_to_library`) and in the app
  (the media view and the lightbox: keep, remove, to the library, each checked against it first).
- **The MCP SDK** is version 2 (`MCPServer`).
- **Nothing per service is fixed in the core or the interface**: each plugin declares it.
  - `service_info` gives how a service looks: name, colour, its icon (an SVG path, shown where the
    service is chosen to send through), whether it is calls only. A chat answers by default through
    the service it was last active on; one nothing can send to now still shows, with a lock for Send.
  - `name_weights` gives how much the names it brings are trusted. An address book declares
    `contacts`, the highest by default.
  - `can_send` and `sending(ctx)` say whether it can send now. By default it can when set up; a
    plugin may add a setting for it.
  - The server combines these declarations (`/api/services`, `/api/names`, a chat's `sendable`).
  - The user's own order of names (Settings → Names) overrides the weights, and can go back to them.
  - Text shown to the user never names a plugin or a service as the way out.
  - `live_default`: a plugin with a live connection may want it on by default. The host then starts
    it once the plugin is set up, unless the user turned it off. Telegram, on connecting, first
    brings what arrived while it was not connected.
- **Names** come from sources (an address book, and each service's names by kind: its copy of the
  user's address book, a chat's name, a name people chose); the plugins weigh them, the user orders
  them (Settings → Names) or pins one source or handle for a person; every name seen is kept, with
  when ("also known as"). Names shared across people are suggested merges (a contact listing both,
  the same name in a service's address book copy, the same rare name), never applied; the user can
  turn one down. Self-chosen names are marked (~) in groups.
- **A chat's state**: muted, pinned and read up to come from what the sources report
  (`state_report`: the iPhone's WhatsApp, the bridge's store, Telegram live as it changes) and what
  the user chose (`chat_state`). Between services the plugins' weights decide; between the user
  and the services the later change wins, unless the user chose "always". Chat info shows what
  each service says. **Archived is the app's own**: decided once, when the app first sees a chat,
  from what the services say then (`Archive.init_archived`: a person's chat archived only if every
  conversation a service reports on is archived there, one in view keeping them in view; a chat no
  service reports on, not archived), and from then on only the user changes it. New messages do
  not: an archived chat gets them like any other, without notifications.
  When two people are merged, the chat is archived only if both were; an address split off keeps
  the archived of the chat it left.
- **Merged groups**: the user can merge group chats (the same people on two services, or a group
  made again) into one chat, `c<id>` of the first, its name the latest one's (`group_link`); the
  app suggests groups with mostly the same members, or the same name and someone in both. A group
  can leave again, archived as the chat it left; if it is the one whose id the chat has, the others
  keep the chat and the user's choices under the latest one's id. Archived after a merge as for
  people.
- **Changing the ways in** (a password, a passkey, recovery codes, an MCP token) needs a setup link,
  or a session that signed in within the last 15 minutes. A stolen session cannot add its own way in.
  Failed password sign-ins lock that address only, so whoever knows the name cannot lock the user
  out.
- **Words**: every word the user reads is translated (Greek and English). The interface's words
  are in `web/src/lib/i18n.ts`; the server's errors are codes (`UserError`) said by the interface;
  what the server says by itself (logs, statuses, notifications, a plugin's errors) is English in
  code, said in the language the user last chose (setting `language`, or the request's `X-Lang`)
  through `plugins/i18n.py`. `tests/test_i18n.py` and tsc keep it so.
- **A demo or a test** keeps its secrets apart (`CHRONIKA_KEYRING`), so it never connects to the
  user's accounts.

Where things are: `chronika/core/` (store, names, queries, changes), `chronika/plugins/`,
`chronika/server/` (app, auth, host, push, users), `chronika/mcp_server.py`, `chronika/demo.py`,
`web/` (the PWA), `tests/` and `web/e2e/`. `docs/app.md` tells how to run it.
