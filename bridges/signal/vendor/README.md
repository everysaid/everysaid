# Vendored crates

Copies of the libraries the helper is built on, with our fixes. Each is under its own licence
(AGPL-3.0, `LICENSE.md` in each).

| Directory | Upstream | Commit |
|---|---|---|
| `presage/`, `presage-store-sqlite/` | https://github.com/whisperfish/presage | `33dd1491130793390e356e15a5711f1e6ca14fdb` |
| `libsignal-service/` | https://github.com/whisperfish/libsignal-service-rs | `9e6c08b8e6d413391831dc49065e5b05490d6c82` (without `examples/`) |

To see our changes, diff a directory against its upstream commit. To move to a newer upstream,
copy it over and carry the changes below again.

## Our changes

- **presage, a linked device does not set the account's attributes.** On every connect presage
  PUT `/v1/accounts/attributes`, which the server applies to the whole account from any device:
  it turned the Registration Lock off, made the number discoverable, and set an unidentified access
  key from the profile key of the day of linking. A linked device now only PUTs
  `/v1/devices/capabilities`, as Signal Desktop does (libsignal-service:
  `set_device_capabilities`). A device counts as linked if it has a name or a device id other
  than 1.
- **presage, messages to our PNI.** The test for a sealed-sender message to the PNI read the
  envelope's string source field, which the server no longer sends (June 2026), so every message
  to the PNI (the first message of anyone who has only the number) was dropped.
  It now uses `parse_source_service_id`.
- **presage, sync messages only from our own account.** A sync message from anyone else is
  ignored, as in Signal Desktop: it would forge what the owner sent, the contacts or the keys.
- **presage, sync requests answered only by a primary.** A linked device's answer (its partial
  contacts as complete, an empty block list) would be applied by the owner's other devices.
- **libsignal-service and presage, an envelope is acknowledged once handled.** The message pipe
  acknowledged each envelope as it arrived, before it was decrypted or stored, so one in hand when
  the process stopped was gone from the server. The pipe now hands each envelope with its `Ack`,
  and presage sends it when the next one is asked for, after the caller is done with it (Signal
  Desktop answers after storing). One that cannot be decoded is acknowledged at once.
  `receive_messages_tracked` sets a flag while an envelope is being taken in, so that a caller
  about to stop waits for it.
- **libsignal-service and presage, retry requests.** An envelope from a known sender that cannot
  be decrypted gives `ServiceError::DecryptionFailed` with what a retry request needs (sender,
  device, timestamp, ciphertext and type, and for sealed sender the content hint and group).
  presage then asks the sender to send it again, as Signal Desktop's `requestResend`: a
  `DecryptionErrorMessage` as plaintext content (`MessageSender::send_retry_request`), at most 5
  times for a message and 60 an hour for the process, never for a message to our PNI or from a
  non-ACI, nor for plaintext content or bytes that are no Signal message; if it cannot be sent, a
  light session reset (that device's session archived, a null message). A failed pre-key message
  has the pre-keys seen to at once. It yields `Received::DecryptionFailed` with the content hint.
  A device whose session was archived (no current state) gets a new session from its pre-keys
  before anything is sent to it. Redeliveries (`DuplicatedMessage`) and our own sealed
  sender messages give no request. `Received::DecryptionError` is now only a contact's
  `DecryptionErrorMessage` (they could not read ours).
- **libsignal-service, decryption:** a sender key distribution message that cannot be applied is
  logged and the message goes on (Signal Desktop); the sealed sender certificate is checked at the
  server's time, not the sender's (Signal Desktop and Android); an envelope without a source is an
  error, not a panic.
- **presage, someone could not decrypt our message.** A contact's `DecryptionErrorMessage` about a
  message this device sent archives the session it was sent in, when its ratchet key is that
  session's current one, and sends a null message that starts a new one (Signal Desktop's
  `onRetryRequest`, without its log of sent messages to send one again), within the same limits.
- **libsignal-service and presage, pre-keys.** They are seen to every 6 hours while connected (and
  5 minutes after a failure), not only when a connection starts, and the signed pre-key is
  replaced once it is a day and a half old (Signal Desktop); the old ones stay in the store.
- **presage, stopping.** Once the caller's `stop` flag is set, an envelope that comes is not
  taken in: the stream ends without acknowledging it, and the server delivers it again, still
  readable.
- **libsignal-service, the Kyber last-resort key** is replaced with the signed pre-key (a day and
  a half), and the newest is the one uploaded.
- **libsignal-service, one-time pre-keys** are made only where the server is short of them (a
  rotation of the signed and last-resort keys alone uploads none, which the server takes as
  keeping its own), so that the store does not grow with keys never handed out.
- **libsignal-service, keep-alive** every 30 seconds, as Signal Desktop: a dead connection is
  noticed within a minute.
- **libsignal-service, a group change's editor** is a service id (a PNI where someone invited by
  number took up or declined the invitation), not only an ACI, so such changes can be read.

Known limits: the helper keeps no block list (Signal's Blocked sync is not read), so a retry
request can go to someone the owner blocked (within the limits above; Signal Desktop sends none);
after a change of the account's number, what is sent to the new number is acknowledged as not ours
and stays only on the phone (Signal Desktop takes the new number's keys from the phone; this is not
done, only said).
