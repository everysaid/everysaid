package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestPeerIDs(t *testing.T) {
	if PeerID(&tg.PeerUser{UserID: 5}) != 5 || PeerID(&tg.PeerChat{ChatID: 5}) != -5 ||
		PeerID(&tg.PeerChannel{ChannelID: 5}) != -1000000000005 {
		t.Fatal("marks")
	}
	for _, id := range []int64{5, -5, -1000000000005} {
		if PeerID(peerOf(id)) != id {
			t.Errorf("peerOf(%d)", id)
		}
	}
	if EntityID(&tg.Channel{ID: 9}) != -1000000000009 || EntityID(&tg.ChatForbidden{ID: 3}) != -3 {
		t.Fatal("entity ids")
	}
}

func TestKindsAndNames(t *testing.T) {
	self := &tg.User{ID: 1, FirstName: "A"}
	self.SetSelf(true)
	bot := &tg.User{ID: 2, LastName: "B"}
	bot.SetBot(true)
	broadcast := &tg.Channel{ID: 3, Title: "C"}
	broadcast.SetBroadcast(true)
	forbidden := &tg.ChannelForbidden{ID: 4, Title: "D"}
	for e, want := range map[any][3]string{
		self: {"saved", "saved", "A"},
		bot:  {"bot", "bot", "B"},
		&tg.User{ID: 5, FirstName: "E", LastName: "F"}: {"user", "user", "E F"},
		broadcast:                      {"channel", "channel", "C"},
		&tg.Channel{ID: 6, Title: "G"}: {"supergroup", "supergroup", "G"},
		forbidden:                      {"supergroup", "supergroup", "D"},
		&tg.Chat{ID: 7, Title: "H"}:    {"group", "group", "H"},
	} {
		if got := [3]string{EntityKind(e), EntityKind(e), DisplayName(e)}; got != want {
			t.Errorf("%T: %v, want %v", e, got, want)
		}
	}
	min := &tg.User{ID: 8}
	min.SetMin(true)
	min.SetAccessHash(1)
	if _, ok := InputPeer(min); ok {
		t.Error("a min user cannot be addressed")
	}
	u := &tg.User{ID: 9}
	u.SetAccessHash(3)
	if p, ok := InputPeer(u); !ok || p.(*tg.InputPeerUser).AccessHash != 3 {
		t.Error("input user")
	}
}
