# In the car: new messages read aloud (iPhone)

A plan, not yet built. A small native iPhone app that hears of new messages from `everysaid serve`
and says them through the car's speakers, Greek in a Greek voice. It is a personal app, installed
from Xcode (on a Mac) with a paid developer account, never through the App Store.

## The problem

CarPlay's "Announce Notifications" reads messages with Siri, and Siri has no Greek: Greek text is
read by an English voice, letter by letter. No setting changes that, and the messaging apps that
CarPlay shows (WhatsApp and the rest) go through Siri too.

iOS itself has a Greek voice (the one of Spoken Content), and an app can use it directly
(`AVSpeechSynthesizer` with `el-GR`; `NLLanguageRecognizer` picks Greek or English per message).

What an app cannot do on iOS is read the notifications of other apps (Android allows it with a
notification listener; iOS has nothing like it). So the messages come from the archive's server,
which already receives them live from its live sources (WhatsApp, Telegram, Signal, Viber). SMS and
iMessage reach the archive only from the iPhone's backups, so they are not read.

## What the app is

An ordinary app with an icon on the iPhone, running in the background. It has no screen on the
car's display: only apps of the categories Apple allows (audio, navigation, communication, parking,
charging…) appear there, each with an entitlement Apple grants on request, and even those use only
Apple's templates; the approved messaging apps show no message text at all. Android Auto has the
same kind of rule, but its developer mode lets one's own apps ("unknown sources") onto the car's
screen; iOS has no such switch.

None of that is needed: CarPlay is an outer screen and speakers, everything runs on the iPhone, and
whatever sound the iPhone plays goes to the car's speakers whatever the car's screen shows. The app
speaks the way a navigation app does when another app is on the screen: the music is lowered
(`AVAudioSession` with `.voicePrompt` and `.duckOthers`), the voice speaks, the music comes back.

From iOS 26, Live Activities and widgets of iPhone apps also show on the CarPlay screen without an
entitlement; a Live Activity with the last message is a possible later addition, to be tried on the
car (how much text it shows is not known).

## How it hears of messages

The app stays connected to the server's WebSocket of live events, `GET /api/events`
(`internal/server/events.go`): a `hello`, every event of the server, and a `ping` every 25 seconds.
A new incoming message comes as `{"type": "new", "notify": [...]}` (`internal/server/host.go`), one
entry per chat with `title` (person or group), `body` (the last message, cut to 240 characters,
`(n) ` before it when there are more), `chat`, `message` (its id), `ts` and `services`. The owner's
own messages, muted, archived and hidden chats are already left out. The whole text is
`GET /api/messages/{mid}`.

A delay of seconds is fine; one of ten minutes or more is not.

### Staying awake

iOS suspends an app a few seconds after it leaves the screen, and a suspended app loses its
connection. An app may keep running in the background only for a few reasons: while it plays sound
(the app is silent most of the time), while it follows the location (navigation and fitness apps),
and some others (VoIP) that do not fit. The app asks for background location only to stay awake; it
uses no position. The cost is the blue location mark in the status bar and some battery, while the
phone charges in the car. The App Store would reject this; a personal app has no such review.

If iOS ends the app anyway (memory, a crash):
- a Shortcuts automation, "when CarPlay connects, open the app", starts it on every drive, with no
  question asked;
- the significant location change service wakes an ended app after a few hundred metres of driving;
- the app's own log records every gap, so we know whether it happens.

### Losing the network

- Reconnect with backoff (1, 2, 4… seconds up to a limit), and at once when `NWPathMonitor` says the
  network is back.
- A connection that looks open but is dead (after a tunnel, a change of cell): no `ping` for about
  40 seconds means reconnect.
- Nothing is lost: the app keeps the id of the last message it saw and, after each reconnection,
  asks the server for what came after it.
- What came while offline is summed up ("3 messages, from Giannis and Maria"); the text is read only
  when asked.

### Why not push

A silent push that wakes the app is delivered at iOS's discretion: often late by more than ten
minutes, or never. A visible push arrives reliably, but it does not wake the app: it runs only a
Notification Service Extension (about 30 seconds, little memory), which may change the notification
(title, text, picture, sound) and cannot speak. A visible push with `content-available` wakes the
app only at the same discretion.

One trick remains, as a safety net: the extension makes a recording of the spoken message (on the
phone, or from the server) and sets it as the notification's sound (from `Library/Sounds` of the
app group's container). Limits: 30 seconds at most, always played (no "read it only if I want"), no
control of the music, and whether such a sound plays in the car with CarPlay and the Driving Focus
on is to be tried. It needs APNs (a `.p8` key from the developer account, no approval) sent by the
server; the server's push today is web push, for the PWA. It is added only if the log shows iOS
ending the app in practice.

### Why not the PWA

On the iPhone a PWA is suspended in the background (no location trick for web apps), and its web
push has the standard sound only. It would work only with the iPhone unlocked and the PWA on its
screen for the whole drive (`speechSynthesis` has the Greek voice, Wake Lock keeps the screen on),
without the steering wheel's buttons; whether Safari lowers the music or stops it is unknown.

## The app

- Announces "Message from X, on Viber"; the steering wheel's play/next button
  (`MPRemoteCommandCenter`) reads the text. Chosen people can be read always, without asking.
- Greek or English voice per message; speed of the voice set by the user.
- Its screen on the iPhone: the connection's state, the last messages it said, the settings (server
  address, token, people read always, voice).
- Its own log file, sent to the server or taken with Xcode.

## On the server

1. **A device token.** `/api/events` takes only a passkey session's cookie. A token like the MCP
   token (`POST /api/auth/mcp-token`), made in the UI, kept in the iPhone's Keychain, revocable.
2. **Messages after a given one**, for catching up after a gap: not in the API today.
3. Later, only if needed: APNs, for the safety net above.

The server must be reachable over mobile data (an HTTPS address, `docs/app.md`); through Tailscale,
the VPN must stay on in the car.

## Building and trying it

The code goes in `ios/` beside `web/`, so that the API and the app change together. Built with
Xcode on a Mac (Xcode 26), no entitlement from Apple needed (background location and audio are
capabilities set in Xcode).

- At home: with the phone's speaker or headphones and Xcode's debugger, over a cable or wireless
  debugging.
- The CarPlay Simulator (Xcode's Additional Tools) makes the Mac look like a car to the iPhone:
  connecting, disconnecting, the buttons.
- In the car there is no debugger: the app's log tells what happened.

## Phases

1. **It speaks**: the connection, the announcement, reading on the button, Greek/English voice,
   the music lowered. Tried at home and on the CarPlay Simulator.
2. **It lasts**: reconnection, dead connections, the summary after a gap, staying awake, the
   Shortcut for CarPlay, the log.
3. **In the car**: tried on real drives, fixed from the log.

## Open

- How the server is reached from mobile data (the owner's setup).
- Whether a Live Activity is worth adding for the car's screen.
