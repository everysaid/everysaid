# The standalone WhatsApp bridge (not used by Everysaid)

Everysaid's WhatsApp connection runs inside `everysaid serve`: the `whatsapp-bridge` source
(`internal/whatsapp`) is a [whatsmeow](https://github.com/tulir/whatsmeow) client linked to the
account as a device (like WhatsApp Web), with its settings in the source and in `config.toml`
(`[whatsapp]`). This folder is the same client as a program of its own, with a REST API on
127.0.0.1. It began as the bridge of [whatsapp-mcp](https://github.com/lharries/whatsapp-mcp)
(MIT, © Luke Harries; its license is in `LICENSE`).

Nothing runs it: it is a Go module of its own (`everysaid/whatsapp-bridge`), outside the app's
build (`go build ./...` and `go test ./...` of the repository do not reach it). What still ties the
app to it:

- the store: `internal/whatsapp` reads and writes the same store folder and files (below), so a
  store made by this program is used as it is;
- the device: `internal/whatsapp/identity_test.go` keeps the app on the whatsmeow version of this
  folder's `go.mod`, so the device WhatsApp sees stays the one this program linked;
- the source does not start its connection while this program answers on `127.0.0.1:8080`
  (`/api/status`): two connections with one device's keys end its session. Stop it first.

Unofficial: WhatsApp may block accounts that use such a client; sending raises that risk.

## The store

The store folder (the source's setting "The bridge's store folder"; default `[whatsapp] bridge` in
`config.toml`, else `<data>/whatsapp-bridge/`) holds the session's keys (`whatsapp.db`), the
messages (`messages.db`) and the downloaded files (`media/`): keep it private, and in the backups.

`messages.db` (columns and tables are added on start to an older store; nothing is removed):

- `chats`, `messages`: text and captions (`content`), `kind` (text, image, video, audio, voice,
  document, sticker, location, contact, poll), `subtype` (gif, video_note, link, live_location,
  view_once), `reply_to`/`reply_text` (the message quoted), `forwarded`, `edited` and `deleted`
  (set when the sender edits or deletes it later; `content` holds the last version),
  `lat`/`lon`/`place`, `mentions` (the jids the text names with `@<user part>`), and the file's
  keys (`url`, `direct_path`, `media_key`, ...);
- `media_path`: the downloaded file, relative to the store (`media/<chat>/<message id><ext>`), or
  `media_error`: why it could not be had. Files are downloaded as messages arrive, and at each
  start for the last 7 days' messages still without one; view-once media are left alone.
  WhatsApp keeps files for a few weeks only;
- `group_info`, `group_members`: the groups the account is in and their members (as the group names
  them, `pn` or `lid`, with both the number and the LID where known), read at each start and a
  group again when its members change; a group left keeps its last members, with `member` 0;
- `receipts`: who got (`delivered`), read and played the account's messages, and when; `read_at`:
  when the account read a message from others, on any device;
- `reactions`: each person's latest reaction to a message (`''` once taken back);
- `calls`, `call_participants`: the call-log message WhatsApp sends to every device after a call,
  and the call signalling received (offer, accept, reject, terminate);
- `bridge_state`, `bridge_events`, `sent`: the connection as WhatsApp reports it, any send block,
  and what was sent (WhatsApp does not echo a device's own messages back to it).

## Sending and the send block

Sending is off by default (here `-send`; in the app `[whatsapp] send = true` in `config.toml` and
the source's "Sending messages"). Even when on, it sends only as a person would by hand:

- only into a chat where the other side has written before (a number may be kept under its LID);
- within the limits a minute, an hour and a day (6, 60, 300);
- never the same longer text (20 characters or more), or the same file, into more than 3 chats an
  hour;
- never while sending is blocked.

Any sign that WhatsApp is unhappy with the account (a temporary ban, a logout, a connect failure
that means one, the session taken over by another client, an outdated client) blocks sending. The
block is kept in `bridge_state`, survives restarts, and is cleared only on purpose (the source's
action "Allow sending again"; here `POST /api/unblock`). The app also turns the source's sending off
and tells the user's devices. WhatsApp's temporary-ban reasons are all signs of bulk messaging:
sending to too many people, being blocked by many users, creating too many groups, the same message
too many times, broadcast lists.

## Running the program by hand

```
cd bridges/whatsapp
go build -o whatsapp-bridge        # needs a C compiler (go-sqlite3)
go test ./...                      # on an in-memory database, never a store
./whatsapp-bridge -store <store folder>
```

A store not linked yet prints a QR code: scan it in WhatsApp on the phone (Linked devices).

| flag | default | |
|---|---|---|
| `-store` | `store` | the store folder |
| `-port` | 8080 | the REST API, on 127.0.0.1 only |
| `-download` | on | download the files of messages as they arrive |
| `-send` | off | allow sending |
| `-send-per-minute`, `-send-per-hour`, `-send-per-day` | 6, 60, 300 | sending limits |
| `-send-same-text` | 3 | the same text or file into at most this many chats an hour |

REST API (127.0.0.1):

- `GET /api/status`: the connection, whether sending is on, any block, the limits, the counts sent
  and the last events.
- `POST /api/download` `{"message_id", "chat_jid"}`: the file's absolute path, downloaded now unless
  it was before.
- `POST /api/send` (only with `-send`): `{"recipient", "message"}`, where the recipient is a
  number's digits or a jid; optionally `"reply_to"` (the id of the message answered, in that chat;
  for one the bridge does not have, `"reply_sender"`: `"me"` or who wrote it, and `"reply_text"`),
  `"mentions"` (in a group: people of it, each a jid or a number, written in the text as
  `@<its user part>`), and a file: `"media"` (base64), `"filename"`, `"mime_type"`, with
  `"message"` as its caption. JPEG and PNG go as pictures, MP4 as videos, sounds without a caption
  as sounds, anything else as a document.
- `POST /api/read` `{"recipient", "until"}` (Unix seconds, 0 for now): read receipts for the
  chat's messages not read yet, up to then and at most 7 days old. Answers `{"marked": n}`.
- `POST /api/unblock`: clears the send block.

The POST endpoints refuse requests with an `Origin` header or without a JSON content type, so a
web page open in a browser cannot reach them. The program never announces itself as online, and
marks messages read only through `/api/read`. Reactions, edits and deletions are sent only by the
app's source, not by this program.
