// Package ferdium makes Everysaid's recipe for Ferdium (and Franz, Ferdi, whose recipes it took):
// the app as a service of theirs, with the number of unread chats on its icon. The app says that
// number in its page's title, "(3) Everysaid"; the recipe hands it to Ferdium (Ferdium.setBadge).
// Notifications need nothing of it: Ferdium shows the page's own (a browser without push).
//
// The recipe is made here, with the server's address in it: written into the apps' folders of
// development recipes (`everysaid ferdium install`), or as a zip from the server (Settings).
package ferdium

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"everysaid/internal/webui"
)

// ID is the recipe's id, and its folder's name.
const ID = "everysaid"

// version is the recipe's: raised when its files change, so the apps take it again.
const version = "1.0.0"

const index = "module.exports = Ferdium => Ferdium;\n"

const webview = `// The unread chats, as the page's title says them ("(3) Everysaid"), on the service's icon.
module.exports = Ferdium => {
  const unread = () => {
    const m = /^\((\d+)\)/.exec(document.title);
    Ferdium.setBadge(m ? Number(m[1]) : 0);
  };
  Ferdium.loop(unread);
};
`

// Files are the recipe's files, for an app at url (the address the devices use).
func Files(url string) (map[string][]byte, error) {
	icon, err := iconSVG()
	if err != nil {
		return nil, err
	}
	pkg, err := json.MarshalIndent(map[string]any{
		"id": ID, "name": "Everysaid", "version": version, "license": "AGPL-3.0-or-later",
		"config": map[string]any{
			"serviceURL": url,
			// the address made in, offered as the "hosted" one (ready to save); another, as a custom one
			"hasHostedOption":      true,
			"hasCustomUrl":         true,
			"hasNotificationSound": true,
			"hasDirectMessages":    true,
		},
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"package.json": append(pkg, '\n'),
		"index.js":     []byte(index),
		"webview.js":   []byte(webview),
		"icon.svg":     icon,
	}, nil
}

// iconSVG is the app's icon, from the interface built in (square, as the apps want it).
func iconSVG() ([]byte, error) {
	ui := webui.FS()
	if ui == nil {
		return nil, errors.New("no interface in this build: cd web && pnpm build, go generate ./internal/webui")
	}
	return fs.ReadFile(ui, "favicon.svg")
}

// Zip is the recipe as a zip of its folder (everysaid/...), to unpack into an app's folder of
// development recipes.
func Zip(w io.Writer, url string) error {
	files, err := Files(url)
	if err != nil {
		return err
	}
	z := zip.NewWriter(w)
	for _, name := range sorted(files) {
		f, err := z.Create(ID + "/" + name)
		if err != nil {
			return err
		}
		if _, err := f.Write(files[name]); err != nil {
			return err
		}
	}
	return z.Close()
}

// Apps are the apps that take these recipes, by the name of their folder of settings.
var Apps = []string{"Ferdium", "Ferdi", "Franz"}

// DevFolders are the folders of development recipes of the apps found on this machine (their
// settings folder there: ~/.config on Linux, ~/Library/Application Support on macOS, %AppData% on
// Windows).
func DevFolders() []string {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	var out []string
	for _, app := range Apps {
		if st, err := os.Stat(filepath.Join(base, app)); err == nil && st.IsDir() {
			out = append(out, filepath.Join(base, app, "recipes", "dev"))
		}
	}
	return out
}

// Install writes the recipe into a folder of development recipes (dev/everysaid), its files
// replaced; it gives the recipe's folder.
func Install(dev, url string) (string, error) {
	files, err := Files(url)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dev, ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, name := range sorted(files) {
		if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func sorted(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
