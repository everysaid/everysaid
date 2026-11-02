package plugins

import (
	"reflect"
	"testing"
)

// The text's styles written as WhatsApp's and Viber's marks: at the edges of words as typed, a
// word joiner beside a mark within a word or between two marks, a style over lines in each.
func TestMarkStyles(t *testing.T) {
	const wj = "⁠"
	for _, c := range []struct {
		name, text string
		styles     []Style
		want       string
	}{
		{"word", "hello world", []Style{{0, 5, Bold, ""}}, "*hello* world"},
		{"within a word", "hello", []Style{{2, 2, Bold, ""}}, "he" + wj + "*ll*" + wj + "o"},
		{"word end", "hello", []Style{{2, 3, Bold, ""}}, "he" + wj + "*llo*"},
		{"greek", "γεια σου", []Style{{0, 4, Italic, ""}}, "_γεια_ σου"},
		{"spaces left out", " a b ", []Style{{0, 5, Strike, ""}}, " ~a b~ "},
		{"nested", "a b c", []Style{{0, 5, Bold, ""}, {2, 1, Italic, ""}}, "*a _b_ c*"},
		{"same place", "ab", []Style{{0, 2, Italic, ""}, {0, 2, Bold, ""}}, "*_ab_*"},
		{"crossing", "a b c", []Style{{0, 3, Bold, ""}, {2, 3, Italic, ""}}, "*a _b_*" + wj + "_ c_"},
		{"side by side", "ab", []Style{{0, 1, Bold, ""}, {1, 1, Italic, ""}}, "*a*" + wj + "_b_"},
		{"over lines", "one\ntwo", []Style{{0, 7, Bold, ""}}, "*one*\n*two*"},
		{"code over lines", "a\nb", []Style{{0, 3, Code, ""}}, "```a\nb```"},
		{"nothing in code", "abc", []Style{{0, 3, Bold, ""}, {1, 1, Code, ""}}, "*a*" + wj + "```b```" + wj + "*c*"},
		{"no mark for spoiler", "a b", []Style{{0, 1, Spoiler, ""}}, "a b"},
		{"punctuation after", "hi, you", []Style{{0, 2, Bold, ""}}, "*hi*, you"},
		{"after a bracket", "(hi)", []Style{{1, 2, Bold, ""}}, "(" + wj + "*hi*)"},
	} {
		got, _ := MarkStyles(c.text, nil, c.styles)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A style is cut where part of it is cleared: what is left, before and after, goes as two.
func TestMarkStylesClearedInPart(t *testing.T) {
	// "hello world" bold, "lo wo" then cleared: the UI sends what is left
	got, _ := MarkStyles("hello world", nil, []Style{{0, 3, Bold, ""}, {8, 3, Bold, ""}})
	if want := "*hel*⁠lo wo⁠*rld*"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Mentions move with the marks before them, and a style is never cut inside one.
func TestMarkStylesMentions(t *testing.T) {
	text := "hi @Anna Smith and you"
	ms := []Mention{{Start: 3, Length: 11, AddressID: 7}}
	got, moved := MarkStyles(text, ms, []Style{{0, 2, Bold, ""}, {6, 12, Italic, ""}})
	if want := "*hi* _@Anna Smith and_ you"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	r := []rune(got)
	if m := moved[0]; string(r[m.Start:m.Start+m.Length]) != "@Anna Smith" || m.AddressID != 7 {
		t.Errorf("mention at %+v: %q", m, string(r[m.Start:m.Start+m.Length]))
	}
}

func TestFitStyles(t *testing.T) {
	got := FitStyles("abc def", []Style{
		{0, 2, Bold, ""}, {1, 3, Bold, ""}, // over each other: one, its space left out
		{4, 10, Italic, ""}, // past the end: to the end
		{0, 3, Spoiler, ""}, // not a kind given
		{2, 0, Strike, ""}, {-1, 2, Strike, ""},
	}, []string{Bold, Italic, Strike})
	want := []Style{{0, 3, Bold, ""}, {4, 3, Italic, ""}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// code takes its place from the others
	got = FitStyles("abcd", []Style{{0, 4, Bold, ""}, {1, 2, Code, ""}}, []string{Bold, Code})
	want = []Style{{0, 1, Bold, ""}, {1, 2, Code, ""}, {3, 1, Bold, ""}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("code: got %+v, want %+v", got, want)
	}
}

func TestNestStyles(t *testing.T) {
	got := NestStyles([]Style{{0, 4, Bold, ""}, {2, 4, Italic, ""}, {3, 5, Strike, ""}})
	want := []Style{{0, 4, Bold, ""}, {2, 2, Italic, ""}, {3, 1, Strike, ""}, {4, 4, Strike, ""}, {4, 2, Italic, ""}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// of any two, one in the other or apart
	for i, a := range got {
		for _, b := range got[i+1:] {
			ae, be := a.Start+a.Length, b.Start+b.Length
			if a.Start < be && b.Start < ae && !(a.Start <= b.Start && be <= ae) && !(b.Start <= a.Start && ae <= be) {
				t.Errorf("%+v and %+v cross", a, b)
			}
		}
	}
}

// "@Anna Smith" written as "Anna Smith" (Telegram) and as one character (Signal).
func TestMoveStyles(t *testing.T) {
	styles := []Style{{0, 2, Bold, ""}, {3, 11, Italic, ""}, {6, 4, Strike, ""}, {15, 3, Bold, ""}}
	got := MoveStyles(styles, []Replaced{{Start: 3, Length: 11, Runes: 10}})
	want := []Style{{0, 2, Bold, ""}, {3, 10, Italic, ""}, {3, 10, Strike, ""}, {14, 3, Bold, ""}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	got = MoveStyles(styles, []Replaced{{Start: 3, Length: 11, Runes: 1}})
	want = []Style{{0, 2, Bold, ""}, {3, 1, Italic, ""}, {3, 1, Strike, ""}, {5, 3, Bold, ""}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("one character: got %+v, want %+v", got, want)
	}
}

func TestUTF16(t *testing.T) {
	r := []rune("😀 γεια")
	if off, n := UTF16(r, 2, 4); off != 3 || n != 4 {
		t.Errorf("got %d %d", off, n)
	}
}
