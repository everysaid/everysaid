# Viber: how the Viber Desktop bridge works

Everysaid's `viber-desktop` source reads, receives and sends on the user's own Viber account by
driving the running Viber Desktop (Linux) from inside, through the bridge in `bridges/viber/`. Its
README has the build, the launcher and the socket protocol; this page is how the bridge reaches
Viber, and what to check when Viber changes.

## Why this way

Viber has no client API. Its Bot REST API is a separate bot account: it never sees the user's own
chats and cannot join groups. The other known approaches do not give a live, two-way connection:

- **Bot API** wrappers (`Viber/viber-bot-python`, `mileusna/viber`, a "mautrix-viber" that is not
  part of mautrix): a bot account, not the user's.
- **UI automation** of Viber Desktop (`rusty4444/viber-matrix-bridge`, Windows UI Automation): text
  only, breaks with every Viber update.
- **Database export** of the running client (`r0073rr0r/ViberPC-Export` via frida on Windows,
  `nemanjacosovic/viber-export-macos` via lldb on macOS): history only, no sending, no live receive.
- Beeper, Matterbridge, BitlBee, libpurple/Pidgin: no Viber.
- The wire protocol is proprietary and undocumented; reverse-engineering it is out of scope (and
  against Viber's terms).

The running Viber Desktop already speaks the protocol, so the bridge drives it.

## The method

Viber Desktop on Linux is a Qt 6 application (Qt 6.10, bundled in `/opt/viber/lib`). Two facts make
it drivable:

1. **Qt's object hooks.** Qt exports `qtHookData`, the table GammaRay and similar tools use to be
   told of every `QObject` created or destroyed. The bridge, an `LD_PRELOAD` library, installs
   hooks there and keeps a live set of Viber's objects, including the ones that send and receive.
2. **The meta-object system.** Viber's binary is stripped, but every class keeps its meta-object:
   method names, signal/slot signatures and properties. A method is invoked by name with
   `QMetaObject::metacall`, the same entry points the QML UI uses, so nothing depends on a binary
   offset.

Reads come from the SQLite connections Viber has already opened and unlocked (no decryption).

## What the bridge calls

| Capability | How |
|---|---|
| Send text | `TrayNotifier::sendMessage(ChatID, QString)` |
| Send a file | `MainWidget::sendFilesToChat(QList<QUrl>, ChatID)` (a picture uploads as a PIC) |
| Open a chat | `MainWidget::openChat(ChatID, QString)`, then wait for its `Chat` object |
| Quoted reply | `Chat::onMessageReply(EventID, bool)`, then compose (below) |
| Edit | `Chat::editMessage(EventID)` puts the message in the input, then compose |
| Compose and send | `InputBoxArea::forceActiveFocus`, `replaceTextWith`, `insertEmoticon`, `mentionSelected(contact)` after an "@", then `InputBoxArea::accept()` (a synthetic Return to the window only where `accept` is missing) |
| Delete for everyone | `MessageActions::deleteMessageForAll(EventID)` |
| Reaction | heart: `MessageActions::like(EventID, bool)` (false takes it back); a quick one: `ReactionsFeature::getReactionFromQuickType(int)`, any emoji: `getReactionFromEmojiCode(QString)`, then `MessageActions::react(EventID, Reaction, prev)` |
| Mark read | `setRead(ChatID, bool)` on the object that has it |
| Receive, instant | connect `EventsStorage::eventsAdded(QList<EventData>)` |
| Copy of the database | `ATTACH` a plain database to Viber's open `viber.db` connection and copy each table (full-text tables left out) |

Types: `ChatID` and `EventID` are 8-byte values, passed as a `qint64` by pointer. `Reaction` is a
32-byte gadget, built by `ReactionsFeature`, never by hand.

Dead ends: the input bar's `onKeyRelease` is not the send trigger (it handles formatting);
`Statistics::MessageEvents::messageQuickReaction` is a telemetry logger, not the reaction action
(`MessageActions` is); a synthetic Return does not commit an edit while another window has the
focus (`accept()` does). Meta-object names are claims: behaviour is the test.

## Viber's database

Open in the running client, no decryption needed: `~/.ViberPC/<number>/viber.db` (messages) and
`data.db`, and `~/.ViberPC/config.db`.

- `ChatInfo(ChatID, Name, Token, Flags, …)`: one row per chat; a group's name in `Name`, a person's
  from `Contact(ContactID, Name, Number, MID, …)`. "My Notes" is the chat whose `Flags` has bit 19.
- `Events(EventID, TimeStamp /*ms*/, Direction /*0 in, 1 out*/, Type, ChatID, ContactID, Token,
  IsRead, …)`, the text in `Messages(EventID, Body, Info, MembersReactions, AdminsReactions, …)`.
  `Token` is the key that matches a message across devices (`ZVIBERMESSAGE.ZTOKEN` on the iPhone).
- A reaction is its own `Type 3` event, linked through `LikeRelation`; an outgoing file also
  writes `UploadFile`. The README's socket protocol has what the database keeps for reactions,
  edits, deletions, quotes and mentions.

## How Viber Desktop runs

The source starts Viber Desktop itself and keeps it running; nothing else needs to be set up
(its setting "Start Viber Desktop here", on by default).

**What is started.** What `bridges/viber/run.sh` does by hand, done by the source:

```
Xvfb :99 -screen 0 1280x900x24 -nolisten tcp          (only if no X server is on the display)
dbus-run-session -- env --default-signal=INT LD_PRELOAD=<the bridge> /opt/viber/Viber
    DISPLAY=:99  QT_QPA_PLATFORM=xcb  VIBER_BRIDGE_SOCK=<the socket>  VIBER_ALLOW_SEND=1
```

- A **virtual display** (Xvfb): composing (replies, edits, mentions) drives Viber's real input, which
  needs a window; nothing shows on the desktop. Xvfb is left running when Viber stops, for the next
  start (in a scope of its own too, `everysaid-xvfb-<display>`, for the same reason as Viber's).
- A **D-Bus session of its own**: on the desktop's, Viber would put its icon in the tray and its
  notifications on the screen.
- **SIGINT** left to stop it: Viber ignores SIGTERM and quits cleanly on SIGINT.
- A **session of its own** (`setsid`) and, where the user's systemd runs, a **scope of its own**
  (`systemd-run --user --scope --unit=everysaid-viber-<id>`, in front of the line above): stopping a
  service ends every process in its cgroup, so without its own scope a restart of
  `everysaid.service` would end Viber too. The scope is made for the run and goes with it; nothing
  is installed. The server's restarts and deployments do not reach Viber: a server that starts
  finds it already there, on its socket, and only connects.
- Viber's own output (the bridge's check at its start among it) goes to
  `~/.local/state/everysaid/logs/plugin-<id>/viber.log`, of its last start.

The source's settings: "The bridge" (default `~/.local/share/everysaid/viber/viber-bridge.so`, where
`make -C bridges/viber/inject install` puts it), "Viber Desktop" (default `/opt/viber/Viber`), "Its
virtual display" (default `:99`), "The bridge's socket" (default
`$XDG_RUNTIME_DIR/viber-bridge.sock`). Needed on the system: Viber Desktop, Xvfb and
`dbus-run-session` (the D-Bus package).

