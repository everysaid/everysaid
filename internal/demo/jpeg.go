// Ports the JPEG encoding Pillow 12 does for Image.save(path, "JPEG", quality=q) of an RGB
// image: libjpeg-turbo 3.1 (jcparam.c, jccolor.c, jcsample.c, jcprepct.c, jccoefct.c, jcdctmgr.c,
// jfdctint.c, jchuff.c, jcmarker.c) with its defaults: JFIF 1.01, YCbCr 4:2:0, the integer slow
// DCT, the standard Huffman tables, no restart markers. Its SIMD routines give the same numbers as
// these C ones (quantize with 16-bit reciprocals, as built WITH_SIMD).

package demo

import "bytes"

var stdLuminance = [64]int{
	16, 11, 10, 16, 24, 40, 51, 61,
	12, 12, 14, 19, 26, 58, 60, 55,
	14, 13, 16, 24, 40, 57, 69, 56,
	14, 17, 22, 29, 51, 87, 80, 62,
	18, 22, 37, 56, 68, 109, 103, 77,
	24, 35, 55, 64, 81, 104, 113, 92,
	49, 64, 78, 87, 103, 121, 120, 101,
	72, 92, 95, 98, 112, 100, 103, 99}

var stdChrominance = [64]int{
	17, 18, 24, 47, 99, 99, 99, 99,
	18, 21, 26, 66, 99, 99, 99, 99,
	24, 26, 56, 99, 99, 99, 99, 99,
	47, 66, 99, 99, 99, 99, 99, 99,
	99, 99, 99, 99, 99, 99, 99, 99,
	99, 99, 99, 99, 99, 99, 99, 99,
	99, 99, 99, 99, 99, 99, 99, 99,
	99, 99, 99, 99, 99, 99, 99, 99}

// naturalOrder[i] is the natural-order position of the i'th element of zigzag order.
var naturalOrder = [64]int{
	0, 1, 8, 16, 9, 2, 3, 10,
	17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34,
	27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36,
	29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46,
	53, 60, 61, 54, 47, 55, 62, 63}

type huffSpec struct {
	bits [17]byte
	vals []byte
}

var (
	dcLuminance   = huffSpec{[17]byte{0, 0, 1, 5, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0}, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}}
	dcChrominance = huffSpec{[17]byte{0, 0, 3, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0}, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}}
	acLuminance   = huffSpec{[17]byte{0, 0, 2, 1, 3, 3, 2, 4, 3, 5, 5, 4, 4, 0, 0, 1, 0x7d}, []byte{
		0x01, 0x02, 0x03, 0x00, 0x04, 0x11, 0x05, 0x12, 0x21, 0x31, 0x41, 0x06, 0x13, 0x51, 0x61, 0x07,
		0x22, 0x71, 0x14, 0x32, 0x81, 0x91, 0xa1, 0x08, 0x23, 0x42, 0xb1, 0xc1, 0x15, 0x52, 0xd1, 0xf0,
		0x24, 0x33, 0x62, 0x72, 0x82, 0x09, 0x0a, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x25, 0x26, 0x27, 0x28,
		0x29, 0x2a, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49,
		0x4a, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5a, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69,
		0x6a, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89,
		0x8a, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7,
		0xa8, 0xa9, 0xaa, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xc2, 0xc3, 0xc4, 0xc5,
		0xc6, 0xc7, 0xc8, 0xc9, 0xca, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda, 0xe1, 0xe2,
		0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9, 0xea, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8,
		0xf9, 0xfa}}
	acChrominance = huffSpec{[17]byte{0, 0, 2, 1, 2, 4, 4, 3, 4, 7, 5, 4, 4, 0, 1, 2, 0x77}, []byte{
		0x00, 0x01, 0x02, 0x03, 0x11, 0x04, 0x05, 0x21, 0x31, 0x06, 0x12, 0x41, 0x51, 0x07, 0x61, 0x71,
		0x13, 0x22, 0x32, 0x81, 0x08, 0x14, 0x42, 0x91, 0xa1, 0xb1, 0xc1, 0x09, 0x23, 0x33, 0x52, 0xf0,
		0x15, 0x62, 0x72, 0xd1, 0x0a, 0x16, 0x24, 0x34, 0xe1, 0x25, 0xf1, 0x17, 0x18, 0x19, 0x1a, 0x26,
		0x27, 0x28, 0x29, 0x2a, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48,
		0x49, 0x4a, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5a, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68,
		0x69, 0x6a, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87,
		0x88, 0x89, 0x8a, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0xa2, 0xa3, 0xa4, 0xa5,
		0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xc2, 0xc3,
		0xc4, 0xc5, 0xc6, 0xc7, 0xc8, 0xc9, 0xca, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda,
		0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9, 0xea, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8,
		0xf9, 0xfa}}
)

