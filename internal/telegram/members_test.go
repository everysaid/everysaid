package telegram

import (
	"bytes"
	"context"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"everysaid/internal/archive"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/importers"
)

// clubID is the supergroup's marked id in withMembers.
const clubID = -(channelMark + 30)

// withMembers is the invented account with members: the group (10) has the owner, user 2, the bot and
// user 4, who never wrote there; a supergroup (30) has the owner, user 2 and 450 others, more than
// two pages of them.
func withMembers() *fake {
	f := account()
	f.users = append(f.users, user(4, "Carol", 44))
	f.chats[1].members = []int64{1, 2, 3, 4}
	club := &tg.Channel{ID: 30, Title: "Club", Photo: &tg.ChatPhotoEmpty{}}
	club.SetMegagroup(true)
	club.SetAccessHash(55)
	members := []int64{1, 2}
	for i := int64(100); i < 550; i++ {
		f.users = append(f.users, user(i, "P", i))
		members = append(members, i)
	}
	f.chats = append(f.chats, &fakeChat{entity: club, peer: &tg.PeerChannel{ChannelID: 30}, members: members,
		messages: []tg.MessageClass{text(1, &tg.PeerChannel{ChannelID: 30}, 2, 1600000000, "c", false)}})
	return f
}

func storedMembers(t *testing.T, chat int64) (ids []int64, complete int64) {
	t.Helper()
	d, err := db.ReadOnly(DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	db.Row(d, "SELECT complete FROM chat_member_list WHERE chat_id = ?", []any{chat}, &complete)
	return db.Ints(d, "SELECT user_id FROM chat_member WHERE chat_id = ? ORDER BY user_id", chat), complete
}

func count(calls []string, name string) int {
	n := 0
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

// The sync asks for every group's members: a basic group's whole list at once, a supergroup's a
// page at a time. A complete list replaces the one kept (one who left goes); a partial or refused
// one leaves it and is said, and the sync goes on.
func TestSyncMembers(t *testing.T) {
	freshCache(t)
	f := withMembers()
	var out bytes.Buffer
	if err := syncRun(newTestCtx(), f.conn(), en(&out)); err != nil {
		t.Fatal(err)
	}
	if ids, complete := storedMembers(t, -10); !reflect.DeepEqual(ids, []int64{1, 2, 3, 4}) || complete != 1 {
		t.Fatalf("group %v %d", ids, complete)
	}
	if ids, complete := storedMembers(t, clubID); len(ids) != 452 || complete != 1 {
		t.Fatalf("supergroup %d %d", len(ids), complete)
	}
	if n := count(f.calls, "getParticipants"); n != 3 {
		t.Fatalf("%d pages", n)
	}
	if n := count(f.calls, "getFullChat"); n != 1 { // not for private chats, channels or bots
		t.Fatalf("%d getFullChat", n)
	}
	d, _ := db.ReadOnly(DBPath())
	var js string
	db.Row(d, "SELECT json FROM entity WHERE id = 4", nil, &js)
	d.Close()
	if !strings.Contains(js, `"first_name":"Carol"`) || !strings.Contains(js, `"access_hash":44`) {
		t.Fatalf("Carol's entity %s", js)
	}

	// user 4 left; the supergroup hides its members but 100; then the group is refused
	f.chats[1].members = []int64{1, 2, 3}
	f.chats[len(f.chats)-1].shown = 100
	out.Reset()
	if err := syncRun(newTestCtx(), f.conn(), en(&out)); err != nil {
		t.Fatal(err)
	}
	if ids, _ := storedMembers(t, -10); !reflect.DeepEqual(ids, []int64{1, 2, 3}) {
		t.Fatalf("left: %v", ids)
	}
	if ids, complete := storedMembers(t, clubID); len(ids) != 452 || complete != 0 {
		t.Fatalf("partial: %d %d", len(ids), complete)
	}
	if !strings.Contains(out.String(), "Club: Telegram gave 100 of its 452 members") {
		t.Fatalf("%q", out.String())
	}
	f.chats[1].refuse = "CHAT_ADMIN_REQUIRED"
	out.Reset()
	if err := syncRun(newTestCtx(), f.conn(), en(&out)); err != nil {
		t.Fatal(err)
	}
	if ids, _ := storedMembers(t, -10); !reflect.DeepEqual(ids, []int64{1, 2, 3}) {
		t.Fatalf("refused: %v", ids)
	}
	if !strings.Contains(out.String(), "Friends: Telegram did not give its members (rpc error code 400: CHAT_ADMIN_REQUIRED") {
		t.Fatalf("%q", out.String())
	}
}

// chatMembers are the names of a Telegram group's members as the server's chat detail gives them.
func chatMembers(t *testing.T, store *core.Store, chat int64) []string {
	t.Helper()
	conv := db.Int(store.Read(), "SELECT c.id FROM conversation c JOIN service s ON s.id = c.service_id "+
		"WHERE s.name = 'telegram' AND c.key = ?", strconv.FormatInt(chat, 10))
	detail := core.GetChat(store, core.ChatOfConversation(store, conv))
	if detail == nil {
		t.Fatalf("no chat for %d", chat)
	}
	var names []string
	for _, m := range detail["members"].([]core.M) {
		names = append(names, m["name"].(string))
	}
	sort.Strings(names)
	return names
}

// The whole way: the members the sync asked for reach the archive and the chat's members, the one
// who never wrote among them, the bot and the owner not; a group whose members Telegram refused
// keeps whoever wrote there.
func TestMembersInTheChat(t *testing.T) {
	in := newInstance(t, M{})
	f := withMembers()
	f.users = append(f.users, user(5, "Dan", 55))
	refused := &fakeChat{entity: &tg.Chat{ID: 11, Title: "Closed", Photo: &tg.ChatPhotoEmpty{}}, peer: &tg.PeerChat{ChatID: 11},
		members: []int64{1, 2, 5}, refuse: "CHAT_ADMIN_REQUIRED",
		messages: []tg.MessageClass{text(1, &tg.PeerChat{ChatID: 11}, 2, 1600000000, "x", false)}}
	f.chats = append(f.chats, refused)
	var out bytes.Buffer
	if err := syncRun(newTestCtx(), f.conn(), en(&out)); err != nil {
		t.Fatal(err)
	}
	a, err := archive.Open(in.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := importers.Telegram(a, nil, importers.TelegramOptions{}); err != nil {
		t.Fatal(err)
	}
	a.Close()
	if got := chatMembers(t, in.h.store, -10); !reflect.DeepEqual(got, []string{"Bob", "Carol"}) {
		t.Fatalf("group: %v", got)
	}
	if got := chatMembers(t, in.h.store, -11); !reflect.DeepEqual(got, []string{"Bob"}) {
		t.Fatalf("refused: %v", got)
	}
	if got := chatMembers(t, in.h.store, clubID); len(got) != 451 {
		t.Fatalf("supergroup: %d", len(got))
	}
}

// Live: Telegram's word that a group's members changed (its updates, its service messages) has
// them asked for again, and they reach the archive; on connecting, the groups not asked for a day.
func TestLiveMembers(t *testing.T) {
	in := newInstance(t, M{})
	importTelegram = func(a *archive.Archive, out func(string), only map[[2]int64]bool, skip map[int64]bool) error {
		return importers.Telegram(a, out, importers.TelegramOptions{Only: only, Skip: skip})
	}
	importMembers = func(a *archive.Archive, chats map[int64]bool) error {
		return importers.TelegramMembers(a, nil, "", chats)
	}
	c := in.ctx()
	f := withMembers()
	cn := f.conn()
	dialogs, err := cn.dialogs(newTestCtx())
	if err != nil {
		t.Fatal(err)
	}
	var told []int64
	d := tg.NewUpdateDispatcher()
	handlers(c, cn, d, func(chat int64) { told = append(told, chat) })
	joined := &tg.MessageService{ID: 6, PeerID: &tg.PeerChat{ChatID: 10}, Date: 1600000006,
		Action: &tg.MessageActionChatAddUser{Users: []int64{4}}}
	joined.SetFromID(&tg.PeerUser{UserID: 2})
	err = d.Handle(newTestCtx(), &tg.Updates{Users: f.users, Chats: f.chatsList(), Updates: []tg.UpdateClass{
		&tg.UpdateNewMessage{Message: joined},
		&tg.UpdateChatParticipantAdd{ChatID: 10, UserID: 4, InviterID: 2},
		&tg.UpdateChannelParticipant{ChannelID: 30, UserID: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(told, []int64{-10, -10, clubID}) {
		t.Fatalf("told %v", told)
	}
	if got := chatMembers(t, in.h.store, -10); !reflect.DeepEqual(got, []string{"Bob"}) { // only who wrote, so far
		t.Fatalf("before: %v", got)
	}

	if stale, err := staleMembers(dialogs); err != nil || !reflect.DeepEqual(stale, []int64{-10, clubID}) {
		t.Fatalf("stale %v %v", stale, err)
	}
	old := memberDelay
	memberDelay = time.Millisecond
	t.Cleanup(func() { memberDelay = old })
	q := newMemberQueue()
	ctx, stop := context.WithCancel(newTestCtx())
	q.add(-10)
	q.add(-10)
	go q.run(ctx, c, cn)
	deadline := time.Now().Add(10 * time.Second)
	for len(chatMembers(t, in.h.store, -10)) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if got := chatMembers(t, in.h.store, -10); !reflect.DeepEqual(got, []string{"Bob", "Carol"}) {
		t.Fatalf("after: %v", got)
	}
	if n := count(f.calls, "getFullChat"); n != 1 {
		t.Fatalf("asked %d times", n)
	}
	if stale, _ := staleMembers(dialogs); !reflect.DeepEqual(stale, []int64{clubID}) {
		t.Fatalf("still stale %v", stale)
	}
}
