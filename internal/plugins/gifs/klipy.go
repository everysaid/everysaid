// Package gifs holds the plugins that find GIFs to send (from any service) and are nothing else:
// Klipy. (Telegram's @gif is its source plugin's.)
package gifs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"everysaid/internal/errs"
	"everysaid/internal/netsafe"
	"everysaid/internal/plugins"
)

func init() { plugins.Register(Klipy{}) }

const (
	noKey       = "Klipy needs its API key"
	gifNotFound = "That GIF is no longer in the results: search again"
	perPage     = 24
)

// klipyAPI is Klipy's API (a variable: the tests' server); its key is part of each path.
var klipyAPI = "https://api.klipy.com/api/v1"

var client = &http.Client{Timeout: 15 * time.Second}

// Klipy finds GIFs through Klipy's API, with the user's own key (partner.klipy.com). It sees what is
// searched for, from this server's address; no id of the user is sent.
type Klipy struct{}

func (Klipy) Info() *plugins.Info {
	return &plugins.Info{
		ID: "klipy", Name: "Klipy", Kind: "gifs",
		Description: "GIFs from Klipy, to send through any service. Klipy sees the words searched for, from this server's address.",
		Needs:       []string{"An API key from partner.klipy.com"},
		Settings: []plugins.Setting{
			{Key: "key", Label: "API key", Type: "secret", Required: true, Help: "partner.klipy.com → API Keys"},
		},
	}
}

func (Klipy) Check(c *plugins.Context) (bool, string) {
	if c.Secret("key") == "" {
		return false, "missing: the API key"
	}
	return true, "ready"
}

func (Klipy) NotSearching(c *plugins.Context) string {
	if c.Secret("key") == "" {
		return noKey
	}
	return ""
}

// found: where each GIF found is (its video, and its slug, which Klipy is told a sending by)
var found = plugins.GIFsFound[gifAt]{Keep: 500, Bytes: 64 << 20}

type gifAt struct{ video, slug string }

// item is a GIF as Klipy gives it: its sizes (hd, md, sm, xs), each in several formats; an ad has
// no file and is passed by.
type item struct {
	ID   json.Number `json:"id"`
	Slug string      `json:"slug"`
	Type string      `json:"type"`
	File map[string]map[string]struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"file"`
}

func (Klipy) SearchGIFs(ctx context.Context, c *plugins.Context, query, offset string) ([]plugins.GIF, string, error) {
	key := c.Secret("key")
	if key == "" {
		return nil, "", errs.Plugin(noKey, 409)
	}
	page := 1
	if offset != "" {
		page, _ = strconv.Atoi(offset)
	}
	what := "trending"
	q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}}
	if query != "" {
		what = "search"
		q.Set("q", query)
	}
	var res struct {
		Result bool `json:"result"`
		Data   struct {
			Data    []item `json:"data"`
			HasNext bool   `json:"has_next"`
		} `json:"data"`
	}
	if err := get(ctx, fmt.Sprintf("%s/%s/gifs/%s?%s", klipyAPI, url.PathEscape(key), what, q.Encode()), &res); err != nil {
		return nil, "", err
	}
	out := []plugins.GIF{}
	for _, it := range res.Data.Data {
		// the video to send: the largest there is
		for _, size := range []string{"hd", "md", "sm", "xs"} {
			v, ok := it.File[size]["mp4"]
			if !ok || v.URL == "" {
				continue
			}
			g := plugins.GIF{ID: it.ID.String(), Width: v.Width, Height: v.Height}
			if g.ID == "" {
				g.ID = it.Slug
			}
			found.Put(g.ID, gifAt{video: v.URL, slug: it.Slug})
			out = append(out, g)
			break
		}
	}
	next := ""
	if res.Data.HasNext {
		next = strconv.Itoa(page + 1)
	}
	return out, next, nil
}

func (Klipy) GIF(ctx context.Context, c *plugins.Context, id string) (*plugins.File, error) {
	data, ok, err := found.File(id, func(at gifAt) ([]byte, error) { return netsafe.Video(ctx, at.video, "Everysaid") })
	if !ok {
		return nil, errs.Plugin(gifNotFound, 404)
	}
	if err != nil {
		return nil, err
	}
	return &plugins.File{Data: data, Filename: "gif-" + id + ".mp4", MimeType: "video/mp4", Animated: true}, nil
}

// SentGIF tells Klipy a GIF of its was sent (its share count, which its terms ask for); nothing of
// who sent it, nor where.
func (Klipy) SentGIF(ctx context.Context, c *plugins.Context, id string) error {
	at, ok := found.At(id)
	key := c.Secret("key")
	if !ok || at.slug == "" || key == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/%s/gifs/%s/share", klipyAPI, url.PathEscape(key), url.PathEscape(at.slug)), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func get(ctx context.Context, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return errs.Plugin("Klipy did not answer", 502)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errs.Plugin("Klipy refused the API key", 409)
	case resp.StatusCode == http.StatusTooManyRequests:
		return errs.Plugin("Klipy's limit of searches for now is reached: try again later", 429)
	case resp.StatusCode != http.StatusOK:
		return errs.Plugin("Klipy did not answer", 502)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(into); err != nil {
		return errs.Plugin("Klipy did not answer", 502)
	}
	return nil
}
