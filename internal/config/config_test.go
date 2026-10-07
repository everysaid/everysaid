package config

import "testing"

// The folders are platformdirs' (4.12, as the Python finds them) on every system: on macOS too
// an XDG variable set to an absolute path wins, a relative one is passed over, spaces around it
// do not count; Windows has only its own.
func TestFoldersAsPlatformdirs(t *testing.T) {
	env := map[string]string{"XDG_DATA_HOME": "/x/data", "XDG_CACHE_HOME": "  /x/cache ", "XDG_CONFIG_HOME": "rel/config",
		"XDG_STATE_HOME": "/x/state/", "LOCALAPPDATA": `C:\Users\u\AppData\Local`}
	getenv := func(k string) string { return env[k] }
	none := func(string) string { return "" }
	for _, c := range []struct {
		goos, kind string
		set        bool
		want       string
	}{
		{"linux", "data", false, "/home/u/.local/share/everysaid"},
		{"linux", "cache", false, "/home/u/.cache/everysaid"},
		{"linux", "config", false, "/home/u/.config/everysaid"},
		{"linux", "state", false, "/home/u/.local/state/everysaid"},
		{"linux", "data", true, "/x/data/everysaid"},
		{"linux", "cache", true, "/x/cache/everysaid"},
		{"linux", "config", true, "/home/u/.config/everysaid"},
		{"linux", "state", true, "/x/state/everysaid"},
		{"darwin", "data", false, "/home/u/Library/Application Support/everysaid"},
		{"darwin", "config", false, "/home/u/Library/Application Support/everysaid"},
		{"darwin", "state", false, "/home/u/Library/Application Support/everysaid"},
		{"darwin", "cache", false, "/home/u/Library/Caches/everysaid"},
		{"darwin", "data", true, "/x/data/everysaid"},
		{"darwin", "cache", true, "/x/cache/everysaid"},
		{"darwin", "config", true, "/home/u/Library/Application Support/everysaid"},
		{"windows", "data", true, `C:\Users\u\AppData\Local/everysaid`},
		{"windows", "cache", true, `C:\Users\u\AppData\Local/everysaid/Cache`},
	} {
		g := none
		if c.set {
			g = getenv
		}
		if got := platformDir(c.goos, c.kind, "/home/u", g); got != c.want {
			t.Errorf("%s %s (XDG set: %v): %s, not %s", c.goos, c.kind, c.set, got, c.want)
		}
	}
}

// fakeVault is the Credential Manager's generic credentials in memory.
type fakeVault map[string]struct {
	user string
	blob []byte
}

func (f fakeVault) read(t string) (string, []byte, bool, error) {
	c, ok := f[t]
	return c.user, c.blob, ok, nil
}
func (f fakeVault) write(t, user string, blob []byte) error {
	f[t] = struct {
		user string
		blob []byte
	}{user, blob}
	return nil
}
func (f fakeVault) remove(t string) error { delete(f, t); return nil }

// On Windows a secret is where Python's keyring keeps it and reads it: the target the service
// (UserName the secret's name), the one there before moved to name@service, in UTF-16.
func TestWindowsSecretsAsPythonsKeyring(t *testing.T) {
	store := fakeVault{}
	// as the Python left them: two secrets of one service
	store.write("everysaid", "immich-key", toUTF16("clé-1 😀"))
	store.write("telegram@everysaid", "telegram", []byte("utf-8 written"))
	v := winVault{store}
	for name, want := range map[string]string{"immich-key": "clé-1 😀", "telegram": "utf-8 written"} {
		if got, err := v.get("everysaid", name); err != nil || got != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	if _, err := v.get("everysaid", "missing"); err == nil {
		t.Fatal("a missing secret was found")
	}
	if err := v.set("everysaid", "session", "s3"); err != nil {
		t.Fatal(err)
	}
	if c := store["everysaid"]; c.user != "session" || string(c.blob) != string(toUTF16("s3")) {
		t.Fatalf("the newest is not under the service: %+v", c)
	}
	if c := store["immich-key@everysaid"]; c.user != "immich-key" || fromUTF16(c.blob) != "clé-1 😀" {
		t.Fatalf("the one there was not moved: %+v", c)
	}
	for _, name := range []string{"immich-key", "telegram", "session"} {
		if _, err := v.get("everysaid", name); err != nil {
			t.Fatalf("%s lost: %v", name, err)
		}
	}
	if err := v.delete("everysaid", "immich-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.get("everysaid", "immich-key"); err == nil {
		t.Fatal("not deleted")
	}
	if err := v.delete("everysaid", "immich-key"); err == nil {
		t.Fatal("a second delete found it")
	}
}

// On macOS the secret is read from `security -g`, printable or in hex.
func TestKeychainAnswers(t *testing.T) {
	for out, want := range map[string]string{
		"keychain: \"/x\"\nclass: \"genp\"\npassword: \"plain text\"\n":     "plain text",
		"password: 0x636CC3A90A78  \"cl\\303\\251...\"\nkeychain: \"/x\"\n": "clé\nx",
		"password: \n": "",
	} {
		got, err := securityPassword(out)
		if err != nil || got != want {
			t.Errorf("%q: %q %v", out, got, err)
		}
	}
	if _, err := securityPassword("something else\n"); err == nil {
		t.Error("no password taken for one")
	}
}
