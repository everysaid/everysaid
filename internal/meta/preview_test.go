package meta

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"go.mau.fi/util/jsontime"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"everysaid/internal/db"
	"everysaid/internal/importers"
)

func pngBytes() []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 40, 20)))
	return b.Bytes()
}

func testSession(t *testing.T, settings M) (*instance, *liveSession) {
	in := newInstance(t, "messenger", settings)
	loggedInSession(t, in.iid)
	return in, &liveSession{c: in.ctx(), service: "messenger", self: "100", selfName: "Me Owner",
		names: map[string]importers.MetaUser{}, threads: map[string]threadInfo{}, wake: make(chan struct{}, 1),
		history: make(chan rangeOp, 1), tr: newTranslator("100"), cli: &fakeClient{}, sess: &session{}}
}

// A shared link's title, description and site go into its preview, its picture downloaded at once
// (even where files are not), not as the message's file; the text keeps what was written.
func TestLinkPreviewLive(t *testing.T) {
	pic := pngBytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(pic) }))
	t.Cleanup(srv.Close)
	in, s := testSession(t, M{"media": false})
	ctx := context.Background()
	tbl := &table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{{ThreadKey: john, ThreadType: table.ONE_TO_ONE}},
		LSInsertMessage: []*table.LSInsertMessage{
			{ThreadKey: john, MessageId: "mid.l1", SenderId: john, TimestampMs: 1600000000000, Text: "read this"},
			{ThreadKey: john, MessageId: "mid.l2", SenderId: john, TimestampMs: 1600000001000},
		},
		LSInsertXmaAttachment: []*table.LSInsertXmaAttachment{
			{MessageId: "mid.l1", ThreadKey: john, AttachmentFbid: "x1",
				ActionUrl: "https://l.facebook.com/l.php?u=https%3A%2F%2Fexample.com%2Fa", TitleText: "A title",
				SubtitleText: "example.com", DescriptionText: "", PreviewUrl: srv.URL + "/p.jpg", PreviewUrlMimeType: "image/jpeg"},
			{MessageId: "mid.l2", ThreadKey: john, AttachmentFbid: "x2", ActionUrl: "https://example.org/b",
				TitleText: "Other", SubtitleText: "What it is about", SourceText: "Example"},
		},
	}
	if err := s.handle(ctx, item{tbl: tbl}); err != nil {
		t.Fatal(err)
	}
	q := in.q()
	if v := db.Str(q, "SELECT text FROM message WHERE key = 'mid.l1'"); v != "read this" {
		t.Errorf("the text: %q", v)
	}
	if v := db.Str(q, "SELECT text FROM message WHERE key = 'mid.l2'"); v != "https://example.org/b" {
		t.Errorf("a link alone: %q", v)
	}
	var url, site, title, desc string
	q.QueryRow("SELECT url, coalesce(site, ''), title, coalesce(description, '') FROM link_preview p JOIN message m "+
		"ON m.id = p.message_id WHERE m.key = 'mid.l1'").Scan(&url, &site, &title, &desc)
	if url != "https://example.com/a" || site != "example.com" || title != "A title" || desc != "" {
		t.Errorf("the preview: %q %q %q %q", url, site, title, desc)
	}
	q.QueryRow("SELECT url, coalesce(site, ''), title, coalesce(description, '') FROM link_preview p JOIN message m "+
		"ON m.id = p.message_id WHERE m.key = 'mid.l2'").Scan(&url, &site, &title, &desc)
	if url != "https://example.org/b" || site != "Example" || title != "Other" || desc != "What it is about" {
		t.Errorf("the other preview: %q %q %q %q", url, site, title, desc)
	}
	if n := s.fetchFiles(ctx, ""); n != 1 {
		t.Errorf("taken: %d", n)
	}
	if n := db.Int(q, "SELECT count(*) FROM link_preview p JOIN link_preview_image i ON i.sha256 = p.image"); n != 1 {
		t.Errorf("the picture: %d", n)
	}
	if n := db.Int(q, "SELECT count(*) FROM attachment"); n != 0 {
		t.Errorf("a preview's picture as a file: %d", n)
	}
	if entries, _ := os.ReadDir(mediaDir(in.iid)); len(entries) != 0 {
		t.Errorf("left in the download folder: %d", len(entries))
	}
	if files, _ := pendingFiles(in.iid, ""); len(files) != 0 {
		t.Errorf("still to download: %d", len(files))
	}
	// the message again: its picture not asked for again
	if err := s.handle(ctx, item{tbl: tbl}); err != nil {
		t.Fatal(err)
	}
	if files, _ := pendingFiles(in.iid, ""); len(files) != 0 {
		t.Errorf("asked for again: %d", len(files))
	}
}

