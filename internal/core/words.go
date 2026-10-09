package core

import (
	"strings"
	"unicode/utf8"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/text"
)

// WordFilters are the conditions on `message m` that find every word of a search (whole: whole
// words, a word ending in * the start of one; else anywhere, inside words too, a word of one or two
// letters at the start of words), with their arguments; none when the search has no words.
// Verify, when not nil: the messages they give are more than those that have the words, and only
// those whose text it accepts have them.
//
// The index holds words (message_fts) and the list of them (term); a word of the search is split as
// the index splits text (archive.Tokens):
//   - whole, one word of the index (signs around it left out, as the index leaves them out): the
//     messages that have it; several (signs inside, as in e-mail): those that have them one after
//     the other in their text;
//   - anywhere, one word of the index as typed: the messages that have a word containing it (the
//     words of `term` that do); else those the matcher finds (text.Matcher), among the messages that
//     have a word containing each of its words of three letters or more;
//   - none (only signs and symbols, an emoji): those the matcher finds, among the messages whose text
//     has one of the characters that fold to its first one (as typed, with caseSensitive).
func WordFilters(s *Store, q string, caseSensitive, whole bool) (conds []string, args []any, verify func(string) bool) {
	var checks []func(string) bool
	for _, w := range strings.Fields(q) {
		base := text.Fold(strings.TrimRight(w, "*"))
		if base == "" {
			continue
		}
		prefix := whole && strings.HasSuffix(w, "*")
		toks := archive.Tokens(base)
		one := len(toks) == 1 && toks[0] == base
		var c string
		var a []any
		switch {
		case whole && len(toks) == 1:
			c, a = fts(quote(toks[0]) + star(prefix))
		case whole && len(toks) > 1:
			parts := make([]string, len(toks))
			for i, t := range toks {
				parts[i] = quote(t) + star(prefix && i == len(toks)-1)
			}
			c, a = fts(strings.Join(parts, " AND "))
			checks = append(checks, inSequence(toks, prefix))
		case one && utf8.RuneCountInString(base) < 3:
			c, a = fts(quote(base) + "*")
		case one:
			c, a = containing(s, base)
		default:
			var cs []string
			for _, p := range pieces(base) {
				var pc string
				var pa []any
				switch {
				case p.after && p.before: // between signs: a word of its own
					pc, pa = fts(quote(p.word))
				case p.after: // after a sign, at the end: a word's start
					pc, pa = fts(quote(p.word) + "*")
				case utf8.RuneCountInString(p.word) >= 3: // inside a word, or its end
					pc, pa = containing(s, p.word)
				default: // short: the words that end so (before a sign) or have it, unless too many
					q := "SELECT term FROM term WHERE instr(term, ?) > 0 LIMIT ?"
					if p.before {
						q = "SELECT term FROM term WHERE substr(term, -length(?1)) = ?1 LIMIT ?2"
					}
					terms := db.Strs(s.Read(), q, p.word, fewTerms+1)
					if len(terms) > fewTerms {
						continue
					}
					pc, pa = withTerms(terms)
				}
				cs, a = append(cs, pc), append(a, pa...)
			}
			matches := text.NewMatcher(w, false, whole).Matches
			if len(cs) == 0 {
				var chars string
				c, a, chars = withCharacter(w, base, caseSensitive)
				checks = append(checks, func(t string) bool { return (chars == "" || strings.ContainsAny(t, chars)) && matches(t) })
			} else {
				c = strings.Join(cs, " AND ")
				checks = append(checks, matches)
			}
		}
		conds, args = append(conds, c), append(args, a...)
	}
	if len(checks) > 0 {
		verify = func(t string) bool {
			for _, ok := range checks {
				if !ok(t) {
					return false
				}
			}
			return true
		}
	}
	return conds, args, verify
}

// inSequence says whether a text has the words one after the other (the last one's start, with
// prefix), as the index splits it.
func inSequence(toks []string, prefix bool) func(string) bool {
	return func(t string) bool {
		words := archive.Words(text.Fold(t))
		for i := 0; i+len(toks) <= len(words); i++ {
			ok := true
			for k, w := range toks {
				last := k == len(toks)-1
				if words[i+k] != w && !(last && prefix && strings.HasPrefix(words[i+k], w)) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}
}

func quote(t string) string { return `"` + strings.ReplaceAll(t, `"`, `""`) + `"` }

func star(prefix bool) string {
	if prefix {
		return "*"
	}
	return ""
}

func fts(match string) (string, []any) {
	return "m.id IN (SELECT rowid FROM message_fts WHERE message_fts MATCH ?)", []any{match}
}

// containing is the messages that have a word containing t (t as the index writes words).
func containing(s *Store, t string) (string, []any) {
	return withTerms(db.Strs(s.Read(), "SELECT term FROM term WHERE instr(term, ?) > 0", t))
}

// fewTerms: more words than this, and a part too short to say much is better left out.
const fewTerms = 20000

// withTerms is the messages that have one of the words.
func withTerms(terms []string) (string, []any) {
	if len(terms) == 0 {
		return "0", nil
	}
	var subs []string
	var args []any
	for i := 0; i < len(terms); i += 200 { // an expression of FTS5 has a depth limit
		part := terms[i:min(i+200, len(terms))]
		for k, t := range part {
			part[k] = quote(t)
		}
		subs = append(subs, "SELECT rowid FROM message_fts WHERE message_fts MATCH ?")
		args = append(args, strings.Join(part, " OR "))
	}
	return "m.id IN (" + strings.Join(subs, " UNION ") + ")", args
}

// piece is a word of the index within a word of the search (base, folded), as the index writes it,
// and whether a sign is right before it (so it is a word's start) and right after (its end).
type piece struct {
	word          string
	after, before bool
}

// pieces are the words of the index in a folded word of the search, split by the index's own
// account of each character (archive.IsWordChar).
func pieces(base string) []piece {
	var out []piece
	var run strings.Builder
	sign := false // a sign before the run
	flush := func(before bool) {
		if w := strings.Join(archive.Words(run.String()), ""); w != "" {
			out = append(out, piece{word: w, after: sign, before: before})
		}
		run.Reset()
	}
	for _, r := range base {
		if archive.IsWordChar(r) {
			run.WriteRune(r)
			continue
		}
		if run.Len() > 0 {
			flush(true)
		}
		sign = true
	}
	if run.Len() > 0 {
		flush(false)
	}
	return out
}

// withCharacter is the messages whose text could have the word w (folded: base), by the character
// of it that the fewest others fold to: as typed (caseSensitive), it; else those that fold to it.
// When every one has many, every message with a text. Chars: those characters, one of which such a
// text has (a quick look before the matcher's).
func withCharacter(w, base string, caseSensitive bool) (cond string, args []any, chars string) {
	var runes []rune
	if caseSensitive {
		r, _ := utf8.DecodeRuneInString(strings.TrimRight(w, "*"))
		runes = []rune{r}
	} else {
		for _, r := range base {
			if p := text.Preimage(r); runes == nil || len(p) < len(runes) {
				runes = p
			}
		}
	}
	if len(runes) == 0 || len(runes) > 6 { // a scan of every text for each would be slower than reading them once
		return "m.text IS NOT NULL", nil, string(runes)
	}
	parts := make([]string, len(runes))
	args = make([]any, len(runes))
	for i, r := range runes {
		parts[i], args[i] = "instr(m.text, ?) > 0", string(r)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args, string(runes)
}
