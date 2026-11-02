package telegram

import (
	"reflect"
	"testing"

	"github.com/gotd/td/tg"

	"everysaid/internal/plugins"
)

// Styles as entities of the text as written: past a mention written without its "@", in UTF-16
// units (an emoji is two), crossing ones cut to nest.
func TestStyleEntities(t *testing.T) {
	text := "😀 hi @Anna there\nx y"
	mentions := []plugins.Mention{{Start: 5, Length: 5, AddressID: 1}}
	written := "😀 hi Anna there\nx y"
	got := styleEntities(text, written, mentions, []plugins.Style{
		{Start: 2, Length: 2, Kind: plugins.Bold},    // "hi"
		{Start: 7, Length: 9, Kind: plugins.Italic},  // from within the mention: all of it, to "there"
		{Start: 11, Length: 7, Kind: plugins.Strike}, // "there\nx": crosses the italic
		{Start: 19, Length: 1, Kind: plugins.Code},   // "y" (as the host gives them, nothing over code)
	}, maxEntities)
	want := []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 3, Length: 2},
		&tg.MessageEntityItalic{Offset: 6, Length: 10},
		&tg.MessageEntityStrike{Offset: 11, Length: 5},
		&tg.MessageEntityStrike{Offset: 16, Length: 2},
		&tg.MessageEntityCode{Offset: 19, Length: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// code over lines: a block
	block := styleEntities("a\nb", "a\nb", nil, []plugins.Style{{Start: 0, Length: 3, Kind: plugins.Code}}, maxEntities)
	if want := []tg.MessageEntityClass{&tg.MessageEntityPre{Offset: 0, Length: 3}}; !reflect.DeepEqual(block, want) {
		t.Errorf("block: got %v", block)
	}
	if got := styleEntities(text, written, mentions, []plugins.Style{{Start: 0, Length: 1, Kind: plugins.Bold}}, 0); got != nil {
		t.Errorf("no room: %v", got)
	}
}
