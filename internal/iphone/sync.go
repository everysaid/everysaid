// Ports scripts/iphone-sync.py: back up the iPhone over the cable, then decrypt SMS, call history,
// Viber and WhatsApp into a folder.
package iphone

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/phones"
)

// files: output name, relative path, domain ("" for any), in the order they are extracted.
var files = []struct{ name, rel, domain string }{
	{"sms.db", "Library/SMS/sms.db", ""},
	{"CallHistory.storedata", "Library/CallHistoryDB/CallHistory.storedata", ""},
	{"viber.sqlite", "com.viber/database/Contacts.data", "AppDomainGroup-group.viber.share.container"},
	{"whatsapp.sqlite", "ChatStorage.sqlite", "AppDomainGroup-group.net.whatsapp.WhatsApp.shared"},
	{"whatsapp-contacts.sqlite", "ContactsV2.sqlite", "AppDomainGroup-group.net.whatsapp.WhatsApp.shared"},
	// WhatsApp's call log, kept apart from its chats (Viber's calls are in viber.sqlite, ZRECENT)
	{"whatsapp-calls.sqlite", "CallHistory.sqlite", "AppDomainGroup-group.net.whatsapp.WhatsApp.shared"},
}

// Media, copied incrementally: each file has a unique name and never changes once written; files
// the archive has already taken are not copied again (see archivedMedia).
var media = []struct {
	folder, domain string
	prefixes       []string
	strip          string // taken off the front of the output path
}{
	{"viber-media", "AppDomain-com.viber",
		[]string{"Documents/Attachments/", "Documents/FileMessages/", "Documents/VoiceMessages/"}, "Documents/"},
	{"whatsapp-media", "AppDomainGroup-group.net.whatsapp.WhatsApp.shared", []string{"Message/Media/"}, "Message/Media/"},
}

var archiveSources = map[string]string{"whatsapp-media": "iphone/whatsapp", "viber-media": "iphone/viber"}

// SyncOptions are what the app gives a sync (iphone-sync.py --password-stdin).
type SyncOptions struct {
	Out        string // default: <cache>/iphone
	BackupRoot string // default: [iphone] backup_root
	UDID       string // default: [iphone] udid, else the only one
	NoBackup   bool   // only decrypt the backup already there
	Full       bool   // a full backup instead of an incremental one
	Only       []string
	// Archive is the archive whose files are not copied again (default: <data>/archive.db); the
	// app gives the one it serves, which may be another.
	Archive string
	// Password is given for this run only (the app asks for it, or has it in the keyring): never
	// stored, never printed.
	Password []byte
	// Say takes the run's lines (Context.Log); Raw the backup tool's own output as it comes, its
	// \r of a progress bar kept (a phones.Terminal for the app). Either may be nil.
	Say phones.Say
	Raw io.Writer
}

type syncRun struct {
	SyncOptions
	backupDir string
	have      bool
}

func (o *SyncOptions) defaults() {
	if o.Out == "" {
		o.Out = filepath.Join(config.Cache, "iphone") // made again from the backup: cache
	}
	if o.BackupRoot == "" {
		o.BackupRoot = config.IphoneBackupRoot
	}
	if o.Archive == "" {
		o.Archive = filepath.Join(config.Data, "archive.db")
	}
	if o.Say == nil {
		o.Say = func(string, map[string]any) {}
	}
	if o.Raw == nil {
		o.Raw = io.Discard
	}
}

