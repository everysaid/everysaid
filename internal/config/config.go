// Package config says where Everysaid keeps its files, and holds the settings of `config.toml` in
// the config folder.
//
// The folders follow the platform's conventions: on Linux the XDG ones, `~/.local/share/everysaid`
// for what cannot be made again (the archive, the review decisions), `~/.cache/everysaid` for what
// can, `~/.config/everysaid` for the settings and the secrets. The environment variables
// EVERYSAID_DATA, EVERYSAID_CACHE, EVERYSAID_CONFIG and EVERYSAID_STATE move them (a demo or a test
// archive is kept wholly apart this way). Every setting has a general default, so the file is
// needed only to change one:
//
//	[owner]
//	numbers = ["+15551234567"]      # the owner's own numbers, in international form
//	region = "US"                   # for numbers written without a country code
//	timezone = "America/New_York"   # default: the system's
//
//	[iphone]
//	udid = "..."                    # default: the only backup in backup_root, else the only phone on the cable
//	backup_root = "..."             # default: <data>/iphone-backup
//
//	[android]
//	export = "..."                  # android-export's folder; default: <data>/android
//
//	[viber]
//	desktop_export = "..."          # a decrypted Viber Desktop database (viber-desktop-export.cpp)
//
//	[whatsapp]
//	bridge = "..."                  # the WhatsApp store folder (messages.db, whatsapp.db)
//
//	[telegram]
//	media = true                    # false: telegram-sync --media downloads nothing
//	no_media = [-100123]            # chats whose media are not wanted
//
//	[immich]
//	url = "http://host:2283"
//	make = "Everysaid"              # the camera make written into uploaded files
//
//	[media]
//	store = "..."                   # the archive's own media files; default: <data> (media/<ab>/...)
//
//	[server]
//	origin = "https://everysaid.example.org"   # the address the app is reached at
//	host = "127.0.0.1"
//	port = 8520
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	_ "time/tzdata" // Windows has no zone database of its own

	"github.com/BurntSushi/toml"
)

const App = "everysaid"

var (
	// Keyring is the keyring's service name for secrets; a demo or a test sets its own, so that it
	// never sees the user's real secrets (and never connects to their accounts).
	Keyring string

	Data, Cache, Config, State, Logs string
	ConfigFile                       string

	settings map[string]any

	OwnNumbers   []string
	Region       string
	TimezoneName string
	Timezone     *time.Location

	IphoneBackupRoot string
	AndroidExport    string
	ViberDesktop     string
	WhatsappBridge   string

	ImmichURL  string
	ImmichMake string
	OllamaURL  string

	MediaStore string

	ServerOrigin string
	ServerHost   string
	ServerPort   int
)

func init() { Load() }

// Load reads the environment and config.toml again (tests and the demo move the folders).
func Load() {
	Keyring = env("EVERYSAID_KEYRING", App)
	Data = env("EVERYSAID_DATA", userDir("data"))
	Cache = env("EVERYSAID_CACHE", userDir("cache"))
	Config = env("EVERYSAID_CONFIG", userDir("config"))
	State = env("EVERYSAID_STATE", userDir("state"))
	Logs = filepath.Join(State, "logs")
	ConfigFile = filepath.Join(Config, "config.toml")

	settings = map[string]any{}
	if b, err := os.ReadFile(ConfigFile); err == nil {
		if _, err := toml.Decode(string(b), &settings); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", ConfigFile, err)
		}
	}

	OwnNumbers = Strings("owner", "numbers")
	Region = String("owner", "region", "")
	TimezoneName, Timezone = zone(String("owner", "timezone", ""))

	IphoneBackupRoot = Path("iphone", "backup_root", filepath.Join(Data, "iphone-backup"))
	AndroidExport = Path("android", "export", filepath.Join(Data, "android"))
	ViberDesktop = Path("viber", "desktop_export", "")
	WhatsappBridge = Path("whatsapp", "bridge", "")

	ImmichURL = strings.TrimRight(String("immich", "url", ""), "/")
	ImmichMake = String("immich", "make", "Everysaid")
	OllamaURL = strings.TrimRight(String("ollama", "url", "http://localhost:11434"), "/")

	MediaStore = Path("media", "store", Data)

	ServerPort = Int("server", "port", 8520)
	ServerOrigin = strings.TrimRight(String("server", "origin", fmt.Sprintf("http://localhost:%d", ServerPort)), "/")
	ServerHost = String("server", "host", "127.0.0.1")
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// userDir is the platform's folder of a kind (data, cache, config, state) for the app.
func userDir(kind string) string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		if kind == "cache" {
			return filepath.Join(home, "Library", "Caches", App)
		}
		return filepath.Join(home, "Library", "Application Support", App)
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		if kind == "cache" {
			return filepath.Join(base, App, "Cache")
		}
		return filepath.Join(base, App)
	}
	xdg := map[string][2]string{
		"data":   {"XDG_DATA_HOME", ".local/share"},
		"cache":  {"XDG_CACHE_HOME", ".cache"},
		"config": {"XDG_CONFIG_HOME", ".config"},
		"state":  {"XDG_STATE_HOME", ".local/state"},
	}[kind]
	base := os.Getenv(xdg[0])
	if base == "" || !filepath.IsAbs(base) {
		base = filepath.Join(home, xdg[1])
	}
	return filepath.Join(base, App)
}

