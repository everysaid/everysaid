// Ports everysaid/telegram.py: Telegram from <cache>/telegram/telegram.db, which the sync fills
// through the Telegram API (each message whole, as JSON).
//
// Every chat is imported but channels and bots, which the sync does not read. Message ids are
// unique only within a chat (the service's key_scope), so a message's key is its id and its row key
// in the source <chat>/<id>. People are stored by phone number where Telegram shows it (so they meet
// the same person on other services), else by their Telegram user id; their other handles join the
// same person: the user id, the username and the name their profile shows (what the owner needs to
// tell who is who, and to join them to their contacts later). The owner's own id is an account.
// Calls, which Telegram keeps as service messages, go to `call` too, keyed by the call's id. Whom
// texts name (`@username`, or a name linked to the user) goes to `mention`; a group's members are
// those Telegram gave when last asked (chat_member) and whoever wrote there; how far each chat was read (chat_read) becomes its read_until and, in a chat
// with one person, receipts of the owner's messages.
package importers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/telegramstore"
	"everysaid/internal/text"
)

const telegramSource = "telegram"

// Service actions: those our vocabulary names; the rest are system messages with their code only.
var telegramActions = map[string]string{"MessageActionPinMessage": "pin", "MessageActionPhoneCall": "call",
	"MessageActionGroupCall": "call", "MessageActionChatCreate": "group event", "MessageActionChatAddUser": "group event",
	"MessageActionChatDeleteUser": "group event", "MessageActionChatJoinedByLink": "group event",
	"MessageActionChatJoinedByRequest": "group event", "MessageActionChatEditTitle": "group event",
	"MessageActionChatEditPhoto": "group event", "MessageActionChatDeletePhoto": "group event",
	"MessageActionChatMigrateTo": "group event", "MessageActionChannelMigrateFrom": "group event"}

var telegramCallDetail = map[string]string{"PhoneCallDiscardReasonBusy": "busy", "PhoneCallDiscardReasonDisconnect": "failed"}

// parseJSON is a JSON text as Python's json.loads gives it (numbers kept as written).
func parseJSON(s string) (map[string]any, error) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// peerID is the marked id of a Peer object: users as they are, groups negative, -100...
// supergroups; false for none.
func peerID(peer any) (int64, bool) {
	p, ok := peer.(map[string]any)
	if !ok || len(p) == 0 {
		return 0, false
	}
	if v, ok := p["user_id"]; ok {
		return toInt(v), true
	}
	if v, ok := p["chat_id"]; ok {
		return -toInt(v), true
	}
	return -(1_000_000_000_000 + toInt(p["channel_id"])), true
}

// textOf: newer layers send some texts (poll questions and answers) as TextWithEntities.
func textOf(v any) any {
	if m, ok := v.(map[string]any); ok {
		return m["text"]
	}
	return v
}

func joinTruthy(sep string, vs ...any) string {
	var parts []string
	for _, v := range vs {
		if truthy(v) {
			parts = append(parts, pyStr(v))
		}
	}
	return strings.Join(parts, sep)
}

// tgKind is what kindOf tells of a message.
type tgKind struct {
	kind, subtype, code string
	text                any // nil for none
	lat, lon            any
	place               string
}

