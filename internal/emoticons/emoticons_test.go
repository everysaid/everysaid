package emoticons

import "testing"

func TestViberEmoticonsAsEmoji(t *testing.T) {
	for in, want := range map[string]string{
		"καλημέρα (inlove)(purple_heart)": "καλημέρα 😍💜",
		"ok (like) (windows) (2019)":      "ok 👍 (windows) (2019)", // text stays text
		"":                                "",
	} {
		if got := ViberEmoji(in); got != want {
			t.Errorf("ViberEmoji(%q) = %q, want %q", in, got, want)
		}
	}
}
