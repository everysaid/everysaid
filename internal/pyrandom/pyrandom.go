// Package pyrandom is CPython's random.Random, number for number: the Mersenne Twister seeded as
// CPython seeds it from an int, and the methods built on it as Lib/random.py writes them (3.14), so
// that what Python made from a seed (the demo archive) Go makes the same.
//
// Ported: seed from an int (init_by_array of its 32-bit words), Random (genrand_res53), Getrandbits,
// Randbelow (rejection sampling with getrandbits), Randrange, Randint, Choice, Choices (with and
// without weights), Shuffle, Sample (both of CPython's ways, a pool or a set of chosen indexes,
// chosen by size as CPython does) and Uniform. Not ported: seeding from a str, bytes or None, and
// the other distributions (gauss, triangular, ...), which nothing here uses.
package pyrandom

import (
	"math"
	"math/big"
	"sort"
)

const (
	n         = 624
	m         = 397
	matrixA   = 0x9908b0df
	upperMask = 0x80000000
	lowerMask = 0x7fffffff
)

// Rand is one random.Random.
type Rand struct {
	mt  [n]uint32
	mti int
}

// New is random.Random(seed).
func New(seed int64) *Rand {
	r := &Rand{}
	r.Seed(seed)
	return r
}

// Seed is random.seed(int): the absolute value's 32-bit words, least significant first (one word
// for 0).
func (r *Rand) Seed(seed int64) {
	u := uint64(seed)
	if seed < 0 {
		u = uint64(-seed) // math.MinInt64 too: its absolute value is 1<<63
	}
	key := []uint32{uint32(u)}
	if u>>32 != 0 {
		key = append(key, uint32(u>>32))
	}
	r.initByArray(key)
}

func (r *Rand) initGenrand(s uint32) {
	r.mt[0] = s
	for i := 1; i < n; i++ {
		r.mt[i] = 1812433253*(r.mt[i-1]^(r.mt[i-1]>>30)) + uint32(i)
	}
	r.mti = n
}

func (r *Rand) initByArray(key []uint32) {
	r.initGenrand(19650218)
	i, j := 1, 0
	k := n
	if len(key) > k {
		k = len(key)
	}
	for ; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1664525)) + key[j] + uint32(j)
		i++
		j++
		if i >= n {
			r.mt[0] = r.mt[n-1]
			i = 1
		}
		if j >= len(key) {
			j = 0
		}
	}
	for k = n - 1; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1566083941)) - uint32(i)
		i++
		if i >= n {
			r.mt[0] = r.mt[n-1]
			i = 1
		}
	}
	r.mt[0] = 0x80000000
}

func (r *Rand) genrand() uint32 {
	mag01 := [2]uint32{0, matrixA}
	if r.mti >= n {
		var kk int
		for kk = 0; kk < n-m; kk++ {
			y := (r.mt[kk] & upperMask) | (r.mt[kk+1] & lowerMask)
			r.mt[kk] = r.mt[kk+m] ^ (y >> 1) ^ mag01[y&1]
		}
		for ; kk < n-1; kk++ {
			y := (r.mt[kk] & upperMask) | (r.mt[kk+1] & lowerMask)
			r.mt[kk] = r.mt[kk+(m-n)] ^ (y >> 1) ^ mag01[y&1]
		}
		y := (r.mt[n-1] & upperMask) | (r.mt[0] & lowerMask)
		r.mt[n-1] = r.mt[m-1] ^ (y >> 1) ^ mag01[y&1]
		r.mti = 0
	}
	y := r.mt[r.mti]
	r.mti++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	y ^= y >> 18
	return y
}

// Random is random(): a float in [0, 1) with 53 random bits.
func (r *Rand) Random() float64 {
	a, b := r.genrand()>>5, r.genrand()>>6
	return (float64(a)*67108864.0 + float64(b)) * (1.0 / 9007199254740992.0)
}

// Getrandbits is getrandbits(k) for k <= 64.
func (r *Rand) Getrandbits(k int) uint64 {
	if k < 0 || k > 64 {
		panic("pyrandom: Getrandbits takes 0 to 64 bits (use GetrandbitsBig)")
	}
	if k == 0 {
		return 0
	}
	if k <= 32 {
		return uint64(r.genrand() >> (32 - k))
	}
	return r.GetrandbitsBig(k).Uint64()
}

// GetrandbitsBig is getrandbits(k) for any k: 32-bit words, least significant first, the last
// one cut to what is left.
func (r *Rand) GetrandbitsBig(k int) *big.Int {
	out := new(big.Int)
	for shift := 0; k > 0; shift += 32 {
		w := r.genrand()
		if k < 32 {
			w >>= 32 - k
		}
		out.Or(out, new(big.Int).Lsh(big.NewInt(int64(w)), uint(shift)))
		k -= 32
	}
	return out
}

func bitLength(x uint64) int {
	k := 0
	for ; x != 0; x >>= 1 {
		k++
	}
	return k
}

