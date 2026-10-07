package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "everysaid-plugins-test")
	for k, v := range map[string]string{"EVERYSAID_DATA": "data", "EVERYSAID_CACHE": "cache",
		"EVERYSAID_CONFIG": "config", "EVERYSAID_STATE": "state"} {
		os.Setenv(k, filepath.Join(dir, v))
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test-plugins")
	config.Load()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type testHost struct{ s *core.Store }

func (h testHost) Store() *core.Store { return h.s }
func (testHost) Emit(M)               {}
func (testHost) Alert(string, string) {}

// Each line of the log file is a line the plugin said: a line ending, of any kind, starts the next
// one, and none follows the last.
func TestLogLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	s, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := NewContext(testHost{s}, Instance{ID: 7, Plugin: "none", Settings: "{}", State: "{}"})
	log := filepath.Join(t.TempDir(), "run.log")
	c.SetLogPath(log)
	c.Logf("one\n")
	c.Logf("two\r\nthree\rfour")
	c.Logf("")
	b, _ := os.ReadFile(log)
	var said []string
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		_, text, _ := strings.Cut(l[11:], " ") // after the date and the time
		said = append(said, text)
	}
	if strings.Join(said, "|") != "one|two|three|four|" {
		t.Fatalf("%q", b)
	}
}

// A setting's value is of its type: what the form or an API client sends is checked.
func TestSettingTypes(t *testing.T) {
	for _, x := range []struct {
		typ   string
		value any
		ok    bool
	}{
		{"number", float64(20), true}, {"number", "20", true}, {"number", "twenty", false}, {"number", true, false},
		{"bool", true, true}, {"bool", "yes", false}, {"bool", float64(1), false},
		{"url", "http://localhost:11434", true}, {"url", "https://cloud.example.org/dav/", true},
		{"url", "localhost:11434", false}, {"url", "ftp://host/", false}, {"url", "http://", false}, {"url", float64(1), false},
		{"text", float64(1), true}, {"number", nil, true}, {"url", "", true},
	} {
		if (Setting{Key: "k", Type: x.typ}).Valid(x.value) != x.ok {
			t.Errorf("%s %#v: %v", x.typ, x.value, !x.ok)
		}
	}
}
