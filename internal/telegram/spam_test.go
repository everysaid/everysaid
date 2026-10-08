package telegram

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/gotd/td/tg"

	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

// blockedIDs are the Telegram users the archive has blocked on Telegram.
func blockedIDs(t *testing.T, in *instance) []string {
	t.Helper()
	return db.Strs(in.h.store.Read(), "SELECT a.value FROM blocked b JOIN address a ON a.id = b.address_id "+
		"WHERE b.phone = 'telegram' ORDER BY a.value")
}

// Someone removed as spam: reported, blocked and their whole chat deleted on Telegram, also when the
// user already deleted the chat there (its access hash then from telegram.db); and their chat
// dropped from telegram.db.
func TestReportSpamAndForget(t *testing.T) {
	in := newInstance(t, M{})
	c := in.ctx()
	f := account()
	cn := f.conn()
	if _, err := cn.dialogs(newTestCtx()); err != nil {
		t.Fatal(err)
	}
	bob, bobChat := f.users[1], f.chats[0]
	if _, err := storeMessages(c, bob, []sent{{bobChat.message(249), bob}, {bobChat.message(250), nil}}); err != nil {
		t.Fatal(err)
	}
	f.chats = f.chats[1:] // the user deleted the chat on the phone: no dialog any more
	cn = f.conn()
	_, release, _ := one.take(newTestCtx(), false, nil)
	defer release()
	defer one.share(cn)()

	var p Plugin
	conv := plugins.Conversation{Key: "2", Service: "telegram"}
	if err := p.ReportSpam(newTestCtx(), c, conv); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls, "reportSpam") || !slices.Contains(f.calls, "block") || !slices.Contains(f.calls, "deleteHistory") {
		t.Fatalf("calls: %v", f.calls)
	}
	if !reflect.DeepEqual(f.blocked, []int64{2}) || !reflect.DeepEqual(blockedIDs(t, in), []string{"2"}) {
		t.Fatalf("blocked on Telegram %v, in the archive %v", f.blocked, blockedIDs(t, in))
	}
	if err := p.ReportSpam(newTestCtx(), c, plugins.Conversation{Key: "-10", Service: "telegram"}); err == nil {
		t.Fatal("a group reported as one person")
	}

	if err := p.Forget(c, conv); err != nil {
		t.Fatal(err)
	}
	store, _ := openStore(DBPath())
	defer store.Close()
	if n := db.Int(store, "SELECT count(*) FROM message WHERE chat_id = 2") + db.Int(store, "SELECT count(*) FROM chat WHERE id = 2"); n != 0 {
		t.Fatalf("%d rows of the chat left in telegram.db", n)
	}
}

// The people blocked on Telegram reach the archive as Telegram lists them (pages of 100), and as
// each is blocked or unblocked on any device.
func TestBlockedPeople(t *testing.T) {
	in := newInstance(t, M{})
	c := in.ctx()
	f := account()
	for i := range 150 {
		f.blocked = append(f.blocked, int64(5000+i))
	}
	if err := noteBlocked(newTestCtx(), c, f.conn()); err != nil {
		t.Fatal(err)
	}
	if got := blockedIDs(t, in); len(got) != 150 {
		t.Fatalf("%d blocked in the archive", len(got))
	}
	f.blocked = []int64{5000}
	if err := noteBlocked(newTestCtx(), c, f.conn()); err != nil {
		t.Fatal(err)
	}
	if err := peerBlocked(c, &tg.UpdatePeerBlocked{PeerID: &tg.PeerUser{UserID: 7}, Blocked: true}); err != nil {
		t.Fatal(err)
	}
	if err := peerBlocked(c, &tg.UpdatePeerBlocked{PeerID: &tg.PeerUser{UserID: 5000}}); err != nil {
		t.Fatal(err)
	}
	if got := blockedIDs(t, in); fmt.Sprint(got) != "[7]" {
		t.Fatalf("blocked: %v", got)
	}
}
