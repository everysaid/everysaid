package viber

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Viber's home keeps its state; what it makes again, and its downloads, are links into the cache.
// A folder Viber made before the links has its files moved there; the temporary caches are emptied
// at each start, the avatars kept.
func TestTheJail(t *testing.T) {
	c := instance(t, nil)
	h := Home(c)
	account := filepath.Join(h, ".ViberPC", "306900000000")
	for _, d := range []string{filepath.Join(account, "Thumbnails"), filepath.Join(account, "Avatars"), filepath.Join(h, "Documents", "ViberDownloads")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(account, "viber.db"), []byte("db"), 0o600)
	os.WriteFile(filepath.Join(account, "Avatars", "0-04-05-abc"), []byte("face"), 0o600)
	os.WriteFile(filepath.Join(account, "Thumbnails", "t.jpg"), []byte("thumb"), 0o600)
	os.WriteFile(filepath.Join(h, "Documents", "ViberDownloads", "a.jpg"), []byte("file"), 0o600)
	if err := prepareJail(c); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(account, "viber.db")); string(b) != "db" {
		t.Fatal("the account's database moved")
	}
	for path, want := range map[string]string{
		filepath.Join(h, "Documents", "ViberDownloads"): Downloads(c),
		filepath.Join(account, "Avatars"):              filepath.Join(scratch(c), "306900000000", "Avatars"),
		filepath.Join(account, "Thumbnails"):           filepath.Join(scratch(c), "306900000000", "Thumbnails"),
		filepath.Join(h, ".ViberPC", "data"):           filepath.Join(scratch(c), "data"),
	} {
		if got, err := os.Readlink(path); err != nil || got != want {
			t.Errorf("%s: %q %v", path, got, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(Downloads(c), "a.jpg")); string(b) != "file" {
		t.Fatal("the download was not moved to the cache")
	}
	if b, _ := os.ReadFile(filepath.Join(account, "Avatars", "0-04-05-abc")); string(b) != "face" {
		t.Fatal("the avatar was lost")
	}
	// again (a second start): the thumbnails emptied, the avatars kept, the links as they were
	if err := prepareJail(c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(account, "Thumbnails", "t.jpg")); !os.IsNotExist(err) {
		t.Fatalf("thumbnails kept: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(account, "Avatars", "0-04-05-abc")); string(b) != "face" {
		t.Fatal("the avatar was emptied")
	}
	env := strings.Join(jailEnv(c), " ")
	if !strings.Contains(env, "HOME="+h) || !strings.Contains(env, "XDG_CACHE_HOME="+scratch(c)) {
		t.Fatalf("env %s", env)
	}
}
