// Package phones holds what the extraction from phones (internal/iphone, internal/android) shares:
// the external tools (scripts/common.py: tool, run), the way a run says its lines, its failures,
// a small argparse for the command line, and the reading of a tool's terminal output that
// everysaid/plugins/sources.py run_script does.
package phones

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"everysaid/internal/i18n"
)

// Say is where a run's lines go: English with {params}, said in the reader's language (a plugin's
// Context.Log, or the command line through Printer).
type Say func(text string, params map[string]any)

// Printer says lines on the command line, in its language.
func Printer(out io.Writer) Say {
	return func(text string, params map[string]any) { fmt.Fprintln(out, i18n.Say(text, params)) }
}

// Failure is the way out of a script (Python's sys.exit): Text in English with {params}, said in
// the user's language; an empty Text only ends with Code (sys.exit(1)).
type Failure struct {
	Text   string
	Params map[string]any
	Code   int
}

func (f *Failure) Error() string {
	if f.Text == "" {
		return fmt.Sprintf("exit status %d", f.Code)
	}
	return i18n.Format(f.Text, f.Params)
}

// In says the failure in a language.
func (f *Failure) In(lang string) string { return i18n.T(f.Text, lang, f.Params) }

// Fail is a Failure with a text.
func Fail(text string, params map[string]any) *Failure {
	return &Failure{Text: text, Params: params, Code: 1}
}

// Exit is how the command line ends after a script's main: its message on stderr in the
// command line's language, and the exit code (0 for nil).
func Exit(err error) int {
	if err == nil {
		return 0
	}
	var f *Failure
	if errors.As(err, &f) {
		if f.Text != "" {
			fmt.Fprintln(os.Stderr, i18n.Say(f.Text, f.Params))
		}
		return f.Code
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}

// tools: the package that provides each external program.
var tools = map[string]string{"exiftool": "ExifTool, https://exiftool.org", "ffmpeg": "FFmpeg", "ffprobe": "FFmpeg",
	"idevicebackup2": "libimobiledevice", "idevice_id": "libimobiledevice", "adb": "Android platform-tools"}

// Tool is the full path of an external program, or the failure saying which package provides it.
// On Windows LookPath also tries PATHEXT (exiftool.exe).
func Tool(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		pkg := tools[name]
		if pkg == "" {
			pkg = name
		}
		return "", Fail("{name} ({package}) is needed and was not found in PATH.", map[string]any{"name": name, "package": pkg})
	}
	return path, nil
}
