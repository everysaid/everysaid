// Ports iphone_backup_decrypt/iphone_backup.py EncryptedBackup: an encrypted iTunes/Finder backup,
// its Manifest.db decrypted into a temporary folder, its files decrypted one by one.
package iphone

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"howett.net/plist"

	"everysaid/internal/db"
)

var (
	// ErrWrongPassword: the keybag did not open with the password.
	ErrWrongPassword = errors.New("Failed to decrypt keys: incorrect passphrase?")
	// ErrNotEncrypted: a backup made without a password (it has no calls).
	ErrNotEncrypted = errors.New("Backup does not look like an encrypted iOS backup!")
	// ErrNotFound: no file of that path (and domain) in the backup.
	ErrNotFound = errors.New("not in the backup")
)

var fileID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Backup is an encrypted backup opened with its password. Close removes the decrypted Manifest.db.
type Backup struct {
	Dir      string
	manifest map[string]any
	keybag   *keybag
	tmp      string
	db       *sql.DB
}

// Open unlocks a backup's keybag with the password and decrypts its Manifest.db (test_decryption):
// ErrWrongPassword when the password is not the backup's.
func Open(dir string, password []byte) (*Backup, error) {
	b, err := load(dir)
	if err != nil {
		return nil, err
	}
	key, err := b.keybag.deriveKey(password)
	if err != nil {
		return nil, err
	}
	return b.unlock(key)
}

// OpenWithKey is Open with the key derived from the password (PasswordKey of an open backup),
// which saves the derivation's seconds.
func OpenWithKey(dir string, key []byte) (*Backup, error) {
	b, err := load(dir)
	if err != nil {
		return nil, err
	}
	return b.unlock(key)
}