func (o *SyncOptions) resolve() (*syncRun, error) {
	o.defaults()
	for _, name := range o.Only { // a name mistyped would decrypt nothing, and say done
		if !slices.ContainsFunc(files, func(f struct{ name, rel, domain string }) bool { return f.name == name }) {
			var names []string
			for _, f := range files {
				names = append(names, f.name)
			}
			return nil, phones.Fail("Not one of the databases: {name} ({names})",
				map[string]any{"name": name, "names": strings.Join(names, ", ")})
		}
	}
	if o.UDID == "" {
		udid, n := config.IphoneUDID(o.BackupRoot)
		if udid == "" {
			return nil, phones.Fail(`No single iPhone found ({n}): write its UDID in {file}, [iphone] udid = "...".`,
				map[string]any{"n": n, "file": config.ConfigFile})
		}
		o.UDID = udid
	}
	r := &syncRun{SyncOptions: *o, backupDir: filepath.Join(o.BackupRoot, o.UDID)}
	_, err := os.Stat(filepath.Join(r.backupDir, "Manifest.plist"))
	r.have = err == nil
	return r, nil
}

var errNoBackup = phones.Fail("There is no complete backup yet (its Manifest.plist is missing).", nil)

// openBackup is Open with a backup made without a password said in the user's words, and what to
// do about it.
func openBackup(dir string, password []byte) (*Backup, error) {
	b, err := Open(dir, password)
	if errors.Is(err, ErrNotEncrypted) {
		return nil, phones.Fail("The backup is not encrypted, so it holds no calls: turn its encryption on "+
			"(idevicebackup2 encryption on) and back up again.", nil)
	}
	return b, err
}

// Sync is iphone-sync.py with the password given: a new backup (unless NoBackup), then the
// databases and the new media out of it.
func Sync(o SyncOptions) error {
	r, err := o.resolve()
	if err != nil {
		return err
	}
	if r.NoBackup && !r.have {
		return errNoBackup
	}
	var opened *Backup
	if r.have {
		if opened, err = openBackup(r.backupDir, r.Password); errors.Is(err, ErrWrongPassword) {
			return phones.Fail("Wrong backup password.", nil)
		} else if err != nil {
			return err
		}
	}
	return r.run(opened)
}

// run makes the backup (unless NoBackup), then extracts. opened: the backup already opened with
// the password (nil when there was none); used as it is when no new backup is made.
func (r *syncRun) run(opened *Backup) error {
	if !r.NoBackup {
		if opened != nil {
			opened.Close()
			opened = nil
		}
		if err := r.backup(); err != nil {
			return err
		}
	}
	b := opened
	if b == nil {
		var err error
		if b, err = openBackup(r.backupDir, r.Password); errors.Is(err, ErrWrongPassword) {
			return phones.Fail("Wrong backup password.", nil)
		} else if err != nil {
			return err
		}
	}
	defer b.Close()
	return r.extract(b)
}

