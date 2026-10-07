// Package webui holds the interface (the PWA built from web/) inside the binary.
//
// `go:embed` cannot reach web/dist from here, so the build is copied into ui/ first:
//
//	cd web && pnpm install && pnpm build      # the interface, into web/dist
//	go generate ./internal/webui              # copied into internal/webui/ui
//	go build ./cmd/everysaid                  # embedded
//
// Without a build (a fresh checkout), the package still compiles: ui/ holds only .keep, and the
// server shows a page saying how to build the interface.
package webui

//go:generate go run gen.go

import (
	"embed"
	"io/fs"
)

//go:embed all:ui
var embedded embed.FS

//go:embed placeholder.html
var Placeholder []byte

// FS is the built interface (index.html at its root), or nil when none was embedded.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "ui")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