// huffTable is jpeg_make_c_derived_tbl: each symbol's code and its length.
type huffTable struct {
	code [256]uint32
	size [256]int
}

func derive(s huffSpec) *huffTable {
	t := &huffTable{}
	code, k := uint32(0), 0
	for l := 1; l <= 16; l++ {
		for i := 0; i < int(s.bits[l]); i++ {
			v := s.vals[k]
			t.code[v], t.size[v] = code, l
			code++
			k++
		}
		code <<= 1
	}
	return t
}

// quantTable is jpeg_add_quant_table at a quality's scale, baseline (1..255).
func quantTable(basic [64]int, scale int) [64]int {
	var q [64]int
	for i, b := range basic {
		t := (b*scale + 50) / 100
		if t <= 0 {
			t = 1
		}
		if t > 32767 {
			t = 32767
		}
		if t > 255 {
			t = 255
		}
		q[i] = t
	}
	return q
}

func qualityScaling(q int) int {
	if q <= 0 {
		q = 1
	}
	if q > 100 {
		q = 100
	}
	if q < 50 {
		return 5000 / q
	}
	return 200 - q*2
}

// divisors is compute_reciprocal for each coefficient of a table (times 8, for the islow DCT),
// with 16-bit DCTELEMs.
type divisors struct{ recip, corr, shift [64]int }

func flss(v int) int {
	n := 0
	for ; v != 0; v >>= 1 {
		n++
	}
	return n
}

func makeDivisors(q [64]int) *divisors {
	d := &divisors{}
	for i, qv := range q {
		divisor := qv << 3
		if divisor <= 1 {
			d.recip[i], d.corr[i], d.shift[i] = 1, 0, -16
			continue
		}
		b := flss(divisor) - 1
		r := 16 + b
		fq := uint32(1) << r / uint32(divisor)
		fr := uint32(1) << r % uint32(divisor)
		c := divisor / 2
		if fr == 0 {
			fq >>= 1
			r--
		} else if fr <= uint32(divisor/2) {
			c++
		} else {
			fq++
		}
		d.recip[i] = int(uint16(fq))
		d.corr[i] = int(uint16(c))
		d.shift[i] = r - 16
	}
	return d
}

// quantize is jcdctmgr.c's quantize with 16-bit DCTELEMs.
func quantize(out *[64]int, d *divisors, ws *[64]int) {
	for i := 0; i < 64; i++ {
		temp := int16(ws[i])
		recip, corr := uint32(uint16(d.recip[i])), uint32(uint16(d.corr[i]))
		shift := d.shift[i] + 16
		if temp < 0 {
			product := (uint32(-int32(temp)) + corr) * recip >> shift
			out[i] = -int(int16(product))
		} else {
			product := (uint32(temp) + corr) * recip >> shift
			out[i] = int(int16(product))
		}
	}
}

const (
	constBits = 13
	pass1Bits = 2
)

func descale(x int64, n uint) int64 { return (x + 1<<(n-1)) >> n }

// fdctIslow is jfdctint.c's jpeg_fdct_islow; the workspace holds 16-bit values between the passes.
func fdctIslow(data *[64]int) {
	for row := 0; row < 8; row++ {
		fdctPass(data, row*8, 1, constBits-pass1Bits, true)
	}
	for col := 0; col < 8; col++ {
		fdctPass(data, col, 8, constBits+pass1Bits, false)
	}
}

// fdctPass is one row (step 1) or column (step 8) of the DCT.
func fdctPass(data *[64]int, base, step int, sh uint, first bool) {
	const (
		f0298 = 2446
		f0390 = 3196
		f0541 = 4433
		f0765 = 6270
		f0899 = 7373
		f1175 = 9633
		f1501 = 12299
		f1847 = 15137
		f1961 = 16069
		f2053 = 16819
		f2562 = 20995
		f3072 = 25172
	)
	var d [8]int64
	for i := range d {
		d[i] = int64(data[base+i*step])
	}
	set := func(i int, v int64) { data[base+i*step] = int(int16(v)) }
	tmp0, tmp7 := d[0]+d[7], d[0]-d[7]
	tmp1, tmp6 := d[1]+d[6], d[1]-d[6]
	tmp2, tmp5 := d[2]+d[5], d[2]-d[5]
	tmp3, tmp4 := d[3]+d[4], d[3]-d[4]
	tmp10, tmp13 := tmp0+tmp3, tmp0-tmp3
	tmp11, tmp12 := tmp1+tmp2, tmp1-tmp2
	if first {
		set(0, (tmp10+tmp11)<<pass1Bits)
		set(4, (tmp10-tmp11)<<pass1Bits)
	} else {
		set(0, descale(tmp10+tmp11, pass1Bits))
		set(4, descale(tmp10-tmp11, pass1Bits))
	}
	z1 := (tmp12 + tmp13) * f0541
	set(2, descale(z1+tmp13*f0765, sh))
	set(6, descale(z1+tmp12*-f1847, sh))
	z1 = tmp4 + tmp7
	z2 := tmp5 + tmp6
	z3 := tmp4 + tmp6
	z4 := tmp5 + tmp7
	z5 := (z3 + z4) * f1175
	tmp4 *= f0298
	tmp5 *= f2053
	tmp6 *= f3072
	tmp7 *= f1501
	z1 *= -f0899
	z2 *= -f2562
	z3 *= -f1961
	z4 *= -f0390
	z3 += z5
	z4 += z5
	set(7, descale(tmp4+z1+z3, sh))
	set(5, descale(tmp5+z2+z4, sh))
	set(3, descale(tmp6+z2+z3, sh))
	set(1, descale(tmp7+z1+z4, sh))
}

