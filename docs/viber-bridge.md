# Viber: live send and receive through Viber Desktop

A feasibility study for a Viber bridge in the spirit of `bridges/whatsapp/`, able to
**send** and **receive** on the owner's own account — not just read old history as the existing export
does (`scripts/viber-desktop-export.cpp`, see `docs/data-sources.md`). The outcome: send, receive,
replies, reactions and media all work, driving the running Viber Desktop client from inside.

## The problem

Viber has no client API. Its Bot REST API is a separate bot account with a webhook: it never sees the
owner's own chats, cannot join groups, and since February 2024 new bots are commercial only. Every
third-party effort surveyed works around the absence of an API, not with one:

- **Official Bot API** wrappers (`Viber/viber-bot-python`, `mileusna/viber`, and the "mautrix-viber"
  that is not part of mautrix) — bot account, not the owner's. Not usable.
- **UI automation** of Viber Desktop (`rusty4444/viber-matrix-bridge`, Windows UI Automation) — text
  only, breaks on every Viber update, abandoned by its author.
- **Database export** of the running client (`r0073rr0r/ViberPC-Export` via frida, Windows;
  `nemanjacosovic/viber-export-macos` via lldb, macOS) — read-only history, no send, no live receive.
- Beeper, Matterbridge, BitlBee, libpurple/Pidgin — no Viber at all.
- Academic work covers only traffic/call analysis, not the message protocol, which is proprietary over
  TCP. Reverse-engineering the wire protocol is out of scope (and against Viber's terms).

So the message protocol is a dead end, but the running Viber Desktop client already speaks it. The
approach is to drive that client from inside, the way GammaRay inspects any Qt app.

## The method

Viber Desktop on Linux is a Qt 6.10 application. Two facts make it drivable:

1. **Qt's object hooks.** Qt exports `qtHookData`, a table GammaRay and other tools use to be notified
   of every `QObject` created or destroyed. An `LD_PRELOAD` library that installs hooks there keeps a
   live set of all of Viber's objects — including the ones that send and receive.
2. **The Qt meta-object system.** Viber's binary is stripped, but every class keeps its meta-object:
   method names, signal/slot signatures, and properties survive. A method is invoked by name with
   `QMetaObject::metacall` (ChatID/EventID are plain 8-byte values; Reaction is a 32-byte gadget built
   by one of Viber's own factory methods). This is the same path the QML UI uses, so it is stable
   across versions and depends on no offset.

The productionised bridge now lives in `bridges/viber/` (resident `LD_PRELOAD` library with named
commands only, headless launcher, systemd unit, and a meta-object dumper); its README has the build,
run, and socket protocol, and how the core side (`internal/viber/`) uses it.
The sections below record how the mechanisms were found.

The study itself used two throwaway `LD_PRELOAD` libraries:

- a **probe** that dumps every non-Qt class's meta-object (methods, signals, properties) to a file —
  907 Viber classes, ~18.8k live objects;
- a **bridge** exposing a mode-600 Unix socket whose one-line commands run on Viber's main thread:
  read a meta-type, read an object's scalar properties, invoke a method, list/query the open
  `QSqlDatabase` connections, connect a live listener, and perform each send/reply/react/file action
  (each guarded by an `allow-send` flag file). This is a research tool, deliberately broad; the real
  bridge exposes only named operations, like `bridges/whatsapp/`.

Build and launch (system Qt headers against Viber's bundled Qt; needs QtCore/QtSql/QtGui, plus `moc`
for the live-event slot):

```
moc watch.h -o moc_watch.cpp
g++ -shared -fPIC -O1 -std=c++17 -DQT_NO_VERSION_TAGGING -o bridge.so bridge.cpp watch.cpp moc_watch.cpp \
    -I/usr/include/qt6 -I/usr/include/qt6/QtCore -I/usr/include/qt6/QtSql -I/usr/include/qt6/QtGui \
    -L/opt/viber/lib -Wl,-rpath,/opt/viber/lib -lQt6Core -lQt6Sql -lQt6Gui -pthread
LD_PRELOAD=$PWD/bridge.so /opt/viber/Viber
```

Viber must not already be running (a second start just hands over to the first). It **ignores
SIGTERM** but quits cleanly on **SIGINT**, so no SIGKILL is needed while the database is open. Only one
Desktop client may be linked to an account at a time, so this host is that single client.

## What was found — the capability map

Every row below was confirmed live, not guessed from strings. Receiving and all reading come from the
live SQLite connections Viber has already unlocked (no decryption). Sending/replying/reacting call the
same objects the UI calls.

| Capability | How | Status |
|---|---|---|
| Send text | `TrayNotifier::sendMessage(ChatID, QString)` | works |
| Send image / file | `MainWidget::sendFilesToChat(QList<QUrl>, ChatID)` | works (uploads, `fileInfo.ContentType=PIC`) |
| Receive, instant | connect `EventsStorage::eventsAdded(QList<EventData>)`, read new rows | works (sub-second) |
| Receive, polling | query `Events` past a watermark | works |
| Quoted **reply** | `Chat::onMessageReply(EventID,bool)` → `InputBoxArea::forceActiveFocus` + `replaceTextWith(QString)` → post a `Qt::Key_Return` key event to the focus object | works (Info carries the `quote`) |
| Set **reaction** | heart: `MessageActions::like(EventID,bool)`; any of 1-5: `ReactionsFeature::getReactionFromQuickType(int)` → `MessageActions::react(EventID,Reaction,prev)` | works (❤️😂😮😢😡 = codes 1-5) |
| Read reactions | `LikeRelation(MessageToken,LikeEventID)`, and `Messages.MembersReactions`/`AdminsReactions` JSON (`{"1":1}` = one heart) | works |
| Read replies/media | `Messages.Info` JSON: `quote{token,text}`, `fileInfo{ContentType,…}` | works |
| Edit / delete / forward | `MessageActions::editMessage/deleteMessage/deleteMessageForAll/forward…` | present (not exercised) |
| Typing / read state | `setRead(ChatID,bool)`, `onUserIsTyping`/`onGroupUserIsTyping` | present (not exercised) |

Dead ends worth recording: the input bar's `onKeyRelease` is **not** the send trigger (it handles
formatting) — a real key event to the focused item is; and `Statistics::MessageEvents::messageQuickReaction`
is a telemetry logger, **not** the reaction action (`MessageActions` is). Both looked right by name and
did nothing — meta-object names are claims, behaviour is the test.

### Types and storage

- `ChatID`/`EventID` — 8-byte value types; a method taking one is invoked by passing a `qint64` by
  pointer. `Reaction` — 32-byte gadget, built via `ReactionsFeature`, never by hand.
- Open connections (no decryption needed): `~/.ViberPC/<number>/viber.db` (messages), `data.db`, and
  `~/.ViberPC/config.db`.
- `ChatInfo(ChatID, Name, Token, Flags, …)` — one row per chat; group name in `Name`, 1:1 names from
  `Contact(ContactID, Name, Number, MID, …)`. My Notes is `ChatID 1`, `isMyNotes = 1`.
- `Events(EventID, TimeStamp /*ms*/, Direction /*0 in, 1 out*/, Type, ChatID, ContactID, Token, IsRead,
  …)`, body text in `Messages(EventID, Body, Info, MembersReactions, AdminsReactions, …)`. `Token` is
  the cross-device dedup key already used by the importers (`ZVIBERMESSAGE.ZTOKEN` on iPhone). A
  reaction is its own `Type 3` event linked through `LikeRelation`; an outgoing file also writes
  `UploadFile`.

## The tests (all passed)

On the owner's own My Notes (`ChatID 1`) unless noted: plain text; an image (240×120 PNG, arrived as a
PIC and uploaded); a quoted reply (Info carried the quote to the target message); a heart via `like`.
In a group: a text message that drew a reply; the reply captured live via
`eventsAdded` within a second of arriving; a heart reaction placed on an incoming message
(`MembersReactions = {"1":1}`), driven by `react` + quick-type.

