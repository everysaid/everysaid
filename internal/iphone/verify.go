// Ports scripts/iphone-verify.py: decrypt every file of an encrypted iPhone backup and check it
// against Manifest.db (read only).
package iphone

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"everysaid/internal/db"
	"everysaid/internal/i18n"
	"everysaid/internal/phones"
)

const page = 4096 // SQLite's page size; live databases change by whole pages during a backup

// Problem is a file that did not decrypt as the manifest says: English with {params}.
type Problem struct {
	Text   string
	Params map[string]any
}

// Verified is what Verify found.
type Verified struct {
	Files, OK, Empty int
	Live             []string // databases that changed during the backup (decrypted normally)
	Problems         []Problem
}

// Verify decrypts every file of the backup (without keeping it) and checks its size against the
// manifest. progress(i, n) is called every 2000 files.
func Verify(b *Backup, progress func(i, n int)) (v Verified, err error) {
	defer db.Recover(&err)
	type row struct {
		id, domain, path string
		blob             []byte
	}
	var rows []row
	db.Each(b.Manifest(), "SELECT fileID, domain, relativePath, file FROM Files WHERE flags = 1", nil, func(scan func(...any)) {
		var r row
		scan(&r.id, &r.domain, &r.path, &r.blob)
		rows = append(rows, r)
	})
	v.Files = len(rows)
	for i, r := range rows {
		name := r.domain + "/" + r.path
		meta, err := ParseRecord(r.blob)
		var onDisk string
		if err == nil {
			onDisk, err = b.FilePath(r.id)
		}
		if err != nil { // a record or an id that cannot be read is a problem of that file, not the end
			v.Problems = append(v.Problems, Problem{"error ({e}): {name}", map[string]any{"e": err, "name": name}})
		} else if _, err := os.Stat(onDisk); err != nil {
			v.Problems = append(v.Problems, Problem{"missing: {name}", map[string]any{"name": name}})
		} else if meta.EncryptionKey == nil {
			v.Empty++ // empty files are stored without a key
		} else if n, err := b.DecryptedSize(r.id, meta); err != nil {
			v.Problems = append(v.Problems, Problem{"error ({e}): {name}", map[string]any{"e": err, "name": name}})
		} else if n == meta.Size {
			v.OK++
		} else if n%page == 0 && meta.Size%page == 0 {
			v.Live = append(v.Live, name)
		} else {
			v.Problems = append(v.Problems, Problem{"size {n} instead of {size}: {name}",
				map[string]any{"n": n, "size": meta.Size, "name": name}})
		}
		if (i+1)%2000 == 0 && progress != nil {
			progress(i+1, len(rows))
		}
	}
	return v, nil
}

// VerifyMain is iphone-verify.py's command line. The password comes from where iphone-sync keeps
// it (keyring, else file), or is asked for; it is never printed.
func VerifyMain(args []string, out io.Writer) error {
	ap := phones.NewArgs("iphone-verify", "Decrypt every file of the iPhone backup and check it (read only).")
	if err := ap.Parse(args, out); err != nil {
		return err
	}
	dir, err := backupDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "Manifest.plist")); err != nil {
		return phones.Fail("No complete backup was found (its Manifest.plist is missing).", nil)
	}
	say := phones.Printer(out)

	// the saved password (as iphone-sync keeps it) first, then as many tries as needed
	pw, err := StoredPassword()
	if err != nil {
		return err
	}
	var b *Backup
	for {
		if pw == nil {
			if pw, err = ask("Backup password (empty to quit): "); err != nil {
				return err
			}
			if len(pw) == 0 {
				return &phones.Failure{Code: 1}
			}
		}
		b, err = Open(dir, pw)
		if errors.Is(err, ErrWrongPassword) {
			say("Wrong password.", nil)
			pw = nil
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	defer b.Close()

	v, err := Verify(b, func(i, n int) { fmt.Fprintf(out, "  %d/%d...\n", i, n) })
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	say("Files in the manifest: {n}", map[string]any{"n": v.Files})
	say("Decrypted correctly: {n}", map[string]any{"n": v.OK})
	say("Empty files (no key): {n}", map[string]any{"n": v.Empty})
	say("Databases that changed during the backup (decrypted normally): {n}", map[string]any{"n": len(v.Live)})
	say("Real problems: {n}", map[string]any{"n": len(v.Problems)})
	for _, p := range v.Problems {
		fmt.Fprintln(out, "  "+i18n.Say(p.Text, p.Params))
	}
	if len(v.Problems) > 0 {
		return &phones.Failure{Code: 1}
	}
	return nil
}
