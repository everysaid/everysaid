package signal

// New (the Python never had Signal); written as the importers of everysaid/telegram.py and
// whatsapp.py are. Imports signal.db into the archive, service "signal".
//
// A chat's conversation is keyed by Signal's own id: the other person's ACI (their PNI, the id of
// their number, for someone the owner wrote to only by number), or the group's id.
// Signal names a message by its author and the time it was sent, so its key (and its row key in the
// source) is "<author ACI>:<sent ms>"; a reply's, its quote's. People are stored by phone number
// where Signal shows it (so they meet the same person on other services), else by their ACI (an
// `id` within the service); the ACI then joins the same person. A PNI is its number's, or its
// ACI's, where Signal told them (the `pni` table), at every import: a chat kept under it before
// joins the person then. The owner's ACI is an account.
// What changed on messages the archive has (edits: the text as last edited, marked so; deletions:
// marked, the text stays; reactions: as they are now), receipts of the owner's messages, how far the
// owner read each chat (on any device) and the calls the phone or the others reported are brought
// too, and the files the helper fetched go to the media store.

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/importers"
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

// people: Signal's ids as the archive's handles.
type people struct {
	contacts map[string]contactRow
	pnis     map[string]pniRow // by "PNI:<uuid>"
	pniOf    map[string]string // an ACI's PNI
}

// pniRow is what Signal told of a PNI: the number it is the id of, the ACI it belongs to.
type pniRow struct{ phone, aci string }

func loadPeople(d db.Querier) *people {
	p := &people{contacts: map[string]contactRow{}, pnis: map[string]pniRow{}, pniOf: map[string]string{}}
	db.Each(d, "SELECT aci, coalesce(phone, ''), coalesce(name, ''), coalesce(profile_name, ''), seen_at FROM contact",
		nil, func(scan func(...any)) {
			var aci string
			var c contactRow
			scan(&aci, &c.phone, &c.name, &c.profile, &c.seen)
			p.contacts[aci] = c
		})
	if db.Exists(d, "SELECT 1 FROM sqlite_master WHERE name = 'pni'") { // a signal.db from before it
		db.Each(d, "SELECT pni, coalesce(phone, ''), coalesce(aci, '') FROM pni ORDER BY pni", nil, func(scan func(...any)) {
			var pni string
			var r pniRow
			scan(&pni, &r.phone, &r.aci)
			p.pnis[pni] = r
			if r.aci != "" {
				p.pniOf[r.aci] = pni
			}
		})
	}
	return p
}

// contact is what is known of someone by any of their ids: a PNI is its ACI's where Signal told it,
// and the number is the one either of them carries.
func (p *people) contact(id string) contactRow {
	r, isPNI := p.pnis[id]
	switch {
	case isPNI && r.aci != "":
		id = r.aci
	case !isPNI:
		r = p.pnis[p.pniOf[id]]
	}
	c := p.contacts[id]
	if c.phone == "" {
		c.phone = r.phone
	}
	return c
}

