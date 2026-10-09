package telegram

// A fake of the Telegram API surface this package uses, answering gotd's own client (tg.Client over
// a tg.Invoker): every test runs on invented chats in temporary folders, never on a real account,
// a real store or a real archive, and nothing connects to Telegram.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/config"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "everysaid-telegram-test")
	for k, v := range map[string]string{"EVERYSAID_DATA": "data", "EVERYSAID_CACHE": "cache",
		"EVERYSAID_CONFIG": "config", "EVERYSAID_STATE": "state"} {
		os.Setenv(k, filepath.Join(dir, v))
	}
	historyPause = 0
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test-telegram")
	config.Load()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeChat is one dialog of the fake account.
type fakeChat struct {
	entity   any // *tg.User, *tg.Chat, *tg.Channel
	peer     tg.PeerClass
	messages []tg.MessageClass // any order
	folder   int
	pinned   bool
	readIn   int
	readOut  int
	mute     int
	members  []int64 // a group's members, as getFullChat and getParticipants give them
	shown    int     // a supergroup's members given at most (hidden members), 0 for all
	refuse   string  // their refusal (CHAT_ADMIN_REQUIRED), "" for none
	hidden   bool    // not among the dialogs (a basic group made a supergroup)
	from     int64   // a supergroup's basic group before (migrated_from_chat_id), 0 if none
}

type fake struct {
	mu       sync.Mutex
	self     *tg.User
	chats    []*fakeChat
	users    []tg.UserClass // all users the answers carry
	calls    []string
	requests []bin.Encoder
	files    map[int64][]byte // document id -> its bytes
	nextID   int
	noPhotos bool                             // sendMedia refuses photos (PHOTO_INVALID_DIMENSIONS)
	refused  string                           // a reaction sendReaction refuses (REACTION_INVALID)
	blocked  []int64                          // the users blocked, in the order blocked
	asked    func(input bin.Encoder)          // called at each request, before its answer
	readBy   map[int][]tg.ReadParticipantDate // who read each message (by id) of a group
	phones   map[string]int64                 // the user each number finds (resolvePhone), as digits
}

func (f *fake) chat(peer tg.PeerClass) *fakeChat {
	for _, c := range f.chats {
		if PeerID(c.peer) == PeerID(peer) {
			return c
		}
	}
	return nil
}

// message is the chat's message of that id, nil if none.
func (c *fakeChat) message(id int) *tg.Message {
	for _, m := range c.messages {
		if x, ok := m.(*tg.Message); ok && x.ID == id {
			return x
		}
	}
	return nil
}

// drop deletes the chat's messages of those ids.
func (c *fakeChat) drop(ids []int) {
	var keep []tg.MessageClass
	for _, m := range c.messages {
		if !slices.Contains(ids, m.GetID()) {
			keep = append(keep, m)
		}
	}
	c.messages = keep
}

func inputToPeer(p tg.InputPeerClass, self int64) tg.PeerClass {
	switch p := p.(type) {
	case *tg.InputPeerUser:
		return &tg.PeerUser{UserID: p.UserID}
	case *tg.InputPeerSelf:
		return &tg.PeerUser{UserID: self}
	case *tg.InputPeerChat:
		return &tg.PeerChat{ChatID: p.ChatID}
	case *tg.InputPeerChannel:
		return &tg.PeerChannel{ChannelID: p.ChannelID}
	}
	return nil
}

func (f *fake) chatsList() []tg.ChatClass {
	var out []tg.ChatClass
	for _, c := range f.chats {
		if ch, ok := c.entity.(tg.ChatClass); ok {
			out = append(out, ch)
		}
	}
	return out
}

func sortedDesc(ms []tg.MessageClass) []tg.MessageClass {
	out := append([]tg.MessageClass{}, ms...)
	sort.Slice(out, func(i, j int) bool { return out[i].GetID() > out[j].GetID() })
	return out
}

// history answers getHistory as Telegram does: newest first, from the first message older than
// offset_id, moved by add_offset, limit of them.
func history(ms []tg.MessageClass, offsetID, addOffset, limit int) []tg.MessageClass {
	ms = sortedDesc(ms)
	p := len(ms)
	if offsetID == 0 {
		p = 0
	} else {
		for i, m := range ms {
			if m.GetID() < offsetID {
				p = i
				break
			}
		}
	}
	from := max(0, p+addOffset)
	to := min(len(ms), max(from, p+addOffset+limit))
	return ms[from:to]
}

