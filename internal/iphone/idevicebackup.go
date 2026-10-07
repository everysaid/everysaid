// Ports the backup step of scripts/iphone-sync.py: idevicebackup2 over the cable.
package iphone

import (
	"os"
	"strings"

	"everysaid/internal/phones"
)

func (r *syncRun) backup() error {
	cmd := []string{"idevicebackup2", "-u", r.UDID, "backup"}
	if r.Full {
		cmd = append(cmd, "--full")
	}
	cmd = append(cmd, r.BackupRoot)
	r.Say("Backup: {cmd}", map[string]any{"cmd": strings.Join(cmd, " ")})
	r.Say("The iPhone may ask for its passcode: type it there.", nil)
	if err := os.MkdirAll(r.BackupRoot, 0o700); err != nil {
		return err
	}
	path, err := phones.Tool(cmd[0])
	if err != nil {
		return err
	}
	// Its output as it comes (a backup takes minutes), and kept to say why if it fails (bytes as
	// they come, its \r of a progress bar kept: the reader draws them as a terminal does). Into a
	// pipe it would hold its lines back for long, so it gets a terminal where there is one.
	proc := command(path, cmd[1:]...)
	out, err := start(proc)
	if err != nil {
		return err
	}
	var said []byte
	buf := make([]byte, 4096)
	for {
		n, err := out.Read(buf)
		if n > 0 {
			r.Raw.Write(buf[:n])
			said = append(said, buf[:n]...)
			if len(said) > 20000 {
				said = said[len(said)-20000:]
			}
		}
		if err != nil { // the end, or a terminal whose writer has closed
			break
		}
	}
	out.Close()
	if proc.Wait() != nil {
		// said in the user's words by the app, so each one whole
		text := string(said)
		switch {
		case strings.Contains(text, "Device locked") || strings.Contains(text, "ErrorCode 208"):
			return phones.Fail("The backup failed: the iPhone is locked. Unlock it, and type its passcode there when it asks.", nil)
		case strings.Contains(text, "No device found") || strings.Contains(text, "ERROR: Could not connect"):
			return phones.Fail("The backup failed: no iPhone found. Connect it with a cable, unlock it and tap Trust.", nil)
		}
		return phones.Fail("The backup failed: idevicebackup2 did not finish (see the whole log).", nil)
	}
	return nil
}
