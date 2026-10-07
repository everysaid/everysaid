# The Go port: how it is written

Everysaid is being ported from Python to Go, for one static binary (no cgo) on Linux, macOS and
Windows. The Python code stays, untouched, as the reference until the port is audited: **no Python
file is deleted or changed**. `docs/go-ecosystem.md` has the libraries chosen and why.

## Layout

| Python | Go |
|---|---|
| `everysaid/config.py` | `internal/config` (folders like platformdirs, `config.toml`, secrets in the keyring or a 600 file) |
| `everysaid/text.py` | `internal/text` (`Fold`, `Query`, `Matcher`, `Lower` as Python's `str.lower`) |
| `everysaid/archive.py` | `internal/archive` (schema, `Archive` for importers, `Address`, `Phone`, `Fingerprint`) |
| `everysaid/errors.py` | `internal/errs` (`UserError`) |
| `everysaid/plugins/i18n.py` | `internal/i18n` (`Tr`, `T`, `Format`; Greek in `el*.go`) |
| `everysaid/core/*` | `internal/core` (`Store`, names, queries, changes, labels) |
| importers (`sms.py` … `imlogs.py`, `extras.py`, `emoticons.py`, `carriers/`) | `internal/importers` |
| `everysaid/plugins/*` | `internal/plugins` (base, registry, instances) and one package per plugin family |
| `everysaid/server/*` | `internal/server` (a library: `server.New(...)` gives an `http.Handler`, so any shell can embed it) |
| `everysaid/mcp_server.py` | `internal/mcp` |
| `everysaid/demo.py` | `internal/demo` |
| `everysaid/cli.py` and the scripts the app runs | `cmd/everysaid` (one binary, subcommands) |
| `bridges/whatsapp/` (Go, a separate process) | `internal/whatsapp` (in-process, same store files) |
| — | `bridges/signal/` (Rust, presage, AGPL-3.0: a separate helper, JSON over stdin/stdout) |

The owner's own picture tools (`immich-*`, `media-*`, `vlm-review.py`) stay Python and are not
part of the distribution.

## Rules

- **A faithful port.** Each Go file says at the top which Python file it ports. Same behaviour,
  same SQL, same JSON shapes (keys, nulls, order of lists), same edge cases; the Python is the
  specification. Where Python's semantics differ from Go's (`str.lower`, `\s` and `\d` being
  Unicode, `isalnum`, integer division of negatives, `round`, sorting stability, dict order),
  match Python's. Comments keep the project's style: plain prose saying why, not what.
- **The archive does not change.** Same schema, same files, same keyring names, same folders: the
  Go binary opens what the Python made, and the Python scripts keep working beside it.
- **Errors.** A failing SQL statement panics with `*db.Error` (as a Python exception would stop
  the script); entry points (`archive.Recover`, `db.Recover`, the server's handlers, the importers'
  `Run`) turn it back into an error. A failure the user is told about is `*errs.UserError`,
  returned. Everything else returns `error` as Go does.
- **Database.** `internal/db`: `db.Open`, `db.ReadOnly` (a source's databases: never written),
  helpers `Exec`, `Each`, `Row`, `Int`, `IntOK`, `Ints`, `Strs`, `Exists`, `Maps`, `Marks`, `Args`,
  `NullStr`, `NullID`, `B`. Importers write through `archive.Archive` (its statements open a
  transaction by themselves; `Commit` ends it, as Python's sqlite3 does). The core reads with
  `store.Read()` and writes inside `store.Write(func(tx *sql.Tx) error)`.
- **JSON.** API values are `core.M` (`map[string]any`) or structs with `json` tags; `nil` is null.
  Empty lists are `[]`, never null, where Python gives `[]`.
- **Words.** Every word the user reads is translated. Server and plugin words: English in code,
  Greek in `internal/i18n/el_<area>.go` (`func init()` adding to `EL`), one file per area so that
  parallel work never edits the same file. Command-line output too (`i18n.Say`), in the system
  locale's language. No Greek in Go code outside `internal/i18n`.
- **Dependencies.** `go get` what you need; never `go mod tidy` (others' packages may be half
  written). Do not edit packages owned by another part of the work; if you need something from
  them, write it in your own package and say so in your report.
- **Tests.** Each Python test file is ported to Go tests next to the code it tests. Tests run on a
  demo archive or on fixtures made in the test, never on the owner's archive.
- **The owner's data.** Read only, and only for parity checks: build an archive in a temporary
  folder from the same sources with the Python and with the Go, and compare them by counts and
  hashes. Never print message text, names or numbers; numbers, timings and status codes only.
  Never write to `~/.local/share/everysaid`, `~/.cache/everysaid` (except a temporary folder of
  your own), the backups, or a phone. Never read `~/.config/everysaid/backup-password` yourself
  (the ported code may read it through `config.Secret`, as the scripts do; never print it).
- **Accounts.** Never connect to the owner's Telegram, WhatsApp or Signal accounts with their
  sessions: a second connection with the same key can end the owner's session. Live connections
  are tested only at the switch-over, by the main session.
- No `sudo`, no installs (say what is missing); no AI attribution anywhere.