// Get is a setting of config.toml, or nil.
func Get(section, key string) any {
	if s, ok := settings[section].(map[string]any); ok {
		return s[key]
	}
	return nil
}

func String(section, key, def string) string {
	if v, ok := Get(section, key).(string); ok && v != "" {
		return v
	}
	return def
}

func Int(section, key string, def int) int {
	switch v := Get(section, key).(type) {
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return def
}

func Bool(section, key string, def bool) bool {
	if v, ok := Get(section, key).(bool); ok {
		return v
	}
	return def
}

func Strings(section, key string) []string {
	var out []string
	if vs, ok := Get(section, key).([]any); ok {
		for _, v := range vs {
			out = append(out, fmt.Sprint(v))
		}
	}
	return out
}

// Path is a setting naming a path, with ~ made whole.
func Path(section, key, def string) string {
	v := String(section, key, "")
	if v == "" {
		return def
	}
	return ExpandUser(v)
}

func ExpandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, _ := os.UserHomeDir()
		return home + p[1:]
	}
	return p
}

// systemZone is the system's zone names, most specific first: TZ (also as a path,
// TZ=:/etc/localtime), the zone /etc/localtime links to, /etc/timezone (Debian).
func systemZone() []string {
	var names []string
	for _, n := range []string{strings.TrimPrefix(os.Getenv("TZ"), ":"), "/etc/localtime"} {
		if filepath.IsAbs(n) { // a zoneinfo file: its name is the part after .../zoneinfo/
			if real, err := filepath.EvalSymlinks(n); err == nil {
				n = real
			}
			if _, after, ok := strings.Cut(n, "/zoneinfo/"); ok {
				n = after
			} else {
				n = ""
			}
		}
		names = append(names, n)
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		names = append(names, strings.TrimSpace(string(b)))
	}
	var out []string
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// zone is (name, location): the one set, else the system's, else the process's local time.
// Never stops: a name the zone database does not know is passed over (a wrong one in the config
// is said on stderr).
func zone(name string) (string, *time.Location) {
	tries := systemZone()
	if name != "" {
		tries = append([]string{name}, tries...)
	}
	for _, n := range tries {
		if loc, err := time.LoadLocation(n); err == nil && n != "Local" {
			return n, loc
		}
		if n == name {
			fmt.Fprintf(os.Stderr, "unknown time zone [owner] timezone = %q in %s; the system's is used.\n", name, ConfigFile)
		}
	}
	return "", time.Local
}

// IphoneUDID is the iPhone's UDID: the one set, else the only backup in the backup folder, else
// the only phone on the cable; "" and how many were found otherwise.
func IphoneUDID(root string) (string, int) {
	if udid := String("iphone", "udid", ""); udid != "" {
		return udid, 1
	}
	if root == "" {
		root = IphoneBackupRoot
	}
	var found []string
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(root, e.Name(), "Manifest.plist")); err == nil {
				found = append(found, e.Name())
			}
		}
	}
	if len(found) == 0 {
		if out, err := exec.Command("idevice_id", "-l").Output(); err == nil {
			found = strings.Fields(string(out))
		}
	}
	if len(found) == 1 {
		return found[0], 1
	}
	return "", len(found)
}
