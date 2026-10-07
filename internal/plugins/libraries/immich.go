package libraries

// Ports Immich of everysaid/plugins/libraries.py.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/plugins"
)

// Immich is the `immich` library: an immich server, through its API.
type Immich struct{}

func (Immich) Info() *plugins.Info {
	return &plugins.Info{
		ID: "immich", Name: "immich", Kind: "library",
		Description: "An immich server, through its API. The API key needs asset.read, asset.view (previews), " +
			"asset.download (originals) and asset.upload.",
		Settings: []plugins.Setting{
			{Key: "url", Label: "Address", Type: "url", Required: true, Default: nilIfEmpty(config.ImmichURL)},
			{Key: "key", Label: "API key", Type: "secret", Help: "If empty, the scripts' immich-key is used"},
			{Key: "make", Label: "Camera make (where missing)", Type: "text", Default: config.ImmichMake},
		},
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func immichKey(c *plugins.Context) string {
	if k := c.Secret("key"); k != "" {
		return k
	}
	return config.SecretOrEmpty("immich-key")
}

func immichURL(c *plugins.Context) string {
	if u := c.Str("url"); u != "" {
		return u
	}
	return config.ImmichURL
}

// HTTPError is an answer of the server that is not a success.
type HTTPError struct {
	Code   int
	Status string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP Error %d: %s", e.Code, e.Status) }

// client takes the API key to the address set and no further: a redirect to another host is refused
// (Go, as the Python, would send the key along to it).
var client = &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
	if r.URL.Hostname() != via[0].URL.Hostname() {
		return fmt.Errorf("immich's address sends elsewhere (%s): set that one", r.URL.Host)
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after %d redirects", len(via))
	}
	return nil
}}

// request asks immich's API; the answer's body and type.
func request(c *plugins.Context, method, path string, body io.Reader, size int64, ctype string, timeout time.Duration) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(immichURL(c), "/")+"/api"+path, body)
	if err != nil {
		return nil, "", err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("x-api-key", immichKey(c))
	req.Header.Set("Accept", "application/json")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	r, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer r.Body.Close()
	if r.StatusCode >= 400 {
		return nil, "", &HTTPError{r.StatusCode, http.StatusText(r.StatusCode)}
	}
	data, err := io.ReadAll(r.Body)
	return data, r.Header.Get("Content-Type"), err
}

// call asks immich's API with a JSON body (or none), its JSON answer into out (unless nil).
func call(c *plugins.Context, method, path string, in, out any, timeout time.Duration) error {
	var body io.Reader
	size, ctype := int64(-1), ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, size, ctype = bytes.NewReader(b), int64(len(b)), "application/json"
	}
	data, _, err := request(c, method, path, body, size, ctype, timeout)
	if err != nil || out == nil || len(data) == 0 {
		return err
	}
	return json.Unmarshal(data, out)
}

func (Immich) Check(c *plugins.Context) (bool, string) {
	if immichURL(c) == "" {
		return false, "missing: the address"
	}
	if immichKey(c) == "" {
		return false, "missing: the API key"
	}
	if err := call(c, "GET", "/server/ping", nil, nil, 10*time.Second); err != nil {
		return false, "no answer: " + err.Error()
	}
	return true, "ready"
}

// Find: the asset of the same file, by its sha1, which immich keeps.
func (Immich) Find(c *plugins.Context, sha256, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if _, err := os.Stat(path); err != nil {
		return "", nil
	}
	sum, err := sha1File(path)
	if err != nil {
		return "", err
	}
	var r struct {
		Results []struct {
			Action  string `json:"action"`
			AssetID string `json:"assetId"`
		} `json:"results"`
	}
	in := map[string]any{"assets": []map[string]string{{"id": sha256, "checksum": sum}}}
	if err := call(c, "POST", "/assets/bulk-upload-check", in, &r, 60*time.Second); err != nil {
		return "", err
	}
	for _, x := range r.Results {
		if x.Action == "reject" && x.AssetID != "" {
			return x.AssetID, nil
		}
	}
	return "", nil
}

// isoformat is Python's datetime.isoformat of an aware time: microseconds only where there are some.
func isoformat(t time.Time) string {
	if t.Nanosecond()/1000 != 0 {
		return t.Format("2006-01-02T15:04:05.000000-07:00")
	}
	return t.Format("2006-01-02T15:04:05-07:00")
}

func (Immich) Store(c *plugins.Context, path string, meta M) (string, error) {
	dateMS, err := metaDate(meta, path)
	if err != nil {
		return "", err
	}
	tmp, err := PreparedCopy(path, dateMS, makeOf(c), metaStr(meta, "service"))
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	w := when(dateMS)
	stamp := isoformat(w)
	service := metaStr(meta, "service")
	if service == "" {
		service = "chat"
	}
	name := service + " " + w.Format("2006-01-02 150405") + ext(path)
	id := metaStr(meta, "sha256")
	if id == "" {
		if id, err = sha256File(path); err != nil {
			return "", err
		}
	}
	mimeType := metaStr(meta, "mime")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	var rb [16]byte
	rand.Read(rb[:])
	boundary := hex.EncodeToString(rb[:])
	var head bytes.Buffer
	for _, kv := range [][2]string{{"deviceAssetId", id}, {"deviceId", "everysaid"}, {"fileCreatedAt", stamp},
		{"fileModifiedAt", stamp}} {
		fmt.Fprintf(&head, "--%s\r\nContent-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n", boundary, kv[0], kv[1])
	}
	fmt.Fprintf(&head, "--%s\r\nContent-Disposition: form-data; name=\"assetData\"; filename=\"%s\"\r\n"+
		"Content-Type: %s\r\n\r\n", boundary, strings.ReplaceAll(name, `"`, `%22`), mimeType)
	tail := []byte("\r\n--" + boundary + "--\r\n")
	f, err := os.Open(tmp)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	body := io.MultiReader(bytes.NewReader(head.Bytes()), f, bytes.NewReader(tail))
	data, _, err := request(c, "POST", "/assets", body, int64(head.Len())+st.Size()+int64(len(tail)),
		"multipart/form-data; boundary="+boundary, 600*time.Second)
	if err != nil {
		return "", err
	}
	var r struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", err
	}
	if r.ID == "" {
		return "", fmt.Errorf("immich gave no id")
	}
	return r.ID, nil
}

func (Immich) Fetch(c *plugins.Context, ref, size string) (*plugins.Fetched, error) {
	asset := "/assets/" + url.PathEscape(ref)
	path := asset + "/original"
	switch size {
	case "original":
	case "preview", "":
		path = asset + "/thumbnail?size=preview"
	default:
		path = asset + "/thumbnail?size=thumbnail"
	}
	data, ctype, err := request(c, "GET", path, nil, -1, "", 60*time.Second)
	if err != nil {
		return nil, err
	}
	return &plugins.Fetched{Data: data, Type: ctype}, nil
}
