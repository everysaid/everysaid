// The command line of the server: `everysaid serve` (everysaid/server/__init__.py) and `everysaid
// user` (users.py), for cmd/everysaid to call.
package server

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/i18n"
)

// ServeMain is `everysaid serve [--host H] [--port P] [--archive PATH] [--web DIR]`: the app on one
// port until interrupted.
//
// Listens on 127.0.0.1:8520 unless config `[server] host/port` say otherwise. To reach it from other
// devices, put it behind a reverse proxy with HTTPS (Caddy, nginx) or a private network (WireGuard,
// Tailscale), and set `[server] origin` to the address the devices use: passkeys are tied to it.
//
// What goes wrong (a request that fails, a task that breaks) is written to the console and to
// `<state>/logs/server.log` (kept to five files of 5 MB).
func ServeMain(argv []string, out io.Writer) error {
	fs := flag.NewFlagSet("everysaid serve", flag.ContinueOnError)
	host := fs.String("host", config.ServerHost, "address to listen on")
	port := fs.Int("port", config.ServerPort, "port to listen on")
	arch := fs.String("archive", "", "archive database (default: the user's)")
	web := fs.String("web", "", "serve the interface from this folder (e.g. web/dist) instead of the embedded one")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	umask()
	s, err := New(Options{Archive: *arch, Host: *host, Port: *port, WebDir: *web, Out: out})
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(out, i18n.Say("Everysaid: {origin} (listening on {host}:{port})", M{"origin": s.Origin(), "host": *host, "port": *port}))
	return s.Run(ctx)
}

// UserMain is `everysaid user list|link|mcp-token|sessions [--user ID] [--minutes N]`: what is done
// on the server's own machine.
//
//	list         the users
//	link         a one-time link for a passkey: the first user, another device, or a way back in
//	             after losing every passkey
//	mcp-token    a token for the MCP server over HTTP (--revoke ID: ends one)
//	mcp-tokens   the MCP tokens: id, label, when made, when last used
//	sessions     the open sessions
func UserMain(argv []string, out io.Writer) error {
	fs := flag.NewFlagSet("everysaid user", flag.ContinueOnError)
	user := fs.Int64("user", 0, "the user (default: the first)")
	minutes := fs.Int("minutes", 60, "how long a link is valid")
	revoke := fs.String("revoke", "", "mcp-token: end the token of this id (from mcp-tokens)")
	what := ""
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		what, argv = argv[0], argv[1:]
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if what == "" && fs.NArg() > 0 {
		what = fs.Arg(0)
	}
	switch what {
	case "list", "link", "mcp-token", "mcp-tokens", "sessions":
	default:
		return fmt.Errorf("everysaid user: list, link, mcp-token, mcp-tokens or sessions")
	}
	auth, err := OpenAuth("")
	if err != nil {
		return err
	}
	defer auth.Close()
	users := auth.Users()
	uid := *user
	if uid == 0 && len(users) > 0 {
		uid = users[0].ID
	}
	switch what {
	case "list":
		for _, u := range users {
			fmt.Fprintf(out, "%d\t%s\t%s\t%s\n", u.ID, u.Name, u.Archive, i18n.Say("{n} passkeys", M{"n": len(auth.Passkeys(u.ID))}))
		}
		if len(users) == 0 {
			fmt.Fprintln(out, i18n.Say("no user yet: `everysaid serve` prints the link of the first setup", nil))
		}
	case "link":
		var forUser *int64
		if uid != 0 {
			forUser = &uid
		}
		token := auth.SetupLink(forUser, *minutes)
		note := i18n.Say("(one use, valid {minutes} minutes)", M{"minutes": *minutes})
		if uid == 0 {
			note = i18n.Say("(one use, valid {minutes} minutes · a new user)", M{"minutes": *minutes})
		}
		fmt.Fprintf(out, "%s/setup#%s\n%s\n", config.ServerOrigin, token, note)
	case "mcp-token":
		if uid == 0 {
			return fmt.Errorf("%s", i18n.Say("no user", nil))
		}
		if *revoke != "" {
			if !auth.RevokeMCPToken(uid, *revoke) {
				return fmt.Errorf("%s", i18n.Say("no such token: {id}", M{"id": *revoke}))
			}
			fmt.Fprintln(out, i18n.Say("token {id} revoked", M{"id": *revoke}))
			return nil
		}
		fmt.Fprintln(out, auth.NewMCPToken(uid, "cli"))
	case "mcp-tokens":
		when := func(v any) string {
			if n, ok := v.(int64); ok {
				return time.Unix(n, 0).Format("2006-01-02 15:04")
			}
			return i18n.Say("never", nil)
		}
		for _, t := range auth.MCPTokens(uid) {
			fmt.Fprintf(out, "%v\t%v\t%s\t%s\n", t["id"], noneStr(t["label"]), when(t["created_at"]), when(t["last_used"]))
		}
	case "sessions":
		for _, s := range auth.Sessions(uid) {
			fmt.Fprintf(out, "%v\t%v\t%v\t%v\n", s["id"], noneStr(s["via"]), noneStr(s["ip"]), noneStr(s["agent"]))
		}
	}
	return nil
}

// noneStr is a value as Python prints it (None for nothing).
func noneStr(v any) string {
	if v == nil {
		return "None"
	}
	return fmt.Sprint(v)
}
