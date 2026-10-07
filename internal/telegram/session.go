// The account's keys: the API id and hash, and the session (scripts/telegram-sync.py's
// save_credentials, client, keep_session).
//
// Telethon keeps its session as a StringSession in the secret `telegram-session`; gotd keeps its own
// (JSON: data centre, address, auth key) in `telegram-session-go`. The first time the Go client
// starts without one, it is made from Telethon's: the same auth key, so no new login, and Telethon's
// is left as it is (the Python scripts stay usable beside the Go).
package telegram

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"

	"github.com/gotd/td/session"

	"everysaid/internal/config"
)

const (
	SecretAPIID    = "telegram-api-id"
	SecretAPIHash  = "telegram-api-hash"
	SecretTelethon = "telegram-session"    // Telethon's StringSession
	SecretSession  = "telegram-session-go" // gotd's session
)

// secrets is how the package reaches the keyring; tests put a map in its place.
var secrets = struct {
	get  func(name string) string
	save func(name, value string) (string, error)
}{config.SecretOrEmpty, config.SaveSecret}

// credentials are the API id and hash; ok false when either is missing.
func credentials() (int, string, bool) {
	id, hash := secrets.get(SecretAPIID), secrets.get(SecretAPIHash)
	n, err := strconv.Atoi(id)
	if err != nil || hash == "" {
		return 0, "", false
	}
	return n, hash, true
}

// hasSession says whether there is a session, gotd's or Telethon's.
func hasSession() bool { return secrets.get(SecretSession) != "" || secrets.get(SecretTelethon) != "" }

// FromTelethon is gotd's session (as session.Loader stores it) made from a Telethon StringSession.
func FromTelethon(stringSession string) ([]byte, error) {
	data, err := session.TelethonSession(stringSession)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version int
		Data    session.Data
	}{1, *data})
}

// keyringSession is gotd's session storage in the secret SecretSession, made from Telethon's when
// there is none yet. saved says where it last went (for --login's line).
type keyringSession struct {
	mu    sync.Mutex
	saved string
}

func (s *keyringSession) LoadSession(context.Context) ([]byte, error) {
	if v := secrets.get(SecretSession); v != "" {
		return []byte(v), nil
	}
	if old := secrets.get(SecretTelethon); old != "" {
		return FromTelethon(old)
	}
	return nil, session.ErrNotFound
}

// StoreSession keeps the session if it is new or changed (another data centre after a migration,
// a new salt).
func (s *keyringSession) StoreSession(_ context.Context, data []byte) error {
	if string(data) == secrets.get(SecretSession) {
		return nil
	}
	where, err := secrets.save(SecretSession, string(data))
	s.mu.Lock()
	s.saved = where
	s.mu.Unlock()
	return err
}

func (s *keyringSession) where() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved
}
