# Everysaid: building, running, developing

Everysaid is one program, `everysaid`, written in Go: the archive's core, the importers, the
extraction from phones, the server with the interface inside it, the MCP server, and the live
connections (Telegram, WhatsApp) all in one static binary (`CGO_ENABLED=0`), with no runtime to
install. Signal is a helper program of its own (`everysaid-signal`, Rust, under the AGPL-3.0 as
the libraries it is built on): Everysaid starts it and speaks to it in JSON lines on its stdin and
stdout, and links none of its code, so the AGPL covers the helper alone. `docs/design.md` tells
how the app is built.

## Building

```
cd web && pnpm install && pnpm build && cd ..   # the interface, into web/dist
go generate ./internal/webui                     # web/dist into the binary's files
CGO_ENABLED=0 go build -o everysaid ./cmd/everysaid
```

Without `go generate` the binary still builds; its pages then say how to build the interface.
`everysaid serve --web web/dist` serves the interface from the folder instead (for working on it).
Any of Linux, macOS and Windows, on amd64 or arm64, builds from any of them:
`GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/everysaid`.

Signal's helper, only where Signal is wanted (Rust; it needs `protoc` and the OpenSSL headers):

```
cd bridges/signal && cargo build --release      # target/release/everysaid-signal
```

Put it beside `everysaid`, or on the PATH, or name it in the Signal source's settings. Whoever
distributes its binary offers its source under the AGPL.

## Running

The commands are those of `docs/app.md`, from the one binary:

```
everysaid serve [--host H] [--port P]        # the app (http://localhost:8520)
everysaid user link                          # a one-time link: the first passkey, a new device, a way back in
everysaid import [IMPORTER...]               # the importers by hand: sms calls viber whatsapp telegram voip media
everysaid mcp [--db PATH]                    # the MCP server for an assistant (stdio)
everysaid demo --dir /tmp/demo --serve       # invented people, on port 8530
everysaid iphone-sync | iphone-ls | iphone-verify | android-export | telegram-sync
```

An assistant's configuration:

```json
{ "mcpServers": { "everysaid": { "command": "/path/to/everysaid", "args": ["mcp"] } } }
```

and a systemd user unit's `ExecStart=%h/.local/bin/everysaid serve`.

`EVERYSAID_NO_LIVE=1` starts the server without any live connection: for trying it on a copy of an
archive whose accounts another server is connected with (two connections with one key can end the
account's session).

## Developing

```
go test ./...                                   # every package, on demo archives and fixtures
go test ./internal/checks/                      # the whole code: translations, error codes, the interface's rules
go vet ./... && gofmt -l .                      # nothing to say
cd web && pnpm dev                              # the interface with hot reload, /api proxied to :8520
```

End to end, on the demo, against a built binary:

```
everysaid demo --dir /tmp/chr-demo --serve &
cd web && EVERYSAID_CMD=/path/to/everysaid pnpm exec playwright test
```

(without `EVERYSAID_CMD` the tests run `go run ./cmd/everysaid` from the repository's folder).

## How the code is written

- **Errors.** A failing SQL statement panics with `*db.Error`; entry points (`archive.Recover`,
  `db.Recover`, the server's handlers, the importers' `Run`) turn it back into an error. A failure
  the user is told about is `*errs.UserError`, returned, with a code the interface says in its own
  words. Everything else returns `error` as Go does.
- **Database.** `internal/db`: `db.Open`, `db.ReadOnly` (a source's databases: never written), and
  helpers (`Exec`, `Each`, `Row`, `Int`, `Strs`, `Maps`, ...). Importers write through
  `archive.Archive` (its statements open a transaction by themselves; `Commit` ends it). The core
  reads with `store.Read()` and writes inside `store.Write(func(tx *sql.Tx) error)`.
- **JSON.** API values are `core.M` (`map[string]any`) or structs with `json` tags; `nil` is null,
  empty lists are `[]`.
- **Words.** Every word the user reads is translated. Server and plugin words: English in code,
  Greek in `internal/i18n/el_<area>.go` (`func init()` adding to `EL`), one file per area.
  Command-line output too (`i18n.Say`), in the system locale's language. No Greek in Go code
  outside `internal/i18n`. The interface's words are in `web/src/lib/i18n.ts`.
- **Checks.** `go test ./internal/checks/` looks over the whole code: the interface's keys, error
  codes with words, plugin words with Greek, no Greek string in Go code outside `internal/i18n`.
  A new plugin package is added to `internal/all` (blank import) so that the checks and the binary
  load it.
- **Dependencies.** `go get` what is needed; never `go mod tidy`.
- **Tests** sit next to the code they test and run on a demo archive or on fixtures made in the
  test, never on a real archive. Live accounts (Telegram, WhatsApp, Signal) are never connected to
  from tests: a second connection with the same key can end the account's session.