// ids are every id of someone Signal told of (ACIs and PNIs), sorted.
func (p *people) ids() []string {
	set := map[string]bool{}
	for id := range p.contacts {
		set[id] = true
	}
	for pni, r := range p.pnis {
		set[pni] = true
		if r.aci != "" {
			set[r.aci] = true
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// handle is a person's handle: their number where Signal shows it, else their ACI (a PNI's, where
// Signal told it), else the id itself.
func (p *people) handle(id string) archive.Handle {
	if c := p.contact(id); c.phone != "" {
		if k, v := archive.Address(c.phone, config.Region); k == "phone" {
			return archive.H(k, v)
		}
	}
	if r := p.pnis[id]; r.aci != "" {
		return archive.H("id", r.aci, Service)
	}
	return archive.H("id", id, Service)
}

// person is someone in a notice: the owner, or their address ("": nobody).
func (p *people) person(a *archive.Archive, id, own string) map[string]any {
	switch id {
	case "":
		return nil
	case own:
		return map[string]any{"self": true}
	}
	return map[string]any{"address": a.Address(p.handle(id))}
}

// noticeOf is what a message's notice says for the interface (nil: it has none; the codes are in
// docs/design.md), and the key of the message it is about ("" for none).
func noticeOf(a *archive.Archive, p *people, own, author string, e event, text string) (*archive.Notice, string) {
	by := p.person(a, author, own)
	notice := func(code string, args map[string]any) *archive.Notice {
		if args == nil {
			args = map[string]any{}
		}
		args["by"] = by
		return &archive.Notice{Code: code, Args: args}
	}
	var pin, unpin pinEv
	hasPin := len(e.PinRaw) > 0 && string(e.PinRaw) != "null" && json.Unmarshal(e.PinRaw, &pin) == nil
	hasUnpin := len(e.UnpinRaw) > 0 && string(e.UnpinRaw) != "null" && json.Unmarshal(e.UnpinRaw, &unpin) == nil
	target := func(t pinEv) string {
		if t.TargetAuthor == nil {
			return ""
		}
		return Key(*t.TargetAuthor, t.TargetTS)
	}
	switch {
	case e.GroupChanges != nil:
		actions := []any{}
		for _, act := range e.GroupChanges.Actions {
			out := map[string]any{}
			for k, v := range act {
				if who, ok := v.(string); ok && k == "who" {
					out[k] = p.person(a, who, own)
				} else {
					out[k] = v
				}
			}
			actions = append(actions, out)
		}
		n := notice("group", map[string]any{"actions": actions})
		n.Args["by"] = p.person(a, e.GroupChanges.Editor, own)
		return n, ""
	case e.GroupChange && text == "": // a change that could not be read
		return notice("group", map[string]any{"actions": []any{}}), ""
	case e.GroupCall:
		return notice("group_call", nil), ""
	case hasPin:
		return notice("pin", map[string]any{"seconds": pin.Seconds}), target(pin)
	case hasUnpin:
		return notice("unpin", nil), target(unpin)
	case e.ExpireUpdate && text == "":
		return notice("timer", map[string]any{"seconds": e.ExpireTimer}), ""
	case e.Payment:
		return notice("payment", nil), ""
	case e.Gift:
		return notice("gift", nil), ""
	case e.Unsupported:
		return notice("unsupported", nil), ""
	case e.PollEnd != nil:
		return notice("poll_end", nil), Key(author, e.PollEnd.TargetTS)
	case e.StoryReaction != nil:
		return notice("story_reaction", map[string]any{"emoji": *e.StoryReaction}), ""
	case e.StoryReply:
		return notice("story_reply", nil), ""
	}
	return nil, ""
}

// pollNotice is a poll with its votes as they are now: its question and options, each with how
// many chose it (each voter's newest vote).
func pollNotice(e event, votes [][]int64, ended bool) *archive.Notice {
	counts := make([]int, len(e.Poll.Options))
	voters := 0
	for _, v := range votes {
		if len(v) > 0 { // a vote taken back has none
			voters++
		}
		for _, i := range v {
			if i >= 0 && int(i) < len(counts) {
				counts[i]++
			}
		}
	}
	options := []any{}
	for i, o := range e.Poll.Options {
		options = append(options, map[string]any{"text": o, "votes": counts[i]})
	}
	return &archive.Notice{Code: "poll", Args: map[string]any{"question": deref(e.Poll.Question), "options": options,
		"multiple": e.Poll.Multiple, "voters": voters, "ended": ended}}
}

// mentionName is how a text names someone it mentions: their name, else their number, else the
// start of their ACI.
func (p *people) mentionName(aci string) string {
	c := p.contact(aci)
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

// asEdited puts an edit's content (its event, "" for none) into a message's event; it says whether
// there was one.
func asEdited(e *event, edit string) bool {
	var ed event
	if edit == "" || json.Unmarshal([]byte(edit), &ed) != nil {
		return false
	}
	e.Text, e.Mentions = ed.Text, ed.Mentions
	if len(ed.Attachments) > 0 {
		e.Attachments = ed.Attachments
	}
	return true
}

// describe is a message's kind, extras and text, from the helper's event.
func describe(e event) (string, *archive.Extras, string) {
	x := &archive.Extras{Forwarded: e.Forwarded}
	text := deref(e.Text)
	sub := func(subtype, code string) { x.Subtype, x.SubtypeCode = subtype, code }
	kind := "text"
	// a long message's text as a file (not fetched yet) is its text, not a file of it
	atts := slices.DeleteFunc(slices.Clone(e.Attachments), func(a attachmentEv) bool {
		return strings.EqualFold(deref(a.ContentType), "text/x-signal-plain")
	})
	switch {
	case len(atts) > 0:
		a := atts[0]
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
			} else if o := strings.TrimSpace(deref(c.Organization)); o != "" { // as Signal names a card
				parts = append(parts, o)
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
	case e.StoryReaction != nil:
		text = *e.StoryReaction
	case e.Unsupported:
		kind = "system"
		sub("notice", "signal:unsupported")
	case e.PollEnd != nil:
		kind = "system"
		sub("poll", "signal:poll_end")
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
		if x.ReplyText == "" && len(e.Quote.Attachments) > 0 { // a reply to a file: what it was
			q := e.Quote.Attachments[0]
			ct := deref(q.ContentType)
			switch {
			case deref(q.Filename) != "":
				x.ReplyText = "📎 " + deref(q.Filename)
			case strings.HasPrefix(ct, "image/"):
				x.ReplyText = "📷"
			case strings.HasPrefix(ct, "video/"):
				x.ReplyText = "🎥"
			case strings.HasPrefix(ct, "audio/"):
				x.ReplyText = "🎤"
			default:
				x.ReplyText = "📎"
			}
		}
	}
	if text == "" && len(atts) > 0 {
		// a sticker's emoji, or a file's own caption: words where the message has none (and what is
		// left of a sticker whose file never came)
		text = strings.TrimSpace(cmp.Or(deref(atts[0].Emoji), deref(atts[0].Caption)))
		if text == "" && kind == "file" && deref(atts[0].Filename) != "" { // a file: its name, as Signal shows it
			text = "📎 " + deref(atts[0].Filename)
		}
	}
	if e.ViewOnce && x.SubtypeCode == "" { // to be seen once, kept here: said as such
		x.SubtypeCode = "signal:view_once"
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
	ids := p.ids()
	for _, id := range ids { // each id joins the person of their number (or of the ACI of a PNI)
		if h := p.handle(id); h != archive.H("id", id, Service) {
			a.Alias(archive.H("id", id, Service), h)
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
			h := p.handle(chat)
			id = a.Conversation(Service, []archive.Handle{h}, chat, "")
			// one person's chat has them alone: an id it was kept under before Signal told their
			// number (or a PNI's ACI) leaves it, so that the chat meets theirs on other services;
			// that id's own person, if it became one, stays as it is (merging people is the owner's)
			a.Exec("DELETE FROM conversation_member WHERE conversation_id = ? AND address_id != ? AND address_id IN "+
				"(SELECT x.id FROM address x JOIN address_kind k ON k.id = x.kind_id WHERE k.name = 'id' AND x.service_id = ?)",
				id, a.Address(h), a.Service.ID(Service))
		}
		convs[kind+"\x00"+chat] = id
		return id
	}
	for g := range titles {
		if !skip[g] {
			conv("group", g)
		}
	}

	// an edit's own time names its message too: Signal's apps aim the next edit (and may aim a
	// deletion, a reaction or a receipt) at the last edit's time, followed back here to the message
	type editRow struct {
		author     string
		target, ts int64
		js         string
	}
	var editRows []editRow
	db.Each(d, "SELECT author, target_ts, ts, json FROM edit ORDER BY ts", nil, func(scan func(...any)) {
		var r editRow
		scan(&r.author, &r.target, &r.ts, &r.js)
		editRows = append(editRows, r)
	})
	edited := map[string]string{} // an edit's key: the key of what it edited
	for _, r := range editRows {
		if r.ts != r.target {
			edited[Key(r.author, r.ts)] = Key(r.author, r.target)
		}
	}
	root := func(k string) string {
		for range len(edited) {
			to, ok := edited[k]
			if !ok {
				break
			}
			k = to
		}
		return k
	}
	edits := map[string]string{} // the newest edit of each message
	for _, r := range editRows {
		edits[root(Key(r.author, r.target))] = r.js
	}
	deleted := map[string]bool{}
	for _, k := range db.Strs(d, "SELECT author || ':' || ts FROM deletion") {
		deleted[root(k)] = true
	}

	var rows []msgRow
	db.Each(d, "SELECT author, ts, chat_kind, chat, outgoing, json FROM message ORDER BY ts, author", nil,
		func(scan func(...any)) {
			var r msgRow
			scan(&r.author, &r.ts, &r.chatKind, &r.chat, &r.outgoing, &r.js)
			rows = append(rows, r)
		})
	files := importers.NewStore(a)
	linkFiles := func(mid int64, atts []attachmentEv) {
		for _, at := range atts {
			f := deref(at.File)
			if f == "" || f != filepath.Base(f) || f == "." || f == ".." {
				continue // only files within the helper's folder
			}
			files.LinkNamed(SourceName(own), src, filepath.Join(mediaDir, f), f, mid, deref(at.Filename))
		}
	}
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
			// the names of its files, linked before names were kept
			if strings.Contains(r.js, `"filename":"`) {
				var e event
				if mid, ok := a.MessageByKey(Service, key, cid); ok && json.Unmarshal([]byte(r.js), &e) == nil {
					linkFiles(mid, e.Attachments)
				}
			}
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
		edited := asEdited(&e, edits[key]) // its content as last edited
		kind, x, text := describe(e)
		if n, about := noticeOf(a, p, own, r.author, e, text); n != nil {
			x.Notice = n
			if about != "" {
				x.ReplyKey = about
			}
		}
		if x.ReplyKey != "" { // a reply to an edited message names one of its versions: the message is the first
			x.ReplyKey = root(x.ReplyKey)
		}
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
		linkFiles(mid, e.Attachments)
	}
	// messages that could not be decrypted and that their sender did not send again within a day: a
	// note where they were (the owner's own, from another device, have no chat known)
	db.Each(d, "SELECT u.author, u.ts, u.chat_kind, u.chat FROM unreadable u WHERE u.at < ? AND u.author != ? "+
		"AND NOT EXISTS (SELECT 1 FROM message m WHERE m.author = u.author AND m.ts = u.ts) ORDER BY u.ts, u.author",
		[]any{time.Now().Add(-24 * time.Hour).Unix(), own}, func(scan func(...any)) {
			var r msgRow
			scan(&r.author, &r.ts, &r.chatKind, &r.chat)
			key := "unreadable:" + Key(r.author, r.ts)
			if skip[r.chat] || a.HasOrigin(src, key, "") {
				return
			}
			a.AddMessage(src, key, archive.Message{Service: Service, ConversationID: conv(r.chatKind, r.chat), TS: r.ts,
				SenderID: a.Address(p.handle(r.author)), Kind: "system", Key: key,
				Extras: &archive.Extras{Subtype: "notice", SubtypeCode: "signal:unreadable",
					Notice: &archive.Notice{Code: "unreadable", Args: map[string]any{"by": p.person(a, r.author, own)}}}})
			n.Messages++
		})
	// polls with their votes as they are now
	votes := map[string][][]int64{}
	db.Each(d, "SELECT target_author, target_ts, options FROM poll_vote ORDER BY voter", nil, func(scan func(...any)) {
		var author, opts string
		var ts int64
		scan(&author, &ts, &opts)
		var v []int64
		if json.Unmarshal([]byte(opts), &v) == nil {
			votes[Key(author, ts)] = append(votes[Key(author, ts)], v)
		}
	})
	ended := map[string]bool{} // polls their authors closed
	for _, r := range rows {
		var e event
		if strings.Contains(r.js, `"poll_end":{`) && json.Unmarshal([]byte(r.js), &e) == nil && e.PollEnd != nil {
			ended[Key(r.author, e.PollEnd.TargetTS)] = true
		}
	}
	for _, r := range rows {
		if !strings.Contains(r.js, `"poll":{`) {
			continue
		}
		var e event
		key := Key(r.author, r.ts)
		if json.Unmarshal([]byte(r.js), &e) != nil || e.Poll == nil {
			continue
		}
		mid, ok := a.MessageByKey(Service, key, 0)
		if !ok {
			continue
		}
		n := pollNotice(e, votes[key], ended[key])
		js, _ := json.Marshal(n.Args)
		var now string
		a.Row("SELECT args FROM notice WHERE message_id = ?", []any{mid}, &now)
		if now != string(js) { // written only when the votes changed
			a.SetNotice(mid, n)
		}
	}
	// files (and a long message's whole text) that came after their message was imported (fetched
	// again)
	db.Each(d, "SELECT l.author, l.ts, m.json FROM late_file l JOIN message m ON m.author = l.author AND m.ts = l.ts "+
		"ORDER BY l.ts, l.author", nil, func(scan func(...any)) {
		var r msgRow
		scan(&r.author, &r.ts, &r.js)
		mid, ok := a.MessageByKey(Service, Key(r.author, r.ts), 0)
		var e event
		if !ok || json.Unmarshal([]byte(r.js), &e) != nil {
			return
		}
		linkFiles(mid, e.Attachments)
		if e.LongText && edits[Key(r.author, r.ts)] == "" { // its whole text, fetched after it was imported
			_, _, text := describe(e)
			text, _ = withMentions(text, e.Mentions, p.mentionName)
			n.Changes += len(importers.ApplyChange(a, mid, importers.Change{Text: &text}))
		}
	})

	n.Changes += changes(a, d, p, own, ownSet, edits, deleted, root, linkFiles)
	n.Files = files.Added[SourceName(own)]
	receipts(a, d, p, own, ownSet, root)
	reads(a, d, src)
	n.Calls += calls(a, d, p, src, conv, skip)

	for _, aci := range ids { // the names Signal shows, for the handles in the archive
		c, ok := p.contacts[aci]
		if !ok {
			continue // a PNI: its ACI's names are said under the ACI
		}
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
	importers.ApplyPins(a, Service)
	a.Imported(src)
	a.Commit()
	return n, nil
}

// changes: what happened to messages the archive has: edits and deletions (marked), and reactions
// as they are now (one per person: changed, added, or removed once taken back). It returns how many.
func changes(a *archive.Archive, d db.Querier, p *people, own string, ownSet map[archive.Handle]bool,
	edits map[string]string, deleted map[string]bool, root func(string) string, linkFiles func(int64, []attachmentEv)) int {
	n := 0
	var ek, dk []string
	for k := range edits {
		ek = append(ek, k)
	}
	for k := range deleted {
		dk = append(dk, k)
	}
	sort.Strings(ek)
	sort.Strings(dk)
	// an edit: the message's text as last edited (as an import of it would have it, its mentions
	// too), its files linked
	for _, k := range ek {
		mid, ok := a.MessageByKey(Service, k, 0)
		author, ts, _ := SplitKey(k)
		var js string
		if !ok || !db.Row(d, "SELECT json FROM message WHERE author = ? AND ts = ?", []any{author, ts}, &js) {
			continue
		}
		var e event
		if json.Unmarshal([]byte(js), &e) != nil || !asEdited(&e, edits[k]) {
			continue
		}
		_, x, text := describe(e)
		text, toks := withMentions(text, e.Mentions, p.mentionName)
		if x.Text != "" {
			text = x.Text
		}
		changed := importers.ApplyChange(a, mid, importers.Change{Text: &text, Edited: true})
		if slices.Contains(changed, "text") {
			a.Exec("DELETE FROM mention WHERE message_id = ?", mid)
			for _, t := range toks {
				a.Exec("INSERT OR IGNORE INTO mention VALUES (?, ?, ?)", mid, a.Address(p.handle(t.aci)), t.text)
			}
		}
		n += len(changed)
		linkFiles(mid, e.Attachments)
	}
	for _, k := range dk {
		if mid, ok := a.MessageByKey(Service, k, 0); ok {
			n += len(importers.ApplyChange(a, mid, importers.Change{Deleted: true}))
		}
	}
	type reaction struct {
		sender string
		emoji  *string
	}
	byTarget := map[string][]reaction{}
	var targets []string
	// in the order they were made: one person's reactions aimed at different times of an edited
	// message are one, the newest last
	db.Each(d, "SELECT target_author, target_ts, sender, emoji FROM reaction ORDER BY ts, target_author, target_ts, sender",
		nil, func(scan func(...any)) {
			var author, sender string
			var ts int64
			var emoji *string
			scan(&author, &ts, &sender, &emoji)
			k := root(Key(author, ts))
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
func receipts(a *archive.Archive, d db.Querier, p *people, own string, ownSet map[archive.Handle]bool, root func(string) string) {
	db.Each(d, "SELECT ts, aci, kind, at FROM receipt ORDER BY ts, aci, kind", nil, func(scan func(...any)) {
		var ts, at int64
		var aci, kind string
		scan(&ts, &aci, &kind, &at)
		col, ok := receiptColumn[kind]
		h := p.handle(aci)
		if !ok || aci == own || ownSet[h] {
			return
		}
		mid, found := a.MessageByKey(Service, root(Key(own, ts)), 0)
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
