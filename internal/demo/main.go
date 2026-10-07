// Ports env_for and main of everysaid/demo.py: the demo's own folders and settings, and the
// command.

package demo

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"everysaid/internal/config"
	"everysaid/internal/i18n"
)

// Serve starts the app on the archive the environment points at (filled by the command line, with
// the server's own entry; the demo does not import the server). Main calls it for --serve, once
// the environment points at the demo's folders; it returns when the server stops.
var Serve func(dir string) error

// Env points Everysaid's folders at dir (data, cache, config, state, a keyring name of its own,
// EVERYSAID_DEMO), makes them, writes the demo's config.toml where there is none, and reads the
// settings again: from then on this process works on the demo, never on the user's archive.
func Env(dir string) error {
	d, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	vars := []struct{ name, value string }{
		{"EVERYSAID_DATA", filepath.Join(d, "data")}, {"EVERYSAID_CACHE", filepath.Join(d, "cache")},
		{"EVERYSAID_CONFIG", filepath.Join(d, "config")}, {"EVERYSAID_STATE", filepath.Join(d, "state")},
		{"EVERYSAID_KEYRING", "everysaid-demo"}, {"EVERYSAID_DEMO", "1"},
	}
	for _, v := range vars {
		if err := os.Setenv(v.name, v.value); err != nil {
			return err
		}
	}
	for _, v := range vars[:4] {
		if err := os.MkdirAll(v.value, 0o777); err != nil {
			return err
		}
	}
	cfg := filepath.Join(d, "config", "config.toml")
	if _, err := os.Stat(cfg); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(cfg, []byte("[owner]\nnumbers = [\"+15550000000\"]\nregion = \"US\"\nname = \"Demo\"\n\n[server]\nport = 8530\n"), 0o666); err != nil {
			return err
		}
	}
	config.Load()
	return nil
}

// Main is `everysaid demo [--dir DIR] [--seed N] [--serve]`.
func Main(args []string) error {
	fs := flag.NewFlagSet("everysaid demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "demo", "")
	seed := fs.Int64("seed", 7, "")
	serve := fs.Bool("serve", false, "")
	help := func() {
		fmt.Println(i18n.Say("usage: everysaid demo [--dir DIR] [--seed SEED] [--serve]", nil))
		fmt.Println()
		fmt.Println(i18n.Say("A demo archive of invented people.", nil))
		fmt.Println()
		fmt.Println(i18n.Say("options:", nil))
		fmt.Println("  --dir DIR    ", i18n.Say("the folder of the demo (default: demo)", nil))
		fmt.Println("  --seed SEED  ", i18n.Say("the seed of what is invented (default: 7)", nil))
		fmt.Println("  --serve      ", i18n.Say("then start the app on it", nil))
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			help()
			return nil
		}
		help()
		return err
	}
	if fs.NArg() > 0 {
		help()
		return fmt.Errorf("%s", i18n.Say("unrecognized arguments: {args}", map[string]any{"args": fs.Args()}))
	}
	if err := Env(*dir); err != nil {
		return err
	}
	RegisterIfDemo()
	private() // the folders above as the user's others, what is in them private (as in Python)
	if _, err := BuildArchive(*seed); err != nil {
		return err
	}
	if *serve {
		if Serve == nil {
			return errors.New("demo: no server to start (demo.Serve is not set)")
		}
		return Serve(*dir)
	}
	return nil
}

// Build makes a fresh demo archive (seed 7) in a temporary folder of the test, with the environment
// and the settings pointing there for the rest of the test (the user's archive and secrets are never
// seen); it gives the folder and the archive's path. Tests that want one archive for many can copy
// the file.
func Build(t testing.TB) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	vars := []string{"EVERYSAID_DATA", "EVERYSAID_CACHE", "EVERYSAID_CONFIG", "EVERYSAID_STATE", "EVERYSAID_KEYRING", "EVERYSAID_DEMO"}
	for _, v := range vars {
		t.Setenv(v, os.Getenv(v)) // given back at the end of the test
	}
	t.Cleanup(config.Load)
	if err := Env(dir); err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	if null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil { // the summary line is not the test's output
		os.Stdout = null
		defer func() { os.Stdout = stdout; null.Close() }()
	}
	path, err := BuildArchive(7)
	if err != nil {
		t.Fatal(err)
	}
	return dir, path
}