func load(dir string) (*Backup, error) {
	plistPath := filepath.Join(dir, "Manifest.plist")
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	_, e1 := os.Stat(plistPath)
	_, e2 := os.Stat(filepath.Join(dir, "Manifest.db"))
	if e1 != nil && e2 != nil {
		return nil, errors.New("Backup folder does not contain expected Manifest files!")
	}
	raw, err := os.ReadFile(plistPath)
	if err != nil {
		return nil, err
	}
	b := &Backup{Dir: dir}
	if _, err := plist.Unmarshal(raw, &b.manifest); err != nil {
		return nil, fmt.Errorf("Manifest.plist: %w", err)
	}
	if enc, _ := b.manifest["IsEncrypted"].(bool); !enc {
		return nil, ErrNotEncrypted
	}
	bag, _ := b.manifest["BackupKeyBag"].([]byte)
	if b.keybag, err = parseKeybag(bag); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Backup) unlock(key []byte) (*Backup, error) {
	ok, err := b.keybag.unlockWithKey(key)
	if err != nil {
		return nil, err
	}
	if !ok || !b.keybag.unlocked {
		return nil, ErrWrongPassword
	}
	if err := b.decryptManifest(); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// PasswordKey is the key derived from the password, for OpenWithKey.
func (b *Backup) PasswordKey() []byte { return b.keybag.passwordKey }

func (b *Backup) decryptManifest() error {
	mk, _ := b.manifest["ManifestKey"].([]byte)
	if len(mk) < 4 {
		return errors.New("Manifest.plist: no ManifestKey")
	}
	class := int64(int32(binary.LittleEndian.Uint32(mk[:4])))
	key, err := b.keybag.unwrapForClass(class, mk[4:])
	if err != nil {
		return err
	}
	if b.tmp, err = os.MkdirTemp("", "everysaid-manifest-"); err != nil {
		return err
	}
	hold(b.tmp)
	path := filepath.Join(b.tmp, "Manifest.db")
	if _, err := decryptFile(filepath.Join(b.Dir, "Manifest.db"), key, path); err != nil {
		return err
	}
	if b.db, err = db.Open(path); err != nil { // a copy of our own, opened as the library does
		return err
	}
	b.db.SetMaxOpenConns(1)
	var max sql.NullInt64
	if err := b.db.QueryRow("SELECT max(length(file)) FROM Files;").Scan(&max); err != nil {
		return fmt.Errorf("Fatal error whilst querying Manifest.db file! (%w)", err)
	}
	if !max.Valid {
		return errors.New("Manifest.db file does not contain any data!")
	}
	if max.Int64 > 100*1024 { // most records are 1-3 KB
		return errors.New("Manifest.db file contains unexpectedly huge file blobs!")
	}
	return nil
}

// Close removes the decrypted Manifest.db.
func (b *Backup) Close() error {
	if b.db != nil {
		b.db.Close()
		b.db = nil
	}
	if b.tmp != "" {
		err := os.RemoveAll(b.tmp)
		release(b.tmp)
		b.tmp = ""
		return err
	}
	return nil
}

// Manifest is the decrypted Manifest.db, for reading.
func (b *Backup) Manifest() *sql.DB { return b.db }

// FilePath is a file's place in the backup, by its id (40 hex digits).
func (b *Backup) FilePath(id string) (string, error) {
	if !fileID.MatchString(id) {
		return "", fmt.Errorf("Invalid backup file ID: %q", id)
	}
	return filepath.Join(b.Dir, id[:2], id), nil
}

// lookup is the id and record of a file by its path and a domain LIKE pattern ("" for any).
func (b *Backup) lookup(relativePath, domainLike string) (string, []byte, error) {
	if domainLike == "" {
		domainLike = "%"
	}
	var id string
	var blob []byte
	err := b.db.QueryRow(`
		SELECT fileID, file
		FROM Files
		WHERE relativePath = ?
		AND domain LIKE ?
		AND flags=1
		ORDER BY domain, relativePath
		LIMIT 1;`, relativePath, domainLike).Scan(&id, &blob)
	if err == sql.ErrNoRows {
		return "", nil, fmt.Errorf("%w: %s", ErrNotFound, relativePath)
	}
	return id, blob, err
}

// ExtractFile decrypts a file, by its path and a domain LIKE pattern ("" for any), into output
// (whole or not at all), with its modification time. Info, when not nil, hears of a size other
// than the manifest's (common for live databases).
func (b *Backup) ExtractFile(relativePath, domainLike, output string, info func(decrypted, claimed int64)) error {
	id, blob, err := b.lookup(relativePath, domainLike)
	if err != nil {
		return err
	}
	rec, err := ParseRecord(blob)
	if err != nil {
		return err
	}
	var n int64
	if rec.EncryptionKey == nil {
		// iOS stores an empty file without a key: it is extracted empty (the library failed on it)
		if rec.Size != 0 {
			return errors.New("Path is not an encrypted file.")
		}
		if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(output, nil, 0o600); err != nil {
			return err
		}
	} else {
		key, err := b.keybag.unwrapForClass(rec.ProtectionClass, rec.EncryptionKey)
		if err != nil {
			return err
		}
		in, err := b.FilePath(id)
		if err != nil {
			return err
		}
		if n, err = decryptFile(in, key, output); err != nil {
			return err
		}
	}
	if n != rec.Size && info != nil {
		info(n, rec.Size)
	}
	if rec.Mtime != 0 {
		t := time.Unix(rec.Mtime, 0)
		return os.Chtimes(output, t, t)
	}
	return nil
}

// DecryptedSize decrypts a file of the backup without keeping it, as iphone-verify reads it: the
// size it decrypts to. It fails on a file stored without a key.
func (b *Backup) DecryptedSize(id string, rec *Record) (int64, error) {
	if rec.EncryptionKey == nil {
		return 0, errors.New("Path is not an encrypted file.")
	}
	key, err := b.keybag.unwrapForClass(rec.ProtectionClass, rec.EncryptionKey)
	if err != nil {
		return 0, err
	}
	path, err := b.FilePath(id)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if st.Size() == 0 { // the library reads it whole and finds no padding byte
		return 0, errPadding
	}
	return decryptStream(f, st.Size(), key, io.Discard)
}