## The bridge

Built in `bridges/viber/` (see its README): Viber Desktop running permanently (headless under Xvfb), a
pinned version that is never updated, and the resident library that

- connects to `EventsStorage::eventsAdded` for instant receive (the `subscribe` command), reading new
  rows from the live `viber.db`/`data.db` connections;
- exposes only named operations over a local socket — `send` / `file` / `reply` / `react` / `unreact` /
  `read`, plus `chats` / `events` / `message` / `subscribe` for reading — not the generic `sql`/`call`/
  `props` of the study;
- gates every action behind `VIBER_ALLOW_SEND`, the equivalent of WhatsApp's `-send`.

The core side is `internal/viber` (the `viber-desktop` source): see `bridges/viber/README.md`, "The
core side". What the database keeps for reactions, edits and deletions, found with the bridge on the
owner's notes, is in the README's socket protocol.

Caveats: it is only as stable as the chosen Viber build (hence the version pin) — the reply path in
particular leans on the QML input (`InputBoxArea`) and a synthetic key event, the most version-fragile
part. When Viber updates, re-run `bridges/viber/tools/probe.so` and re-check the signatures named in
`inject/viber-bridge.cpp`. It is the account's sole Desktop client; and sending is real — gated behind
an explicit opt-in, exactly as WhatsApp's "Sending messages" is.
