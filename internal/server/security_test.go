package server

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/crypto/scrypt"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/mcp"
	"everysaid/internal/plugins"
)

// atOnce runs f n times at the same moment and counts the times it said yes.
func atOnce(n int, f func() bool) int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	start := make(chan struct{})
	yes := 0
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if f() {
				mu.Lock()
				yes++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	return yes
}

// Whatever lets someone in once (a recovery code, an authenticator's code, a setup link) lets in
// once, also when the same one comes twice at the same moment: what is checked is what is used up,
// in one statement.
func TestOneTimeSecretsAreUsedOnceEvenAtOnce(t *testing.T) {
	c := newServer(t)
	a := c.s.Auth
	uid := a.CreateUser("Once", c.s.Store.Path)

	code := a.NewRecoveryCodes(uid)[0]
	got := atOnce(40, func() bool { return a.UseRecoveryCode(code) == uid })
	must(t, got == 1, "a recovery code let in %d times", got)

	for range 5 {
		token := a.SetupLink(&uid, 60)
		got = atOnce(40, func() bool { ok, _ := a.TakeSetup(token, true); return ok })
		must(t, got == 1, "a setup link was used %d times", got)
	}

	secret := TOTPSecret()
	a.SetPassword(uid, "a long enough password", secret, nil)
	step := time.Now().Unix() / 30
	totp := TOTPAt(secret, step)
	got = atOnce(6, func() bool { return a.CheckPassword("Once", "a long enough password", totp, "10.0.0.1") == uid })
	if time.Now().Unix()/30 == step { // a step that ended meanwhile lets the next code in, rightly
		must(t, got == 1, "an authenticator's code let in %d times", got)
	}
}

// Files of the archive are what other people sent: served from this origin, none of them may be a
// page that runs anything (an HTML or SVG file that loads another file as its script would act as
// the user). Each answer forbids scripts and plugins; what is not a picture, a video, a sound or
// plain text is a download.
func TestArchiveFilesAreNeverActivePages(t *testing.T) {
	c := newServer(t)
	c.login()
	files := map[string]bool{".html": true, ".svg": true, ".js": true, ".pdf": true, ".jpg": false, ".mp4": false, ".txt": false}
	for ext, download := range files {
		data := []byte("<html><script src=\"/api/media/x/original\"></script>" + ext)
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		rel := filepath.Join("media", sha[:2], sha+ext)
		os.MkdirAll(filepath.Join(archive.MediaRoot(), filepath.Dir(rel)), 0o700)
		if err := os.WriteFile(filepath.Join(archive.MediaRoot(), rel), data, 0o600); err != nil {
			t.Fatal(err)
		}
		c.s.Store.MustWrite(func(tx *sql.Tx) {
			db.Exec(tx, "INSERT INTO media (sha256, size, mime, path) VALUES (?, ?, NULL, ?)", sha, len(data), filepath.ToSlash(rel))
		})
		r := c.get("/api/media/" + sha + "/original")
		must(t, r.status == 200, "%s: %d", ext, r.status)
		csp := r.header.Get("Content-Security-Policy")
		must(t, strings.Contains(csp, "sandbox") && strings.Contains(csp, "default-src 'none'") && !strings.Contains(csp, "script-src 'self'"),
			"%s: the policy lets it run: %q", ext, csp)
		disp := strings.HasPrefix(r.header.Get("Content-Disposition"), "attachment")
		must(t, disp == download, "%s: download %v, want %v (%s)", ext, disp, download, r.header.Get("Content-Type"))
	}
	// nor a plugin's log, which holds what came from outside too
	folder := filepath.Join(config.Logs, "plugin-1")
	os.MkdirAll(folder, 0o700)
	os.WriteFile(filepath.Join(folder, "run.log"), []byte("<html>"), 0o600)
	r := c.get("/api/plugins/1/logs/run.log")
	must(t, r.status == 200 && strings.Contains(r.header.Get("Content-Security-Policy"), "sandbox"), "a log: %d %q", r.status,
		r.header.Get("Content-Security-Policy"))
}

