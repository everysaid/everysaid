// Package db opens SQLite databases (pure Go, modernc.org/sqlite) by path, whatever characters the
// path has.
package db

import (
	"database/sql"
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

// Open opens a database for reading and writing, with foreign keys on and a wait of 30 s on a
// lock (another process writing). pragmas: more, as "name(value)".
func Open(path string, pragmas ...string) (*sql.DB, error) {
	ps := append([]string{"busy_timeout(30000)", "foreign_keys(1)"}, pragmas...)
	return sql.Open("sqlite", dsn(URI(path), ps))
}

// OpenTxImmediate is Open where every transaction begins IMMEDIATE (a writer's: it takes the
// write lock at once, so it never fails half way on a lock another writer took).
func OpenTxImmediate(path string, pragmas ...string) (*sql.DB, error) {
	ps := append([]string{"busy_timeout(30000)", "foreign_keys(1)"}, pragmas...)
	return sql.Open("sqlite", dsn(URI(path)+"?_txlock=immediate", ps))
}

// ReadOnly opens a database read only (a source's: never written).
func ReadOnly(path string) (*sql.DB, error) {
	return sql.Open("sqlite", dsn(URI(path)+"?mode=ro", []string{"busy_timeout(30000)"}))
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
