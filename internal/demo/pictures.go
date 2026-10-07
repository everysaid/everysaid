// Ports the pictures of everysaid/demo.py (picture, avatar), drawn as Pillow 12 draws them
// (libImaging/Draw.c, Paste.c, _imagingft.c) so that each file is the Python's, byte for byte.

package demo

import (
	"os"

	"everysaid/internal/pyrandom"
)

// canvas is an RGB image as Pillow keeps one.
type canvas struct {
	w, h int
	pix  []byte // RGB, row by row
}

func newCanvas(w, h int, c [3]byte) *canvas {
	im := &canvas{w: w, h: h, pix: make([]byte, w*h*3)}
	for i := 0; i < w*h; i++ {
		copy(im.pix[i*3:], c[:])
	}
	return im
}

func (im *canvas) point(x, y int, c [3]byte) {
	if x >= 0 && x < im.w && y >= 0 && y < im.h {
		copy(im.pix[(y*im.w+x)*3:], c[:])
	}
}

// hline is Draw.c's hline32: x0 to x1, both included, clipped.
func (im *canvas) hline(x0, y0, x1 int, c [3]byte) {
	if y0 < 0 || y0 >= im.h {
		return
	}
	if x0 < 0 {
		x0 = 0
	} else if x0 >= im.w {
		return
	}
	if x1 < 0 {
		return
	} else if x1 >= im.w {
		x1 = im.w - 1
	}
	for ; x0 <= x1; x0++ {
		copy(im.pix[(y0*im.w+x0)*3:], c[:])
	}
}

// line is ImageDraw.line of width 1 between two points of the same row (all the demo draws):
// line32's horizontal case, which stops before the end point, then the end point (_draw_lines).
func (im *canvas) hlineSegment(x0, x1, y int, c [3]byte) {
	dx, xs := x1-x0, 1
	if dx < 0 {
		dx, xs = -dx, -1
	}
	x := x0
	for i := 0; i < dx; i++ {
		im.point(x, y, c)
		x += xs
	}
	im.point(x1, y, c)
}

// rectangle is ImagingDrawRectangle, filled.
func (im *canvas) rectangle(x0, y0, x1, y1 int, c [3]byte) {
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	if y0 < 0 {
		y0 = 0
	} else if y0 >= im.h {
		return
	}
	if y1 < 0 {
		return
	} else if y1 > im.h {
		y1 = im.h
	}
	for y := y0; y <= y1; y++ {
		im.hline(x0, y, x1, c)
	}
}

// quarter and ellipseState are Draw.c's quarter_* and ellipse_*: a quarter of the ellipse on a
// grid of step 2, then the four quarters joined into horizontal segments.
type quarter struct {
	a, b, cx, cy, ex, ey int32
	a2, b2, a2b2         int64
	finished             bool
}

func (s *quarter) init(a, b int32) {
	if a < 0 || b < 0 {
		s.finished = true
		return
	}
	*s = quarter{a: a, b: b, cx: a, cy: b % 2, ex: a % 2, ey: b}
	s.a2 = int64(a) * int64(a)
	s.b2 = int64(b) * int64(b)
	s.a2b2 = s.a2 * s.b2
}

func (s *quarter) delta(x, y int64) int64 {
	d := s.a2*y*y + s.b2*x*x - s.a2b2
	if d < 0 {
		return -d
	}
	return d
}

func (s *quarter) next() (x, y int32, ok bool) {
	if s.finished {
		return 0, 0, false
	}
	x, y = s.cx, s.cy
	if s.cx == s.ex && s.cy == s.ey {
		s.finished = true
	} else {
		nx, ny := s.cx, s.cy+2
		nd := s.delta(int64(nx), int64(ny))
		if nx > 1 {
			d := s.delta(int64(s.cx-2), int64(s.cy+2))
			if nd > d {
				nx, ny, nd = s.cx-2, s.cy+2, d
			}
			d = s.delta(int64(s.cx-2), int64(s.cy))
			if nd > d {
				nx, ny = s.cx-2, s.cy
			}
		}
		s.cx, s.cy = nx, ny
	}
	return x, y, true
}

type ellipseState struct {
	o, i       quarter
	py, pl, pr int32
	cy, cl, cr [4]int32
	bufcnt     int
	finished   bool
	leftmost   int32
}