func telegramKindOf(m map[string]any) tgKind {
	if pyStr(m["_"]) == "MessageService" {
		action := obj(m["action"])
		name := pyStr(action["_"])
		return tgKind{kind: "system", subtype: telegramActions[name], code: "telegram:" + name, text: action["title"]}
	}
	media := obj(m["media"])
	name, _ := media["_"].(string)
	if name == "" {
		return tgKind{kind: "text"}
	}
	code := "telegram:" + name
	switch name {
	case "MessageMediaWebPage":
		return tgKind{kind: "text", subtype: "link", code: code}
	case "MessageMediaPhoto":
		return tgKind{kind: "image"}
	case "MessageMediaGeo", "MessageMediaGeoLive", "MessageMediaVenue":
		geo := obj(media["geo"])
		return tgKind{kind: "location", lat: geo["lat"], lon: geo["long"], place: joinTruthy(", ", media["title"], media["address"])}
	case "MessageMediaContact":
		who := joinTruthy(" ", media["first_name"], media["last_name"], media["phone_number"])
		k := tgKind{kind: "contact"}
		if who != "" {
			k.text = who
		}
		return k
	case "MessageMediaPoll":
		poll := obj(media["poll"])
		lines := []any{textOf(poll["question"])}
		for _, a := range list(poll["answers"]) {
			lines = append(lines, "- "+pyStr(textOf(obj(a)["text"])))
		}
		k := tgKind{kind: "text", subtype: "poll", code: code}
		if t := joinTruthy("\n", lines...); t != "" {
			k.text = t
		}
		return k
	}
	if name != "MessageMediaDocument" || !truthy(media["document"]) {
		return tgKind{kind: "file", code: code}
	}
	doc := obj(media["document"])
	attrs := map[string]map[string]any{}
	for _, a := range list(doc["attributes"]) {
		am := obj(a)
		attrs[pyStr(am["_"])] = am
	}
	mime := ""
	if truthy(doc["mime_type"]) {
		mime = pyStr(doc["mime_type"])
	}
	if _, ok := attrs["DocumentAttributeSticker"]; ok {
		return tgKind{kind: "sticker"}
	}
	if _, ok := attrs["DocumentAttributeAnimated"]; ok {
		return tgKind{kind: "image", subtype: "gif", code: code}
	}
	video, isVideo := attrs["DocumentAttributeVideo"]
	if isVideo && truthy(video["round_message"]) {
		return tgKind{kind: "video", subtype: "video note", code: code}
	}
	if (isVideo && truthy(video)) || strings.HasPrefix(mime, "video/") {
		return tgKind{kind: "video"}
	}
	if audio, ok := attrs["DocumentAttributeAudio"]; ok && truthy(audio["voice"]) {
		return tgKind{kind: "voice"}
	}
	if strings.HasPrefix(mime, "image/") {
		return tgKind{kind: "image"}
	}
	return tgKind{kind: "file"}
}

// tgPeople maps a Telegram peer id to a handle.
type tgPeople struct {
	entity   map[int64]map[string]any
	order    []int64
	me       int64
	hasMe    bool
	username map[string]int64 // lower-case username -> user id, for "@username" in texts
}

func newTGPeople(d *sql.DB) *tgPeople {
	p := &tgPeople{entity: map[int64]map[string]any{}, username: map[string]int64{}}
	db.Each(d, "SELECT id, json FROM entity UNION ALL SELECT id, json FROM chat", nil, func(scan func(...any)) {
		var id int64
		var js string
		scan(&id, &js)
		e, err := parseJSON(js)
		if err != nil {
			panic(err)
		}
		if _, ok := p.entity[id]; !ok {
			p.order = append(p.order, id)
		}
		p.entity[id] = e
	})
	for _, id := range p.order {
		if truthy(p.entity[id]["is_self"]) {
			p.me, p.hasMe = id, true
			break
		}
	}
	for _, id := range p.order {
		e := p.entity[id]
		if pyStr(e["_"]) != "User" {
			continue
		}
		us := []any{e["username"]}
		for _, x := range list(e["usernames"]) {
			us = append(us, obj(x)["username"])
		}
		for _, u := range us {
			if truthy(u) {
				k := text.Lower(pyStr(u))
				if _, ok := p.username[k]; !ok {
					p.username[k] = id
				}
			}
		}
	}
	return p
}

// of is the handle of a peer id; ok false is Python's None (its "None" id, as the Python made it).
func (p *tgPeople) of(pid int64, ok bool) archive.Handle {
	if !ok {
		return archive.H("id", "None", "telegram")
	}
	e := p.entity[pid]
	if truthy(e["phone"]) {
		return address("+" + strings.TrimLeft(pyStr(e["phone"]), "+"))
	}
	return archive.H("id", fmt.Sprint(pid), "telegram")
}

// others are a user's handles besides the one `of` gives: id, username.
func (p *tgPeople) others(pid int64) []archive.Handle {
	e := p.entity[pid]
	if pyStr(e["_"]) != "User" {
		return nil
	}
	var out []archive.Handle
	if truthy(e["phone"]) {
		out = append(out, archive.H("id", fmt.Sprint(pid), "telegram"))
	}
	if u := e["username"]; truthy(u) {
		out = append(out, archive.H("username", text.Lower(pyStr(u)), "telegram"))
	}
	return out
}

