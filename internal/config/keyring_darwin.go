package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/zalando/go-keyring"
)

// The Keychain through /usr/bin/security, the secret stored as its own bytes (as Python's keyring
// stores it) and read back from -g, which gives any secret whole (-w gives some in hex).
func init() {
	keyringGet = darwinGet
	keyringSet = darwinSet
	keyringDelete = darwinDelete
}

const security = "/usr/bin/security"

func darwinGet(service, name string) (string, error) {
	out, err := exec.Command(security, "find-generic-password", "-s", service, "-a", name, "-g").CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "could not be found") {
			return "", keyring.ErrNotFound
		}
		return "", err
	}
	return securityPassword(string(out))
}

// darwinSet gives the secret on security's standard input (never on a command line others can see),
// in hex; one too long for its line is refused, and SaveSecret keeps it in a file instead.
func darwinSet(service, name, value string) error {
	for _, s := range []string{service, name} {
		if strings.ContainsAny(s, "\"\\\n") {
			return fmt.Errorf("unusable keychain name %q", s)
		}
	}
	line := fmt.Sprintf("add-generic-password -U -s \"%s\" -a \"%s\" -X %s\n", service, name, hex.EncodeToString([]byte(value)))
	if len(line) > 4000 {
		return errors.New("secret too long for the keychain's command line")
	}
	cmd := exec.Command(security, "-i")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := io.WriteString(in, line); err != nil {
		in.Close()
		cmd.Wait()
		return err
	}
	in.Close()
	return cmd.Wait()
}

func darwinDelete(service, name string) error {
	out, err := exec.Command(security, "delete-generic-password", "-s", service, "-a", name).CombinedOutput()
	if strings.Contains(string(out), "could not be found") {
		return keyring.ErrNotFound
	}
	return err
}
