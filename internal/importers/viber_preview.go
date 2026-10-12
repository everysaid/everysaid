package importers

// Viber's link previews: the link, its title (and description, where the desktop has one) as the
// sender's app made them (the iPhone's ZMETADATA, the desktop's Info: URL, Title, ThumbnailURL),
// kept on messages new and old. The picture is not in the message, only its address on the linked
// site: it is fetched from there (the picture alone, never the page), through netsafe (the internet
// only), once, at a gentle pace and for a bounded time each import (the rest on the next one), and
// an address that failed is not tried again for a while (remembered in the cache, not the archive).

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/netsafe"
)

// viberLink is a message's preview, and the address of its picture ("" for none).
type viberLink struct {
	preview archive.Preview
	thumb   string
}

// viberPreview is the preview a message's metadata carries; nil for none (no link, or neither a
// title nor a description nor a picture).
func viberPreview(md map[string]any) *viberLink {
	u := strings.TrimSpace(str(md["URL"]))
	if u == "" {
		return nil
	}
	l := &viberLink{preview: archive.Preview{URL: u, Title: strings.TrimSpace(str(md["Title"])),
		Description: strings.TrimSpace(str(md["Description"]))}, thumb: strings.TrimSpace(str(md["ThumbnailURL"]))}
	if l.preview.Title == "" && l.preview.Description == "" && l.thumb == "" {
		return nil
	}
	return l
}

// viberLinks are the previews of a run by token, the iPhone's and the desktop's of the same message
// as one (what one has not, taken from the other).
type viberLinks struct {
	by    map[string]*viberLink
	order []string
}

// add keeps a message's preview; the one kept (shared: a later source's fills it in).
func (v *viberLinks) add(key string, l *viberLink) *viberLink {
	if v.by == nil {
		v.by = map[string]*viberLink{}
	}
	have, ok := v.by[key]
	if !ok {
		v.by[key] = l
		v.order = append(v.order, key)
		return l
	}
	fill := func(dst *string, src string) {
		if *dst == "" {
			*dst = src
		}
	}
	fill(&have.preview.URL, l.preview.URL)
	fill(&have.preview.Title, l.preview.Title)
	fill(&have.preview.Description, l.preview.Description)
	fill(&have.thumb, l.thumb)
	return have
}

// apply brings the previews to the messages the archive had (a new one has its own already); how
// many changed.
func (v *viberLinks) apply(a *archive.Archive) int {
	n := 0
	for _, k := range v.order {
		if mid, ok := a.MessageByKey("viber", k, 0); ok && mid != 0 && a.SetPreview(mid, &v.by[k].preview) {
			n++
		}
	}
	return n
}

// pictureJob is a picture to fetch, and the messages whose preview it is.
type pictureJob struct {
	url      string
	mids     []int64
	previews []*archive.Preview
}

// jobs are the pictures the archive has not yet, but for addresses that failed lately.
func (v *viberLinks) jobs(a *archive.Archive, fails *previewFails, now time.Time) []*pictureJob {
	var out []*pictureJob
	byURL := map[string]*pictureJob{}
	for _, k := range v.order {
		l := v.by[k]
		if l.thumb == "" || fails.skip(l.thumb, now) {
			continue
		}
		mid, ok := a.MessageByKey("viber", k, 0)
		if !ok || mid == 0 || a.Exists("SELECT 1 FROM link_preview WHERE message_id = ? AND image IS NOT NULL", mid) {
			continue
		}
		j := byURL[l.thumb]
		if j == nil {
			j = &pictureJob{url: l.thumb}
			byURL[l.thumb] = j
			out = append(out, j)
		}
		j.mids = append(j.mids, mid)
		j.previews = append(j.previews, &l.preview)
	}
	return out
}

const previewUserAgent = "Everysaid (link preview picture)"

var (
	// previewFetch fetches a picture (a variable: the tests' sites are on this machine).
	previewFetch = func(ctx context.Context, u string) ([]byte, error) {
		return netsafe.Image(ctx, u, previewUserAgent)
	}
	previewBudget  = 2 * time.Minute // for the pictures of one import; the rest on the next
	previewSpacing = time.Second     // between two requests to the same site
	previewWorkers = 4               // sites asked at once
)

