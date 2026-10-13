package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/bits"
	"strconv"
	"testing"

	xdraw "golang.org/x/image/draw"
)

// The same picture at another size and quality looks nearly the same; another does not; one of
// nearly one colour has no look.
func TestDHash(t *testing.T) {
	pic := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			pic.Set(x, y, color.RGBA{uint8(x * 255 / 320), uint8(y), uint8(128 + 100*math.Sin(float64(x)/23)*math.Cos(float64(y)/17)), 255})
		}
	}
	small := image.NewRGBA(image.Rect(0, 0, 160, 120))
	xdraw.CatmullRom.Scale(small, small.Bounds(), pic, pic.Bounds(), xdraw.Src, nil)
	var b bytes.Buffer
	jpeg.Encode(&b, small, &jpeg.Options{Quality: 40})
	again, _ := jpeg.Decode(&b)

	other := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			other.Set(x, y, color.RGBA{uint8(255 - x*255/320), uint8(240 - y), 90, 255})
		}
	}
	dist := func(a, b string) int {
		x, _ := strconv.ParseUint(a, 16, 64)
		y, _ := strconv.ParseUint(b, 16, 64)
		return bits.OnesCount64(x ^ y)
	}
	h1, h2, h3 := dHash(pic), dHash(again), dHash(other)
	if h1 == "" || dist(h1, h2) > 2 || dist(h1, h3) < 20 {
		t.Fatalf("%s %s %s: %d %d", h1, h2, h3, dist(h1, h2), dist(h1, h3))
	}
	flat := image.NewGray(image.Rect(0, 0, 64, 64))
	for i := range flat.Pix {
		flat.Pix[i] = uint8(30 + i%5)
	}
	if dHash(flat) != "" {
		t.Fatal("one colour")
	}
}