// name is the user's profile name, "" for none.
func (p *tgPeople) name(pid int64) string {
	e := p.entity[pid]
	if pyStr(e["_"]) != "User" {
		return ""
	}
	return joinTruthy(" ", e["first_name"], e["last_name"])
}

func tgEmoji(x any) (string, string) {
	xm := obj(x)
	switch pyStr(xm["_"]) {
	case "ReactionEmoji":
		return strOrEmpty(xm["emoticon"]), ""
	case "ReactionCustomEmoji":
		return "", "telegram:custom:" + pyStr(xm["document_id"])
	}
	_, has := xm["_"]
	if !has {
		return "", "telegram:None"
	}
	return "", "telegram:" + pyStr(xm["_"])
}

func tgReactions(m map[string]any, person *tgPeople, own map[archive.Handle]bool) []archive.Reaction {
	r := obj(m["reactions"])
	results := list(r["results"])
	recent := list(r["recent_reactions"])
	var sum int64
	for _, x := range results {
		if c, ok := obj(x)["count"]; ok {
			sum += toInt(c)
		}
	}
	var out []archive.Reaction
	if len(recent) > 0 && int64(len(recent)) == sum { // everyone who reacted, by name
		for _, x := range recent {
			xm := obj(x)
			// no peer: not known who (the Python made an address "None" of it)
			pid, known := peerID(xm["peer_id"])
			who := person.of(pid, known)
			mine := truthy(xm["my"]) || (known && own[who])
			e, c := tgEmoji(xm["reaction"])
			rr := archive.Reaction{Emoji: e, Code: c, Count: 1, Outgoing: mine}
			if !mine && known {
				rr.Who = who
			}
			out = append(out, rr)
		}
		return out
	}
	for _, x := range results {
		xm := obj(x)
		e, c := tgEmoji(xm["reaction"])
		count := int64(1)
		if v, ok := xm["count"]; ok {
			count = toInt(v)
		}
		chosen, has := xm["chosen_order"]
		out = append(out, archive.Reaction{Emoji: e, Code: c, Count: int(count), Outgoing: has && chosen != nil})
	}
	return out
}

type tgMention struct {
	who   archive.Handle
	token string
}

// tgMentions: whom the text names, as "@username" or by their name (an entity Telegram links to
// the user); the token as the text has it (entities count UTF-16 units).
func tgMentions(m map[string]any, person *tgPeople) []tgMention {
	var out []tgMention
	var units []uint16
	for _, e := range list(m["entities"]) {
		em := obj(e)
		kind := pyStr(em["_"])
		if kind != "MessageEntityMention" && kind != "MessageEntityMentionName" {
			continue
		}
		if len(units) == 0 {
			units = utf16.Encode([]rune(str(m["message"])))
		}
		off, length := int(toInt(em["offset"])), int(toInt(em["length"]))
		start, end := max(0, min(off, len(units))), max(0, min(off+length, len(units)))
		token := ""
		if start < end {
			token = utf16Decode(units[start:end])
		}
		var pid int64
		var ok bool
		if kind == "MessageEntityMention" {
			pid, ok = person.username[text.Lower(strings.TrimLeft(token, "@"))]
		} else if v, has := em["user_id"]; has && truthy(v) {
			pid, ok = toInt(v), true
		}
		if ok && pid != 0 {
			out = append(out, tgMention{person.of(pid, true), token})
		}
	}
	return out
}

// TelegramReads: how far each chat was read (telegram.db's chat_read): the owner's reading as the
// chat's read_until; the other's, in a chat with one person, as receipts of the owner's messages
// (when, where the live connection saw it happen; else 0, known but not when). chats: only these
// (nil: all).
func TelegramReads(a *archive.Archive, out func(string), dbPath string, chats map[int64]bool) (err error) {
	defer archive.Recover(&err)
	telegramReads(a, dbPath, chats)
	return nil
}

