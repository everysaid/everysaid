package importers

// Viber's link previews: kept from the desktop and the iPhone, on new messages and on those the
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
	"strings"
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

func TestViberDesktopPreviews(t *testing.T) {
	srv, hits := previewSites(t)
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "desktop.db")
	f := newDesktop(t, path)
	f.message(dtGroup, dtMaria, false, dtHello, 9, "see https://example.com/a", js(M{"URL": "https://example.com/a",
		"Title": "A page", "Description": "About it", "ThumbnailURL": srv.URL + "/pic.png", "Type": "unhandled"}))
	f.message(dtGroup, dtMe, true, dtNote, 9, "https://example.com/b", js(M{"URL": "https://example.com/b",
		"Title": "B page", "ThumbnailURL": srv.URL + "/gone"}))
	f.message(dtGroup, dtBob, false, dtFixed, 9, "https://example.com/c", js(M{"URL": "https://example.com/c"}))
	f.message(dtGroup, dtBob, false, dtGone, 1, "https://example.com/d", "{}") // a preview later
	opt := ViberOptions{DesktopDB: path, NoIphone: true, DesktopSource: "viber-desktop/viber"}
	var said []string
	out := func(s string) { said = append(said, s) }
	must(t, Viber(a, out, opt))
	preview := func(key string) []any {
		var u, title, desc, img *string
		if !a.Row("SELECT p.url, p.title, p.description, p.image FROM link_preview p JOIN message m ON m.id = p.message_id "+
			"WHERE m.key = ?", []any{key}, &u, &title, &desc, &img) {
			return nil
		}
		s := func(p *string) string {
			if p == nil {
				return "-"
			}
			return *p
		}
		return []any{s(u), s(title), s(desc), img != nil}
	}
	eq(t, "with a picture", preview("6200000000000000001"), []any{"https://example.com/a", "A page", "About it", true})
	eq(t, "its picture gone", preview("6288698136818094900"), []any{"https://example.com/b", "B page", "-", false})
	eq(t, "nothing to show", preview("6288698136818095000"), []any(nil))
	eq(t, "no preview", preview("6288698136818095100"), []any(nil))
	eq(t, "no media", a.Int("SELECT count(*) FROM attachment"), int64(0))
	eq(t, "said", said[len(said)-1], "link preview pictures: 1 kept, 1 could not be fetched, 0 left for the next import")
	b, err := os.ReadFile(filepath.Join(config.Cache, "link-preview-failed.tsv"))
	must(t, err)
	if !strings.Contains(string(b), "\tp\t"+srv.URL+"/gone\n") {
		t.Fatalf("failures %q", b)
	}

	// again: nothing fetched (one has its picture, the other failed lately); the old message's
	// preview, come since, kept
	db.Exec(f.d, "UPDATE Messages SET Type = 9, Info = ? WHERE EventID = (SELECT EventID FROM Events WHERE Token = ?)",
		js(M{"URL": "https://example.com/d", "Title": "D page"}), dtGone)
	said = nil
	must(t, Viber(a, out, opt))
	eq(t, "fetched once", []int32{hits["/pic.png"].Load(), hits["/gone"].Load()}, []int32{1, 1})
	eq(t, "later", preview("6288698136818095100"), []any{"https://example.com/d", "D page", "-", false})
	eq(t, "said again", said[len(said)-1], "link previews on messages already here: 1")
	eq(t, "pictures", a.Int("SELECT count(*) FROM link_preview_image"), int64(1))
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
	must(t, Viber(a, func(s string) { said = append(said, s) }, ViberOptions{IphoneDB: path, NoDesktop: true}))
	eq(t, "said", said[len(said)-1], "link preview pictures: 0 kept, 0 could not be fetched, 1 left for the next import")
	eq(t, "title kept", a.Int("SELECT count(*) FROM link_preview WHERE title = 'X' AND image IS NULL"), int64(1))
	if _, err := os.Stat(filepath.Join(config.Cache, "link-preview-failed.tsv")); err == nil {
		t.Fatal("a picture not reached remembered as failed")
	}
	previewBudget = 5 * time.Second
	must(t, Viber(a, nil, ViberOptions{IphoneDB: path, NoDesktop: true}))
	eq(t, "the next import", a.Int("SELECT count(*) FROM link_preview WHERE title = 'X' AND image IS NOT NULL"), int64(1))
	eq(t, "asked twice", hits["/slow"].Load(), int32(2))
}

// What a message's metadata gives, and the desktop's copy filling the iPhone's in.
func TestViberPreviewMerge(t *testing.T) {
	var links viberLinks
	l := links.add("1", viberPreview(M{"URL": "https://e.com", "Title": "T"}))
	links.add("1", viberPreview(M{"URL": "https://e.com", "Title": "Other", "Description": "D", "ThumbnailURL": "https://e.com/t.jpg"}))
	eq(t, "merged", []string{l.preview.Title, l.preview.Description, l.thumb}, []string{"T", "D", "https://e.com/t.jpg"})
	if viberPreview(M{"URL": "https://e.com"}) != nil || viberPreview(M{"Title": "no link"}) != nil {
		t.Fatal("a preview of nothing")
	}
}
