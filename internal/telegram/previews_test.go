package telegram

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/gotd/td/tg"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// link is a message with a link's preview: its photo (photoID, sizes m and x), none (0), or one
// Telegram has not made yet (-1).
func link(id int, peer tg.PeerClass, photoID int64) *tg.Message {
	m := text(id, peer, 2, 1600000500+id, "see https://example.com", false)
	var page tg.WebPageClass
	if photoID < 0 {
		page = &tg.WebPagePending{ID: int64(id), Date: 1600000500 + id}
	} else {
		w := &tg.WebPage{ID: int64(id), URL: "https://example.com", DisplayURL: "example.com"}
		w.SetTitle("Example")
		if photoID > 0 {
			w.SetPhoto(&tg.Photo{ID: photoID, AccessHash: 1, FileReference: []byte("r"), DCID: 2,
				Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "m", W: 320, H: 200, Size: 10}, &tg.PhotoSize{Type: "x", W: 800, H: 500, Size: 20}}})
		}
		page = w
	}
	m.SetMedia(&tg.MessageMediaWebPage{Webpage: page})
	return m
}

// The size of a preview's picture: the largest of at most 800 px, else the smallest; never an
// inline thumbnail alone.
func TestPreviewSize(t *testing.T) {
	for _, c := range []struct {
		sizes []tg.PhotoSizeClass
		want  string
	}{
		{[]tg.PhotoSizeClass{&tg.PhotoSize{Type: "s", W: 100, H: 80}, &tg.PhotoSize{Type: "x", W: 800, H: 600},
			&tg.PhotoSizeProgressive{Type: "y", W: 1280, H: 960}, &tg.PhotoSize{Type: "m", W: 320, H: 240}}, "x"},
		{[]tg.PhotoSizeClass{&tg.PhotoSizeProgressive{Type: "w", W: 2560, H: 1920}, &tg.PhotoSize{Type: "y", W: 1280, H: 960}}, "y"},
		{[]tg.PhotoSizeClass{&tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1, 2, 3}}}, ""},
	} {
		if got, _ := previewSize(&tg.Photo{Sizes: c.sizes}); got != c.want {
			t.Fatalf("%q, not %q", got, c.want)
		}
	}
}

// A link preview's picture is never the message's file: FetchMedia gives none for it, and asks
// Telegram for no file.
func TestLinkPreviewNotTheMessagesFile(t *testing.T) {
	in := newInstance(t, M{})
	c := in.ctx()
	a, _ := archive.Open(in.path)
	bobID := a.Address(archive.H("id", "2", "telegram"))
	conv := a.Conversation("telegram", []archive.Handle{archive.H("id", "2", "telegram")}, "2", "Bob")
	src := a.Source(Source, DBPath(), "telegram", MediaPath())
	a.AddMessage(src, "2/300", archive.Message{Service: "telegram", ConversationID: conv, TS: 1000, SenderID: bobID, Kind: "text",
		Key: "300", Extras: &archive.Extras{Subtype: "link"}})
	a.Commit()
	mid := a.Int("SELECT id FROM message WHERE key = '300'")
	a.Close()
	f := account()
	f.chats[0].messages = append(f.chats[0].messages, link(300, &tg.PeerUser{UserID: 2}, 88))
	f.photos = map[string][]byte{"88/x": []byte("picture")}
	cn := f.conn()
	if err := syncRun(newTestCtx(), cn, en(&bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if p, d := fileOf(link(1, &tg.PeerUser{UserID: 2}, 88)); p != nil || d != nil {
		t.Fatal("a preview's photo as the message's file")
	}
	_, release, _ := one.take(newTestCtx(), false, nil)
	defer release()
	defer one.share(cn)()
	liveConn.Store(cn)
	defer liveConn.Store(nil)
	var p Plugin
	if got, err := p.FetchMedia(newTestCtx(), c, mid); got != "" || err != nil {
		t.Fatal(got, err)
	}
	if count(f.calls, "getFile") != 0 {
		t.Fatal("downloaded", f.calls)
	}
	d, _ := readStore()
	defer d.Close()
	if n := db.Int(d, "SELECT count(*) FROM telegram_message WHERE file IS NOT NULL"); n != 0 {
		t.Fatalf("%d recorded as files", n)
	}
}

// The live connection downloads the pictures of the stored messages' previews (once), marks those
// with none (a preview never made too), and has the messages that got theirs imported again. A
// link the archive has that the store no longer keeps is fetched again, then gets its picture.
func TestPreviewPictures(t *testing.T) {
	old := previewPause
	previewPause = 0
	defer func() { previewPause = old }()
	in := newInstance(t, M{})
	c := in.ctx()
	peer := &tg.PeerUser{UserID: 2}
	f := account()
	f.chats[0].messages = append(f.chats[0].messages, link(300, peer, 88), link(301, peer, 0), link(302, peer, -1))
	f.photos = map[string][]byte{"88/x": []byte("picture"), "89/x": []byte("old picture")}
	cn := f.conn()
	if err := syncRun(newTestCtx(), cn, en(&bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if _, err := cn.dialogs(newTestCtx()); err != nil {
		t.Fatal(err)
	}
	// one the archive has, before there were previews, that the store let go
	a, _ := archive.Open(in.path)
	conv := a.Conversation("telegram", []archive.Handle{archive.H("id", "2", "telegram")}, "2", "Bob")
	src := a.Source(Source, DBPath(), "telegram", MediaPath())
	a.AddMessage(src, "2/310", archive.Message{Service: "telegram", ConversationID: conv, TS: 1000, Kind: "text",
		Key: "310", Extras: &archive.Extras{Subtype: "link"}})
	a.Commit()
	a.Close()
	f.chats[0].messages = append(f.chats[0].messages, link(310, peer, 89))

	in.keys = nil
	left, err := previewRound(newTestCtx(), c, cn, true)
	if err != nil || left {
		t.Fatal(left, err)
	}
	d, _ := readStore()
	defer d.Close()
	got := map[int64]string{}
	db.Each(d, "SELECT id, photo_id, coalesce(data, '') FROM telegram_webpage_photo", nil, func(scan func(...any)) {
		var id, photo int64
		var data string
		scan(&id, &photo, &data)
		got[id] = fmt.Sprint(photo, " ", data)
	})
	want := map[int64]string{300: "88 picture", 301: "0 ", 302: "0 ", 310: "89 old picture"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if !db.Exists(d, "SELECT 1 FROM telegram_message WHERE chat_id = 2 AND id = 310") {
		t.Fatal("the old one not fetched again")
	}
	// refetch imported the old one (its preview's words), then both pictures were imported
	if last := in.keys[len(in.keys)-1]; !reflect.DeepEqual(last, map[[2]int64]bool{{2, 300}: true, {2, 310}: true}) {
		t.Fatalf("%v", in.keys)
	}
	// again: nothing downloaded, nothing imported
	files, imports := count(f.calls, "getFile"), in.imports
	if _, err := previewRound(newTestCtx(), c, cn, true); err != nil || count(f.calls, "getFile") != files || in.imports != imports {
		t.Fatal("again", err, f.calls)
	}
}
