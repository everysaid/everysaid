//go:build signal

package signal

// What Signal sends, as the client's events (transport.go): plain JSON that the store and the
// importer read without knowing Signal's protobufs. Ported from the earlier helper's convert.rs
// (presage), field for field. Nothing here touches the network or the device's keys: a group's id
// and the reading of a group's change are given (conv), so it is tested on messages made by hand.

import (
	"encoding/base64"
	"mime"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
)

// meta is who sent a content (to whom), from which device, and when (Unix ms). The ids are as
// libsignalgo writes them: an ACI, or "PNI:<uuid>".
type meta struct {
	sender, destination string
	device              uint32
	ts, serverTS        uint64
}

// converted is an event, with the attachments still to fetch (in the order of its `attachments`
// list) and the master key of the group it is in (nil: none).
type converted struct {
	name  string
	ev    M
	atts  []*signalpb.AttachmentPointer
	group []byte
	// the hashes of the files' contents, where they are checked by those (the transfer archive's)
	// rather than by their digests
	hashes [][]byte
}

// longText is the type of the file that carries a long message's whole text (its body has the
// first 2 KiB).
const longText = "text/x-signal-plain"

// conv converts for an account (own, its ACI). groupID is a group's id from its master key ("" where
// it is not one); groupChanges reads a group's change ({editor, actions}; false where it cannot).
type conv struct {
	own          string
	groupID      func(masterKey []byte) string
	groupChanges func(group *signalpb.GroupContextV2) (M, bool)
}

// groupIDOf is a group's id as Signal's apps show it (base64 of the identifier derived from its
// master key); the master key itself, which lets one read the group, stays here.
func groupIDOf(masterKey []byte) string {
	if len(masterKey) != libsignalgo.GroupMasterKeyLength {
		return ""
	}
	gid, err := libsignalgo.GroupMasterKey(masterKey).GroupIdentifier()
	if err != nil {
		return ""
	}
	return gid.String()
}

func masterKey(g *signalpb.GroupContextV2) []byte {
	if k := g.GetMasterKey(); len(k) == libsignalgo.GroupMasterKeyLength {
		return k
	}
	return nil
}

// aciText is an ACI written as text, from the 16 bytes a message carries or its string (nil: none).
func aciText(text string, binary []byte) any {
	if len(binary) == 16 {
		return uuid.UUID(binary).String()
	}
	if text != "" {
		return strings.ToLower(text)
	}
	return nil
}

// serviceIDText is a service id (an ACI, or "PNI:<uuid>") as text, from its bytes or its string
// ("": none).
func serviceIDText(text string, binary []byte) string {
	if len(binary) > 0 {
		if s, err := libsignalgo.ServiceIDFromBytes(binary); err == nil {
			return s.String()
		}
	}
	if text == "" {
		return ""
	}
	if s, err := libsignalgo.ServiceIDFromString(text); err == nil {
		return s.String()
	}
	return text
}

func contactChat(id string) M { return M{"kind": "contact", "id": id} }

func (c conv) groupChat(key []byte) M { return M{"kind": "group", "id": c.groupID(key)} }

func plain(name string, ev M) converted { return converted{name: name, ev: ev} }

// idsEvent is what Signal told of one person's ids: a PNI (the id of a phone number) with the
// number, or with the ACI it belongs to; an ACI with its number. A chat with someone known only by
// number is kept under their PNI, and this is what ties it to the person.
func idsEvent(aci, pni, phone any) converted {
	return plain("ids", M{"aci": aci, "pni": pni, "phone": phone})
}

func optU64(p *uint64) any {
	if p == nil {
		return nil
	}
	return *p
}

func optU32(p *uint32) any {
	if p == nil {
		return nil
	}
	return *p
}

func optStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// convert is the events of one decrypted content.
func (c conv) convert(m meta, content *signalpb.Content) []converted {
	// a person's chat is the other person's: the sender's, or (what this device sent) the destination's
	peer := m.sender
	if m.sender == c.own && m.destination != "" {
		peer = m.destination
	}
	outgoing := m.sender == c.own
	switch {
	case content.GetDataMessage() != nil:
		dm := content.GetDataMessage()
		chat, group := contactChat(peer), masterKey(dm.GetGroupV2())
		if group != nil {
			chat = c.groupChat(group)
		}
		// a message names itself by the time it was sent, which its envelope says too: one that
		// says another is not taken (Signal Desktop: "Timestamp mismatch")
		if !outgoing && dm.Timestamp != nil && dm.GetTimestamp() != m.ts {
			return nil
		}
		ts := m.ts
		if dm.Timestamp != nil {
			ts = dm.GetTimestamp()
		}
		return c.dataEvents(m, dm, m.sender, outgoing, chat, group, ts)
	case content.GetEditMessage() != nil:
		em := content.GetEditMessage()
		dm := em.GetDataMessage()
		if em.TargetSentTimestamp == nil || dm == nil {
			return nil
		}
		chat, group := contactChat(peer), masterKey(dm.GetGroupV2())
		if group != nil {
			chat = c.groupChat(group)
		}
		// as a message, the edit's new content names itself by its envelope's time
		if !outgoing && dm.Timestamp != nil && dm.GetTimestamp() != m.ts {
			return nil
		}
		return c.editEvent(m, dm, m.sender, outgoing, chat, group, em.GetTargetSentTimestamp())
	case content.GetSyncMessage() != nil:
		// only from the owner's own account: from anyone else it would forge what the owner sent
		if m.sender != c.own {
			return nil
		}
		return c.syncEvents(m, content.GetSyncMessage())
	case content.GetCallMessage() != nil:
		return c.callEvents(m, content.GetCallMessage())
	case content.GetReceiptMessage() != nil:
		return receiptEvent(m, content.GetReceiptMessage())
	case content.GetDecryptionErrorMessage() != nil:
		// someone could not read a message sent from here
		return []converted{plain("decryption_error", M{"sender": m.sender})}
	}
	// typing, stories, null messages (padding, or a session reset): nothing to keep
	return nil
}

func base(m meta, sender string, outgoing bool, chat M, ts uint64) M {
	return M{"chat": chat, "sender": sender, "sender_device": m.device, "outgoing": outgoing, "ts": ts, "server_ts": m.serverTS}
}

func (c conv) dataEvents(m meta, dm *signalpb.DataMessage, sender string, outgoing bool, chat M, group []byte, ts uint64) []converted {
	ev := base(m, sender, outgoing, chat, ts)
	// the group's revision the sender saw, on every event of a group (deliver checks the sender was
	// a member against the group at that revision)
	if r := dm.GetGroupV2(); r != nil && r.Revision != nil {
		ev["group_revision"] = r.GetRevision()
	}
	one := func(name string) []converted { return []converted{{name: name, ev: ev, group: group}} }
	// a reaction to a story is a message of its own (the story is not kept)
	if r := dm.GetReaction(); r != nil && dm.GetStoryContext() != nil {
		if r.GetRemove() {
			return nil
		}
		ev["story_reaction"] = optStr(r.Emoji)
		return one("message")
	}
	if v := dm.GetPollVote(); v != nil {
		opts := []uint32{}
		opts = append(opts, v.GetOptionIndexes()...)
		ev["target_author"], ev["target_ts"] = aciText("", v.GetTargetAuthorAciBinary()), optU64(v.TargetSentTimestamp)
		ev["options"], ev["vote_count"] = opts, v.GetVoteCount()
		return one("poll_vote")
	}
	// made by a version of Signal newer than this one knows: kept as such (Signal Desktop does too)
	if dm.RequiredProtocolVersion != nil && dm.GetRequiredProtocolVersion() > uint32(signalpb.DataMessage_CURRENT) {
		ev["unsupported"] = true
		return one("message")
	}
	if r := dm.GetReaction(); r != nil {
		ev["emoji"], ev["remove"] = optStr(r.Emoji), r.GetRemove()
		ev["target_author"], ev["target_ts"] = aciText(r.GetTargetAuthorAci(), r.GetTargetAuthorAciBinary()), optU64(r.TargetSentTimestamp)
		return one("reaction")
	}
	if d := dm.GetDelete(); d != nil && d.TargetSentTimestamp != nil {
		ev["target_author"], ev["target_ts"] = sender, d.GetTargetSentTimestamp()
		return one("delete")
	}
	if d := dm.GetAdminDelete(); d != nil {
		ev["target_author"], ev["target_ts"] = aciText("", d.GetTargetAuthorAciBinary()), optU64(d.TargetSentTimestamp)
		ev["admin"] = true // honoured only from a group's admin (deliver)
		return one("delete")
	}
	atts := c.messageFields(ev, dm)
	if !hasContent(ev) {
		return nil // a profile key, a timer's version: nothing to show
	}
	return []converted{{name: "message", ev: ev, atts: atts, group: group}}
}