func telegramReads(a *archive.Archive, dbPath string, chats map[int64]bool) {
	if dbPath == "" {
		dbPath = telegramstore.DB()
	}
	if !exists(dbPath) {
		return
	}
	d := ro(dbPath)
	defer d.Close()
	if !db.Exists(d, "SELECT 1 FROM sqlite_master WHERE name = 'chat_read'") {
		return
	}
	person := newTGPeople(d)
	src := a.Source(telegramSource, dbPath, "telegram", telegramstore.Media())
	for _, r := range maps(d, "SELECT r.chat_id, c.kind, r.inbox, r.outbox, r.outbox_at, r.observed_at FROM chat_read r "+
		"JOIN chat c ON c.id = r.chat_id") {
		chatID := toInt(r["chat_id"])
		if chats != nil && !chats[chatID] {
			continue
		}
		conv, ok := a.FindConversation("telegram", fmt.Sprint(chatID))
		if !ok {
			continue
		}
		if inbox := r["inbox"]; truthy(inbox) {
			var ts sql.NullInt64
			if mid, ok := a.MessageByKey("telegram", pyStr(inbox), conv); ok && mid != 0 {
				a.Row("SELECT ts FROM message WHERE id = ?", []any{mid}, &ts)
			} else {
				a.Row("SELECT max(ts) FROM message WHERE conversation_id = ? AND CAST(key AS INTEGER) <= ?", []any{conv, inbox}, &ts)
			}
			if ts.Valid && ts.Int64 != 0 {
				a.ReportState(src, conv, "read_until", ts.Int64, toInt(r["observed_at"])*1000, 0)
			}
		}
		if outbox := r["outbox"]; truthy(outbox) && str(r["kind"]) == "user" {
			peer := a.Address(person.of(chatID, true))
			mine := "SELECT id FROM message WHERE conversation_id = ? AND outgoing AND key IS NOT NULL " +
				"AND CAST(key AS INTEGER) <= ?"
			a.Exec("INSERT OR IGNORE INTO receipt (message_id, address_id) SELECT id, ? FROM ("+mine+")", peer, conv, outbox)
			a.Exec("UPDATE receipt SET read_at = ? WHERE address_id = ? AND read_at IS NULL "+
				"AND message_id IN ("+mine+")", toInt(r["outbox_at"])*1000, peer, conv, outbox)
		}
	}
}

func telegramCall(a *archive.Archive, src int64, rowKey string, m map[string]any, peer archive.Handle, ts int64) int {
	action := obj(m["action"])
	if a.HasOrigin(src, rowKey, "call_origin") {
		return 0
	}
	key := "" // no call id: no key (the Python keyed it "None")
	if action["call_id"] != nil {
		key = pyStr(action["call_id"])
	}
	if key != "" && a.Exists("SELECT 1 FROM call WHERE service_id = ? AND key = ?", a.Service.ID("telegram"), key) {
		return 0
	}
	reasonV, hasReason := obj(action["reason"])["_"]
	reason := ""
	if hasReason && reasonV != nil {
		reason = pyStr(reasonV)
	}
	duration := toInt(action["duration"])
	outgoing := truthy(m["out"])
	detail := telegramCallDetail[reason]
	if reason == "PhoneCallDiscardReasonMissed" || (duration == 0 && reason == "PhoneCallDiscardReasonHangup") {
		detail = "missed"
		if outgoing {
			detail = "unanswered"
		}
	}
	code := ""
	if reason != "" {
		code = "telegram:" + reason
	}
	a.AddCall(src, rowKey, archive.Call{Service: "telegram", AddressID: a.Address(peer), TS: ts, Outgoing: outgoing,
		Answered: duration > 0, Duration: duration, Key: key, Detail: detail, DetailCode: code, Video: truthy(action["video"])})
	return 1
}

// TelegramOptions: DBPath "" is the default; Only: just these (chat id, message id) (the live
// connection's new messages), nil for all; Skip: chats not to import (the user's choice); what is
// already in the archive stays.
type TelegramOptions struct {
	DBPath string
	Only   map[[2]int64]bool
	Skip   map[int64]bool
}

