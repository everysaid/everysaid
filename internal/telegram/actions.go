// What the user does to a message from the app (no Python counterpart): a reaction put, changed or
// taken back, an edit of their own message, a deletion of it for everyone. Each goes through the
// connection open in this process, as sending does, and what Telegram says of the message afterwards
// is written to telegram.db and imported, the way the live connection brings the same change made on
// a phone.
package telegram

import (
	"context"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/plugins"
)

// reactions are the reactions Telegram lets anyone put without Premium (its default set, as
// messages.getAvailableReactions gives it, written as Telegram writes them: without the emoji
// variation selector), the quick ones of its apps first. A chat may allow fewer: Telegram then
// refuses, and the user is told.
var reactions = []string{"👍", "❤", "🔥", "🥰", "👏", "😁",
	"👎", "🤔", "🤯", "😱", "🤬", "😢", "🎉", "🤩", "🤮", "💩", "🙏", "👌", "🕊", "🤡", "🥱", "🥴", "😍", "🐳",
	"❤\u200d🔥", "🌚", "🌭", "💯", "🤣", "⚡", "🍌", "🏆", "💔", "🤨", "😐", "🍓", "🍾", "💋", "🖕", "😈", "😴",
	"😭", "🤓", "👻", "👨\u200d💻", "👀", "🎃", "🙈", "😇", "😨", "🤝", "✍", "🤗", "🫡", "🎅", "🎄", "☃", "💅",
	"🤪", "🗿", "🆒", "💘", "🙉", "🦄", "😘", "💊", "🙊", "😎", "👾", "🤷\u200d♂", "🤷", "🤷\u200d♀", "😡"}

const (
	reactionRefused = "Telegram does not allow this reaction in this chat"
	editTooLate     = "Telegram no longer allows this message to be edited"
	deleteRefused   = "Telegram does not allow this message to be deleted for everyone"
	messageGone     = "The message is no longer on Telegram"
)

func (Plugin) React(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, emoji string) error {
	return onMessage(ctx, c, conv, msg, func(ctx context.Context, cn *conn, peer tg.InputPeerClass, chat int64, id int) error {
		req := &tg.MessagesSendReactionRequest{Peer: peer, MsgID: id}
		if emoji != "" { // none at all takes the user's back
			// the same emoji with the variation selector (as other services and keyboards write it)
			// is not one Telegram knows
			req.SetReaction([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: strings.ReplaceAll(emoji, "\ufe0f", "")}})
		}
		r, err := cn.api.MessagesSendReaction(ctx, req)
		switch {
		case tgerr.Is(err, "REACTION_INVALID", "REACTION_EMPTY"):
			return pluginErr(reactionRefused)
		case err != nil:
			return err
		}
		return restore(ctx, c, cn, peer, chat, id, r)
	})
}

func (Plugin) Edit(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, text string) error {
	return onMessage(ctx, c, conv, msg, func(ctx context.Context, cn *conn, peer tg.InputPeerClass, chat int64, id int) error {
		req := &tg.MessagesEditMessageRequest{Peer: peer, ID: id}
		req.SetMessage(text)
		r, err := cn.api.MessagesEditMessage(ctx, req)
		switch {
		case tgerr.Is(err, "MESSAGE_NOT_MODIFIED"): // the same text: nothing to do
			return nil
		case tgerr.Is(err, "MESSAGE_EDIT_TIME_EXPIRED"):
			return pluginErr(editTooLate)
		case err != nil:
			return err
		}
		return restore(ctx, c, cn, peer, chat, id, r)
	})
}

func (Plugin) Delete(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref) error {
	return onMessage(ctx, c, conv, msg, func(ctx context.Context, cn *conn, peer tg.InputPeerClass, chat int64, id int) error {
		var err error
		if ch, ok := peer.(*tg.InputPeerChannel); ok { // a supergroup's messages are its own, deleted for all
			_, err = cn.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
				Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}, ID: []int{id}})
		} else {
			_, err = cn.api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{id}})
		}
		switch {
		case tgerr.Is(err, "MESSAGE_DELETE_FORBIDDEN"):
			return pluginErr(deleteRefused)
		case err != nil:
			return err
		}
		return noteDeleted(c, chat, []int{id})
	})
}

// onMessage runs fn on the message (its chat's id, its own id on Telegram) through the connection
// open in this process, else one made for it.
func onMessage(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref,
	fn func(ctx context.Context, cn *conn, peer tg.InputPeerClass, chat int64, id int) error) error {
	chat, err := strconv.ParseInt(conv.Key, 10, 64)
	if err != nil {
		return err
	}
	id, err := strconv.Atoi(msg.Key)
	if err != nil {
		return err
	}
	return withConn(ctx, func(ctx context.Context, cn *conn) error {
		peer, err := cn.peer(ctx, chat)
		if err != nil {
			return err
		}
		err = fn(ctx, cn, peer, chat, id)
		if tgerr.Is(err, "MESSAGE_ID_INVALID") {
			return pluginErr(messageGone)
		}
		return err
	})
}

// restore writes the message as Telegram has it now to telegram.db and imports it: the message the
// answer carries (an edit's), else the message asked for again (a reaction's answer says only the
// reactions).
func restore(ctx context.Context, c *plugins.Context, cn *conn, peer tg.InputPeerClass, chat int64, id int, r tg.UpdatesClass) error {
	var list []tg.UpdateClass
	switch u := r.(type) {
	case *tg.Updates:
		list = u.Updates
		cn.seen(u.Users, u.Chats)
	case *tg.UpdatesCombined:
		list = u.Updates
		cn.seen(u.Users, u.Chats)
	case *tg.UpdateShort:
		list = []tg.UpdateClass{u.Update}
	}
	var m tg.MessageClass
	for _, u := range list {
		var x tg.MessageClass
		switch u := u.(type) {
		case *tg.UpdateEditMessage:
			x = u.Message
		case *tg.UpdateEditChannelMessage:
			x = u.Message
		}
		if x != nil && x.GetID() == id {
			m = x
		}
	}
	if m == nil {
		got, err := cn.byIDs(ctx, peer, []int{id})
		if err != nil {
			return err
		}
		if m = got[0]; m == nil {
			return nil // gone meanwhile: its deletion comes as any other
		}
	}
	entity := cn.entity(chat)
	if entity == nil {
		var err error
		if entity, err = fetchEntity(ctx, cn, peer); err != nil {
			return err
		}
	}
	var sender any
	if s := senderID(m); s != 0 {
		sender = cn.entity(s)
	}
	_, err := storeMessages(c, entity, []sent{{m, sender}})
	return err
}
