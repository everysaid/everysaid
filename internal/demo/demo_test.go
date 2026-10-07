package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/pyjson"
)

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// The pictures are Pillow's and libjpeg-turbo's, byte for byte (the digests of the Python's files).
func TestPicturesAsPillowMakesThem(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		seed int64
		want string
	}{
		{7001, "09cc4760f009087a93187fb6dea960a8cc165d393d508ee0d05f2827747fdf0a"},
		{7002, "ac3ff8e60ba1e095b203e1fc564af07dcdcd4b100559ad988fa986bd1ff6e682"},
		{7003, "6145343d089acab83c66a7116322da0e602ce3be839160e060716cb8ae2c7dcd"},
	} {
		f := filepath.Join(dir, fmt.Sprintf("p%d.jpg", c.seed))
		if err := picture(f, c.seed, "05/03/2024"); err != nil {
			t.Fatal(err)
		}
		if got := fileSHA(t, f); got != c.want {
			t.Errorf("picture %d: %s, want %s", c.seed, got, c.want)
		}
	}
	for _, c := range []struct {
		seed     int64
		initials string
		want     string
	}{
		{7, "ΕΠ", "4345035f4fb3b5034806df04efd0084fa89f6a9e00097eb62c60340232e9ce63"},
		{9, "ΆΓ", "b0efd014b155ee5b12614f9e8a72ee55443df1fa4ce527a4e6e6ac0fe40bb7bf"},
	} {
		f := filepath.Join(dir, fmt.Sprintf("a%d.jpg", c.seed))
		if err := avatar(f, c.seed, c.initials); err != nil {
			t.Fatal(err)
		}
		if got := fileSHA(t, f); got != c.want {
			t.Errorf("avatar %d: %s, want %s", c.seed, got, c.want)
		}
	}
}

func value(v any) string {
	switch x := v.(type) {
	case nil:
		return "N"
	case float64:
		return "f" + pyjson.Dumps(x, true)
	case int64:
		return fmt.Sprintf("i%d", x)
	case []byte:
		return "b" + hex.EncodeToString(x)
	case string:
		return "s" + x
	}
	panic(fmt.Sprintf("a value of type %T", v))
}

// The demo archive is the Python's, table by table and row by row, and so are its files: checked
// against testdata/digest.json, made by testdata/digest.py from the Python at the same fixed hour.
func TestSameArchiveAsPython(t *testing.T) {
	data, err := os.ReadFile("testdata/digest.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Now    int64
		Tables map[string]struct {
			Rows   int
			SHA256 string
		}
		Files map[string]string
	}
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	local := time.Local
	time.Local = time.UTC // as the digests were made (the pictures show their day there)
	defer func() { time.Local = local }()
	Now = func() time.Time { return time.Unix(want.Now, 0) }
	defer func() { Now = time.Now }()

	dir, path := Build(t)
	d, err := db.ReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	skip := map[string]bool{"handle_name.first_seen": true, "handle_name.last_seen": true, "chat_state.set_at": true,
		"plugin_instance.settings": true}
	tables := db.Strs(d, "SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if len(tables) != len(want.Tables) {
		t.Errorf("%d tables, want %d", len(tables), len(want.Tables))
	}
	for _, table := range tables {
		cols := db.Strs(d, fmt.Sprintf(`SELECT name FROM pragma_table_info('%s')`, table))
		rows, err := d.Query(fmt.Sprintf(`SELECT * FROM "%s"`, table))
		if err != nil {
			t.Fatal(err)
		}
		h, n := sha256.New(), 0
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var parts []string
			for i, c := range cols {
				if !skip[table+"."+c] {
					parts = append(parts, value(vals[i]))
				}
			}
			h.Write([]byte(strings.Join(parts, "\x1f") + "\x1e"))
			n++
		}
		rows.Close()
		w := want.Tables[table]
		if got := hex.EncodeToString(h.Sum(nil)); n != w.Rows || got != w.SHA256 {
			t.Errorf("table %s: %d rows (%s), want %d (%s)", table, n, got[:12], w.Rows, w.SHA256[:12])
		}
	}
	var settings string
	d.QueryRow("SELECT settings FROM plugin_instance WHERE plugin = 'folder'").Scan(&settings)
	if want := pyjson.Dumps(pyjson.OrderedMap{{Key: "path", Value: filepath.Join(config.Data, "library")}}, true); settings != want {
		t.Errorf("the library's settings: %s, want %s", settings, want)
	}

	files := map[string]string{}
	for _, base := range []string{"data", "cache"} {
		filepath.WalkDir(filepath.Join(dir, base), func(p string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() && !strings.HasPrefix(e.Name(), "archive.db") {
				rel, _ := filepath.Rel(dir, p)
				files[filepath.ToSlash(rel)] = fileSHA(t, p)
			}
			return nil
		})
	}
	var missing, wrong []string
	for name, sum := range want.Files {
		if got, ok := files[name]; !ok {
			missing = append(missing, name)
		} else if got != sum {
			wrong = append(wrong, name)
		}
		delete(files, name)
	}
	sort.Strings(missing)
	sort.Strings(wrong)
	if len(missing) > 0 || len(wrong) > 0 || len(files) > 0 {
		t.Errorf("files: %d missing (%v), %d different (%v), %d more", len(missing), missing, len(wrong), wrong, len(files))
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config", "config.toml"))
	if string(cfg) != "[owner]\nnumbers = [\"+15550000000\"]\nregion = \"US\"\nname = \"Demo\"\n\n[server]\nport = 8530\n" {
		t.Errorf("config.toml: %q", cfg)
	}
}