func Telegram(a *archive.Archive, out func(string), opt TelegramOptions) (err error) {
	defer archive.Recover(&err)
	dbPath := opt.DBPath
	if dbPath == "" {
		dbPath = telegramstore.DB()
	}
	if !exists(dbPath) {
		say(out, "no source: {db} (telegram-sync)", map[string]any{"db": dbPath})
		return nil
	}
	d := ro(dbPath)
	defer d.Close()
	var chatsWanted map[int64]bool
	if opt.Only != nil {
		chatsWanted = map[int64]bool{}
		for k := range opt.Only {
			chatsWanted[k[0]] = true
		}
	}
	person := newTGPeople(d)
	meID := "None"
	if person.hasMe {
		meID = fmt.Sprint(person.me)
		a.Account(archive.H("id", meID, "telegram"), "telegram", "")
	}
	own := a.Own()
	own[archive.H("id", meID, "telegram")] = true
	src := a.Source(telegramSource, dbPath, "telegram", telegramstore.Media())
	for _, pid := range person.order {
		for _, h := range person.others(pid) {
			a.Alias(h, person.of(pid, true))
		}
	}
	added, calls, updated := map[string]int{}, 0, map[string]int{}
	type chat struct {
		id          int64
		kind, title any
	}
	var chats []chat
	for _, r := range maps(d, "SELECT id, kind, title FROM chat ORDER BY id") {
		chats = append(chats, chat{toInt(r["id"]), r["kind"], r["title"]})
	}
	hasDeleted := db.Exists(d, "SELECT 1 FROM sqlite_master WHERE name = 'deleted'") // not in a store the Python made
	hasMembers := db.Exists(d, "SELECT 1 FROM sqlite_master WHERE name = 'chat_member'")
	for _, c := range chats {
		if (chatsWanted != nil && !chatsWanted[c.id]) || opt.Skip[c.id] {
			continue
		}
		kind := str(c.kind)
		key := fmt.Sprint(c.id)
		var conv int64
		if kind == "user" || kind == "saved" {
			conv = titled(a, "telegram", []archive.Handle{person.of(c.id, true)}, key, c.title)
		} else {
			conv = titled(a, "telegram", nil, key, c.title)
			a.Exec("UPDATE conversation SET is_group = 1 WHERE id = ?", conv)
		}
		query, args := "SELECT id, date, json FROM message WHERE chat_id = ? ORDER BY id", []any{c.id}
		if opt.Only != nil {
			var ids []int64
			for k := range opt.Only {
				if k[0] == c.id {
					ids = append(ids, k[1])
				}
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			query = "SELECT id, date, json FROM message WHERE chat_id = ? AND id IN (" + db.Marks(len(ids)) + ") ORDER BY id"
			args = append(args, db.Args(ids)...)
		}
		deleted := map[int64]bool{} // deleted on Telegram since (the live connection's record)
		if hasDeleted {
			db.Each(d, "SELECT id FROM deleted WHERE chat_id = ?", []any{c.id}, func(scan func(...any)) {
				var id int64
				scan(&id)
				deleted[id] = true
			})
		}
		senders := map[archive.Handle]bool{}
		var senderOrder []archive.Handle
		type named struct {
			key   string
			who   archive.Handle
			token string
		}
		var names []named
		db.Each(d, query, args, func(scan func(...any)) {
			var mid, date int64
			var js []byte
			scan(&mid, &date, &js)
			rowKey := fmt.Sprintf("%d/%d", c.id, mid)
			dec := json.NewDecoder(bytes.NewReader(js))
			dec.UseNumber()
			var m map[string]any
			if err := dec.Decode(&m); err != nil {
				panic(err)
			}
			ts := date * 1000
			outgoing := truthy(m["out"])
			if pyStr(m["_"]) == "MessageService" && pyStr(obj(m["action"])["_"]) == "MessageActionPhoneCall" && kind == "user" {
				calls += telegramCall(a, src, rowKey, m, person.of(c.id, true), ts)
			}
			from, fromOK := peerID(m["from_id"])
			if !outgoing && kind != "user" && kind != "saved" && fromOK && from > 0 {
				h := person.of(from, true) // a group's members: whoever wrote there
				if !senders[h] {
					senders[h] = true
					senderOrder = append(senderOrder, h)
				}
			}
			if truthy(m["entities"]) {
				for _, x := range tgMentions(m, person) {
					names = append(names, named{fmt.Sprint(mid), x.who, x.token})
				}
			}
			if a.HasOrigin(src, rowKey, "") {
				telegramChanges(a, src, rowKey, m, person, own, updated, deleted[mid])
				return
			}
			k := telegramKindOf(m)
			x := &archive.Extras{Place: k.place}
			if k.text != nil {
				x.Text = pyStr(k.text)
			}
			x.Lat, x.Lon = floatPtr(k.lat), floatPtr(k.lon)
			if k.subtype != "" {
				x.Subtype, x.SubtypeCode = k.subtype, k.code
			}
			reply := obj(m["reply_to"])
			if truthy(reply["reply_to_msg_id"]) && !truthy(reply["reply_to_peer_id"]) {
				x.ReplyKey = pyStr(reply["reply_to_msg_id"])
				if truthy(reply["quote_text"]) {
					x.ReplyText = pyStr(reply["quote_text"])
				}
			}
			if truthy(m["edit_date"]) && !truthy(m["edit_hide"]) {
				x.Edited = true
			}
			if truthy(m["fwd_from"]) {
				x.Forwarded = true
			}
			if truthy(m["reactions"]) {
				x.Reactions = tgReactions(m, person, own)
			}
			var senderID int64
			if !outgoing {
				who, whoOK := from, fromOK && from != 0
				if !whoOK {
					who = c.id
				}
				senderID = a.Address(person.of(who, true))
			}
			text := ""
			if truthy(m["message"]) {
				text = pyStr(m["message"])
			}
			a.AddMessage(src, rowKey, archive.Message{Service: "telegram", ConversationID: conv, TS: ts, Outgoing: outgoing,
				SenderID: senderID, Kind: k.kind, Text: text, Key: fmt.Sprint(mid), Extras: x})
			added[kind]++
			if deleted[mid] { // deleted before it was first imported: kept, said deleted
				telegramChanges(a, src, rowKey, m, person, own, updated, true)
			}
		})
		for _, member := range senderOrder {
			if !own[member] {
				a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", conv, a.Address(member))
			}
		}
		if hasMembers && kind != "user" && kind != "saved" {
			telegramMembers(a, d, person, own, c.id, conv)
		}
		for _, n := range names {
			if mid, ok := a.MessageByKey("telegram", n.key, conv); ok && mid != 0 {
				a.Exec("INSERT OR IGNORE INTO mention VALUES (?, ?, ?)", mid, a.Address(n.who), n.token)
			}
		}
		a.Commit()
	}
	for _, pid := range person.order { // profile names, for the people the archive has
		if (!person.hasMe || pid != person.me) && person.name(pid) != "" {
			a.HandleName(person.of(pid, true), "telegram", person.name(pid), "profile", 0)
		}
	}
	// archived chats, as the last sync saw them (only the start of ours: see Archive.InitArchived)
	for _, r := range maps(d, "SELECT id, archived, synced_at FROM chat WHERE synced_at IS NOT NULL") {
		conv, _ := a.FindConversation("telegram", pyStr(r["id"]))
		a.ReportState(src, conv, "archived", int64(archive.B2I(truthy(r["archived"]))), toInt(r["synced_at"])*1000, 0)
	}
	telegramReads(a, dbPath, chatsWanted)
	a.Resolve()
	a.Imported(src)
	a.Commit()
	var kinds []string
	for k := range added {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		say(out, "new:     {n} {kind}", map[string]any{"n": fmt.Sprintf("%7d", added[k]), "kind": k})
	}
	say(out, "calls: {n}", map[string]any{"n": fmt.Sprintf("%7d", calls)})
	if len(updated) > 0 {
		var parts []string
		for _, k := range []string{"text", "edited", "deleted", "reactions"} {
			if updated[k] > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", k, updated[k]))
			}
		}
		say(out, "changes to what was there: {list}", map[string]any{"list": strings.Join(parts, ", ")})
	}
	return nil
}

