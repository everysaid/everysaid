package core_test

import (
	"fmt"
	"strings"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/text"
)

// The index finds what the search means, one message by one: for parts of words taken from the
// demo's own texts, and for words with signs inside or of nothing but signs and emoji, the messages
// whose text the matcher matches; for whole words, those that have the word's words one after the
// other (signs left out, as the index leaves them; with none, the matcher's).
func TestWordFiltersFindWhatTheMatcherFinds(t *testing.T) {
	s := store(t)
	q := s.Read()
	type msg struct {
		id    int64
		text  string
		words []string // as the index splits it
	}
	var all []msg
	db.Each(q, "SELECT id, text FROM message WHERE text IS NOT NULL ORDER BY id", nil, func(scan func(...any)) {
		var m msg
		scan(&m.id, &m.text)
		all = append(all, m)
	})
	for i := range all {
		all[i].words = archive.Words(text.Fold(all[i].text))
	}
	words := map[string]bool{"e-mail": true, "wi-fi": true, "😂": true, "👍": true, "❤️": true, "!": true, "...": true,
		":)": true, "10'": true, "?": true, "καλημέρα!": true, "Wi-Fi γραφείου": true}
	for i, m := range all {
		r := []rune(m.text)
		if i%61 != 0 || len(r) < 3 {
			continue
		}
		for _, n := range []int{3, 4, 6} {
			if a := (i / 61) % len(r); a+n <= len(r) {
				words[strings.TrimSpace(string(r[a:a+n]))] = true
			}
		}
	}
	checked := 0
	for w := range words {
		for _, c := range []struct{ caseSensitive, whole bool }{{false, false}, {false, true}, {true, false}} {
			short := false
			for _, x := range strings.Fields(w) {
				short = short || (!c.whole && len([]rune(text.Fold(x))) < 3)
			}
			if strings.TrimSpace(w) == "" || short {
				continue // one or two letters: at the start of words, not anywhere
			}
			conds, args, verify := core.WordFilters(s, w, c.caseSensitive, c.whole)
			if len(conds) == 0 {
				continue
			}
			m := text.NewMatcher(w, c.caseSensitive, c.whole)
			means := func(x msg) bool { return m.Matches(x.text) }
			if c.whole {
				has := hasWords(w)
				means = func(x msg) bool { return has(x.words, x.text) }
			}
			got := map[int64]bool{}
			db.Each(q, "SELECT m.id, m.text FROM message m WHERE "+strings.Join(conds, " AND "), args, func(scan func(...any)) {
				var id int64
				var txt string
				scan(&id, &txt)
				if (verify == nil || verify(txt)) && (!c.caseSensitive || m.Matches(txt)) {
					got[id] = true
				}
			})
			var miss, extra []int64
			want := 0
			for _, x := range all {
				if means(x) {
					want++
					if !got[x.id] {
						miss = append(miss, x.id)
					}
				} else if got[x.id] {
					extra = append(extra, x.id)
				}
			}
			if len(miss) > 0 || len(extra) > 0 {
				t.Errorf("%q %+v (exact %v): %d found, %d want; missing %v, more %v", w, c, verify == nil, len(got), want,
					head(miss), head(extra))
			}
			checked++
		}
	}
	if checked < 200 {
		t.Fatalf("only %d searches checked", checked)
	}
}

// hasWords: whole words, every word of q, its words (as the index splits it) one after the other
// in the text's words; a word without any, the matcher's.
func hasWords(q string) func(words []string, t string) bool {
	var each []func(words []string, t string) bool
	for _, w := range strings.Fields(q) {
		toks := strings.Join(archive.Tokens(text.Fold(w)), " ")
		if toks == "" {
			m := text.NewMatcher(w, false, true)
			each = append(each, func(_ []string, t string) bool { return m.Matches(t) })
			continue
		}
		n := len(strings.Fields(toks))
		each = append(each, func(words []string, _ string) bool {
			for i := 0; i+n <= len(words); i++ {
				if strings.Join(words[i:i+n], " ") == toks {
					return true
				}
			}
			return false
		})
	}
	return func(words []string, t string) bool {
		for _, ok := range each {
			if !ok(words, t) {
				return false
			}
		}
		return true
	}
}

func head(ids []int64) string {
	if len(ids) > 5 {
		return fmt.Sprint(ids[:5], "…")
	}
	return fmt.Sprint(ids)
}