// A session ended (signed out, or ended from another device) stops the live events at once: an
// open WebSocket is not a way around the revocation.
func TestEndedSessionClosesItsWebSocket(t *testing.T) {
	c := newServer(t)
	uid := c.s.Auth.CreateUser("Me", c.s.Store.Path)
	token := c.s.Auth.NewSession(uid, "go test", "127.0.0.1", "passkey")
	c.setSession(token)
	ws, _, err := dialEvents(c)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var hello M
	if err := wsjson.Read(ctx, ws, &hello); err != nil {
		t.Fatal(err)
	}
	c.s.Auth.EndSessionHash(h(token))
	c.s.Host.Emit(M{"type": "changed"})
	var ev M
	err = wsjson.Read(ctx, ws, &ev)
	must(t, err != nil && websocket.CloseStatus(err) != -1, "an ended session still gets events: %v %v", ev, err)
}

// A session in use stays signed in: its cookie is renewed as the server renews the session, so
// that a device used every day is not signed out a month after it signed in.
func TestASessionInUseKeepsItsCookie(t *testing.T) {
	c := newServer(t)
	uid := c.s.Auth.CreateUser("Me", c.s.Store.Path)
	token := c.s.Auth.NewSession(uid, "go test", "127.0.0.1", "passkey")
	c.setSession(token)
	db.Exec(c.s.Auth.DB(), "UPDATE session SET last_seen = last_seen - 2 * 86400")
	r := c.get("/api/chats")
	must(t, r.status == 200, "chats: %d", r.status)
	var renewed *http.Cookie
	for _, x := range (&http.Response{Header: r.header}).Cookies() {
		if x.Name == Cookie {
			renewed = x
		}
	}
	must(t, renewed != nil && renewed.Value == token && renewed.MaxAge == SessionDays*86400 && renewed.HttpOnly &&
		renewed.SameSite == http.SameSiteStrictMode, "the cookie is not renewed: %v", renewed)
	again := c.get("/api/chats")
	must(t, again.header.Get("Set-Cookie") == "", "renewed on every request: %q", again.header.Get("Set-Cookie"))
}

// The attempts are counted per address; an IPv6 address is one of the many its owner has (a /64),
// so they are counted per /64, or changing the last digits would be a new budget each time.
func TestAttemptsAreCountedPerNetworkOfIPv6(t *testing.T) {
	c := newServer(t)
	from := func(ip string) resp {
		return c.do("POST", "/api/auth/recover", M{"code": "nope"}, map[string]string{"X-Everysaid": "1", "X-Forwarded-For": ip})
	}
	for i := range 5 {
		r := from(fmt.Sprintf("2001:db8:1:2::%x", i+1))
		must(t, r.status == 403, "attempt %d: %d", i, r.status)
	}
	must(t, from("2001:db8:1:2::ffff").status == 429, "the same /64 had a budget of its own")
	must(t, from("2001:db8:1:3::1").status == 403, "another /64")
	must(t, from("192.0.2.1").status == 403, "IPv4")
	must(t, contains(c.s.opts.TrustedProxies, "::1"), "a proxy on this machine over IPv6 is not trusted as over IPv4")
}

