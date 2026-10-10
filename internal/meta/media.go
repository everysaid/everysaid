package meta

// The files of messages: downloaded as they arrive (Meta's addresses expire), into the instance's
// download folder, and from there into the archive's store at once (importers.Store with Move: the
// download leaves the folder once its link is committed). The list of what is still to download is
// in keys.db (meta_file), so that a restart goes on with it; a file that cannot be had after a few
// tries, or whose address is too old, is given up (its message stays, without it).
//
// Downloads from Meta's CDN go as its web client's do (mautrix-meta's pkg/msgconv/mediadl): its
// browser's headers; a video that redirects to video.xx.fbcdn.net is fetched there in pieces.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/useragent"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

// maxFile is the largest file downloaded (Meta's own limit for a file is 100 MB, a video more).
const maxFile = 2 << 30

// tooOld: a file waiting longer than this is given up (its address has expired long ago).
const tooOld = 7 * 24 * time.Hour

// mediaDir is the instance's download folder.
func mediaDir(iid int64) string {
	return filepath.Join(config.Cache, "meta", strconv.FormatInt(iid, 10), "media")
}

var mediaHTTP = &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if req.URL.Hostname() == "video.xx.fbcdn.net" {
		return http.ErrUseLastResponse
	}
	return nil
}}

func downloadHeaders(h http.Header, mimeType string) {
	h.Set("Accept", "*/*")
	switch strings.Split(mimeType, "/")[0] {
	case "image":
		h.Set("Accept", "image/avif,image/webp,*/*")
		h.Set("Sec-Fetch-Dest", "image")
	case "video":
		h.Set("Sec-Fetch-Dest", "video")
	case "audio":
		h.Set("Sec-Fetch-Dest", "audio")
	default:
		h.Set("Sec-Fetch-Dest", "empty")
	}
	h.Set("Sec-Fetch-Mode", "no-cors")
	h.Set("Sec-Fetch-Site", "cross-site")
	h.Set("User-Agent", useragent.UserAgent)
	h.Set("sec-ch-ua", useragent.SecCHUserAgent)
	h.Set("sec-ch-ua-platform", useragent.SecCHPlatform)
}

// errForbidden: the address has expired (or was never ours).
var errForbidden = errors.New("Meta refused the file (its address has expired)")

// fetchURL writes a file of Meta's CDN to w.
func fetchURL(ctx context.Context, u, mimeType string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	downloadHeaders(req.Header, mimeType)
	resp, err := mediaHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusFound {
		if loc, _ := resp.Location(); loc != nil && loc.Hostname() == "video.xx.fbcdn.net" {
			return fetchChunked(ctx, loc.String(), mimeType, w)
		}
	}
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return errForbidden
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	case resp.ContentLength > maxFile:
		return fmt.Errorf("too large (%d bytes)", resp.ContentLength)
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, maxFile))
	return err
}

// fetchChunked fetches a video a megabyte at a time, as Meta's web client does.
func fetchChunked(ctx context.Context, u, mimeType string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return err
	}
	downloadHeaders(req.Header, mimeType)
	resp, err := mediaHTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Accept-Ranges") != "bytes" || resp.ContentLength <= 0 {
		return fetchWhole(ctx, u, mimeType, w)
	}
	size := resp.ContentLength
	if size > maxFile {
		return fmt.Errorf("too large (%d bytes)", size)
	}
	const chunk = 1 << 20
	for off := int64(0); off < size; off += chunk {
		end := min(off+chunk, size) - 1
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		downloadHeaders(req.Header, mimeType)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))
		resp, err := mediaHTTP.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		_, err = io.Copy(w, resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func fetchWhole(ctx context.Context, u, mimeType string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	downloadHeaders(req.Header, mimeType)
	resp, err := mediaHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, maxFile))
	return err
}

