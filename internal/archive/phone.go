package archive

import (
	"strings"

	"github.com/nyaruka/phonenumbers/v2"
)

// Phone is +E.164 where the number is valid as written (national ones in `region`) or with a '+'
// put before it; otherwise short codes (under 10 digits) as bare digits (the iPhone adds a '+',
// Android does not), and anything longer as '+digits'.
func Phone(digits, region string) string {
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
