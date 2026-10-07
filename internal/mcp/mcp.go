// Package mcp ports everysaid/mcp_server.py: the archive for an assistant, as an MCP server.
//
//	everysaid mcp [--db PATH]
//
// Every tool is a call into the core (the same answers the app gives). Times are given in the
// user's time zone (config `[owner] timezone`), as ISO text. Reading is free; the changes an
// assistant can make are a person's name and note, and storing a file in the photo library (media
// on demand: found with find_media, checked against the library first), each only with the user's
// approval.
//
// Two ways in: Main, over stdio, for an assistant on this computer; Handler, over streamable HTTP,
// for the server to mount (token-protected), where download_media may also ask a live source for a
// file the archive does not hold.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/i18n"
)

// MediaFetcher brings a message's file from the service it came by, where a source at work can (a
// live WhatsApp connection downloads what it never did): the file's path on this machine. The
// server wires it; the stdio mode has none.
type MediaFetcher interface {
	FetchMedia(ctx context.Context, s *core.Store, messageID int64) (path string, err error)
}

// ErrNoFetch is a MediaFetcher's answer when no source at work can bring the file.
var ErrNoFetch = errors.New("no source at work can bring this file")

// Librarian stores a file in the default photo library unless it is there already: {already, ref,
// library}. The server's host is one; without it this package does it through the library plugins.
type Librarian interface {
	ToLibrary(sha256 string, dateMs *int64) (core.M, error)
}

// Env is what the tools work on: one archive, and what the server lends them.
type Env struct {
	Store   *core.Store
	Fetcher MediaFetcher // nil: files only from the media store and the libraries
	Library Librarian    // nil: through the library plugins, here
	Inline  bool         // download_media also gives the file itself (a client on another machine)
}

// New is the MCP server of one archive.
func New(env Env) *sdk.Server {
	if env.Library == nil {
		env.Library = localLibrary{env.Store}
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "everysaid", Version: "1"},
		&sdk.ServerOptions{Instructions: instructions(env.Store), SchemaCache: schemas})
	// arguments sent as null: the SDK would fail on them (it puts the defaults into a nil map)
	srv.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if r, ok := req.(*sdk.CallToolRequest); ok && r.Params != nil && strings.TrimSpace(string(r.Params.Arguments)) == "null" {
				r.Params.Arguments = json.RawMessage("{}")
			}
			return next(ctx, method, req)
		}
	})
	addTools(srv, env)
	return srv
}

var schemas = sdk.NewSchemaCache()

func instructions(s *core.Store) string {
	return fmt.Sprintf("The user's personal archive of messages and calls, from the services its plugins bring: %s. "+
		"A person is one chat (id p<number>) whatever services they were reached on; groups are "+
		"c<number>. Find people or chats first (find_people, get_direct_chat, list_chats), then read (read_chat, "+
		"list_messages, search_messages). Times are in the user's time zone.", strings.Join(knownServices(s), ", "))
}

// Main is `everysaid mcp`: the archive as an MCP server over stdio. Nothing but the protocol goes
// to stdout.
func Main(args []string) error {
	fs := flag.NewFlagSet("everysaid mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	path := fs.String("db", "", i18n.Say("the archive (default: {path})", map[string]any{"path": archive.DB()}))
	fs.StringVar(path, "archive", "", i18n.Say("the same as --db", nil))
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "everysaid mcp [--db PATH]\n\n"+i18n.Say("The archive as an MCP server for an assistant (stdio).", nil))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	store, err := core.Open(*path)
	if err != nil {
		return errors.New(i18n.Say("cannot open the archive: {error}", map[string]any{"error": err}))
	}
	defer store.Close()
	err = New(Env{Store: store}).Run(context.Background(), &sdk.StdioTransport{})
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Handler serves MCP over streamable HTTP. Every request carries `Authorization: Bearer <token>`;
// auth gives the Env of the token's user, or false (401). Stateless: each request is checked and
// answered on its own, so no one can carry on another token's session.
func Handler(auth func(token string) (Env, bool)) http.Handler {
	var mu sync.Mutex
	servers := map[*core.Store]*sdk.Server{}
	type key struct{}
	h := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		env, _ := r.Context().Value(key{}).(Env)
		if env.Store == nil {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		srv, ok := servers[env.Store]
		if !ok {
			srv = New(env)
			servers[env.Store] = srv
		}
		return srv
	}, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true,
		// the token is the protection (a DNS-rebinding page has none); behind a reverse proxy every
		// request comes from localhost with the public host name
		DisableLocalhostProtection: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the scheme's name in any case (RFC 9110: "bearer" is "Bearer")
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		ok = ok && strings.EqualFold(scheme, "Bearer")
		var env Env
		if ok && strings.TrimSpace(token) != "" {
			env, ok = auth(strings.TrimSpace(token))
		}
		if !ok || env.Store == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="everysaid"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), key{}, env)))
	})
}

