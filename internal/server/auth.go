// Ports everysaid/server/auth.py.
//
// Who may use the app: users, their passkeys, sessions and recovery codes, in `<data>/server.db`
// (apart from the archives: one server, later more than one user, each with an archive of their own).
//
//   - Passkeys (WebAuthn) are the first way in: a fingerprint, a face, a security key or a password
//     manager; discoverable, so logging in asks for nothing first.
//   - Where a passkey cannot be used (a browser or password manager that refuses it), a password
//     together with a 6-digit code from an authenticator app (TOTP, RFC 6238) is the other way: both
//     are required. Passwords are kept as scrypt hashes; a code is accepted once.
//   - The first passkey of a new server is made through a one-time setup link printed on the terminal
//     (`everysaid serve` prints it while there is no user; `everysaid user link` prints a new one any
//     time, also for adding a passkey on a new device or after losing one).
//   - Recovery codes (ten, each once) let the user in without a passkey, to add a new one.
//   - A session is a random token in an HttpOnly, SameSite=Strict cookie (Secure over HTTPS); only its
//     hash is stored. Sessions are listed and can be ended one by one; idle ones end after 30 days.
//
// The database is the Python's, unchanged: the same tables, hashes and formats, so that passkeys,
// sessions, passwords and push subscriptions made by either server work with the other.
package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/scrypt"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/pyjson"
)

const (
	SessionDays = 30
	Cookie      = "everysaid_session"
)

// AuthDB is the server's database of users: <data>/server.db.
func AuthDB() string { return filepath.Join(config.Data, "server.db") }

