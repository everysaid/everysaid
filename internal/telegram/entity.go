// Ports the parts of Telethon's utils the Python uses (get_peer_id, get_display_name,
// get_input_peer) and entity_kind of everysaid/telegram_store.py, for gotd's types.
package telegram

import "github.com/gotd/td/tg"

// channelMark: channels' marked ids grow backwards from -100_0000_000_000.
const channelMark = 1000000000000

// PeerID is Telethon's marked id of a peer: users as they are, groups negative, -100...
// supergroups and channels.
func PeerID(p tg.PeerClass) int64 {
	switch p := p.(type) {
	case *tg.PeerUser:
		return p.UserID
	case *tg.PeerChat:
		return -p.ChatID
	case *tg.PeerChannel:
		return -(channelMark + p.ChannelID)
	}
	return 0
}

// EntityID is the marked id of a user, a group or a channel (0 for anything else).
func EntityID(e any) int64 {
	switch e := e.(type) {
	case *tg.User:
		return e.ID
	case *tg.UserEmpty:
		return e.ID
	case *tg.Chat:
		return -e.ID
	case *tg.ChatForbidden:
		return -e.ID
	case *tg.ChatEmpty:
		return -e.ID
	case *tg.Channel:
		return -(channelMark + e.ID)
	case *tg.ChannelForbidden:
		return -(channelMark + e.ID)
	case *tg.Community:
		return -(channelMark + e.ID)
	case *tg.CommunityForbidden:
		return -(channelMark + e.ID)
	}
	return 0
}

// peerOf is the peer of a marked id.
func peerOf(id int64) tg.PeerClass {
	switch {
	case id >= 0:
		return &tg.PeerUser{UserID: id}
	case id <= -channelMark:
		return &tg.PeerChannel{ChannelID: -id - channelMark}
	}
	return &tg.PeerChat{ChatID: -id}
}

// DisplayName is Telethon's get_display_name: a user's first and last name, a chat's title.
func DisplayName(e any) string {
	switch e := e.(type) {
	case *tg.User:
		switch {
		case e.LastName != "" && e.FirstName != "":
			return e.FirstName + " " + e.LastName
		case e.FirstName != "":
			return e.FirstName
		}
		return e.LastName
	case *tg.Chat:
		return e.Title
	case *tg.ChatForbidden:
		return e.Title
	case *tg.Channel:
		return e.Title
	case *tg.ChannelForbidden:
		return e.Title
	case *tg.Community:
		return e.Title
	case *tg.CommunityForbidden:
		return e.Title
	}
	return ""
}

// EntityKind is user, saved, bot, group, supergroup or channel, from an entity (telegram_store.py).
func EntityKind(e any) string {
	switch e := e.(type) {
	case *tg.User:
		if e.Self {
			return "saved"
		}
		if e.Bot {
			return "bot"
		}
		return "user"
	case *tg.Channel:
		if e.Broadcast {
			return "channel"
		}
		return "supergroup"
	case *tg.ChannelForbidden:
		if e.Broadcast {
			return "channel"
		}
		return "supergroup"
	}
	return "group"
}

// InputPeer is Telethon's get_input_peer (with the access hash checked): ok false for a user or
// channel known only by "min" information, which cannot be addressed.
func InputPeer(e any) (tg.InputPeerClass, bool) {
	switch e := e.(type) {
	case *tg.User:
		if e.Min {
			return nil, false
		}
		hash, ok := e.GetAccessHash()
		if !ok {
			return nil, false
		}
		return &tg.InputPeerUser{UserID: e.ID, AccessHash: hash}, true
	case *tg.Chat:
		return &tg.InputPeerChat{ChatID: e.ID}, true
	case *tg.ChatForbidden:
		return &tg.InputPeerChat{ChatID: e.ID}, true
	case *tg.ChatEmpty:
		return &tg.InputPeerChat{ChatID: e.ID}, true
	case *tg.Channel:
		hash, ok := e.GetAccessHash()
		if e.Min || !ok {
			return nil, false
		}
		return &tg.InputPeerChannel{ChannelID: e.ID, AccessHash: hash}, true
	case *tg.ChannelForbidden:
		return &tg.InputPeerChannel{ChannelID: e.ID, AccessHash: e.AccessHash}, true
	case *tg.Community:
		hash, ok := e.GetAccessHash()
		if e.Min || !ok {
			return nil, false
		}
		return &tg.InputPeerChannel{ChannelID: e.ID, AccessHash: hash}, true
	case *tg.CommunityForbidden:
		return &tg.InputPeerChannel{ChannelID: e.ID, AccessHash: e.AccessHash}, true
	}
	return nil, false
}

// isEntity says whether a value is a user, a group or a channel (what the sync keeps of senders:
// User, Chat, Channel).
func isEntity(e any) bool {
	switch e.(type) {
	case *tg.User, *tg.Chat, *tg.Channel:
		return true
	}
	return false
}
