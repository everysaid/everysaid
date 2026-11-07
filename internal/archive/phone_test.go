package archive

import "testing"

func TestPhone(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"6912345678", "+306912345678"},
		{"+306912345678", "+306912345678"},
		{"447700900123", "+447700900123"},
		{"+447700900123", "+447700900123"},
		// the region's code put before a foreign number saved without its '+'
		{"+30447700900123", "+447700900123"},
		{"30447700900123", "+447700900123"},
		{"+3012025550123", "+12025550123"},
		{"+30351912345678", "+351912345678"},
		// not a number without the region's code either: left as it is
		{"+3069123456789", "+3069123456789"},
		{"+30032486672309", "+30032486672309"},
		{"12345", "12345"},
	} {
		if got := Phone(c.in, "GR"); got != c.want {
			t.Errorf("Phone(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
