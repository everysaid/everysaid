package server

import (
	"image"
	"testing"
)

// A thumbnail keeps the shorter side sharp: a banner is cut to 3:1, not shrunk to a sliver.
func TestCover(t *testing.T) {
	for _, c := range []struct{ w, h, ww, wh int }{
		{1238, 103, 309, 103},  // a banner: cut, not scaled (smaller than the edge)
		{4000, 3000, 853, 640}, // a photo: scaled to the edge
		{1000, 5000, 640, 1920},
		{300, 200, 300, 200}, // small: as it was
	} {
		got := cover(image.NewRGBA(image.Rect(0, 0, c.w, c.h)), thumbEdge, thumbRatio).Bounds()
		if got.Dx() != c.ww || got.Dy() != c.wh {
			t.Errorf("%dx%d: %dx%d", c.w, c.h, got.Dx(), got.Dy())
		}
	}
}
