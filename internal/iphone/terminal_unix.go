//go:build linux || darwin

package iphone

import (
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// terminal is a pseudo-terminal (reader, writer), its lines as written (no \r added before \n);
// nil where the system will not give one.
func terminal() (*os.File, *os.File) {
	reader, writer, err := pty.Open()
	if err != nil {
		return nil, nil
	}
	if mode, err := unix.IoctlGetTermios(int(writer.Fd()), getTermios); err == nil {
		mode.Oflag &^= unix.OPOST
		unix.IoctlSetTermios(int(writer.Fd()), setTermios, mode)
	}
	return reader, writer
}

// command runs a program with everything it writes private (umask 077), as the script, which set
// it for itself and its children.
func command(path string, args ...string) *exec.Cmd {
	return exec.Command("/bin/sh", append([]string{"-c", `umask 077 && exec "$0" "$@"`, path}, args...)...)
}

// start starts the command with its output (stdout and stderr) into a terminal where there is one,
// else a pipe; it gives the reader of that output.
func start(cmd *exec.Cmd) (io.ReadCloser, error) {
	reader, writer := terminal()
	if reader == nil {
		var err error
		if reader, writer, err = os.Pipe(); err != nil {
			return nil, err
		}
	}
	cmd.Stdin = nil // /dev/null
	cmd.Stdout, cmd.Stderr = writer, writer
	err := cmd.Start()
	writer.Close()
	if err != nil {
		reader.Close()
		return nil, err
	}
	return reader, nil
}
