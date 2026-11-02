package signal

import (
	"reflect"
	"testing"

	"everysaid/internal/plugins"
)

// Styles where they are in the text as sent: a mention one character, an emoji two UTF-16 units.
func TestStylesOut(t *testing.T) {
	text := "😀 hi @Anna there"
	mentions := []plugins.Mention{{Start: 5, Length: 5, AddressID: 1}}
	written := "😀 hi ￼ there"
	got := stylesOut(text, written, mentions, []plugins.Style{
		{Start: 2, Length: 2, Kind: plugins.Bold},
		{Start: 7, Length: 9, Kind: plugins.Italic}, // from within the mention: all of it
		{Start: 11, Length: 5, Kind: plugins.Spoiler},
	})
	want := []outStyle{{3, 2, "bold"}, {6, 7, "italic"}, {8, 5, "spoiler"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
