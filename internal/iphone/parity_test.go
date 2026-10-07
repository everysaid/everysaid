package iphone

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParityPhones runs iphone-sync (--no-backup), iphone-ls and, with EVERYSAID_PARITY_VERIFY=1,
// iphone-verify on the configured backup, their output into files of EVERYSAID_PARITY_PHONES, for a
// comparison with the Python scripts' (by hashes and counts, never by content).
func TestParityPhones(t *testing.T) {
	dir := os.Getenv("EVERYSAID_PARITY_PHONES")
	if dir == "" {
		t.Skip("no EVERYSAID_PARITY_PHONES")
	}
	run := func(name string, main func([]string, *os.File) error, args ...string) {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := main(args, f); err != nil {
			t.Errorf("%s: failed (%T)", name, err)
		}
	}
	run("go-sync.txt", func(a []string, f *os.File) error { return SyncMain(a, f) },
		"--no-backup", "-o", filepath.Join(dir, "go-sync"))
	run("go-ls0.txt", func(a []string, f *os.File) error { return LsMain(a, f) }, "%", "%", "--depth", "0")
	run("go-ls3.txt", func(a []string, f *os.File) error { return LsMain(a, f) }, "%", "%", "--depth", "3")
	if os.Getenv("EVERYSAID_PARITY_VERIFY") == "1" {
		run("go-verify.txt", func(a []string, f *os.File) error { return VerifyMain(a, f) })
	}
}
