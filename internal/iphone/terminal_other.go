//go:build !linux && !darwin

package iphone

import (
	"io"
	"os"
	"os/exec"
)

func command(path string, args ...string) *exec.Cmd { return exec.Command(path, args...) }

// start starts the command with its output (stdout and stderr) into a pipe (no terminal here).
func start(cmd *exec.Cmd) (io.ReadCloser, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = writer, writer
	err = cmd.Start()
	writer.Close()
	if err != nil {
		reader.Close()
		return nil, err
	}
	return reader, nil
}
