// Package keys is the one file of the secrets the live services need: the devices' keys and
// sessions (whatsmeow's store, Signal's, Telegram's session and the peers' access hashes, Meta's
// cookies), each service in tables of its own prefix. It is <data>/keys.db, a SQLite database
// encrypted whole (Adiantum, through ncruces/go-sqlite3's VFS, pure Go), with a key of 32 random
// bytes made once and kept in the system's keyring (config.SaveSecret: its file where there is no
// keyring). The archive itself stays open: only what would let someone act as the owner's devices
// is here.
//
// A key is made only when keys.db does not exist yet. A keys.db with no key to open it (a keyring
// not unlocked yet) is an error, never a reason to make a new one: that would lose every session.
package keys

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"

	"everysaid/internal/config"
	"everysaid/internal/db"
)

// Secret is the name of keys.db's key among the secrets.
const Secret = "keys-db"

// Driver is the database/sql driver keys.db is opened with (ncruces's), for libraries that take a
// dialect: SQLite's.
const Driver = "sqlite3"

// ErrNoKey: keys.db exists and no key for it was found (the keyring locked, or the key lost).
var ErrNoKey = errors.New("keys.db exists but its key is not in the keyring")

var (
	mu    sync.Mutex
	opens = map[string]*sql.DB{}
)

// Path is keys.db's path.
func Path() string { return filepath.Join(config.Data, "keys.db") }

// Open is keys.db (Path), opened once per process and shared: made, with its key, the first time.
func Open() (*sql.DB, error) { return OpenAt(Path()) }

// OpenAt is Open for a keys database at path (tests, a demo).
func OpenAt(path string) (*sql.DB, error) {
	mu.Lock()
	defer mu.Unlock()
	if d := opens[path]; d != nil {
		if _, err := os.Stat(path); err == nil {
			return d, nil
		}
		d.Close() // removed since (a test, a reset): made again
		delete(opens, path)
	}
	key, err := keyFor(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	d, err := sql.Open(Driver, DSN(path, key))
	if err != nil {
		return nil, err
	}
	// a wrong key reads as a database that is not one
	if _, err := d.Exec("SELECT count(*) FROM sqlite_master"); err != nil {
		d.Close()
		return nil, fmt.Errorf("keys.db: %w", err)
	}
	os.Chmod(path, 0o600)
	opens[path] = d
	return d, nil
}

// Own is a connection pool of its own to the keys database at path ("" for Path()), for a caller
// that sets its pool's limits and closes it; immediate: its transactions begin as writers'.
func Own(path string, immediate bool) (*sql.DB, error) {
	if path == "" {
		path = Path()
	}
	if _, err := OpenAt(path); err != nil { // made, and its key checked, once
		return nil, err
	}
	key, err := keyFor(path)
	if err != nil {
		return nil, err
	}
	dsn := DSN(path, key)
	if immediate {
		dsn += "&_txlock=immediate"
	}
	return sql.Open(Driver, dsn)
}

// DSN is the data source name of an encrypted database at path with key (hex), WAL, a wait of
// 30 s on a lock and foreign keys on.
func DSN(path, key string) string {
	uri := db.URI(path) + "?vfs=adiantum"
	for _, p := range []string{"hexkey('" + key + "')", "busy_timeout(30000)", "foreign_keys(1)",
		"journal_mode(WAL)", fmt.Sprintf("journal_size_limit(%d)", db.JournalLimit)} {
		uri += "&_pragma=" + p
	}
	return uri
}

// keyFor is the key of the keys database at path: the saved one, or a new one where there is no
// database yet. The default database's key is the secret Secret; another's is kept beside it
// (path + ".key", 600), for tests and demos.
func keyFor(path string) (string, error) {
	_, statErr := os.Stat(path)
	exists := statErr == nil
	var key string
	if path == Path() {
		k, err := config.Secret(Secret)
		if err != nil {
			return "", err
		}
		key = k
	} else if b, err := os.ReadFile(path + ".key"); err == nil {
		key = strings.TrimSpace(string(b))
	}
	if key != "" {
		return key, nil
	}
	if exists {
		return "", ErrNoKey
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key = hex.EncodeToString(b)
	if path == Path() {
		if _, err := config.SaveSecret(Secret, key); err != nil {
			return "", err
		}
		if back, _ := config.Secret(Secret); back != key {
			return "", errors.New("keys.db: the new key did not read back from the keyring")
		}
	} else if err := os.WriteFile(path+".key", []byte(key), 0o600); err != nil {
		return "", err
	}
	return key, nil
}

// Close closes every keys database opened (at the process's end, in tests).
func Close() {
	mu.Lock()
	defer mu.Unlock()
	for p, d := range opens {
		d.Close()
		delete(opens, p)
	}
}
