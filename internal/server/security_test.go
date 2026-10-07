package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
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