// An MCP token is a lasting key to the archive: the user sees each (when made, when last used) and
// ends any, from the app (with a recent sign-in, as making one) or the command line; it is logged.
func TestMCPTokensAreListedAndRevoked(t *testing.T) {
	c := newServer(t)
	c.login()
	token := c.post("/api/auth/mcp-token", M{"label": "laptop"}).json()["token"].(string)
	list := func() []any { return c.getJSON("/api/auth/account")["mcp_tokens"].([]any) }
	must(t, len(list()) == 1, "listed: %v", list())
	tok := list()[0].(map[string]any)
	must(t, tok["label"] == "laptop" && tok["last_used"] == nil && num(tok["created_at"]) > 0, "a token: %v", tok)
	id := tok["id"].(string)
	mcpCall := func() int {
		return c.do("POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"ping"}`,
			map[string]string{"Authorization": "Bearer " + token, "Accept": "application/json, text/event-stream"}).status
	}
	must(t, mcpCall() != 401, "the token works")
	must(t, list()[0].(map[string]any)["last_used"] != nil, "its use is seen")

	db.Exec(c.s.Auth.DB(), "UPDATE session SET created_at = created_at - 3600")
	r := c.do("DELETE", "/api/auth/mcp-tokens/"+id, nil, H)
	must(t, r.status == 403 && r.code() == "auth.recent_sign_in", "an old session revokes: %d %s", r.status, r.body)
	db.Exec(c.s.Auth.DB(), "UPDATE session SET created_at = ?", time.Now().Unix()) // signed in again
	must(t, c.do("DELETE", "/api/auth/mcp-tokens/0000000000000000", nil, H).status == 404, "no such token")
	r = c.do("DELETE", "/api/auth/mcp-tokens/"+id, nil, H)
	must(t, r.status == 200, "revoke: %d %s", r.status, r.body)
	must(t, mcpCall() == 401, "a revoked token still works")
	must(t, len(list()) == 0, "still listed")
	must(t, strings.Contains(fmt.Sprint(c.getJSON("/api/auth/account")["audit"]), "mcp token revoked"), "not in the log")

	// the command line, on the server's own database
	a, err := OpenAuth("")
	if err != nil {
		t.Fatal(err)
	}
	uid := a.CreateUser("Cli", "x")
	a.NewMCPToken(uid, "desk")
	cliID := a.MCPTokens(uid)[0]["id"].(string)
	a.Close()
	var out strings.Builder
	user := fmt.Sprint(uid)
	must(t, UserMain([]string{"mcp-tokens", "--user", user}, &out) == nil && strings.Contains(out.String(), cliID+"\tdesk\t"),
		"listed: %q", out.String())
	out.Reset()
	must(t, UserMain([]string{"mcp-token", "--user", user, "--revoke", cliID}, &out) == nil, "revoked")
	must(t, UserMain([]string{"mcp-token", "--user", user, "--revoke", cliID}, &out) != nil, "revoked twice")
	out.Reset()
	UserMain([]string{"mcp-tokens", "--user", user}, &out)
	must(t, out.String() == "", "still listed: %q", out.String())
}

// Password hashes are made a few at a time: each takes 32 MB, and a flood of attempts must not take
// the server's memory.
func TestPasswordHashesAreMadeAFewAtATime(t *testing.T) {
	stored := HashPassword("a long enough password")
	var mu sync.Mutex
	now, most := 0, 0
	scryptRaw = func(pw, salt []byte, n, r, p, l int) ([]byte, error) {
		mu.Lock()
		now++
		most = max(most, now)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		now--
		mu.Unlock()
		return make([]byte, l), nil
	}
	defer func() { scryptRaw = scrypt.Key }()
	atOnce(8, func() bool { return VerifyPassword("x", stored) })
	must(t, most <= 2, "%d at once", most)
}

