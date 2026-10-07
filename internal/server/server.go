// Package server is `everysaid serve` as a library: the API over the core, the WebSocket of live
// events, the PWA's files, the plugins' host, passkeys and push. New gives an http.Handler, so that
// any shell (the command line, a desktop app) can embed it; Run listens on its own.
//
// Ports everysaid/server/__init__.py and app.py. Guards on every request:
//   - Host must name this server (its origin's host, or localhost): no DNS rebinding.
//   - Everything under /api needs a session (the cookie), except the login steps.
//   - A change (any method but GET/HEAD/OPTIONS) needs the header `X-Everysaid: 1`, which another
//     site's page cannot send without a CORS preflight this server never grants; and its Origin, when
//     sent, must be this server's. With SameSite=Strict cookies this closes CSRF.
//   - Responses carry a strict Content-Security-Policy (only this server's own scripts), no framing,
//     no referrer, no sniffing; HSTS over HTTPS.
//   - Login attempts are limited per address.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/demo"
	"everysaid/internal/i18n"
	"everysaid/internal/mcp"
	"everysaid/internal/webui"
)

// MaxUpload is the largest file sent in a chat: what the services take (WhatsApp about 96 MB,
// Telegram 2 GB).
const MaxUpload = 100_000_000

// open are the routes under /api that need no session.
var open = map[string]bool{"/api/auth/status": true, "/api/auth/login/options": true, "/api/auth/login/verify": true,
	"/api/auth/register/options": true, "/api/auth/register/verify": true, "/api/auth/recover": true, "/api/health": true,
	"/api/auth/password/options": true, "/api/auth/password/set": true, "/api/auth/password/login": true,
	"/api/events": true} // the WebSocket checks its own (and answers as a WebSocket refusal)

const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"media-src 'self' blob:; font-src 'self' data:; connect-src 'self' %s; worker-src 'self'; " +
	"manifest-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Options are what a server is made of; every field has a default.
type Options struct {
	Archive string // the archive (default: the user's, <data>/archive.db)
	AuthDB  string // the users' database (default: <data>/server.db)
	Host    string // where Run listens (default: config [server] host)
	Port    int    // (default: config [server] port)
	// Origin is the address the devices use (default: config [server] origin); passkeys are tied to
	// its host. ExtraOrigins are more (default: EVERYSAID_EXTRA_ORIGINS, comma-separated).
	Origin       string
	ExtraOrigins []string
	// WebDir serves the interface from a folder (web/dist while developing) instead of the build
	// embedded in the binary.
	WebDir string
	// Demo adds /api/demo/incoming (default: EVERYSAID_DEMO set): a message arrives in a chat, from
	// the other side, through DemoIncoming (default: the demo's writer, demo.Message).
	Demo         bool
	DemoIncoming func(h *Host, conversationID int64, text string) (int64, error)
	// Out is where the setup link of a new server is printed (default: stdout).
	Out io.Writer
	// Logger: what goes wrong (default: the console and <state>/logs/server.log).
	Logger *slog.Logger
	// TrustedProxies: the addresses whose X-Forwarded-For and X-Forwarded-Proto are believed
	// (default: this machine, 127.0.0.1 as uvicorn's forwarded_allow_ips, and ::1: a proxy here may
	// come over IPv6, and then every client would be one address, sharing one budget of attempts).
	TrustedProxies []string
}

// Server is one server: its handler, the archive, the users and the plugins' host.
type Server struct {
	opts    Options
	Auth    *Auth
	Store   *core.Store
	Host    *Host
	Push    *Push
	log     *slog.Logger
	mux     *http.ServeMux
	handler http.Handler
	web     fs.FS
	wa      *webauthn.WebAuthn // nil when the origin's host cannot be a relying party (waErr says why)
	waErr   error
	rp      string

	origins  []string        // the origins passkeys accept
	allowed  map[string]bool // the origins a change or the WebSocket may come from
	hosts    map[string]bool // the Host headers this server answers
	https    bool
	cspValue string
	logClose func()
}

