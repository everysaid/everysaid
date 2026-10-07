package signal

// New (the Python never had Signal); written as the importers of everysaid/telegram.py and
// whatsapp.py are. Imports signal.db into the archive, service "signal".
//
// A chat's conversation is keyed by Signal's own id: the other person's ACI, or the group's id.
// Signal names a message by its author and the time it was sent, so its key (and its row key in the
// source) is "<author ACI>:<sent ms>"; a reply's, its quote's. People are stored by phone number
// where Signal shows it (so they meet the same person on other services), else by their ACI (an
// `id` within the service); the ACI then joins the same person. The owner's ACI is an account.
// What changed on messages the archive has (edits, deletions: marked, the text stays as the
// archive first had it; reactions: as they are now), receipts of the owner's messages, how far the
// owner read each chat (on any device) and the calls the phone or the others reported are brought
// too, and the files the helper fetched go to the media store.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
)

// Service is the archive's service of this plugin.
const Service = "signal"

// SourceName is the archive's source of an account.
func SourceName(own string) string { return "signal/" + own }

// Key is a message's key: its author and sent time.
func Key(author string, ts int64) string { return author + ":" + strconv.FormatInt(ts, 10) }

// SplitKey is a key's author and sent time (false when it is not one).
func SplitKey(key string) (string, int64, bool) {
	i := strings.LastIndexByte(key, ':')
	if i < 0 {
		return "", 0, false
	}
	ts, err := strconv.ParseInt(key[i+1:], 10, 64)
	return key[:i], ts, err == nil
}

// Counts is what an import brought.
type Counts struct {
	Messages, Calls, Files, Changes int
}

type contactRow struct {
	phone, name, profile string
	seen                 int64
}

// people: Signal's ACIs as the archive's handles.
type people struct {
	contacts map[string]contactRow
}

func loadPeople(d db.Querier) *people {
	p := &people{contacts: map[string]contactRow{}}
	db.Each(d, "SELECT aci, coalesce(phone, ''), coalesce(name, ''), coalesce(profile_name, ''), seen_at FROM contact",
		nil, func(scan func(...any)) {
			var aci string
			var c contactRow
			scan(&aci, &c.phone, &c.name, &c.profile, &c.seen)
			p.contacts[aci] = c
		})
	return p
}

// handle is a person's handle: their number where Signal shows it, else their ACI.
func (p *people) handle(aci string) archive.Handle {
	if c, ok := p.contacts[aci]; ok && c.phone != "" {
		if k, v := archive.Address(c.phone, config.Region); k == "phone" {
			return archive.H(k, v)
		}
	}
	return archive.H("id", aci, Service)
}

// mentionName is how a text names someone it mentions: their name, else their number, else the
// start of their ACI.
func (p *people) mentionName(aci string) string {
	c := p.contacts[aci]
	for _, n := range []string{c.name, c.profile, c.phone} {
		if strings.TrimSpace(n) != "" {
			return strings.TrimSpace(n)
		}
	}
	if len(aci) > 8 {
		return aci[:8]
	}
	return aci
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type token struct{ aci, text string }

// withMentions is the text with each mention (in UTF-16 units, over the U+FFFC Signal's apps put
// there) written as "@Name", and the tokens as the text now has them.
func withMentions(text string, ms []mentionEv, name func(string) string) (string, []token) {
	if len(ms) == 0 {
		return text, nil
	}
	u := utf16.Encode([]rune(text))
	sorted := append([]mentionEv(nil), ms...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start > sorted[j].Start })
	var toks []token
	for _, m := range sorted {
		if m.Start < 0 || m.Length <= 0 || m.Start+m.Length > len(u) {
			continue
		}
		t := "@" + name(m.ACI)
		u = append(append(append([]uint16{}, u[:m.Start]...), utf16.Encode([]rune(t))...), u[m.Start+m.Length:]...)
		toks = append(toks, token{m.ACI, t})
	}
	return string(utf16.Decode(u)), toks
}

