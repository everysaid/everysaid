package ferdium

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"everysaid/internal/webui"
)

// The recipe: its files, with the app's address; written into a folder of development recipes
// (again over itself), and as a zip of its folder.
func TestRecipe(t *testing.T) {
	if webui.FS() == nil {
		t.Skip("no interface in this build (its icon is the recipe's)")
	}
	files, err := Files("https://chat.example")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		ID     string
		Config struct {
			ServiceURL   string
			HasCustomUrl bool
		}
	}
	if err := json.Unmarshal(files["package.json"], &pkg); err != nil || pkg.ID != ID ||
		pkg.Config.ServiceURL != "https://chat.example" || !pkg.Config.HasCustomUrl {
		t.Fatalf("package.json: %v %+v", err, pkg)
	}
	if !bytes.HasPrefix(files["icon.svg"], []byte("<svg")) {
		t.Fatal("icon")
	}

	dev := t.TempDir()
	for range 2 {
		dir, err := Install(dev, "https://chat.example")
		if err != nil || dir != filepath.Join(dev, ID) {
			t.Fatal(dir, err)
		}
	}
	got, _ := os.ReadDir(filepath.Join(dev, ID))
	var names []string
	for _, e := range got {
		names = append(names, e.Name())
	}
	if want := []string{"icon.svg", "index.js", "package.json", "webview.js"}; !equal(names, want) {
		t.Fatal(names)
	}

	var b bytes.Buffer
	if err := Zip(&b, "https://chat.example"); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names = nil
	for _, f := range z.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if want := []string{"everysaid/icon.svg", "everysaid/index.js", "everysaid/package.json", "everysaid/webview.js"}; !equal(names, want) {
		t.Fatal(names)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
