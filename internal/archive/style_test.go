package archive_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"everysaid/internal/archive"
)

func st(start, length int, kind string) archive.Style {
	return archive.Style{Start: start, Length: length, Kind: kind}
}

// WhatsApp's and Viber's marks as their apps show them: the text without those that style, the
// styles in UTF-16 units; the word joiners this app writes beside a mark within a word left out;
// marks that do not style kept as they are.
func TestReadMarks(t *testing.T) {
	const wj = "⁠"
	for _, c := range []struct {
		in, text string
		styles   []archive.Style
	}{
		{"say *hi* _you_", "say hi you", []archive.Style{st(4, 2, "bold"), st(7, 3, "italic")}},
		{"```code```", "code", []archive.Style{st(0, 4, "code")}},
		{"```a\nb```", "a\nb", []archive.Style{st(0, 3, "code")}},
		{"μιλάμε για *ΑΜΑΞΙΑ*)", "μιλάμε για ΑΜΑΞΙΑ)", []archive.Style{st(11, 6, "bold")}},
		{"😀 *hi*", "😀 hi", []archive.Style{st(3, 2, "bold")}}, // an emoji is two units
		{"he" + wj + "*ll*" + wj + "o", "hello", []archive.Style{st(2, 2, "bold")}},
		{"*a*" + wj + "_b_", "ab", []archive.Style{st(0, 1, "bold"), st(1, 1, "italic")}},
		{"*_ab_*", "ab", []archive.Style{st(0, 2, "bold"), st(0, 2, "italic")}},
		{"~gone~ now", "gone now", []archive.Style{st(0, 4, "strike")}},
	} {
		text, styles := archive.ReadMarks(c.in)
		if text != c.text || !reflect.DeepEqual(styles, c.styles) {
			t.Errorf("%q: got %q %+v, want %q %+v", c.in, text, styles, c.text, c.styles)
		}
	}
	for _, s := range []string{"2*3*4", "snake_case_name", "* item", "a * b", "*open", "wo*rd*s", "*a\nb*", "**", "plain",
		"see https://x.org/_a_/b and www.y.com/~z~", "*_*", "*-*"} {
		if text, styles := archive.ReadMarks(s); text != s || styles != nil {
			t.Errorf("%q: got %q %+v, want it as it is", s, text, styles)
		}
	}
}

// A message is kept as its service's apps show it: of WhatsApp without its marks, their styles
// kept apart; of another service with the styles given.
func TestAddMessageKeepsStyles(t *testing.T) {
	a, err := archive.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	wa := a.Conversation("whatsapp", []archive.Handle{archive.H("phone", "+15550001111")}, "", "")
	tg := a.Conversation("telegram", nil, "-100", "Group")
	m1 := a.AddMessage(0, "", archive.Message{Service: "whatsapp", ConversationID: wa, TS: 1, Kind: "text", Text: "say *hi*"})
	m2 := a.AddMessage(0, "", archive.Message{Service: "telegram", ConversationID: tg, TS: 2, Kind: "text", Text: "a link",
		Extras: &archive.Extras{Styles: []archive.Style{{Start: 2, Length: 4, Kind: "link", URL: "https://x.org"}, st(0, 1, "bold")}}})
	a.Commit()
	var text string
	a.Row("SELECT text FROM message WHERE id = ?", []any{m1}, &text)
	if text != "say hi" || !reflect.DeepEqual(a.StylesOf(m1), []archive.Style{st(4, 2, "bold")}) {
		t.Errorf("whatsapp: %q %+v", text, a.StylesOf(m1))
	}
	if got := a.StylesOf(m2); !reflect.DeepEqual(got, []archive.Style{st(0, 1, "bold"), {Start: 2, Length: 4, Kind: "link", URL: "https://x.org"}}) {
		t.Errorf("telegram: %+v", got)
	}
}
