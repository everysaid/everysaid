// Package sources holds the source plugins that wrap the phones' extraction and the importers:
// iphone-backup, android-adb, carrier-notices, im-logs (viber-desktop: internal/viber). Ports those classes of
// everysaid/plugins/sources.py (the live ones, WhatsApp and Telegram, have packages of their own).
package sources

// Ports IphoneBackup of everysaid/plugins/sources.py. Where the Python ran scripts/iphone-sync.py
// with the password on its stdin, the sync runs here, in the process (internal/iphone).

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/errs"
	"everysaid/internal/importers"
	"everysaid/internal/iphone"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

type M = plugins.M

// IphoneBackup is the `iphone-backup` source.
type IphoneBackup struct{}

func init() {
	plugins.Register(IphoneBackup{})
	plugins.Register(AndroidAdb{})
	plugins.Register(CarrierNotices{})
	plugins.Register(ImLogs{})
}

var iphoneServices = []string{"sms", "imessage", "rcs", "phone", "facetime", "whatsapp", "viber"}

func (IphoneBackup) Info() *plugins.Info {
	return &plugins.Info{
		ID: "iphone-backup", Name: "iPhone (encrypted backup)", Kind: "source",
		Services:    iphoneServices,
		ServiceInfo: sourcekit.Looks(iphoneServices...),
		NameWeights: []plugins.Weight{{Key: "whatsapp/book", Weight: 80}, {Key: "whatsapp/chat", Weight: 50},
			{Key: "whatsapp/profile", Weight: 30}},
		StateWeights: map[string]int{"muted": 60},
		Description: "Messages, iMessage, calls, WhatsApp and Viber from an encrypted iPhone backup, made over " +
			"the cable with libimobiledevice. The backup must be encrypted: only then does it hold calls.",
		Platforms: []string{"linux", "darwin"},
		Needs:     []string{"the phone on a USB cable", "libimobiledevice (idevicebackup2)", "the backup password"},
		Settings: []plugins.Setting{
			{Key: "backup", Label: "A new backup before importing", Type: "bool", Default: true,
				Help: "Off: only decrypt the backup already there"},
			{Key: "backup_root", Label: "Backup folder", Type: "path", Default: config.IphoneBackupRoot,
				Help: "Where the encrypted backup is kept (a folder for each phone inside)"},
			{Key: "udid", Label: "UDID", Help: "Only with more than one iPhone; otherwise it is found",
				Pattern: `[0-9A-Fa-f]{8}-?[0-9A-Fa-f]{16}|[0-9A-Fa-f]{40}`},
			{Key: "password", Label: "The backup password", Type: "select", Default: "ask",
				Options: []plugins.Option{{Value: "ask", Label: "Asked for at each import, kept nowhere"},
					{Value: "keyring", Label: "Kept in the system's keyring"}},
				Keeps: map[string]plugins.Keep{"keyring": {Key: "backup_password", Label: "The backup password"}},
				Help:  "Going back to asking takes it out of the keyring"},
		},
	}
}

func (IphoneBackup) Check(c *plugins.Context) (bool, string) {
	if c.Str("password") == "keyring" && c.Secret("backup_password") == "" {
		return false, "missing: The backup password"
	}
	return true, "ready"
}

func (IphoneBackup) Asks(c *plugins.Context) []plugins.Ask {
	if c.Str("password") == "keyring" {
		return nil
	}
	return []plugins.Ask{{Key: "backup_password", Label: "The backup password"}}
}

func backupRoot(c *plugins.Context) string {
	if r := c.Str("backup_root"); r != "" {
		return config.ExpandUser(r)
	}
	return config.IphoneBackupRoot
}

// backupDir is the folder of this phone's backup, "" when it is not known yet.
func backupDir(c *plugins.Context) string {
	root := backupRoot(c)
	udid := c.Str("udid")
	if udid == "" {
		var found []string
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(root, e.Name(), "Manifest.plist")); err == nil {
				found = append(found, e.Name())
			}
		}
		if len(found) != 1 {
			return ""
		}
		udid = found[0]
	}
	return filepath.Join(root, udid)
}

