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

// Query is a user's search turned into an FTS5 query: each word folded and quoted (so that FTS5's
// own operators in it are taken as text), a trailing * kept as a prefix search; words are ANDed.
func Query(q string) string {
	var words []string
	for _, w := range strings.Fields(q) {
		prefix := strings.HasSuffix(w, "*")
		w = strings.ReplaceAll(Fold(strings.TrimRight(w, "*")), `"`, `""`)
		if w != "" {
			s := `"` + w + `"`
			if prefix {
				s += "*"
			}
			words = append(words, s)
		}
	}
	return strings.Join(words, " ")
}

// runeFolds holds Fold of one rune as runes, once worked out (ASCII aside: its fold is its lower case).
var runeFolds sync.Map

func foldRune(c rune) []rune {
	if c < 0x80 {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		return asciiRunes[c : c+1]
	}
	if f, ok := runeFolds.Load(c); ok {
		return f.([]rune)
	}
	f := []rune(Fold(string(c)))
	runeFolds.Store(c, f)
	return f
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
// else folded. Whole: whole words only; else anywhere, inside words too.
type Matcher struct {
	Case, Whole bool
	Words       []string
	needles     [][]rune
}

func NewMatcher(q string, caseSensitive, whole bool) *Matcher {
	m := &Matcher{Case: caseSensitive, Whole: whole, Words: strings.Fields(q)}
	for _, w := range m.Words {
		if !caseSensitive {
			w = Fold(w)
		}
		m.needles = append(m.needles, []rune(w))
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
	for _, n := range m.needles {
		find(hay, n, m.Whole, func(a, b int) bool {
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
// inside a word), as long as found says to go on.
func find(hay, n []rune, whole bool, found func(a, b int) bool) {
	if len(n) == 0 {
		return
	}
	for a := 0; a+len(n) <= len(hay); {
		if !equalRunes(hay[a:a+len(n)], n) {
			a++
			continue
		}
		b := a + len(n)
		if whole && ((a > 0 && isAlnum(hay[a-1])) || (b < len(hay) && isAlnum(hay[b]))) {
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
	for _, n := range m.needles {
		in := false
		find(hay, n, m.Whole, func(int, int) bool { in = true; return false })
		if !in {
			return false
		}
	}
	return true
}