// Randbelow is _randbelow(n): an int in [0, n), for n > 0.
func (r *Rand) Randbelow(n int) int {
	if n <= 0 {
		panic("pyrandom: Randbelow needs n > 0")
	}
	k := bitLength(uint64(n))
	v := r.Getrandbits(k)
	for v >= uint64(n) {
		v = r.Getrandbits(k)
	}
	return int(v)
}

// Randrange is randrange(start, stop) (step 1); RandrangeN is randrange(stop).
func (r *Rand) Randrange(start, stop int) int {
	if stop-start <= 0 {
		panic("pyrandom: empty range in Randrange")
	}
	return start + r.Randbelow(stop-start)
}

func (r *Rand) RandrangeN(stop int) int {
	if stop <= 0 {
		panic("pyrandom: empty range in RandrangeN")
	}
	return r.Randbelow(stop)
}

// RandrangeStep is randrange(start, stop, step).
func (r *Rand) RandrangeStep(start, stop, step int) int {
	if step == 1 {
		return r.Randrange(start, stop)
	}
	width := stop - start
	var k int
	switch {
	case step > 0:
		k = floorDiv(width+step-1, step)
	case step < 0:
		k = floorDiv(width+step+1, step)
	default:
		panic("pyrandom: zero step for RandrangeStep")
	}
	if k <= 0 {
		panic("pyrandom: empty range in RandrangeStep")
	}
	return start + step*r.Randbelow(k)
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// Randint is randint(a, b): a <= x <= b.
func (r *Rand) Randint(a, b int) int { return r.Randrange(a, b+1) }

// Choice is the index choice(seq) takes of a sequence of n items.
func (r *Rand) Choice(n int) int {
	if n == 0 {
		panic("pyrandom: Choice from an empty sequence")
	}
	return r.Randbelow(n)
}

// Pick is choice(seq).
func Pick[T any](r *Rand, seq []T) T { return seq[r.Choice(len(seq))] }

// Shuffle is shuffle(x).
func Shuffle[T any](r *Rand, x []T) {
	for i := len(x) - 1; i >= 1; i-- {
		j := r.Randbelow(i + 1)
		x[i], x[j] = x[j], x[i]
	}
}

// SampleIndexes is the indexes sample(population, k) takes of a population of n, in order.
func (r *Rand) SampleIndexes(n, k int) []int {
	if k < 0 || k > n {
		panic("pyrandom: Sample larger than population or is negative")
	}
	result := make([]int, k)
	setsize := 21.0 // the size of a small set minus that of an empty list
	if k > 5 {
		setsize += math.Pow(4, math.Ceil(math.Log(float64(k*3))/math.Log(4)))
	}
	if float64(n) <= setsize { // an n-length list is smaller than a k-length set
		pool := make([]int, n)
		for i := range pool {
			pool[i] = i
		}
		for i := 0; i < k; i++ {
			j := r.Randbelow(n - i)
			result[i] = pool[j]
			pool[j] = pool[n-i-1]
		}
		return result
	}
	selected := map[int]bool{}
	for i := 0; i < k; i++ {
		j := r.Randbelow(n)
		for selected[j] {
			j = r.Randbelow(n)
		}
		selected[j] = true
		result[i] = j
	}
	return result
}

// Sample is sample(population, k).
func Sample[T any](r *Rand, population []T, k int) []T {
	out := make([]T, k)
	for i, j := range r.SampleIndexes(len(population), k) {
		out[i] = population[j]
	}
	return out
}

// ChoicesIndexes is the indexes choices(population, weights, k=k) takes of a population of n;
// weights nil for equal ones.
func (r *Rand) ChoicesIndexes(n int, weights []float64, k int) []int {
	out := make([]int, k)
	if weights == nil {
		fn := float64(n)
		for i := range out {
			out[i] = int(math.Floor(r.Random() * fn))
		}
		return out
	}
	if len(weights) != n {
		panic("pyrandom: the number of weights does not match the population")
	}
	cum := make([]float64, n)
	total := 0.0
	for i, w := range weights {
		total += w
		cum[i] = total
	}
	if total <= 0 || math.IsInf(total, 0) || math.IsNaN(total) {
		panic("pyrandom: total of weights must be greater than zero and finite")
	}
	hi := n - 1
	for i := range out {
		x := r.Random() * total
		// bisect_right(cum, x, 0, hi)
		out[i] = sort.Search(hi, func(j int) bool { return x < cum[j] })
	}
	return out
}

// Choices is choices(population, weights, k=k).
func Choices[T any](r *Rand, population []T, weights []float64, k int) []T {
	out := make([]T, k)
	for i, j := range r.ChoicesIndexes(len(population), weights, k) {
		out[i] = population[j]
	}
	return out
}

// Uniform is uniform(a, b).
func (r *Rand) Uniform(a, b float64) float64 { return a + (b-a)*r.Random() }
