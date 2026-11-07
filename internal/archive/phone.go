package archive

import (
	"strconv"
	"strings"

	"github.com/nyaruka/phonenumbers/v2"
)

// Phone is +E.164 where the number is valid as written (national ones in `region`) or with a '+'
// put before it; otherwise short codes (under 10 digits) as bare digits (the iPhone adds a '+',
// Android does not), and anything longer as '+digits'. A foreign number saved without its '+' can
// come with the region's code put before it (the iPhone keeps "+44 7785…" typed as "447785…" as
// "+30447785…"): one not valid so, longer than the region's numbers, whose rest is a possible
// international number, is that number.
func Phone(digits, region string) string {
	n := phone(digits, region)
	if cc, ex := phonenumbers.GetCountryCodeForRegion(region), phonenumbers.GetExampleNumber(region); cc != 0 && ex != nil {
		prefix := "+" + strconv.Itoa(cc)
		long := len(phonenumbers.GetNationalSignificantNumber(ex))
		if rest, ok := strings.CutPrefix(n, prefix); ok && len(rest) > long && !valid(n) {
			if m, err := phonenumbers.Parse("+"+rest, ""); err == nil && phonenumbers.IsPossibleNumber(m) {
				return phone("+"+rest, "")
			}
		}
	}
	return n
}

func valid(s string) bool {
	n, err := phonenumbers.Parse(s, "")
	return err == nil && phonenumbers.IsValidNumber(n)
}

func phone(digits, region string) string {
	bare := strings.TrimLeft(digits, "+")
	type try struct{ text, region string }
	tries := []try{{digits, ""}}
	if !strings.HasPrefix(digits, "+") {
		tries = []try{{bare, region}, {"+" + bare, ""}}
	}
	for _, t := range tries {
		if t.region == "" && !strings.HasPrefix(t.text, "+") {
			continue
		}
		n, err := phonenumbers.Parse(t.text, t.region)
		if err != nil {
			continue
		}
		if phonenumbers.IsValidNumber(n) {
			return phonenumbers.Format(n, phonenumbers.E164)
		}
	}
	if len([]rune(bare)) < 10 {
		return bare
	}
	return "+" + bare
}