// fileName is a downloaded file's name in the folder: its message, its place, an extension.
func fileName(f pendingFile) string {
	ext := filepath.Ext(f.Name)
	if ext == "" && f.Mime != "" {
		if exts, _ := mime.ExtensionsByType(strings.Split(f.Mime, ";")[0]); len(exts) > 0 {
			ext = exts[0]
			if f.Mime == "image/jpeg" {
				ext = ".jpg"
			}
		}
	}
	safe := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*$.`, r) || r < 32 {
			return '_'
		}
		return r
	}, f.Message)
	return fmt.Sprintf("%s-%d%s", safe, f.N, ext)
}

// download fetches a file into the folder; its path.
func (s *liveSession) download(ctx context.Context, f pendingFile) (string, error) {
	dir := mediaDir(s.c.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fileName(f))
	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if len(f.WA) > 0 {
		err = s.downloadWA(ctx, f, tmp)
	} else {
		err = fetchURL(ctx, f.URL, f.Mime, tmp)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}

// downloadWA fetches an encrypted chat's file through whatsmeow.
func (s *liveSession) downloadWA(ctx context.Context, f pendingFile, w io.Writer) error {
	e := s.waClient()
	if e == nil {
		return errors.New("not connected to the encrypted chats")
	}
	var wf waFile
	if err := json.Unmarshal(f.WA, &wf); err != nil {
		return err
	}
	var integral waMediaTransport.WAMediaTransport_Integral
	if err := proto.Unmarshal(wf.Integral, &integral); err != nil {
		return err
	}
	data, err := e.DownloadFB(ctx, &integral, wf.Type)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// link takes downloaded files into the archive's store, linked to their messages.
func link(c *plugins.Context, service string, files map[string]pendingFile) error {
	_, _, err := sourcekit.RunImporters(c, []sourcekit.Step{{Label: "files", Run: func(a *archive.Archive, out func(string)) error {
		w := writer(a, c, service)
		st := importers.NewStore(a)
		st.Move = true
		for path, f := range files {
			mid := w.Message(f.Thread, f.Message)
			if mid == 0 {
				continue // a message the archive does not have (yet)
			}
			st.LinkNamed(w.SourceName, w.Source, path, filepath.Base(path), mid, f.Name)
		}
		a.Commit()
		st.Release()
		return nil
	}}})
	return err
}

// fetchFiles downloads what is still to download (of one message where given) and links it; it
// says how many it took.
func (s *liveSession) fetchFiles(ctx context.Context, message string) int {
	pending, err := pendingFiles(s.c.ID, message)
	if err != nil {
		s.c.Logf("files: %v", err)
		return 0
	}
	got := map[string]pendingFile{}
	for _, f := range pending {
		if ctx.Err() != nil {
			break
		}
		if time.Since(time.Unix(f.At, 0)) > tooOld || f.Tries >= 5 {
			s.c.Log("{file}: not downloaded ({e})", map[string]any{"file": fileName(f), "e": "gave up"})
			fileDone(s.c.ID, f)
			continue
		}
		path, err := s.download(ctx, f)
		if err != nil {
			s.c.Log("{file}: not downloaded ({e})", map[string]any{"file": fileName(f), "e": err.Error()})
			if errors.Is(err, errForbidden) {
				fileDone(s.c.ID, f)
			} else {
				fileTried(s.c.ID, f)
			}
			continue
		}
		got[path] = f
	}
	if len(got) == 0 {
		return 0
	}
	if err := link(s.c, s.service, got); err != nil {
		s.c.Logf("files: %v", err)
		return 0
	}
	for _, f := range got {
		fileDone(s.c.ID, f)
	}
	return len(got)
}

// downloader fetches files as they are queued, until ctx ends.
func (s *liveSession) downloader(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		if s.c.Bool("media") {
			s.fetchFiles(ctx, "")
		}
	}
}

// wakeDownloader asks the downloader to look at the list.
func (s *liveSession) wakeDownloader() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