// describe is a message's kind, extras and text, from the helper's event.
func describe(e event) (string, *archive.Extras, string) {
	x := &archive.Extras{Forwarded: e.Forwarded}
	text := deref(e.Text)
	sub := func(subtype, code string) { x.Subtype, x.SubtypeCode = subtype, code }
	kind := "text"
	switch {
	case len(e.Attachments) > 0:
		a := e.Attachments[0]
		ct := strings.ToLower(deref(a.ContentType))
		switch {
		case a.Sticker:
			kind = "sticker"
		case a.Gif || ct == "image/gif":
			kind = "image"
			sub("gif", "signal:gif")
		case strings.HasPrefix(ct, "image/"):
			kind = "image"
		case strings.HasPrefix(ct, "video/"):
			kind = "video"
		case a.Voice:
			kind = "voice"
		default:
			kind = "file"
		}
	case len(e.Contacts) > 0:
		kind = "contact"
		var lines []string
		for _, c := range e.Contacts {
			parts := []string{}
			if n := strings.TrimSpace(deref(c.Name)); n != "" {
				parts = append(parts, n)
			}
			parts = append(parts, c.Phones...)
			parts = append(parts, c.Emails...)
			if len(parts) > 0 {
				lines = append(lines, strings.Join(parts, " "))
			}
		}
		x.Text = strings.Join(lines, "\n")
	case e.Poll != nil:
		sub("poll", "signal:poll")
		lines := []string{}
		if q := deref(e.Poll.Question); q != "" {
			lines = append(lines, q)
		}
		for _, o := range e.Poll.Options {
			lines = append(lines, "- "+o)
		}
		x.Text = strings.Join(lines, "\n")
	case e.GroupChange && text == "":
		kind = "system"
		sub("group event", "signal:group_change")
	case e.GroupCall:
		kind = "system"
		sub("call", "signal:group_call")
	case len(e.PinRaw) > 0 && string(e.PinRaw) != "null":
		kind = "system"
		sub("pin", "signal:pin")
	case len(e.UnpinRaw) > 0 && string(e.UnpinRaw) != "null":
		kind = "system"
		sub("pin", "signal:unpin")
	case e.ExpireUpdate && text == "":
		kind = "system"
		sub("notice", "signal:expire_timer")
	case e.Payment:
		kind = "system"
		sub("notice", "signal:payment")
	case e.Gift:
		kind = "system"
		sub("notice", "signal:gift")
	case len(e.Previews) > 0:
		sub("link", "signal:preview")
	}
	if e.Quote != nil && e.Quote.Author != nil {
		x.ReplyKey = Key(*e.Quote.Author, e.Quote.TS)
		x.ReplyText = deref(e.Quote.Text)
	}
	return kind, x, text
}

type msgRow struct {
	author         string
	ts             int64
	chatKind, chat string
	outgoing       bool
	js             string
}

