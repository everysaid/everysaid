package viber

// Viber Desktop runs in a home of its own, so that nothing of it is left around the user's: HOME and
// the XDG folders point into <data>/viber/<instance>/home, which keeps only Viber's state (its
// account, config.db and the account's databases: what lets it start again without the phone, in
// the backups). What Viber makes again by itself (its thumbnails, caches, stickers) and the files it
// downloads are links from there into <cache>/viber/<instance>/ (out of the backups); the downloads
// are taken into the archive's store as they are linked to their messages, and the temporary caches
// are emptied before each start. The avatars stay (a source of the people's pictures).

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"everysaid/internal/config"
	"everysaid/internal/plugins"
)

// Home is Viber Desktop's home: its state, kept.
func Home(c *plugins.Context) string {
	return filepath.Join(config.Data, "viber", fmt.Sprint(c.ID), "home")
}

// scratch is where what Viber makes again goes: out of the backups.
func scratch(c *plugins.Context) string { return filepath.Join(dir(c), "scratch") }

// Downloads is the folder Viber downloads the messages' files into (a link from its home).
func Downloads(c *plugins.Context) string { return filepath.Join(dir(c), "downloads") }

// remade are the folders of an account Viber makes again; emptied are those of them emptied before
// each start (the others it takes time to fetch again, and they stay).
var (
	remade  = []string{"Avatars", "Backgrounds", "Icons", "QmlUrlCache", "QmlWebCache", "Temporary", "Thumbnails"}
	emptied = map[string]bool{"QmlUrlCache": true, "QmlWebCache": true, "Temporary": true, "Thumbnails": true}
)

// jailed are the variables jailEnv sets (the others of the user's, XDG_DATA_DIRS, XDG_RUNTIME_DIR and
// the rest, stay: Qt and D-Bus need them).
var jailed = map[string]bool{"HOME": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
	"XDG_STATE_HOME": true}

// jailEnv is the environment that keeps Viber in its home.
func jailEnv(c *plugins.Context) []string {
	h := Home(c)
	return []string{"HOME=" + h, "XDG_CONFIG_HOME=" + filepath.Join(h, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(h, ".local", "share"), "XDG_CACHE_HOME=" + filepath.Join(scratch(c), "xdg-cache"),
		"XDG_STATE_HOME=" + filepath.Join(h, ".local", "state")}
}

// prepareJail makes Viber's home and its links before a start (Viber is not running).
func prepareJail(c *plugins.Context) error {
	h := Home(c)
	for _, d := range []string{h, scratch(c), Downloads(c), filepath.Join(h, ".ViberPC"), filepath.Join(h, "Documents")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := linkDir(filepath.Join(h, "Documents", "ViberDownloads"), Downloads(c)); err != nil {
		return err
	}
	// the stickers and the emoji's words, of all the accounts
	if err := linkDir(filepath.Join(h, ".ViberPC", "data"), filepath.Join(scratch(c), "data")); err != nil {
		return err
	}
	accounts, err := os.ReadDir(filepath.Join(h, ".ViberPC"))
	if err != nil {
		return err
	}
	for _, a := range accounts {
		if !a.IsDir() || !digits(a.Name()) {
			continue // an account's folder is its number
		}
		for _, name := range remade {
			to := filepath.Join(scratch(c), a.Name(), name)
			if emptied[name] {
				os.RemoveAll(to) // Viber is not running: what it makes again
			}
			if err := linkDir(filepath.Join(h, ".ViberPC", a.Name(), name), to); err != nil {
				return err
			}
		}
	}
	return nil
}

// linkDir makes path a link to the folder to (made if need be). A folder already at path (Viber's,
// from before the link) has its files moved to the target first, those already there kept.
func linkDir(path, to string) error {
	if err := os.MkdirAll(to, 0o700); err != nil {
		return err
	}
	if target, err := os.Readlink(path); err == nil {
		if target == to {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if st, err := os.Lstat(path); err == nil && st.IsDir() {
		if err := moveInto(path, to); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	} else if err == nil {
		return fmt.Errorf("%s: not a folder", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Symlink(to, path)
}

// moveInto moves a folder's files into another (copied where they are on another file system), the
// target's own files kept.
func moveInto(from, to string) error {
	return filepath.WalkDir(from, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dest := filepath.Join(to, rel)
		if e.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		if _, err := os.Lstat(dest); err == nil {
			return nil
		}
		if err := os.Rename(p, dest); err == nil {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, b, 0o600)
	})
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
