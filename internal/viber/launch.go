package viber

// Viber Desktop started and kept running by the source itself: headless on a virtual display of its
// own, on a D-Bus session of its own, with the bridge preloaded (what bridges/viber/run.sh does by
// hand). It runs apart from the server (a session of its own and, under systemd, a scope of its own:
// stopping a service ends every process of its cgroup), so the server's restarts leave it running:
// the next start finds it on its socket. It stops when the user says (an action of the
// source, `everysaid viber stop`) and when the user ends the live connection; once stopped by the
// user it stays stopped until started again.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
)

const (
	defaultViber   = "/opt/viber/Viber"
	defaultDisplay = ":99"
)

// DefaultLibrary is where `make -C bridges/viber/inject install` puts the bridge.
func DefaultLibrary() string { return filepath.Join(config.Data, "viber", "viber-bridge.so") }

func library(c *plugins.Context) string {
	if s := c.Str("library"); s != "" {
		return config.ExpandUser(s)
	}
	return DefaultLibrary()
}

func viberBin(c *plugins.Context) string {
	if s := c.Str("viber"); s != "" {
		return config.ExpandUser(s)
	}
	return defaultViber
}

func display(c *plugins.Context) string {
	if s := c.Str("display"); s != "" {
		return s
	}
	return defaultDisplay
}

// managed: the source starts Viber Desktop itself (its setting, on by default).
func managed(c *plugins.Context) bool {
	if _, set := c.Settings["launch"]; !set {
		return true
	}
	return c.Bool("launch")
}

// stateDir keeps what must outlive the server and be seen by the command line: Viber's process,
// and whether the user stopped it.
func stateDir(c *plugins.Context) string {
	return filepath.Join(config.State, "viber", fmt.Sprint(c.ID))
}

func stoppedFile(c *plugins.Context) string { return filepath.Join(stateDir(c), "stopped") }
func pidFile(c *plugins.Context) string     { return filepath.Join(stateDir(c), "viber.pid") }

// LogPath is Viber Desktop's own output (the bridge's check at its start among it), of its last start.
func LogPath(c *plugins.Context) string {
	return filepath.Join(config.Logs, fmt.Sprintf("plugin-%d", c.ID), "viber.log")
}

// stoppedByUser: the user stopped Viber Desktop here; it is not started again until they say.
func stoppedByUser(c *plugins.Context) bool {
	_, err := os.Stat(stoppedFile(c))
	return err == nil
}

func setStopped(c *plugins.Context, stopped bool) error {
	if !stopped {
		if err := os.Remove(stoppedFile(c)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(stateDir(c), 0o700); err != nil {
		return err
	}
	return os.WriteFile(stoppedFile(c), []byte(time.Now().Format(time.RFC3339)+"\n"), 0o600)
}

// started is the process the source started (dbus-run-session, leading its own session, Viber
// under it), 0 when there is none still running.
func started(c *plugins.Context) int {
	b, err := os.ReadFile(pidFile(c))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || syscall.Kill(pid, 0) != nil {
		return 0
	}
	cmd, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)) // not another process given the same number
	if err != nil || !strings.Contains(string(cmd), "dbus-run-session") {
		return 0
	}
	return pid
}

// launching: one start or stop at a time per instance (the live connection, an action, the command
// line in this process).
var launching sync.Map

func launchLock(c *plugins.Context) *sync.Mutex {
	l, _ := launching.LoadOrStore(c.ID, &sync.Mutex{})
	return l.(*sync.Mutex)
}

// launchWait is how long Viber Desktop may take to answer once started.
var launchWait = 90 * time.Second

// start starts Viber Desktop (tests stand in for it).
var start = launch

// ensure starts Viber Desktop where the source keeps it running and it is not: not running, not
// stopped by the user, not already starting.
func ensure(c *plugins.Context) error {
	if !managed(c) || stoppedByUser(c) || running(c) {
		return nil
	}
	l := launchLock(c)
	l.Lock()
	defer l.Unlock()
	if running(c) || started(c) != 0 {
		return nil
	}
	c.Log("starting Viber Desktop", nil)
	return start(c)
}

