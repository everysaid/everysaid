package server

// The server's database as the Python left it keeps working: passkeys (made by an authenticator of
// the test's own, as a browser would), sessions, passwords and authenticator codes, the VAPID key
// and the push messages sent with it.

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/golang-jwt/jwt/v5"

	"everysaid/internal/config"
	"everysaid/internal/db"
)

// authenticator is a passkey of the test's own: an ES256 key, as a phone or a password manager holds one.
type authenticator struct {
	key    *ecdsa.PrivateKey
	id     []byte
	handle []byte
	count  uint32
	be     bool // backed up (a synced passkey)
}

func newAuthenticator(be bool) *authenticator {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return &authenticator{key: k, id: randomBytes(16), be: be}
}

var b64 = base64.RawURLEncoding

func (a *authenticator) cose() []byte {
	pub, _ := a.key.PublicKey.ECDH()
	raw := pub.Bytes() // 0x04 | x | y
	b, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: raw[1:33], -3: raw[33:]})
	return b
}

func (a *authenticator) flags(attested bool) byte {
	f := byte(0x01 | 0x04) // user present, verified
	if a.be {
		f |= 0x08 | 0x10
	}
	if attested {
		f |= 0x40
	}
	return f
}

func (a *authenticator) authData(rpID string, attested bool) []byte {
	h := sha256.Sum256([]byte(rpID))
	out := append([]byte{}, h[:]...)
	out = append(out, a.flags(attested))
	out = binary.BigEndian.AppendUint32(out, a.count)
	if attested {
		out = append(out, make([]byte, 16)...) // aaguid
		out = binary.BigEndian.AppendUint16(out, uint16(len(a.id)))
		out = append(out, a.id...)
		out = append(out, a.cose()...)
	}
	return out
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

// create answers registration options as navigator.credentials.create would.
func (a *authenticator) create(opts map[string]any, origin string) M {
	a.handle, _ = b64.DecodeString(opts["user"].(map[string]any)["id"].(string))
	rp := opts["rp"].(map[string]any)["id"].(string)
	att, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(rp, true)})
	return M{"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": M{"clientDataJSON": b64.EncodeToString(clientData("webauthn.create", opts["challenge"].(string), origin)),
			"attestationObject": b64.EncodeToString(att), "transports": []string{"internal", "hybrid"}},
		"clientExtensionResults": M{}}
}

// get answers login options as navigator.credentials.get would.
func (a *authenticator) get(opts map[string]any, origin string) M {
	a.count++
	ad := a.authData(opts["rpId"].(string), false)
	cd := clientData("webauthn.get", opts["challenge"].(string), origin)
	h := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	return M{"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": M{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(a.handle)},
		"clientExtensionResults": M{}}
}

func (c *client) passkeyLogin(a *authenticator) resp {
	o := c.post("/api/auth/login/options", nil).json()
	return c.post("/api/auth/login/verify", M{"nonce": o["nonce"], "credential": a.get(o["options"].(map[string]any), "http://"+base)})
}

func TestPasskeys(t *testing.T) {
	c := newServer(t)
	token := c.s.Auth.SetupLink(nil, 60)
	o := c.post("/api/auth/register/options", M{"token": token, "name": "Me"}).json()
	a := newAuthenticator(true)
	cred := a.create(o["options"].(map[string]any), "http://"+base)
	r := c.post("/api/auth/register/verify", M{"nonce": o["nonce"], "credential": cred, "label": "Phone"})
	must(t, r.status == 200 && len(r.json()["recovery_codes"].([]any)) == 10, "register: %d %s", r.status, r.body)
	must(t, c.getJSON("/api/auth/account")["passkeys"].([]any)[0].(map[string]any)["name"] == "Phone", "the passkey")
	must(t, c.post("/api/auth/register/verify", M{"nonce": o["nonce"], "credential": cred}).code() == "auth.challenge_expired", "once")
	ok, _ := c.s.Auth.TakeSetup(token, false)
	must(t, !ok, "the setup link is used up")

	c.clearCookies()
	r = c.passkeyLogin(a)
	must(t, r.status == 200, "login: %d %s", r.status, r.body)
	must(t, c.get("/api/chats").status == 200, "signed in")
	used := db.Int(c.s.Auth.DB(), "SELECT sign_count FROM passkey")
	must(t, used == int64(a.count), "the count kept: %d", used)

	// a count that does not grow: a copy of the key elsewhere
	c.clearCookies()
	a.count--
	r = c.passkeyLogin(a)
	must(t, r.status == 403 && r.code() == "auth.passkey_rejected", "a cloned authenticator: %d %s", r.status, r.body)
	// another site's page: refused
	o = c.post("/api/auth/login/options", nil).json()
	a.count += 5
	r = c.post("/api/auth/login/verify", M{"nonce": o["nonce"], "credential": a.get(o["options"].(map[string]any), "https://evil.example")})
	must(t, r.status == 403, "another origin: %d", r.status)
	// one the server does not know
	r = c.passkeyLogin(newAuthenticator(false))
	must(t, r.code() == "auth.unknown_passkey", "unknown: %s", r.body)
}