func (IphoneBackup) InfoFacts(c *plugins.Context) []plugins.Fact {
	where := backupDir(c)
	var st fs.FileInfo
	var err error = fs.ErrNotExist
	if where != "" {
		st, err = os.Stat(filepath.Join(where, "Manifest.db"))
	}
	if err != nil {
		return []plugins.Fact{{Label: "Backup", Value: backupRoot(c)}, {Label: "Last backup", Value: "none yet"}}
	}
	return []plugins.Fact{{Label: "Backup", Value: where},
		{Label: "Last backup", Value: st.ModTime().Local().Format("2006-01-02 15:04")},
		{Label: "Size", Value: size(where, st.ModTime())}}
}

var sizes = struct {
	sync.Mutex
	of map[string]sizeOf
}{of: map[string]sizeOf{}}

type sizeOf struct {
	stamp time.Time
	words string
}

// size is a backup's size in words (worked out again only when its manifest changes).
func size(folder string, stamp time.Time) string {
	sizes.Lock()
	defer sizes.Unlock()
	if s, ok := sizes.of[folder]; ok && s.stamp.Equal(stamp) {
		return s.words
	}
	var total int64
	filepath.WalkDir(folder, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() { // links are not followed
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	words := fmt.Sprintf("%.0f MB", float64(total)/1e6)
	if total >= 1e9 {
		words = fmt.Sprintf("%.1f GB", float64(total)/1e9)
	}
	sizes.of[folder] = sizeOf{stamp, words}
	return words
}

// RunImport makes a new backup over the cable (unless turned off), takes the databases out of it,
// then imports them.
func (IphoneBackup) RunImport(c *plugins.Context) error {
	password := c.Given["backup_password"]
	if password == "" {
		password = c.Secret("backup_password")
	}
	if password == "" {
		return errs.Plugin("The backup password is needed", 0)
	}
	raw := sourcekit.Terminal(c)
	err := iphone.Sync(iphone.SyncOptions{
		BackupRoot: backupRoot(c),
		UDID:       c.Str("udid"),
		NoBackup:   !c.Bool("backup"),
		Archive:    c.Store().Path,
		Password:   []byte(password),
		Say:        sourcekit.Say(c),
		Raw:        raw,
	})
	raw.Close()
	if err != nil {
		return sourcekit.UserError(err)
	}
	_, _, err = sourcekit.RunImporters(c, iphoneSteps())
	return err
}

func iphoneSteps() []sourcekit.Step {
	return []sourcekit.Step{
		{Label: "SMS, iMessage", Run: func(a *archive.Archive, out func(string)) error {
			return importers.SMS(a, out, importers.SMSOptions{})
		}},
		{Label: "calls", Run: func(a *archive.Archive, out func(string)) error {
			return importers.Calls(a, out, importers.CallsOptions{})
		}},
		{Label: "Viber", Run: func(a *archive.Archive, out func(string)) error {
			return importers.Viber(a, out, importers.ViberOptions{})
		}},
		// not the WhatsApp bridge's databases, even where config.toml names them: those are the
		// bridge source's, another instance
		{Label: "WhatsApp", Run: func(a *archive.Archive, out func(string)) error {
			_, err := importers.WhatsApp(a, out, importers.WhatsAppOptions{NoBridge: true, NoStore: true})
			return err
		}},
		{Label: "WhatsApp and Viber calls", Run: func(a *archive.Archive, out func(string)) error {
			return importers.VoIP(a, out, importers.VoIPOptions{NoBridge: true})
		}},
		{Label: "files", Run: func(a *archive.Archive, out func(string)) error {
			return importers.Media(a, out, importers.Phones...)
		}},
	}
}
