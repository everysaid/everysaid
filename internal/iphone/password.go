// Ports the password handling of scripts/iphone-*.py and everysaid/config.py (secret,
// move_to_keyring): the keyring, else the 600 file, else asked with no echo. Never printed.
package iphone

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"everysaid/internal/config"
	"everysaid/internal/i18n"
	"everysaid/internal/phones"
)

// Secret is the name the backup password is kept under (keyring, else a file in the config folder).
const Secret = "backup-password"

// stdin is one reader of the standard input for the whole run (a line given, then the answers).
var stdin = bufio.NewReader(os.Stdin)

// StoredPassword is the password as kept, nil when it is not.
func StoredPassword() ([]byte, error) {
	v, err := config.Secret(Secret)
	if errors.Is(err, config.ErrExposed) {
		return nil, phones.Fail("{path} can be read by others: chmod 600 and again.", map[string]any{"path": config.SecretFile(Secret)})
	}
	if err != nil || v == "" {
		return nil, err
	}
	return []byte(v), nil
}

// ask asks for the password on the terminal, not shown (getpass); from a line of the standard
// input when it is not a terminal. The prompt goes to stderr.
func ask(prompt string) ([]byte, error) {
	fmt.Fprint(os.Stderr, i18n.Say(prompt, nil))
	if term.IsTerminal(int(os.Stdin.Fd())) {
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return pw, err
	}
	return readLine()
}

// readLine is a line of the standard input without its end.
func readLine() ([]byte, error) {
	line, err := stdin.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		if err == io.EOF {
			return nil, nil
		}
		return nil, err
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}

// moveToKeyring copies the password's file into the keyring, checks it reads back the same, then
// offers to remove the file (only on "y"). The value is never shown.
func moveToKeyring(out io.Writer) error {
	say := phones.Printer(out)
	path := config.SecretFile(Secret)
	if _, err := os.Stat(path); err != nil {
		return phones.Fail("{path} does not exist.", map[string]any{"path": path})
	}
	if config.Exposed(path) {
		return phones.Fail("{path} can be read by others: chmod 600 and again.", map[string]any{"path": path})
	}
	if err := config.MoveToKeyring(Secret); err != nil {
		return phones.Fail("There is no keyring on this system (or it refused): the file stays as it is.", nil)
	}
	say("{name} is in the keyring ({app}/{name}) and reads back correctly.", map[string]any{"name": Secret, "app": config.Keyring})
	fmt.Fprint(out, i18n.Say("Remove {path}? [y/N] ", map[string]any{"path": path}))
	answer, _ := readLine()
	if strings.ToLower(strings.TrimSpace(string(answer))) == "y" {
		if err := os.Remove(path); err != nil {
			return err
		}
		say("Removed.", nil)
	} else {
		say("The file stays; the keyring comes first, so it is no longer used.", nil)
	}
	return nil
}
