package importers

import (
	"os"
	"path/filepath"
	"testing"

	"everysaid/internal/config"
)

// TestMain points every folder of Everysaid into a temporary one, with invented owner settings, so
// that the tests never touch the user's archive, sources or secrets.
func TestMain(m *testing.M) {
	if os.Getenv("EVERYSAID_PARITY_DB") != "" { // a parity run: the folders are given
		os.Exit(m.Run())
	}
	root, err := os.MkdirTemp("", "everysaid-importers-test-")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{"DATA", "CACHE", "CONFIG", "STATE"} {
		dir := filepath.Join(root, name)
		os.MkdirAll(dir, 0o700)
		os.Setenv("EVERYSAID_"+name, dir)
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test")
	os.WriteFile(filepath.Join(root, "CONFIG", "config.toml"),
		[]byte("[owner]\nnumbers = [\"+15550000000\"]\nregion = \"US\"\n"), 0o600)
	config.Load()
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}