// New makes a server; nothing runs until Start (or Run).
func New(o Options) (*Server, error) {
	if o.Archive == "" {
		o.Archive = archive.DB()
	}
	if o.AuthDB == "" {
		o.AuthDB = AuthDB()
	}
	if o.Host == "" {
		o.Host = config.ServerHost
	}
	if o.Port == 0 {
		o.Port = config.ServerPort
	}
	if o.Origin == "" {
		o.Origin = config.ServerOrigin
	}
	o.Origin = strings.TrimRight(o.Origin, "/")
	if o.ExtraOrigins == nil {
		for _, x := range strings.Split(os.Getenv("EVERYSAID_EXTRA_ORIGINS"), ",") {
			if x = strings.TrimRight(strings.TrimSpace(x), "/"); x != "" {
				o.ExtraOrigins = append(o.ExtraOrigins, x)
			}
		}
	}
	if !o.Demo && os.Getenv("EVERYSAID_DEMO") != "" {
		o.Demo = true
	}
	if o.Demo && o.DemoIncoming == nil {
		o.DemoIncoming = func(h *Host, conv int64, text string) (int64, error) {
			return demo.Message(h, conv, text, false, "", nil, nil)
		}
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.TrustedProxies == nil {
		o.TrustedProxies = []string{"127.0.0.1", "::1"}
	}
	s := &Server{opts: o}
	s.log = o.Logger
	if s.log == nil {
		s.log, s.logClose = defaultLogger()
	}
	abs, err := filepath.Abs(o.Archive)
	if err != nil {
		return nil, err
	}
	if s.Store, err = core.Open(abs); err != nil {
		return nil, err
	}
	if s.Auth, err = OpenAuth(o.AuthDB); err != nil {
		s.Store.Close()
		return nil, err
	}
	s.Push = NewPush(s.Auth, s.log)
	s.Host = NewHost(s.Store, s.Push, s.log)

	s.origins = append([]string{o.Origin}, o.ExtraOrigins...)
	s.allowed = map[string]bool{}
	s.hosts = map[string]bool{}
	for _, x := range s.origins {
		s.allowed[x] = true
		if u, err := url.Parse(x); err == nil {
			s.hosts[u.Host] = true
		}
	}
	// the local addresses: of the configured port (as the Python's), and of the one listened on
	for _, port := range []int{config.ServerPort, o.Port} {
		for _, hst := range []string{"localhost", "127.0.0.1"} {
			s.hosts[fmt.Sprintf("%s:%d", hst, port)] = true
			s.allowed[fmt.Sprintf("http://%s:%d", hst, port)] = true
		}
	}
	s.https = strings.HasPrefix(o.Origin, "https://")
	var ws []string
	for x := range s.allowed {
		ws = append(ws, "ws"+strings.TrimPrefix(x, "http"))
	}
	sort.Strings(ws)
	s.cspValue = fmt.Sprintf(csp, strings.Join(ws, " "))

	if u, err := url.Parse(o.Origin); err == nil {
		s.rp = u.Hostname()
	}
	// an origin by IP address or a name of one label cannot have passkeys (go-webauthn refuses it as
	// a relying party): the server still runs, with the password and its code as the way in
	if s.wa, s.waErr = newWebAuthn(s.rp, s.origins); s.waErr != nil {
		s.wa = nil
		s.log.Warn("passkeys are not possible with this origin: set [server] origin to a domain name", "origin", o.Origin, "error", s.waErr)
	}

	if o.WebDir != "" {
		s.web = os.DirFS(o.WebDir)
	} else {
		s.web = webui.FS()
	}
	s.mux = http.NewServeMux()
	s.routes()
	s.handler = s.guard(s.mux)
	return s, nil
}

// ServeHTTP answers a request (the server is an http.Handler).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Mux is the router itself (tests walk its routes).
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Origin is the address the devices use.
func (s *Server) Origin() string { return s.opts.Origin }

// Start starts what runs beside the requests (live connections, events) until ctx ends; on a new
// server it prints the one-time setup link.
func (s *Server) Start(ctx context.Context) {
	s.Host.Start(ctx)
	if len(s.Auth.Users()) == 0 {
		token := s.Auth.SetupLink(nil, 60)
		fmt.Fprintf(s.opts.Out, "\n  %s\n  %s\n\n", i18n.Say("First setup: open {url}", M{"url": s.opts.Origin + "/setup#" + token}),
			i18n.Say("(valid for one hour)", nil))
	}
}

// Run listens on Host:Port and serves until ctx ends, then stops the live connections.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(s.opts.Host, fmt.Sprint(s.opts.Port)))
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// idleTimeout: a kept-alive connection with nothing asked of it is closed after so long (the
// WebSocket, taken over, is not one of them).
var idleTimeout = 2 * time.Minute

// Serve is Run on a listener of the caller's.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hs := &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: idleTimeout,
		ErrorLog:    slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		BaseContext: func(net.Listener) context.Context { return ctx }}
	s.Start(ctx)
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-errc:
		cancel()
		s.Host.Wait(10 * time.Second)
		return err
	}
	shut, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	hs.Shutdown(shut)
	s.Host.Wait(10 * time.Second)
	return nil
}

