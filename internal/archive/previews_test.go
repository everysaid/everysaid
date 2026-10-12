package archive_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"testing"

	"everysaid/internal/archive"
)

// A link's preview: kept with its picture made small (JPEG, once for the same picture), brought up
// to date when its source says otherwise, its picture kept when the source is read again without
// it, nothing kept for a preview that says nothing.
func TestPreview(t *testing.T) {
	a, err := archive.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	src := a.Source("test", "/x", "", "")
	conv := a.Conversation("whatsapp", []archive.Handle{archive.H("phone", "+15550001111")}, "", "")
	big := image.NewNRGBA(image.Rect(0, 0, 1200, 600))
	for x := 0; x < 1200; x++ {
		big.Set(x, x/2, color.NRGBA{200, 10, 10, 255})
	}
	var pic bytes.Buffer
	png.Encode(&pic, big)
	p := &archive.Preview{URL: "https://example.org/a", Site: "Example", Title: "A page", Description: "About it", Image: pic.Bytes()}
	m1 := a.AddMessage(src, "1", archive.Message{Service: "whatsapp", ConversationID: conv, TS: 1, Kind: "text",
		Text: "see https://example.org/a", Extras: &archive.Extras{Subtype: "link", Preview: p}})
	m2 := a.AddMessage(src, "2", archive.Message{Service: "whatsapp", ConversationID: conv, TS: 2, Kind: "text",
		Text: "again https://example.org/a", Extras: &archive.Extras{Preview: p}})
	m3 := a.AddMessage(src, "3", archive.Message{Service: "whatsapp", ConversationID: conv, TS: 3, Kind: "text",
		Text: "https://example.org/b", Extras: &archive.Extras{Preview: &archive.Preview{URL: "https://example.org/b"}}})
	if n := a.Int("SELECT count(*) FROM link_preview_image"); n != 1 {
		t.Fatalf("the same picture kept %d times", n)
	}
	var w, h int
	a.Row("SELECT width, height FROM link_preview_image", nil, &w, &h)
	if w != 480 || h != 240 {
		t.Fatalf("picture kept as %dx%d, want 480x240", w, h)
	}
	if a.Exists("SELECT 1 FROM link_preview WHERE message_id = ?", m3) {
		t.Fatal("a preview that says nothing was kept")
	}
	if a.SetPreview(m1, p) {
		t.Fatal("the same preview again said changed")
	}
	if !a.SetPreview(m1, &archive.Preview{URL: p.URL, Site: p.Site, Title: "Another title", Description: p.Description}) {
		t.Fatal("a new title not kept")
	}
	var title string
	var img *string
	a.Row("SELECT title, image FROM link_preview WHERE message_id = ?", []any{m1}, &title, &img)
	if title != "Another title" || img == nil {
		t.Fatalf("after the change: title %q, picture %v (kept when not said)", title, img)
	}
	_ = m2
}
