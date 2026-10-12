package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"strings"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Preview is a link's preview as its service sent it (link_preview): the link, the site's name,
// the title and description, and its picture in any format Go reads (JPEG, PNG, GIF, WebP); nil:
// none (one already kept stays).
type Preview struct {
	URL, Site, Title, Description string
	Image                         []byte
}

// previewEdge is the longest side of a preview's picture as kept.
const previewEdge = 480

// SetPreview writes (or brings up to date) a message's link preview; it says whether anything
// changed. A preview with neither title nor description nor picture says nothing (none kept); a
// picture that cannot be read is left out, as is one the preview no longer has (the one kept
// stays: a source read again without its pictures takes none away).
func (a *Archive) SetPreview(messageID int64, p *Preview) bool {
	if p == nil {
		return false
	}
	url, site := strings.TrimSpace(p.URL), strings.TrimSpace(p.Site)
	title, desc := strings.TrimSpace(p.Title), strings.TrimSpace(p.Description)
	img := a.previewImage(p.Image)
	if title == "" && desc == "" && img == "" {
		return false
	}
	var oldURL, oldSite, oldTitle, oldDesc, oldImg *string
	had := a.Row("SELECT url, site, title, description, image FROM link_preview WHERE message_id = ?", []any{messageID},
		&oldURL, &oldSite, &oldTitle, &oldDesc, &oldImg)
	if img == "" && oldImg != nil {
		img = *oldImg
	}
	str := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	if had && str(oldURL) == url && str(oldSite) == site && str(oldTitle) == title && str(oldDesc) == desc && str(oldImg) == img {
		return false
	}
	a.Exec("INSERT INTO link_preview (message_id, url, site, title, description, image) VALUES (?, ?, ?, ?, ?, ?) "+
		"ON CONFLICT (message_id) DO UPDATE SET url = excluded.url, site = excluded.site, title = excluded.title, "+
		"description = excluded.description, image = excluded.image",
		messageID, nullStr(url), nullStr(site), nullStr(title), nullStr(desc), nullStr(img))
	if had && oldImg != nil && *oldImg != img {
		a.Exec("DELETE FROM link_preview_image WHERE sha256 = ? AND NOT EXISTS (SELECT 1 FROM link_preview WHERE image = ?)",
			*oldImg, *oldImg)
	}
	return true
}

// previewImage keeps a preview's picture, small, as JPEG (once for the same picture); its sha256,
// "" when there is none or it cannot be read.
func (a *Archive) previewImage(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(b)); err != nil || cfg.Width <= 0 || cfg.Height <= 0 ||
		cfg.Width*cfg.Height > 50_000_000 {
		return ""
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return ""
	}
	small := previewFit(src)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, small, &jpeg.Options{Quality: 82}); err != nil {
		return ""
	}
	sum := sha256.Sum256(out.Bytes())
	sha := hex.EncodeToString(sum[:])
	r := small.Bounds()
	a.Exec("INSERT INTO link_preview_image (sha256, jpeg, width, height) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING",
		sha, out.Bytes(), r.Dx(), r.Dy())
	return sha
}

// previewFit is the picture within previewEdge, never larger than it was, on white where it was
// transparent (JPEG has no transparency).
func previewFit(img image.Image) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if w > previewEdge || h > previewEdge {
		if w > h {
			nw, nh = previewEdge, max(1, int(float64(h)*previewEdge/float64(w)+0.5))
		} else {
			nw, nh = max(1, int(float64(w)*previewEdge/float64(h)+0.5)), previewEdge
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	return dst
}
