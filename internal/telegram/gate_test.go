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
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/importers"
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
	_, release, err := one.take(newTestCtx(), false, nil)
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
		_, done, err := one.take(ctx, false, nil)
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
	_, user, _ := one.take(newTestCtx(), true, nil)
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
	_, release, _ := one.take(newTestCtx(), false, nil)
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

// Another process with the session open (the server, or telegram-sync run by hand): the sync and a
// send say so plainly, the live connection waits for it. A second open file stands for the other
// process: the system's locks are per open file.
func TestSessionLockAcrossProcesses(t *testing.T) {
	freshCache(t)
	signedIn(t)
	other, err := lockSession()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = Sync(newTestCtx(), SyncOptions{Lang: "en"}, &out)
	if err == nil || err.Error() != heldElsewhere {
		t.Fatal(err)
	}
	err = withConn(newTestCtx(), func(context.Context, *conn) error { return nil })
	if err == nil || err.Error() != heldElsewhere {
		t.Fatal(err)
	}
	waited := 0
	got := make(chan error, 1)
	go func() {
		_, done, err := one.take(newTestCtx(), false, func() { waited++ })
		if err == nil {
			done()
		}
		got <- err
	}()
	select {
	case <-got:
		t.Fatal("taken while another process holds it")
	case <-time.After(200 * time.Millisecond):
	}
	unlockSession(other)
	select {
	case err := <-got:
		if err != nil || waited != 1 {
			t.Fatal(waited, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("still waiting after the other let go")
	}
	// and the other way: while this process has it, another cannot take it
	_, done, err := one.take(newTestCtx(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockSession(); !errors.Is(err, errHeld) {
		t.Fatal(err)
	}
	done()
	f, err := lockSession()
	if err != nil {
		t.Fatal(err)
	}
	unlockSession(f)
}

// gotd's updates manager on a fake API: what it asks Telegram for on starting.
type stateAPI struct {
	diffs  chan int // the pts of each getDifference
	states int
}

func (a *stateAPI) UpdatesGetState(context.Context) (*tg.UpdatesState, error) {
	a.states++
	return &tg.UpdatesState{Pts: 500, Qts: 1, Date: 1700000000, Seq: 1}, nil
}

func (a *stateAPI) UpdatesGetDifference(_ context.Context, r *tg.UpdatesGetDifferenceRequest) (tg.UpdatesDifferenceClass, error) {
	a.diffs <- r.Pts
	return &tg.UpdatesDifferenceEmpty{Date: r.Date, Seq: 1}, nil
}

func (a *stateAPI) UpdatesGetChannelDifference(context.Context, *tg.UpdatesGetChannelDifferenceRequest) (tg.UpdatesChannelDifferenceClass, error) {
	return nil, errors.New("no channels here")
}

// The updates' state outlives a restart: the next start asks for the difference since where the last
// one stood, not for the state of now.
func TestUpdatesStateKept(t *testing.T) {
	freshCache(t)
	start := func() *stateAPI {
		api := &stateAPI{diffs: make(chan int, 4)}
		mgr := updates.New(updates.Config{Handler: tg.NewUpdateDispatcher(), Storage: newFileState(StatePath())})
		ctx, cancel := context.WithCancel(newTestCtx())
		ended := make(chan struct{})
		go func() { mgr.Run(ctx, api, 1, updates.AuthOptions{}); close(ended) }()
		<-api.diffs // the start's
		cancel()
		<-ended
		return api
	}
	if api := start(); api.states != 1 {
		t.Fatalf("first start: %d getState", api.states)
	}
	s := newFileState(StatePath())
	if err := s.SetPts(newTestCtx(), 1, 612); err != nil { // as updates move it on
		t.Fatal(err)
	}
	api := &stateAPI{diffs: make(chan int, 4)}
	mgr := updates.New(updates.Config{Handler: tg.NewUpdateDispatcher(), Storage: newFileState(StatePath())})
	ctx, cancel := context.WithCancel(newTestCtx())
	defer cancel()
	go mgr.Run(ctx, api, 1, updates.AuthOptions{})
	if pts := <-api.diffs; pts != 612 || api.states != 0 {
		t.Fatalf("restart: difference from %d, %d getState", pts, api.states)
	}
	// another account's state is not this one's
	if _, ok, _ := newFileState(StatePath()).GetState(newTestCtx(), 2); ok {
		t.Fatal("another account")
	}
	if err := newFileState(StatePath()).SetPts(newTestCtx(), 2, 1); err == nil {
		t.Fatal("no state to move on")
	}
}

// A message's file on demand, through the live connection only, into the sync's media place.
func TestFetchMedia(t *testing.T) {
	in := newInstance(t, M{})
	c := in.ctx()
	a, _ := archive.Open(in.path)
	bobID := a.Address(archive.H("id", "2", "telegram"))
	conv := a.Conversation("telegram", []archive.Handle{archive.H("id", "2", "telegram")}, "2", "Bob")
	src := a.Source(Source, DBPath(), "telegram", MediaPath())
	a.AddMessage(src, "2/300", archive.Message{Service: "telegram", ConversationID: conv, TS: 1000, SenderID: bobID, Kind: "video", Key: "300"})
	a.Commit()
	var mid int64
	db.Row(a.DB, "SELECT id FROM message WHERE key = '300'", nil, &mid)
	a.Close()
	f := account()
	f.chats[0].messages = append(f.chats[0].messages, video(300, &tg.PeerUser{UserID: 2}, 77, "video/mp4"))
	f.files[77] = []byte("video bytes")
	cn := f.conn()
	if err := syncRun(newTestCtx(), cn, en(&bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	var p Plugin
	if got, err := p.FetchMedia(newTestCtx(), c, mid); got != "" || err != nil {
		t.Fatal("not live:", got, err)
	}
	_, release, _ := one.take(newTestCtx(), false, nil)
	defer release()
	defer one.share(cn)()
	if got, _ := p.FetchMedia(newTestCtx(), c, mid); got != "" {
		t.Fatal("a sync's connection is not the live one")
	}
	liveConn.Store(cn)
	defer liveConn.Store(nil)
	got, err := p.FetchMedia(newTestCtx(), c, mid)
	want := filepath.Join(MediaPath(), "2", "300.mp4")
	if err != nil || got != want {
		t.Fatal(got, err)
	}
	if b, _ := os.ReadFile(got); string(b) != "video bytes" {
		t.Fatalf("%q", b)
	}
	d, _ := db.ReadOnly(DBPath())
	defer d.Close()
	if files := db.Strs(d, "SELECT file FROM message WHERE file IS NOT NULL"); !reflect.DeepEqual(files, []string{"2/300.mp4"}) {
		t.Fatalf("%v", files)
	}
	n := len(f.calls)
	if again, _ := p.FetchMedia(newTestCtx(), c, mid); again != want || len(f.calls) != n {
		t.Fatal("downloaded twice")
	}
}

// A message deleted on Telegram: the live update reaches telegram.db, the import marks the
// archive's message deleted and keeps its text. Private chats' ids are the account's, found in the
// stored rows; a supergroup's come with its channel.
func TestDeletedEndToEnd(t *testing.T) {
	in := newInstance(t, M{})
	importTelegram = func(a *archive.Archive, out func(string), only map[[2]int64]bool, skip map[int64]bool) error {
		return importers.Telegram(a, out, importers.TelegramOptions{Only: only, Skip: skip})
	}
	c := in.ctx()
	bob := user(2, "Bob", 22)
	group := &tg.Channel{ID: 30, Title: "Club", Photo: &tg.ChatPhotoEmpty{}}
	group.SetMegagroup(true)
	group.SetAccessHash(55)
	club := &tg.PeerChannel{ChannelID: 30}
	if _, err := storeMessages(c, bob, []sent{{text(5, &tg.PeerUser{UserID: 2}, 0, 1600000000, "hello", false), bob},
		{text(6, &tg.PeerUser{UserID: 2}, 0, 1600000001, "stays", false), bob}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeMessages(c, group, []sent{{text(5, club, 2, 1600000002, "in the club", false), bob}}); err != nil {
		t.Fatal(err)
	}
	d := tg.NewUpdateDispatcher()
	handlers(c, account().conn(), d, func(int64) {})
	err := d.Handle(newTestCtx(), &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateDeleteMessages{Messages: []int{5, 99}},
		&tg.UpdateDeleteChannelMessages{ChannelID: 30, Messages: []int{5}}}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	db.Each(in.h.store.Read(), "SELECT m.text, m.deleted FROM message m", nil, func(scan func(...any)) {
		var txt string
		var del int64
		scan(&txt, &del)
		got[txt] = del
	})
	if !reflect.DeepEqual(got, map[string]int64{"hello": 1, "stays": 0, "in the club": 1}) {
		t.Fatalf("%v", got)
	}
	s, _ := db.ReadOnly(DBPath())
	defer s.Close()
	if n := db.Int(s, "SELECT count(*) FROM deleted"); n != 2 {
		t.Fatalf("%d recorded", n)
	}
	if lines := strings.Join(c.LastLines(50), "\n"); strings.Contains(lines, "error") {
		t.Fatal(lines)
	}
}