// A passkey stored as the Python stored it (raw id, COSE key, count; no flags) signs in.
func TestAPasskeyMadeByThePythonSignsIn(t *testing.T) {
	c := newServer(t)
	for _, be := range []bool{true, false} {
		a := newAuthenticator(be)
		a.handle = randomBytes(32)
		a.count = 7
		uid := c.s.Auth.CreateUser(fmt.Sprint("Py", be), c.s.Store.Path)
		c.s.Auth.setHandle(uid, a.handle)
		db.Exec(c.s.Auth.DB(), "INSERT INTO passkey (id, user_id, public_key, sign_count, transports, name, created_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?)", a.id, uid, a.cose(), 7, `["internal"]`, "Firefox", 1)
		c.clearCookies()
		r := c.passkeyLogin(a)
		must(t, r.status == 200, "backed up %v: %d %s", be, r.status, r.body)
		must(t, c.getJSON("/api/auth/status")["user"].(map[string]any)["id"] == float64(uid), "as the user")
	}
}

func TestPythonServerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.db")
	// as the first Python server made it: without the password's columns
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(authSchema); err != nil {
		t.Fatal(err)
	}
	token := "py-session-token-0123456789"
	db.Exec(d, "INSERT INTO user (id, name, handle, archive, created_at) VALUES (1, 'Me', x'01', '/a.db', 1)")
	db.Exec(d, "INSERT INTO session VALUES (?, 1, ?, ?, 'ua', '127.0.0.1', 'passkey')", h(token), time.Now().Unix(), time.Now().Unix())
	db.Exec(d, "INSERT INTO kv VALUES (?, ?)", "setup:"+h("py-link"), fmt.Sprintf("[%v, 1]", float64(time.Now().Unix())+600.25))
	d.Close()
	a, err := OpenAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, col := range []string{"password", "totp", "totp_step"} {
		must(t, db.Exists(a.DB(), "SELECT 1 FROM pragma_table_info('user') WHERE name = ?", col), "column %s", col)
	}
	uid, _, ok := a.Session(token)
	must(t, ok && uid == 1, "the Python's session")
	ok, u := a.TakeSetup("py-link", false)
	must(t, ok && u != nil && *u == 1 && a.SetupLeft("py-link") > 500, "the Python's setup link")
	// the Python's password hash and codes
	db.Exec(a.DB(), "UPDATE user SET password = ?, totp = 'JBSWY3DPEHPK3PXP' WHERE id = 1",
		"scrypt$32768$8$1$4a4c47c2c228db4ad03e8c447a169714$46204273f9bcf98ba9ed21668aa2d98b4ffaa8b3d2c1af8c97b22be80bddfab5")
	must(t, VerifyPassword("a long enough password", db.Str(a.DB(), "SELECT password FROM user")), "the Python's hash")
	must(t, !VerifyPassword("another password", db.Str(a.DB(), "SELECT password FROM user")), "not another")
	must(t, TOTPAt("JBSWY3DPEHPK3PXP", 56789012) == "470210" && TOTPAt("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", 1) == "287082", "codes")
	must(t, TOTPURI("ABC", "Μαρία K & co/x") == "otpauth://totp/Everysaid:%CE%9C%CE%B1%CF%81%CE%AF%CE%B1%20K%20%26%20co/x?secret=ABC&issuer=Everysaid&digits=6&period=30",
		"uri: %s", TOTPURI("ABC", "Μαρία K & co/x"))
	// a hash of this server is one the Python reads: the same form
	parts := strings.Split(HashPassword("x"), "$")
	must(t, len(parts) == 6 && parts[0] == "scrypt" && parts[1] == "32768" && len(parts[4]) == 32 && len(parts[5]) == 64, "form: %v", parts)
}