// Import brings signal.db (at dbPath, its files in mediaDir) into the archive, for the plugin
// instance iid; skip: chats not to import (the user's choice), what is in the archive stays.
func Import(a *archive.Archive, dbPath, mediaDir string, iid int64, skip map[string]bool) (n Counts, err error) {
	defer archive.Recover(&err)
	if _, err := os.Stat(dbPath); err != nil {
		return n, nil
	}
	d, err := db.ReadOnly(dbPath)
	if err != nil {
		return n, err
	}
	defer d.Close()
	own := db.Str(d, "SELECT value FROM account WHERE key = 'aci'")
	if own == "" {
		return n, nil // not linked yet: nothing it could have
	}
	p := loadPeople(d)
	src := a.Source(SourceName(own), dbPath, Service, mediaDir)
	if iid != 0 {
		a.Exec("UPDATE source SET instance_id = ? WHERE id = ? AND instance_id IS NULL", iid, src)
	}
	ownH := archive.H("id", own, Service)
	a.Account(ownH, Service, "")
	ownSet := a.Own()
	ownSet[ownH] = true
	acis := make([]string, 0, len(p.contacts))
	for aci := range p.contacts {
		acis = append(acis, aci)
	}
	sort.Strings(acis)
	for _, aci := range acis {
		if h := p.handle(aci); h.Kind == "phone" {
			a.Alias(archive.H("id", aci, Service), h)
		}
	}

	titles, members := map[string]string{}, map[string][]string{}
	db.Each(d, "SELECT id, coalesce(title, '') FROM grp", nil, func(scan func(...any)) {
		var id, t string
		scan(&id, &t)
		titles[id] = t
	})
	db.Each(d, "SELECT group_id, aci FROM group_member ORDER BY group_id, aci", nil, func(scan func(...any)) {
		var g, aci string
		scan(&g, &aci)
		members[g] = append(members[g], aci)
	})
	convs := map[string]int64{}
	conv := func(kind, chat string) int64 {
		if id, ok := convs[kind+"\x00"+chat]; ok {
			return id
		}
		var id int64
		if kind == "group" {
			id = a.Conversation(Service, nil, chat, titles[chat])
			a.Exec("UPDATE conversation SET is_group = 1 WHERE id = ?", id)
			for _, m := range members[chat] {
				if h := p.handle(m); !ownSet[h] && m != own {
					a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", id, a.Address(h))
				}
			}
		} else {
			id = a.Conversation(Service, []archive.Handle{p.handle(chat)}, chat, "")
		}
		convs[kind+"\x00"+chat] = id
		return id
	}
	for g := range titles {
		if !skip[g] {
			conv("group", g)
		}
	}

	edits := map[string]string{} // the newest edit of each message
	db.Each(d, "SELECT author, target_ts, json FROM edit ORDER BY ts", nil, func(scan func(...any)) {
		var author, js string
		var ts int64
		scan(&author, &ts, &js)
		edits[Key(author, ts)] = js
	})
	deleted := map[string]bool{}
	for _, k := range db.Strs(d, "SELECT author || ':' || ts FROM deletion") {
		deleted[k] = true
	}

	var rows []msgRow
	db.Each(d, "SELECT author, ts, chat_kind, chat, outgoing, json FROM message ORDER BY ts, author", nil,
		func(scan func(...any)) {
			var r msgRow
			scan(&r.author, &r.ts, &r.chatKind, &r.chat, &r.outgoing, &r.js)
			rows = append(rows, r)
		})
	for _, r := range rows {
		if skip[r.chat] {
			continue
		}
		cid := conv(r.chatKind, r.chat)
		if !r.outgoing && r.chatKind == "group" && r.author != own { // a group's members: whoever wrote there too
			if h := p.handle(r.author); !ownSet[h] {
				a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", cid, a.Address(h))
			}
		}
		key := Key(r.author, r.ts)
		if a.HasOrigin(src, key, "") {
			continue
		}
		if mid, ok := a.MessageByKey(Service, key, cid); ok { // another instance of the same account brought it
			a.Exec("INSERT OR IGNORE INTO message_origin VALUES (?, ?, ?)", src, key, mid)
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(r.js), &e); err != nil {
			continue
		}
		edited := false
		if js, ok := edits[key]; ok { // its content as last edited
			var ed event
			if json.Unmarshal([]byte(js), &ed) == nil {
				e.Text, e.Mentions, edited = ed.Text, ed.Mentions, true
				if len(ed.Attachments) > 0 {
					e.Attachments = ed.Attachments
				}
			}
		}
		kind, x, text := describe(e)
		x.Edited, x.Deleted = edited, deleted[key]
		text, toks := withMentions(text, e.Mentions, p.mentionName)
		var sender int64
		if !r.outgoing {
			sender = a.Address(p.handle(r.author))
		}
		mid := a.AddMessage(src, key, archive.Message{Service: Service, ConversationID: cid, TS: r.ts,
			Outgoing: r.outgoing, SenderID: sender, Kind: kind, Text: text, Key: key, Extras: x})
		n.Messages++
		for _, t := range toks {
			a.Exec("INSERT OR IGNORE INTO mention VALUES (?, ?, ?)", mid, a.Address(p.handle(t.aci)), t.text)
		}
		for _, at := range e.Attachments {
			f := deref(at.File)
			if f == "" || f != filepath.Base(f) || f == "." || f == ".." {
				continue // only files within the helper's folder
			}
			added, err := linkMedia(a, src, filepath.Join(mediaDir, f), f, mid)
			if err != nil {
				return n, err
			}
			if added {
				n.Files++
			}
		}
	}

	n.Changes += changes(a, d, p, own, ownSet, edits, deleted)
	receipts(a, d, p, own, ownSet)
	reads(a, d, src)
	n.Calls += calls(a, d, p, src, conv, skip)

	for _, aci := range acis { // the names Signal shows, for the handles in the archive
		c := p.contacts[aci]
		if h := p.handle(aci); !ownSet[h] && aci != own {
			if c.name != "" {
				a.HandleName(h, Service, c.name, "book", c.seen)
			}
			if c.profile != "" {
				a.HandleName(h, Service, c.profile, "profile", c.seen)
			}
		}
	}
	a.Resolve()
	a.Imported(src)
	a.Commit()
	return n, nil
}