// telegramMembers adds a group's members as Telegram last gave them (chat_member) to its
// conversation, but bots, deleted accounts and the owner; it gives the user ids added. One who left
// stays, as in the other services' groups (a member once, and the author of what they wrote).
func telegramMembers(a *archive.Archive, d *sql.DB, person *tgPeople, own map[archive.Handle]bool, chatID, conv int64) []int64 {
	var out []int64
	for _, uid := range db.Ints(d, "SELECT user_id FROM chat_member WHERE chat_id = ? ORDER BY user_id", chatID) {
		e := person.entity[uid]
		if truthy(e["bot"]) || truthy(e["deleted"]) || truthy(e["is_self"]) || (person.hasMe && uid == person.me) {
			continue
		}
		h := person.of(uid, true)
		if own[h] {
			continue
		}
		a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", conv, a.Address(h))
		out = append(out, uid)
	}
	return out
}

// TelegramMembers: the members kept for those groups (telegram.db's chat_member, as the live
// connection asked them) into their conversations, with the members' other handles and profile
// names, as the import gives them. A group with no conversation yet waits for the next import.
func TelegramMembers(a *archive.Archive, out func(string), dbPath string, chats map[int64]bool) (err error) {
	defer archive.Recover(&err)
	if dbPath == "" {
		dbPath = telegramstore.DB()
	}
	if !exists(dbPath) {
		return nil
	}
	d := ro(dbPath)
	defer d.Close()
	if !db.Exists(d, "SELECT 1 FROM sqlite_master WHERE name = 'chat_member'") {
		return nil
	}
	person := newTGPeople(d)
	own := a.Own()
	if person.hasMe {
		own[archive.H("id", fmt.Sprint(person.me), "telegram")] = true
	}
	for _, r := range maps(d, "SELECT id, kind FROM chat WHERE kind NOT IN ('user', 'saved') ORDER BY id") {
		chatID := toInt(r["id"])
		if !chats[chatID] {
			continue
		}
		conv, ok := a.FindConversation("telegram", fmt.Sprint(chatID))
		if !ok || conv == 0 {
			continue
		}
		for _, uid := range telegramMembers(a, d, person, own, chatID, conv) {
			for _, h := range person.others(uid) {
				a.Alias(h, person.of(uid, true))
			}
			if name := person.name(uid); name != "" {
				a.HandleName(person.of(uid, true), "telegram", name, "profile", 0)
			}
		}
	}
	a.Commit()
	return nil
}

