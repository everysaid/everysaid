package importers

import "testing"

// The app of a CallKit call, by its bundle id (with or without the team id), a variant of a known
// one, or kept as it is when unknown.
func TestServiceOf(t *testing.T) {
	for provider, want := range map[string]string{
		"":                                 "phone",
		"com.apple.Telephony":              "phone",
		"UKFA9XBX6K.net.whatsapp.WhatsApp": "whatsapp",
		"com.viber.voip":                   "viber",
		"L5E8HT2MN6.cz.acrobits.softphone.business": "sip",
		"com.example.unknown":                       "com.example.unknown",
	} {
		if got := serviceOf(provider); got != want {
			t.Errorf("serviceOf(%q) = %q, want %q", provider, got, want)
		}
	}
}