const authSchema = `
CREATE TABLE IF NOT EXISTS user (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    handle BLOB NOT NULL UNIQUE,            -- WebAuthn user handle (random)
    archive TEXT NOT NULL,                  -- the user's archive
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS passkey (
    id BLOB PRIMARY KEY,                    -- credential id
    user_id INTEGER NOT NULL REFERENCES user,
    public_key BLOB NOT NULL,
    sign_count INTEGER NOT NULL,
    transports TEXT,
    name TEXT,
    created_at INTEGER NOT NULL,
    last_used INTEGER
);
CREATE TABLE IF NOT EXISTS session (
    hash TEXT PRIMARY KEY,                  -- sha256 of the token
    user_id INTEGER NOT NULL REFERENCES user,
    created_at INTEGER NOT NULL,
    last_seen INTEGER NOT NULL,
    agent TEXT,
    ip TEXT,
    via TEXT                                -- passkey, recovery, setup
);
CREATE TABLE IF NOT EXISTS recovery_code (
    user_id INTEGER NOT NULL REFERENCES user,
    hash TEXT NOT NULL,
    used_at INTEGER,
    PRIMARY KEY (user_id, hash)
);
CREATE TABLE IF NOT EXISTS push_subscription (
    endpoint TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES user,
    keys TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp_token (
    hash TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES user,
    label TEXT,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
    ts INTEGER NOT NULL,
    user_id INTEGER,
    event TEXT NOT NULL,
    detail TEXT
);
CREATE TABLE IF NOT EXISTS kv (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// h is the hash a token is kept by: sha256 of it, in hex.
func h(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokenURLSafe is Python's secrets.token_urlsafe(n): n random bytes in base64url without padding.
func tokenURLSafe(n int) string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(n))
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// User is a user row, without its handle unless asked.
type User struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Archive string `json:"archive"`
	Handle  []byte `json:"-"`
}

type challenge struct {
	expires float64
	value   any // what the step needs back: a passkey ceremony's session, an authenticator secret
	purpose string
	info    map[string]any
}

type setupLink struct {
	expires float64
	uid     *int64
}

// Auth is server.db and what lives only in memory beside it (setup links made by this process,
// challenges, attempts).
type Auth struct {
	Path string
	db   *sql.DB

	mu         sync.Mutex
	setup      map[string]setupLink
	challenges map[string]challenge
	attempts   map[string][]float64
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// OpenAuth opens (and makes, if new) the server's database.
func OpenAuth(path string) (*Auth, error) {
	if path == "" {
		path = AuthDB()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// As the Python's: no foreign keys enforced, a wait on another writer
	d, err := db.Open(path, "journal_mode(WAL)", "foreign_keys(0)")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1) // one connection, as the Python's: its writes are each its own transaction
	a := &Auth{Path: path, db: d, setup: map[string]setupLink{}, challenges: map[string]challenge{},
		attempts: map[string][]float64{}}
	err = func() (err error) {
		defer db.Recover(&err)
		if _, err := d.Exec(authSchema); err != nil {
			return err
		}
		have := map[string]bool{}
		db.Each(d, "PRAGMA table_info(user)", nil, func(scan func(...any)) {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt sql.NullString
			scan(&cid, &name, &typ, &notnull, &dflt, &pk)
			have[name] = true
		})
		for _, col := range []string{"password TEXT", "totp TEXT", "totp_step INTEGER"} {
			if !have[strings.Fields(col)[0]] {
				db.Exec(d, "ALTER TABLE user ADD COLUMN "+col)
			}
		}
		return nil
	}()
	if err != nil {
		d.Close()
		return nil, err
	}
	os.Chmod(path, 0o600)
	return a, nil
}

func (a *Auth) Close() error { return a.db.Close() }

// DB is the database itself (for tests and the command line).
func (a *Auth) DB() *sql.DB { return a.db }

// --- users ---------------------------------------------------------------------------------------

func (a *Auth) Users() []User {
	out := []User{}
	db.Each(a.db, "SELECT id, name, archive, created_at FROM user ORDER BY id", nil, func(scan func(...any)) {
		var u User
		var created int64
		scan(&u.ID, &u.Name, &u.Archive, &created)
		out = append(out, u)
	})
	return out
}

// User is a user by id, or nil.
func (a *Auth) User(uid int64) *User {
	var u User
	if !db.Row(a.db, "SELECT id, name, handle, archive FROM user WHERE id = ?", []any{uid}, &u.ID, &u.Name, &u.Handle, &u.Archive) {
		return nil
	}
	return &u
}

func (a *Auth) CreateUser(name, archive string) int64 {
	uid := db.LastID(a.db, "INSERT INTO user (name, handle, archive, created_at) VALUES (?, ?, ?, ?)",
		name, randomBytes(32), archive, time.Now().Unix())
	a.Log(&uid, "user created", name)
	return uid
}

func (a *Auth) setHandle(uid int64, handle []byte) {
	db.Exec(a.db, "UPDATE user SET handle = ? WHERE id = ?", handle, uid)
}

// --- setup links (first passkey, or another one) -------------------------------------------------

// SetupLink makes a one-time link for a user (nil: a new one), valid so many minutes; it returns
// its token. Links live in the database too, so that one made by `everysaid user link` works.
func (a *Auth) SetupLink(uid *int64, minutes int) string {
	token := tokenURLSafe(32)
	exp := now() + float64(minutes*60)
	a.mu.Lock()
	a.setup[token] = setupLink{exp, uid}
	a.mu.Unlock()
	var u any
	if uid != nil {
		u = *uid
	}
	db.Exec(a.db, "INSERT OR REPLACE INTO kv VALUES (?, ?)", "setup:"+h(token), pyjson.Dumps([]any{exp, u}, true))
	return token
}

func (a *Auth) findSetup(token string) (setupLink, bool) {
	if token == "" {
		return setupLink{}, false
	}
	a.mu.Lock()
	l, ok := a.setup[token]
	a.mu.Unlock()
	if ok {
		return l, true
	}
	raw := db.Str(a.db, "SELECT value FROM kv WHERE key = ?", "setup:"+h(token))
	if raw == "" {
		return setupLink{}, false
	}
	var v []any
	if json.Unmarshal([]byte(raw), &v) != nil || len(v) != 2 {
		return setupLink{}, false
	}
	exp, _ := v[0].(float64)
	l = setupLink{expires: exp}
	if f, ok := v[1].(float64); ok {
		uid := int64(f)
		l.uid = &uid
	}
	return l, true
}

// SetupLeft is how many seconds a setup link has left (0: none, or used, or expired).
func (a *Auth) SetupLeft(token string) int64 {
	l, ok := a.findSetup(token)
	if !ok {
		return 0
	}
	return max(0, int64(l.expires-now()))
}

// TakeSetup says whether a setup link is valid, and whose it is (nil: a new user); consume uses it up.
func (a *Auth) TakeSetup(token string, consume bool) (bool, *int64) {
	l, ok := a.findSetup(token)
	if !ok || l.expires < now() {
		return false, nil
	}
	if consume {
		a.mu.Lock()
		delete(a.setup, token)
		a.mu.Unlock()
		db.Exec(a.db, "DELETE FROM kv WHERE key = ?", "setup:"+h(token))
	}
	return true, l.uid
}

// --- challenges ----------------------------------------------------------------------------------

func (a *Auth) Challenge(value any, purpose string, info map[string]any, ttl float64) string {
	if ttl == 0 {
		ttl = 300
	}
	nonce := tokenURLSafe(24)
	t := now()
	a.mu.Lock()
	for k, v := range a.challenges {
		if v.expires <= t {
			delete(a.challenges, k)
		}
	}
	a.challenges[nonce] = challenge{t + ttl, value, purpose, info}
	a.mu.Unlock()
	return nonce
}

// TakeChallenge is what a challenge keeps, or false if unknown or expired; keep leaves it for
// another try.
func (a *Auth) TakeChallenge(nonce, purpose string, keep bool) (any, map[string]any, bool) {
	a.mu.Lock()
	v, ok := a.challenges[nonce]
	if !keep {
		delete(a.challenges, nonce)
	}
	a.mu.Unlock()
	if !ok || v.expires < now() || v.purpose != purpose {
		return nil, nil, false
	}
	return v.value, v.info, true
}

// --- passkeys ------------------------------------------------------------------------------------

func (a *Auth) Passkeys(uid int64) []map[string]any {
	out := []map[string]any{}
	db.Each(a.db, "SELECT id, name, created_at, last_used, transports FROM passkey WHERE user_id = ? ORDER BY created_at",
		[]any{uid}, func(scan func(...any)) {
			var id []byte
			var name, transports sql.NullString
			var created int64
			var used sql.NullInt64
			scan(&id, &name, &created, &used, &transports)
			var t any = []any{}
			if transports.String != "" {
				json.Unmarshal([]byte(transports.String), &t)
			}
			out = append(out, map[string]any{"id": hex.EncodeToString(id), "name": nullStr(name), "created_at": created,
				"last_used": nullInt(used), "transports": t})
		})
	return out
}

func (a *Auth) passkeyIDs(uid int64) [][]byte {
	var out [][]byte
	db.Each(a.db, "SELECT id FROM passkey WHERE user_id = ? ORDER BY created_at", []any{uid}, func(scan func(...any)) {
		var id []byte
		scan(&id)
		out = append(out, id)
	})
	return out
}

func (a *Auth) AddPasskey(uid int64, credID, publicKey []byte, signCount uint32, transports any, name string) {
	if transports == nil {
		transports = []any{}
	}
	db.Exec(a.db, "INSERT INTO passkey (id, user_id, public_key, sign_count, transports, name, created_at) "+
		"VALUES (?, ?, ?, ?, ?, ?, ?)", credID, uid, publicKey, int64(signCount), pyjson.Dumps(transports, true), name,
		time.Now().Unix())
	a.Log(&uid, "passkey added", name)
}

type storedPasskey struct {
	uid       int64
	publicKey []byte
	signCount int64
}

func (a *Auth) passkey(credID []byte) *storedPasskey {
	var p storedPasskey
	if !db.Row(a.db, "SELECT user_id, public_key, sign_count FROM passkey WHERE id = ?", []any{credID}, &p.uid, &p.publicKey, &p.signCount) {
		return nil
	}
	return &p
}

func (a *Auth) usedPasskey(credID []byte, signCount uint32) {
	db.Exec(a.db, "UPDATE passkey SET sign_count = ?, last_used = ? WHERE id = ?", int64(signCount), time.Now().Unix(), credID)
}

func (a *Auth) RemovePasskey(uid int64, credHex string) error {
	if len(a.Passkeys(uid)) <= 1 && !a.HasPassword(uid) {
		return errs.New("auth.last_passkey", 0, nil)
	}
	id, err := hex.DecodeString(credHex)
	if err != nil {
		return err
	}
	db.Exec(a.db, "DELETE FROM passkey WHERE user_id = ? AND id = ?", uid, id)
	a.Log(&uid, "passkey removed", pyCut(credHex, 12))
	return nil
}

func (a *Auth) RenamePasskey(uid int64, credHex, name string) error {
	id, err := hex.DecodeString(credHex)
	if err != nil {
		return err
	}
	db.Exec(a.db, "UPDATE passkey SET name = ? WHERE user_id = ? AND id = ?", name, uid, id)
	return nil
}

// --- recovery codes ------------------------------------------------------------------------------

func (a *Auth) NewRecoveryCodes(uid int64) []string {
	codes := make([]string, 10)
	for i := range codes {
		parts := make([]string, 4)
		for j := range parts {
			parts[j] = hex.EncodeToString(randomBytes(2))
		}
		codes[i] = strings.Join(parts, "-")
	}
	tx, err := a.db.Begin()
	if err != nil {
		panic(&db.Error{Query: "BEGIN", Err: err})
	}
	db.Exec(tx, "DELETE FROM recovery_code WHERE user_id = ?", uid)
	for _, c := range codes {
		db.Exec(tx, "INSERT INTO recovery_code (user_id, hash) VALUES (?, ?)", uid, h(c))
	}
	if err := tx.Commit(); err != nil {
		panic(&db.Error{Query: "COMMIT", Err: err})
	}
	a.Log(&uid, "recovery codes made", nil)
	return codes
}

// UseRecoveryCode is the user whose unused code this is (the code then used), or 0.
func (a *Auth) UseRecoveryCode(code string) int64 {
	code = strings.ToLower(strings.TrimSpace(code))
	uid, ok := db.IntOK(a.db, "SELECT user_id FROM recovery_code WHERE hash = ? AND used_at IS NULL", h(code))
	if !ok {
		return 0
	}
	db.Exec(a.db, "UPDATE recovery_code SET used_at = ? WHERE hash = ?", time.Now().Unix(), h(code))
	a.Log(&uid, "recovery code used", nil)
	return uid
}

func (a *Auth) RecoveryLeft(uid int64) int64 {
	return db.Int(a.db, "SELECT count(*) FROM recovery_code WHERE user_id = ? AND used_at IS NULL", uid)
}

// --- sessions ------------------------------------------------------------------------------------

func (a *Auth) NewSession(uid int64, agent, ip, via string) string {
	token := tokenURLSafe(32)
	t := time.Now().Unix()
	var ipv any
	if ip != "" {
		ipv = ip
	}
	db.Exec(a.db, "INSERT INTO session VALUES (?, ?, ?, ?, ?, ?, ?)", h(token), uid, t, t, pyCut(agent, 200), ipv, via)
	a.Log(&uid, "login", via)
	return token
}

// Recent says whether this session signed in within the last minutes: changing the ways in asks
// for it, so that a stolen session cannot add one of its own.
func (a *Auth) Recent(token string, minutes int) bool {
	if token == "" {
		return false
	}
	created, ok := db.IntOK(a.db, "SELECT created_at FROM session WHERE hash = ?", h(token))
	return ok && float64(created) >= now()-float64(minutes*60)
}

// Session is the user and session hash of a live session, renewing it; ok false otherwise.
func (a *Auth) Session(token string) (int64, string, bool) {
	if token == "" {
		return 0, "", false
	}
	hs := h(token)
	var uid, last int64
	if !db.Row(a.db, "SELECT user_id, last_seen FROM session WHERE hash = ?", []any{hs}, &uid, &last) {
		return 0, "", false
	}
	t := time.Now().Unix()
	if t-last > SessionDays*86400 {
		db.Exec(a.db, "DELETE FROM session WHERE hash = ?", hs)
		return 0, "", false
	}
	if t-last > 60 {
		db.Exec(a.db, "UPDATE session SET last_seen = ? WHERE hash = ?", t, hs)
	}
	return uid, hs, true
}

func (a *Auth) Sessions(uid int64) []map[string]any {
	out := []map[string]any{}
	db.Each(a.db, "SELECT hash, created_at, last_seen, agent, ip, via FROM session WHERE user_id = ? ORDER BY last_seen DESC",
		[]any{uid}, func(scan func(...any)) {
			var hs string
			var c, s int64
			var agent, ip, via sql.NullString
			scan(&hs, &c, &s, &agent, &ip, &via)
			out = append(out, map[string]any{"id": hs[:16], "created_at": c, "last_seen": s, "agent": nullStr(agent),
				"ip": nullStr(ip), "via": nullStr(via)})
		})
	return out
}

func (a *Auth) EndSession(uid int64, shortID string) {
	db.Exec(a.db, "DELETE FROM session WHERE user_id = ? AND substr(hash, 1, 16) = ?", uid, shortID)
	a.Log(&uid, "session ended", shortID)
}

func (a *Auth) EndSessionHash(hs string) {
	db.Exec(a.db, "DELETE FROM session WHERE hash = ?", hs)
}

// --- MCP tokens ----------------------------------------------------------------------------------

func (a *Auth) NewMCPToken(uid int64, label string) string {
	token := "chk_" + tokenURLSafe(32)
	db.Exec(a.db, "INSERT INTO mcp_token VALUES (?, ?, ?, ?)", h(token), uid, label, time.Now().Unix())
	a.Log(&uid, "mcp token made", label)
	return token
}

// MCPUser is the user of an MCP token, or 0.
func (a *Auth) MCPUser(token string) int64 {
	uid, _ := db.IntOK(a.db, "SELECT user_id FROM mcp_token WHERE hash = ?", h(token))
	return uid
}

// --- rate limit and audit ------------------------------------------------------------------------

// Allow says whether another attempt from this address is allowed: limit within window seconds.
func (a *Auth) Allow(ip string, limit int, window float64) bool {
	if limit == 0 {
		limit, window = 20, 300
	}
	t := now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.attempts) > 10_000 { // addresses not seen within the window are forgotten
		for k, times := range a.attempts {
			if len(times) == 0 || times[len(times)-1] <= t-window {
				delete(a.attempts, k)
			}
		}
	}
	var seen []float64
	for _, x := range a.attempts[ip] {
		if x > t-window {
			seen = append(seen, x)
		}
	}
	if len(seen) >= limit {
		a.attempts[ip] = seen
		return false
	}
	a.attempts[ip] = append(seen, t)
	return true
}

// Log is a line of the audit log; uid and detail may be nil.
func (a *Auth) Log(uid *int64, event string, detail any) {
	var u any
	if uid != nil {
		u = *uid
	}
	db.Exec(a.db, "INSERT INTO audit VALUES (?, ?, ?, ?)", time.Now().Unix(), u, event, detail)
}

func (a *Auth) Audit(uid int64, limit int) []map[string]any {
	out := []map[string]any{}
	db.Each(a.db, "SELECT ts, event, detail FROM audit WHERE user_id = ? ORDER BY ts DESC LIMIT ?", []any{uid, limit},
		func(scan func(...any)) {
			var ts int64
			var event string
			var detail sql.NullString
			scan(&ts, &event, &detail)
			out = append(out, map[string]any{"ts": ts, "event": event, "detail": nullStr(detail)})
		})
	return out
}

// KV is a stored value (JSON) decoded into out; false when there is none.
func (a *Auth) KV(key string, out any) bool {
	raw := db.Str(a.db, "SELECT value FROM kv WHERE key = ?", key)
	return raw != "" && json.Unmarshal([]byte(raw), out) == nil
}

func (a *Auth) SetKV(key string, value any) {
	db.Exec(a.db, "INSERT OR REPLACE INTO kv VALUES (?, ?)", key, pyjson.Dumps(value, true))
}

// --- password and authenticator code -------------------------------------------------------------

const (
	lockFailures = 5
	lockMinutes  = 15
)

// Locked: five failed password sign-ins from one address within 15 minutes lock the password way
// for that address for the rest of that time (others, passkeys and recovery codes still work:
// whoever knows the name cannot lock the user out). Each address is also rate limited.
func (a *Auth) Locked(uid int64, ip string) bool {
	since := time.Now().Unix() - lockMinutes*60
	var ipv any
	if ip != "" {
		ipv = ip
	}
	n := db.Int(a.db, "SELECT count(*) FROM audit WHERE user_id = ? AND event = 'password login failed' AND ts >= ? "+
		"AND detail IS ? AND ts > ifnull((SELECT max(ts) FROM audit WHERE user_id = ? AND event = 'login'), 0)",
		uid, since, ipv, uid)
	return n >= lockFailures
}

// SetPassword: usedStep is the time step of the code that confirmed the secret (not accepted again).
func (a *Auth) SetPassword(uid int64, password, totpSecret string, usedStep *int64) {
	var step any
	if usedStep != nil {
		step = *usedStep
	}
	db.Exec(a.db, "UPDATE user SET password = ?, totp = ?, totp_step = ? WHERE id = ?", HashPassword(password), totpSecret, step, uid)
	a.Log(&uid, "password set", nil)
}

func (a *Auth) ClearPassword(uid int64) error {
	if len(a.Passkeys(uid)) == 0 {
		return errs.New("auth.only_password", 0, nil)
	}
	db.Exec(a.db, "UPDATE user SET password = NULL, totp = NULL, totp_step = NULL WHERE id = ?", uid)
	a.Log(&uid, "password removed", nil)
	return nil
}

func (a *Auth) HasPassword(uid int64) bool {
	return db.Exists(a.db, "SELECT 1 FROM user WHERE id = ? AND password IS NOT NULL", uid)
}

// CheckPassword is the user whose name, password and current code these are, or 0 (the same work
// either way).
func (a *Auth) CheckPassword(name, password, code, ip string) int64 {
	var uid int64
	var stored string
	var secret sql.NullString
	var last sql.NullInt64
	found := db.Row(a.db, "SELECT id, password, totp, totp_step FROM user WHERE lower(name) = lower(?) AND password IS NOT NULL",
		[]any{strings.TrimFunc(name, unicode.IsSpace)}, &uid, &stored, &secret, &last)
	if !found {
		stored = dummyHash()
	}
	if found && a.Locked(uid, ip) {
		VerifyPassword(password, dummyHash()) // the same time spent
		a.Log(&uid, "password login refused", "too many failures")
		return 0
	}
	ok := VerifyPassword(password, stored)
	var step int64
	stepOK := false
	if secret.String != "" {
		var lastStep *int64
		if last.Valid {
			lastStep = &last.Int64
		}
		step, stepOK = TOTPCheck(secret.String, code, lastStep)
	}
	if !(found && ok && stepOK) {
		if found {
			var ipv any
			if ip != "" {
				ipv = ip
			}
			a.Log(&uid, "password login failed", ipv)
		}
		return 0
	}
	db.Exec(a.db, "UPDATE user SET totp_step = ? WHERE id = ?", step, uid)
	return uid
}

// HashPassword is scrypt (N 2^15, r 8, p 1, 32 bytes) of the password with a random salt, kept as
// "scrypt$N$r$p$salt$hash" (hex).
func HashPassword(password string) string {
	const n, r, p = 1 << 15, 8, 1
	salt := randomBytes(16)
	k, err := scrypt.Key([]byte(password), salt, n, r, p, 32)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("scrypt$%d$%d$%d$%s$%s", n, r, p, hex.EncodeToString(salt), hex.EncodeToString(k))
}

// VerifyPassword says whether the password is the one of a stored hash.
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 {
		return false
	}
	n, err1 := strconv.Atoi(strings.TrimSpace(parts[1]))
	r, err2 := strconv.Atoi(strings.TrimSpace(parts[2]))
	p, err3 := strconv.Atoi(strings.TrimSpace(parts[3]))
	salt, err4 := hex.DecodeString(parts[4])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return false
	}
	// the Python's limit (maxmem 64 MB): a hash asking for more is refused, not computed
	if n <= 1 || r <= 0 || p <= 0 || float64(128)*float64(n)*float64(r)+float64(128)*float64(r)*float64(p) > 64*1024*1024 {
		return false
	}
	got, err := scrypt.Key([]byte(password), salt, n, r, p, 32)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(got)), []byte(parts[5])) == 1
}

var (
	dummyOnce sync.Once
	dummy     string
)

// dummyHash is a hash of nothing anyone knows, for unknown names: the same time spent.
func dummyHash() string {
	dummyOnce.Do(func() { dummy = HashPassword(hex.EncodeToString(randomBytes(8))) })
	return dummy
}

// TOTPSecret is a new authenticator secret: 20 random bytes in base32, without padding.
func TOTPSecret() string {
	return strings.TrimRight(base32.StdEncoding.EncodeToString(randomBytes(20)), "=")
}

// TOTPAt is the 6-digit code of a secret at a time step (RFC 6238, SHA-1, 30 s).
func TOTPAt(secret string, step int64) string {
	pad := (8 - len(secret)%8) % 8
	key, err := base32.StdEncoding.DecodeString(secret + strings.Repeat("=", pad))
	if err != nil {
		return ""
	}
	mac := hmac.New(sha1.New, key)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac.Write(msg[:])
	d := mac.Sum(nil)
	o := d[len(d)-1] & 15
	v := binary.BigEndian.Uint32(d[o:o+4]) & 0x7FFFFFFF
	return fmt.Sprintf("%06d", v%1_000_000)
}

// TOTPCheck is the time step a 6-digit code belongs to (now, or one step either side), if not used
// before (after lastStep).
func TOTPCheck(secret, code string, lastStep *int64) (int64, bool) {
	var digits strings.Builder
	for _, c := range code {
		if unicode.IsDigit(c) {
			digits.WriteRune(c)
		}
	}
	got := digits.String()
	if len([]rune(got)) != 6 {
		return 0, false
	}
	t := time.Now().Unix() / 30
	for step := t - 1; step <= t+1; step++ {
		if (lastStep == nil || step > *lastStep) && hmac.Equal([]byte(TOTPAt(secret, step)), []byte(got)) {
			return step, true
		}
	}
	return 0, false
}

// pyQuote is Python's urllib.parse.quote (safe "/"): all but letters, digits, "_.-~/" escaped.
func pyQuote(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~/", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// TOTPURI is the otpauth:// address an authenticator app reads (as a QR code).
func TOTPURI(secret, name string) string {
	issuer := "Everysaid"
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&digits=6&period=30", pyQuote(issuer), pyQuote(name), secret, pyQuote(issuer))
}

func nullStr(v sql.NullString) any {
	if v.Valid {
		return v.String
	}
	return nil
}

func nullInt(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

// pyCut is Python's s[:n] (characters).
func pyCut(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}