// Close closes the databases (after the server stopped).
func (s *Server) Close() {
	if s.Auth != nil {
		s.Auth.Close()
	}
	if s.Store != nil {
		s.Store.Close()
	}
	if s.logClose != nil {
		s.logClose()
	}
}

// --- the guard -----------------------------------------------------------------------------------

type authKey struct{}

type authInfo struct {
	uid     int64
	session string
}

func detail(code string) map[string]any {
	return map[string]any{"detail": map[string]any{"code": code, "params": map[string]any{}}}
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.trustProxy(r)
		hd := w.Header()
		hd.Set("Content-Security-Policy", s.cspValue)
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		if s.https {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if !s.hosts[r.Host] {
			writeJSON(w, 421, detail("auth.unknown_host"))
			return
		}
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/") {
			w = &noStore{ResponseWriter: w}
			if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
				if r.Header.Get("X-Everysaid") != "1" {
					writeJSON(w, 403, detail("auth.header_missing"))
					return
				}
				if o := r.Header.Get("Origin"); o != "" && !s.allowed[o] {
					writeJSON(w, 403, detail("auth.foreign_origin"))
					return
				}
			}
			if !open[p] {
				token := cookieOf(r)
				uid, hs, renewed, ok := s.Auth.session(token)
				if !ok {
					writeJSON(w, 401, detail("auth.sign_in_needed"))
					return
				}
				if renewed { // in use: the cookie lasts as long as the session does
					s.setCookie(w, token)
				}
				r = r.WithContext(context.WithValue(r.Context(), authKey{}, &authInfo{uid, hs}))
			}
		}
		s.route(w, r, next)
	})
}

// route answers through the router; what it has no route for is answered in JSON, as FastAPI did
// (an address of the API with another method: 405).
func (s *Server) route(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if _, pattern := s.mux.Handler(r); pattern == "" {
		rec := &statusOnly{header: http.Header{}}
		next.ServeHTTP(rec, r)
		switch rec.status {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			w.Header().Set("Location", rec.header.Get("Location"))
			w.WriteHeader(rec.status)
		case http.StatusMethodNotAllowed:
			if a := rec.header.Get("Allow"); a != "" {
				w.Header().Set("Allow", a)
			}
			writeJSON(w, 405, map[string]any{"detail": "Method Not Allowed"})
		default:
			writeJSON(w, 404, map[string]any{"detail": "Not Found"})
		}
		return
	}
	next.ServeHTTP(w, r)
}

type statusOnly struct {
	header http.Header
	status int
}

func (s *statusOnly) Header() http.Header         { return s.header }
func (s *statusOnly) Write(b []byte) (int, error) { return len(b), nil }
func (s *statusOnly) WriteHeader(code int)        { s.status = code }

// noStore: the API's answers are not kept, unless they say otherwise.
type noStore struct {
	http.ResponseWriter
	wrote bool
}

func (n *noStore) WriteHeader(code int) {
	if !n.wrote {
		n.wrote = true
		if n.Header().Get("Cache-Control") == "" {
			n.Header().Set("Cache-Control", "no-store")
		}
	}
	n.ResponseWriter.WriteHeader(code)
}

func (n *noStore) Write(b []byte) (int, error) {
	if !n.wrote {
		n.WriteHeader(200)
	}
	return n.ResponseWriter.Write(b)
}

func (n *noStore) Unwrap() http.ResponseWriter { return n.ResponseWriter }

