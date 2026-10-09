# Everysaid Viber bridge

Live send and receive on the user's own Viber account, by driving the running Viber Desktop (Linux)
from inside: Viber has no client API. This folder is the bridge (the part loaded into Viber). The
Everysaid side is the `viber-desktop` source in `internal/viber/` (import, live, sending).
`docs/viber-bridge.md` explains the method and every mechanism the bridge calls.

## How it works

An `LD_PRELOAD` library (`inject/viber-bridge.so`) loads into Viber and installs Qt's `qtHookData`
object create/destroy hooks (the mechanism GammaRay uses) to keep a live registry of Viber's
`QObject`s. It calls their methods by name through the meta-object system, the same entry points
Viber's QML UI uses, so nothing depends on a binary offset. Reads come from the SQLite connections
Viber has already unlocked (no decryption). It answers a small line-based Unix socket of named
commands only (no generic SQL or method call).

The library hooks only the process `/opt/viber/Viber` (not Viber's helper processes), so Viber
Desktop must be installed there.

## Build

Needs the system Qt 6 headers (QtCore/QtSql/QtGui) and `moc`; it links against Viber's bundled Qt in
`/opt/viber/lib` (`make QT=... MOC=... VIBERLIB=...` to change the paths).

```
make -C inject          # -> inject/viber-bridge.so
make -C inject install  # -> ~/.local/share/everysaid/viber/viber-bridge.so, where the source loads it
make -C tools           # -> tools/probe.so   (meta-object dumper, optional)
```

The library is written beside and moved into place: a running Viber has the old file mapped, and
writing into it brings that Viber down. A new build takes effect when Viber is started again.

## Run

The `viber-desktop` source starts Viber Desktop with the bridge itself, keeps it running apart from
the server, and stops it when asked: the source's actions, `everysaid viber start|stop|restart|status`
(`docs/viber-bridge.md`, "How Viber Desktop runs"). What follows is the way by hand, for a source
with "Start Viber Desktop here" off (or for trying the bridge alone).

```
./run.sh                       # read-only
VIBER_ALLOW_SEND=1 ./run.sh    # with sending (and reactions, edits, deletions, read receipts)
```

`run.sh` starts Viber with the bridge preloaded, headless on an Xvfb display (composing drives the
real QML input, so it needs a window) and on a D-Bus session of its own (so Viber puts no tray icon
or notifications on the desktop). It needs Xvfb, `xdpyinfo` and `dbus-run-session`. Viber must not
already be running: a second start hands over to the first.

Viber ignores SIGTERM but quits cleanly on SIGINT, or on the bridge's `quit`. A program started in
the background of a shell without job control has SIGINT ignored from the start, so `run.sh` starts
Viber with `env --default-signal=INT`. Kept running by systemd instead of the source, copy `viber-bridge.service` to
`~/.config/systemd/user/`, adjust its paths, and `systemctl --user enable --now viber-bridge` (it
stops Viber with SIGINT).

Environment: `VIBER_BRIDGE_SOCK` (default `$XDG_RUNTIME_DIR/viber-bridge.sock`, else
`/tmp/viber-bridge.sock`; mode 600), `VIBER_ALLOW_SEND` (`1` to allow actions), `VIBER_DISPLAY`
(the Xvfb display, default `:99`), `VIBER_BIN` (default `/opt/viber/Viber`), `VIBER_BRIDGE_DRY`
(below).

## Socket protocol

One command per line; the reply is lines, ended by the bridge closing the connection (except
`subscribe`, which streams until the client disconnects). `TEXT`/`PATH` is the rest of the line and
may contain spaces. `Body`/`Info` are escaped (`\t`, `\n`, `\\`) so one event is always one line.
Every action answers `ok` or `error <what>` (`error send-disabled` without `VIBER_ALLOW_SEND=1`).

| Command | Reply | Needs send |
|---|---|---|
| `ping` | `pong` | — |
| `chats` | `id⇥name⇥flags⇥token⇥lastReadToken⇥timestampMs` per chat | — |
| `events SINCE [LIMIT]` | per event with `id` > SINCE (LIMIT default 1000, at most 5000): `id⇥chat⇥contact⇥dir⇥type⇥ts⇥token⇥read⇥body⇥info` | — |
| `message EVENTID` | that one event, same columns | — |
| `subscribe` | `subscribed`, then the same event rows live as they arrive | — |
| `snapshot PATH` | a plain copy of `viber.db` at PATH (absolute; mode 600, moved into place when whole) | — |
| `input` | `edit=on/off`, a tab, then the focused input's text (to check what `compose` typed) | — |
| `check` | `version V`, then per capability (`read`, `live`, `send`, `file`, `compose`, `react`, `delete`, `read-receipts`) `ok NAME [note]` or `missing NAME WHAT,…`: whether this Viber still has what each command calls (nothing is called; My Notes may be opened, as `compose` opens a chat) | — |
| `quit` | `ok`, and Viber quits cleanly | — |
| `send CHATID TEXT` | TEXT with `\n`, `\t` and `\\` escaped | yes |
| `file CHATID PATH` | sends the file | yes |
| `reply CHATID TARGET TEXT` | a quoted reply (a `compose` with one text part) | yes |
| `compose JSON` | `{"chat":ID,"reply":EVENT,"edit":EVENT,"parts":[{"text":…},{"mention":CONTACTID}]}` | yes |
| `react TARGET CODE` | CODE: `like`, a quick reaction number (1-5 = ❤️😂😮😢😡), or any emoji | yes |
| `unreact TARGET` | takes the user's reaction back | yes |
| `delete TARGET` | the user's own message, deleted for everyone | yes |
| `read CHATID` | marks the chat read | yes |

`compose` opens the chat, types into its input (`InputBoxArea::replaceTextWith`/`insertEmoticon`,
`mentionSelected` after an "@") and sends with `InputBoxArea::accept()`, which does not depend on
which window has the focus. `mentionSelected` replaces all the text before the cursor, so the parts
are typed from the last to the first, each at the beginning. Before sending it checks the input
(each text part there, an "@" for each mention), types again after a moment where a chat still
opening lost a piece, and otherwise sends nothing (`error compose-mismatch`). With
`VIBER_BRIDGE_DRY=1` it types and stops before sending (`ok dry`), for checking with `input`.

### What the database keeps

`dir`: 0 incoming, 1 outgoing. "My Notes" is the chat whose `ChatInfo.Flags` has bit 19 (524288).

- Reactions arrive as their own `type 3` events, linked through `LikeRelation` (`MessageToken`: the
  message reacted to); the event's `Messages.PGIsLiked` is the quick reaction (0: taken back), 7 with
  the emoji in `SelfReaction` for any other emoji, `ContactID` who. `Messages.MembersReactions`/
  `AdminsReactions` hold the counts (`{"1":2,"🙏":1}`). My Notes takes only the quick ones.
- An edit rewrites the message (`Info.desktop_info.edit_token`) and adds an event whose
  `Info.edit.token` is the message edited.
- A deletion for everyone makes the message `Messages.Type` 72, with `Body` and `Info` emptied. A
  message deleted from the history only (on any device of the account) leaves Viber Desktop's
  database altogether, with its reactions' events: an import cannot tell that from history it never
  had, so the archive keeps it.
- Quotes and file metadata are JSON in `Messages.Info` (`quote{token,text}`,
  `fileInfo{ContentType,…}`); mentions are `textMetaInfo` (type 0, `memberId`, UTF-16
  `start`/`end`), as on the iPhone.

Quick check (read-only):

```
python3 - <<'PY'
import os, socket
s = socket.socket(socket.AF_UNIX)
s.connect(os.environ.get("VIBER_BRIDGE_SOCK") or os.path.join(os.environ["XDG_RUNTIME_DIR"], "viber-bridge.sock"))
s.sendall(b"ping\n"); print(s.makefile().read())
PY
```

## The Everysaid side

`internal/viber` (the `viber-desktop` source; its setting "The bridge's socket" defaults to the
bridge's). An import is a `snapshot` into the cache, read by the Viber importer (source
`viber-desktop/viber`) and its files (`PayloadPath`). Live, it imports again whenever `subscribe`
says Viber added events, and every `interval` seconds (default 60) for what changes without one (a
deletion); a stopped Viber is waited for. Sending maps the archive's conversation to a `ChatID` (a
group's or the notes' token, a person's number) and a message to its `EventID` (by token), on the
last copy, or a fresh one where it is not there. It uses `send`, `file`, `compose`, `react`/`unreact`,
`delete` and `read`, gated by the source's "Sending messages" (and, for `read`, "Send read
receipts"); a Viber started by hand also by `VIBER_ALLOW_SEND=1` here (the source starts it with
it). Viber Desktop itself is started, kept running and stopped by the source (`launch.go`).

## Layout

```
inject/viber-bridge.cpp   the resident LD_PRELOAD bridge
inject/watch.h            QObject slot for EventsStorage::eventsAdded (moc)
inject/Makefile
run.sh                    headless launcher by hand (Xvfb, D-Bus session, preload)
viber-bridge.service      systemd user unit template, for Viber started by hand
tools/probe.cpp           meta-object dumper, for re-checking signatures after a Viber update
tools/Makefile
```
