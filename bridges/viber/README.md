# Everysaid Viber bridge

Live send **and** receive on the owner's own Viber account, by driving the running Viber Desktop
(Linux, Qt 6.10) from inside — Viber has no client API. This folder is the **bridge side** (the part
that talks to Viber), analogous to `bridges/whatsapp/`. The **core side** is the `viber-desktop` source
in `internal/viber/` (import, live, sending), reading through `snapshot` with the Viber importer.

The feasibility study, the method, and every mechanism (with how each was verified) are in
`docs/viber-bridge.md`. Read that first. This README is how to build, run, and talk to the bridge.

## How it works (one paragraph)

An `LD_PRELOAD` library (`inject/viber-bridge.so`) loads into Viber and installs Qt's own
`qtHookData` object create/destroy hooks — the mechanism GammaRay uses — to keep a live registry of
Viber's `QObject`s. It calls their methods by name through the meta-object system
(`QMetaObject::metacall`), the same entry points Viber's QML UI uses, so nothing depends on a binary
offset. Reads come from the SQLite connections Viber has already unlocked (no decryption). It exposes
a small line-based Unix socket of **named** commands only (no generic SQL/method surface).

## Build

Needs the system Qt 6 headers (QtCore/QtSql/QtGui) and `moc`.

```
make -C inject          # -> inject/viber-bridge.so
make -C tools           # -> tools/probe.so   (dev meta-object dumper, optional)
```

The library is written beside and moved into place: a running Viber has the old file mapped, and
writing into it brings that Viber down. A new build takes effect when Viber is started again.

## Run

```
# read-only
./run.sh
# with sending enabled (the owner's deliberate choice, like WhatsApp's -send)
VIBER_ALLOW_SEND=1 ./run.sh
```

`run.sh` starts Viber headless under an Xvfb display (the reply path drives the real QML input, so it
needs a window) with the bridge preloaded. For a permanent service, adapt `viber-bridge.service`.
Viber ignores SIGTERM but quits cleanly on **SIGINT**, or on the bridge's `quit`; it is the account's
single linked Desktop client. A program started in the background of a shell without job control has
SIGINT ignored from the start, and Viber then never quits on it: `run.sh` starts it with
`env --default-signal=INT`.

Env: `VIBER_BRIDGE_SOCK` (default `$XDG_RUNTIME_DIR/viber-bridge.sock`, mode 600), `VIBER_ALLOW_SEND`
(`1` to allow actions), `VIBER_DISPLAY` (Xvfb display, default `:99`), `VIBER_BIN`.

## Socket protocol

One command per line; the reply is lines terminated by the server closing the connection (except
`subscribe`, which streams until the client disconnects). `TEXT`/`PATH` is the rest of the line and
may contain spaces. `Body`/`Info` are escaped (`\t`, `\n`, `\\`) so one event is always one line.

| Command | Reply | Needs send |
|---|---|---|
| `ping` | `pong` | — |
| `chats` | `id⇥name⇥flags⇥token⇥lastReadToken⇥timestampMs` per chat | — |
| `events SINCE [LIMIT]` | per event `id`>SINCE: `id⇥chat⇥contact⇥dir⇥type⇥ts⇥token⇥read⇥body⇥info` | — |
| `message EVENTID` | that one event, same columns | — |
| `subscribe` | `subscribed`, then the same event rows live as they arrive | — |
| `send CHATID TEXT` | `ok` / `error …` | yes |
| `file CHATID PATH` | `ok` / `error …` | yes |
| `reply CHATID TARGET TEXT` | `ok` / `error …` | yes |
| `react TARGET CODE` | `ok` / `error …` (CODE = `1`-`5` = ❤️😂😮😢😡, or `like`) | yes |
| `unreact TARGET` | `ok` / `error …` | yes |
| `read CHATID` | `ok` / `error …` | yes |
| `compose JSON` | `ok` / `error …` (`{"chat":ID,"reply":EVENT,"edit":EVENT,"parts":[{"text":…},{"mention":CONTACTID}]}`) | yes |
| `delete TARGET` | `ok` / `error …` (the user's own message, for everyone) | yes |
| `snapshot PATH` | `ok` / `error …`: a plain copy of `viber.db` at PATH (mode 600) | — |
| `input` | `edit=on/off`, then the focused input's text (to check what `compose` typed) | — |
| `quit` | `ok`, and Viber quits cleanly | — |

`send`'s TEXT has `\n`, `\t` and `\\` escaped. `react`'s CODE is a quick reaction (1-5), `like`, or any
emoji. `compose` types into the chat's input (`InputBoxArea::replaceTextWith`/`insertEmoticon`,
`mentionSelected` after an "@") and sends it with `InputBoxArea::accept()`, which does not depend on
which window has the focus (a synthetic Return did not commit an edit while the desktop was in use).
With `VIBER_BRIDGE_DRY=1` it types and stops before sending, for checking with `input`.

