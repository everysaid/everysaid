// Package text holds text as the search index holds it: one form for every way of writing the same
// word.
//
// Fold lower-cases (Unicode case folding, which also makes the final sigma ς a σ), takes off the
// accents and other combining marks of every script (Greek tonos and dialytika, Latin accents), and
// brings compatibility forms to one (ligatures, full-width letters). The index stores folded text
// and a query is folded the same way, so `καλημερα` finds `Καλημέρα` and `φιλοσ` finds `φίλος`.
package text

import (
	"sort"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var folder = cases.Fold()

func isMark(r rune) bool {
	return (r >= 0x0300 && r <= 0x036F) || (r >= 0x1AB0 && r <= 0x1AFF) || (r >= 0x1DC0 && r <= 0x1DFF) ||
		(r >= 0x20D0 && r <= 0x20FF) || (r >= 0xFE20 && r <= 0xFE2F)
}

// Fold is the text as the index holds it.
func Fold(s string) string {
	if s == "" {
		return s
	}
	s = norm.NFKD.String(folder.String(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isMark(r) {
			b.WriteRune(r)
		}
	}
	return norm.NFC.String(b.String())
}

// runeFolds holds Fold of one rune as runes, once worked out (ASCII aside: its fold is its lower case).
var runeFolds sync.Map

func foldRune(c rune) []rune {
	if c < 0x80 {
		return foldOne(c)
	}
	if f, ok := runeFolds.Load(c); ok {
		return f.([]rune)
	}
	f := foldOne(c)
	runeFolds.Store(c, f)
	return f
}

// foldOne is Fold of one rune, not kept.
func foldOne(c rune) []rune {
	if c < 0x80 {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		return asciiRunes[c : c+1]
	}
	return []rune(Fold(string(c)))
}

var asciiRunes = func() []rune {
	r := make([]rune, 0x80)
	for i := range r {
		r[i] = rune(i)
	}
	return r
}()

// foldedWithMap is the text with each rune folded on its own (Fold), and for each rune of it the
// index (in runes) in s it came from.
func foldedWithMap(s []rune) ([]rune, []int) {
	out := make([]rune, 0, len(s))
	where := make([]int, 0, len(s))
	for i, c := range s {
		for _, f := range foldRune(c) {
			out = append(out, f)
			where = append(where, i)
		}
	}
	return out, where
}

// Span is a match in a text, in runes: [Start, End).
type Span struct{ Start, End int }

// Matcher says where a search's words are in a text. Case: exact (case and accents as typed);
// else folded. Whole: whole words only, a word ending in * the start of a word; else anywhere,
// inside words too (a * at the end means nothing then). A word that is nothing but * is left out.
type Matcher struct {
	Case, Whole bool
	Words       []string
	needles     [][]rune
	prefix      []bool
}

func NewMatcher(q string, caseSensitive, whole bool) *Matcher {
	m := &Matcher{Case: caseSensitive, Whole: whole, Words: strings.Fields(q)}
	for _, w := range m.Words {
		n := strings.TrimRight(w, "*")
		if !caseSensitive {
			n = Fold(n)
		}
		if n == "" {
			continue
		}
		m.needles = append(m.needles, []rune(n))
		m.prefix = append(m.prefix, whole && strings.HasSuffix(w, "*"))
	}
	return m
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsNumber(r) }

// Spans are the [start, end) in runes of `text` of every match of every word, in order.
func (m *Matcher) Spans(text string) []Span {
	if text == "" || len(m.needles) == 0 {
		return nil
	}
	src := []rune(text)
	hay, where := src, []int(nil)
	if !m.Case {
		hay, where = foldedWithMap(src)
	}
	var out []Span
	for i, n := range m.needles {
		find(hay, n, m.Whole, m.prefix[i], func(a, b int) bool {
			if where == nil {
				out = append(out, Span{a, b})
			} else {
				out = append(out, Span{where[a], where[b-1] + 1})
			}
			return true
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].End < out[j].End
	})
	return out
}

// find gives each match [a, b) of the needle n in hay, left to right without overlaps (whole: not
// inside a word; prefix: only its start must be a word's), as long as found says to go on.
func find(hay, n []rune, whole, prefix bool, found func(a, b int) bool) {
	if len(n) == 0 {
		return
	}
	for a := 0; a+len(n) <= len(hay); {
		if !equalRunes(hay[a:a+len(n)], n) {
			a++
			continue
		}
		b := a + len(n)
		if whole && ((a > 0 && isAlnum(hay[a-1])) || (!prefix && b < len(hay) && isAlnum(hay[b]))) {
			a = b
			continue
		}
		if !found(a, b) {
			return
		}
		a = b
	}
}

func equalRunes(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Matches says whether every word is in the text.
func (m *Matcher) Matches(text string) bool {
	if len(m.needles) == 0 {
		return true
	}
	if text == "" {
		return false
	}
	hay := []rune(text)
	if !m.Case {
		hay, _ = foldedWithMap(hay)
	}
	for i, n := range m.needles {
		in := false
		find(hay, n, m.Whole, m.prefix[i], func(int, int) bool { in = true; return false })
		if !in {
			return false
		}
	}
	return true
}

var preimages struct {
	sync.Once
	of map[rune][]rune // a rune, and the others whose fold (on its own) has it
}

// Preimage is every rune whose fold (Fold of it alone, as the Matcher folds) has the rune c: a
// text whose folded form has c has one of them.
func Preimage(c rune) []rune {
	preimages.Do(func() {
		preimages.of = map[rune][]rune{}
		for r := rune(0); r <= unicode.MaxRune; r++ {
			if r >= 0xD800 && r <= 0xDFFF {
				continue
			}
			f := foldOne(r) // not kept: a million runes
			if len(f) == 1 && f[0] == r {
				continue
			}
			for _, x := range f {
				if p := preimages.of[x]; len(p) == 0 || p[len(p)-1] != r {
					preimages.of[x] = append(p, r)
				}
			}
		}
	})
	var out []rune
	if f := foldRune(c); len(f) == 1 && f[0] == c {
		out = append(out, c)
	}
	return append(out, preimages.of[c]...)
}