func (r *syncRun) extract(b *Backup) (err error) {
	defer db.Recover(&err)
	if err := os.MkdirAll(r.Out, 0o700); err != nil {
		return err
	}
	info := func(path string) func(int64, int64) {
		return func(n, claimed int64) {
			r.Say("INFO: decrypted {n} bytes to '{path}', iOS claimed {size} bytes.",
				map[string]any{"n": n, "path": path, "size": claimed})
		}
	}
	for _, f := range files {
		if len(r.Only) > 0 && !slices.Contains(r.Only, f.name) {
			continue
		}
		dest := filepath.Join(r.Out, f.name)
		tmp := dest + ".part"
		if err := b.ExtractFile(f.rel, f.domain, tmp, info(tmp)); err != nil {
			if errors.Is(err, ErrNotFound) {
				return phones.Fail("Not in the backup: {name}", map[string]any{"name": f.name})
			}
			return err
		}
		if err := os.Chmod(tmp, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, dest); err != nil {
			return err
		}
		for _, side := range []string{"-wal", "-shm"} { // left by readers of the previous copy; they don't belong to this one
			if err := os.Remove(dest + side); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		r.Say("OK {name}", map[string]any{"name": f.name})
	}
	if len(r.Only) > 0 {
		return nil
	}

	// Media are copied when they belong to a message, and unless the archive already has them: it
	// keeps a record of every file it took (`attachment.source_path`) even after the file itself
	// has gone to the photo library or been removed, so those are not brought back.
	known, err := archivedMedia(r.Archive)
	if err != nil {
		return err
	}
	wanted, err := messageFiles(r.Out)
	if err != nil {
		return err
	}
	for _, m := range media {
		folder := filepath.Join(r.Out, m.folder)
		if err := os.MkdirAll(folder, 0o700); err != nil {
			return err
		}
		var paths []string
		for _, p := range db.Strs(b.Manifest(), "SELECT relativePath FROM Files WHERE flags = 1 AND domain = ?", m.domain) {
			for _, prefix := range m.prefixes {
				if strings.HasPrefix(p, prefix) {
					paths = append(paths, p)
					break
				}
			}
		}
		added, skipped := 0, 0
		for _, rel := range paths {
			out := strings.TrimPrefix(rel, m.strip)
			dest := filepath.Join(append([]string{folder}, strings.Split(out, "/")...)...)
			if !inside(folder, dest) { // a path of the manifest that would lead out of the folder
				continue
			}
			if _, err := os.Stat(dest); err == nil || !wanted[m.folder][out] {
				continue
			}
			if known[m.folder][out] {
				skipped++
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			if err := b.ExtractFile(rel, m.domain, dest+".part", info(dest+".part")); err != nil {
				return err
			}
			if err := os.Chmod(dest+".part", 0o600); err != nil {
				return err
			}
			if err := os.Rename(dest+".part", dest); err != nil {
				return err
			}
			added++
		}
		r.Say("OK {folder}: {new} new, {skipped} already in the archive, {total} in all",
			map[string]any{"folder": m.folder, "new": added, "skipped": skipped, "total": len(paths)})
	}
	r.Say("Done: {out}", map[string]any{"out": r.Out})
	return nil
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// messageFiles is, for each media folder, the file paths that belong to a message (what the
// archive links), read from the databases just extracted: thumbnails, favicons and link previews
// beside them are not copied.
func messageFiles(out string) (wanted map[string]map[string]bool, err error) {
	defer db.Recover(&err)
	wa, err := db.ReadOnly(filepath.Join(out, "whatsapp.sqlite"))
	if err != nil {
		return nil, err
	}
	defer wa.Close()
	vb, err := db.ReadOnly(filepath.Join(out, "viber.sqlite"))
	if err != nil {
		return nil, err
	}
	defer vb.Close()
	names := db.Strs(vb, "SELECT ZNAME FROM ZATTACHMENT WHERE ZNAME IS NOT NULL")
	wanted = map[string]map[string]bool{"whatsapp-media": {}, "viber-media": {}}
	for _, p := range db.Strs(wa, "SELECT ZMEDIALOCALPATH FROM ZWAMEDIAITEM WHERE ZMEDIALOCALPATH IS NOT NULL") {
		wanted["whatsapp-media"][strings.TrimPrefix(p, "Media/")] = true
	}
	for _, folder := range []string{"Attachments", "FileMessages", "VoiceMessages"} {
		for _, n := range names {
			wanted["viber-media"][folder+"/"+n] = true
		}
	}
	return wanted, nil
}

// archivedMedia is, for each media folder, the file paths the archive at path has already taken
// (read only).
func archivedMedia(path string) (known map[string]map[string]bool, err error) {
	known = map[string]map[string]bool{}
	if _, err := os.Stat(path); err != nil {
		return known, nil
	}
	defer db.Recover(&err)
	a, err := db.ReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	for folder, source := range archiveSources {
		known[folder] = map[string]bool{}
		for _, p := range db.Strs(a, "SELECT a.source_path FROM attachment a JOIN source s ON s.id = a.source_id WHERE s.name = ?", source) {
			known[folder][p] = true
		}
	}
	return known, nil
}

// SyncMain is iphone-sync.py's command line.
//
//	everysaid iphone-sync [-o DIR] [--no-backup] [--full]
//	everysaid iphone-sync --save-password | --move-to-keyring
//
// The password (secret `backup-password`) is read from the system's keyring, else from its file in
// the config folder (mode 600); --save-password asks for it once, checks it and stores it in the
// keyring (the file where there is none), --move-to-keyring moves an existing file into the
// keyring. Without it, it is asked first (hidden), so the rest runs unattended. It is never printed.
func SyncMain(args []string, out io.Writer) error {
	defer onSignal()()
	def := filepath.Join(config.Cache, "iphone")
	ap := phones.NewArgs("iphone-sync", "Back up the iPhone and decrypt its databases.")
	ap.Params = map[string]any{"default": def, "file": config.SecretFile(Secret)}
	outDir := ap.String("-o,--out", "OUT", def, "folder for the decrypted files (default {default})")
	noBackup := ap.Bool("--no-backup", "only decrypt the existing backup")
	root := ap.String("--backup-root", "BACKUP_ROOT", "", "the folder the backups are in (default: [iphone] backup_root)")
	udid := ap.String("--udid", "UDID", "", "which iPhone (default: [iphone] udid, else the only one)")
	fromStdin := ap.Bool("--password-stdin", "read the backup password from the standard input")
	full := ap.Bool("--full", "force a full backup instead of an incremental one")
	only := ap.List("--only", "NAME", "decrypt only these databases (e.g. whatsapp-calls.sqlite), no media")
	save := ap.Bool("--save-password", "ask for the password, check it and store it in the keyring (else a file, mode 600)")
	move := ap.Bool("--move-to-keyring", "move the password from {file} into the keyring")
	if err := ap.Parse(args, out); err != nil {
		return err
	}
	if *move {
		return moveToKeyring(out)
	}
	say := phones.Printer(out)
	r, err := (&SyncOptions{Out: *outDir, BackupRoot: *root, UDID: *udid, NoBackup: *noBackup, Full: *full,
		Only: *only, Say: say, Raw: out}).resolve()
	if err != nil {
		return err
	}
	if (*noBackup || *save) && !r.have {
		return errNoBackup
	}

	var opened *Backup
	tryOpen := func(pw []byte) error {
		if opened != nil {
			opened.Close()
		}
		var err error
		opened, err = openBackup(r.backupDir, pw)
		return err
	}
	var pw []byte
	if *fromStdin { // given for this run only (the app asks for it): never stored
		if pw, err = readLine(); err != nil {
			return err
		}
		if pw == nil {
			pw = []byte{}
		}
		if r.have {
			if err := tryOpen(pw); errors.Is(err, ErrWrongPassword) {
				return phones.Fail("Wrong backup password.", nil)
			} else if err != nil {
				return err
			}
		}
	} else if !*save {
		if pw, err = StoredPassword(); err != nil {
			return err
		}
		if pw != nil && r.have {
			if err := tryOpen(pw); errors.Is(err, ErrWrongPassword) {
				return phones.Fail("The stored backup password is wrong: store it again with --save-password.", nil)
			} else if err != nil {
				return err
			}
		}
	}
	for pw == nil {
		got, err := ask("Backup password (empty to quit): ")
		if err != nil {
			return err
		}
		if len(got) == 0 {
			return &phones.Failure{Code: 1}
		}
		pw = got
		if !r.have { // nothing to check it against yet; the backup itself will tell
			break
		}
		if err := tryOpen(pw); errors.Is(err, ErrWrongPassword) {
			say("Wrong password.", nil)
			pw = nil
		} else if err != nil {
			say("Another error (not a wrong password): {e}", map[string]any{"e": err})
			pw = nil
		}
	}

	if *save {
		if opened != nil {
			opened.Close()
		}
		where, err := config.SaveSecret(Secret, string(pw))
		if err != nil {
			return err
		}
		say("Stored in the {where}.", map[string]any{"where": where})
		return nil
	}
	r.Password = pw
	return r.run(opened)
}