// --- times -----------------------------------------------------------------------------------------

func zone() *time.Location {
	if config.Timezone != nil {
		return config.Timezone
	}
	return time.Local
}

// when is an instant (Unix ms) as the user's local time, to the minute; nil for none.
func when(ms any) any {
	var v int64
	switch x := ms.(type) {
	case int64:
		v = x
	case *int64:
		if x == nil {
			return nil
		}
		v = *x
	case int:
		v = int64(x)
	case float64:
		v = int64(x)
	default:
		return nil
	}
	if v == 0 {
		return nil
	}
	return time.UnixMilli(v).In(zone()).Format("2006-01-02T15:04-07:00")
}

// day is the start of a day (YYYY-MM-DD) in the user's time zone; days later: that many after it.
func day(d string, later int) (int64, error) {
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(d), zone())
	if err != nil {
		return 0, fmt.Errorf("a date is YYYY-MM-DD, not %q", d)
	}
	return t.AddDate(0, 0, later).UnixMilli(), nil
}

// span is since and until (YYYY-MM-DD, both days included) as instants; nil where not given.
func span(since, until string) (*int64, *int64, error) {
	var lo, hi *int64
	if since != "" {
		v, err := day(since, 0)
		if err != nil {
			return nil, nil, err
		}
		lo = &v
	}
	if until != "" {
		v, err := day(until, 1)
		if err != nil {
			return nil, nil, err
		}
		hi = &v
	}
	return lo, hi, nil
}

// --- items -----------------------------------------------------------------------------------------

// slim is a stream item as the assistant needs it.
func slim(item core.M) core.M {
	if item["type"] == "call" {
		dir := "in"
		if item["outgoing"] == true {
			dir = "out"
		}
		out := core.M{"type": "call", "id": item["id"], "time": when(item["ts"]), "service": item["service"],
			"direction": dir, "answered": item["answered"], "duration_s": item["duration"], "video": item["video"]}
		if truthy(item["detail"]) {
			out["detail"] = item["detail"]
		}
		if truthy(item["with"]) {
			out["with"] = item["with"]
		}
		if truthy(item["chat_id"]) {
			out["chat"] = item["chat_id"]
		}
		return out
	}
	var from any = "them"
	if item["outgoing"] == true {
		from = "me"
	} else if truthy(item["sender"]) {
		from = item["sender"]
	}
	out := core.M{"id": item["id"], "time": when(item["ts"]), "service": item["service"], "from": from, "kind": item["kind"]}
	if truthy(item["text"]) {
		out["text"] = item["text"]
	}
	for _, k := range []string{"subtype", "edited", "deleted", "forwarded"} {
		if truthy(item[k]) {
			out[k] = item[k]
		}
	}
	if r, ok := item["reply"].(core.M); ok {
		txt, _ := r["text"].(string)
		out["reply_to"] = core.M{"id": r["id"], "text": cut(txt, 120)}
	}
	if rs, _ := item["reactions"].([]core.M); len(rs) > 0 {
		list := []any{}
		for _, r := range rs {
			if truthy(r["emoji"]) {
				list = append(list, r["emoji"])
			} else {
				list = append(list, r["code"])
			}
		}
		out["reactions"] = list
	}
	if as, _ := item["attachments"].([]core.M); len(as) > 0 {
		files := []core.M{}
		for _, a := range as {
			files = append(files, core.M{"sha256": a["sha256"], "mime": a["mime"], "size": a["size"]})
		}
		out["files"] = files
	}
	if truthy(item["location"]) {
		out["location"] = item["location"]
	}
	if truthy(item["chat_id"]) {
		out["chat"] = item["chat_id"]
	}
	return out
}

func slimAll(items []core.M) []core.M {
	out := make([]core.M, 0, len(items))
	for _, it := range items {
		out = append(out, slim(it))
	}
	return out
}

// cut is the first n characters (as Python's s[:n]).
func cut(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

// truthy is Python's truth of a value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case []core.M:
		return len(x) > 0
	case core.M:
		return len(x) > 0
	}
	return true
}

// capped is n within 1..most, def when not given.
func capped(n, def, most int) int {
	if n <= 0 {
		n = def
	}
	return min(n, most)
}
