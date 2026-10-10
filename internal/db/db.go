// Package db opens SQLite databases (pure Go, modernc.org/sqlite) by path, whatever characters the
// path has.
package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

// URI is a file: URI naming the path (any characters, `#`, `?` and `%` included, and Windows drive
// letters), so that it always names that file.
func URI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	p := filepath.ToSlash(abs)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(p)
	return "file://" + p
}

// JournalLimit is the size a WAL is cut back to after a checkpoint (64 MB): without it a WAL stays
// as large as the largest write it ever held (an import of millions of rows left one of 800 MB).
const JournalLimit = 64 << 20

// Open opens a database for reading and writing, with foreign keys on, a wait of 30 s on a lock
// (another process writing) and the WAL kept under JournalLimit. pragmas: more, as "name(value)".
func Open(path string, pragmas ...string) (*sql.DB, error) {
	ps := append([]string{"busy_timeout(30000)", "foreign_keys(1)", fmt.Sprintf("journal_size_limit(%d)", JournalLimit)}, pragmas...)
	return sql.Open("sqlite", dsn(URI(path), ps))
}

// OpenTxImmediate is Open where every transaction begins IMMEDIATE (a writer's: it takes the
// write lock at once, so it never fails half way on a lock another writer took).
func OpenTxImmediate(path string, pragmas ...string) (*sql.DB, error) {
	ps := append([]string{"busy_timeout(30000)", "foreign_keys(1)", fmt.Sprintf("journal_size_limit(%d)", JournalLimit)}, pragmas...)
	return sql.Open("sqlite", dsn(URI(path)+"?_txlock=immediate", ps))
}

// ReadOnly opens a database read only (a source's: never written).
func ReadOnly(path string) (*sql.DB, error) {
	return sql.Open("sqlite", dsn(URI(path)+"?mode=ro", []string{"busy_timeout(30000)"}))
}

// Checkpoint writes the WAL back into the database and empties it (TRUNCATE), so that a large
// write (an import, a rebuild) does not stay on the disk as a WAL of its size. It waits for the
// readers under way; an error (a reader that holds a snapshot) only means it is done later.
func Checkpoint(d *sql.DB) error {
	_, err := d.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

func dsn(uri string, pragmas []string) string {
	sep := "?"
	if strings.Contains(uri, "?") {
		sep = "&"
	}
	for _, p := range pragmas {
		uri += sep + "_pragma=" + p
		sep = "&"
	}
	return uri
}
