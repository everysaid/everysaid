package demo

// The demo's GIFs: three invented ones (test patterns that move), found by their names.

import (
	"context"
	"embed"
	"strings"
	"time"

	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

//go:embed gifs/*.mp4
var gifFiles embed.FS

var demoGIFs = []plugins.GIF{{ID: "dog", Width: 160, Height: 120}, {ID: "cat", Width: 160, Height: 160},
	{ID: "party", Width: 200, Height: 120}}

// GIFs is the demo's `demo-gifs` plugin (only in the demo, as its sender).
type GIFs struct{}

func (GIFs) Info() *plugins.Info {
	return &plugins.Info{ID: "demo-gifs", Name: "Demo GIFs", Kind: "gifs",
		Description: "Invented: three GIFs that move, found by their names."}
}

func (GIFs) NotSearching(c *plugins.Context) string { return "" }

// SearchWait: invented, at once (a moment's pause, as Telegram's).
func (GIFs) SearchWait() time.Duration { return 300 * time.Millisecond }

func (GIFs) SearchGIFs(ctx context.Context, c *plugins.Context, query, offset string) ([]plugins.GIF, string, error) {
	out := []plugins.GIF{}
	for _, g := range demoGIFs {
		if strings.Contains(g.ID, strings.ToLower(strings.TrimSpace(query))) {
			out = append(out, g)
		}
	}
	return out, "", nil
}

func (GIFs) GIF(ctx context.Context, c *plugins.Context, id string) (*plugins.File, error) {
	data, err := gifFiles.ReadFile("gifs/" + id + ".mp4")
	if err != nil || strings.Contains(id, "/") {
		return nil, errs.Plugin("That GIF is no longer in the results: search again", 404)
	}
	return &plugins.File{Data: data, Filename: id + ".mp4", MimeType: "video/mp4", Animated: true}, nil
}
