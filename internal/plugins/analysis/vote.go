// Package analysis holds the analysis plugin: local models read the archive and suggest what no
// source says. Ports everysaid/plugins/analysis.py.
//
// The one here asks models served by Ollama on this computer (or its own network: nothing goes
// further) to read a little of each chat of a person without a name, and say their name, with the
// line that shows it, the chat's tone and who the person is to the owner, from the user's lists of
// labels (core/labels.go). With two or three models they vote. Nothing is applied: what they say
// is a suggestion the user accepts or turns down.
//
// It works in the background while it is turned on, the largest chats first, and reads a chat
// again when it has grown by half; "judge again" reads again those read by another list of labels.
package analysis

import (
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"everysaid/internal/core"
	"everysaid/internal/text"
)

// LocalAddress says whether the URL's host is this computer or on its own network (a private address).
func LocalAddress(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return false
		}
		for _, ip := range ips {
			a, _ := netip.AddrFromSlice(ip)
			addrs = append(addrs, a)
		}
	}
	for _, a := range addrs {
		a = a.Unmap()
		if !(a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified()) {
			return false
		}
	}
	return true
}

// pySplit is Python's str.split(): words between runs of whitespace.
func pySplit(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) })
}

func runes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// shown says whether every word of the name (less its ending, for its other cases) is in the text.
func shown(name, txt string) bool {
	t := text.Fold(txt)
	for _, w := range pySplit(name) {
		n := len([]rune(w)) - 2
		if n < 3 {
			n = 3
		}
		if !strings.Contains(t, runes(text.Fold(w), n)) {
			return false
		}
	}
	return true
}

// copied says whether a line the model says it copied is in the text (a few words at least).
func copied(line, txt string) bool {
	l := strings.Join(pySplit(text.Fold(line)), " ")
	return len([]rune(l)) >= 8 && strings.Contains(strings.Join(pySplit(text.Fold(txt)), " "), l)
}

var notAName = regexp.MustCompile(`[@\p{Nd}_/\\]`)

// aName is a model's name, if it can be one: not an email, a handle or a number, not the owner's.
func aName(v any, handles []string, own map[string]bool) string {
	name, ok := v.(string)
	if !ok {
		return ""
	}
	name = strings.Trim(strings.Join(pySplit(name), " "), " .,:;\"'")
	f := text.Fold(name)
	if name == "" || f == "null" || f == "none" || f == "unknown" || len([]rune(name)) > 60 || len(pySplit(name)) > 4 {
		return ""
	}
	if notAName.MatchString(name) {
		return ""
	}
	for _, h := range handles {
		if f == text.Fold(h) {
			return ""
		}
	}
	all := true
	for _, w := range pySplit(name) {
		if !own[core.SoundKey(w)] {
			all = false
			break
		}
	}
	if all {
		return ""
	}
	return name
}

// counter counts in the order things are first seen (Python's Counter, whose ties go by that order).
type counter struct {
	n     map[int64]int
	order []int64
}

func (c *counter) add(k int64) {
	if c.n == nil {
		c.n = map[int64]int{}
	}
	if _, ok := c.n[k]; !ok {
		c.order = append(c.order, k)
	}
	c.n[k]++
}

// mostCommon is the keys by count, the first seen first between equals.
func (c *counter) mostCommon() []int64 {
	out := append([]int64{}, c.order...)
	// a stable insertion sort: the lists are a few labels long
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && c.n[out[j]] > c.n[out[j-1]]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func strList(v any) []string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// Vote makes the models' answers one: (name, tones, relation) as core.SaveAnalysis takes them. A
// name counts where its words are in what they read, the same name in another case,
// and with a surname or without as one; the most said wins.
// A tone needs most of the models (a sensitive one, two at least, each with a line it copied); a
// relation, most of them.
func Vote(answers []map[string]any, txt string, handles []string, own map[string]bool,
	tones, relations []core.ModelLabel) (*core.NameJudged, []core.Judged, *core.Judged) {
	n := len(answers)
	most := n/2 + 1
	type said struct {
		name     string
		evidence any
	}
	groups := map[string][]said{}
	var order []string
	for _, a := range answers {
		name := aName(a["name"], handles, own)
		if name != "" && shown(name, txt) {
			k := core.SoundKey(pySplit(name)[0])
			if _, ok := groups[k]; !ok {
				order = append(order, k)
			}
			groups[k] = append(groups[k], said{name, a["evidence"]})
		}
	}
	var name *core.NameJudged
	if len(order) > 0 {
		group := groups[order[0]]
		for _, k := range order[1:] {
			if len(groups[k]) > len(group) {
				group = groups[k]
			}
		}
		forms := map[string]int{}
		var formOrder []string
		for _, s := range group {
			if _, ok := forms[s.name]; !ok {
				formOrder = append(formOrder, s.name)
			}
			forms[s.name]++
		}
		// the most said, then the longest, then one in the nominative (its ending s or ς)
		rank := func(f string) [3]int {
			nom := 0
			if strings.HasSuffix(f, "s") || strings.HasSuffix(f, string(rune(0x3c2))) {
				nom = 1
			}
			return [3]int{forms[f], len(pySplit(f)), nom}
		}
		best := formOrder[0]
		for _, f := range formOrder[1:] {
			if r, b := rank(f), rank(best); r[0] > b[0] || r[0] == b[0] && (r[1] > b[1] || r[1] == b[1] && r[2] > b[2]) {
				best = f
			}
		}
		evidence := ""
		for _, s := range group {
			if e, ok := s.evidence.(string); ok && s.name == best {
				evidence = e
				break
			}
		}
		name = &core.NameJudged{Name: best, Votes: len(group), Of: n, Evidence: runes(evidence, 200)}
	}

	byWord := map[string]core.ModelLabel{}
	sensitive := map[int64]bool{}
	for _, t := range tones {
		byWord[t.Word] = t
		if t.Sensitive {
			sensitive[t.ID] = true
		}
	}
	var counted counter
	lines := map[int64]string{}
	for _, a := range answers {
		seen := map[string]bool{}
		for _, w := range strList(a["tone"]) {
			if seen[w] {
				continue
			}
			seen[w] = true
			t, ok := byWord[w]
			if !ok {
				continue
			}
			if t.Sensitive {
				line, ok := a["sensitive_evidence"].(string)
				if !ok || !copied(line, txt) {
					continue
				}
				if _, ok := lines[t.ID]; !ok {
					lines[t.ID] = runes(line, 200)
				}
			}
			counted.add(t.ID)
		}
	}
	chosen := []core.Judged{}
	for _, i := range counted.mostCommon() {
		need := most
		if sensitive[i] && need < 2 {
			need = 2
		}
		if v := counted.n[i]; v >= need {
			chosen = append(chosen, core.Judged{LabelID: i, Votes: v, Of: n, Evidence: lines[i]})
		}
	}

	relIDs := map[string]int64{}
	for _, r := range relations {
		relIDs[r.Word] = r.ID
	}
	var rel counter
	for _, a := range answers {
		if w, ok := a["relationship"].(string); ok {
			if i, ok := relIDs[w]; ok {
				rel.add(i)
			}
		}
	}
	var relation *core.Judged
	if top := rel.mostCommon(); len(top) > 0 && rel.n[top[0]] >= most {
		relation = &core.Judged{LabelID: top[0], Votes: rel.n[top[0]], Of: n}
	}
	return name, chosen, relation
}