// A connection kept alive with nothing asked is closed after a while.
func TestIdleConnectionsAreClosed(t *testing.T) {
	c := newServer(t)
	idleTimeout = 200 * time.Millisecond
	defer func() { idleTimeout = 2 * time.Minute }()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.s.Serve(ctx, ln)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /api/health HTTP/1.1\r\nHost: %s\r\n\r\n", base)
	rd := bufio.NewReader(conn)
	resp, err := http.ReadResponse(rd, nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = rd.ReadByte()
	must(t, errors.Is(err, io.EOF), "an idle connection is kept: %v", err)
}

// slowReader marks read slowly, counting how often it is asked.
type slowReader struct{}

var slowReads atomic.Int32

func (slowReader) Info() *plugins.Info {
	return &plugins.Info{ID: "test-slow-reader", Name: "Slow reader", Kind: "source", Services: []string{"imessage"},
		CanMarkRead: true}
}

func (slowReader) MarkRead(ctx context.Context, c *plugins.Context, conv plugins.Conversation, until int64) (int, error) {
	slowReads.Add(1)
	time.Sleep(300 * time.Millisecond)
	return 1, nil
}

// A chat read on two devices at once is told to the service once.
func TestAChatReadTwiceAtOnceIsToldOnce(t *testing.T) {
	c := newServer(t)
	plugins.Register(slowReader{})
	if _, err := plugins.Create(c.s.Store, "test-slow-reader", "Slow", M{}); err != nil {
		t.Fatal(err)
	}
	chat := core.ChatOfConversation(c.s.Store, db.Int(c.s.Store.Read(),
		"SELECT c.id FROM conversation c JOIN service s ON s.id = c.service_id WHERE s.name = 'imessage'"))
	slowReads.Store(0)
	until := time.Now().UnixMilli()
	atOnce(30, func() bool { return c.s.Host.MarkRead(context.Background(), chat, until) == nil })
	must(t, slowReads.Load() == 1, "told %d times", slowReads.Load())
}

// Mentions whose numbers would wrap around are refused as any wrong mention is.
func TestMentionsOutOfRangeAreRefused(t *testing.T) {
	c := newServer(t)
	c.login()
	chat := items(c.getJSON("/api/chats"))[0]["id"].(string)
	r := c.post("/api/chats/"+chat+"/send", M{"text": "@x hello", "mentions": []M{{"start": int64(9223372036854775000), "length": 1000, "address_id": 1}}})
	must(t, r.status == 400 && r.code() == "failed", "mentions: %d %s", r.status, r.body)
}

// The server posts to a subscription's endpoint: only to a push service on the internet, never to
// this machine or its network, also when a name resolves there only when the push is sent.
func TestPushGoesOnlyToTheInternet(t *testing.T) {
	c := newServer(t)
	c.login()
	for _, e := range []string{"https://127.0.0.1/x", "https://localhost/x", "https://10.1.2.3/x", "https://192.168.0.10/x",
		"https://[::1]/x", "https://[fd00::1]/x", "https://169.254.169.254/x", "https://100.64.1.1/x", "http://203.0.113.5/x"} {
		r := c.post("/api/push/subscribe", M{"endpoint": e, "keys": M{}})
		must(t, r.status == 400 && r.code() == "push.bad_subscription", "%s: %d", e, r.status)
	}
	must(t, c.post("/api/push/subscribe", M{"endpoint": "https://203.0.113.5/x", "keys": M{}}).status == 200, "on the internet")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			conn.Close()
		}
	}()
	defer ln.Close()
	browser, _ := ecdh.P256().GenerateKey(rand.Reader)
	b64 := base64.RawURLEncoding
	keys := fmt.Sprintf(`{"p256dh":%q,"auth":%q}`, b64.EncodeToString(browser.PublicKey().Bytes()), b64.EncodeToString(randomBytes(16)))
	c.s.Push.Send([]subscription{{"https://" + ln.Addr().String() + "/x", keys}}, []map[string]any{{"title": "t"}})
	must(t, accepted.Load() == 0, "the push went to this machine")
}

