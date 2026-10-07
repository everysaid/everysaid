// Ports everysaid/carriers/__init__.py and everysaid/carriers/gr.py: parsers of the notices
// carriers send as SMS about calls the phone did not get, one per carrier or country, enabled by
// config [import] carrier_notices (e.g. ["gr"]).
//
// Each has Source, the archive's source name for the calls it finds, and Alerts(text, sent): the
// calls a notice tells of, sent being when the notice came.
package importers

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Alert is one call a carrier's notice tells of.
type Alert struct {
	Number   string
	When     time.Time
	Attempts int
	Busy     bool
}

// Carrier is a parser of one carrier's (or country's) notices.
type Carrier struct {
	Name, Source string
	Alerts       func(text string, sent time.Time) []Alert
}

var carriers = map[string]Carrier{"gr": {"gr", "sms-alerts", grAlerts}}

// EnabledCarriers are the parsers of the names given; an unknown name is an error.
func EnabledCarriers(names []string) ([]Carrier, error) {
	var out []Carrier
	for _, n := range names {
		c, ok := carriers[n]
		if !ok {
			return nil, fmt.Errorf("unknown carrier in [import] carrier_notices: %s", n)
		}
		out = append(out, c)
	}
	return out, nil
}

// --- gr: missed-call notices of Greek carriers, sent as SMS when a call came while the phone was
// off or busy: "ΕΙΧΑΤΕ 2 ΚΛΗΣΕΙΣ: ...", "ΚΛΗΣΕΙΣ: ...", "ΔΩΡΕΑΝ ΕΝΗΜΕΡΩΣΗ: ΕΙΧΑΤΕ 1 ΚΛΗΣΗ ΑΠΟ ΤΟ ...",
// with each caller's number, the time (Greek time) and how many times they called.

var grTZ = mustZone("Europe/Athens")

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// The carrier writes Greek in capitals, often with Latin look-alikes (KΛHΣEIΣ): compare in Latin.
var grLookalike = strings.NewReplacer("Α", "A", "Β", "B", "Ε", "E", "Ζ", "Z", "Η", "H", "Ι", "I", "Κ", "K", "Μ", "M",
	"Ν", "N", "Ο", "O", "Ρ", "P", "Τ", "T", "Υ", "Y", "Χ", "X")

// Python's \s and \d (any whitespace, any decimal digit).
const (
	pyS = `[\t\n\v\f\r\x1c-\x1f \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]`
	pyD = `\p{Nd}`
)

func pyRE(re string) *regexp.Regexp {
	return regexp.MustCompile(strings.NewReplacer(`\s`, pyS, `\d`, pyD).Replace(re))
}

var (
	grAlert = pyRE(`^\s*(?:ΔΩPEAN ENHMEPΩΣH:\s*)?(?:EIXATE \d+ KΛHΣ|KΛHΣEIΣ:)`)
	// [\d ] in Python: a digit or a space
	grNumber = regexp.MustCompile(`\+?` + pyD + `(?:` + pyD + `| ){6,}` + pyD)
	grWhen   = pyRE(`(?P<d>\d{1,2})[/-](?P<m>\d{1,2})(?:[/-](?P<y>\d{2,4}))?\s*(?:,\s*|\s+)(?P<H>\d{1,2}):(?P<M>\d{2})` +
		`|(?P<H2>\d{1,2}):(?P<M2>\d{2}),(?P<d2>\d{1,2})/(?P<m2>\d{1,2})/(?P<y2>\d{2})`)
	grCount = pyRE(`\((\d+)\)|(\d+)\s*ΦOPEΣ`)
	grTotal = pyRE(`EIXATE (\d+) KΛHΣ`)
)

// searchFrom is the first match of re in t at or after byte pos, in t's byte offsets.
func searchFrom(re *regexp.Regexp, t string, pos int) []int {
	m := re.FindStringSubmatchIndex(t[pos:])
	if m == nil {
		return nil
	}
	for i := range m {
		if m[i] >= 0 {
			m[i] += pos
		}
	}
	return m
}

// grAlerts are the calls of a notice; sent: when the notice came.
func grAlerts(text string, sent time.Time) []Alert {
	t := grLookalike.Replace(upper(text))
	sent = sent.In(grTZ)
	if !grAlert.MatchString(t) {
		return nil
	}
	busy := strings.Contains(t, "KATEIΛHMMENOΣ")
	total := grTotal.FindStringSubmatch(t)
	type found struct {
		num  []int
		when []int
	}
	var fs []found
	for _, num := range grNumber.FindAllStringIndex(t, -1) {
		when := searchFrom(grWhen, t, num[1])
		if when == nil {
			break
		}
		if nxt := searchFrom(grNumber, t, num[1]); nxt != nil && nxt[0] < when[0] {
			continue // another number before the time: this one is not a caller
		}
		if len(fs) > 0 && fs[len(fs)-1].when[0] == when[0] {
			continue
		}
		fs = append(fs, found{num, when})
	}
	names := grWhen.SubexpNames()
	var out []Alert
	for _, f := range fs {
		g := map[string]string{}
		for i, n := range names {
			if n != "" && f.when[2*i] >= 0 {
				g[n] = t[f.when[2*i]:f.when[2*i+1]]
			}
		}
		either := func(a, b string) string {
			if g[a] != "" {
				return g[a]
			}
			return g[b]
		}
		day, month := atoiPy(either("d", "d2")), atoiPy(either("m", "m2"))
		hour, minute := atoiPy(either("H", "H2")), atoiPy(either("M", "M2"))
		var year int
		if y := either("y", "y2"); y != "" {
			year = 2000 + atoiPy(y)%100
		} else {
			year = sent.Year()
			if month > int(sent.Month()) {
				year--
			}
		}
		w, ok := newWall(year, time.Month(month), day, hour, minute, 0, grTZ)
		if !ok {
			continue
		}
		// a count near the number: from its end to 12 characters past the time
		rest := []rune(t[f.when[1]:])
		near := t[f.num[1]:f.when[1]] + string(rest[:min(12, len(rest))])
		attempts := 1
		if c := grCount.FindStringSubmatch(near); c != nil && (c[1] != "" || c[2] != "") {
			attempts = atoiPy(c[1] + c[2])
		} else if total != nil && len(fs) == 1 {
			attempts = atoiPy(total[1])
		}
		out = append(out, Alert{strings.ReplaceAll(t[f.num[0]:f.num[1]], " ", ""), w.instant(), attempts, busy})
	}
	return out
}
