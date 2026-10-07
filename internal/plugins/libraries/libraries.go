// Package libraries holds the library plugins: where kept pictures and videos go, a folder on disk
// and immich. Ports everysaid/plugins/libraries.py. Each answers whether a file is already there,
// stores one with its date, and gives a stored file back (for showing it in the app).
//
// A file's date is its own EXIF date where it has one (never changed); else the date given (the
// message's, or one the user typed) is written into the copy that is stored, with the camera make
// where the file has none (`make`, by default "Everysaid": how chat media are told apart in a library).
package libraries

import (
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"hash"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

type M = plugins.M

func init() {
	plugins.Register(Folder{})
	plugins.Register(Immich{})
}

// Link records that a file of the archive is in a library (method: checksum, found there; upload,
// stored there).
func Link(s *core.Store, instanceID int64, label, sha256, ref, method string) error {
	return s.Write(func(tx *sql.Tx) error {
		db.Exec(tx, "INSERT INTO library_link (sha256, library, asset_id, method, linked_at, instance_id) "+
			"VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (sha256, library) DO UPDATE SET asset_id = excluded.asset_id, "+
			"method = excluded.method, instance_id = excluded.instance_id",
			sha256, label, ref, method, time.Now().Unix(), instanceID)
		return nil
	})
}

func fileHash(path string, h hash.Hash) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sha256File(path string) (string, error) { return fileHash(path, sha256.New()) }
func sha1File(path string) (string, error)   { return fileHash(path, sha1.New()) }

// ext is the file's extension in lower case, as Python's os.path.splitext gives it (a name's
// leading dots are not one).
func ext(path string) string {
	base := filepath.Base(path)
	i := strings.LastIndex(base, ".")
	if i <= 0 || strings.Trim(base[:i], ".") == "" {
		return ""
	}
	return strings.ToLower(base[i:])
}

// preferred: the extension of a type, as Python's mimetypes.guess_extension gives it where Go's
// list would start with a rarer one.
var preferred = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif",
	"image/webp": ".webp", "image/heic": ".heic", "image/heif": ".heif", "image/tiff": ".tiff",
	"video/mp4": ".mp4", "video/quicktime": ".mov", "video/3gpp": ".3gp", "video/webm": ".webm",
	"audio/mpeg": ".mp3", "audio/ogg": ".ogg", "audio/mp4": ".m4a", "application/pdf": ".pdf"}

func extensionOf(mimeType string) string {
	if e, ok := preferred[mimeType]; ok {
		return e
	}
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// metaDate is meta's date_ms, else the file's time.
func metaDate(meta M, path string) (int64, error) {
	switch v := meta["date_ms"].(type) {
	case int64:
		if v != 0 {
			return v, nil
		}
	case int:
		if v != 0 {
			return int64(v), nil
		}
	case float64:
		if v != 0 {
			return int64(v), nil
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.ModTime().UnixMilli(), nil
}

func metaStr(meta M, key string) string {
	s, _ := meta[key].(string)
	return s
}

func when(dateMS int64) time.Time { return time.UnixMilli(dateMS).In(config.Timezone) }

// copyFile copies src to dest, its time kept (shutil.copy2).
func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if st, err := in.Stat(); err == nil {
		os.Chtimes(dest, st.ModTime(), st.ModTime())
	}
	return nil
}

// move is shutil.move: a rename, else a copy then the original removed.
func move(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	if err := copyFile(src, dest); err != nil {
		os.Remove(dest)
		return err
	}
	return os.Remove(src)
}

// PreparedCopy is a temporary copy of src carrying the date (where it has no capture date of its
// own) and the make and model (where it has no make). Without exiftool, the copy as it is.
func PreparedCopy(src string, dateMS int64, make, service string) (string, error) {
	f, err := os.CreateTemp("", "everysaid-*"+ext(src))
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	f.Close()
	if err := copyFile(src, tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if tool, err := exec.LookPath("exiftool"); err == nil && dateMS != 0 {
		w := when(dateMS)
		// only where the file has no capture date of its own (-if): an EXIF date is never changed
		exec.Command(tool, "-q", "-q", "-overwrite_original", "-P", "-m",
			"-if", "not $DateTimeOriginal and not $CreateDate",
			"-AllDates="+w.Format("2006:01:02 15:04:05"), "-OffsetTimeOriginal="+w.Format("-07:00"), tmp).Run()
		if make != "" {
			exec.Command(tool, "-q", "-q", "-overwrite_original", "-P", "-m", "-if", "not $Make",
				"-Make="+make, "-Model="+service, tmp).Run()
		}
	}
	if dateMS != 0 {
		t := time.UnixMilli(dateMS)
		os.Chtimes(tmp, t, t)
	}
	return tmp, nil
}

func makeOf(c *plugins.Context) string {
	if m := c.Str("make"); m != "" {
		return m
	}
	return config.ImmichMake
}