// fetchPictures fetches the jobs' pictures, a few sites at once and one request at a time to each,
// for at most previewBudget; the pictures (nil: failed or not reached), and the errors (nil for
// those not tried).
func fetchPictures(jobs []*pictureJob) ([][]byte, []error) {
	got, errs := make([][]byte, len(jobs)), make([]error, len(jobs))
	bySite := map[string][]int{}
	var sites []string
	for i, j := range jobs {
		h := ""
		if u, err := url.Parse(j.url); err == nil {
			h = strings.ToLower(u.Hostname())
		}
		if _, ok := bySite[h]; !ok {
			sites = append(sites, h)
		}
		bySite[h] = append(bySite[h], i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), previewBudget)
	defer cancel()
	next := make(chan string)
	var wg sync.WaitGroup
	for range min(previewWorkers, len(sites)) {
		wg.Go(func() {
			for s := range next {
				for n, i := range bySite[s] {
					if n > 0 {
						select {
						case <-ctx.Done():
						case <-time.After(previewSpacing):
						}
					}
					if ctx.Err() != nil {
						break
					}
					b, err := previewFetch(ctx, jobs[i].url)
					if ctx.Err() != nil { // cut off by the budget: not the site's fault
						break
					}
					got[i], errs[i] = b, err
				}
			}
		})
	}
	for _, s := range sites {
		next <- s
	}
	close(next)
	wg.Wait()
	return got, errs
}

// viberPictures fetches the pictures the archive's previews have not yet, outside the import's
// transaction (committed first: nobody waits on the archive meanwhile), and keeps them.
func viberPictures(a *archive.Archive, out func(string), links *viberLinks) {
	if len(links.order) == 0 {
		return
	}
	now := time.Now()
	fails := loadPreviewFails()
	jobs := links.jobs(a, fails, now)
	if len(jobs) == 0 {
		return
	}
	a.Commit()
	got, errs := fetchPictures(jobs)
	kept, failed, left := 0, 0, 0
	for i, j := range jobs {
		switch {
		case got[i] != nil:
			fails.forget(j.url)
			for n, mid := range j.mids {
				p := *j.previews[n]
				p.Image = got[i]
				if a.SetPreview(mid, &p) {
					kept++
				}
			}
		case errs[i] != nil:
			fails.note(j.url, permanentFail(errs[i]), now)
			failed++
		default:
			left++
		}
	}
	a.Commit()
	fails.save(now)
	say(out, "link preview pictures: {kept} kept, {failed} could not be fetched, {left} left for the next import",
		map[string]any{"kept": kept, "failed": failed, "left": left})
}

// permanentFail says whether a fetch failed for good (no such site or page, no picture, refused):
// tried again only after previewRetryPermanent; else (the network, the site busy) after a day.
func permanentFail(err error) bool {
	var se *netsafe.StatusError
	if errors.As(err, &se) {
		return se.Code >= 400 && se.Code < 500 && se.Code != 408 && se.Code != 429
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return de.IsNotFound
	}
	return errors.Is(err, netsafe.ErrNotPublic) || errors.Is(err, netsafe.ErrURL) ||
		errors.Is(err, netsafe.ErrNotImage) || errors.Is(err, netsafe.ErrTooLarge)
}

const (
	previewRetryPermanent = 90 * 24 * time.Hour
	previewRetryTransient = 24 * time.Hour
)

type previewFail struct {
	at        int64 // Unix seconds
	permanent bool
}

// previewFails are the picture addresses that failed lately (config.Cache/link-preview-failed.tsv:
// when, "p" or "t", the address).
type previewFails struct {
	path    string
	by      map[string]previewFail
	changed bool
}

func loadPreviewFails() *previewFails {
	f := &previewFails{path: filepath.Join(config.Cache, "link-preview-failed.tsv"), by: map[string]previewFail{}}
	file, err := os.Open(f.path)
	if err != nil {
		return f
	}
	defer file.Close()
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		at, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		f.by[parts[2]] = previewFail{at, parts[1] == "p"}
	}
	return f
}

func (f *previewFails) expired(e previewFail, now time.Time) bool {
	wait := previewRetryTransient
	if e.permanent {
		wait = previewRetryPermanent
	}
	return now.Sub(time.Unix(e.at, 0)) >= wait
}

func (f *previewFails) skip(u string, now time.Time) bool {
	e, ok := f.by[u]
	return ok && !f.expired(e, now)
}

func (f *previewFails) note(u string, permanent bool, now time.Time) {
	f.by[u] = previewFail{now.Unix(), permanent}
	f.changed = true
}

func (f *previewFails) forget(u string) {
	if _, ok := f.by[u]; ok {
		delete(f.by, u)
		f.changed = true
	}
}

// save writes them back, those whose time is up left out; a cache: a failure to write is only a
// later retry.
func (f *previewFails) save(now time.Time) {
	if !f.changed {
		return
	}
	us := make([]string, 0, len(f.by))
	for u, e := range f.by {
		if !f.expired(e, now) {
			us = append(us, u)
		}
	}
	sort.Strings(us)
	var b strings.Builder
	for _, u := range us {
		kind := "t"
		if f.by[u].permanent {
			kind = "p"
		}
		fmt.Fprintf(&b, "%d\t%s\t%s\n", f.by[u].at, kind, strings.ReplaceAll(u, "\n", ""))
	}
	if os.MkdirAll(filepath.Dir(f.path), 0o700) != nil {
		return
	}
	tmp := f.path + ".tmp"
	if os.WriteFile(tmp, []byte(b.String()), 0o600) == nil {
		os.Rename(tmp, f.path)
	}
}