// changes: what happened to messages the archive has: edits and deletions (marked), and reactions
// as they are now (one per person: changed, added, or removed once taken back). It returns how many.
func changes(a *archive.Archive, d db.Querier, p *people, own string, ownSet map[archive.Handle]bool,
	edits map[string]string, deleted map[string]bool) int {
	n := 0
	mark := func(keys []string, flag string) {
		sort.Strings(keys)
		for _, k := range keys {
			if mid, ok := a.MessageByKey(Service, k, 0); ok {
				n += int(db.Changed(a.Tx(), "UPDATE message SET "+flag+" = 1 WHERE id = ? AND NOT "+flag, mid))
			}
		}
	}
	var ek, dk []string
	for k := range edits {
		ek = append(ek, k)
	}
	for k := range deleted {
		dk = append(dk, k)
	}
	mark(ek, "edited")
	mark(dk, "deleted")
	type reaction struct {
		sender string
		emoji  *string
	}
	byTarget := map[string][]reaction{}
	var targets []string
	db.Each(d, "SELECT target_author, target_ts, sender, emoji FROM reaction ORDER BY target_author, target_ts, sender",
		nil, func(scan func(...any)) {
			var author, sender string
			var ts int64
			var emoji *string
			scan(&author, &ts, &sender, &emoji)
			k := Key(author, ts)
			if _, ok := byTarget[k]; !ok {
				targets = append(targets, k)
			}
			byTarget[k] = append(byTarget[k], reaction{sender, emoji})
		})
	for _, k := range targets {
		mid, ok := a.MessageByKey(Service, k, 0)
		if !ok {
			continue
		}
		for _, r := range byTarget[k] {
			h := p.handle(r.sender)
			mine := r.sender == own || ownSet[h]
			var who any
			q, args := "SELECT rowid, emoji FROM reaction WHERE message_id = ? AND outgoing = 1", []any{mid}
			if !mine {
				who = a.Address(h)
				q, args = "SELECT rowid, emoji FROM reaction WHERE message_id = ? AND address_id = ?", []any{mid, who}
			}
			var rowid int64
			var old *string
			found := a.Row(q, args, &rowid, &old)
			switch {
			case r.emoji == nil:
				if found {
					a.Exec("DELETE FROM reaction WHERE rowid = ?", rowid)
					n++
				}
			case !found:
				var out any
				if mine {
					out = 1
				}
				a.Exec("INSERT INTO reaction (message_id, emoji, count, address_id, outgoing) VALUES (?, ?, 1, ?, ?)",
					mid, *r.emoji, who, out)
				n++
			case old == nil || *old != *r.emoji:
				a.Exec("UPDATE reaction SET emoji = ?, code = NULL WHERE rowid = ?", *r.emoji, rowid)
				n++
			}
		}
	}
	return n
}

var receiptColumn = map[string]string{"delivery": "delivered_at", "read": "read_at", "viewed": "played_at"}

// receipts: who got, read and played the owner's messages, and when (the first time each).
func receipts(a *archive.Archive, d db.Querier, p *people, own string, ownSet map[archive.Handle]bool) {
	db.Each(d, "SELECT ts, aci, kind, at FROM receipt ORDER BY ts, aci, kind", nil, func(scan func(...any)) {
		var ts, at int64
		var aci, kind string
		scan(&ts, &aci, &kind, &at)
		col, ok := receiptColumn[kind]
		h := p.handle(aci)
		if !ok || aci == own || ownSet[h] {
			return
		}
		mid, found := a.MessageByKey(Service, Key(own, ts), 0)
		if !found {
			return
		}
		aid := a.Address(h)
		a.Exec("INSERT OR IGNORE INTO receipt (message_id, address_id) SELECT id, ? FROM message WHERE id = ? AND outgoing", aid, mid)
		a.Exec("UPDATE receipt SET "+col+" = ? WHERE message_id = ? AND address_id = ? AND (coalesce("+col+", 0) = 0 OR ("+
			col+" > ? AND ? > 0))", at, mid, aid, at, at)
	})
}

// reads: how far the owner read each chat, on any device (the newest message read there, and when).
func reads(a *archive.Archive, d db.Querier, src int64) {
	db.Each(d, "SELECT m.chat, max(m.ts), max(r.at) FROM read r JOIN message m ON m.author = r.author AND m.ts = r.ts "+
		"GROUP BY m.chat ORDER BY m.chat", nil, func(scan func(...any)) {
		var chat string
		var newest, when int64
		scan(&chat, &newest, &when)
		if cid, ok := a.FindConversation(Service, chat); ok {
			a.ReportState(src, cid, "read_until", newest, when, when)
		}
	})
}