// waitRunning waits until Viber Desktop answers, at most launchWait.
func waitRunning(ctx context.Context, c *plugins.Context) error {
	for end := time.Now().Add(launchWait); !running(c); {
		if time.Now().After(end) {
			return said("Viber Desktop did not start: its output is in {log}", "log", LogPath(c))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil
}

// launch starts Viber Desktop with the bridge, on its display (Xvfb, started if none is there).
func launch(c *plugins.Context) error {
	cmd, err := viberCommand(c)
	if err != nil {
		return err
	}
	if err := ensureDisplay(display(c)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(LogPath(c)), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(LogPath(c), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // not left a zombie while the server runs; after, the system takes it
	if err := os.MkdirAll(stateDir(c), 0o700); err != nil {
		return err
	}
	return os.WriteFile(pidFile(c), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600)
}

// viberCommand is Viber Desktop with the bridge preloaded, on a D-Bus session of its own (on the
// desktop's it would put an icon in the tray and its notifications on the screen), in a session of
// its own (the server's restarts do not reach it), SIGINT left to stop it (Viber ignores SIGTERM).
// The bridge may act: the source's "Sending messages" is what allows it.
func viberCommand(c *plugins.Context) (*exec.Cmd, error) {
	so, bin := library(c), viberBin(c)
	if _, err := os.Stat(so); err != nil {
		return nil, said("The bridge is not installed at {path}: make -C bridges/viber/inject install", "path", so)
	}
	if _, err := os.Stat(bin); err != nil {
		return nil, said("Viber Desktop is not installed at {path}", "path", bin)
	}
	for _, tool := range []string{"dbus-run-session", "env"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, said("{tool} is not installed", "tool", tool)
		}
	}
	args := []string{"dbus-run-session", "--", "env", "--default-signal=INT", "LD_PRELOAD=" + so, bin}
	if userScope() {
		args = append([]string{"systemd-run", "--user", "--scope", "--quiet", "--collect",
			fmt.Sprintf("--unit=everysaid-viber-%d", c.ID), "--"}, args...)
	}
	cmd := exec.Command(args[0], args[1:]...)
	var env []string
	for _, e := range os.Environ() {
		if k, _, _ := strings.Cut(e, "="); k != "DISPLAY" && k != "WAYLAND_DISPLAY" && k != "DBUS_SESSION_BUS_ADDRESS" &&
			k != "LD_PRELOAD" && !strings.HasPrefix(k, "VIBER_") && k != "QT_QPA_PLATFORM" {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "DISPLAY="+display(c), "QT_QPA_PLATFORM=xcb", "VIBER_BRIDGE_SOCK="+socket(c), "VIBER_ALLOW_SEND=1")
	cmd.Dir = filepath.Dir(so)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, nil
}

// userScope: the user's systemd is there to put Viber in a scope of its own (systemd-run --scope
// runs it in place, the same process, in another cgroup).
var userScope = func() bool {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "systemd", "private"))
	return os.Getenv("XDG_RUNTIME_DIR") != "" && err == nil
}

// ensureDisplay starts Xvfb on the display, unless an X server is there already (kept for the next
// start: it costs little).
func ensureDisplay(d string) error {
	n := strings.TrimPrefix(d, ":")
	if i := strings.IndexByte(n, '.'); i >= 0 {
		n = n[:i]
	}
	sock := "/tmp/.X11-unix/X" + n
	if _, err := os.Stat(sock); err == nil {
		return nil
	}
	if _, err := exec.LookPath("Xvfb"); err != nil {
		return said("{tool} is not installed", "tool", "Xvfb")
	}
	args := []string{"Xvfb", d, "-screen", "0", "1280x900x24", "-nolisten", "tcp"}
	if userScope() { // as Viber: out of the server's cgroup, or a restart of the server takes its display
		args = append([]string{"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=everysaid-xvfb-" + n, "--"}, args...)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			return nil
		}
	}
	return said("Xvfb did not start on {display}", "display", d)
}

// stopWait is how long Viber Desktop may take to quit, asked, then told with SIGINT.
var stopWait = 20 * time.Second

// stopViber quits Viber Desktop cleanly (the bridge's quit, its database closed), else with SIGINT
// to the session the source started it in.
func stopViber(ctx context.Context, c *plugins.Context) error {
	l := launchLock(c)
	l.Lock()
	defer l.Unlock()
	gone := func() bool { return !running(c) && started(c) == 0 }
	wait := func() bool {
		for end := time.Now().Add(stopWait); time.Now().Before(end) && ctx.Err() == nil; time.Sleep(200 * time.Millisecond) {
			if gone() {
				return true
			}
		}
		return gone()
	}
	if gone() {
		return nil
	}
	if running(c) {
		act(ctx, socket(c), "quit")
		if wait() {
			return nil
		}
	}
	if pid := started(c); pid != 0 {
		syscall.Kill(-pid, syscall.SIGINT)
		if wait() {
			return nil
		}
	}
	if running(c) {
		return errs.Plugin("Viber Desktop did not stop", 0)
	}
	return nil
}

// LiveStopped: the user ended the live connection (turned it off, disabled or removed the source):
// Viber Desktop, there for it, stops too; it starts again with the live connection.
func (Plugin) LiveStopped(c *plugins.Context) {
	if !managed(c) {
		return
	}
	if err := stopViber(context.Background(), c); err != nil {
		c.Log("error: {e}", map[string]any{"e": err})
	}
}

// said is a plugin error with one value in its words.
func said(text, key string, value any) error {
	return &errs.UserError{Code: "plugin", Status: 409, Text: text, Params: map[string]any{key: value}}
}

// --- the actions ---------------------------------------------------------------------------------

func (Plugin) Action(c *plugins.Context, name string) error {
	ctx := context.Background()
	switch name {
	case "start":
		if err := setStopped(c, false); err != nil {
			return err
		}
		if err := ensure(c); err != nil {
			return err
		}
		return waitRunning(ctx, c)
	case "stop":
		if err := setStopped(c, true); err != nil {
			return err
		}
		return stopViber(ctx, c)
	case "restart":
		if err := stopViber(ctx, c); err != nil {
			return err
		}
		if err := setStopped(c, false); err != nil {
			return err
		}
		if err := ensure(c); err != nil {
			return err
		}
		return waitRunning(ctx, c)
	}
	return fmt.Errorf("no action %s", name)
}

// IdleActions: starting where it runs, stopping where it does not; none where the source does not
// start Viber Desktop.
func (Plugin) IdleActions(c *plugins.Context) []string {
	switch {
	case !managed(c):
		return []string{"start", "stop", "restart"}
	case running(c):
		return []string{"start"}
	}
	return []string{"stop", "restart"}
}

// --- the command line ----------------------------------------------------------------------------

// cliHost is what the command line gives an instance: the archive, nothing to tell.
type cliHost struct{ store *core.Store }

func (h cliHost) Store() *core.Store { return h.store }
func (cliHost) Emit(plugins.M)       {}
func (cliHost) Alert(_, _ string)    {}

// Main is `everysaid viber start|stop|restart|status`: Viber Desktop as the source keeps it, by hand.
func Main(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("viber", flag.ContinueOnError)
	db := fs.String("db", "", i18n.Say("the archive (default: its usual place)", nil))
	iid := fs.Int64("instance", 0, i18n.Say("the Viber Desktop source (its number), where there are several", nil))
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "everysaid viber start|stop|restart|status [--instance N] [--db PATH]\n\n"+
			i18n.Say("Viber Desktop, as the Viber Desktop source keeps it running: started, stopped (until started again), restarted, or how it is.", nil))
		fs.PrintDefaults()
	}
	var verb string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verb == "" && fs.NArg() > 0 {
		verb = fs.Arg(0)
	}
	switch verb {
	case "start", "stop", "restart", "status":
	default:
		fs.Usage()
		return flag.ErrHelp
	}
	store, err := core.Open(*db)
	if err != nil {
		return errors.New(i18n.Say("cannot open the archive: {error}", map[string]any{"error": err}))
	}
	defer store.Close()
	var rows []plugins.Instance
	for _, r := range plugins.Instances(store, "source") {
		if r.Plugin == "viber-desktop" && (*iid == 0 || r.ID == *iid) {
			rows = append(rows, r)
		}
	}
	switch {
	case len(rows) == 0:
		return errors.New(i18n.Say("no Viber Desktop source", nil))
	case len(rows) > 1:
		return errors.New(i18n.Say("several Viber Desktop sources: say which with --instance", nil))
	}
	c := plugins.NewContext(cliHost{store}, rows[0])
	if verb == "status" {
		say := func(text string, params map[string]any) { fmt.Fprintln(out, i18n.Say(text, params)) }
		state := "not running"
		if running(c) {
			state = "running"
		}
		say("Viber Desktop: {state}", map[string]any{"state": i18n.Say(state, nil)})
		if pid := started(c); pid != 0 {
			say("started by the source: process {pid}", map[string]any{"pid": pid})
		}
		if !managed(c) {
			say("the source does not start it (its setting)", nil)
		} else if stoppedByUser(c) {
			say("stopped by the user: not started again until started", nil)
		}
		if running(c) {
			if ck := checkNow(context.Background(), c); ck.Known {
				say("version {version}", map[string]any{"version": ck.Version})
				if m := ck.lacks(func(s string) string { return i18n.Say(s, nil) }); m != "" {
					say("missing: {what}", map[string]any{"what": m})
				}
			}
		}
		say("its output: {log}", map[string]any{"log": LogPath(c)})
		return nil
	}
	if err := (Plugin{}).Action(c, verb); err != nil {
		var ue *errs.UserError
		if errors.As(err, &ue) {
			return errors.New(i18n.Say(ue.Text, ue.Params))
		}
		return err
	}
	return nil
}
