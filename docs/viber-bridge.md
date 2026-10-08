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

## When Viber updates

The bridge is only as stable as the Viber build it was checked on. The most fragile part is
composing (replies, edits, mentions), which drives the QML input. After an update: run
`bridges/viber/tools/probe.so` (it dumps every class's methods, signals and properties) and check
the signatures named in `inject/viber-bridge.cpp`; then check a send, a reply and a reaction in My
Notes. Updating Viber deliberately, not automatically, keeps this under control.

Viber allows one linked Desktop client per account, so the bridge's Viber Desktop is that client.
Sending is real, and is gated twice: the bridge's `VIBER_ALLOW_SEND=1` and the source's "Sending
messages".
