package config

import (
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/zalando/go-keyring"
)

// The keyring as the Python's `keyring` package keeps secrets, so that a secret saved by one is
// read by the other. On Linux both use the Secret Service with the same attributes (service,
// username), which go-keyring does as it is. On macOS and Windows go-keyring writes its own way
// (on macOS a "go-keyring-base64:" prefix; on Windows the target "service:name" and the secret in
// UTF-8), which the Python read as a wrong secret or not at all: keyring_darwin.go and
// keyring_windows.go put these in Python's way.
var (
	keyringGet    = keyring.Get
	keyringSet    = keyring.Set
	keyringDelete = keyring.Delete
)

// --- Windows: Python keyring's WinVaultKeyring ---------------------------------------------------

// credStore is the Credential Manager's generic credentials, by target name.
type credStore interface {
	read(target string) (user string, blob []byte, found bool, err error)
	write(target, user string, blob []byte) error
	remove(target string) error
}

// winVault keeps a secret under the target `service` (UserName the secret's name, the secret in
// UTF-16); a second secret of the same service moves the one there to `name@service`.
type winVault struct{ store credStore }

func compound(name, service string) string { return name + "@" + service }

func (v winVault) resolve(service, name string) (string, []byte, bool, error) {
	user, blob, found, err := v.store.read(service)
	if err != nil {
		return "", nil, false, err
	}
	if !found || (name != "" && user != name) {
		return v.store.read(compound(name, service))
	}
	return user, blob, true, nil
}

func (v winVault) get(service, name string) (string, error) {
	_, blob, found, err := v.resolve(service, name)
	if err != nil {
		return "", err
	}
	if !found {
		return "", keyring.ErrNotFound
	}
	return fromUTF16(blob), nil
}

func (v winVault) set(service, name, value string) error {
	user, blob, found, err := v.store.read(service)
	if err != nil {
		return err
	}
	if found { // the one there keeps its place under a compound target
		if err := v.store.write(compound(user, service), user, toUTF16(fromUTF16(blob))); err != nil {
			return err
		}
	}
	return v.store.write(service, name, toUTF16(value))
}

func (v winVault) delete(service, name string) error {
	deleted := false
	for _, target := range []string{service, compound(name, service)} {
		user, _, found, err := v.store.read(target)
		if err != nil {
			return err
		}
		if found && user == name {
			if err := v.store.remove(target); err != nil {
				return err
			}
			deleted = true
		}
	}
	if !deleted {
		return keyring.ErrNotFound
	}
	return nil
}

// toUTF16 is a text as the Credential Manager keeps it (UTF-16, little-endian, no BOM).
func toUTF16(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2*len(units))
	for i, u := range units {
		out[2*i], out[2*i+1] = byte(u), byte(u>>8)
	}
	return out
}

// fromUTF16 reads a secret as Python's keyring does: UTF-16 (a BOM says the order, else
// little-endian), else UTF-8.
func fromUTF16(b []byte) string {
	if len(b)%2 == 0 {
		big := false
		if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
			big, b = true, b[2:]
		} else if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
			b = b[2:]
		}
		units := make([]uint16, len(b)/2)
		for i := range units {
			if big {
				units[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
			} else {
				units[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
			}
		}
		if validUTF16(units) {
			return string(utf16.Decode(units))
		}
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

func validUTF16(units []uint16) bool {
	for i := 0; i < len(units); i++ {
		switch u := units[i]; {
		case u >= 0xD800 && u < 0xDC00:
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] >= 0xE000 {
				return false
			}
			i++
		case u >= 0xDC00 && u < 0xE000:
			return false
		}
	}
	return true
}

// --- macOS: the Keychain's generic passwords, the secret as its bytes ----------------------------

// securityPassword is the secret in what `security find-generic-password -g` says on stderr:
// `password: "text"` for printable text, `password: 0x<hex>  "..."` for anything else.
func securityPassword(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "password: ")
		if !ok {
			continue
		}
		if strings.HasPrefix(rest, "0x") {
			h, _, _ := strings.Cut(rest[2:], " ")
			b, err := hex.DecodeString(h)
			return string(b), err
		}
		if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
			return rest[1 : len(rest)-1], nil
		}
		if rest == "" {
			return "", nil
		}
	}
	return "", errors.New("no password in the keychain's answer")
}
