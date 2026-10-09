# The app: running, reaching it, the assistant

`everysaid serve` is the whole app in one process: the API over the core, the live connections
(Telegram, WhatsApp, Signal, Viber Desktop), the PWA (the web/mobile interface) and push
notifications. `everysaid mcp` gives an assistant the same archive. `docs/design.md` explains how it
is built.

## Building and running

```
cd web && pnpm install && pnpm build && cd ..   # the interface, into web/dist
go generate ./internal/webui                     # web/dist into the binary's files
CGO_ENABLED=0 go build -o everysaid ./cmd/everysaid
./everysaid serve                                # http://localhost:8520
```

`docs/go.md` has the rest of the building (other systems, Signal's helper) and the other commands.

The first start prints a one-time setup link (`.../setup#...`, valid for an hour). Open it, give a
name, and choose how you will sign in:

- **a passkey** (a fingerprint, a face, a security key, or wherever your system or browser keeps
  passkeys): nothing to type afterwards;
- **a password with an authenticator app**: scan the QR code (or type the secret) into any
  authenticator app (TOTP), choose a password of at least 12 characters, and confirm with the
  6-digit code. Signing in then takes the name, the password and the current code; each code is
  accepted once, and five failures in 15 minutes lock this way for the rest of that time (passkeys
  and recovery codes still work).

Either way, ten recovery codes are shown once; keep them somewhere safe. Both ways can be used side
by side (Settings): a passkey where the device can make one, the password elsewhere. If a passkey
cannot be made in a browser, use the password way: the app does not depend on any one tool.

- `everysaid user link` prints a new one-time link at any time: for a passkey on a new device, a new
  authenticator app (a lost phone), or a way back in after losing everything else (it needs access
  to the server's machine). A recovery code also lets you in, to set a new password or passkey.
- In Settings: more passkeys, the signed-in devices (each can be signed out), new recovery codes,
  notifications, theme and language.

### Trying it on invented data

```
everysaid demo --dir /tmp/chr-demo --serve     # http://localhost:8530
```

builds an archive of invented people, groups, calls and pictures in that folder (with its own
settings and data, never touching yours) and serves it.

## Reaching it from other devices

Passkeys and push notifications need HTTPS, and passkeys are tied to the address: set it once in
`config.toml` before creating passkeys.

```toml
[server]
origin = "https://everysaid.example.org"   # the address your devices use
host = "127.0.0.1"                        # keep it local behind a proxy
port = 8520
```

Two ways, either is fine:

- **A reverse proxy with HTTPS** on the internet, e.g. Caddy (certificates are automatic):

  ```
  everysaid.example.org {
      reverse_proxy 127.0.0.1:8520
  }
  ```

  The app is built to be exposed: passkeys, or a password that is useless without the
  authenticator's current code; cookies HttpOnly, Secure and
  SameSite=Strict; every change needs a header other sites cannot send; a strict
  Content-Security-Policy; limits on login attempts; an audit log of sign-ins and changes.

- **A private network only** (WireGuard, Tailscale): nothing is exposed to the internet; each device
  joins the network. With Tailscale, `tailscale serve --https=443 http://127.0.0.1:8520` gives the
  machine an HTTPS address (`https://name.tailnet.ts.net`) to use as `origin`.

### Keeping it running (Linux, systemd user service)

`~/.config/systemd/user/everysaid.service`:

```ini
[Unit]
Description=Everysaid
After=network-online.target

[Service]
ExecStart=%h/.local/bin/everysaid serve
Restart=on-failure

[Install]
WantedBy=default.target
```

`systemctl --user enable --now everysaid`; `loginctl enable-linger $USER` keeps it running without
a login. (macOS: a launchd agent; Windows: a scheduled task at logon.)

## On a phone

Open the address in the browser and install it: on Android, the install prompt (or "Add to Home
screen"); on an iPhone, Share → Add to Home Screen. Installed, it runs full screen, keeps its data,
and can receive notifications (Settings → Notifications; on an iPhone only once installed). What
the web cannot do on an iPhone: reply from a notification, or share into the app from another app.

## Sources, libraries, contacts

All in Sources: add a plugin instance, set it up, import, and for the live services turn on the
live connection. Each instance shows whether it is ready, what it needs, and its log as it runs.

- **Sources:** an iPhone backup, an Android phone over adb, Viber Desktop, WhatsApp, Telegram,
  Signal, the carriers' missed-call notices, Adium and Pidgin logs.
- **Photo libraries:** a folder, or immich.
- **Names and photos:** a CardDAV address book, or a .vcf file.
- **Local analysis:** Ollama.

The live services: Telegram and WhatsApp run inside `everysaid serve` (WhatsApp as a device linked
from the phone with a QR code: the source's "Link a device"); Signal through its helper
(`bridges/signal/`, `docs/go.md`); Viber Desktop through Everysaid's bridge (`bridges/viber/`,
Linux), a library loaded into Viber Desktop, which the source starts headless and keeps running
(`docs/viber-bridge.md`, "How Viber Desktop runs"; `everysaid viber start|stop|restart|status`).

**Sending** is off until turned on in the source's settings ("Sending messages"); WhatsApp also
needs `[whatsapp] send = true` in `config.toml` (an unofficial client risks the account); Viber
Desktop started by hand (not by the source) its bridge started with `VIBER_ALLOW_SEND=1`. Then the app sends text, answers to a message, mentions
(in a group "@" lists its members) and files (the clip sends a file with the text as its caption;
Viber sends the file, then the text). A message's actions (its smiley button, or a long press on a
touch screen) put the user's reaction (the service's quick ones, and any other emoji where it takes
them: WhatsApp, Signal, Viber; Telegram's own list), change it, or take it back (also a tap on it),
and edit the user's own message or delete it for everyone (asked first), within the time the
service allows (WhatsApp 15 minutes to edit and two days to delete, Signal a day for both, Telegram
two days to edit).

A message deleted for everyone (by the user or its sender) is kept in the archive but shows as the
service shows it, "deleted", what it was on a tap; search and the media pages leave it out.

**Read receipts** go out only where the source's "Send read receipts" is on (off by default), when
a chat with something new from the others is read in the app while it is in view (a page in the
background reads nothing). The user's messages show ✓ sent, ✓✓ delivered to all, coloured when read
by all ("all" in a group: whoever the service said got the user's messages there about then), where
the service tells (WhatsApp, Signal; Telegram in a person's chat, read but not when; Viber from the
iPhone, where the other lets it be seen); a tap on them says who got and read the message, and when.

**WhatsApp's safeguards:** it sends only into chats where the other side has written (or that
were started from here: a first message to someone found on WhatsApp, only to people in the
owner's contacts or in a group with them, at most so many new chats a day, 5 at first), within limits
a minute, an hour and a day (the source's settings: 15, 300, 1000 at first; 0 for none), and never
the same longer text into more than so many chats an hour (3). When
WhatsApp warns the account (a temporary ban, a logout) sending is blocked until the user clears it
(the source's "Allow sending again"), and the app turns the source's sending off and tells the
user's devices.

Secrets (passwords, API keys, the Telegram session) go to the system keyring, never to the archive
or the browser. The iPhone's backup password is asked for at each import by default, used for that
import only and kept nowhere; its settings can keep it in the keyring instead (one for each iPhone).
The iPhone's card shows where its backup is, of when, and how big; "A new backup before importing"
(on by default) takes one over the cable first, else the import decrypts the backup that is there.

Every run of a source writes a whole log of its own (all it said, errors in full), and a
live connection one a day, in the state folder (`~/.local/state/everysaid/logs/` on Linux); the
source's card lists them, each opened whole.

## The assistant (MCP)

On the same machine, over stdio:

```json
{ "mcpServers": { "everysaid": { "command": "/path/to/everysaid", "args": ["mcp"] } } }
```

From another machine, over HTTP: the server's `/mcp` (e.g. `https://everysaid.example.org/mcp`),
with `Authorization: Bearer <token>`, a token made in Settings or by `everysaid user mcp-token`
(`everysaid user mcp-tokens` lists them, `everysaid user mcp-token --revoke ID` ends one).

Tools: search messages, list and read chats, a message in context, people, a person's direct chat
and last interaction, calls, a day's timeline, statistics, a chat's files and a file itself; and,
only with the user's approval, a person's name or note, or a file sent to the photo library. It
reads the same archive as the app.

## Developing

`docs/go.md` has the tests and the end-to-end run on the demo. For the interface with hot reload:

```
cd web && pnpm dev                                               # /api proxied to :8520
EVERYSAID_EXTRA_ORIGINS=http://localhost:5173 everysaid serve    # so passkeys work on the dev port
```