// The VAPID key the Python made (a PEM in the keyring) is read as it is: the same public key, so the
// browsers' subscriptions stay valid; push messages go out signed with it, and a subscription the
// push service says is gone is forgotten.
func TestPushWithThePythonsKey(t *testing.T) {
	const pemKey = "-----BEGIN PRIVATE KEY-----\nREDACTED\n-----END PRIVATE KEY-----\n"
	const pub = "BFOCeKnmSry-ab-SG1psPP2KXwT8bX_74BIwEWYNdrY0o4QFrpjYouuvol6tNav7Nf5ClwIfDr_2rz2aRv03eGk"
	if _, err := config.SaveSecret("vapid-private", pemKey); err != nil {
		t.Fatal(err)
	}
	defer config.DeleteSecret("vapid-private")
	c := newServer(t)
	uid := c.login()
	must(t, c.getJSON("/api/push/key")["key"] == pub, "the same public key")

	var mu sync.Mutex
	var got []*http.Request
	var bodies [][]byte
	status := 201
	ps := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got, bodies = append(got, r), append(bodies, b)
		st := status
		mu.Unlock()
		w.WriteHeader(st)
	}))
	defer ps.Close()
	c.s.Push.httpClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	browser, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := M{"endpoint": ps.URL + "/push/1", "keys": M{"p256dh": b64.EncodeToString(browser.PublicKey().Bytes()),
		"auth": b64.EncodeToString(randomBytes(16))}}
	must(t, c.post("/api/push/subscribe", M{"endpoint": "http://plain/1"}).code() == "push.bad_subscription", "https only")
	must(t, c.post("/api/push/subscribe", sub).status == 200, "subscribe")
	r := c.post("/api/push/test", nil)
	must(t, r.status == 200 && num(r.json()["devices"]) == 1, "test: %s", r.body)
	mu.Lock()
	must(t, len(got) == 1, "sent: %d", len(got))
	req := got[0]
	mu.Unlock()
	must(t, req.Header.Get("Content-Encoding") == "aes128gcm" && req.Header.Get("TTL") == "3600", "headers: %v", req.Header)
	auth := req.Header.Get("Authorization")
	must(t, strings.HasPrefix(auth, "vapid t=") && strings.HasSuffix(auth, ", k="+pub), "authorization: %s", auth)
	tok := strings.TrimSuffix(strings.TrimPrefix(auth, "vapid t="), ", k="+pub)
	raw, _ := b64.DecodeString(pub)
	x, y := elliptic.Unmarshal(elliptic.P256(), raw)
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(tok, claims, func(*jwt.Token) (any, error) {
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
	})
	must(t, err == nil && claims["sub"] == "mailto:everysaid@localhost" && claims["aud"] == ps.URL, "the token: %v %v", err, claims)

	// gone: forgotten
	mu.Lock()
	status = 410
	mu.Unlock()
	c.post("/api/push/test", nil)
	must(t, db.Int(c.s.Auth.DB(), "SELECT count(*) FROM push_subscription WHERE user_id = ?", uid) == 0, "gone")
}

// An origin by IP address cannot have passkeys: the server still runs, with the password's way in.
func TestAnOriginByAddressStillServes(t *testing.T) {
	dir := t.TempDir()
	b, _ := os.ReadFile(pristine)
	os.WriteFile(filepath.Join(dir, "a.db"), b, 0o600)
	s, err := New(Options{Archive: filepath.Join(dir, "a.db"), AuthDB: filepath.Join(dir, "s.db"), Origin: "http://192.168.0.5:8520",
		ExtraOrigins: []string{}, Out: io.Discard, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &client{t: t, s: s, ts: httptestServer(s)}
	defer c.ts.Close()
	c.hc, c.jar = newHTTPClient()
	must(t, c.getJSON("/api/auth/status")["rp_id"] == "192.168.0.5", "status")
	token := s.Auth.SetupLink(nil, 60)
	r := c.post("/api/auth/register/options", M{"token": token})
	must(t, r.status == 400 && r.code() == "auth.passkey_failed", "passkeys: %d %s", r.status, r.body)
	must(t, c.post("/api/auth/password/options", M{"token": token, "name": "Me"}).status == 200, "the password's way")
}
