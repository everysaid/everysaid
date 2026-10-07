package signal

// Ports Store.link and link_or_copy of everysaid/media.py, for this plugin's files alone (the media
// importer is another part of the port): each file stored once, by content, in the archive's media
// store (`<media store>/media/<ab>/<sha256><ext>`), hard-linked where it can be, and tied to its
// message.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"everysaid/internal/archive"
)

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// linkOrCopy is a hard link to src at dest, or a copy wherever a link cannot be made (another file
// system, or one without hard links). A missing source or an existing destination is still an error.
func linkOrCopy(src, dest string) error {
	err := os.Link(src, dest)
	if err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrExist) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	if st, err := in.Stat(); err == nil {
		defer os.Chtimes(dest, st.ModTime(), st.ModTime())
	}
	return out.Close()
}

// mimeOf is the type a file's name says, as Python's mimetypes.guess_type would (nil: not known).
func mimeOf(path string) any {
	t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if t == "" {
		return nil
	}
	t, _, _ = strings.Cut(t, ";")
	return strings.TrimSpace(t)
}

// linkMedia ties the file at path (rel within the source's folder) to a message; it says whether it
// was new to the message.
func linkMedia(a *archive.Archive, sourceID int64, path, rel string, messageID int64) (bool, error) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return false, nil // not (or no longer) there
	}
	if a.Exists("SELECT 1 FROM attachment WHERE source_id = ? AND source_path = ? AND message_id = ?", sourceID, rel, messageID) {
		return false, nil
	}
	var digest string
	// the file of this source path hashed before, unless it is another file now: its size tells
	if !a.Row("SELECT a.sha256 FROM attachment a JOIN media md ON md.sha256 = a.sha256 "+
		"WHERE a.source_id = ? AND a.source_path = ? AND md.size = ?", []any{sourceID, rel, st.Size()}, &digest) {
		if digest, err = fileHash(path); err != nil {
			return false, err
		}
	}
	if !a.Exists("SELECT 1 FROM media WHERE sha256 = ?", digest) {
		stored := "media/" + digest[:2] + "/" + digest + strings.ToLower(filepath.Ext(path))
		target := filepath.Join(archive.MediaRoot(), filepath.FromSlash(stored))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return false, err
		}
		if _, err := os.Stat(target); err != nil {
			if err := linkOrCopy(path, target); err != nil {
				return false, err
			}
		}
		a.Exec("INSERT INTO media VALUES (?, ?, ?, ?)", digest, st.Size(), mimeOf(path), stored)
	}
	a.Exec("INSERT INTO attachment (message_id, sha256, source_id, source_path) VALUES (?, ?, ?, ?)",
		messageID, digest, sourceID, rel)
	return true, nil
}
