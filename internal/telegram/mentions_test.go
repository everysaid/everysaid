package telegram

import (
	"testing"
	"unicode/utf16"

	"github.com/gotd/td/tg"

	"everysaid/internal/archive"
	"everysaid/internal/plugins"
)

// Two people named, the mentions given out of order: each "@" dropped, each link where its name is
// in the text as sent (counted in UTF-16 units: the emoji before them is two).
func TestTelegramMentionsSentEachAtItsPlace(t *testing.T) {
	in := newInstance(t, nil)
	a, _ := archive.Open(in.path)
	first, second := archive.H("id", "2", "telegram"), archive.H("id", "3", "telegram")
	conv := a.Conversation("telegram", []archive.Handle{first, second}, "-10", "group")
	firstID, secondID := a.Address(first), a.Address(second)
	a.Resolve()
	a.Commit()
	a.Close()

	cn := account().conn()
	if _, err := cn.dialogs(newTestCtx()); err != nil {
		t.Fatal(err)
	}
	text := "🙂 @one and @two!"
	out, entities, err := mentionEntities(in.ctx(), cn, text, []plugins.Mention{
		{Start: 11, Length: 4, AddressID: secondID}, {Start: 2, Length: 4, AddressID: firstID}}, conv)
	if err != nil {
		t.Fatal(err)
	}
	if out != "🙂 one and two!" {
		t.Fatalf("%q", out)
	}
	units := utf16.Encode([]rune(out))
	type said struct {
		text string
		user int64
	}
	var got []said
	for _, e := range entities {
		m := e.(*tg.InputMessageEntityMentionName)
		got = append(got, said{string(utf16.Decode(units[m.Offset : m.Offset+m.Length])), m.UserID.(*tg.InputUser).UserID})
	}
	if len(got) != 2 || got[0] != (said{"one", 2}) || got[1] != (said{"two", 3}) {
		t.Fatalf("%v", got)
	}
}
