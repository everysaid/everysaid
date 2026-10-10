# Everysaid's patches to signalmeow

This is mautrix-signal v0.2609.0's `pkg/signalmeow` and `pkg/libsignalgo` (AGPL-3.0, see `LICENSE`),
taken in place of the module (`replace go.mau.fi/mautrix-signal => ./third_party/mautrix-signal`
in the repository's `go.mod`), its tests left out. Everysaid's Signal client (`internal/signal`,
built with `-tags signal`) is its only user. Each change from upstream is marked `everysaid` in the
code and listed here.

## pkg/signalmeow/events/message.go, pkg/signalmeow/receiving.go: the content whole

`events.Content`: every decrypted content, with its envelope, its sender and their device and its
destination, is handed to the event handler before signalmeow's own handling of it (in
`handleDecryptedResult`, before the unencrypted types are dealt with). The handler's false leaves
the envelope unacknowledged (it comes again), as for signalmeow's own events.

Why: Everysaid reads the protobufs itself, as its earlier helper did, and signalmeow's events leave
out what it keeps: the phone's call log (`SyncMessage.callEvent`, not handled by signalmeow at all),
a call's answer, busy and hangup type, its id and whether it is video (signalmeow's `events.Call`
has only offers and hangups, without detail), an envelope's own timestamps and sender device, a
sync message's destination number, reactions, polls, pins and the rest of a data message as sent.
One event with the whole content is a smaller change than one event for each of these.

## pkg/signalmeow/events/message.go, pkg/signalmeow/receiving.go: a decryption error's detail

`events.DecryptionError` carries the sender's device, the group (where sealed sender says it), the
content hint and whether the sender was asked to send the message again (`Retried`).

Why: Everysaid notes a message that could not be read in its chat (a group's, where known) and says
whether it was asked for again, as its helper did.

## pkg/signalmeow/web/signalwebsocket.go: HTTP 499

A websocket refused with HTTP 499 ends the connection loop with a fatal error wrapping
`web.ErrClientOutdated`, instead of the general "unexpected status".

Why: 499 is Signal's "this client version is too old": only an update brings the connection back,
and Everysaid says so in those words (`receive_ended` with the code `outdated`), apart from a device
removed from the phone (403, signalmeow's `LoggedOut`).

## pkg/signalmeow/backup.go: update messages and story replies of the transfer archive

The transfer archive's chat items of the kinds `UpdateMessage` (calls, group changes, timer
changes) and `DirectStoryReplyMessage` are kept in `signalmeow_backup_message` with the others,
instead of being dropped.

Why: the history brought at link time keeps the calls of before the link, a group's changes and the
answers to stories, as the messages that arrive later do.
