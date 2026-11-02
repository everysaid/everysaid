package archive

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Style is a part of a message's text shown so, where it is in the text in UTF-16 units (as
// Telegram, Signal and the browser count). Kind: bold, italic, underline, strike, code, spoiler,
// link (URL: where it goes).
type Style struct {
	Start, Length int
	Kind, URL     string
}

// Marked are the services whose apps read a text's styles from marks in it ("*bold*").
var Marked = map[string]bool{"whatsapp": true, "viber": true}

// Styled is a message's text and styles as the archive keeps them: of a service with marks, the
// text as its apps show it, without them, and the styles they say; else as given.
func Styled(service, text string, styles []Style) (string, []Style) {
	if Marked[service] {
		return ReadMarks(text)
	}
	styles = append([]Style(nil), styles...)
	sortStyles(styles)
	return text, styles
}

// sortStyles puts styles in the order StylesOf gives them: by place, the longer first, by kind.
func sortStyles(s []Style) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Start != s[j].Start {
			return s[i].Start < s[j].Start
		}
		if s[i].Length != s[j].Length {
			return s[i].Length > s[j].Length
		}
		return s[i].Kind < s[j].Kind
	})
}

// SetStyles makes a message's styles those given.
func (a *Archive) SetStyles(messageID int64, styles []Style) {
	a.Exec("DELETE FROM style WHERE message_id = ?", messageID)
	for _, s := range styles {
		if s.Length > 0 {
			a.Exec("INSERT OR IGNORE INTO style (message_id, start, length, kind, url) VALUES (?, ?, ?, ?, ?)",
				messageID, s.Start, s.Length, s.Kind, nullStr(s.URL))
		}
	}
}

// StylesOf is a message's styles, in order of place.
func (a *Archive) StylesOf(messageID int64) []Style {
	var out []Style
	a.Each("SELECT start, length, kind, coalesce(url, '') FROM style WHERE message_id = ? ORDER BY start, length DESC, kind",
		[]any{messageID}, func(scan func(...any)) {
			var s Style
			scan(&s.Start, &s.Length, &s.Kind, &s.URL)
			out = append(out, s)
		})
	return out
}

const wordJoiner = '⁠'

var singleMarks = map[rune]string{'*': "bold", '_': "italic", '~': "strike"}

var linkRe = regexp.MustCompile(`(?i)(?:https?://|www\.)\S+`)

// ReadMarks is a text WhatsApp or Viber wrote with marks as their apps show it: without the marks
// that style ("*bold*", "_italic_", "~strike~", "```code```"; one opening at the edge of a word, not
// before a space, and closed on its line at the edge of a word, a letter or a digit between them;
// none within a link), nor the word joiners beside them (as this app writes a mark within a word);
// the styles they say, in UTF-16 units of the text left.
func ReadMarks(text string) (string, []Style) {
	runes := []rune(text)
	inLink := make([]bool, len(runes)) // a link's characters: its marks are its own
	for _, m := range linkRe.FindAllStringIndex(text, -1) {
		from, to := utf8.RuneCountInString(text[:m[0]]), utf8.RuneCountInString(text[:m[1]])
		for i := from; i < to; i++ {
			inLink[i] = true
		}
	}
	var out []rune
	units := 0 // UTF-16 length of out
	put := func(r ...rune) {
		out = append(out, r...)
		for _, x := range r {
			units += utf16.RuneLen(x)
		}
	}
	var styles []Style
	open := map[rune][2]int{} // a mark open: where its text starts (UTF-16), and the mark's place
	word := func(i int) bool {
		return i >= 0 && i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsNumber(runes[i]))
	}
	space := func(i int) bool { return i < 0 || i >= len(runes) || unicode.IsSpace(runes[i]) }
	for i := 0; i < len(runes); {
		if strings.HasPrefix(string(runes[i:min(i+3, len(runes))]), "```") {
			if end := indexFrom(runes, "```", i+3); end > i+3 {
				start := units
				put(runes[i+3 : end]...)
				styles = append(styles, Style{Start: start, Length: units - start, Kind: "code"})
				i = end + 3
				if i < len(runes) && runes[i] == wordJoiner {
					i++
				}
				continue
			}
		}
		r := runes[i]
		if kind, ok := singleMarks[r]; ok && !inLink[i] {
			at, isOpen := open[r]
			if isOpen && !space(i-1) && (!word(i+1) || (i+1 < len(runes) && runes[i+1] == wordJoiner)) && says(runes[at[1]+1:i]) {
				styles = append(styles, Style{Start: at[0], Length: units - at[0], Kind: kind})
				delete(open, r)
				i++
				if i < len(runes) && runes[i] == wordJoiner && word(i+1) {
					i++
				}
				continue
			}
			before := i == 0 || !word(i-1) || runes[i-1] == wordJoiner
			if !isOpen && before && !space(i+1) && closes(runes, inLink, i, r) {
				if i > 0 && runes[i-1] == wordJoiner && len(out) > 0 && out[len(out)-1] == wordJoiner {
					out = out[:len(out)-1]
					units--
				}
				open[r] = [2]int{units, i}
				i++
				continue
			}
		}
		if r == '\n' {
			open = map[rune][2]int{} // a style ends with its line
		}
		put(r)
		i++
	}
	sortStyles(styles)
	var kept []Style
	for _, s := range styles {
		if s.Length > 0 {
			kept = append(kept, s)
		}
	}
	return string(out), kept
}

// closes says whether the mark opening at i is closed later on its line, at the edge of a word, not
// within a link, a letter or a digit between them.
func closes(runes []rune, inLink []bool, i int, m rune) bool {
	for j := i + 2; j < len(runes) && runes[j] != '\n'; j++ {
		if runes[j] != m || inLink[j] || unicode.IsSpace(runes[j-1]) || !says(runes[i+1:j]) {
			continue
		}
		if j+1 >= len(runes) || runes[j+1] == wordJoiner || !(unicode.IsLetter(runes[j+1]) || unicode.IsNumber(runes[j+1])) {
			return true
		}
	}
	return false
}

// says: a letter or a digit among them (marks around only signs are an emoticon, "*_*", not a style).
func says(runes []rune) bool {
	return slices.ContainsFunc(runes, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) })
}

func indexFrom(runes []rune, s string, from int) int {
	if from > len(runes) {
		return -1
	}
	if i := strings.Index(string(runes[from:]), s); i >= 0 {
		return from + len([]rune(string(runes[from:])[:i]))
	}
	return -1
}
