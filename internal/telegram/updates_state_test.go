package telegram

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"

	"everysaid/internal/db"
)

// A kept supergroup's sequence survives a restart and the common state set again after each
// difference; a channel not kept (broadcast) stays in memory only.
func TestChannelStateKept(t *testing.T) {
	freshCache(t)
	w, err := openStore(DBPath())
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(w, "INSERT INTO chat (id, kind, title, archived, json) VALUES (?, 'supergroup', 'G', 0, '{}')", -channelMark-40)
	w.Close()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	s := newFileState(path)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetState(ctx, 1, updates.State{Pts: 5}))
	must(s.SetChannelPts(ctx, 1, 40, 70))
	must(s.SetChannelPts(ctx, 1, 20, 9))            // a broadcast channel, not kept
	must(s.SetState(ctx, 1, updates.State{Pts: 6})) // after a difference
	again := newFileState(path)
	got := map[int64]int{}
	must(again.ForEachChannels(ctx, 1, func(_ context.Context, id int64, pts int) error { got[id] = pts; return nil }))
	if len(got) != 1 || got[40] != 70 {
		t.Fatalf("%v", got)
	}
	if again.SetState(ctx, 2, updates.State{}) != nil || again.ForEachChannels(ctx, 2, func(context.Context, int64, int) error {
		t.Fatal("another account's channels")
		return nil
	}) != nil {
		t.Fatal()
	}
}

// Reactions given without the owner's choice (min) keep the owner's as stored.
func TestMinReactionsKeepOwn(t *testing.T) {
	in := newInstance(t, nil)
	c := in.ctx()
	bob := user(2, "Bob", 22)
	m := text(9, &tg.PeerUser{UserID: 2}, 0, 1600000000, "hi", false)
	m.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}})
	m.Reactions.Results[0].SetChosenOrder(0)
	if _, err := storeMessages(c, bob, []sent{{m, bob}}); err != nil {
		t.Fatal(err)
	}
	u := &tg.UpdateMessageReactions{Peer: &tg.PeerUser{UserID: 2}, MsgID: 9, Reactions: tg.MessageReactions{Min: true,
		Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 2}}}}
	if err := noteReactions(c, u); err != nil {
		t.Fatal(err)
	}
	d, _ := db.ReadOnly(DBPath())
	defer d.Close()
	var order, count int64 = -1, 0
	db.Row(d, "SELECT coalesce(json_extract(json, '$.reactions.results[0].chosen_order'), -1), "+
		"json_extract(json, '$.reactions.results[0].count') FROM message WHERE id = 9", nil, &order, &count)
	if order != 0 || count != 2 {
		t.Fatalf("chosen %d, count %d", order, count)
	}
}
