# mautrix-meta, as taken into Everysaid

The client libraries of mautrix-meta v0.2609.0 (`go.mau.fi/mautrix-meta`, AGPL-3.0, see `LICENSE`
and `LICENSE.exceptions`), taken in place of the module (`replace` in the top `go.mod`). Only:

- `pkg/messagix`: Messenger's client (Lightspeed over MQTT/DGW, the web pages, cookies, and the
  end-to-end encrypted chats through whatsmeow);
- `pkg/instameow`: Instagram's client (its mobile API), on parts of messagix.

The bridge itself (`pkg/connector`, `pkg/igconnector`, `pkg/msgconv`, `pkg/metadb`, `cmd`) is
left out: `internal/meta` reads what the clients give in its own way, with the bridge as the
reference. The tests are left out too.

## Patches

1. `maunium.net/go/mautrix` is not needed. Its only uses were the login step types of `bridgev2`
   (`LoginStep`, `RespError`, the cookie and user input fields and their constants) and four
   `event.Msg*` constants. The types are in a small package of their own,
   `pkg/messagix/loginstub` (the same names and JSON, so the steps mean what they did), imported
   under the name `bridgev2` by:
   - `pkg/messagix/bloks/selenium.go` (also: `event.MsgImage`, `event.MsgAudio` written as their
     values `"m.image"`, `"m.audio"`), `pkg/messagix/bloks/selenium_platform.go`,
     `pkg/messagix/loginerrors/errors.go`, `pkg/messagix/messengerlite.go`;
   - `pkg/instameow/login_account_manager.go`, `login_caa.go`, `login_web_auth_platform.go`,
     `login_web_captcha.go`.
2. `go.mod`: only the requirements of these two packages (no `maunium.net/go/mautrix`, no
   `tool`, no `toolchain`), with mautrix-meta's own
   `replace github.com/imroc/req/v3 => github.com/beeper/req/v3 v3.0.0-20260808092153-100cef0a2fbd`
   (the same `replace` is in the top `go.mod`, which is the one that counts).
3. `gofmt` on the files above (imports sorted).

messagix builds against the whatsmeow of the top `go.mod`
(`go.mau.fi/whatsmeow v0.0.0-20260929112325-8b41cfe6d9c4`): every update of whatsmeow must keep
both building.