// An encrypted chat's link: its title, description and the picture it carries.
func TestLinkPreviewEncrypted(t *testing.T) {
	in, s := testSession(t, nil)
	ext := &waConsumerApplication.ConsumerApplication_ExtendedTextMessage{
		Text:         &waCommon.MessageText{Text: proto.String("see https://example.com/c")},
		MatchedText:  proto.String("https://example.com/c"),
		CanonicalURL: proto.String("https://example.com/c"),
		Title:        proto.String("C's title"),
		Description:  proto.String("About C"),
	}
	if err := ext.SetThumbnail(&waMediaTransport.ImageTransport{Integral: &waMediaTransport.ImageTransport_Integral{
		Transport: &waMediaTransport.WAMediaTransport{Ancillary: &waMediaTransport.WAMediaTransport_Ancillary{
			Thumbnail: &waMediaTransport.WAMediaTransport_Ancillary_Thumbnail{JPEGThumbnail: pngBytes()}}}}}); err != nil {
		t.Fatal(err)
	}
	chat := types.NewJID("200", types.MessengerServer)
	evt := &events.FBMessage{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "W9",
		Timestamp: time.UnixMilli(1600000000000)},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage{ExtendedTextMessage: ext}}}}},
		FBApplication: &waMsgApplication.MessageApplication{}}
	if err := s.handle(context.Background(), item{wa: evt}); err != nil {
		t.Fatal(err)
	}
	q := in.q()
	if v := db.Str(q, "SELECT text FROM message WHERE key = 'W9' AND subtype = 'link'"); v != "see https://example.com/c" {
		t.Errorf("the text: %q", v)
	}
	if n := db.Int(q, "SELECT count(*) FROM link_preview WHERE url = 'https://example.com/c' AND title = 'C''s title' "+
		"AND description = 'About C' AND image IS NOT NULL"); n != 1 {
		t.Errorf("the preview: %d", n)
	}
}

// Instagram's shared link: the preview, its picture queued; the text what was written.
func TestLinkPreviewInstagram(t *testing.T) {
	tr := newTranslator("100")
	o := &ops{}
	m := &slidetypes.Message{ID: "ig1", SenderFBID: john, TimestampMS: jsontime.UnixMilliString{Time: time.UnixMilli(1600000000000)},
		Content: slidetypes.MessageContentWrapper{Content: &slidetypes.MessageContentXMA{XMATextBody: "",
			XMA: &slidetypes.XMAContent{TargetURL: "https://example.net/d", XMATitle: "IG title",
				SubtitleText: "a description", HeaderTitleText: "site.example",
				PreviewImage: &slidetypes.XMAPreviewImage{URL: "https://cdn.example/p.jpg"}}}}}
	msg, ok := tr.igMessage(o, "t1", m)
	if !ok || msg.Text != "https://example.net/d" || msg.Subtype != "link" {
		t.Fatalf("the message: %+v", msg)
	}
	if p := msg.Preview; p == nil || p.URL != "https://example.net/d" || p.Title != "IG title" ||
		p.Description != "a description" || p.Site != "site.example" {
		t.Errorf("the preview: %+v", msg.Preview)
	}
	if len(o.files) != 1 || o.files[0].N != previewN || o.files[0].URL != "https://cdn.example/p.jpg" ||
		o.files[0].Name != "https://example.net/d" {
		t.Errorf("the picture: %+v", o.files)
	}
}
