package telegram

// What the audit of the Go port found: one client per process, the live connection kept up to date
// after a drop, flood waits, media that do not stop at one bad file, pictures Telegram will not take
// as photos.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

// signedIn puts credentials and a session in place of the keyring's.
func signedIn(t *testing.T) {
	fakeSecrets(t, map[string]string{SecretAPIID: "1", SecretAPIHash: "x", SecretSession: "{}"})
}

// While a connection is open (the live one), an import's sync and a send use it: never a second
// client with the same key in the process. The context is already cancelled, so that a client of
// their own could not connect anywhere: only the open connection can answer.
func TestOneClientPerProcess(t *testing.T) {
	freshCache(t)
	signedIn(t)
	f := account()
	_, release, err := one.take(newTestCtx(), false)
	if err != nil {
		t.Fatal(err)
	}
	stop := one.share(f.conn())
	ctx, cancel := context.WithCancel(newTestCtx())
	cancel()
	var out bytes.Buffer
	if err := Sync(ctx, SyncOptions{Lang: "en"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "256 new messages") {
		t.Fatalf("%q", out.String())
	}
	if err := withConn(ctx, func(ctx context.Context, cn *conn) error { return nil }); err != nil {
		t.Fatal(err)
	}

	// a login, or a second live connection, waits for the open client to end
	got := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(newTestCtx(), 5*time.Second)
		defer cancel()
		_, done, err := one.take(ctx, false)
		if err == nil {
			done()
		}
		got <- err
	}()
	select {
	case <-got:
		t.Fatal("a second client while one is open")
	case <-time.After(100 * time.Millisecond):
	}

	// the open one waits, when it ends, for those using it
	_, user, _ := one.take(newTestCtx(), true)
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("ended while in use")
	case <-time.After(100 * time.Millisecond):
	}
	user()
	<-stopped
	release()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}

// A long FLOOD_WAIT is the error, unless the call allows longer (the sync on the live connection).
func TestFloodThreshold(t *testing.T) {
	calls := 0
	next := tg.Invoker(invoker(func() error {
		calls++
		if calls == 1 {
			return &tgerr.Error{Code: 420, Type: "FLOOD_WAIT", Argument: 1}
		}
		return nil
	}))
	mw := floodWait(0).Handle(next)
	if err := mw.Invoke(newTestCtx(), nil, nil); !tgerr.Is(err, "FLOOD_WAIT") {
		t.Fatal(err)
	}
	calls = 0
	if err := mw.Invoke(withThreshold(newTestCtx(), 5*time.Second), nil, nil); err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
}

type invoker func() error

func (f invoker) Invoke(context.Context, bin.Encoder, bin.Decoder) error { return f() }

type recorder struct{ got []tg.UpdatesClass }

func (r *recorder) Handle(_ context.Context, u tg.UpdatesClass) error {
	r.got = append(r.got, u)
	return nil
}

// Back after a drop, the live connection asks for what came meanwhile; told the gap is too long, it
// reads the chats again.
func TestFollow(t *testing.T) {
	in := newInstance(t, M{})
	c := in.ctx()
	f := account()
	cn := f.conn()
	var mgr recorder
	done := make(chan error)
	reconnected, again := make(chan struct{}, 1), make(chan struct{}, 1)
	ended := make(chan error)
	go func() { ended <- follow(newTestCtx(), c, cn, &mgr, done, reconnected, again) }()
	reconnected <- struct{}{}
	again <- struct{}{}
	time.Sleep(200 * time.Millisecond)
	done <- errors.New("dropped")
	if err := <-ended; err == nil || err.Error() != "dropped" {
		t.Fatal(err)
	}
	if len(mgr.got) != 1 {
		t.Fatalf("%v", mgr.got)
	}
	if _, ok := mgr.got[0].(*tg.UpdatesTooLong); !ok {
		t.Fatalf("%T", mgr.got[0])
	}
	d, _ := db.ReadOnly(DBPath())
	defer d.Close()
	if n := db.Int(d, "SELECT count(*) FROM message"); n != 256 {
		t.Fatalf("%d messages", n)
	}
}

