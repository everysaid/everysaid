//go:build signal

package signal

// The history the phone transfers when it links this device (Signal's "transfer message history":
// an encrypted archive of its chats, signalmeow's backup.go), as the client's events: the people and
// groups, each chat's state, and each chat's items as messages with their edits, reactions,
// receipts and reads, and the calls of the phone's call log. signalmeow keeps the archive in its
// tables signalmeow_backup_*; once its events are in the store they are cleared, nothing kept.

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/backuppb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
)

func base64Std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// transfer waits for the history the phone transfers once linked (for as long as signalmeow does:
// the phone may ask its user first), takes it into the store and clears it. What a stopped client
// took but did not finish is finished first.
func (c *client) transfer(cli *signalmeow.Client) {
	if n, err := c.applyBackup(cli); err != nil || n > 0 {
		c.sayTransfer(n, err)
	}
	if cli.Store.EphemeralBackupKey == nil {
		return
	}
	c.mu.Lock()
	if c.transferring {
		c.mu.Unlock()
		return
	}
	c.transferring = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.transferring = false
		c.mu.Unlock()
	}()
	meta, err := cli.WaitForTransfer(c.ctx)
	if c.ctx.Err() != nil {
		return
	}
	// the phone's user chose to go on without it (or to link again), or it did not come within the
	// hour signalmeow waits: not waited for again
	giveUp := func() {
		cli.Store.EphemeralBackupKey = nil
		cli.Store.DeviceStore.PutDevice(c.ctx, &cli.Store.DeviceData)
	}
	switch {
	case err != nil && err.Error() == "timed out":
		giveUp()
		err = fmt.Errorf("the phone did not send it in time")
	case err == nil && meta.Error != "":
		giveUp()
		err = fmt.Errorf("the phone did not send it (%s)", meta.Error)
	}
	if err == nil {
		err = cli.FetchAndProcessTransfer(c.ctx, meta)
	}
	if err != nil {
		c.sayTransfer(0, err)
		return
	}
	c.sayTransfer(c.applyBackup(cli))
}

func (c *client) sayTransfer(n int, err error) {
	if err != nil {
		c.ev.say("transfer", M{"error": err.Error()})
		return
	}
	c.ev.say("transfer", M{"messages": n})
}

// applyBackup says the events of the archive signalmeow holds, each handled before the next, then
// clears the archive; it returns how many messages it had.
func (c *client) applyBackup(cli *signalmeow.Client) (int, error) {
	c.mu.Lock()
	download := c.download
	c.mu.Unlock()
	some := false
	n, err := c.backupEach(c.ctx, cli, func(x converted) error {
		some = true
		c.files(x, download)
		return c.ev.sayAndWait(x.name, x.ev)
	})
	if err != nil || !some {
		return 0, err // kept: done again at the next connection
	}
	return n, cli.Store.BackupStore.ClearBackup(c.ctx)
}