// telegramChanges: what Telegram says now of a message the archive already has (the live
// connection writes an edited message over its row and imports it again): an edit's new text,
// marked edited, and its reactions as they are now. deleted: deleted on Telegram (its text kept,
// marked deleted, as other clients show it).
func telegramChanges(a *archive.Archive, src int64, rowKey string, m map[string]any, person *tgPeople,
	own map[archive.Handle]bool, updated map[string]int, deleted bool) {
	mid, ok := a.IntOK("SELECT message_id FROM message_origin WHERE source_id = ? AND row_key = ?", src, rowKey)
	if !ok {
		return
	}
	if deleted {
		for _, c := range ApplyChange(a, mid, Change{Deleted: true}) {
			updated[c]++
		}
	}
	if truthy(m["edit_date"]) && !truthy(m["edit_hide"]) {
		var t string // the text as the import makes it: a poll's or a contact's, else the message's
		if k := telegramKindOf(m); k.text != nil {
			t = pyStr(k.text)
		} else if truthy(m["message"]) {
			t = pyStr(m["message"])
		}
		for _, c := range ApplyChange(a, mid, Change{Text: &t, Edited: true}) {
			updated[c]++
		}
	}
	var want []archive.Reaction
	if truthy(m["reactions"]) {
		want = tgReactions(m, person, own)
	}
	if ReplaceReactions(a, mid, want) {
		updated["reactions"]++
	}
}

// TelegramMedia: the downloaded files (telegram-sync --media), each to its message (a media step).
func TelegramMedia(a *archive.Archive, s *Store) {
	dbPath := telegramstore.DB()
	if !exists(dbPath) {
		return
	}
	media := telegramstore.Media()
	src := a.Source(telegramSource, dbPath, "telegram", media)
	origins := map[string]int64{}
	a.Each("SELECT row_key, message_id FROM message_origin WHERE source_id = ?", []any{src}, func(scan func(...any)) {
		var k string
		var id int64
		scan(&k, &id)
		origins[k] = id
	})
	d := ro(dbPath)
	defer d.Close()
	for _, r := range maps(d, "SELECT chat_id, id, file FROM message WHERE file IS NOT NULL ORDER BY chat_id, id") {
		rel := str(r["file"])
		s.Link(telegramSource, src, filepath.Join(media, rel), rel, origins[fmt.Sprintf("%v/%v", r["chat_id"], r["id"])])
	}
}
