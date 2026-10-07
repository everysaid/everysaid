package libraries

// Ports Folder of everysaid/plugins/libraries.py.

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

// Folder is the `folder` library: kept files go into year/month subfolders, named by date and service.
type Folder struct{}

func (Folder) Info() *plugins.Info {
	return &plugins.Info{
		ID: "folder", Name: "Folder", Kind: "library",
		Description: "A folder on disk: kept files go into year/month subfolders, named by date and service.",
		Settings: []plugins.Setting{
			{Key: "path", Label: "Folder", Type: "path", Required: true},
			{Key: "make", Label: "Camera make (where missing)", Type: "text", Default: config.ImmichMake},
		},
	}
}

func folderRoot(c *plugins.Context) string { return config.ExpandUser(c.Str("path")) }

func (Folder) Check(c *plugins.Context) (bool, string) {
	root := folderRoot(c)
	if root == "" {
		return false, "missing: the folder"
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return false, "not found: " + root
	}
	return true, "ready"
}

// index is the sha256 of every file in the folder (in the cache, library-<id>.db), refreshed for
// the files new or changed since last time.
func index(c *plugins.Context) (d *sql.DB, err error) {
	if err := os.MkdirAll(config.Cache, 0o700); err != nil {
		return nil, err
	}
	d, err = db.Open(filepath.Join(config.Cache, fmt.Sprintf("library-%d.db", c.ID)))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			d.Close()
		}
	}()
	defer db.Recover(&err)
	db.Exec(d, "CREATE TABLE IF NOT EXISTS file (path TEXT PRIMARY KEY, size INTEGER, mtime INTEGER, sha256 TEXT)")
	known := map[string][2]int64{}
	db.Each(d, "SELECT path, size, mtime FROM file", nil, func(scan func(...any)) {
		var p string
		var size, mtime int64
		scan(&p, &size, &mtime)
		known[p] = [2]int64{size, mtime}
	})
	root := folderRoot(c)
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil // a folder that cannot be read (or none): what it holds is not there
		}
		if e.IsDir() {
			return nil
		}
		st, err := os.Stat(p) // a link: what it points to
		if err != nil || st.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		if known[rel] != [2]int64{st.Size(), st.ModTime().Unix()} {
			sum, err := sha256File(p)
			if err != nil {
				return nil
			}
			db.Exec(tx, "INSERT OR REPLACE INTO file VALUES (?, ?, ?, ?)", rel, st.Size(), st.ModTime().Unix(), sum)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for rel := range known {
		if !seen[rel] {
			db.Exec(tx, "DELETE FROM file WHERE path = ?", rel)
		}
	}
	return d, tx.Commit()
}

func (Folder) Find(c *plugins.Context, sha256, path string) (string, error) {
	d, err := index(c)
	if err != nil {
		return "", err
	}
	defer d.Close()
	var rel string
	if err := d.QueryRow("SELECT path FROM file WHERE sha256 = ?", sha256).Scan(&rel); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return filepath.FromSlash(rel), nil
}

func (Folder) Store(c *plugins.Context, path string, meta M) (string, error) {
	root := folderRoot(c)
	dateMS, err := metaDate(meta, path)
	if err != nil {
		return "", err
	}
	w := when(dateMS)
	e := ext(path)
	if e == "" {
		e = extensionOf(metaStr(meta, "mime"))
	}
	folder := filepath.Join(root, w.Format("2006"), w.Format("01"))
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", err
	}
	service := metaStr(meta, "service")
	if service == "" {
		service = "chat"
	}
	base := w.Format("2006-01-02 150405") + " " + service
	dest := filepath.Join(folder, base+e)
	for n := 2; exists(dest); n++ {
		dest = filepath.Join(folder, fmt.Sprintf("%s %d%s", base, n, e))
	}
	tmp, err := PreparedCopy(path, dateMS, makeOf(c), metaStr(meta, "service"))
	if err != nil {
		return "", err
	}
	if err := move(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", err
	}
	rel, _ := filepath.Rel(root, dest)
	return rel, nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (Folder) Fetch(c *plugins.Context, ref, size string) (*plugins.Fetched, error) {
	p := filepath.Join(folderRoot(c), ref)
	if !inside(folderRoot(c), p) {
		return nil, nil
	}
	if _, err := os.Stat(p); err != nil {
		return nil, nil
	}
	return &plugins.Fetched{Path: p}, nil
}

// inside: a ref leads to a file of the folder, not out of it (a ref is the archive's, but a file
// served must be the library's).
func inside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) &&
		(len(rel) < 3 || rel[:3] != ".."+string(filepath.Separator))
}