// history is the archive's events sent after since (Unix ms), as they are, without their files.
func (c *client) history(ctx context.Context, since uint64) (any, error) {
	cli, err := c.linked()
	if err != nil {
		return nil, err
	}
	out := []M{}
	_, err = c.backupEach(ctx, cli, func(x converted) error {
		if ts := toU64(x.ev["ts"]); ts == 0 || ts > since {
			ev := M{"event": x.name}
			for k, v := range x.ev {
				ev[k] = v
			}
			out = append(out, ev)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return M{"events": out}, nil
}

// transferArchive is what the transfer archive says of the account's people, groups and chats.
type transferArchive struct {
	own        string
	recipients map[uint64]*backuppb.Recipient
	groups     map[uint64]string // a group recipient's id
}

// person is a recipient's id: an ACI, else "PNI:<uuid>", the owner's own ACI for the account itself
// ("": none, a group, a list).
func (a *transferArchive) person(id uint64) string {
	r := a.recipients[id]
	switch {
	case r.GetSelf() != nil:
		return a.own
	case r.GetContact() != nil:
		x := r.GetContact()
		if len(x.GetAci()) == 16 {
			return uuid.UUID(x.GetAci()).String()
		}
		if len(x.GetPni()) == 16 {
			return libsignalgo.NewPNIServiceID(uuid.UUID(x.GetPni())).String()
		}
	}
	return ""
}

// chatOf is a chat by its recipient: a person's or a group's (nil: one that is neither).
func (a *transferArchive) chatOf(recipient uint64) M {
	if g := a.groups[recipient]; g != "" {
		return M{"kind": "group", "id": g}
	}
	if p := a.person(recipient); p != "" {
		return contactChat(p)
	}
	return nil
}

func aciOfBytes(b []byte) any {
	if len(b) == 16 {
		return uuid.UUID(b).String()
	}
	return nil
}

// backupEach hands each event of the archive signalmeow holds (none: it holds none) to each, a chat
// read at a time, and says how many messages they brought; each's error ends it.
func (c *client) backupEach(ctx context.Context, cli *signalmeow.Client, each func(converted) error) (int, error) {
	a := &transferArchive{own: c.own(cli), recipients: map[uint64]*backuppb.Recipient{}, groups: map[uint64]string{}}
	rows, err := c.pool.QueryContext(ctx, "SELECT data FROM signalmeow_backup_recipient WHERE account_id = ?", cli.Store.ACI.String())
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var data []byte
		var r backuppb.Recipient
		if rows.Scan(&data) == nil && proto.Unmarshal(data, &r) == nil {
			a.recipients[r.GetId()] = &r
		}
	}
	rows.Close()
	if len(a.recipients) == 0 {
		return 0, nil
	}
	now := time.Now().UnixMilli()
	ids := make([]uint64, 0, len(a.recipients))
	for id := range a.recipients {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	// the people (their ids, names) and the groups
	for _, id := range ids {
		r := a.recipients[id]
		if g := r.GetGroup(); g != nil {
			gid := groupIDOf(g.GetMasterKey())
			if gid == "" {
				continue
			}
			a.groups[id] = gid
			s := g.GetSnapshot()
			members := []string{}
			for _, m := range s.GetMembers() {
				if len(m.GetUserId()) == 16 {
					members = append(members, uuid.UUID(m.GetUserId()).String())
				}
			}
			if err := each(plain("group", M{"id": gid, "title": s.GetTitle().GetTitle(),
				"description": s.GetDescription().GetDescriptionText(), "revision": s.GetVersion(), "members": members,
				"pending": []string{}})); err != nil {
				return 0, err
			}
		}
		x := r.GetContact()
		if x == nil {
			continue
		}
		var aci, pni, phone any = aciOfBytes(x.GetAci()), nil, nil
		if len(x.GetPni()) == 16 {
			pni = libsignalgo.NewPNIServiceID(uuid.UUID(x.GetPni())).String()
		}
		if x.E164 != nil {
			phone = "+" + strconv.FormatUint(x.GetE164(), 10)
		}
		if aci != nil && (pni != nil || phone != nil) || pni != nil && phone != nil {
			if err := each(idsEvent(aci, pni, phone)); err != nil {
				return 0, err
			}
		}
		if aci != nil {
			name := joinNonEmpty(x.GetSystemGivenName(), x.GetSystemFamilyName())
			if name == "" {
				name = x.GetSystemNickname()
			}
			ev := M{"aci": aci, "phone": phone, "name": nil, "profile_name": nil}
			if name != "" {
				ev["name"] = name
			}
			if p := joinNonEmpty(x.GetProfileGivenName(), x.GetProfileFamilyName()); p != "" {
				ev["profile_name"] = p
			}
			if err := each(plain("contact", ev)); err != nil {
				return 0, err
			}
		}
	}
	chats, err := cli.Store.BackupStore.GetBackupChats(ctx)
	if err != nil {
		return 0, err
	}
	messages := 0
	for _, ch := range chats {
		chat := a.chatOf(ch.GetRecipientId())
		if chat == nil {
			continue
		}
		muted := int64(ch.GetMuteUntilMs())
		if ch.GetMuteUntilMs() >= math.MaxInt64 {
			muted = -1
		}
		if err := each(plain("chat_state", M{"chat": chat, "ts": now, "state": M{"archived": b2i(ch.GetArchived()),
			"pinned": b2i(ch.PinnedOrder != nil), "muted": muted}})); err != nil {
			return 0, err
		}
		items, err := cli.Store.BackupStore.GetBackupChatItems(ctx, ch.GetId(), time.Time{}, false, ch.TotalMessages+1000)
		if err != nil {
			return 0, err
		}
		slices.Reverse(items) // oldest first
		for _, it := range items {
			for _, x := range a.item(chat, it) {
				if x.name == "message" {
					messages++
				}
				if err := each(x); err != nil {
					return 0, err
				}
			}
		}
	}
	return messages, nil
}

// pointer is a transfer archive's file as Signal's AttachmentPointer (to fetch it from the transit
// CDN, where it still is), and the hash of its content where that is its check.
func pointer(f *backuppb.FilePointer, flags uint32) (*signalpb.AttachmentPointer, []byte) {
	p := &signalpb.AttachmentPointer{ContentType: f.ContentType, FileName: f.FileName, Width: f.Width, Height: f.Height,
		Caption: f.Caption, BlurHash: f.BlurHash}
	if flags != 0 {
		p.Flags = proto.Uint32(flags)
	}
	l := f.GetLocatorInfo()
	if l == nil {
		return p, nil
	}
	p.Key, p.Size = l.GetKey(), proto.Uint32(l.GetSize())
	if l.TransitCdnKey != nil {
		p.AttachmentIdentifier = &signalpb.AttachmentPointer_CdnKey{CdnKey: l.GetTransitCdnKey()}
		p.CdnNumber = l.TransitCdnNumber
	}
	if d := l.GetEncryptedDigest(); len(d) > 0 {
		p.Digest = d
		return p, nil
	}
	return p, l.GetPlaintextHash()
}

func attachmentFlags(f backuppb.MessageAttachment_Flag) uint32 {
	switch f {
	case backuppb.MessageAttachment_VOICE_MESSAGE:
		return uint32(signalpb.AttachmentPointer_VOICE_MESSAGE)
	case backuppb.MessageAttachment_BORDERLESS:
		return uint32(signalpb.AttachmentPointer_BORDERLESS)
	case backuppb.MessageAttachment_GIF:
		return uint32(signalpb.AttachmentPointer_GIF)
	}
	return 0
}

// item is one chat item's events: its first version as a message (or a call), each later version
// as an edit of it, its reactions, receipts and the owner's reading.
func (a *transferArchive) item(chat M, it *backuppb.ChatItem) []converted {
	author := a.person(it.GetAuthorId())
	if author == "" {
		return nil
	}
	outgoing := it.GetOutgoing() != nil || author == a.own
	versions := append(slices.Clone(it.GetRevisions()), it)
	slices.SortStableFunc(versions, func(x, y *backuppb.ChatItem) int {
		return int(min(max(int64(x.GetDateSent())-int64(y.GetDateSent()), -1), 1))
	})
	first := versions[0].GetDateSent()
	var out []converted
	for i, v := range versions {
		x, ok := a.message(chat, author, outgoing, v)
		if !ok {
			continue
		}
		if i > 0 && x.name == "message" {
			x.name = "edit"
			x.ev["target_ts"] = first
		}
		out = append(out, x)
		if i == 0 {
			out = append(out, a.extras(chat, author, first, v)...)
		}
	}
	// the reactions, as the newest version has them
	for _, r := range reactionsOf(it) {
		sender := a.person(r.GetAuthorId())
		if sender == "" {
			continue
		}
		out = append(out, plain("reaction", M{"chat": chat, "sender": sender, "sender_device": 0, "outgoing": sender == a.own,
			"ts": r.GetSentTimestamp(), "server_ts": r.GetSentTimestamp(), "emoji": r.GetEmoji(), "remove": false,
			"target_author": author, "target_ts": first}))
	}
	// the others' receipts of the owner's message, the owner's reading of theirs
	for _, s := range it.GetOutgoing().GetSendStatus() {
		kind := ""
		switch {
		case s.GetViewed() != nil:
			kind = "viewed"
		case s.GetRead() != nil:
			kind = "read"
		case s.GetDelivered() != nil:
			kind = "delivery"
		}
		if who := a.person(s.GetRecipientId()); kind != "" && who != "" {
			out = append(out, plain("receipt", M{"sender": who, "kind": kind, "timestamps": []uint64{first}, "ts": s.GetTimestamp()}))
		}
	}
	if in := it.GetIncoming(); in != nil && in.GetRead() {
		out = append(out, plain("read", M{"messages": []M{{"author": author, "ts": first}}, "ts": in.GetDateReceived()}))
	}
	return out
}

func reactionsOf(it *backuppb.ChatItem) []*backuppb.Reaction {
	switch {
	case it.GetStandardMessage() != nil:
		return it.GetStandardMessage().GetReactions()
	case it.GetContactMessage() != nil:
		return it.GetContactMessage().GetReactions()
	case it.GetStickerMessage() != nil:
		return it.GetStickerMessage().GetReactions()
	case it.GetViewOnceMessage() != nil:
		return it.GetViewOnceMessage().GetReactions()
	case it.GetDirectStoryReplyMessage() != nil:
		return it.GetDirectStoryReplyMessage().GetReactions()
	case it.GetPoll() != nil:
		return it.GetPoll().GetReactions()
	}
	return nil
}

// extras are what goes with a message's first version: its deletion for everyone, a poll's votes.
func (a *transferArchive) extras(chat M, author string, ts uint64, it *backuppb.ChatItem) []converted {
	var out []converted
	switch {
	case it.GetRemoteDeletedMessage() != nil:
		out = append(out, plain("delete", M{"chat": chat, "sender": author, "sender_device": 0, "outgoing": author == a.own,
			"ts": ts, "server_ts": ts, "target_author": author, "target_ts": ts}))
	case it.GetAdminDeletedMessage() != nil:
		admin := a.person(it.GetAdminDeletedMessage().GetAdminId())
		out = append(out, plain("delete", M{"chat": chat, "sender": admin, "sender_device": 0, "outgoing": admin == a.own,
			"ts": ts, "server_ts": ts, "target_author": author, "target_ts": ts, "admin": true}))
	case it.GetPoll() != nil:
		votes := map[uint64][]uint32{}
		counts := map[uint64]uint32{}
		var voters []uint64
		for i, o := range it.GetPoll().GetOptions() {
			for _, v := range o.GetVotes() {
				if _, ok := votes[v.GetVoterId()]; !ok {
					voters = append(voters, v.GetVoterId())
				}
				votes[v.GetVoterId()] = append(votes[v.GetVoterId()], uint32(i))
				counts[v.GetVoterId()] = max(counts[v.GetVoterId()], v.GetVoteCount())
			}
		}
		for _, id := range voters {
			if who := a.person(id); who != "" {
				out = append(out, plain("poll_vote", M{"chat": chat, "sender": who, "sender_device": 0, "outgoing": who == a.own,
					"ts": ts, "server_ts": ts, "target_author": author, "target_ts": ts, "options": votes[id], "vote_count": counts[id]}))
			}
		}
	}
	return out
}

// message is one version of a chat item as a message event (false: none to keep), or a call.
func (a *transferArchive) message(chat M, author string, outgoing bool, it *backuppb.ChatItem) (converted, bool) {
	ts := it.GetDateSent()
	server := ts
	if in := it.GetIncoming(); in != nil {
		server = cmpU64(in.GetDateServerSent(), in.GetDateReceived(), ts)
	}
	ev := M{"chat": chat, "sender": author, "sender_device": 0, "outgoing": outgoing, "ts": ts, "server_ts": server}
	if t := it.GetExpiresInMs(); t > 0 {
		ev["expire_timer"] = t / 1000
	}
	x := converted{name: "message", ev: ev}
	attach := func(f *backuppb.FilePointer, flags uint32, sticker bool) M {
		p, hash := pointer(f, flags)
		j := attachmentJSON(p, sticker)
		x.atts = append(x.atts, p)
		x.hashes = append(x.hashes, hash)
		return j
	}
	var list []M
	switch {
	case it.GetStandardMessage() != nil:
		m := it.GetStandardMessage()
		a.text(ev, m.GetText())
		if q := m.GetQuote(); q != nil {
			quoted := []M{}
			for _, at := range q.GetAttachments() {
				quoted = append(quoted, M{"content_type": optStr(at.ContentType), "filename": optStr(at.FileName)})
			}
			var author any
			if p := a.person(q.GetAuthorId()); p != "" {
				author = p
			}
			var text any
			if q.GetText() != nil {
				text = q.GetText().GetBody()
			}
			ev["quote"] = M{"ts": optU64(q.TargetSentTimestamp), "author": author, "text": text, "attachments": quoted}
		}
		for _, at := range m.GetAttachments() {
			if at.GetPointer() != nil {
				list = append(list, attach(at.GetPointer(), attachmentFlags(at.GetFlag()), false))
			}
		}
		if lt := m.GetLongText(); lt != nil {
			if lt.ContentType == nil {
				lt.ContentType = proto.String(longText)
			}
			list = append(list, attach(lt, 0, false))
		}
		if ps := m.GetLinkPreview(); len(ps) > 0 {
			previews := []M{}
			for _, p := range ps {
				previews = append(previews, M{"url": p.GetUrl(), "title": optStr(p.Title)})
			}
			ev["previews"] = previews
		}
	case it.GetContactMessage() != nil:
		ct := it.GetContactMessage().GetContact()
		var name any
		if n := ct.GetName(); n != nil {
			if full := joinNonEmpty(n.GetPrefix(), n.GetGivenName(), n.GetMiddleName(), n.GetFamilyName(), n.GetSuffix()); full != "" {
				name = full
			} else if n.GetNickname() != "" {
				name = n.GetNickname()
			}
		}
		phones, emails := []string{}, []string{}
		for _, p := range ct.GetNumber() {
			phones = append(phones, p.GetValue())
		}
		for _, e := range ct.GetEmail() {
			emails = append(emails, e.GetValue())
		}
		var org any
		if ct.GetOrganization() != "" {
			org = ct.GetOrganization()
		}
		ev["contacts"] = []M{{"name": name, "phones": phones, "emails": emails, "organization": org}}
	case it.GetStickerMessage() != nil:
		s := it.GetStickerMessage().GetSticker()
		if s.GetData() != nil {
			j := attach(s.GetData(), 0, true)
			j["emoji"] = optStr(s.Emoji)
			list = append(list, j)
		}
	case it.GetRemoteDeletedMessage() != nil, it.GetAdminDeletedMessage() != nil:
		// deleted for everyone: a message with nothing left, its deletion beside it (extras)
		x.ev = ev
		return x, true
	case it.GetUpdateMessage() != nil:
		return a.update(chat, author, outgoing, it, ev)
	case it.GetPaymentNotification() != nil:
		ev["payment"] = true
	case it.GetGiftBadge() != nil:
		ev["gift"] = true
	case it.GetViewOnceMessage() != nil:
		if at := it.GetViewOnceMessage().GetAttachment(); at.GetPointer() != nil {
			list = append(list, attach(at.GetPointer(), attachmentFlags(at.GetFlag()), false))
		}
		ev["view_once"] = true
	case it.GetDirectStoryReplyMessage() != nil:
		r := it.GetDirectStoryReplyMessage()
		if e := r.GetEmoji(); e != "" {
			ev["story_reaction"] = e
		} else {
			a.text(ev, r.GetTextReply().GetText())
			ev["story_reply"] = true
		}
	case it.GetPoll() != nil:
		p := it.GetPoll()
		opts := []string{}
		for _, o := range p.GetOptions() {
			opts = append(opts, o.GetOption())
		}
		ev["poll"] = M{"question": p.GetQuestion(), "options": opts, "multiple": p.GetAllowMultiple()}
	default:
		return x, false
	}
	if len(list) > 0 {
		ev["attachments"] = list
	}
	return x, hasContent(ev) || ev["story_reaction"] != nil || ev["view_once"] == true
}

func cmpU64(xs ...uint64) uint64 {
	for _, x := range xs {
		if x != 0 {
			return x
		}
	}
	return 0
}

// text puts a message's text and mentions into ev.
func (a *transferArchive) text(ev M, t *backuppb.Text) {
	if t == nil {
		return
	}
	ev["text"] = t.GetBody()
	var ms []M
	for _, r := range t.GetBodyRanges() {
		if who := aciOfBytes(r.GetMentionAci()); who != nil {
			ms = append(ms, M{"start": r.GetStart(), "length": r.GetLength(), "aci": who})
		}
	}
	if len(ms) > 0 {
		ev["mentions"] = ms
	}
}

// update is a chat update: a call (the phone's call log), a group's call or change, a timer's
// change, a poll's end, a pin; the rest (safety numbers, sessions, profile changes) is not kept.
func (a *transferArchive) update(chat M, author string, outgoing bool, it *backuppb.ChatItem, ev M) (converted, bool) {
	u := it.GetUpdateMessage()
	ts := it.GetDateSent()
	switch {
	case u.GetIndividualCall() != nil:
		call := u.GetIndividualCall()
		kind := "audio"
		if call.GetType() == backuppb.IndividualCall_VIDEO_CALL {
			kind = "video"
		}
		direction := "incoming"
		if call.GetDirection() == backuppb.IndividualCall_OUTGOING {
			direction = "outgoing"
		}
		result := "accepted"
		switch call.GetState() {
		case backuppb.IndividualCall_NOT_ACCEPTED, backuppb.IndividualCall_MISSED, backuppb.IndividualCall_MISSED_NOTIFICATION_PROFILE:
			result = "not_accepted"
		}
		id := "transfer:" + strconv.FormatUint(ts, 10)
		if call.CallId != nil {
			id = strconv.FormatUint(call.GetCallId(), 10)
		}
		started := cmpU64(call.GetStartedCallTimestamp(), ts)
		return plain("call", M{"source": "sync", "id": id, "ts": started, "type": kind, "direction": direction,
			"result": result, "chat": chat}), true
	case u.GetGroupCall() != nil:
		ev["group_call"] = true
	case u.GetGroupChange() != nil:
		ev["group_change"] = true
		if changes, ok := a.groupChanges(u.GetGroupChange()); ok {
			ev["group_changes"] = changes
		}
	case u.GetExpirationTimerChange() != nil:
		ev["expire_timer_update"], ev["expire_timer"] = true, u.GetExpirationTimerChange().GetExpiresInMs()/1000
	case u.GetPollTerminate() != nil:
		ev["poll_end"] = M{"target_ts": u.GetPollTerminate().GetTargetSentTimestamp()}
	case u.GetPinMessage() != nil:
		p := u.GetPinMessage()
		var target any
		if t := a.person(p.GetAuthorId()); t != "" {
			target = t
		}
		ev["pin"] = M{"target_author": target, "target_ts": p.GetTargetSentTimestamp(), "seconds": nil}
	default:
		return converted{}, false
	}
	return converted{name: "message", ev: ev}, true
}

// groupChanges reads a group's change of the archive as the events' actions (those Signal's apps show
// most; others leave the change without words): false for none read.
func (a *transferArchive) groupChanges(g *backuppb.GroupChangeChatUpdate) (M, bool) {
	var editor any
	by := func(b []byte) {
		if editor == nil {
			editor = aciOfBytes(b)
		}
	}
	actions := []M{}
	for _, u := range g.GetUpdates() {
		switch {
		case u.GetGroupNameUpdate() != nil:
			x := u.GetGroupNameUpdate()
			by(x.GetUpdaterAci())
			actions = append(actions, M{"type": "title", "title": x.GetNewGroupName()})
		case u.GetGroupDescriptionUpdate() != nil:
			x := u.GetGroupDescriptionUpdate()
			by(x.GetUpdaterAci())
			actions = append(actions, M{"type": "description", "text": x.GetNewDescription()})
		case u.GetGroupAvatarUpdate() != nil:
			x := u.GetGroupAvatarUpdate()
			by(x.GetUpdaterAci())
			if x.GetWasRemoved() {
				actions = append(actions, M{"type": "avatar", "removed": true})
			} else {
				actions = append(actions, M{"type": "avatar"})
			}
		case u.GetGroupMemberLeftUpdate() != nil:
			x := u.GetGroupMemberLeftUpdate()
			by(x.GetAci())
			actions = append(actions, M{"type": "left", "who": aciOfBytes(x.GetAci())})
		case u.GetGroupMemberRemovedUpdate() != nil:
			x := u.GetGroupMemberRemovedUpdate()
			by(x.GetRemoverAci())
			actions = append(actions, M{"type": "removed", "who": aciOfBytes(x.GetRemovedAci())})
		case u.GetGroupMemberAddedUpdate() != nil:
			x := u.GetGroupMemberAddedUpdate()
			by(x.GetUpdaterAci())
			actions = append(actions, M{"type": "added", "who": aciOfBytes(x.GetNewMemberAci())})
		case u.GetGroupMemberJoinedByLinkUpdate() != nil:
			x := u.GetGroupMemberJoinedByLinkUpdate()
			by(x.GetNewMemberAci())
			actions = append(actions, M{"type": "joined_link", "who": aciOfBytes(x.GetNewMemberAci())})
		case u.GetGroupMemberJoinedUpdate() != nil:
			x := u.GetGroupMemberJoinedUpdate()
			by(x.GetNewMemberAci())
			actions = append(actions, M{"type": "invite_accepted", "who": aciOfBytes(x.GetNewMemberAci())})
		case u.GetGroupInvitationAcceptedUpdate() != nil:
			x := u.GetGroupInvitationAcceptedUpdate()
			by(x.GetNewMemberAci())
			actions = append(actions, M{"type": "invite_accepted", "who": aciOfBytes(x.GetNewMemberAci())})
		case u.GetGroupAdminStatusUpdate() != nil:
			x := u.GetGroupAdminStatusUpdate()
			by(x.GetUpdaterAci())
			actions = append(actions, M{"type": "admin", "who": aciOfBytes(x.GetMemberAci()), "on": x.GetWasAdminStatusGranted()})
		case u.GetGroupExpirationTimerUpdate() != nil:
			x := u.GetGroupExpirationTimerUpdate()
			by(x.GetUpdaterAci())
			actions = append(actions, M{"type": "timer", "seconds": x.GetExpiresInMs() / 1000})
		case u.GetGroupInviteLinkResetUpdate() != nil:
			by(u.GetGroupInviteLinkResetUpdate().GetUpdaterAci())
			actions = append(actions, M{"type": "link_reset"})
		case u.GetGroupTerminateChangeUpdate() != nil:
			actions = append(actions, M{"type": "ended"})
		}
	}
	if len(actions) == 0 {
		return nil, false
	}
	return M{"editor": editor, "actions": actions}, true
}

// hashOf is the hash of the content of an event's i-th file, where that is its check (nil: none).
func hashOf(x converted, i int) []byte {
	if i < len(x.hashes) {
		return x.hashes[i]
	}
	return nil
}