// bitWriter is jchuff.c's output: bits from the most significant, 0xFF followed by 0x00.
type bitWriter struct {
	out   *bytes.Buffer
	acc   uint64
	nbits uint
}

func (w *bitWriter) emit(code uint32, size int) {
	w.acc = w.acc<<uint(size) | uint64(code)&(1<<uint(size)-1)
	w.nbits += uint(size)
	for w.nbits >= 8 {
		b := byte(w.acc >> (w.nbits - 8))
		w.out.WriteByte(b)
		if b == 0xFF {
			w.out.WriteByte(0)
		}
		w.nbits -= 8
	}
}

// flush fills the partial byte with ones.
func (w *bitWriter) flush() {
	if w.nbits > 0 {
		w.emit(0x7F, 7)
	}
	w.acc, w.nbits = 0, 0
}

func nbitsOf(v int) int {
	n := 0
	for ; v != 0; v >>= 1 {
		n++
	}
	return n
}

func encodeBlock(w *bitWriter, block *[64]int, lastDC *int, dc, ac *huffTable) {
	temp := block[0] - *lastDC
	*lastDC = block[0]
	temp2 := temp
	if temp < 0 {
		temp = -temp
		temp2--
	}
	nbits := nbitsOf(temp)
	w.emit(dc.code[nbits], dc.size[nbits])
	if nbits > 0 {
		w.emit(uint32(temp2), nbits)
	}
	r := 0
	for k := 1; k < 64; k++ {
		temp = block[naturalOrder[k]]
		if temp == 0 {
			r++
			continue
		}
		for r > 15 {
			w.emit(ac.code[0xF0], ac.size[0xF0])
			r -= 16
		}
		temp2 = temp
		if temp < 0 {
			temp = -temp
			temp2--
		}
		nbits = nbitsOf(temp)
		i := r<<4 + nbits
		w.emit(ac.code[i], ac.size[i])
		w.emit(uint32(temp2), nbits)
		r = 0
	}
	if r > 0 {
		w.emit(ac.code[0], ac.size[0])
	}
}

// The conversion's factors, FIX(x) of jccolor.c: x in 16-bit fixed point, rounded.
var (
	fix0299   = fix(0.29900)
	fix0587   = fix(0.58700)
	fix0114   = fix(0.11400)
	fix016874 = fix(0.16874)
	fix033126 = fix(0.33126)
	fix05     = fix(0.50000)
	fix041869 = fix(0.41869)
	fix008131 = fix(0.08131)
)

func fix(x float64) int64 { return int64(x*65536 + 0.5) }

func ceilDiv(a, b int) int { return (a + b - 1) / b }

