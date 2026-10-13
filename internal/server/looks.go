package server

import (
	"fmt"
	"image"
	"os"
	"sync"

	xdraw "golang.org/x/image/draw"
)

// A GIF's look: a hash of its preview frame (a difference hash, 64 bits) that the same picture keeps
// when a service encodes it again (another size, another quality), so that the archive's GIFs can
// be shown once each. Kept in memory: made again from the cached previews.

var looks = struct {
	sync.Mutex
	of map[string]string
}{of: map[string]string{}}

// look is the file's look ("" where none can be made, or where its frame is nearly one colour: a
// black start would make many GIFs one).
func look(path, sha string) string {
	looks.Lock()
	l, ok := looks.of[sha]
	looks.Unlock()
	if ok {
		return l
	}
	l = makeLook(path, sha)
	looks.Lock()
	looks.of[sha] = l
	looks.Unlock()
	return l
}

func makeLook(path, sha string) string {
	thumb, _ := MakeThumb(path, sha, "thumb")
	if thumb == "" {
		return ""
	}
	f, err := os.Open(thumb)
	if err != nil {
		return ""
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return ""
	}
	return dHash(img)
}

// dHash compares each of 8 rows' 9 neighbouring greys (the picture made 9×8): a bit for each that is
// brighter than the next. "" for a picture of nearly one grey.
func dHash(img image.Image) string {
	small := image.NewGray(image.Rect(0, 0, 9, 8))
	xdraw.ApproxBiLinear.Scale(small, small.Bounds(), img, img.Bounds(), xdraw.Src, nil)
	lo, hi := uint8(255), uint8(0)
	var h uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 9; x++ {
			g := small.GrayAt(x, y).Y
			lo, hi = min(lo, g), max(hi, g)
			if x < 8 && g > small.GrayAt(x+1, y).Y {
				h |= 1 << (y*8 + x)
			}
		}
	}
	if hi-lo < 24 {
		return ""
	}
	return fmt.Sprintf("%016x", h)
}

// addLooks gives each GIF that is here its look (a few at once: each new one is a frame from ffmpeg).
func (s *Server) addLooks(items []M) {
	var wg sync.WaitGroup
	work := make(chan M)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range work {
				sha, _ := it["sha256"].(string)
				if path := s.Host.LocalFile(sha); path != "" {
					it["look"] = look(path, sha)
				}
			}
		}()
	}
	for _, it := range items {
		work <- it
	}
	close(work)
	wg.Wait()
}
