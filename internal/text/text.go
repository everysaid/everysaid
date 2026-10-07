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

// foldedWithMap is Fold(s) as runes, and for each of them the index (in runes) in s it came from.
func foldedWithMap(s []rune) ([]rune, []int) {
	var out []rune
	var where []int
	for i, c := range s {
		for _, f := range Fold(string(c)) {
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
		if len(n) == 0 {
			continue
		}
		for a := 0; a+len(n) <= len(hay); {
			if !equalRunes(hay[a:a+len(n)], n) {
				a++
				continue
			}
			b := a + len(n)
			if m.Whole && ((a > 0 && isAlnum(hay[a-1])) || (b < len(hay) && isAlnum(hay[b]))) {
				a = b
				continue
			}
			if where == nil {
				out = append(out, Span{a, b})
			} else {
				out = append(out, Span{where[a], where[b-1] + 1})
			}
			a = b
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].End < out[j].End
	})
	return out
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
	for _, w := range m.Words {
		if len(NewMatcher(w, m.Case, m.Whole).Spans(text)) == 0 {
			return false
		}
	}
	return true
}
