package text

import (
	"strings"
	"unicode"
)

// cased and caseIgnorable approximate Unicode's Cased and Case_Ignorable properties.
func cased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

func caseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) ||
		r == '\'' || r == '.' || r == ':' || r == '^' || r == '`' || r == 0xB7 || r == 0x2019 || r == 0x2018 || r == 0x2024 || r == 0x2027 || r == 0xAD
}

// Lower is Python's str.lower(): each letter lowered, and a Σ ending a word (a cased letter before
// it, none after it) as ς.
func Lower(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if r != 'Σ' {
			b.WriteString(strings.ToLower(string(r)))
			continue
		}
		before := false
		for j := i - 1; j >= 0; j-- {
			if caseIgnorable(rs[j]) {
				continue
			}
			before = cased(rs[j])
			break
		}
		after := false
		for j := i + 1; j < len(rs); j++ {
			if caseIgnorable(rs[j]) {
				continue
			}
			after = cased(rs[j])
			break
		}
		if before && !after {
			b.WriteRune('ς')
		} else {
			b.WriteRune('σ')
		}
	}
	return b.String()
}
