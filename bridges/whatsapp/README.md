# The WhatsApp bridge

A [whatsmeow](https://github.com/tulir/whatsmeow) client linked to the account as a device (like
WhatsApp Web), keeping what arrives in SQLite for Everysaid's `whatsapp-bridge` source to import and
watch. It began as the bridge of [whatsapp-mcp](https://github.com/lharries/whatsapp-mcp) (MIT,
© Luke Harries) and is Everysaid's own since: every message kind, reactions, edits, deletions and
calls, the files of messages, mentions, the groups' members, receipts, the connection's state,
and guarded sending. Everysaid's MCP server
(`everysaid mcp`) is the way an assistant reads it, with the rest of the archive.

Unofficial: WhatsApp may block accounts that use such a client; sending raises that risk. The
bridge never announces itself as online, and marks messages read only when asked (`/api/read`,
which Everysaid calls only where the user turned read receipts on).

## Building and linking

```
cd bridges/whatsapp
go build -o whatsapp-bridge        # needs Go and a C compiler (go-sqlite3)
go test ./...                      # on an in-memory database, never a store
./whatsapp-bridge -store ~/.local/share/everysaid/whatsapp-bridge
```

The first start prints a QR code: scan it in WhatsApp on the phone (Linked devices). After that it
starts on its own. The store folder holds the session's keys (`whatsapp.db`), the messages
(`messages.db`) and the downloaded files (`media/`): keep it private, and in the backups. Everysaid
looks for it where `[whatsapp] bridge` in its `config.toml` says, else in its data folder
(`whatsapp-bridge/`); the source's settings can name another.

| flag | default | |
|---|---|---|
| `-store` | `store` | the folder above |
| `-port` | 8080 | the REST API, on 127.0.0.1 only |
| `-download` | on | download the files of messages as they arrive |
| `-send` | off | allow sending (below) |
| `-send-per-minute`, `-send-per-hour`, `-send-per-day` | 6, 60, 300 | sending limits |
| `-send-same-text` | 3 | the same text (20 characters or more) or file into at most this many chats an hour |

### Keeping it running (Linux, systemd user service)

`~/.config/systemd/user/whatsapp-bridge.service`:

```ini
[Unit]
Description=WhatsApp bridge for Everysaid
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%h/path/to/everysaid/bridges/whatsapp/whatsapp-bridge -store %h/.local/share/everysaid/whatsapp-bridge
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

`systemctl --user enable --now whatsapp-bridge`; restart it after each build; `loginctl
enable-linger $USER` keeps it running without a login. If WhatsApp logs the device out, stop the
service, run the bridge once in a terminal to scan a new code, and start it again.

## What it keeps

`messages.db` (columns and tables are added on start to an older store; nothing is removed):

- `chats`, `messages`: text and captions (`content`), `kind` (text, image, video, audio, voice,
  document, sticker, location, contact, poll), `subtype` (gif, video_note, link, live_location,
  view_once), `reply_to`/`reply_text` (the message quoted), `forwarded`, `edited` and `deleted`
  (set when the sender edits or deletes it later; `content` holds the last version),
  `lat`/`lon`/`place`, and the file's keys (`url`, `direct_path`, `media_key`, ...);
- `media_path`: the downloaded file, relative to the store (`media/<chat>/<message id><ext>`), or
  `media_error`: why it could not be had. Files are downloaded one at a time as messages arrive,
  and at each start for the last 7 days' messages still without one; view-once media are left
  alone. WhatsApp keeps files for a few weeks only: an older one cannot be had any more;
- `mentions`: the jids the text names with `@<user part>`;
- `group_info`, `group_members`: the groups the account is in and their members (as the group names
  them, `pn` or `lid`, with both the number and the LID where known), all read at each start (one
  query, as WhatsApp Web makes on connecting) and a group again when its members change; a group
  left keeps its last members, with `member` 0;
- `receipts`: who got (`delivered`), read and played the account's messages, and when, the first
  time each (from history only that it was, in a person's chat); `read_at`: when the account read
  a message from others, on any device;
- `reactions`: each person's latest reaction to a message (`''` once taken back);
- `calls`, `call_participants`: the call-log message WhatsApp sends to every device after a call,
  and the call signalling the bridge receives (offer, accept, reject, terminate);
- `bridge_state`, `bridge_events`, `sent`: the connection as WhatsApp reports it, and what was sent.

The log shows each message's kind, not its text.

## REST API (127.0.0.1)

- `GET /api/status`: the connection, whether sending is on, any block, the limits, the counts sent
  and the last events.
- `POST /api/download` `{"message_id", "chat_jid"}`: the file's absolute path, downloaded now unless
  it was before.
- `POST /api/send` (only with `-send`): `{"recipient", "message"}`, where the recipient is a
  number's digits or a jid; optionally `"reply_to"` (the id of the message answered, in that chat;
  for one the bridge does not have, `"reply_sender"`: `"me"` or who wrote it, and `"reply_text"`),
  `"mentions"` (in a group: people of it, each a jid or a number, written in the text as
  `@<its user part>`; the bridge rewrites each as the group names its members, a number or a LID),
  and a file: `"media"` (base64), `"filename"`, `"mime_type"`, with `"message"` as its caption.
  JPEG and PNG go as pictures (with a thumbnail), MP4 as videos, sounds without a caption as
  sounds, anything else as a document.
- `POST /api/read` `{"recipient", "until"}` (Unix seconds, 0 for now): read receipts for the
  chat's messages not read yet, up to then and at most 7 days old, as the phone sends them when a
  chat is opened; they are then read on the account's other devices too. Answers `{"marked": n}`.
- `POST /api/unblock`: clears the send block (below).

The POST endpoints refuse requests with an `Origin` header or without a JSON content type, so a
web page open in a browser cannot reach them.

## Sending (off by default)

Even with `-send` the bridge sends only as a person would by hand:

- only into a chat where the other side has written before (a number may be kept under its LID);
- within the limits a minute, an hour and a day;
- never the same longer text, or the same file, into more chats an hour than `-send-same-text`;
- never while sending is blocked.

It stores what it sent (and the file) in `messages`, since WhatsApp does not echo a device's own
messages back to it.

## The send block

Any sign that WhatsApp is unhappy with the account (a temporary ban, a logout, a connect failure
that means one, the session taken over by another client, an outdated client) blocks sending. The
block is kept in `bridge_state`, survives restarts, and is cleared only on purpose:

```
curl -X POST -H "Content-Type: application/json" http://127.0.0.1:8080/api/unblock
```

Everysaid turns its own sending off when it sees a block, and tells the user's devices. WhatsApp's
temporary-ban reasons are all signs of bulk messaging: sending to too many people, being blocked
by many users, creating too many groups, the same message too many times, broadcast lists.
