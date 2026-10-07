package config

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The Keychain as Python's keyring leaves it, with security(1) as the Python's stand-in: what one
// writes the other reads, byte for byte. Only where asked (EVERYSAID_KEYCHAIN_TEST=1): it writes
// to the user's keychain, under a service of its own, and takes it away again.
func TestKeychainAsPythonsKeyring(t *testing.T) {
	if os.Getenv("EVERYSAID_KEYCHAIN_TEST") == "" {
		t.Skip("EVERYSAID_KEYCHAIN_TEST not set")
	}
	const service = "everysaid-keychain-test"
	for _, value := range []string{"plain-secret", "clé avec espaces\net lignes \"guillemets\" 😀"} {
		// Go writes: security reads the very bytes, no prefix
		if err := darwinSet(service, "go", value); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(security, "find-generic-password", "-s", service, "-a", "go", "-g").CombinedOutput()
		if err != nil {
			t.Fatal(string(out))
		}
		if got, _ := securityPassword(string(out)); got != value {
			t.Fatalf("security read %q", got)
		}
		if strings.Contains(string(out), "go-keyring") {
			t.Fatal("go-keyring's prefix stored")
		}
		// written as the Python's keyring writes (the value's own bytes): Go reads it
		cmd := exec.Command(security, "add-generic-password", "-U", "-s", service, "-a", "py", "-w", value)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
		if got, err := darwinGet(service, "py"); err != nil || got != value {
			t.Fatalf("Go read %q %v", got, err)
		}
		for _, n := range []string{"go", "py"} {
			if err := darwinDelete(service, n); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := darwinGet(service, "go"); err == nil {
			t.Fatal("not deleted")
		}
	}
}
