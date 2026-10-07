// Ports scripts/iphone-ls.py: what the iPhone backup holds, by folder, without extracting anything.
package iphone

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/i18n"
	"everysaid/internal/phones"
)

// Group is a domain and the leading components of a path, with its files and their total size.
type Group struct {
	Domain, Path string
	Files, Size  int64
}

// List groups the files whose domain and relative path match SQL LIKE patterns ("%viber%",
// "%Attachments%") by domain and the first depth components of the path, largest first.
func List(b *Backup, domainLike, pathLike string, depth int) (groups []Group, err error) {
	defer db.Recover(&err)
	index := map[[2]string]int{}
	rows := db.Query(b.Manifest(), "SELECT domain, relativePath, file FROM Files WHERE flags = 1 AND domain LIKE ? AND relativePath LIKE ?",
		domainLike, pathLike)
	defer rows.Close()
	for rows.Next() {
		var domain, path string
		var blob []byte
		rows.Scan(&domain, &path, &blob)
		size, err := listedSize(blob)
		if err != nil {
			return nil, err
		}
		key := [2]string{domain, strings.Join(head(strings.Split(path, "/"), depth), "/")}
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, Group{Domain: key[0], Path: key[1]})
		}
		groups[i].Files++
		groups[i].Size += size
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Size > groups[j].Size })
	return groups, nil
}

// head is Python's parts[:n], a negative n counting from the end.
func head(parts []string, n int) []string {
	if n < 0 {
		n += len(parts)
	}
	return parts[:max(0, min(n, len(parts)))]
}

// backupDir is config.iphone_backup(): the backup of the configured (or only) iPhone.
func backupDir() (string, error) {
	udid, n := config.IphoneUDID(config.IphoneBackupRoot)
	if udid == "" {
		return "", phones.Fail(`No single iPhone found ({n}): write its UDID in {file}, [iphone] udid = "...".`,
			map[string]any{"n": n, "file": config.ConfigFile})
	}
	return filepath.Join(config.IphoneBackupRoot, udid), nil
}

// LsMain is iphone-ls.py's command line: everysaid iphone-ls DOMAIN_LIKE [PATH_LIKE] [--depth N].
// The password comes from where iphone-sync keeps it (keyring, else file), or is asked for; it is
// never printed.
func LsMain(args []string, out io.Writer) error {
	ap := phones.NewArgs("iphone-ls", "List the iPhone backup's files by folder.")
	domain := ap.Pos("domain", "", "SQL LIKE pattern for the domain", false)
	path := ap.Pos("path", "%", "SQL LIKE pattern for the relative path", true)
	depth := ap.Int("--depth", "DEPTH", 3, "path components to group by (default 3)")
	if err := ap.Parse(args, out); err != nil {
		return err
	}
	dir, err := backupDir()
	if err != nil {
		return err
	}
	pw, err := StoredPassword()
	if err != nil {
		return err
	}
	if pw == nil {
		if pw, err = ask("Backup password: "); err != nil {
			return err
		}
	}
	b, err := Open(dir, pw)
	if errors.Is(err, ErrWrongPassword) {
		return phones.Fail("Wrong password.", nil)
	} else if err != nil {
		return err
	}
	defer b.Close()
	groups, err := List(b, *domain, *path, *depth)
	if err != nil {
		return err
	}
	var size, files int64
	for _, g := range groups {
		fmt.Fprintf(out, "%10.1f MB %7d  %s  %s\n", float64(g.Size)/1e6, g.Files, g.Domain, g.Path)
		size += g.Size
		files += g.Files
	}
	fmt.Fprintf(out, "%10.1f MB %7d  %s\n", float64(size)/1e6, files, i18n.Say("total", nil))
	return nil
}