func (s *ellipseState) init(a, b, w int32) {
	s.bufcnt = 0
	s.leftmost = a % 2
	s.o.init(a, b)
	var ok bool
	if w >= 1 {
		s.pr, s.py, ok = s.o.next()
	}
	if !ok {
		s.finished = true
		return
	}
	s.finished = false
	s.i.init(a-2*(w-1), b-2*(w-1))
	s.pl = s.leftmost
}

func (s *ellipseState) next() (x0, y, x1 int32, ok bool) {
	if s.bufcnt == 0 {
		if s.finished {
			return 0, 0, 0, false
		}
		y := s.py
		l, r := s.pl, s.pr
		var cx, cy int32
		var more bool
		for {
			cx, cy, more = s.o.next()
			if !more || cy > y {
				break
			}
		}
		if !more {
			s.finished = true
		} else {
			s.pr, s.py = cx, cy
		}
		for {
			cx, cy, more = s.i.next()
			if !more || cy > y {
				break
			}
			l = cx
		}
		if more {
			s.pl = cx
		} else {
			s.pl = s.leftmost
		}
		lo := l
		if l == 0 {
			lo = 2
		}
		if (l > 0 || l < r) && y > 0 {
			s.cl[s.bufcnt], s.cy[s.bufcnt], s.cr[s.bufcnt] = lo, y, r
			s.bufcnt++
		}
		if y > 0 {
			s.cl[s.bufcnt], s.cy[s.bufcnt], s.cr[s.bufcnt] = -r, y, -l
			s.bufcnt++
		}
		if l > 0 || l < r {
			s.cl[s.bufcnt], s.cy[s.bufcnt], s.cr[s.bufcnt] = lo, -y, r
			s.bufcnt++
		}
		s.cl[s.bufcnt], s.cy[s.bufcnt], s.cr[s.bufcnt] = -r, -y, -l
		s.bufcnt++
	}
	s.bufcnt--
	return s.cl[s.bufcnt], s.cy[s.bufcnt], s.cr[s.bufcnt], true
}

// ellipse is ImagingDrawEllipse, filled (ellipseNew).
func (im *canvas) ellipse(x0, y0, x1, y1 int, c [3]byte) {
	a, b := int32(x1-x0), int32(y1-y0)
	if a < 0 || b < 0 {
		return
	}
	var st ellipseState
	st.init(a, b, a+b)
	for {
		X0, Y, X1, ok := st.next()
		if !ok {
			break
		}
		im.hline(x0+int((X0+a)/2), y0+int((Y+b)/2), x0+int((X1+a)/2), c)
	}
}

// glyph is one character as FreeType renders it (font_data.go).
type glyph struct {
	advance     int    // 26.6
	cbox        [4]int // xMin, yMin, xMax, yMax in pixels
	left, top   int
	width, rows int
	pixels      []byte
}

func pixel(x int) int { return ((x + 32) & -64) >> 6 }

func clip8(v int) byte {
	if v <= 0 {
		return 0
	}
	if v < 256 {
		return byte(v)
	}
	return 255
}

func div255(a int) int { a += 128; return ((a >> 8) + a) >> 8 }

func blend(mask, in1, in2 int) byte { return byte(div255(in1*(255-mask) + in2*mask)) }