// What is deleted is in the audit log: a source, a label, a picture chosen for removal.
func TestDeletionsAreLogged(t *testing.T) {
	c := newServer(t)
	c.login()
	lib := t.TempDir()
	iid := num(c.post("/api/plugins", M{"plugin": "test-folder", "label": "Gone", "settings": M{"path": lib}}).json()["id"])
	must(t, c.do("DELETE", fmt.Sprintf("/api/plugins/%d", iid), nil, H).status == 200, "remove")
	lid := num(items(c.getJSON("/api/labels"))[0]["id"])
	must(t, c.do("DELETE", fmt.Sprintf("/api/labels/%d", lid), nil, H).status == 200, "remove a label")
	sha := items(c.getJSON("/api/media?kind=image"))[0]["sha256"].(string)
	must(t, c.post("/api/media/"+sha+"/decision", M{"decision": "remove"}).status == 200, "decision")
	audit := fmt.Sprint(c.getJSON("/api/auth/account")["audit"])
	for _, e := range []string{"plugin removed", "test-folder: Gone", "label removed", "media to remove"} {
		must(t, strings.Contains(audit, e), "%q not in the log: %s", e, audit)
	}
}

// fetcher brings files of iMessage (live or not, as told), counting how often it is asked.
type fetcher struct {
	id   string
	live bool
	path string
	n    *atomic.Int32
}

func (f fetcher) Info() *plugins.Info {
	i := &plugins.Info{ID: f.id, Name: f.id, Kind: "source", Services: []string{"imessage"}}
	if f.live {
		i.Modes = []string{"import", "live"}
	}
	return i
}

func (f fetcher) FetchMedia(ctx context.Context, c *plugins.Context, messageID int64) (string, error) {
	f.n.Add(1)
	return f.path, nil
}

func (f fetcher) Live(ctx context.Context, c *plugins.Context) error { <-ctx.Done(); return nil }

// A message whose file the archive never had: the assistant (MCP) gets it from a source that can
// bring it now, an enabled one that reads its service, a live one only while connected.
func TestMediaIsFetchedThroughASourceAtWork(t *testing.T) {
	c := newServer(t)
	file := filepath.Join(t.TempDir(), "fetched.jpg")
	os.WriteFile(file, tinyJPEG(), 0o600)
	var asked, liveAsked atomic.Int32
	plugins.Register(fetcher{"test-fetcher", false, file, &asked})
	plugins.Register(fetcher{"test-live-fetcher", true, "/elsewhere", &liveAsked})
	mid := db.Int(c.s.Store.Read(), "SELECT m.id FROM message m JOIN service s ON s.id = m.service_id WHERE s.name = 'imessage' LIMIT 1")
	_, err := c.s.Host.FetchMedia(context.Background(), c.s.Store, mid)
	must(t, errors.Is(err, mcp.ErrNoFetch), "with no fetcher: %v", err)

	plugins.Create(c.s.Store, "test-live-fetcher", "Live fetcher", M{})
	iid, _ := plugins.Create(c.s.Store, "test-fetcher", "Fetcher", M{})
	uid := c.s.Auth.CreateUser("Me", c.s.Store.Path)
	token := c.s.Auth.NewMCPToken(uid, "test")
	r := c.do("POST", "/mcp", fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"download_media","arguments":{"message_id":%d}}}`, mid),
		map[string]string{"Authorization": "Bearer " + token, "Accept": "application/json, text/event-stream"})
	must(t, r.status == 200 && strings.Contains(string(r.body), `\"from\":\"service\"`), "download_media: %d %s", r.status, r.body)
	must(t, asked.Load() == 1 && liveAsked.Load() == 0, "asked %d, the live one (not connected) %d", asked.Load(), liveAsked.Load())

	other := db.Int(c.s.Store.Read(), "SELECT m.id FROM message m JOIN service s ON s.id = m.service_id WHERE s.name = 'whatsapp' LIMIT 1")
	_, err = c.s.Host.FetchMedia(context.Background(), c.s.Store, other)
	must(t, errors.Is(err, mcp.ErrNoFetch) && asked.Load() == 1, "another service's message: %v", err)
	off := false
	plugins.Update(c.s.Store, iid, nil, nil, &off, false)
	_, err = c.s.Host.FetchMedia(context.Background(), c.s.Store, mid)
	must(t, errors.Is(err, mcp.ErrNoFetch) && asked.Load() == 1, "a disabled source: %v", err)
}
