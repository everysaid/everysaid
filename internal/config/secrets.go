package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

// Secrets (the backup password, the immich key, a source's session) are kept in the system's
// keyring (Secret Service on Linux, the Keychain on macOS, the Credential Manager on Windows),
// under the service Keyring and the secret's name. Where there is no keyring (a headless Linux
// without a Secret Service), a file of that name in Config, mode 600, is used instead.

// ErrExposed: a secret's file others may read.
var ErrExposed = errors.New("secret file readable by others")

func SecretFile(name string) string { return filepath.Join(Config, name) }

// Exposed says whether others may read a file. Windows does not say in its mode: not checked there.
func Exposed(path string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && st.Mode().Perm()&0o077 != 0
}

// Secret is a secret by name: from the keyring, else from its file in Config; "" if neither has it.
func Secret(name string) (string, error) {
	if v, err := keyring.Get(Keyring, name); err == nil && v != "" {
		return v, nil
	}
	path := SecretFile(name)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if Exposed(path) {
		return "", ErrExposed
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// SecretOrEmpty is Secret where a failure counts as no secret.
func SecretOrEmpty(name string) string {
	v, _ := Secret(name)
	return v
}

// SaveSecret puts a secret into the keyring, else into its file (600); it returns where it went.
func SaveSecret(name, value string) (string, error) {
	if err := keyring.Set(Keyring, name, value); err == nil {
		if back, err := keyring.Get(Keyring, name); err == nil && back == value {
			return "keyring", nil
		}
	}
	path := SecretFile(name)
	if err := os.MkdirAll(Config, 0o700); err != nil {
		return "", err
	}
	part := path + ".part"
	os.Remove(part) // one left by an interrupted write: never written through
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(value); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(part, path)
}

// DeleteSecret takes a secret away: from the keyring, and its file if there is one.
func DeleteSecret(name string) error {
	_ = keyring.Delete(Keyring, name)
	err := os.Remove(SecretFile(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// MoveToKeyring copies a file secret into the keyring and checks it reads back the same. The
// caller offers to remove the file.
func MoveToKeyring(name string) error {
	path := SecretFile(name)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if Exposed(path) {
		return ErrExposed
	}
	value := strings.TrimRight(string(b), "\r\n")
	if err := keyring.Set(Keyring, name, value); err != nil {
		return err
	}
	if back, err := keyring.Get(Keyring, name); err != nil || back != value {
		return errors.New("the keyring did not give the secret back")
	}
	return nil
}