// callSettle: a call not yet ended nor reported by the phone is taken as over after this long.
const callSettle = 10 * time.Minute

// calls: the calls as they ended (or as the phone reported them); a call already in the archive is
// brought up to date. It returns how many are new.
func calls(a *archive.Archive, d db.Querier, p *people, src int64, conv func(kind, chat string) int64, skip map[string]bool) int {
	n := 0
	now := time.Now().UnixMilli()
	type row struct {
		id, kind, chat, result, hangup string
		ts                             int64
		outgoing, accepted, ended      *int64
		video                          bool
	}
	var rows []row
	db.Each(d, "SELECT id, coalesce(chat_kind, ''), coalesce(chat, ''), ts, outgoing, video, accepted, coalesce(result, ''), "+
		"coalesce(hangup, ''), ended_at FROM call WHERE chat IS NOT NULL ORDER BY ts, id", nil, func(scan func(...any)) {
		var r row
		scan(&r.id, &r.kind, &r.chat, &r.ts, &r.outgoing, &r.video, &r.accepted, &r.result, &r.hangup, &r.ended)
		rows = append(rows, r)
	})
	for _, r := range rows {
		if skip[r.chat] || (r.result == "" && r.ended == nil && now-r.ts < callSettle.Milliseconds()) {
			continue
		}
		outgoing := r.outgoing != nil && *r.outgoing == 1
		answered := (r.accepted != nil && *r.accepted == 1) || r.hangup == "accepted"
		var duration int64
		if answered && r.ended != nil && *r.ended > r.ts {
			duration = (*r.ended - r.ts) / 1000
		}
		detail := ""
		switch {
		case r.hangup == "busy":
			detail = "busy"
		case r.hangup == "declined" && !answered:
			detail = "rejected"
		case !answered && outgoing:
			detail = "unanswered"
		case !answered:
			detail = "missed"
		}
		code := ""
		if r.hangup != "" {
			code = "signal:" + r.hangup
		}
		c := archive.Call{Service: Service, TS: r.ts, Outgoing: outgoing, Answered: answered, Duration: duration,
			Key: r.id, Detail: detail, DetailCode: code, Video: r.video}
		if r.kind == "group" {
			c.ConversationID = conv("group", r.chat)
		} else {
			c.AddressID = a.Address(p.handle(r.chat))
		}
		if cid, ok := a.IntOK("SELECT call_id FROM call_origin WHERE source_id = ? AND row_key = ?", src, r.id); ok {
			a.Exec("UPDATE call SET answered = ?, duration = ?, detail = ?, detail_code = ? WHERE id = ? AND "+
				"(answered, duration, detail, detail_code) IS NOT (?, ?, ?, ?)", archive.B2I(answered), duration,
				archive.NullStr(detail), archive.NullStr(code), cid, archive.B2I(answered), duration,
				archive.NullStr(detail), archive.NullStr(code))
			continue
		}
		if a.Exists("SELECT 1 FROM call WHERE service_id = ? AND key = ?", a.Service.ID(Service), r.id) {
			continue
		}
		a.AddCall(src, r.id, c)
		n++
	}
	return n
}

// ChatList is the chats signal.db has, for the user's choice of what to import: [{id, kind, title,
// messages, first, last, import}].
func ChatList(dbPath string, skip map[string]bool) (out []map[string]any, err error) {
	out = []map[string]any{}
	if _, err := os.Stat(dbPath); err != nil {
		return out, nil
	}
	d, err := db.ReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	defer db.Recover(&err)
	p := loadPeople(d)
	titles := map[string]string{}
	db.Each(d, "SELECT id, coalesce(title, '') FROM grp", nil, func(scan func(...any)) {
		var id, t string
		scan(&id, &t)
		titles[id] = t
	})
	db.Each(d, "SELECT chat_kind, chat, count(*), min(ts), max(ts) FROM message GROUP BY chat_kind, chat ORDER BY max(ts) DESC",
		nil, func(scan func(...any)) {
			var kind, chat string
			var count, first, last int64
			scan(&kind, &chat, &count, &first, &last)
			title := titles[chat]
			if kind != "group" {
				title = p.mentionName(chat)
			}
			out = append(out, map[string]any{"id": chat, "kind": kind, "title": title, "messages": count,
				"first": first, "last": last, "import": !skip[chat]})
		})
	return out, nil
}