`dir`: 0 incoming, 1 outgoing. Reactions arrive as their own `type 3` events, linked through
`LikeRelation` (`MessageToken`: the message reacted to); the event's `Messages.PGIsLiked` is the quick
reaction (0: taken back), `SelfReaction` any other emoji, `ContactID` who. `Messages.MembersReactions`/
`AdminsReactions` hold the counts (`{"1":2,"🙏":1}`). An edit rewrites the message (`Info.desktop_info.
edit_token`) and adds an event whose `Info.edit.token` is the message edited. A deletion for everyone
makes the message `Messages.Type` 72 with `Body` and `Info` emptied . A message deleted from the
history only (on any device of the account) leaves Viber Desktop's database altogether, with its
reactions' events: an import cannot tell that from history it never had, and the archive keeps it.
The source also marks its own deletions for everyone in the archive at once. Any emoji as a reaction is `PGIsLiked` 7 with the emoji in `SelfReaction`; My
Notes takes only the quick ones. "My Notes" is the chat whose
`ChatInfo.Flags` has bit 19 (524288). Quotes and file metadata are JSON in `Messages.Info`
(`quote{token,text}`, `fileInfo{ContentType,…}`); mentions are `textMetaInfo` (type 0, `memberId`,
UTF-16 `start`/`end`), as on the iPhone.

Quick check (read-only):

```
python3 - <<'PY'
import socket
s=socket.socket(socket.AF_UNIX); s.connect("/run/user/$(id -u)/viber-bridge.sock")
s.sendall(b"ping\n"); print(s.makefile().read())
PY
```

## Verified

All of the following were exercised against the live client (see `docs/viber-bridge.md` for details):
send text, send image (uploads as a PIC), receive both by polling and live via `subscribe`, quoted
reply (the sent message carries the quote), set reaction (heart via `like`; any of 1-5 via `react`),
and reading chats/events/reactions/quotes. Both outward actions were confirmed in a group chat.

Two name-traps found and avoided: the send-on-Enter is a real key event to the window, **not**
`InputBoxArea::onKeyRelease`; and `Statistics::MessageEvents::messageQuickReaction` is a telemetry
logger, **not** the reaction action (`MessageActions::like`/`react` is). Behaviour is the test, not the
method name.

## The core side

`internal/viber` (the `viber-desktop` source): an import is a `snapshot` into the cache read by
`importers.Viber` (no iPhone, source `viber-desktop/viber`) and its files (`PayloadPath`); live, the
same again whenever `subscribe` says Viber added events, and every minute for what changes without
one. Sending maps the archive's conversation to a `ChatID` (a group's or the notes' token, a person's
number) and a message to its `EventID` (by token), on the last copy, or a fresh one where it is not
there. It is gated by the source's "Sending messages" and by `VIBER_ALLOW_SEND` here.

Mentions were checked on a group: `mentionSelected` replaces all the
text before the cursor, so `compose` types from the end to the start, each piece at the beginning,
and checks the input (each text piece there, an "@" for each mention) before sending, typing again
after a moment where a chat still opening lost a piece, else sending nothing.

## Layout

```
inject/viber-bridge.cpp   the resident LD_PRELOAD bridge
inject/watch.h            QObject slot for EventsStorage::eventsAdded (moc)
inject/Makefile
run.sh                    headless launcher (Xvfb + preload)
viber-bridge.service      systemd user unit template
tools/probe.cpp           meta-object dumper for re-checking signatures after a Viber update
```