// encodeJPEG is the file Pillow writes for the image at this quality.
func encodeJPEG(im *canvas, quality int) []byte {
	W, H := im.w, im.h
	// the planes, padded to whole MCUs by repeating the last column and row (expand_right_edge,
	// expand_bottom_edge), converted as rgb_ycc_convert does
	PW, PH := ceilDiv(W, 16)*16, ceilDiv(H, 16)*16
	const half, cbcrOffset = 1 << 15, 128 << 16
	// the planes of the image's rows (and one more, the last again, where their number is odd),
	// each row widened to whole MCUs by repeating its last pixel (expand_right_edge)
	RH := ceilDiv(H, 2) * 2
	planes := [3][]uint8{make([]uint8, PW*RH), make([]uint8, PW*RH), make([]uint8, PW*RH)}
	for y := 0; y < RH; y++ {
		src := im.pix[min(y, H-1)*W*3:]
		for x := 0; x < PW; x++ {
			p := src[min(x, W-1)*3:]
			r, g, b := int64(p[0]), int64(p[1]), int64(p[2])
			i := y*PW + x
			planes[0][i] = uint8((fix0299*r + fix0587*g + fix0114*b + half) >> 16)
			planes[1][i] = uint8((-fix016874*r - fix033126*g + fix05*b + cbcrOffset + half - 1) >> 16)
			planes[2][i] = uint8((fix05*r + cbcrOffset + half - 1 - fix041869*g - fix008131*b) >> 16)
		}
	}
	// below the image, to a whole MCU row, the last row of each component again (expand_bottom_edge
	// after downsampling): luminance as it is (read so in block), chroma at half size
	// (h2v2_downsample, its bias 1, 2, 1, 2, ... along each row)
	luma := planes[0]
	CW, CH := PW/2, PH/2
	var chroma [2][]uint8
	for c := 0; c < 2; c++ {
		src := planes[c+1]
		out := make([]uint8, CW*CH)
		for y := 0; y < CH; y++ {
			if y >= RH/2 {
				copy(out[y*CW:(y+1)*CW], out[(RH/2-1)*CW:])
				continue
			}
			bias := 1
			for x := 0; x < CW; x++ {
				i := 2*y*PW + 2*x
				out[y*CW+x] = uint8((int(src[i]) + int(src[i+1]) + int(src[i+PW]) + int(src[i+PW+1]) + bias) >> 2)
				bias ^= 3
			}
		}
		chroma[c] = out
	}

	scale := qualityScaling(quality)
	qtabs := [2][64]int{quantTable(stdLuminance, scale), quantTable(stdChrominance, scale)}
	divs := [2]*divisors{makeDivisors(qtabs[0]), makeDivisors(qtabs[1])}
	dcs := [2]*huffTable{derive(dcLuminance), derive(dcChrominance)}
	acs := [2]*huffTable{derive(acLuminance), derive(acChrominance)}

	var out bytes.Buffer
	marker := func(m byte, payload ...byte) {
		out.Write([]byte{0xFF, m, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)})
		out.Write(payload)
	}
	out.Write([]byte{0xFF, 0xD8})
	marker(0xE0, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0)
	for t := 0; t < 2; t++ {
		p := []byte{byte(t)}
		for i := 0; i < 64; i++ {
			p = append(p, byte(qtabs[t][naturalOrder[i]]))
		}
		marker(0xDB, p...)
	}
	marker(0xC0, 8, byte(H>>8), byte(H), byte(W>>8), byte(W), 3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1)
	for t, s := range []huffSpec{dcLuminance, acLuminance, dcChrominance, acChrominance} {
		p := []byte{byte(t%2<<4 | t/2)}
		p = append(p, s.bits[1:]...)
		p = append(p, s.vals...)
		marker(0xC4, p...)
	}
	marker(0xDA, 3, 1, 0x00, 2, 0x11, 3, 0x11, 0, 63, 0)

	w := &bitWriter{out: &out}
	var lastDC [3]int
	yWB, yHB := ceilDiv(W, 8), ceilDiv(H, 8)   // the luminance's blocks
	cWB, cHB := ceilDiv(W, 16), ceilDiv(H, 16) // the chroma's, one per MCU
	var ws [64]int
	block := func(out *[64]int, plane []uint8, stride, rows, bx, by int, q *divisors) {
		for r := 0; r < 8; r++ {
			row := plane[min(by*8+r, rows-1)*stride+bx*8:]
			for c := 0; c < 8; c++ {
				ws[r*8+c] = int(row[c]) - 128
			}
		}
		fdctIslow(&ws)
		quantize(out, q, &ws)
	}
	for my := 0; my < ceilDiv(H, 16); my++ {
		for mx := 0; mx < ceilDiv(W, 16); mx++ {
			// luminance: four blocks, those outside the image dummies (zero, the previous DC)
			var ys [4][64]int
			for k := 0; k < 4; k++ {
				bx, by := 2*mx+k%2, 2*my+k/2
				switch {
				case by >= yHB:
					ys[k] = [64]int{}
					ys[k][0] = ys[1][0] // the last block of the row above (blkn - 1)
				case bx >= yWB:
					ys[k] = [64]int{}
					ys[k][0] = ys[k-1][0]
				default:
					block(&ys[k], luma, PW, H, bx, by, divs[0])
				}
			}
			for k := 0; k < 4; k++ {
				encodeBlock(w, &ys[k], &lastDC[0], dcs[0], acs[0])
			}
			for c := 0; c < 2; c++ {
				var b [64]int
				if mx < cWB && my < cHB {
					block(&b, chroma[c], CW, CH, mx, my, divs[1])
				}
				encodeBlock(w, &b, &lastDC[c+1], dcs[1], acs[1])
			}
		}
	}
	w.flush()
	out.Write([]byte{0xFF, 0xD9})
	return out.Bytes()
}
