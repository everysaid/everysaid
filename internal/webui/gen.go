//go:build ignore

// gen copies the built interface (web/dist) into ui/, for go:embed: `go generate ./internal/webui`
// after `cd web && pnpm build`. What ui/ held before (an older build) goes, all but .keep.
package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	src := filepath.Join("..", "..", "web", "dist") // go generate runs in this package's folder
	if _, err := os.Stat(filepath.Join(src, "index.html")); err != nil {
		fmt.Fprintln(os.Stderr, "no build in web/dist: cd web && pnpm install && pnpm build")
		os.Exit(1)
	}
	dst, err := filepath.Abs("ui")
	if err != nil || filepath.Base(dst) != "ui" || filepath.Base(filepath.Dir(dst)) != "webui" {
		fmt.Fprintln(os.Stderr, "run from internal/webui (go generate ./internal/webui)")
		os.Exit(1)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		fail(err)
	}
	for _, e := range entries {
		if e.Name() != ".keep" {
			if err := os.RemoveAll(filepath.Join(dst, e.Name())); err != nil {
				fail(err)
			}
		}
	}
	n := 0
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		n++
		return copyFile(p, target)
	})
	if err != nil {
		fail(err)
	}
	fmt.Printf("webui: %d files from web/dist\n", n)
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
