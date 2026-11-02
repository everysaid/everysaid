package plugins

import (
	"context"
	"slices"
	"sort"
	"unicode"
	"unicode/utf16"
)

// Style: a part of a text shown so, where it is in the text (in characters, as Mention). URL: a
// link's address.
type Style struct {
	Start, Length int
	Kind          string
	URL           string
}

// The kinds of Style. Code is monospaced: a block where it spans lines. Nothing else is shown
// inside code (as on the services that have it). Link: words that open an address.
const (
	Bold      = "bold"
	Italic    = "italic"
	Underline = "underline"
	Strike    = "strike"
	Code      = "code"
	Spoiler   = "spoiler"
	Link      = "link"
)

// StyleKinds are the kinds, in the order their marks open (the first outermost).
var StyleKinds = []string{Bold, Italic, Underline, Strike, Code, Spoiler, Link}

// StyledEditor is an Editor that also takes the new text's styles (only the kinds its Info.Styles
// lists); one that is not gets them as marks (Info.StyleMarks), else the text alone.
type StyledEditor interface {
	EditStyled(ctx context.Context, c *Context, conv Conversation, msg Ref, text string, styles []Style) error
}

// StyledSender is a Sender that also takes the text's styles, as its service carries them (only the
// kinds its Info.Styles lists). One that is not gets them as marks in the text (Info.StyleMarks),
// else the text alone.
type StyledSender interface {
	SendStyled(ctx context.Context, c *Context, conv Conversation, text string, reply *Reply, mentions []Mention,
		styles []Style, file *File) (any, error)
}

// FitStyles is styles as they can go into text: of the kinds given only, each within the text, none
// empty, those of a kind over or beside each other as one (not links: each its own address), the
// other kinds out of code, and the spaces at their ends left out (code keeps them); in order of place.
func FitStyles(text string, styles []Style, kinds []string) []Style {
	runes := []rune(text)
	n := len(runes)
	on := map[string][]bool{}
	var links []Style
	for _, k := range kinds {
		if k != Link {
			on[k] = make([]bool, n)
		}
	}
	for _, s := range styles {
		if s.Kind == Link && slices.Contains(kinds, Link) && s.URL != "" && s.Start >= 0 && s.Length > 0 && s.Start < n {
			links = append(links, Style{Start: s.Start, Length: min(s.Length, n-s.Start), Kind: Link, URL: s.URL})
			continue
		}
		marks, ok := on[s.Kind]
		if !ok || s.Start < 0 || s.Length <= 0 || s.Start >= n {
			continue
		}
		for i := s.Start; i < n && i-s.Start < s.Length; i++ {
			marks[i] = true
		}
	}
	if code := on[Code]; code != nil {
		for k, marks := range on {
			if k != Code {
				for i := range marks {
					marks[i] = marks[i] && !code[i]
				}
			}
		}
	}
	var out []Style
	for _, k := range StyleKinds {
		marks := on[k]
		for i := 0; i < len(marks); {
			if !marks[i] {
				i++
				continue
			}
			j := i
			for j < n && marks[j] {
				j++
			}
			a, b := i, j
			if k != Code {
				for a < b && unicode.IsSpace(runes[a]) {
					a++
				}
				for b > a && unicode.IsSpace(runes[b-1]) {
					b--
				}
			}
			if a < b {
				out = append(out, Style{Start: a, Length: b - a, Kind: k})
			}
			i = j
		}
	}
	out = append(out, links...)
	sortStyles(out)
	return out
}

// WholeMentions is styles with no end inside a mention: one that starts or ends within it takes
// it all (a mention is written again by the plugin, and a mark or a boundary inside would cut it).
func WholeMentions(styles []Style, mentions []Mention) []Style {
	out := make([]Style, 0, len(styles))
	for _, s := range styles {
		start, end := s.Start, s.Start+s.Length
		for _, m := range mentions {
			if start > m.Start && start < m.Start+m.Length {
				start = m.Start
			}
			if end > m.Start && end < m.Start+m.Length {
				end = m.Start + m.Length
			}
		}
		out = append(out, Style{Start: start, Length: end - start, Kind: s.Kind})
	}
	return out
}

// Replaced is a part of a text (in characters) written anew as Runes characters.
type Replaced struct {
	Start, Length, Runes int
}

// MoveStyles is styles where they are once the parts given (none over another) are written anew:
// one that ends or starts within a part takes the whole of it.
func MoveStyles(styles []Style, parts []Replaced) []Style {
	parts = append([]Replaced(nil), parts...)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Start < parts[j].Start })
	at := func(p int, end bool) int {
		shift := 0
		for _, r := range parts {
			switch {
			case p >= r.Start+r.Length && (r.Length > 0 || p > r.Start || !end): // what is put in at an end is not in the style
				shift += r.Runes - r.Length
			case p > r.Start && p < r.Start+r.Length:
				if end {
					return r.Start + r.Runes + shift
				}
				return r.Start + shift
			}
		}
		return p + shift
	}
	out := make([]Style, 0, len(styles))
	for _, s := range styles {
		start, end := at(s.Start, false), at(s.Start+s.Length, true)
		if end > start {
			out = append(out, Style{Start: start, Length: end - start, Kind: s.Kind})
		}
	}
	return out
}

