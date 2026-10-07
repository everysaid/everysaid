package libraries

import (
	"database/sql"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "everysaid-libraries-test")
	for k, v := range map[string]string{"EVERYSAID_DATA": "data", "EVERYSAID_CACHE": "cache",
		"EVERYSAID_CONFIG": "config", "EVERYSAID_STATE": "state"} {
		os.Setenv(k, filepath.Join(dir, v))
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test-libraries")
	os.Setenv("PATH", "") // no exiftool: the copies are the files as they are
	config.Load()
	config.Timezone = time.FixedZone("EEST", 3*3600)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type host struct{ store *core.Store }

func (h *host) Store() *core.Store      { return h.store }
func (h *host) Emit(M)                  {}
func (h *host) Alert(_, _ string)       {}
func (h *host) ImportLock() sync.Locker { return &sync.Mutex{} }

func instance(t *testing.T, plugin string, settings M) (*core.Store, *plugins.Context) {
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
	t.Cleanup(s.Close)
	iid, err := plugins.Create(s, plugin, "Library", settings)
	if err != nil {
		t.Fatal(err)
	}
	return s, plugins.NewContext(&host{s}, *plugins.GetInstance(s, iid))
}

func file(t *testing.T, name, content string) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// 2024-05-01 09:34:56 at +03:00
const dateMS = int64(1714545296000)

func TestFolder(t *testing.T) {
	root := t.TempDir()
	s, c := instance(t, "folder", M{"path": root})
	if ok, why := plugins.Check(Folder{}, c); !ok {
		t.Fatal(why)
	}
	p := file(t, "IMG.JPG", "a picture")
	sum, _ := sha256File(p)
	if ref, err := (Folder{}).Find(c, sum, p); err != nil || ref != "" {
		t.Fatal(ref, err)
	}
	ref, err := (Folder{}).Store(c, p, M{"date_ms": dateMS, "service": "whatsapp", "sha256": sum})
	if err != nil || ref != filepath.Join("2024", "05", "2024-05-01 093456 whatsapp.jpg") {
		t.Fatal(ref, err)
	}
	st, _ := os.Stat(filepath.Join(root, ref))
	if st.ModTime().UnixMilli() != dateMS {
		t.Fatal(st.ModTime())
	}
	// the same name again: a number; no extension: the type's; no date: the file's own
	ref2, _ := (Folder{}).Store(c, p, M{"date_ms": float64(dateMS), "service": "whatsapp"})
	if ref2 != filepath.Join("2024", "05", "2024-05-01 093456 whatsapp 2.jpg") {
		t.Fatal(ref2)
	}
	q := file(t, "voice", "a video")
	os.Chtimes(q, time.UnixMilli(dateMS), time.UnixMilli(dateMS))
	if ref3, _ := (Folder{}).Store(c, q, M{"mime": "video/mp4"}); ref3 != filepath.Join("2024", "05", "2024-05-01 093456 chat.mp4") {
		t.Fatal(ref3)
	}
	if got, err := (Folder{}).Find(c, sum, ""); err != nil || got != ref && got != ref2 {
		t.Fatal(got, err)
	}
	f, err := (Folder{}).Fetch(c, ref, "original")
	if err != nil || f == nil || f.Path != filepath.Join(root, ref) {
		t.Fatal(f, err)
	}
	if f, _ := (Folder{}).Fetch(c, "../../etc/passwd", ""); f != nil {
		t.Fatal("a file out of the folder")
	}
	if f, _ := (Folder{}).Fetch(c, "gone.jpg", ""); f != nil {
		t.Fatal("a file that is not there")
	}
	// a file gone from the folder is gone from its index
	os.Remove(filepath.Join(root, ref))
	os.Remove(filepath.Join(root, ref2))
	if got, _ := (Folder{}).Find(c, sum, ""); got != "" {
		t.Fatal(got)
	}
	s.MustWrite(func(tx *sql.Tx) { db.Exec(tx, "INSERT INTO media VALUES (?, 9, 'image/jpeg', 'media/x.jpg')", sum) })
	if err := Link(s, c.ID, "Library", sum, ref, "upload"); err != nil {
		t.Fatal(err)
	}
	Link(s, c.ID, "Library", sum, "other", "checksum")
	var asset, method string
	db.Row(s.Read(), "SELECT asset_id, method FROM library_link WHERE sha256 = ?", []any{sum}, &asset, &method)
	if asset != "other" || method != "checksum" {
		t.Fatal(asset, method)
	}
	_, c2 := instance(t, "folder", M{"path": filepath.Join(root, "none")})
	if ok, why := plugins.Check(Folder{}, c2); ok || !strings.HasPrefix(why, "not found: ") {
		t.Fatal(why)
	}
}

func TestImmich(t *testing.T) {
	var mu sync.Mutex
	uploaded := map[string]string{}
	var content []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("x-api-key") != "the-key" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/api/server/ping":
			io.WriteString(w, `{"res":"pong"}`)
		case r.URL.Path == "/api/assets/bulk-upload-check":
			var in struct {
				Assets []struct{ ID, Checksum string } `json:"assets"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			a := in.Assets[0]
			if uploaded["sha1"] == a.Checksum {
				json.NewEncoder(w).Encode(M{"results": []M{{"id": a.ID, "action": "reject", "assetId": "asset-1"}}})
				return
			}
			json.NewEncoder(w).Encode(M{"results": []M{{"id": a.ID, "action": "accept"}}})
		case r.URL.Path == "/api/assets" && r.Method == "POST":
			_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if r.ContentLength <= 0 {
				w.WriteHeader(411)
				return
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(part)
				if part.FormName() == "assetData" {
					uploaded["filename"], uploaded["type"] = part.FileName(), part.Header.Get("Content-Type")
					content = b
				} else {
					uploaded[part.FormName()] = string(b)
				}
			}
			w.WriteHeader(201)
			io.WriteString(w, `{"id":"asset-2","status":"created"}`)
		case r.URL.Path == "/api/assets/asset-2/thumbnail":
			w.Header().Set("Content-Type", "image/jpeg")
			io.WriteString(w, "thumb:"+r.URL.Query().Get("size"))
		case r.URL.Path == "/api/assets/asset-2/original":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(content)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	_, c := instance(t, "immich", M{"url": srv.URL + "/"})
	if ok, why := plugins.Check(Immich{}, c); ok || why != "missing: the API key" {
		t.Fatal(why)
	}
	if _, err := c.SaveSecret("key", "the-key"); err != nil {
		t.Skip("no place for a secret:", err)
	}
	defer c.DeleteSecret("key")
	if ok, why := plugins.Check(Immich{}, c); !ok {
		t.Fatal(why)
	}
	p := file(t, "photo.JPG", "a picture")
	sum, _ := sha256File(p)
	if ref, err := (Immich{}).Find(c, sum, p); err != nil || ref != "" {
		t.Fatal(ref, err)
	}
	if ref, _ := (Immich{}).Find(c, sum, ""); ref != "" {
		t.Fatal(ref)
	}
	ref, err := (Immich{}).Store(c, p, M{"date_ms": dateMS, "service": "viber", "mime": "image/jpeg", "sha256": sum})
	if err != nil || ref != "asset-2" {
		t.Fatal(ref, err)
	}
	if uploaded["deviceAssetId"] != sum || uploaded["deviceId"] != "everysaid" ||
		uploaded["fileCreatedAt"] != "2024-05-01T09:34:56+03:00" || uploaded["fileModifiedAt"] != uploaded["fileCreatedAt"] ||
		uploaded["filename"] != "viber 2024-05-01 093456.jpg" || uploaded["type"] != "image/jpeg" || string(content) != "a picture" {
		t.Fatalf("%v %q", uploaded, content)
	}
	sha1sum, _ := sha1File(p)
	uploaded["sha1"] = sha1sum
	if ref, err := (Immich{}).Find(c, sum, p); err != nil || ref != "asset-1" {
		t.Fatal(ref, err)
	}
	for size, want := range map[string]string{"preview": "thumb:preview", "": "thumb:preview", "thumbnail": "thumb:thumbnail",
		"original": "a picture"} {
		f, err := (Immich{}).Fetch(c, "asset-2", size)
		if err != nil || string(f.Data) != want || f.Type != "image/jpeg" {
			t.Fatal(size, f, err)
		}
	}
	if _, err := (Immich{}).Fetch(c, "asset-9", "original"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatal(err)
	}
	if isoformat(time.UnixMilli(dateMS+5).In(config.Timezone)) != "2024-05-01T09:34:56.005000+03:00" {
		t.Fatal(isoformat(time.UnixMilli(dateMS + 5).In(config.Timezone)))
	}
}