// One file Telegram will not give is skipped (and tried again later): the others still come.
// A sender's file name gives an extension only when it is a plain one.
func TestMediaOneFails(t *testing.T) {
	freshCache(t)
	f := account()
	peer := &tg.PeerUser{UserID: 2}
	f.chats[0].messages = append(f.chats[0].messages,
		video(300, peer, 77, "video/mp4"), video(301, peer, 78, "video/mp4"),
		video(302, peer, 79, "video/x-odd", &tg.DocumentAttributeFilename{FileName: "clip.a:b"}))
	f.files[78] = []byte("second")
	f.files[79] = []byte("third")
	var out bytes.Buffer
	if err := syncRun(newTestCtx(), f.conn(), en(&out)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := mediaRun(newTestCtx(), f.conn(), false, nil, en(&out)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2/300.mp4: not downloaded") || !strings.Contains(out.String(), "2 downloaded") {
		t.Fatalf("%q", out.String())
	}
	d, _ := db.ReadOnly(DBPath())
	defer d.Close()
	if files := db.Strs(d, "SELECT file FROM message WHERE file IS NOT NULL ORDER BY id"); !reflect.DeepEqual(files, []string{"2/301.mp4", "2/302"}) {
		t.Fatalf("%v", files)
	}
	if _, err := os.Stat(filepath.Join(MediaPath(), "2", "300.mp4.part")); !os.IsNotExist(err) {
		t.Fatal("a part left")
	}
}

// A picture Telegram refuses as a photo, or one too large for it, goes as a file.
func TestSendPictureAsFile(t *testing.T) {
	in := newInstance(t, M{})
	c := in.ctx()
	f := account()
	f.noPhotos = true
	cn := f.conn()
	_, release, _ := one.take(newTestCtx(), false)
	defer release()
	defer one.share(cn)()
	var p Plugin
	conv := plugins.Conversation{ID: 1, Key: "2", Service: "telegram"}
	if _, err := p.Send(newTestCtx(), c, conv, "look", nil, nil,
		&plugins.File{Filename: "wide.jpg", MimeType: "image/jpeg", Data: []byte("jpeg")}); err != nil {
		t.Fatal(err)
	}
	f.noPhotos = false
	if _, err := p.Send(newTestCtx(), c, conv, "", nil, nil,
		&plugins.File{Filename: "big.png", MimeType: "image/png", Data: make([]byte, maxPhoto+1)}); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, r := range f.requests {
		switch r.(*tg.MessagesSendMediaRequest).Media.(type) {
		case *tg.InputMediaUploadedPhoto:
			kinds = append(kinds, "photo")
		case *tg.InputMediaUploadedDocument:
			kinds = append(kinds, "file")
		}
	}
	if !reflect.DeepEqual(kinds, []string{"photo", "file", "file"}) {
		t.Fatalf("%v", kinds)
	}
	if r := f.requests[1].(*tg.MessagesSendMediaRequest); r.Message != "look" {
		t.Fatalf("%q", r.Message)
	}
}

// The sync holds no write lock on the store while it asks Telegram (a flood wait can last minutes):
// the live connection writes to it meanwhile.
func TestSyncLeavesStoreFree(t *testing.T) {
	freshCache(t)
	f := account()
	d, err := openStore(DBPath())
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	other, err := db.Open(DBPath(), "busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var locked []string
	f.asked = func(input bin.Encoder) {
		if _, history := input.(*tg.MessagesGetHistoryRequest); !history {
			return
		}
		if _, err := other.Exec("UPDATE chat SET synced_at = synced_at WHERE id = 0"); err != nil {
			locked = append(locked, err.Error())
		}
	}
	var out bytes.Buffer
	if err := syncRun(newTestCtx(), f.conn(), en(&out)); err != nil {
		t.Fatal(err)
	}
	if len(locked) > 0 {
		t.Fatalf("%d writes refused: %s", len(locked), locked[0])
	}
}