func (c conv) editEvent(m meta, dm *signalpb.DataMessage, sender string, outgoing bool, chat M, group []byte, target uint64) []converted {
	ts := m.ts
	if dm.Timestamp != nil {
		ts = dm.GetTimestamp()
	}
	ev := base(m, sender, outgoing, chat, ts)
	ev["target_ts"] = target
	atts := c.messageFields(ev, dm)
	return []converted{{name: "edit", ev: ev, atts: atts, group: group}}
}

// hasContent: a message event carries something beyond who and when.
func hasContent(ev M) bool {
	for _, f := range []string{"text", "attachments", "contacts", "poll", "group_change", "group_call", "expire_timer_update",
		"pin", "unpin", "payment", "gift", "quote", "poll_end"} {
		switch v := ev[f].(type) {
		case nil:
		case bool:
			if v {
				return true
			}
		case string:
			if v != "" {
				return true
			}
		case []M:
			if len(v) > 0 {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func mentions(ranges []*signalpb.BodyRange) []M {
	var out []M
	for _, r := range ranges {
		who := aciText(r.GetMentionAci(), r.GetMentionAciBinary())
		if who == nil {
			continue
		}
		out = append(out, M{"start": r.GetStart(), "length": r.GetLength(), "aci": who})
	}
	return out
}

// pointerText is where Signal keeps a file and its key (its AttachmentPointer), as text: what
// fetches it again, and keeps it in an edit of the message.
func pointerText(a *signalpb.AttachmentPointer) string {
	b, err := proto.Marshal(a)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// pointerOf is the AttachmentPointer pointerText wrote (nil: none).
func pointerOf(text string) *signalpb.AttachmentPointer {
	b, err := base64.StdEncoding.DecodeString(text)
	if text == "" || err != nil {
		return nil
	}
	var a signalpb.AttachmentPointer
	if proto.Unmarshal(b, &a) != nil {
		return nil
	}
	return &a
}

// attachmentJSON is an attachment as the events have it (the file is filled in once fetched).
func attachmentJSON(a *signalpb.AttachmentPointer, sticker bool) M {
	flags := a.GetFlags()
	return M{"content_type": optStr(a.ContentType), "filename": optStr(a.FileName), "size": optU32(a.Size),
		"width": optU32(a.Width), "height": optU32(a.Height), "caption": optStr(a.Caption),
		"voice":      flags&uint32(signalpb.AttachmentPointer_VOICE_MESSAGE) != 0,
		"borderless": flags&uint32(signalpb.AttachmentPointer_BORDERLESS) != 0,
		"gif":        flags&uint32(signalpb.AttachmentPointer_GIF) != 0,
		"sticker":    sticker, "file": nil, "pointer": pointerText(a)}
}

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// messageFields puts the fields of a message (text, mentions, quote, attachments, ...) into ev; it
// returns the attachments to fetch.
func (c conv) messageFields(ev M, dm *signalpb.DataMessage) []*signalpb.AttachmentPointer {
	if dm.Body != nil {
		ev["text"] = dm.GetBody()
	}
	if ms := mentions(dm.GetBodyRanges()); len(ms) > 0 {
		ev["mentions"] = ms
	}
	if q := dm.GetQuote(); q != nil {
		quoted := []M{}
		for _, a := range q.GetAttachments() { // what the quoted message carried (a reply to a picture quotes no text)
			quoted = append(quoted, M{"content_type": optStr(a.ContentType), "filename": optStr(a.FileName)})
		}
		ev["quote"] = M{"ts": optU64(q.Id), "author": aciText(q.GetAuthorAci(), q.GetAuthorAciBinary()), "text": optStr(q.Text),
			"attachments": quoted}
	}
	pointers := append([]*signalpb.AttachmentPointer{}, dm.GetAttachments()...)
	list := []M{}
	for _, a := range dm.GetAttachments() {
		list = append(list, attachmentJSON(a, false))
	}
	if s := dm.GetSticker(); s != nil && s.GetData() != nil {
		a := attachmentJSON(s.GetData(), true)
		a["emoji"] = optStr(s.Emoji)
		list = append(list, a)
		pointers = append(pointers, s.GetData())
	}
	if len(list) > 0 {
		ev["attachments"] = list
	}
	if cs := dm.GetContact(); len(cs) > 0 {
		shared := []M{}
		for _, x := range cs {
			var name any
			if n := x.GetName(); n != nil {
				if full := joinNonEmpty(n.GetPrefix(), n.GetGivenName(), n.GetMiddleName(), n.GetFamilyName(), n.GetSuffix()); full != "" {
					name = full
				} else if n.Nickname != nil {
					name = n.GetNickname()
				}
			}
			phones, emails := []string{}, []string{}
			for _, p := range x.GetNumber() {
				if p.Value != nil {
					phones = append(phones, p.GetValue())
				}
			}
			for _, e := range x.GetEmail() {
				if e.Value != nil {
					emails = append(emails, e.GetValue())
				}
			}
			shared = append(shared, M{"name": name, "phones": phones, "emails": emails, "organization": optStr(x.Organization)})
		}
		ev["contacts"] = shared
	}
	if ps := dm.GetPreview(); len(ps) > 0 {
		previews := []M{}
		for _, p := range ps {
			previews = append(previews, M{"url": optStr(p.Url), "title": optStr(p.Title)})
		}
		ev["previews"] = previews
	}
	if p := dm.GetPollCreate(); p != nil {
		opts := append([]string{}, p.GetOptions()...)
		ev["poll"] = M{"question": optStr(p.Question), "options": opts, "multiple": p.GetAllowMultiple()}
	}
	if g := dm.GetGroupV2(); g != nil && g.GroupChange != nil {
		changes, ok := M(nil), false
		if c.groupChanges != nil {
			changes, ok = c.groupChanges(g)
		}
		switch {
		case ok && len(changes["actions"].([]M)) == 0:
			// only what the apps do not show (a new profile key, labels): no change to tell
		case ok:
			ev["group_change"], ev["group_changes"] = true, changes
		default:
			ev["group_change"] = true // a change that could not be read
		}
	}
	if g := dm.GetGroupV2(); g != nil && g.Revision != nil {
		ev["group_revision"] = g.GetRevision()
	}
	if dm.GetGroupCallUpdate() != nil {
		ev["group_call"] = true
	}
	flags := dm.GetFlags()
	// the timer a message carries; with its update, also when it was turned off (0)
	if flags&uint32(signalpb.DataMessage_EXPIRATION_TIMER_UPDATE) != 0 {
		ev["expire_timer_update"], ev["expire_timer"] = true, dm.GetExpireTimer()
	} else if t := dm.GetExpireTimer(); t > 0 {
		ev["expire_timer"] = t
	}
	if flags&uint32(signalpb.DataMessage_FORWARD) != 0 {
		ev["forwarded"] = true
	}
	if dm.GetIsViewOnce() {
		ev["view_once"] = true
	}
	if p := dm.GetPinMessage(); p != nil {
		var seconds any // for how long (nil: for good)
		if d, ok := p.GetPinDuration().(*signalpb.DataMessage_PinMessage_PinDurationSeconds); ok {
			seconds = d.PinDurationSeconds
		}
		ev["pin"] = M{"target_author": aciText("", p.GetTargetAuthorAciBinary()), "target_ts": optU64(p.TargetSentTimestamp),
			"seconds": seconds}
	}
	if t := dm.GetPollTerminate(); t != nil {
		ev["poll_end"] = M{"target_ts": optU64(t.TargetSentTimestamp)}
	}
	if p := dm.GetUnpinMessage(); p != nil {
		ev["unpin"] = M{"target_author": aciText("", p.GetTargetAuthorAciBinary()), "target_ts": optU64(p.TargetSentTimestamp)}
	}
	if dm.GetPayment() != nil {
		ev["payment"] = true
	}
	if dm.GetGiftBadge() != nil {
		ev["gift"] = true
	}
	if dm.GetStoryContext() != nil {
		ev["story_reply"] = true
	}
	return pointers
}

func (c conv) syncEvents(m meta, sm *signalpb.SyncMessage) []converted {
	var out []converted
	if sent := sm.GetSent(); sent != nil {
		dest := serviceIDText(sent.GetDestinationServiceId(), sent.GetDestinationServiceIdBinary())
		// the phone says the number it wrote to: for someone it knows only by number, the
		// destination is their PNI, and the number is all that ties that chat to them
		if phone := strings.TrimSpace(sent.GetDestinationE164()); dest != "" && phone != "" {
			if strings.HasPrefix(dest, "PNI:") {
				out = append(out, idsEvent(nil, dest, phone))
			} else if dest != c.own {
				out = append(out, idsEvent(dest, nil, phone))
			}
		}
		if dm := sent.GetMessage(); dm != nil {
			ts := m.ts
			if dm.Timestamp != nil {
				ts = dm.GetTimestamp()
			} else if sent.Timestamp != nil {
				ts = sent.GetTimestamp()
			}
			if k := masterKey(dm.GetGroupV2()); k != nil {
				out = append(out, c.dataEvents(m, dm, c.own, true, c.groupChat(k), k, ts)...)
			} else if dest != "" {
				out = append(out, c.dataEvents(m, dm, c.own, true, contactChat(dest), nil, ts)...)
			}
		}
		if em := sent.GetEditMessage(); em != nil && em.TargetSentTimestamp != nil && em.GetDataMessage() != nil {
			dm := em.GetDataMessage()
			if k := masterKey(dm.GetGroupV2()); k != nil {
				out = append(out, c.editEvent(m, dm, c.own, true, c.groupChat(k), k, em.GetTargetSentTimestamp())...)
			} else if dest != "" {
				out = append(out, c.editEvent(m, dm, c.own, true, contactChat(dest), nil, em.GetTargetSentTimestamp())...)
			}
		}
	}
	if reads := sm.GetRead(); len(reads) > 0 {
		items := []M{}
		for _, r := range reads {
			items = append(items, M{"author": aciText(r.GetSenderAci(), r.GetSenderAciBinary()), "ts": optU64(r.Timestamp)})
		}
		out = append(out, plain("read", M{"messages": items, "ts": m.ts}))
	}
	if ce := sm.GetCallEvent(); ce != nil {
		kind := map[signalpb.SyncMessage_CallEvent_Type]string{signalpb.SyncMessage_CallEvent_AUDIO_CALL: "audio",
			signalpb.SyncMessage_CallEvent_VIDEO_CALL: "video", signalpb.SyncMessage_CallEvent_GROUP_CALL: "group",
			signalpb.SyncMessage_CallEvent_AD_HOC_CALL: "ad_hoc"}[ce.GetType()]
		if kind == "" {
			kind = "unknown"
		}
		var chat any
		if id := ce.GetConversationId(); len(id) > 0 {
			if kind == "group" {
				chat = M{"kind": "group", "id": base64.StdEncoding.EncodeToString(id)}
			} else if s, err := libsignalgo.ServiceIDFromBytes(id); err == nil {
				chat = contactChat(s.String())
			}
		}
		result := map[signalpb.SyncMessage_CallEvent_Event]string{signalpb.SyncMessage_CallEvent_ACCEPTED: "accepted",
			signalpb.SyncMessage_CallEvent_NOT_ACCEPTED: "not_accepted", signalpb.SyncMessage_CallEvent_DELETE: "delete",
			signalpb.SyncMessage_CallEvent_OBSERVED: "observed"}[ce.GetEvent()]
		if result == "" {
			result = "unknown"
		}
		direction := map[signalpb.SyncMessage_CallEvent_Direction]string{signalpb.SyncMessage_CallEvent_INCOMING: "incoming",
			signalpb.SyncMessage_CallEvent_OUTGOING: "outgoing"}[ce.GetDirection()]
		if direction == "" {
			direction = "unknown"
		}
		var id any
		if ce.CallId != nil {
			id = strconv.FormatUint(ce.GetCallId(), 10)
		}
		ts := m.ts
		if ce.Timestamp != nil {
			ts = ce.GetTimestamp()
		}
		out = append(out, plain("call", M{"source": "sync", "id": id, "ts": ts, "type": kind, "direction": direction,
			"result": result, "chat": chat}))
	}
	return out
}

func (c conv) callEvents(m meta, cm *signalpb.CallMessage) []converted {
	var out []converted
	push := func(id *uint64, action string, extra M) {
		ev := base(m, m.sender, m.sender == c.own, contactChat(m.sender), m.ts)
		ev["source"], ev["action"] = "message", action
		ev["id"] = nil
		if id != nil {
			ev["id"] = strconv.FormatUint(*id, 10)
		}
		for k, v := range extra {
			ev[k] = v
		}
		out = append(out, plain("call", ev))
	}
	if o := cm.GetOffer(); o != nil {
		push(o.Id, "offer", M{"video": o.GetType() == signalpb.CallMessage_Offer_OFFER_VIDEO_CALL})
	}
	if a := cm.GetAnswer(); a != nil {
		push(a.Id, "answer", nil)
	}
	if b := cm.GetBusy(); b != nil {
		push(b.Id, "busy", nil)
	}
	if h := cm.GetHangup(); h != nil {
		how := map[signalpb.CallMessage_Hangup_Type]string{signalpb.CallMessage_Hangup_HANGUP_NORMAL: "normal",
			signalpb.CallMessage_Hangup_HANGUP_ACCEPTED: "accepted", signalpb.CallMessage_Hangup_HANGUP_DECLINED: "declined",
			signalpb.CallMessage_Hangup_HANGUP_BUSY: "busy", signalpb.CallMessage_Hangup_HANGUP_NEED_PERMISSION: "need_permission"}[h.GetType()]
		push(h.Id, "hangup", M{"hangup": how})
	}
	return out
}

func receiptEvent(m meta, rm *signalpb.ReceiptMessage) []converted {
	kind := map[signalpb.ReceiptMessage_Type]string{signalpb.ReceiptMessage_DELIVERY: "delivery",
		signalpb.ReceiptMessage_READ: "read", signalpb.ReceiptMessage_VIEWED: "viewed"}[rm.GetType()]
	stamps := append([]uint64{}, rm.GetTimestamp()...)
	return []converted{plain("receipt", M{"sender": m.sender, "kind": kind, "timestamps": stamps, "ts": m.ts})}
}

// sentEvent is a message this client sent, as the event the others' messages are.
func (c conv) sentEvent(device uint32, chat M, dm *signalpb.DataMessage, ts uint64) converted {
	m := meta{sender: c.own, device: device, ts: ts, serverTS: ts}
	ev := base(m, c.own, true, chat, ts)
	atts := c.messageFields(ev, dm)
	return converted{name: "message", ev: ev, atts: atts}
}

// sentEvents is what this client sent (a reaction, an edit, a deletion), as the events the others'
// are: to destination (a person's id; for a group, its context in the message says where).
func (c conv) sentEvents(device uint32, destination string, content *signalpb.Content, ts uint64) []converted {
	return c.convert(meta{sender: c.own, destination: destination, device: device, ts: ts, serverTS: ts}, content)
}

// edited is an edit's message: an edit replaces the whole message, so the old one's files, quote
// and timer go with it; its text is new (the mentions and link previews were of the old text).
func edited(old *signalpb.DataMessage, text string, ts uint64) *signalpb.DataMessage {
	dm := &signalpb.DataMessage{}
	if old != nil {
		dm.Attachments, dm.Quote = old.Attachments, old.Quote
		dm.ExpireTimer, dm.ExpireTimerVersion = old.ExpireTimer, old.ExpireTimerVersion
	}
	if text != "" {
		dm.Body = proto.String(text)
	}
	dm.Timestamp = proto.Uint64(ts)
	return dm
}

// storedMessage is the message a stored event was (its files, by their pointers; its quote; its
// timer): what an edit of it keeps.
func storedMessage(e event) *signalpb.DataMessage {
	dm := &signalpb.DataMessage{}
	for _, a := range e.Attachments {
		if a.Sticker || strings.EqualFold(deref(a.ContentType), longText) {
			continue
		}
		if p := pointerOf(a.Pointer); p != nil {
			dm.Attachments = append(dm.Attachments, p)
		}
	}
	if q := e.Quote; q != nil && q.Author != nil {
		dm.Quote = &signalpb.DataMessage_Quote{Id: proto.Uint64(uint64(q.TS)), AuthorAci: q.Author, Text: q.Text,
			Type: signalpb.DataMessage_Quote_NORMAL.Enum()}
	}
	if e.ExpireTimer > 0 {
		dm.ExpireTimer = proto.Uint32(uint32(e.ExpireTimer))
	}
	return dm
}

// adminDeleteAllowed: a group's admin's deletion is kept of the sender's own message always; of
// another's only when the sender is one of the group's admins (as known here; not known: no).
func adminDeleteAllowed(sender, targetAuthor string, admins []string, known bool) bool {
	if sender == targetAuthor {
		return true
	}
	if !known {
		return false
	}
	for _, a := range admins {
		if a == sender {
			return true
		}
	}
	return false
}

// attachmentName is the file name an attachment of a message is saved under (unique by author,
// time and place).
func attachmentName(ts uint64, author string, index int, contentType, filename string) string {
	ext := ""
	if i := strings.LastIndexByte(filename, '.'); i >= 0 {
		e := strings.ToLower(filename[i+1:])
		ok := e != "" && len(e) <= 8
		for _, r := range e {
			ok = ok && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
		}
		if ok {
			ext = e
		}
	}
	if ext == "" && contentType != "" {
		ext = map[string]string{"image/jpeg": "jpg", "audio/aac": "aac", "audio/mp4": "m4a", "video/mp4": "mp4",
			"image/webp": "webp", longText: "txt"}[contentType]
		if ext == "" {
			if exts, _ := mime.ExtensionsByType(contentType); len(exts) > 0 {
				ext = strings.TrimPrefix(exts[0], ".")
			}
		}
	}
	var short strings.Builder
	for _, r := range author {
		if short.Len() == 8 {
			break
		}
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			short.WriteRune(r)
		}
	}
	name := strconv.FormatUint(ts, 10) + "-" + short.String() + "-" + strconv.Itoa(index)
	if ext != "" {
		name += "." + ext
	}
	return name
}
