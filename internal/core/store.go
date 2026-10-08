// Package core holds every question about the archive and every change to it, for the app, the MCP
// server, the command line and the tests alike. No web framework here.
//
//	store := core.Open(path)                 // one per archive
//	core.Chats(store, ...), core.Stream(store, "p12", ...), core.Search(store, "καλημέρα", ...)
//	core.SetPerson(store, 12, ...), core.MergePeople(store, 12, 40), ...
//
// A failing statement panics (a *db.Error), as in the importers; the server and the MCP server
// recover it into a failed request. A change the user asked for and cannot be made is an
// *errs.UserError, returned.
package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// M is a JSON object as the API gives it.
type M = map[string]any

// Store is one archive, as the core uses it: readers in parallel, one writer at a time.
//
// SQLite in WAL mode lets many readers work while one writes. The core reads through Read() (a pool
// of connections, query_only), and every change goes through Write(), which holds the store's lock
// and commits or rolls back as a whole. Importers keep using archive.Archive (its own connection);
// busy_timeout makes either wait for the other instead of failing.
//
// Version() changes whenever anything in the database changes (from this process or another), so
// caches built from it (the chat list, the names) know when to rebuild.
type Store struct {
	Path string

	read   *sql.DB
	writer *sql.DB
	lock   sync.Mutex

	probeMu   sync.Mutex
	probe     *sql.Conn
	ownWrites atomic.Int64

	cacheMu  sync.Mutex
	cache    map[string]cacheEntry
	building map[string]*sync.Mutex // one build of a key at a time

	counted *counted // the last messages counted for Stats, brought up to date by the next count
}

type cacheEntry struct {
	version [2]int64
	value   any
	at      time.Time // when it was built
}

// Open opens an archive that exists, of the known schema.
func Open(path string) (*Store, error) {
	if path == "" {
		path = archive.DB()
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	read, err := db.Open(abs, "query_only(1)")
	if err != nil {
		return nil, err
	}
	writer, err := db.OpenTxImmediate(abs, "journal_mode(WAL)")
	if err != nil {
		read.Close()
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	// readers stay open between requests, as the Python's one per thread: database/sql keeps only
	// two idle by default, and the rest were closed and opened again (their cache lost) under a
	// few requests at once, which made them take twice as long
	read.SetMaxIdleConns(16)
	s := &Store{Path: abs, read: read, writer: writer, cache: map[string]cacheEntry{}, building: map[string]*sync.Mutex{}}
	if v := archive.Version(read); v != archive.SchemaVersion {
		s.Close()
		return nil, fmt.Errorf("%s: schema v%d, known: v%d", abs, v, archive.SchemaVersion)
	}
	return s, nil
}

// Read is the readers' pool (query_only).
func (s *Store) Read() *sql.DB { return s.read }

// Write runs fn inside one transaction (BEGIN IMMEDIATE), committed at the end, rolled back on an
// error or a panic (which goes on up). One writer at a time within the process.
func (s *Store) Write(fn func(tx *sql.Tx) error) (err error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	defer s.ownWrites.Add(1)
	tx, err := s.writer.Begin() // BEGIN IMMEDIATE (_txlock)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	done = true
	return tx.Commit()
}

// MustWrite is Write for changes that only fail by panicking.
func (s *Store) MustWrite(fn func(tx *sql.Tx)) {
	if err := s.Write(func(tx *sql.Tx) error { fn(tx); return nil }); err != nil {
		panic(&db.Error{Query: "COMMIT", Err: err})
	}
}

// Version changes with every write: SQLite's data_version, as one connection kept for asking it sees
// it (writes by any other connection or process), plus this store's own writes.
func (s *Store) Version() [2]int64 {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.probe == nil {
		c, err := s.read.Conn(context.Background())
		if err != nil {
			panic(&db.Error{Query: "connect", Err: err})
		}
		s.probe = c
	}
	var v int64
	if err := s.probe.QueryRowContext(context.Background(), "PRAGMA data_version").Scan(&v); err != nil {
		panic(&db.Error{Query: "PRAGMA data_version", Err: err})
	}
	return [2]int64{v, s.ownWrites.Load()}
}

// Cached is build() once per version of the database. Requests asking at once for what is not
// built wait for the one build of it rather than each building it again; what was built for an
// older version is let go.
func Cached[T any](s *Store, key string, build func() T) T { return CachedFor(s, key, 0, build) }

// CachedFor is Cached, and built again after maxAge too (0: never): for what depends on more than
// the archive (a plugin's login, which changes only the keyring).
func CachedFor[T any](s *Store, key string, maxAge time.Duration, build func() T) T {
	v := s.Version()
	if value, ok := s.cachedAt(key, v, maxAge); ok {
		return value.(T)
	}
	s.cacheMu.Lock()
	mu := s.building[key]
	if mu == nil {
		mu = &sync.Mutex{}
		s.building[key] = mu
	}
	s.cacheMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	v = s.Version()
	if value, ok := s.cachedAt(key, v, maxAge); ok { // built meanwhile
		return value.(T)
	}
	value := build()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.building[key] == mu { // those waiting have it; later ones find the value
		delete(s.building, key)
	}
	for k, e := range s.cache {
		if older(e.version, v) {
			delete(s.cache, k)
		}
	}
	if e, ok := s.cache[key]; !ok || !older(v, e.version) {
		s.cache[key] = cacheEntry{v, value, time.Now()}
	}
	return value
}

func (s *Store) cachedAt(key string, v [2]int64, maxAge time.Duration) (any, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	hit, ok := s.cache[key]
	if ok && hit.version == v && (maxAge == 0 || time.Since(hit.at) < maxAge) {
		return hit.value, true
	}
	return nil, false
}

// older says whether version a is before b (both of its counts only grow).
func older(a, b [2]int64) bool { return a != b && a[0] <= b[0] && a[1] <= b[1] }

// Setting is a shared setting's value (JSON) decoded into out; false when it is not set.
func (s *Store) Setting(key string, out any) bool {
	raw := db.Str(s.read, "SELECT value FROM setting WHERE key = ?", key)
	if raw == "" {
		return false
	}
	return json.Unmarshal([]byte(raw), out) == nil
}

// SettingAny is a shared setting's value, or def.
func (s *Store) SettingAny(key string, def any) any {
	var v any
	if s.Setting(key, &v) && v != nil {
		return v
	}
	return def
}

// SettingString is a shared setting as text, or def.
func (s *Store) SettingString(key, def string) string {
	var v string
	if s.Setting(key, &v) && v != "" {
		return v
	}
	return def
}

// Language is the language the user last chose (setting `language`), for words said without a request.
func (s *Store) Language() string { return s.SettingString("language", "en") }

func (s *Store) Close() {
	s.probeMu.Lock()
	if s.probe != nil {
		s.probe.Close()
		s.probe = nil
	}
	s.probeMu.Unlock()
	s.read.Close()
	s.writer.Close()
}

// What the core needs of the plugins (set by package plugins, which the core cannot import).
var (
	// StateWeight is how much a plugin's report of a chat's state counts (0: not applied).
	StateWeight = func(pluginID, field string) int { return 0 }
	// NameWeights is the sources of names with their weight, in the order first declared.
	NameWeights = func() []Weight { return nil }
	// NameLabel is a source of names in words.
	NameLabel = func(source, lang string) string { return source }
)

// Weight is how much a source of names is trusted.
type Weight struct {
	Key    string
	Weight int
}
