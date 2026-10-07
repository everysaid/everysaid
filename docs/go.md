# Everysaid in Go: building, running, developing

Everysaid is one program, `everysaid`, written in Go: the archive's core, the importers, the
extraction from phones, the server with the interface inside it, the MCP server, and the live
connections (Telegram, WhatsApp) all in one static binary, with no runtime to install. Signal is a
helper program of its own (`everysaid-signal`, licensed AGPL-3.0 as the library it is built on).
`docs/go-port.md` tells how the port from Python was made, `docs/go-ecosystem.md` which libraries it
uses and why.

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

Put it beside `everysaid`, or on the PATH, or name it in the Signal source's settings.

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

The folders, `config.toml`, the keyring's secrets and the archive are the same the Python used:
the Go opens what it made, with nothing to convert. Telegram's session is carried over the first
time (from `telegram-session` to `telegram-session-go`, the first left as it was); WhatsApp keeps
its linked device (the same store folder). The assistant's configuration becomes:

```json
{ "mcpServers": { "everysaid": { "command": "/path/to/everysaid", "args": ["mcp"] } } }
```

and the systemd unit's `ExecStart=%h/.local/bin/everysaid serve`.

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