func (n *noStore) Flush() {
	if f, ok := n.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func cookieOf(r *http.Request) string {
	if c, err := r.Cookie(Cookie); err == nil {
		return c.Value
	}
	return ""
}

// trustProxy takes the client's address and scheme from a trusted proxy's headers (uvicorn's
// proxy_headers): the first address from the right that is not a trusted proxy.
func (s *Server) trustProxy(r *http.Request) {
	host, port, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !contains(s.opts.TrustedProxies, host) {
		return
	}
	if proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); proto != "" {
		r.URL.Scheme = strings.ToLower(proto)
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		hops := strings.Split(xff, ",")
		client := strings.TrimSpace(hops[0])
		for i := len(hops) - 1; i >= 0; i-- {
			hop := strings.TrimSpace(hops[i])
			if !contains(s.opts.TrustedProxies, hop) {
				client = hop
				break
			}
		}
		if client != "" {
			r.RemoteAddr = net.JoinHostPort(client, port)
		}
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: Cookie, Value: token, MaxAge: SessionDays * 86400, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: s.https, Path: "/"})
}

// --- the PWA -------------------------------------------------------------------------------------

func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if strings.HasPrefix(p, "api/") {
		s.writeError(w, r, notFound(""))
		return
	}
	if s.web == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(503)
		w.Write(webui.Placeholder)
		return
	}
	name := path.Clean("/" + p)[1:]
	if name != "" && fs.ValidPath(name) {
		if st, err := fs.Stat(s.web, name); err == nil && !st.IsDir() {
			cache := "no-cache"
			if strings.Contains("/"+name, "/assets/") {
				cache = "public, max-age=31536000, immutable"
			}
			s.serveFS(w, r, name, cache)
			return
		}
	}
	s.serveFS(w, r, "index.html", "no-cache")
}

func (s *Server) serveFS(w http.ResponseWriter, r *http.Request, name, cache string) {
	f, err := s.web.Open(name)
	if err != nil {
		s.writeError(w, r, notFound(""))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", cache)
	if t := guessType(name); t != "" {
		if strings.HasPrefix(t, "text/") && !strings.Contains(t, "charset") {
			t += "; charset=utf-8"
		}
		w.Header().Set("Content-Type", t)
	}
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, st.ModTime(), rs)
		return
	}
	b, err := io.ReadAll(f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	http.ServeContent(w, r, name, st.ModTime(), strings.NewReader(string(b)))
}

// serveFile answers with a file on disk (ranges too: a video seeks), of a type if given.
func serveFile(w http.ResponseWriter, r *http.Request, p, typ, cache string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return errors.New("a folder")
	}
	if typ == "" {
		typ = guessType(p)
	}
	if typ != "" {
		w.Header().Set("Content-Type", typ)
	}
	inert(w.Header(), typ)
	if cache != "" {
		w.Header().Set("Cache-Control", cache)
	}
	http.ServeContent(w, r, filepath.Base(p), st.ModTime(), f)
	return nil
}

// fileCSP is the policy of a file served from the archive or the server's folders: what other people
// sent comes from this origin, and must never be a page that runs anything (an HTML or SVG file
// loading another file as its script would act as the user). No scripts, no plugins, an origin of
// its own (sandbox).
const fileCSP = "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; " +
	"frame-ancestors 'none'; sandbox"

// inert makes a file's answer harmless as a page: fileCSP, and a download unless it is a picture,
// a video, a sound or plain text (shown by the browser as such, never as a document of its own).
func inert(h http.Header, typ string) {
	h.Set("Content-Security-Policy", fileCSP)
	t := strings.ToLower(strings.TrimSpace(strings.Split(typ, ";")[0]))
	shown := t != "image/svg+xml" && (strings.HasPrefix(t, "image/") || strings.HasPrefix(t, "video/") ||
		strings.HasPrefix(t, "audio/") || t == "text/plain")
	if !shown {
		h.Set("Content-Disposition", "attachment")
	}
}

// mcpEnv is the archive of an MCP token's user (this server's, the only one it serves).
func (s *Server) mcpEnv(token string) (mcp.Env, bool) {
	uid := s.Auth.MCPUser(token)
	if uid == 0 {
		return mcp.Env{}, false
	}
	u := s.Auth.User(uid)
	if u == nil || u.Archive != s.Store.Path {
		return mcp.Env{}, false
	}
	return mcp.Env{Store: s.Store, Library: librarian{s.Host}, Fetcher: s.Host, Inline: true}, true
}

// librarian is the host's ToLibrary into the default library, as the MCP tools want it.
type librarian struct{ h *Host }

func (l librarian) ToLibrary(sha256 string, dateMs *int64) (core.M, error) {
	return l.h.ToLibrary(sha256, 0, dateMs) // the tool records the decision itself
}