**When it starts.** With the live connection: a live connection that finds Viber not running starts
it, waits for its socket, and follows it. If Viber goes away (it crashed, it was killed), the live
connection starts it again. "Import now" also starts it when it is not running. A source whose
Viber is not running but can be started counts as ready ("the live connection starts it").

**Stopping it, and starting it again.** On the source's card in Sources:

| Action | What it does |
|---|---|
| Start Viber Desktop | starts it (and forgets an earlier stop) |
| Restart Viber Desktop | stops it and starts it again (after a new bridge, for example) |
| Stop Viber Desktop | stops it, and keeps it stopped: nothing starts it again until "Start" |

The same from the command line, also with the server not running:

```
everysaid viber status       # running or not, started by the source, stopped by the user, version, what is missing
everysaid viber start
everysaid viber stop
everysaid viber restart
                             # --instance N where there are several Viber Desktop sources
```

Stopping asks the bridge to `quit` (Viber closes its database and ends); if it has not ended within
20 seconds, SIGINT goes to its session. A stop by the user is kept in
`~/.local/state/everysaid/viber/<id>/stopped`, so neither the live connection nor a restart of the
server starts Viber again; "Start" removes it. The process the source started is in `viber.pid`
beside it.

Turning the live connection off, disabling the source or removing it stops Viber too (it is there
for the live connection), without keeping it stopped: turning the live connection on starts it
again. The server stopping or restarting does not stop it.

**Started by hand instead.** With "Start Viber Desktop here" off, the source starts and stops
nothing: Viber Desktop is started with `bridges/viber/run.sh`, or kept running by the systemd user
unit `bridges/viber/viber-bridge.service` (its README), and the source waits for it. Then sending
also needs the bridge started with `VIBER_ALLOW_SEND=1`. Only one of the two ways at a time: a
second Viber hands over to the first.

**A new bridge.** `make -C bridges/viber/inject install` puts it in place (beside, then moved: the
running Viber keeps the old one mapped); it takes effect when Viber starts again ("Restart Viber
Desktop").

## When Viber updates

Viber is updated as any other package, not held back: the bridge says itself what an update broke.
Its `check` command looks up, on the classes Viber has (nothing is called), everything each command
uses (the table above), and answers with Viber's version and, per capability (`read`, `live`,
`send`, `file`, `compose`, `react`, `delete`, `read-receipts`), `ok` or what is missing. It runs
once at Viber's start (its summary in Viber's output: `viber.log`, above, or the journal of
`viber-bridge.service` where it is started by hand) and whenever the source connects. The source then:

- shows the version, and what this version cannot do, on its card; it is not ready without `read`
  or `live`;
- refuses an action that can no longer work with what is missing, before anything is sent;
- keeps the version it saw, and says a new one (or something gone) in its log and as a
  notification to the user's devices;
- says, once until it works again, when live receiving stopped: the checks every interval bring
  messages the bridge did not tell of, twice running.

When something is missing: run `bridges/viber/tools/probe.so` (it dumps every class's methods,
signals and properties), find what the call became, fix `inject/viber-bridge.cpp` (and its check),
rebuild and install it (`make -C bridges/viber/inject install`), restart Viber ("Restart Viber
Desktop"), and try a send, a reply and a reaction in My Notes. The most fragile part is composing
(replies, edits, mentions), which drives the QML input. Until it is fixed, the version before can
be put back from the package cache (Arch: `/var/cache/pacman/pkg/`, or the AUR helper's cache).

Viber allows one linked Desktop client per account, so the bridge's Viber Desktop is that client.
Sending is real. It is allowed by the source's "Sending messages" (marking chats read too, as
opening them in Viber does); the bridge it starts may act, its socket private to the user (mode 600, in the
user's runtime folder). Viber Desktop started by hand is gated twice: its bridge also needs
`VIBER_ALLOW_SEND=1`.
