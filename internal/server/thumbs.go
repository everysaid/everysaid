// Previews of pictures and videos. Go has no lossy WebP encoder worth trusting yet, so they are
// JPEG (PNG where the picture has transparency, as stickers do), in the cache folder.
package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"everysaid/internal/config"
)

// Thumbs is where the previews are kept: <cache>/thumbs.
func Thumbs() string { return filepath.Join(config.Cache, "thumbs") }

// extraTypes are the types a system's tables may not know.
var extraTypes = map[string]string{".heic": "image/heic", ".heif": "image/heif", ".webp": "image/webp",
	".avif": "image/avif", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
	".mp4": "video/mp4", ".mov": "video/quicktime", ".m4v": "video/x-m4v", ".3gp": "video/3gpp", ".webm": "video/webm",
	".mkv": "video/x-matroska", ".avi": "video/x-msvideo", ".opus": "audio/ogg", ".ogg": "audio/ogg", ".m4a": "audio/mp4",
	".mp3": "audio/mpeg", ".amr": "audio/amr", ".aac": "audio/aac", ".caf": "audio/x-caf", ".txt": "text/plain",
	".pdf": "application/pdf", ".vcf": "text/vcard", ".webmanifest": "application/manifest+json", ".js": "text/javascript",
	".svg": "image/svg+xml", ".ico": "image/vnd.microsoft.icon"}

// guessType is a file's type by its name ("" when not known), as Python's mimetypes.guess_type.
func guessType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if t, ok := extraTypes[ext]; ok {
		return t
	}
	t := mime.TypeByExtension(ext)
	if i := strings.Index(t, ";"); i >= 0 && !strings.HasPrefix(t, "text/") {
		t = t[:i]
	}
	return t
}

// existingThumb is the preview made before, if any.
func existingThumb(sha, size string) (string, string) {
	for _, ext := range []string{".jpg", ".png"} {
		p := filepath.Join(Thumbs(), sha+"-"+size+ext)
		if _, err := os.Stat(p); err == nil {
			if ext == ".png" {
				return p, "image/png"
			}
			return p, "image/jpeg"
		}
	}
	return "", ""
}

// MakeThumb is a preview of a picture (or of a video's first second, with ffmpeg), cached: its path
// and type, or "" when none can be made.
func MakeThumb(path, sha, size string) (string, string) {
	if p, t := existingThumb(sha, size); p != "" {
		return p, t
	}
	if err := os.MkdirAll(Thumbs(), 0o700); err != nil {
		return "", ""
	}
	edge := 1600
	if size == "thumb" {
		edge = 360
	}
	var img image.Image
	orientation := 1
	if strings.HasPrefix(guessType(path), "video/") {
		img = ffmpegFrame(path, true)
	} else {
		img, orientation = decodePicture(path)
		if img == nil { // HEIC and what else Go cannot read: ffmpeg can, where it is installed
			img = ffmpegFrame(path, false)
		}
	}
	if img == nil {
		return "", ""
	}
	img = orient(fit(img, edge), orientation) // turned once small: far less to move
	var buf bytes.Buffer
	ext, typ := ".jpg", "image/jpeg"
	if opaque(img) {
		if err := jpeg.Encode(&buf, flatten(img), &jpeg.Options{Quality: 80}); err != nil {
			return "", ""
		}
	} else {
		ext, typ = ".png", "image/png"
		if err := png.Encode(&buf, img); err != nil {
			return "", ""
		}
	}
	out := filepath.Join(Thumbs(), sha+"-"+size+ext)
	tmp := out + ".part"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return "", ""
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		return "", ""
	}
	return out, typ
}

// decodePicture is a picture Go can read, and its EXIF orientation.
func decodePicture(path string) (image.Image, int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 1
	}
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, 1
	}
	if format == "jpeg" {
		return img, jpegOrientation(b)
	}
	return img, 1
}

// ffmpegFrame is a frame of a video (at its first second, else its first), or a picture ffmpeg can
// read; nil without ffmpeg.
func ffmpegFrame(path string, video bool) image.Image {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil
	}
	run := func(args ...string) []byte {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, ff, args...).Output()
		return out
	}
	var out []byte
	if video {
		out = run("-v", "quiet", "-ss", "1", "-i", path, "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "-")
	}
	if len(out) == 0 {
		out = run("-v", "quiet", "-i", path, "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "-")
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		return nil
	}
	return img
}

// fit is the picture within an edge × edge box, never larger than it was.
func fit(img image.Image, edge int) image.Image {
	b := img.Bounds()
	w, hgt := b.Dx(), b.Dy()
	if w <= edge && hgt <= edge {
		return img
	}
	nw, nh := edge, edge
	if w > hgt {
		nh = max(1, int(float64(hgt)*float64(edge)/float64(w)+0.5))
	} else {
		nw = max(1, int(float64(w)*float64(edge)/float64(hgt)+0.5))
	}
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

func opaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	return true
}

// flatten is the picture on white (a JPEG has no transparency).
func flatten(img image.Image) image.Image {
	if _, ok := img.(*image.YCbCr); ok {
		return img
	}
	dst := image.NewRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Over)
	return dst
}

// jpegOrientation is the EXIF orientation of a JPEG (1: as stored).
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xD8 || marker >= 0xD0 && marker <= 0xD7 || marker == 0x01 {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // the picture itself: no EXIF before it
			return 1
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n; k++ {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			v := int(bo.Uint16(t[e+8:]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient turns a picture as its EXIF orientation says (Pillow's exif_transpose).
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, hgt := b.Dx(), b.Dy()
	dw, dh := w, hgt
	if o >= 5 {
		dw, dh = hgt, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < hgt; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, hgt-1-y
			case 4:
				dx, dy = x, hgt-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = hgt-1-y, x
			case 7:
				dx, dy = hgt-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
