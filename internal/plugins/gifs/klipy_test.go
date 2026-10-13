package gifs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/netsafe"
	"everysaid/internal/plugins"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "everysaid-gifs-test")
	for k, v := range map[string]string{"EVERYSAID_DATA": "data", "EVERYSAID_CACHE": "cache",
		"EVERYSAID_CONFIG": "config", "EVERYSAID_STATE": "state"} {
		os.Setenv(k, filepath.Join(dir, v))
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test-gifs")
	config.Load()
	netsafe.Dialable = func(netip.AddrPort) bool { return true } // the test's server is on this machine
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type host struct{ store *core.Store }

func (h *host) Store() *core.Store      { return h.store }
func (h *host) Emit(plugins.M)          {}
func (h *host) Alert(_, _ string)       {}
func (h *host) ImportLock() sync.Locker { return &sync.Mutex{} }

// Klipy: nothing without its key; then what is popular, words searched for page by page (its ad
// passed by), a GIF's video, its sending told by its slug; a key refused said so, never shown.
func TestKlipy(t *testing.T) {
	var shared []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/api/v1/bad/"):
			w.WriteHeader(401)
		case p == "/api/v1/k1/gifs/trending":
			io.WriteString(w, `{"result":true,"data":{"data":[{"id":1,"slug":"one","file":{"hd":{"mp4":{"url":"`+
				"http://"+r.Host+`/files/one.mp4","width":480,"height":270}}}}],"has_next":false}}`)
		case p == "/api/v1/k1/gifs/search" && r.URL.Query().Get("page") == "1":
			if r.URL.Query().Get("q") != "κόκκαλης" || r.URL.Query().Has("customer_id") {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"result":true,"data":{"data":[{"id":2,"slug":"kokkalis","file":{"md":{"mp4":{"url":"`+
				"http://"+r.Host+`/files/k.mp4","width":220,"height":160}}}},{"type":"ad"}],"has_next":true}}`)
		case p == "/api/v1/k1/gifs/search" && r.URL.Query().Get("page") == "2":
			io.WriteString(w, `{"result":true,"data":{"data":[{"id":3,"slug":"three","file":{"sm":{"mp4":{"url":"x","width":1,"height":1}}}}],"has_next":false}}`)
		case strings.HasPrefix(p, "/files/"):
			w.Header().Set("Content-Type", "video/mp4")
			io.WriteString(w, "mp4 "+p)
		case r.Method == "POST" && strings.HasSuffix(p, "/share"):
			shared = append(shared, p)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := klipyAPI
	klipyAPI = srv.URL + "/api/v1"
	defer func() { klipyAPI = old }()

	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	s, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	iid, err := plugins.Create(s, "klipy", "Klipy", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := plugins.NewContext(&host{s}, *plugins.GetInstance(s, iid))
	var k Klipy
	if k.NotSearching(c) != noKey {
		t.Fatal("no key")
	}
	if _, err := c.SaveSecret("key", "k1"); err != nil {
		t.Skip("no place for a secret:", err)
	}
	defer c.DeleteSecret("key")
	ctx := context.Background()

	got, next, err := k.SearchGIFs(ctx, c, "", "")
	if err != nil || len(got) != 1 || got[0].ID != "1" || got[0].Width != 480 || next != "" {
		t.Fatalf("trending: %+v %q %v", got, next, err)
	}
	got, next, err = k.SearchGIFs(ctx, c, "κόκκαλης", "")
	if err != nil || len(got) != 1 || got[0].ID != "2" || got[0].Height != 160 || next != "2" {
		t.Fatalf("search: %+v %q %v", got, next, err)
	}
	more, next, err := k.SearchGIFs(ctx, c, "κόκκαλης", next)
	if err != nil || len(more) != 1 || next != "" {
		t.Fatalf("next page: %+v %q %v", more, next, err)
	}
	f, err := k.GIF(ctx, c, "2")
	if err != nil || string(f.Data) != "mp4 /files/k.mp4" || !f.Animated || f.MimeType != "video/mp4" {
		t.Fatalf("%+v %v", f, err)
	}
	if err := k.SentGIF(ctx, c, "2"); err != nil || len(shared) != 1 || shared[0] != "/api/v1/k1/gifs/kokkalis/share" {
		t.Fatalf("shared %v %v", shared, err)
	}
	if _, err := k.GIF(ctx, c, "99"); err == nil {
		t.Fatal("a GIF never found")
	}

	c.SaveSecret("key", "bad")
	_, _, err = k.SearchGIFs(ctx, c, "x", "")
	if err == nil || !strings.Contains(err.Error(), "refused") || strings.Contains(err.Error(), "bad") {
		t.Fatalf("%v", err)
	}
}
