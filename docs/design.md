# Everysaid: how the app is built

The archive, the importers and the sources are described in `README.md` and
`docs/data-sources.md`; `docs/go.md` tells how to build, run and develop the program, `docs/app.md`
how to run the app.

## 1. What it is for

One messenger for a person's whole history: each person shows the whole conversation across every
service (SMS, iMessage, RCS, phone and FaceTime calls, Viber, WhatsApp, Telegram, Signal, the old
messengers of Adium and Pidgin logs), old and new, in one stream. Like WhatsApp or Telegram in look
and speed, like Pidgin in reach. Online, on desktop and mobile; light and dark; in the user's
language; secure without getting in the way. An assistant reaches the same history through an MCP
server.

What it is not: a backup tool (keeping the data safe is the user's concern), nor a replacement for
the services themselves (it reads them; where a service allows, it also sends).

## 2. Shape

```
                ┌──────────────── one process: everysaid serve ────────────────┐
  browser /     │  HTTP API (REST + WebSocket)  ←→  core  ←→  archive.db       │
  installed PWA ┤                                   ↑   ↑                      │
                │  the PWA's files (in the binary)  │   └── media store         │
  assistant ────┤  MCP server (/mcp, same core)     │       + library plugins   │
                │                         plugin host: plugin instances       │
                └──────────────────────────────────┬──────────────────────────┘
                                                    │
   iPhone backup · adb · Adium/Pidgin logs · Telegram API · WhatsApp (whatsmeow) ·
   Viber Desktop (bridge) · Signal (helper) · CardDAV / .vcf · immich / folder · Ollama
```

One program, `everysaid` (Go, one static binary, `CGO_ENABLED=0`; see `docs/go.md`):

- **The core** (`internal/core`): what the app, the MCP server and the tests read and change:
  queries (chats, a person's stream, search, people, calls, the day's timeline, statistics, media)
  and changes (merging and splitting people and groups, names, notes, labels, chat state, decisions
  on media). Plain functions over a `Store`, no web framework inside, so every caller gets the same
  answers.
- **The archive** (`internal/archive`): the schema and the helpers the importers write through;
  **the importers** (`internal/importers`) read each source's databases and files into it,
  deduplicating as they go.
- **The API** (`internal/server`): a thin layer over the core. REST for queries and changes, one
  WebSocket per open client for live events (new messages, sync progress, plugin status; typing
  indicators are out of scope).
- **The plugin host** (`internal/server/host.go`): runs plugin instances: imports on demand, live
  connections as long-running tasks; restarts them, reports their state.
- **The UI** (`web/`): a PWA built into the binary (`internal/webui`), served by the same process;
  it talks only to the API.
- **The MCP server** (`internal/mcp`): tools that call the core; mounted in the server at `/mcp`
  (streamable HTTP, with a token), or `everysaid mcp` over stdio for an assistant on the same
  machine.
- **The command line** (`cmd/everysaid`): `serve`, `user`, `mcp`, `demo`, `import`, and the extraction
  commands (`iphone-sync`, `iphone-ls`, `iphone-verify`, `android-export`, `telegram-sync`).

One process by default, so a single user runs `everysaid serve` and nothing else. SQLite in WAL
mode serves many readers and one writer, which fits: the core reads through a pool of read-only
connections and makes every change through one writer, one transaction at a time; imports hold a
lock of their own while they write.

## 3. Plugins

Plugins are of four kinds under one system (manifest, instances, settings, secrets, actions,
logs): **source**, **library** (where kept pictures and videos go, 5), **contacts** (address books)
and **analysis** (local models, 6). They are Go packages compiled into the binary, each registering
itself, all loaded by `internal/all`; there are no plugins from others (they would need isolation,
a subprocess with a narrow protocol).

A source plugin is one **way of reaching a service**, not a service: "WhatsApp from an iPhone
backup" and "WhatsApp live" are two plugins. The plugins today:

| Kind | Plugins |
|---|---|
| source | `iphone-backup`, `android-adb`, `carrier-notices`, `im-logs` (`internal/plugins/sources`); `telegram`, `whatsapp-bridge`, `signal`, `viber-desktop` (packages of their own) |
| library | `folder`, `immich` (`internal/plugins/libraries`) |
| contacts | `carddav`, `vcard-file` (`internal/plugins/contacts`) |
| analysis | `ollama` (`internal/plugins/analysis`) |

Each plugin declares a manifest (`plugins.Info`, `internal/plugins/base.go`): id, name, kind,
services, description; `modes` (`import`: runs, brings what is new, stops; `live`: stays
connected) and `live_default`; `platforms` (where its tools exist); `needs`, in plain words; its
settings (typed fields the UI turns into a form; a `secret` field goes to the keyring through
`internal/config`, never into the settings); what it can do (`can_send`, `can_reply`,
`can_mention`, `can_mark_read`, `can_send_files`, `can_react`, `can_edit`, `can_delete`, with the
service's time limits, `can_report_spam`); extra actions (a QR link, an unlink); how its services look; and the
weights of the names and the chat state it brings (7).

Beyond the manifest a plugin implements only the interfaces it needs: `RunImport` (an import),
`Live` (a connection), `Send`, `React`, `Edit`, `Delete`, `MarkRead`, `FetchMedia`, `ReportSpam`
(report, block and delete the chat on the service), `Forget` (drop its own copy of a chat removed
as spam), `Chats` (the
chats it can see, with kind and size, for the user's choice of what to import and whose media to
fetch; Telegram and Signal have it), `Asks` (what the user types for one run only, such as a backup
password not kept), `Action`, `Check`; a library `Find`, `Store`, `Fetch`; a contacts plugin
`Sync`.

The source plugins run the extraction and the importers: an import extracts what is new (an iPhone
backup, adb, Telegram's API) and runs the importers on it, which write through `internal/archive`;
a live plugin keeps its own store (whatsmeow's, the Viber bridge's snapshot, Telegram's,
Signal's) and imports from it as things arrive. Planned (README, "Plans"): importers that yield
records for one import pipeline in the core.

The live sources:

- **Telegram** (`internal/telegram`, gotd): in the process; on connecting it first brings what
  arrived while it was not connected.
- **WhatsApp** (`internal/whatsapp`, whatsmeow): in the process, linked as a device by a QR code.
  Unofficial, so sending is a setting of the instance, off by default, with the risk to the account
  stated where it is turned on, and turned off by itself when WhatsApp warns the account.
- **Signal** (`internal/signal`): a separate helper program, `everysaid-signal` (`bridges/signal`,
  Rust, on presage, AGPL-3.0), started by the plugin and spoken to in JSON lines on its stdin and
  stdout; none of its code is linked into `everysaid`.
- **Viber** (`internal/viber`): through the running Viber Desktop on Linux, driven from inside by
  Everysaid's bridge (`bridges/viber`, an `LD_PRELOAD` library; `docs/viber-bridge.md`).

**Instances.** A plugin can be added many times: two iPhones, several Android phones, two Telegram
accounts. Each instance (`plugin_instance`) has its own label, settings, secrets, state (its
cursors), log and device; the user chooses which instances feed the archive.

## 4. Data model

The schema is `internal/archive/schema.sql`, version 1 until the first release (until then it
changes in place).

- **One archive per user.** The core takes the archive it works on as a parameter, never a global.
  Users, passkeys, sessions, push subscriptions, MCP tokens and the audit log live in
  `<data>/server.db`, apart from the archives; each user row names its archive, so a second user is
  a second row and a second archive. One user per installation for now (planned: several, README).
- **`plugin_instance`**: plugin, kind, label, settings (JSON), state (JSON), enabled, device,
  whether it is the default library, last run and status. `source` rows (one database or folder
  read) hang from an instance.
- **Devices and periods**: `device.used_from`/`used_until`, editable in the app (Sources →
  Devices: "this phone was in use from ... to ..."), choose which copy of a record found on two
  devices is kept: the one in use at the time, else the newest device.
- **Deduplication** by the importers, per record kind: the service's own key where there is one
  (Viber token, WhatsApp stanza id, iMessage guid, Telegram chat and id); else the message's time,
  direction, kind and text within its conversation; every origin kept (`message_origin`,
  `call_origin`) but the Adium and Pidgin logs', which have no ids.
- **People**: `address` (one handle), `person`, `person_address` (`auto`, or `manual` when the
  user merged), `account` (the user's own handles), `handle_name` (every name a service showed for a
  handle). Suggested merges are shown to the user, never applied by themselves; the same phone
  number on two services is one address, so one person. Contacts from an address book (`contact`,
  `contact_address`) are linked by `contact_uid`/`contact_url`; an avatar is the contact's photo,
  else initials. Viber member ids are kept in `viber_member`.
- **Media**: `media` (by content), `attachment`, `library_link` (pointing to a library instance),
  `media_same` (a copy linked to the kept one), `media_decision` (5).
- **Text search**: two contentless FTS5 indexes of folded text (`internal/text`: case folded,
  accents and other marks removed, final sigma made σ, compatibility forms made one), so `καλημερα`
  finds `Καλημέρα`: `message_fts` by words and `message_tri` by trigrams (parts of words). The
  archive writes them as it adds a message, not a trigger, so that every connection that writes
  needs no custom function; the snippet is made from the original text.
- **Notices** (`notice`: a message's code and values, JSON) say what a notice or a message's context
  is in a form the interface puts in the user's language, whatever the source: the importers write
  the codes below, and the UI and the MCP read them without knowing the service. A person in the
  values is `{"address": id}` (the core adds `name`, `person_id`, `me`), the owner `{"self": true}`;
  `by` is who did it. A source adds them as it learns its service's events; a message without one
  is shown by its text and subtype as before.
  - `group`: `actions`, each `{"type", ...}`: `created` (`title`), `added`, `removed`, `joined`, `joined_link` (by the group's link), `left`,
    `invited`, `invite_accepted`, `invite_declined`, `invite_revoked`, `requested`,
    `request_approved`, `request_denied`, `request_withdrawn` (each with `who`); `admin` (`who`,
    `on`); `title` (`title`); `description` (`text`); `avatar`; `timer` (`seconds`, 0: off);
    `access_info`, `access_members`, `access_link` (`level`: anyone, members, admins, off);
    `link_reset`; `announcements` (`on`); `approval` (joining needs an admin's approval: `on`,
    absent where the service does not say which); `banned`, `unbanned` (`who`); `ended`; `topic` (a
    forum's: `created`, `title`, `closed`). No actions: the
    group changed, how is not known.
  - `timer` (`seconds`, 0: off), `pin` (`seconds`, null: for good) and `unpin` (the message is the
    one it answers), `poll` (on the poll itself: `question`, `options` `[{text, votes}]`,
    `multiple`, `voters`, `ended`, kept as the votes change; an option's `votes` null where the
    service gave only how many voted), `poll_end` (answers the poll), `group_call`,
    `payment`, `gift`, `unsupported` (made by a newer version of the service), `unreadable` (could
    not be decrypted), `view_once` (a view-once message, which only the phone can open), `story_reply` (a message answering a story), `story_reaction` (`emoji`), `story` (a story shared; `mention`: one that names the owner),
    `signed_up` (someone the owner knows joined the service), `screenshot`.
- **The user's settings** shared by every device (theme, language, names' order, hidden services,
  labels' settings) are in the archive (`setting`), and so is a chat's state the user chose
  (`chat_state`).

Scale: millions of messages, thousands of calls and people, a few GB. SQLite with the right indexes
answers a person's stream page or a search in milliseconds at that size; nothing larger is needed.

## 5. Media

- **The media store** holds the archive's files by content (`media/<ab>/<sha256><ext>`), in the
  **data** folder by default (config `[media] store`), not the cache: some files exist nowhere else
  once their source is gone. Thumbnails are made on demand into `<cache>/thumbs`.
- **Libraries** are plugins: `folder` (a folder on disk, year/month subfolders, files named by date
  and service) and `immich` (upload through its API). A library answers "is it already there?" (by
  checksum), stores a file with its date and the camera make written in where the file has none
  (with exiftool), and returns a reference for `library_link`. More than one may be set up; a file
  goes to the default one unless the user chooses another.
- **Sorting** in the app: the media view (per person, chat or all, filters by kind) and the
  lightbox: keep, remove, to the library, each checked against the library first. The newest
  decision on a file wins (`media_decision`). Private pictures never leave the machine without
  consent.
- **Dates**: the file's own EXIF date, else one the user gives, else the message date.
- **Media on demand**: "the last two pictures X sent on WhatsApp": the MCP server finds them
  (`find_media`), checks the library and stores only what the user approves
  (`send_media_to_library`); `download_media` may ask a live source for a file the archive never
  had.
- **Media of live services** are fetched as the instance's settings say: the files of new
  messages as they arrive, and on connecting those of the last week still missing; for Telegram
  also a chat's whole history, per chat.

## 6. The UI

A PWA: one code base for desktop and mobile browsers, installable to the home screen, with push
notifications.

| Need | Choice | Why |
|---|---|---|
| Language | TypeScript | the API's types written by hand (`web/src/lib/api.ts`), checked by tsc |
| Framework | React with Vite | the largest ecosystem for what a messenger needs: long lists, rich components, accessibility; long-term safety (over SvelteKit, Vue and Solid) |
| Routing and server state | TanStack Router and TanStack Query | typed routes; caching, pagination and live updates of API data |
| Components | Radix primitives with Tailwind CSS | accessible, themeable (light/dark), owned code rather than a dependency |
| Long lists | react-virtuoso | chats of hundreds of thousands of messages, scrolled both ways, pages added above, following new output |
| i18n | i18next | plurals, dates and numbers per locale; Greek and English |
| PWA | vite-plugin-pwa (Workbox) | service worker, offline shell, install, push |
| Tests | Vitest, Playwright (`web/e2e`) | units and end-to-end in real browsers, desktop and mobile |

Screens: the chat list; a chat (a person's unified stream: every service interleaved, each message
marked by its service, calls inline, replies, reactions, edits, media, jump to date); search with
filters (dates alone give everything of those days, calls too; like the chat list, in the archived
chats or the others); calls; media; people (merge, split, names, notes, labels), the merge
suggestions, the people without a name; overview; sources; settings.

What a PWA can and cannot do (iOS 27 / Safari 27 and Android Chrome, as of October 2026):

| | iPhone (installed to the Home Screen) | Android Chrome |
|---|---|---|
| Push notifications | yes, only once installed | yes, installed or not |
| Actions or inline reply in a notification | no: the user opens the app to reply | yes |
| Badge with the unread count | yes (Badging API) | no programmatic badge (Android's own dot) |
| Background sync or fetch | none; only a push wakes the service worker briefly | yes, limited |
| Receiving shares from other apps | no | yes, installed |
| Passkeys (Face ID, fingerprint) | yes | yes |
| Local storage | durable once installed; a Safari tab is wiped after 7 days unused | durable |
| Installing | any site added to the Home Screen opens as an app | install prompt |

So the server keeps every live connection and pushes; the app is a window onto it, with no
background work of its own; users are led to install it (on an iPhone, push works only then). What
is lost on an iPhone: reply from the notification and sharing into the app. If they matter, the
same UI can be wrapped with Capacitor (a native app with notification actions and a share
extension), or Tauri on desktop, without rewriting it (planned only if wanted, README). Live SMS
and calls of an Android phone need a small native Android app, which would be a **plugin**, not the
UI (planned); on an iPhone that is not possible at all, and the backup stays.

## 7. Behaviour

- **Nothing per service is fixed in the core or the interface**: each plugin declares it.
  - How a service looks (name, colour, short name, icon as an SVG path, whether it has messages or
    only calls). A chat answers by default through the service it was last active on; one nothing
    can send to now still shows, with a lock for Send.
  - How much the names it brings are trusted (`name_weights`, by kind: its copy of the user's
    address book, a chat's name, a name people chose); an address book declares `contacts`, the
    highest by default.
  - Whether it can send now: by default when set up; a plugin may add a setting for it.
  - The server combines these declarations (`/api/services`, `/api/names`, a chat's `sendable`).
    Text shown to the user never names a plugin or a service as the way out.
  - A plugin with a live connection may want it on by default (`live_default`): the host starts it
    once the plugin is set up, unless the user turned it off.
- **Names**: the plugins weigh them, the user orders them (Settings → Names, which can go back to
  the weights) or pins one source or handle for a person; every name seen is kept, with when ("also
  known as"). Self-chosen names are marked (~) in groups.
- **Merge suggestions**: names shared across people (a contact listing both, the same name in a
  service's address book copy, the same rare name, or names that sound the same whatever the
  accents, word order or alphabet), never applied by themselves: the user sees them side by side
  with a little of each one's history and ticks who is one, one suggestion at a time or all of them
  on a page of their own (ticked, but for names that only sound alike; applied together). Who is
  left out, or a suggestion turned down, is not suggested with them again; those pairs are listed
  there too, and each can be suggested again.
- **People without a name** (only a number or handle) always show, after all the named ones, in
  the chats, calls and people. They have a page of their own, those with the most messages first,
  with a little of each one's history: a name for them, or the person they are. **Names found** for
  them, from their handles (an email's or a user name's words, where one is a first name the
  archive knows: `first.last@…` is "First Last") and from the local analysis, are shown there and on
  the person's page, accepted with a click or turned down for good; never applied by themselves.
- **Labels** describe people: the tone of their chats (friendly, professional, romantic…, many to
  a person) and who they are to the user (friend, relative, client…, one). The lists are the
  user's (Settings → Labels): the app starts them with a few, in its languages by key, and the user
  renames, adds, orders, merges and removes them. Each label has a meaning, which is what the local
  models read to judge by (none: given only by the user), and may be sensitive. A person's label is
  the user's (yes; or no: never suggested again) or the models' (suggested, with their votes and a
  line of the chat), and the models' never touch the user's. On a merge the labels go over and the
  person is read again; on a split, too. The models' labels show only when the user asks
  (`show_tone`), the assistant sees labels only when allowed (`mcp_labels`); "forget the analysis"
  takes away all the models said and keeps the user's. Labels show on the list of people too,
  which is filtered by one with a click.
- **Local analysis** (`ollama`, kind `analysis`): Ollama on this computer or its own network
  (another address is refused), one model or two or three that vote. It reads a little of each chat
  (the first lines, lines spread over all of it, lines that name someone), largest chats first, in
  the background while turned on, and again when a chat grows by half. A name counts only where its
  words are in what it read (in any case, with or without a surname, as one), never an email, a
  handle or the owner's; a tone needs most of the models, a sensitive one two of them each with a
  line copied from the chat. The prompt is built from the user's lists, so a new label is judged by
  its meaning; those read by another list are read again when the user asks. **Analyse now**, in a
  person's chat info, reads their chat at once (a run with the action `person:<id>`).
- **The chat list's filters**, in a small panel kept on the device: the archived chats, at least
  or at most so many messages (a slider in growing steps: 0, 1, 2, 3, 5, 10, 20, 50, 100 … 5000),
  each service as must have, must not have or either, and a label.
- **Hidden services** (Settings → Services, `hidden_services`): nothing of them shows anywhere
  (chats, streams, calls, search, media, the day's timeline); the archive keeps all of it.
- **Hidden accounts** (`hidden_accounts`), where the user has several on a service (several MSN
  accounts): the chats held only by the hidden ones show nowhere; a chat also on an account shown
  stays. Which account a chat was on is the user's address among its members (the Adium and Pidgin
  importer writes it, from the log folder of each account).
- **Short numbers** (five digits or fewer: carriers, banks, services) stay out of the chats, calls
  and people unless the user shows them (Settings → Names, `show_short_numbers`, off by default); a
  person is left out only when every handle of theirs is one; a search still finds them.
- **Groups with no one else** (everyone left, or the source listed no members) stay out of the chat
  list unless the user shows them (`hide_empty_groups`, on by default); a search still finds them,
  and one with an unread message still shows.
- **Nothing empty is shown**: the chat list leaves out chats with no message and no call, and the
  people list people with nothing in the archive (a source may leave such: a chat it lists with
  nothing in it, a handle no message came from), so a stray row of an import never reaches the user.
- **A chat's state**: muted, pinned and read up to come from what the sources report
  (`state_report`: the iPhone's WhatsApp, the live sources as it changes) and what the user chose
  (`chat_state`). Between services the plugins' weights decide; between the user and the services
  the later change wins, unless the user chose "always". Chat info shows what each service says.
  **Archived is the app's own**: decided once, when the app first sees a chat, from what the
  services say then (`Archive.InitArchived`: a person's chat archived only if every conversation a
  service reports on is archived there, one in view keeping them in view; a chat no service reports
  on, not archived), and from then on only the user changes it. New messages do not: an archived
  chat gets them like any other, without notifications. When two people are merged, the chat is
  archived only if both were; an address split off keeps the archived of the chat it left.
- **Merged groups**: the user can merge group chats (the same people on two services, or a group
  made again) into one chat, `c<id>` of the first, its name the latest one's (`group_link`); the
  app suggests groups with mostly the same members, or the same name and someone in both. A group
  can leave again, archived as the chat it left; if it is the one whose id the chat has, the others
  keep the chat and the user's choices under the latest one's id. Archived after a merge as for
  people.
- **Back**: every page reached from another has a way back to it, on every screen size; one opened
  from the app's menu has none (the menu is the way on), and one opened directly goes back to its
  section (a person's page to People, the calls or media of a chat to the chat). On a wide screen
  a chat shows back only when it was opened from another page (the list is beside it).
- **Where a chat was being read** is kept for the session (this tab, a reload included), not
  across sessions; a chat opened afresh stays at its end while what is drawn grows to its real
  height (pictures, previews), until the user scrolls.
- **Settings** are in tabs: general, names, labels, services, security. **Sources** are in tabs by
  kind (sources, libraries, contacts, analysis) and devices; the local analysis has a start and
  pause button, and an action shows only when it has something to do.
- **Words**: every word the user reads is translated (Greek and English). The interface's words
  are in `web/src/lib/i18n.ts`; the server's errors are codes (`internal/errs`, `UserError`) said
  by the interface; what the server says by itself (logs, statuses, notifications, a plugin's
  errors, the command line) is English in code, said in the language the user last chose (setting
  `language`, or the request's `X-Lang`) through `internal/i18n`. `internal/checks` and tsc keep it
  so.
- **A demo or a test** keeps its secrets apart (`EVERYSAID_KEYRING`) and its folders apart
  (`EVERYSAID_DATA`, `EVERYSAID_CACHE`, `EVERYSAID_CONFIG`), so it never touches the user's archive
  or accounts. `everysaid demo` makes an archive of invented people.

## 8. The API

- Go's `net/http` (`internal/server`), JSON under `/api`: `/api/chats`, `/api/chats/{id}/stream`,
  `/api/people`, `/api/search`, `/api/calls`, `/api/media`, `/api/plugins`, `/api/devices`,
  `/api/labels`, `/api/settings`, `/api/auth/...` and the rest; long lists are paginated. Every
  call the interface makes is checked against the routes by the server's tests.
- WebSocket `/api/events`: new records, plugin status, import progress; a ping every 25 seconds.
- Web Push (VAPID) for new messages while the app is closed.

## 9. Security

Online, for one owner, with the strongest protection that does not get in the way:

- **Login with passkeys** (WebAuthn: fingerprint, face, security key), more than one per user. The
  first is made through a one-time setup link printed on the terminal (`everysaid serve` while there
  is no user, `everysaid user link` any time). Ten recovery codes, each once. Where a passkey cannot
  be made (some browsers or setups refuse it), a password together with a TOTP code from any
  authenticator app, both required, so that the app does not depend on any one tool working.
- **Changing the ways in** (a password, a passkey, recovery codes, an MCP token) needs a setup
  link, or a session that signed in within the last 15 minutes, so a stolen session cannot add its
  own way in. Failed password sign-ins lock that address only, so whoever knows the name cannot lock
  the user out.
- **Sessions**: a random token in an HttpOnly, SameSite=Strict cookie (Secure over HTTPS), only its
  hash stored; every device listed and revocable; idle ones end after 30 days.
- **Transport**: the server listens on plain HTTP, on localhost by default; HTTPS comes from a
  reverse proxy with automatic certificates (Caddy), or the app stays on a private network
  (WireGuard, Tailscale) for those who prefer not to expose it at all. Both are described in
  `docs/app.md`.
- **Hardening**: the Host must name this server (no DNS rebinding); a strict Content-Security-Policy
  (only the server's own scripts), no framing, no referrer, HSTS over HTTPS; changes need the header
  `X-Everysaid: 1` and this server's Origin (with SameSite cookies, no CSRF); login attempts limited
  per address; an audit log of logins, changes to the ways in and removals.
- **Secrets** (backup passwords, service sessions, API keys) in the system keyring, or in files of
  mode 600 under the config folder where there is none; never in the archive, never sent to the
  browser.
- **Data at rest**: the archive stays plain SQLite; encryption at rest is the disk's (LUKS,
  FileVault, BitLocker), as SQLCipher would cost speed and tooling.
- **The MCP server** over HTTP needs its own token (`Authorization: Bearer`); reading is free, and
  the few changes it can make are listed (10).
- **Deletion** only on the user's explicit confirmation, with the exact list; the archive's records
  stay when files go.
- **Spam**: someone the user has not named and no contact lists may be removed as spam, after a
  dialog that says what goes (`core/spam.go`): their one-to-one chats with every message and file
  only they used, their calls, the names services showed for them; what they wrote in groups stays,
  as do their address and person, recorded in `spam`, so that every import removes again what the
  sources bring of them (`archive.PurgeSpam`, in `Resolve` and at the end of the call importers).
  The user may let them back (Settings → Names). Where a source can (`ReportSpam`), the service is
  told too: Telegram reports, blocks and deletes the chat; WhatsApp blocks. People blocked on a
  phone or a service (`blocked`: Android's export, Telegram's and WhatsApp's blocklists, as they
  change) are suggested for removal: one line on the People page leads to their page
  (`/people/blocked`), where each is marked spam or not spam and all are applied together
  (`POST /api/spam/apply`), after a dialog that adds up what goes. Those said not to be spam are
  listed apart there, each can be suggested again.

## 10. The MCP server

Tools as calls into the core, with the official Go SDK (`github.com/modelcontextprotocol/go-sdk`,
`internal/mcp`): search messages, list and read chats, a message with its context, find people, a
person (handles, services, counts, first and last contact), a person's direct chat, the last
interaction, messages and calls of a period, a day's timeline across services, statistics, media
(`find_media`, `download_media`). The changes it can make are a person's name and note and storing
a file in the photo library (`send_media_to_library`), each with the user's approval. Times are
given in the user's time zone.

## 11. Platforms and installation

Linux first; macOS and Windows where the plugins' tools exist (each plugin says where it runs; the
Viber bridge is Linux only). For users one static binary per system, built for Linux, macOS and
Windows on amd64 or arm64 (`docs/go.md`), and, where Signal is wanted, its helper beside it.

## 12. Where things are

`internal/core/` (store, names, queries, changes, labels), `internal/archive/` (schema, writing),
`internal/importers/`, `internal/plugins/` and the packages of the live sources
(`internal/telegram`, `internal/whatsapp`, `internal/signal`, `internal/viber`), `bridges/` (the
Signal helper, the Viber bridge), `internal/server/` (routes, auth, host, push, events),
`internal/mcp`, `internal/demo`, `cmd/everysaid` (the binary), `web/` (the PWA); tests beside the
code they test, `internal/checks` over the whole code, and `web/e2e/`.
