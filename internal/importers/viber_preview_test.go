package importers

// Viber's link previews: kept from the iPhone, on new messages and on those the
// archive had; their pictures fetched from the linked sites (here on this machine) once, a dead
// address not tried again, and an import not held up past its budget.

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/netsafe"
)

// previewSites is a site on this machine (allowed for the test): /pic.png a picture, /gone a 404,
// /slow a picture late; and how many requests each had.
func previewSites(t *testing.T) (*httptest.Server, map[string]*atomic.Int32) {
	t.Cleanup(config.Load)
	t.Setenv("EVERYSAID_CACHE", t.TempDir())
	config.Load()
	old, oldSpacing, oldBudget := netsafe.Dialable, previewSpacing, previewBudget
	netsafe.Dialable = func(netip.AddrPort) bool { return true }
	previewSpacing = 0
	t.Cleanup(func() { netsafe.Dialable, previewSpacing, previewBudget = old, oldSpacing, oldBudget })
	var pic bytes.Buffer
	png.Encode(&pic, image.NewGray(image.Rect(0, 0, 40, 30)))
	hits := map[string]*atomic.Int32{"/pic.png": {}, "/gone": {}, "/slow": {}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := hits[r.URL.Path]; n != nil {
			n.Add(1)
		}
		switch r.URL.Path {
		case "/pic.png":
			if r.UserAgent() != previewUserAgent {
				t.Errorf("user agent %q", r.UserAgent())
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write(pic.Bytes())
		case "/slow":
			time.Sleep(500 * time.Millisecond)
			w.Header().Set("Content-Type", "image/png")
			w.Write(pic.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

// The iPhone's preview, its picture late: past the budget it is left for the next import, not
// taken as failed.
func TestViberIphonePreviewBudget(t *testing.T) {
	srv, hits := previewSites(t)
	previewBudget = 100 * time.Millisecond
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "viber.sqlite")
	viberDB(t, path)
	d, err := db.Open(path)
	must(t, err)
	db.Exec(d, "UPDATE ZVIBERMESSAGE SET ZSYSTEMTYPE = 'url', ZTEXT = 'https://example.com/x', ZMETADATA = ? WHERE ZTOKEN = 1001",
		js(M{"URL": "https://example.com/x", "Title": "X", "ThumbnailURL": srv.URL + "/slow", "ThumbnailWidth": 40}))
	d.Close()
	var said []string
	must(t, Viber(a, func(s string) { said = append(said, s) }, ViberOptions{IphoneDB: path}))
	eq(t, "said", said[len(said)-1], "link preview pictures: 0 kept, 0 could not be fetched, 1 left for the next import")
	eq(t, "title kept", a.Int("SELECT count(*) FROM link_preview WHERE title = 'X' AND image IS NULL"), int64(1))
	if _, err := os.Stat(filepath.Join(config.Cache, "link-preview-failed.tsv")); err == nil {
		t.Fatal("a picture not reached remembered as failed")
	}
	previewBudget = 5 * time.Second
	must(t, Viber(a, nil, ViberOptions{IphoneDB: path}))
	eq(t, "the next import", a.Int("SELECT count(*) FROM link_preview WHERE title = 'X' AND image IS NOT NULL"), int64(1))
	eq(t, "asked twice", hits["/slow"].Load(), int32(2))
}

// What a message's metadata gives, and a later preview filling in an earlier one.
func TestViberPreviewMerge(t *testing.T) {
	var links viberLinks
	l := links.add("1", viberPreview(M{"URL": "https://e.com", "Title": "T"}))
	links.add("1", viberPreview(M{"URL": "https://e.com", "Title": "Other", "Description": "D", "ThumbnailURL": "https://e.com/t.jpg"}))
	eq(t, "merged", []string{l.preview.Title, l.preview.Description, l.thumb}, []string{"T", "D", "https://e.com/t.jpg"})
	if viberPreview(M{"URL": "https://e.com"}) != nil || viberPreview(M{"Title": "no link"}) != nil {
		t.Fatal("a preview of nothing")
	}
}