func (f *fake) answer(input bin.Encoder) (bin.Encoder, error) {
	switch r := input.(type) {
	case *tg.MessagesGetDialogsRequest:
		f.calls = append(f.calls, "getDialogs")
		var shown []*fakeChat
		for _, c := range f.chats {
			if !c.hidden {
				shown = append(shown, c)
			}
		}
		start := 0
		if p := inputToPeer(r.OffsetPeer, f.self.ID); p != nil {
			for i, c := range shown {
				if PeerID(c.peer) == PeerID(p) {
					start = i + 1
				}
			}
		}
		end := min(len(shown), start+r.Limit)
		out := &tg.MessagesDialogsSlice{Count: len(shown), Users: f.users, Chats: f.chatsList()}
		for _, c := range shown[start:end] {
			top := sortedDesc(c.messages)
			d := &tg.Dialog{Peer: c.peer, ReadInboxMaxID: c.readIn, ReadOutboxMaxID: c.readOut, Pinned: c.pinned}
			if c.mute != 0 {
				d.NotifySettings.SetMuteUntil(c.mute)
			}
			if c.folder != 0 {
				d.SetFolderID(c.folder)
			}
			if len(top) > 0 {
				d.TopMessage = top[0].GetID()
				out.Messages = append(out.Messages, top[0])
			}
			out.Dialogs = append(out.Dialogs, d)
		}
		return &tg.MessagesDialogsBox{Dialogs: out}, nil
	case *tg.MessagesGetHistoryRequest:
		f.calls = append(f.calls, "getHistory")
		c := f.chat(inputToPeer(r.Peer, f.self.ID))
		if c == nil {
			return nil, fmt.Errorf("PEER_ID_INVALID")
		}
		return &tg.MessagesMessagesBox{Messages: &tg.MessagesMessagesSlice{Count: len(c.messages),
			Messages: history(c.messages, r.OffsetID, r.AddOffset, r.Limit), Users: f.users, Chats: f.chatsList()}}, nil
	case *tg.MessagesSearchRequest:
		f.calls = append(f.calls, "search")
		c := f.chat(inputToPeer(r.Peer, f.self.ID))
		n := 0
		for _, m := range c.messages {
			if msg, ok := m.(*tg.Message); ok && msg.Media != nil {
				n++
			}
		}
		return &tg.MessagesMessagesBox{Messages: &tg.MessagesMessagesSlice{Count: n}}, nil
	case *tg.MessagesGetMessagesRequest:
		f.calls = append(f.calls, "getMessages")
		var out []tg.MessageClass
		for _, in := range r.ID {
			id := in.(*tg.InputMessageID).ID
			for _, c := range f.chats {
				if _, ch := c.peer.(*tg.PeerChannel); ch {
					continue
				}
				for _, m := range c.messages {
					if m.GetID() == id {
						out = append(out, m)
					}
				}
			}
		}
		return &tg.MessagesMessagesBox{Messages: &tg.MessagesMessages{Messages: out, Users: f.users}}, nil
	case *tg.UploadGetFileRequest:
		f.calls = append(f.calls, "getFile")
		loc := r.Location.(*tg.InputDocumentFileLocation)
		data, ok := f.files[loc.ID]
		if !ok {
			return nil, &tgerr.Error{Code: 400, Type: "FILE_ID_INVALID"}
		}
		if r.Offset >= int64(len(data)) {
			data = nil
		} else {
			data = data[r.Offset:min(int64(len(data)), r.Offset+int64(r.Limit))]
		}
		return &tg.UploadFileBox{File: &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}}, nil
	case *tg.UploadSaveFilePartRequest:
		return &tg.BoolBox{Bool: &tg.BoolTrue{}}, nil
	case *tg.UploadSaveBigFilePartRequest:
		return &tg.BoolBox{Bool: &tg.BoolTrue{}}, nil
	case *tg.MessagesSendMediaRequest:
		f.calls = append(f.calls, "sendMedia")
		sent := *r // as it was asked (the caller may change its request and ask again)
		f.requests = append(f.requests, &sent)
		if _, photo := r.Media.(*tg.InputMediaUploadedPhoto); photo && f.noPhotos {
			return nil, &tgerr.Error{Code: 400, Type: "PHOTO_INVALID_DIMENSIONS"}
		}
		f.nextID++
		return &tg.UpdatesBox{Updates: &tg.UpdateShortSentMessage{Out: true, ID: f.nextID, Date: 1700000000}}, nil
	case *tg.MessagesSendMessageRequest:
		f.calls = append(f.calls, "sendMessage")
		f.requests = append(f.requests, r)
		f.nextID++
		return &tg.UpdatesBox{Updates: &tg.UpdateShortSentMessage{Out: true, ID: f.nextID, Date: 1700000000}}, nil
	case *tg.UsersGetUsersRequest:
		f.calls = append(f.calls, "getUsers")
		var out []tg.UserClass
		for _, in := range r.ID {
			for _, u := range f.users {
				if u.(*tg.User).ID == in.(*tg.InputUser).UserID {
					out = append(out, u)
				}
			}
		}
		return &tg.UserClassVector{Elems: out}, nil
	case *tg.MessagesReadHistoryRequest:
		f.calls = append(f.calls, "readHistory")
		f.requests = append(f.requests, r)
		return &tg.MessagesAffectedMessages{}, nil
	case *tg.ChannelsGetMessagesRequest:
		f.calls = append(f.calls, "channels.getMessages")
		c := f.chat(&tg.PeerChannel{ChannelID: r.Channel.(*tg.InputChannel).ChannelID})
		var out []tg.MessageClass
		for _, in := range r.ID {
			if m := c.message(in.(*tg.InputMessageID).ID); m != nil {
				out = append(out, m)
			}
		}
		return &tg.MessagesMessagesBox{Messages: &tg.MessagesChannelMessages{Messages: out, Users: f.users, Chats: f.chatsList()}}, nil
	case *tg.MessagesSendReactionRequest:
		f.calls = append(f.calls, "sendReaction")
		f.requests = append(f.requests, r)
		peer := inputToPeer(r.Peer, f.self.ID)
		m := f.chat(peer).message(r.MsgID)
		if m == nil {
			return nil, &tgerr.Error{Code: 400, Type: "MESSAGE_ID_INVALID"}
		}
		var mr tg.MessageReactions
		if len(r.Reaction) > 0 {
			if e := r.Reaction[0].(*tg.ReactionEmoji); e.Emoticon == f.refused {
				return nil, &tgerr.Error{Code: 400, Type: "REACTION_INVALID"}
			}
			count := tg.ReactionCount{Reaction: r.Reaction[0], Count: 1}
			count.SetChosenOrder(0)
			mr.Results = []tg.ReactionCount{count}
			mine := tg.MessagePeerReaction{PeerID: &tg.PeerUser{UserID: f.self.ID}, Reaction: r.Reaction[0], My: true}
			mr.SetRecentReactions([]tg.MessagePeerReaction{mine})
		}
		m.SetReactions(mr)
		return &tg.UpdatesBox{Updates: &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageReactions{Peer: peer, MsgID: r.MsgID, Reactions: mr}}}}, nil
	case *tg.MessagesEditMessageRequest:
		f.calls = append(f.calls, "editMessage")
		f.requests = append(f.requests, r)
		m := f.chat(inputToPeer(r.Peer, f.self.ID)).message(r.ID)
		if m == nil {
			return nil, &tgerr.Error{Code: 400, Type: "MESSAGE_ID_INVALID"}
		}
		if m.Message == r.Message {
			return nil, &tgerr.Error{Code: 400, Type: "MESSAGE_NOT_MODIFIED"}
		}
		m.Message = r.Message
		m.SetEditDate(1700000100)
		return &tg.UpdatesBox{Updates: &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateEditMessage{Message: m}}}}, nil
	case *tg.MessagesDeleteMessagesRequest:
		f.calls = append(f.calls, "deleteMessages")
		f.requests = append(f.requests, r)
		for _, c := range f.chats {
			if _, ch := c.peer.(*tg.PeerChannel); !ch {
				c.drop(r.ID)
			}
		}
		return &tg.MessagesAffectedMessages{}, nil
	case *tg.MessagesGetFullChatRequest:
		f.calls = append(f.calls, "getFullChat")
		c := f.chat(&tg.PeerChat{ChatID: r.ChatID})
		if c == nil {
			return nil, &tgerr.Error{Code: 400, Type: "CHAT_ID_INVALID"}
		}
		if c.refuse != "" {
			return nil, &tgerr.Error{Code: 400, Type: c.refuse}
		}
		ps := &tg.ChatParticipants{ChatID: r.ChatID}
		for _, id := range c.members {
			ps.Participants = append(ps.Participants, &tg.ChatParticipant{UserID: id})
		}
		return &tg.MessagesChatFull{FullChat: &tg.ChatFull{ID: r.ChatID, Participants: ps}, Users: f.users, Chats: f.chatsList()}, nil
	case *tg.ChannelsGetParticipantsRequest:
		f.calls = append(f.calls, "getParticipants")
		c := f.chat(&tg.PeerChannel{ChannelID: r.Channel.(*tg.InputChannel).ChannelID})
		if c.refuse != "" {
			return nil, &tgerr.Error{Code: 400, Type: c.refuse}
		}
		list := c.members
		if c.shown > 0 {
			list = list[:c.shown]
		}
		out := &tg.ChannelsChannelParticipants{Count: len(c.members), Users: f.users, Chats: f.chatsList()}
		for _, id := range list[min(r.Offset, len(list)):min(r.Offset+r.Limit, len(list))] {
			out.Participants = append(out.Participants, &tg.ChannelParticipant{UserID: id})
		}
		return out, nil
	case *tg.ContactsResolvePhoneRequest:
		f.calls = append(f.calls, "resolvePhone")
		for _, u := range f.users {
			if id, ok := f.phones[r.Phone]; ok && u.(*tg.User).ID == id {
				x := *u.(*tg.User) // Telegram gives the number only to contacts
				x.Phone = ""
				return &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: id}, Users: []tg.UserClass{&x}}, nil
			}
		}
		return nil, &tgerr.Error{Code: 400, Type: "PHONE_NOT_OCCUPIED"}
	case *tg.MessagesReportSpamRequest:
		f.calls = append(f.calls, "reportSpam")
		return &tg.BoolBox{Bool: &tg.BoolTrue{}}, nil
	case *tg.ContactsBlockRequest:
		f.calls = append(f.calls, "block")
		if u, ok := r.ID.(*tg.InputPeerUser); ok && !slices.Contains(f.blocked, u.UserID) {
			f.blocked = append(f.blocked, u.UserID)
		}
		return &tg.BoolBox{Bool: &tg.BoolTrue{}}, nil
	case *tg.MessagesDeleteHistoryRequest:
		f.calls = append(f.calls, "deleteHistory")
		if c := f.chat(inputToPeer(r.Peer, f.self.ID)); c != nil {
			if len(c.messages) > 100 { // a part at a time, as Telegram deletes a long chat
				c.messages = sortedDesc(c.messages)[100:]
				return &tg.MessagesAffectedHistory{Offset: 1}, nil
			}
			c.messages = nil
		}
		return &tg.MessagesAffectedHistory{}, nil
	case *tg.ContactsGetBlockedRequest:
		f.calls = append(f.calls, "getBlocked")
		var page []tg.PeerBlocked
		for _, id := range f.blocked[min(r.Offset, len(f.blocked)):min(r.Offset+r.Limit, len(f.blocked))] {
			page = append(page, tg.PeerBlocked{PeerID: &tg.PeerUser{UserID: id}})
		}
		return &tg.ContactsBlockedBox{Blocked: &tg.ContactsBlockedSlice{Count: len(f.blocked), Blocked: page, Users: f.users}}, nil
	case *tg.ChannelsGetFullChannelRequest:
		f.calls = append(f.calls, "getFullChannel")
		id := r.Channel.(*tg.InputChannel).ChannelID
		c := f.chat(&tg.PeerChannel{ChannelID: id})
		full := &tg.ChannelFull{ID: id, ChatPhoto: &tg.PhotoEmpty{}, NotifySettings: tg.PeerNotifySettings{},
			ExportedInvite: &tg.ChatInviteExported{}}
		if c != nil && c.from != 0 {
			full.SetMigratedFromChatID(c.from)
			full.SetMigratedFromMaxID(len(f.chat(&tg.PeerChat{ChatID: c.from}).messages))
		}
		return &tg.MessagesChatFull{FullChat: full, Users: f.users, Chats: f.chatsList()}, nil
	case *tg.MessagesGetMessageReadParticipantsRequest:
		f.calls = append(f.calls, "getMessageReadParticipants")
		return &tg.ReadParticipantDateVector{Elems: f.readBy[r.MsgID]}, nil
	case *tg.ChannelsDeleteMessagesRequest:
		f.calls = append(f.calls, "channels.deleteMessages")
		f.requests = append(f.requests, r)
		f.chat(&tg.PeerChannel{ChannelID: r.Channel.(*tg.InputChannel).ChannelID}).drop(r.ID)
		return &tg.MessagesAffectedMessages{}, nil
	}
	return nil, fmt.Errorf("the fake does not answer %T", input)
}