// text is ImageDraw.text at (x, y) with the default font (Aileron at 10, basic layout, no
// kerning in it), anchor "la": the string's mask as font_render makes it, then filled with the ink
// through it (fill_mask_L).
func (im *canvas) text(x, y int, s string, ink [3]byte) {
	var gs []glyph
	for _, r := range s {
		g, ok := glyphs[r]
		if !ok {
			g = glyphs['Α'] // a character the font does not have: its .notdef box
		}
		gs = append(gs, g)
	}
	if len(gs) == 0 {
		return
	}
	// bounding_box_and_anchors
	position, xMin, xMax, yMin, yMax := 0, 0, 0, 0, 0
	for _, g := range gs {
		px := pixel(position)
		position += g.advance
		if adv := pixel(position); adv > xMax {
			xMax = adv
		}
		xMax = max(xMax, g.cbox[2]+px)
		xMin = min(xMin, g.cbox[0]+px)
		yMax = max(yMax, g.cbox[3])
		yMin = min(yMin, g.cbox[1])
	}
	width, height := xMax-xMin, yMax-yMin
	xOffset, yOffset := xMin, pixel(fontAscender)-yMax
	if width == 0 || height == 0 {
		return
	}
	mask := make([]int, width*height)
	// font_render: the pen at the text's origin, each glyph blended in
	px0, top := 0, 0
	for i, pos := 0, 0; i < len(gs); i++ {
		px := pixel(pos)
		top = max(top, gs[i].top)
		px0 = min(px0, gs[i].left+px)
		pos += gs[i].advance
	}
	pen, py := -px0*64, pixel(-top*64)
	for _, g := range gs {
		px := pixel(pen)
		xx, yy := px+g.left, -(py + g.top)
		x0, x1 := 0, g.width
		if xx < 0 {
			x0 = -xx
		}
		if xx+x1 > width {
			x1 = width - xx
		}
		for row := 0; row < g.rows; row, yy = row+1, yy+1 {
			if yy < 0 || yy >= height {
				continue
			}
			for k := x0; k < x1; k++ {
				src := int(g.pixels[row*g.width+k])
				if src == 0 {
					continue
				}
				t := &mask[yy*width+xx+k]
				if *t > 0 {
					m := *t*(255-src) + 128
					*t = int(clip8(src + ((m>>8)+m)>>8))
				} else {
					*t = src
				}
			}
		}
		pen += g.advance
	}
	// ImagingFill2 with the mask, at (x + offset)
	dx, dy := x+xOffset, y+yOffset
	for my := 0; my < height; my++ {
		for mx := 0; mx < width; mx++ {
			ox, oy := dx+mx, dy+my
			if ox < 0 || ox >= im.w || oy < 0 || oy >= im.h {
				continue
			}
			m := mask[my*width+mx]
			p := im.pix[(oy*im.w+ox)*3:]
			for c := 0; c < 3; c++ {
				p[c] = blend(m, int(p[c]), int(ink[c]))
			}
		}
	}
}

// picture is a made-up picture: a gradient with a few shapes and a label.
func picture(path string, seed int64, label string) error {
	rnd := pyrandom.New(seed)
	size := pyrandom.Pick(rnd, [][2]int{{1200, 900}, {900, 1200}, {1080, 1080}})
	w, h := size[0], size[1]
	var a, b [3]int
	for i := range a {
		a[i] = rnd.Randrange(40, 220)
	}
	for i := range b {
		b[i] = rnd.Randrange(40, 220)
	}
	im := newCanvas(w, h, [3]byte{})
	for y := 0; y < h; y++ {
		t := float64(y) / float64(h)
		var c [3]byte
		for i := range c {
			// int() of the float, the products kept apart (no fused multiply-add)
			c[i] = byte(int(float64(float64(a[i])*(1-t)) + float64(float64(b[i])*t)))
		}
		im.hlineSegment(0, w, y, c)
	}
	for k, n := 0, rnd.Randrange(3, 8); k < n; k++ {
		x0, y0 := rnd.RandrangeN(w), rnd.RandrangeN(h)
		r := rnd.Randrange(40, 260)
		var c [3]byte
		for i := range c {
			c[i] = byte(rnd.RandrangeN(255))
		}
		if rnd.Random() < .5 {
			im.ellipse(x0-r, y0-r, x0+r, y0+r, c)
		} else {
			im.rectangle(x0-r, y0-r, x0+r, y0+r, c)
		}
	}
	im.text(40, h-80, label, [3]byte{255, 255, 255})
	return os.WriteFile(path, encodeJPEG(im, 82), 0o666)
}

func avatar(path string, seed int64, initials string) error {
	rnd := pyrandom.New(seed)
	var bg, face, body [3]byte
	for i := range bg {
		bg[i] = byte(rnd.Randrange(60, 200))
	}
	im := newCanvas(256, 256, bg)
	for i := range face {
		face[i] = byte(rnd.Randrange(150, 255))
	}
	im.ellipse(60, 40, 196, 176, face)
	for i := range body {
		body[i] = byte(rnd.Randrange(150, 255))
	}
	im.ellipse(20, 170, 236, 380, body)
	im.text(110, 100, initials, [3]byte{30, 30, 30})
	return os.WriteFile(path, encodeJPEG(im, 85), 0o666)
}
