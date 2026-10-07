package telegram

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gotd/td/tg"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
)

// The user's reaction put, changed and taken back, their message edited, then deleted for
// everyone: each reaches Telegram, then telegram.db, then the archive through the importer, as the
// same change made on a phone would.
func TestActionsEndToEnd(t *testing.T) {
	in := newInstance(t, M{})
	importTelegram = func(a *archive.Archive, out func(string), only map[[2]int64]bool, skip map[int64]bool) error {
		return importers.Telegram(a, out, importers.TelegramOptions{Only: only, Skip: skip})
	}
	c := in.ctx()
	f := account()
	club := &tg.Channel{ID: 30, Title: "Club", Photo: &tg.ChatPhotoEmpty{}}
	club.SetMegagroup(true)
	club.SetAccessHash(55)
	clubPeer := &tg.PeerChannel{ChannelID: 30}
	f.chats = append(f.chats, &fakeChat{entity: club, peer: clubPeer,
		messages: []tg.MessageClass{text(7, clubPeer, 1, 1600000300, "to the club", true)}})
	cn := f.conn()
	if _, err := cn.dialogs(newTestCtx()); err != nil {
		t.Fatal(err)
	}
	bob, bobChat := f.users[1], f.chats[0]
	if _, err := storeMessages(c, bob, []sent{{bobChat.message(249), bob}, {bobChat.message(250), nil}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeMessages(c, club, []sent{{f.chats[len(f.chats)-1].messages[0], f.self}}); err != nil {
		t.Fatal(err)
	}
	_, release, _ := one.take(newTestCtx(), false, nil)
	defer release()
	defer one.share(cn)()

	q := in.h.store.Read()
	ref := func(key, conv string) (plugins.Conversation, plugins.Ref) {
		var id, cid int64
		var out bool
		if !db.Row(q, "SELECT m.id, m.conversation_id, m.outgoing FROM message m JOIN conversation c ON c.id = m.conversation_id "+
			"WHERE m.key = ? AND c.key = ?", []any{key, conv}, &id, &cid, &out) {
			t.Fatalf("no message %s/%s", conv, key)
		}
		return plugins.Conversation{ID: cid, Key: conv, Service: "telegram"}, plugins.Ref{ID: id, Key: key, Outgoing: out}
	}
	reactions := func(id int64) []string {
		var out []string
		db.Each(q, "SELECT emoji, outgoing FROM reaction WHERE message_id = ? ORDER BY rowid", []any{id}, func(scan func(...any)) {
			var e string
			var o bool
			scan(&e, &o)
			if o {
				e += " (mine)"
			}
			out = append(out, e)
		})
		return out
	}
	var p Plugin
	ctx := newTestCtx()

	conv, theirs := ref("249", "2")
	if err := p.React(ctx, c, conv, theirs, "👍"); err != nil {
		t.Fatal(err)
	}
	if got := reactions(theirs.ID); !reflect.DeepEqual(got, []string{"👍 (mine)"}) {
		t.Fatalf("%v", got)
	}
	// written as other services write it, sent as Telegram knows it; in place of the one there was
	if err := p.React(ctx, c, conv, theirs, "❤\ufe0f"); err != nil {
		t.Fatal(err)
	}
	if e := f.requests[len(f.requests)-1].(*tg.MessagesSendReactionRequest).Reaction[0].(*tg.ReactionEmoji); e.Emoticon != "❤" {
		t.Fatalf("%q", e.Emoticon)
	}
	if got := reactions(theirs.ID); !reflect.DeepEqual(got, []string{"❤ (mine)"}) {
		t.Fatalf("%v", got)
	}
	if err := p.React(ctx, c, conv, theirs, ""); err != nil {
		t.Fatal(err)
	}
	if got := reactions(theirs.ID); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if r := f.requests[len(f.requests)-1].(*tg.MessagesSendReactionRequest); len(r.Reaction) != 0 {
		t.Fatalf("%v", r.Reaction)
	}

	// a reaction the chat does not allow: the user is told, nothing changes
	f.refused = "🤡"
	var ue *errs.UserError
	if err := p.React(ctx, c, conv, theirs, "🤡"); !errors.As(err, &ue) || ue.Text != reactionRefused {
		t.Fatal(err)
	}

	_, mine := ref("250", "2")
	if err := p.Edit(ctx, c, conv, mine, "m250, better"); err != nil {
		t.Fatal(err)
	}
	if err := p.Edit(ctx, c, conv, mine, "m250, better"); err != nil { // the same text again: nothing to do
		t.Fatal(err)
	}
	var txt string
	var edited, deleted bool
	db.Row(q, "SELECT text, edited, deleted FROM message WHERE id = ?", []any{mine.ID}, &txt, &edited, &deleted)
	if txt != "m250, better" || !edited || deleted {
		t.Fatal(txt, edited, deleted)
	}

	if err := p.Delete(ctx, c, conv, mine); err != nil {
		t.Fatal(err)
	}
	if r := f.requests[len(f.requests)-1].(*tg.MessagesDeleteMessagesRequest); !r.Revoke || !reflect.DeepEqual(r.ID, []int{250}) {
		t.Fatalf("%+v", r)
	}
	db.Row(q, "SELECT text, deleted FROM message WHERE id = ?", []any{mine.ID}, &txt, &deleted)
	if txt != "m250, better" || !deleted { // kept, said deleted
		t.Fatal(txt, deleted)
	}
	if bobChat.message(250) != nil {
		t.Fatal("still on Telegram")
	}

	// a supergroup's message: deleted through the channel
	conv, ours := ref("7", "-1000000000030")
	if err := p.Delete(ctx, c, conv, ours); err != nil {
		t.Fatal(err)
	}
	if r, ok := f.requests[len(f.requests)-1].(*tg.ChannelsDeleteMessagesRequest); !ok || r.Channel.(*tg.InputChannel).AccessHash != 55 {
		t.Fatalf("%T", f.requests[len(f.requests)-1])
	}
	db.Row(q, "SELECT deleted FROM message WHERE id = ?", []any{ours.ID}, &deleted)
	if !deleted {
		t.Fatal("not marked deleted")
	}

	// one gone from Telegram meanwhile
	if err := p.Edit(ctx, c, conv, plugins.Ref{Key: "999", Outgoing: true}, "x"); !errors.As(err, &ue) || ue.Text != messageGone {
		t.Fatal(err)
	}
}

// Without a sign-in, nothing is tried.
func TestActionsNeedSignIn(t *testing.T) {
	in := newInstance(t, M{})
	fakeSecrets(t, map[string]string{})
	var p Plugin
	conv, msg := plugins.Conversation{ID: 1, Key: "2"}, plugins.Ref{ID: 1, Key: "5", Outgoing: true}
	ctx, cancel := context.WithCancel(newTestCtx())
	defer cancel()
	for _, err := range []error{p.React(ctx, in.ctx(), conv, msg, "👍"), p.Edit(ctx, in.ctx(), conv, msg, "x"),
		p.Delete(ctx, in.ctx(), conv, msg)} {
		var ue *errs.UserError
		if !errors.As(err, &ue) || ue.Text != notSignedIn {
			t.Fatal(err)
		}
	}
}

func TestActionsManifest(t *testing.T) {
	var i interface {
		plugins.Reactor
		plugins.Editor
		plugins.Deleter
	} = Plugin{}
	_ = i
	m := plugins.Manifest(plugins.Get("telegram"), "en")
	if m["can_react"] != true || m["can_edit"] != true || m["can_delete"] != true || m["free_reactions"] != false {
		t.Fatalf("%v", m)
	}
	if r := m["reactions"].([]string); !reflect.DeepEqual(r[:6], []string{"👍", "❤", "🔥", "🥰", "👏", "😁"}) {
		t.Fatalf("%v", r)
	}
}