func (f *fake) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	if f.asked != nil {
		f.asked(input)
	}
	f.mu.Lock()
	resp, err := f.answer(input)
	f.mu.Unlock()
	if err != nil {
		return err
	}
	var b bin.Buffer
	if err := resp.Encode(&b); err != nil {
		return err
	}
	return output.Decode(&b)
}

func (f *fake) conn() *conn { return newConn(tg.NewClient(f)) }

func user(id int64, first string, hash int64) *tg.User {
	u := &tg.User{ID: id, FirstName: first}
	u.SetAccessHash(hash)
	return u
}

func text(id int, peer tg.PeerClass, from int64, date int, s string, out bool) *tg.Message {
	m := &tg.Message{ID: id, PeerID: peer, Date: date, Message: s}
	m.SetOut(out)
	if from != 0 {
		m.SetFromID(&tg.PeerUser{UserID: from})
	}
	return m
}

// account is the invented account: the owner (1), user 2 with 250 messages, a group (10) of
// five, a channel and a bot that stay out, and Saved Messages.
func account() *fake {
	self := user(1, "Me", 11)
	self.SetSelf(true)
	bob := user(2, "Bob", 22)
	bot := user(3, "Botty", 33)
	bot.SetBot(true)
	channel := &tg.Channel{ID: 20, Title: "News", Photo: &tg.ChatPhotoEmpty{}}
	channel.SetBroadcast(true)
	channel.SetAccessHash(44)
	group := &tg.Chat{ID: 10, Title: "Friends", Photo: &tg.ChatPhotoEmpty{}}
	f := &fake{self: self, users: []tg.UserClass{self, bob, bot}, nextID: 1000, files: map[int64][]byte{}}
	bobChat := &fakeChat{entity: bob, peer: &tg.PeerUser{UserID: 2}, readIn: 240, readOut: 245, pinned: true, mute: 2147483647}
	for i := 1; i <= 250; i++ {
		bobChat.messages = append(bobChat.messages, text(i, &tg.PeerUser{UserID: 2}, 0, 1600000000+i, fmt.Sprint("m", i), i%2 == 0))
	}
	groupChat := &fakeChat{entity: group, peer: &tg.PeerChat{ChatID: 10}, folder: 1}
	for i := 1; i <= 5; i++ {
		groupChat.messages = append(groupChat.messages, text(i, &tg.PeerChat{ChatID: 10}, 2, 1600000000+i, "g", false))
	}
	f.chats = []*fakeChat{bobChat, groupChat,
		{entity: channel, peer: &tg.PeerChannel{ChannelID: 20}, messages: []tg.MessageClass{text(1, &tg.PeerChannel{ChannelID: 20}, 0, 1600000000, "n", false)}},
		{entity: bot, peer: &tg.PeerUser{UserID: 3}, messages: []tg.MessageClass{text(1, &tg.PeerUser{UserID: 3}, 0, 1600000000, "b", false)}},
		{entity: self, peer: &tg.PeerUser{UserID: 1}, messages: []tg.MessageClass{text(1, &tg.PeerUser{UserID: 1}, 1, 1600000000, "note", true)}},
	}
	return f
}

// many is an account of n people, one message each, for the dialogs' pages.
func many(n int) *fake {
	self := user(1, "Me", 11)
	self.SetSelf(true)
	f := &fake{self: self, users: []tg.UserClass{self}}
	for i := 0; i < n; i++ {
		u := user(int64(100+i), fmt.Sprint("P", i), int64(i))
		f.users = append(f.users, u)
		peer := &tg.PeerUser{UserID: u.ID}
		f.chats = append(f.chats, &fakeChat{entity: u, peer: peer, messages: []tg.MessageClass{text(1, peer, 0, 1600000000-i, "x", false)}})
	}
	return f
}

func newTestCtx() context.Context { return context.Background() }