// NestStyles is styles cut where two cross, so that of any two one is wholly in the other or they
// are apart (as Telegram takes them); in order of place, the longer first.
func NestStyles(styles []Style) []Style {
	todo := append([]Style(nil), styles...)
	sortStyles(todo)
	var out, open []Style
	for len(todo) > 0 {
		s := todo[0]
		todo = todo[1:]
		for len(open) > 0 && open[len(open)-1].Start+open[len(open)-1].Length <= s.Start {
			open = open[:len(open)-1]
		}
		if len(open) > 0 {
			if top := open[len(open)-1]; s.Start+s.Length > top.Start+top.Length {
				end := top.Start + top.Length
				rest := Style{Start: end, Length: s.Start + s.Length - end, Kind: s.Kind}
				s.Length = end - s.Start
				i := sort.Search(len(todo), func(i int) bool { return !before(todo[i], rest) })
				todo = append(todo[:i], append([]Style{rest}, todo[i:]...)...)
			}
		}
		out = append(out, s)
		open = append(open, s)
	}
	return out
}

// UTF16 is where a part of runes (in characters) is in UTF-16 code units, as Telegram and Signal
// count.
func UTF16(runes []rune, start, length int) (int, int) {
	off := len(utf16.Encode(runes[:start]))
	return off, len(utf16.Encode(runes[start : start+length]))
}

// The marks of WhatsApp and Viber: a style written into the text, "*bold*". A mark counts only
// at the edge of a word; one within a word, or one closing where another opens, gets a word joiner
// (U+2060) beside it, which both take as an edge without showing anything (tried on both).
var marks = map[string]string{Bold: "*", Italic: "_", Strike: "~", Code: "```"}

const wordJoiner = '⁠'

// MarkStyles is the text with its styles written as marks (only those that have one; a style
// spanning lines closed at the end of each and opened again, code apart), and the mentions where
// they are then.
func MarkStyles(text string, mentions []Mention, styles []Style) (string, []Mention) {
	runes := []rune(text)
	var lines []Style
	for _, s := range FitStyles(text, WholeMentions(styles, mentions), []string{Bold, Italic, Strike, Code}) {
		if s.Kind == Code {
			lines = append(lines, s)
			continue
		}
		a := s.Start
		for i := s.Start; i <= s.Start+s.Length; i++ {
			if i == s.Start+s.Length || runes[i] == '\n' {
				lines = append(lines, FitStyles(string(runes), []Style{{Start: a, Length: i - a, Kind: s.Kind}}, []string{s.Kind})...)
				a = i + 1
			}
		}
	}
	nested := NestStyles(lines)
	opens := map[int][]Style{}
	closes := map[int][]Style{}
	for _, s := range nested {
		opens[s.Start] = append(opens[s.Start], s) // outer first, as nested
		closes[s.Start+s.Length] = append([]Style{s}, closes[s.Start+s.Length]...)
	}
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }
	var out []rune
	at := make([]int, len(runes)+1) // where each character went
	for i := 0; i <= len(runes); i++ {
		for _, s := range closes[i] {
			out = append(out, []rune(marks[s.Kind])...)
		}
		if len(closes[i]) > 0 && len(opens[i]) == 0 && i < len(runes) && word(runes[i]) {
			out = append(out, wordJoiner)
		}
		if len(opens[i]) > 0 && len(out) > 0 && !unicode.IsSpace(out[len(out)-1]) && out[len(out)-1] != wordJoiner {
			out = append(out, wordJoiner)
		}
		for _, s := range opens[i] {
			out = append(out, []rune(marks[s.Kind])...)
		}
		at[i] = len(out)
		if i < len(runes) {
			out = append(out, runes[i])
		}
	}
	moved := make([]Mention, 0, len(mentions))
	for _, m := range mentions {
		if m.Start < 0 || m.Length < 1 || m.Start+m.Length > len(runes) {
			continue
		}
		start, end := at[m.Start], at[m.Start+m.Length-1]+1
		moved = append(moved, Mention{Start: start, Length: end - start, AddressID: m.AddressID})
	}
	return string(out), moved
}

func sortStyles(s []Style) {
	sort.SliceStable(s, func(i, j int) bool { return before(s[i], s[j]) })
}

// before: by place, the longer first, then by kind as the marks open.
func before(a, b Style) bool {
	if a.Start != b.Start {
		return a.Start < b.Start
	}
	if a.Length != b.Length {
		return a.Length > b.Length
	}
	return kindOrder(a.Kind) < kindOrder(b.Kind)
}

func kindOrder(k string) int {
	for i, x := range StyleKinds {
		if x == k {
			return i
		}
	}
	return len(StyleKinds)
}
