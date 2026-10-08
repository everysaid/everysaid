// Ports everysaid/whatsapp.py: WhatsApp from the iPhone's ChatStorage.sqlite and the live bridge's
// messages.db (config [whatsapp] bridge). Either may be missing.
//
// Messages are matched by stanza id (the message id WhatsApp sends, the same on every device). The
// iPhone is read first; the bridge adds only what the archive does not have yet (what arrived after
// the last iPhone backup). People are stored by phone number: WhatsApp's LIDs (`...@lid`) are mapped
// to numbers through the iPhone's WhatsApp contacts and the bridge's whatsmeow_lid_map; LIDs with no
// known number stay as they are. Groups are keyed by their jid. Channels (`@newsletter`) and status
// (`status@broadcast`) are not wanted and not imported.
package importers

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
)

// WhatsAppIphoneDB and the others are where the iPhone's WhatsApp databases are.
func WhatsAppIphoneDB() string { return filepath.Join(archive.IphoneData(), "whatsapp.sqlite") }
func WhatsAppContactsDB() string {
	return filepath.Join(archive.IphoneData(), "whatsapp-contacts.sqlite")
}

// BridgeDB and BridgeStore are the bridge's databases, where config names a bridge ("" otherwise).
func BridgeDB() string {
	if config.WhatsappBridge == "" {
		return ""
	}
	return filepath.Join(config.WhatsappBridge, "messages.db")
}

func BridgeStore() string {
	if config.WhatsappBridge == "" {
		return ""
	}
	return filepath.Join(config.WhatsappBridge, "whatsapp.db")
}

// ZWAMESSAGE.ZMESSAGETYPE, from the files each type carries (`file`, `ffprobe`) and its metadata:
// 11 are GIFs (silent mp4), 54 round video notes (square, with sound), 14 deleted messages; 19, 20,
// 25, 30, 31 and 41 are business messages (templates, buttons) whose text is only in the media
// item's protobuf metadata. Anything else (10, 28, 59, 66, ...) carries nothing and is 'system'.
var iphoneKinds = map[int64]string{0: "text", 7: "text", 1: "image", 11: "image", 38: "image", 2: "video", 39: "video",
	54: "video", 3: "voice", 4: "contact", 5: "location", 8: "file", 15: "sticker",
	19: "text", 20: "text", 25: "text", 30: "text", 31: "text", 41: "text"}

const metadataText = "(19, 20, 25, 30, 31, 41)"

// The bridge's kinds (messages.kind); a bridge from before that column says only its media type.
var bridgeKinds = map[string]string{"text": "text", "image": "image", "video": "video", "audio": "voice", "voice": "voice",
	"document": "file", "sticker": "sticker", "location": "location", "contact": "contact", "poll": "text"}
var bridgeMediaKinds = map[string]string{"": "text", "image": "image", "video": "video", "audio": "voice",
	"document": "file", "sticker": "sticker"}

// roBridge is a database that may not be there (the bridge's, or the iPhone's), or nil.
func roBridge(path string) *sql.DB {
	if path == "" || !exists(path) {
		return nil
	}
	return ro(path)
}

// selectAll is the columns of a table for a SELECT, its times as their text (the driver would read
// a TIMESTAMP column into a time of its own making).
func selectAll(q db.Querier, table, alias string) string {
	var cols []string
	for _, c := range maps(q, "PRAGMA table_info("+table+")") {
		name := str(c["name"])
		col := `"` + name + `"`
		if alias != "" {
			col = alias + "." + col
		}
		t := upper(str(c["type"]))
		if strings.Contains(t, "DATE") || strings.Contains(t, "TIME") {
			col = "CAST(" + col + ` AS TEXT) AS "` + name + `"`
		}
		cols = append(cols, col)
	}
	return strings.Join(cols, ", ")
}

// bridgeMembers adds the groups' members as the bridge last read them to their conversations (one
// who left stays: a member once).
func bridgeMembers(a *archive.Archive, bridge *sql.DB, person *waPeople, own map[archive.Handle]bool) {
	if !hasTable(bridge, "group_members") {
		return
	}
	for _, r := range maps(bridge, "SELECT group_jid, jid FROM group_members") {
		conv, ok := a.FindConversation("whatsapp", str(r["group_jid"]))
		p, pok := person.of(str(r["jid"]))
		if ok && conv != 0 && pok && !own[p] {
			a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", conv, a.Address(p))
		}
	}
}

// bridgeBlocked: the people the account blocked on WhatsApp, as the bridge last heard.
func bridgeBlocked(a *archive.Archive, bridge *sql.DB, person *waPeople, own map[archive.Handle]bool) {
	if !hasTable(bridge, "blocklist") {
		return
	}
	var handles []archive.Handle
	for _, r := range maps(bridge, "SELECT jid FROM blocklist ORDER BY jid") {
		if p, ok := person.of(str(r["jid"])); ok && !own[p] {
			handles = append(handles, p)
		}
	}
	a.SetBlocked("whatsapp", handles, nil)
}

// bridgeMentions: whom each message names with @, also on the messages the archive has from the
// iPhone.
func bridgeMentions(a *archive.Archive, bridge *sql.DB, person *waPeople) {
	if !columns(bridge, "messages")["mentions"] {
		return
	}
	for _, r := range maps(bridge, "SELECT id, mentions FROM messages WHERE coalesce(mentions, '') != ''") {
		key, isStr := r["id"].(string)
		if !isStr {
			continue
		}
		mid, ok := a.MessageByKey("whatsapp", key, 0)
		if !ok || mid == 0 {
			continue
		}
		for _, jid := range strings.Split(str(r["mentions"]), ",") {
			if p, ok := person.of(jid); ok {
				a.Exec("INSERT OR IGNORE INTO mention VALUES (?, ?, ?)", mid, a.Address(p), "@"+strings.SplitN(jid, "@", 2)[0])
			}
		}
	}
}

// bridgeReceipts: who got, read and played the owner's messages, and when (the first time each).
func bridgeReceipts(a *archive.Archive, bridge *sql.DB, person *waPeople, own map[archive.Handle]bool) {
	if !hasTable(bridge, "receipts") {
		return
	}
	for _, r := range maps(bridge, "SELECT message_id, jid, type, CAST(timestamp AS TEXT) AS ts FROM receipts") {
		kind := str(r["type"])
		if kind != "delivered" && kind != "read" && kind != "played" {
			continue
		}
		key, isStr := r["message_id"].(string)
		mid, ok := int64(0), false
		if isStr {
			mid, ok = a.MessageByKey("whatsapp", key, 0)
		}
		p, pok := person.of(str(r["jid"]))
		if !ok || mid == 0 || !pok || own[p] {
			continue
		}
		aid := a.Address(p)
		stamp := isoMS(str(r["ts"]))
		a.Exec("INSERT OR IGNORE INTO receipt (message_id, address_id) SELECT id, ? FROM message WHERE id = ? AND outgoing", aid, mid)
		a.Exec(fmt.Sprintf("UPDATE receipt SET %[1]s_at = ? WHERE message_id = ? AND address_id = ? "+
			"AND (coalesce(%[1]s_at, 0) = 0 OR (%[1]s_at > ? AND ? > 0))", kind), stamp, mid, aid, stamp, stamp)
	}
}

type readAt struct{ newest, when int64 }

// bridgeRead is chat jid -> (the newest message the owner read there, when), Unix ms, from the
// bridge's read_at; in the order the chats come.
func bridgeRead(bridge *sql.DB) ([]string, map[string]readAt) {
	out := map[string]readAt{}
	var order []string
	if !columns(bridge, "messages")["read_at"] {
		return order, out
	}
	for _, r := range maps(bridge, "SELECT chat_jid, CAST(timestamp AS TEXT) AS ts, CAST(read_at AS TEXT) AS ra "+
		"FROM messages WHERE read_at IS NOT NULL") {
		jid := str(r["chat_jid"])
		cur, seen := out[jid]
		if !seen {
			order = append(order, jid)
		}
		out[jid] = readAt{max(cur.newest, isoMS(str(r["ts"]))), max(cur.when, isoMS(str(r["ra"])))}
	}
	return order, out
}

type reactionKey struct{ chat, message string }

// bridgeChanges: what the bridge saw happen to messages the archive already has (from it or from
// the iPhone: the same stanza id): an edit's new text, marked edited; a deletion marked (the text
// kept); and reactions as
// they are now (one per person: changed, added, or removed once taken back). Returns the counts.
func bridgeChanges(a *archive.Archive, bridge *sql.DB, person *waPeople, own map[archive.Handle]bool,
	order []reactionKey, reactions map[reactionKey][]bridgeReaction) map[string]int {
	out := map[string]int{}
	cols := columns(bridge, "messages")
	if cols["edited"] && cols["deleted"] {
		for _, r := range maps(bridge, "SELECT "+selectAll(bridge, "messages", "")+" FROM messages WHERE edited OR deleted") {
			key, isStr := r["id"].(string)
			if !isStr {
				continue
			}
			mid, ok := a.MessageByKey("whatsapp", key, 0)
			if !ok || mid == 0 {
				continue
			}
			c := Change{Edited: truthy(r["edited"]), Deleted: truthy(r["deleted"])}
			if c.Edited && !c.Deleted { // content is the last version: the text as the import makes it
				t := whatsappBridgeExtras(r, nil).Text
				if t == "" {
					t = str(r["content"])
				}
				if t != "" {
					c.Text = &t
				}
			}
			for _, k := range ApplyChange(a, mid, c) {
				out[k]++
			}
		}
	}
	for _, k := range order {
		mid, ok := a.MessageByKey("whatsapp", k.message, 0)
		if !ok || mid == 0 {
			continue
		}
		for _, x := range reactions[k] {
			mine := x.mine
			var p archive.Handle
			pok := false
			if !mine {
				p, pok = person.of(str(x.jid))
			}
			mine = mine || (pok && own[p])
			var who int64
			if !mine && pok {
				who = a.Address(p)
			}
			if !mine && who == 0 {
				continue
			}
			var oldID int64
			var oldEmoji sql.NullString
			var found bool
			if mine {
				found = a.Row("SELECT rowid, emoji FROM reaction WHERE message_id = ? AND outgoing = 1", []any{mid}, &oldID, &oldEmoji)
			} else {
				found = a.Row("SELECT rowid, emoji FROM reaction WHERE message_id = ? AND address_id = ?", []any{mid, who}, &oldID, &oldEmoji)
			}
			switch {
			case x.emoji == "":
				if found {
					a.Exec("DELETE FROM reaction WHERE rowid = ?", oldID)
					out["reactions"]++
				}
			case !found:
				var o any
				if mine {
					o = 1
				}
				a.Exec("INSERT INTO reaction (message_id, emoji, count, address_id, outgoing) VALUES (?, ?, 1, ?, ?)",
					mid, x.emoji, archive.NullID(who), o)
				out["reactions"]++
			case !oldEmoji.Valid || oldEmoji.String != x.emoji:
				a.Exec("UPDATE reaction SET emoji = ?, code = NULL WHERE rowid = ?", x.emoji, oldID)
				out["reactions"]++
			}
		}
	}
	return out
}

// --- protobuf ------------------------------------------------------------------------------------

// varint reads a varint at i; false where the data ends first.
func varint(data []byte, i int) (uint64, int, bool) {
	var n uint64
	var shift uint
	for {
		if i >= len(data) {
			return 0, i, false
		}
		b := data[i]
		if shift < 64 {
			n |= uint64(b&0x7F) << shift
		}
		i++
		if b < 0x80 {
			return n, i, true
		}
		shift += 7
	}
}

// span is data[i:i+n] and i+n, clamped where it runs past the data (as a slice past its end is, in Python).
func span(data []byte, i int, n uint64) ([]byte, int) {
	if n > uint64(len(data)-min(i, len(data))) {
		if i > len(data) {
			return nil, len(data) + 1
		}
		return data[i:], len(data) + 1
	}
	return data[i : i+int(n)], i + int(n)
}

// protobufStrings are the readable strings in a protobuf message, nested ones included, in order;
// nil (and false) where it is not one.
func protobufStrings(data []byte, depth int) ([]string, bool) {
	out := []string{}
	i := 0
	for i < len(data) {
		key, j, ok := varint(data, i)
		if !ok {
			return nil, false
		}
		i = j
		switch key & 7 {
		case 0:
			if _, i, ok = varint(data, i); !ok {
				return nil, false
			}
		case 1:
			i += 8
		case 5:
			i += 4
		case 2:
			n, j, ok := varint(data, i)
			if !ok {
				return nil, false
			}
			var chunk []byte
			chunk, i = span(data, j, n)
			var nested []string
			if depth < 8 {
				nested, _ = protobufStrings(chunk, depth+1)
			}
			if len(nested) > 0 {
				out = append(out, nested...)
			} else {
				if !utf8.Valid(chunk) {
					continue
				}
				text := string(chunk)
				if printable(text) || strings.Contains(text, "\n") {
					out = append(out, text)
				}
			}
		default:
			return nil, false
		}
		if i > len(data) {
			return nil, false
		}
	}
	return out, true
}

// protobufFields is field number -> its values, one level deep: uint64, or []byte for nested
// messages and strings.
func protobufFields(data []byte) map[uint64][]any {
	out := map[uint64][]any{}
	i := 0
	for i < len(data) {
		key, j, ok := varint(data, i)
		if !ok {
			break
		}
		i = j
		field := key >> 3
		var value any
		switch key & 7 {
		case 0:
			v, j, ok := varint(data, i)
			if !ok {
				return out
			}
			value, i = v, j
		case 1:
			value, i = span(data, i, 8)
		case 5:
			value, i = span(data, i, 4)
		case 2:
			n, j, ok := varint(data, i)
			if !ok {
				return out
			}
			value, i = span(data, j, n)
		default:
			return out
		}
		out[field] = append(out[field], value)
	}
	return out
}

// printable is Python's str.isprintable().
func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func metadataTextOf(blob []byte) string {
	strs, _ := protobufStrings(blob, 0)
	// Sentences, not the ids, hashes, mime types and urls around them.
	seen := map[string]bool{}
	var lines []string
	for _, s := range strs {
		t := pyStrip(s)
		if (strings.Contains(t, " ") || !isASCII(s)) && !strings.HasPrefix(s, "http") && !strings.HasPrefix(s, "/v/") &&
			!strings.HasSuffix(s, "@s.whatsapp.net") && !strings.HasSuffix(s, "@lid") && !seen[t] {
			seen[t] = true
			lines = append(lines, t)
		}
	}
	return strings.Join(lines, "\n")
}

// --- people --------------------------------------------------------------------------------------

func user(jid string) string {
	u, _, _ := strings.Cut(jid, "@")
	u, _, _ = strings.Cut(u, ":")
	return u
}

type waName struct {
	h    archive.Handle
	kind string
}

// waPeople maps a jid (or the bridge's bare user part) to a handle.
type waPeople struct {
	lidPhone map[string]string
	lids     map[string]bool
	names    map[waName]string
	order    []waName
}

// newWAPeople: each may be nil: the iPhone's contacts and messages, the bridge's store and messages.
func newWAPeople(contacts, store, iphone, bridge *sql.DB) *waPeople {
	p := &waPeople{lidPhone: map[string]string{}, lids: map[string]bool{}, names: map[waName]string{}}
	if contacts != nil {
		for _, r := range maps(contacts, "SELECT ZLID, ZWHATSAPPID FROM ZWAADDRESSBOOKCONTACT "+
			"WHERE ZLID IS NOT NULL AND ZWHATSAPPID IS NOT NULL") {
			p.lidPhone[user(str(r["ZLID"]))] = user(str(r["ZWHATSAPPID"]))
		}
	}
	if store != nil {
		for _, r := range maps(store, "SELECT lid, pn FROM whatsmeow_lid_map") {
			if _, ok := p.lidPhone[str(r["lid"])]; !ok {
				p.lidPhone[str(r["lid"])] = str(r["pn"])
			}
		}
	}
	if bridge != nil && hasTable(bridge, "group_members") { // the groups' members, named both ways
		for _, r := range maps(bridge, "SELECT lid, phone FROM group_members WHERE lid != '' AND phone != ''") {
			if _, ok := p.lidPhone[user(str(r["lid"]))]; !ok {
				p.lidPhone[user(str(r["lid"]))] = user(str(r["phone"]))
			}
		}
	}
	// Every LID seen anywhere, to tell them from phone numbers in the bridge's bare senders.
	for k := range p.lidPhone {
		p.lids[k] = true
	}
	if iphone != nil {
		for _, jid := range db.Strs(iphone, "SELECT ZFROMJID FROM ZWAMESSAGE WHERE ZFROMJID LIKE '%@lid' UNION "+
			"SELECT ZMEMBERJID FROM ZWAGROUPMEMBER WHERE ZMEMBERJID LIKE '%@lid' UNION "+
			"SELECT ZCONTACTJID FROM ZWACHATSESSION WHERE ZCONTACTJID LIKE '%@lid'") {
			p.lids[user(jid)] = true
		}
	}
	if bridge != nil {
		for _, jid := range db.Strs(bridge, "SELECT chat_jid FROM messages WHERE chat_jid LIKE '%@lid' UNION "+
			"SELECT sender FROM messages WHERE sender LIKE '%@lid'") {
			p.lids[user(jid)] = true
		}
	}
	// The names WhatsApp shows for people, by kind (book: its copy of the phone's address book;
	// chat: a chat's name; profile: the name they chose, a push name): one per handle and kind,
	// the first found.
	type named struct{ jid, name any }
	var rows []struct {
		named
		kind string
	}
	add := func(q *sql.DB, query, kind string) {
		for _, r := range maps(q, query) {
			var j, n any
			for k, v := range r {
				if k == "j" {
					j = v
				} else {
					n = v
				}
			}
			rows = append(rows, struct {
				named
				kind string
			}{named{j, n}, kind})
		}
	}
	if contacts != nil {
		add(contacts, "SELECT ZWHATSAPPID AS j, ZFULLNAME AS n FROM ZWAADDRESSBOOKCONTACT UNION ALL "+
			"SELECT ZLID, ZFULLNAME FROM ZWAADDRESSBOOKCONTACT", "book")
	}
	if store != nil {
		add(store, "SELECT their_jid AS j, full_name AS n FROM whatsmeow_contacts", "book")
	}
	if iphone != nil {
		add(iphone, "SELECT ZCONTACTJID AS j, ZPARTNERNAME AS n FROM ZWACHATSESSION "+
			"WHERE ZCONTACTJID LIKE '%@s.whatsapp.net' OR ZCONTACTJID LIKE '%@lid'", "chat")
		add(iphone, "SELECT ZJID AS j, ZPUSHNAME AS n FROM ZWAPROFILEPUSHNAME", "profile")
	}
	if bridge != nil {
		add(bridge, "SELECT jid AS j, name AS n FROM chats WHERE jid LIKE '%@s.whatsapp.net' OR jid LIKE '%@lid'", "chat")
	}
	if store != nil {
		add(store, "SELECT their_jid AS j, push_name AS n FROM whatsmeow_contacts", "profile")
	}
	for _, r := range rows {
		if !truthy(r.jid) {
			continue
		}
		jid := pyStr(r.jid)
		if !strings.Contains(jid, "@") {
			jid += "@s.whatsapp.net"
		}
		h, ok := p.of(jid)
		if ok && truthy(r.name) {
			k := waName{h, r.kind}
			if _, seen := p.names[k]; !seen {
				p.names[k] = pyStr(r.name)
				p.order = append(p.order, k)
			}
		}
	}
	return p
}

// conversationKey is the archive's key of a WhatsApp chat: a group's jid, a person's handle value.
func (p *waPeople) conversationKey(jid string) string {
	if strings.HasSuffix(jid, "@s.whatsapp.net") || strings.HasSuffix(jid, "@lid") {
		if h, ok := p.of(jid); ok {
			return h.Value
		}
		return jid
	}
	return jid
}

// of is the handle of a jid; false for none.
func (p *waPeople) of(jid string) (archive.Handle, bool) {
	if jid == "" {
		return archive.Handle{}, false
	}
	u := user(jid)
	if strings.HasSuffix(jid, "@lid") || (!strings.Contains(jid, "@") && p.lids[u]) {
		if pn, ok := p.lidPhone[u]; ok {
			k, v := archive.Address(pn, config.Region)
			return archive.H(k, v), true
		}
		return archive.H("id", u+"@lid", "whatsapp"), true
	}
	if strings.HasSuffix(jid, "@s.whatsapp.net") || (!strings.Contains(jid, "@") && utf8.RuneCountInString(u) <= 15) { // longer: no phone (E.164)
		k, v := archive.Address(u, config.Region)
		return archive.H(k, v), true
	}
	return archive.Handle{}, false
}

// channels, and status (all of it, and each contact's own: "<number>@status", "<lid>@lid.status"):
// not wanted, not imported.
var waChannels = []string{"@newsletter", "status@broadcast", "@status", ".status"}

func isWAChannel(jid string) bool {
	for _, c := range waChannels {
		if strings.HasSuffix(jid, c) {
			return true
		}
	}
	return false
}

// waConversation is the conversation of a chat; 0 for a channel.
func waConversation(a *archive.Archive, person *waPeople, jid string, title any, members []archive.Handle) int64 {
	if isWAChannel(jid) {
		return 0
	}
	if strings.HasSuffix(jid, "@s.whatsapp.net") || strings.HasSuffix(jid, "@lid") {
		if h, ok := person.of(jid); ok {
			return a.Conversation("whatsapp", []archive.Handle{h}, "", "")
		}
		return a.Conversation("whatsapp", []archive.Handle{archive.H("id", jid, "whatsapp")}, "", "")
	}
	cid := titled(a, "whatsapp", members, jid, title)
	a.Exec("UPDATE conversation SET is_group = 1 WHERE id = ?", cid)
	return cid
}

// WhatsAppOptions: the databases; "" is the default one, and NoX leaves it out (Python's None).
type WhatsAppOptions struct {
	IphoneDB, ContactsDB, BridgeDB, StoreDB string
	NoIphone, NoContacts, NoBridge, NoStore bool
}

func pick(path string, none bool, def func() string) string {
	switch {
	case none:
		return ""
	case path != "":
		return path
	}
	return def()
}

// WhatsApp imports WhatsApp; it gives what the bridge changed on messages already there (edited,
// deleted, reactions: counts).
func WhatsApp(a *archive.Archive, out func(string), opt WhatsAppOptions) (updated map[string]int, err error) {
	defer archive.Recover(&err)
	iphoneDB := pick(opt.IphoneDB, opt.NoIphone, WhatsAppIphoneDB)
	contactsDB := pick(opt.ContactsDB, opt.NoContacts, WhatsAppContactsDB)
	bridgeDB := pick(opt.BridgeDB, opt.NoBridge, BridgeDB)
	storeDB := pick(opt.StoreDB, opt.NoStore, BridgeStore)
	iphone, bridge := roBridge(iphoneDB), roBridge(bridgeDB)
	for _, d := range []*sql.DB{iphone, bridge} {
		if d != nil {
			defer d.Close()
		}
	}
	if iphone == nil && bridge == nil {
		b := bridgeDB
		if b == "" {
			b = "[whatsapp] bridge"
		}
		say(out, "no source: neither {a} nor {b}", map[string]any{"a": iphoneDB, "b": b})
		return map[string]int{}, nil
	}
	contacts, store := roBridge(contactsDB), roBridge(storeDB)
	for _, d := range []*sql.DB{contacts, store} {
		if d != nil {
			defer d.Close()
		}
	}
	person := newWAPeople(contacts, store, iphone, bridge)
	own := a.Own()
	src := map[string]int64{}
	var srcOrder []string
	if iphone != nil {
		src["iphone"] = a.Source(archive.Iphone()+"/whatsapp", iphoneDB, archive.Iphone(), "")
		srcOrder = append(srcOrder, "iphone")
	}
	if bridge != nil {
		src["bridge"] = a.Source("whatsapp-bridge", bridgeDB, "whatsapp-bridge", "")
		srcOrder = append(srcOrder, "bridge")
	}
	added, skipped := map[string]int{}, map[string]int{}

	add := func(source, rowKey string, extra *archive.Extras, conv int64, ts int64, outgoing bool, sender *archive.Handle,
		kind, text string, key any) {
		if conv == 0 { // a channel
			return
		}
		if a.HasOrigin(src[source], rowKey, "") {
			return
		}
		k, isStr := key.(string)
		if isStr {
			if mid, ok := a.MessageByKey("whatsapp", k, 0); ok && mid != 0 {
				skipped[source]++
				return
			}
		}
		if extra != nil && len(extra.Reactions) > 0 { // the reactors are jids: people, or the owner
			for i, r := range extra.Reactions {
				var p archive.Handle
				pok := false
				if r.Who != nil {
					p, pok = person.of(str(r.Who))
				}
				mine := r.Who == nil || (pok && own[p])
				extra.Reactions[i].Outgoing = mine
				extra.Reactions[i].Who = nil
				if !mine && pok {
					extra.Reactions[i].Who = p
				}
			}
		}
		var senderID int64
		if sender != nil {
			senderID = a.Address(*sender)
		}
		a.AddMessage(src[source], rowKey, archive.Message{Service: "whatsapp", ConversationID: conv, TS: ts,
			Outgoing: outgoing, SenderID: senderID, Kind: kind, Text: text, Key: k, Extras: extra})
		added[source]++
	}

	// iPhone
	if iphone != nil {
		groupMembers := map[int64][]archive.Handle{}
		memberJID := map[int64]string{}
		for _, r := range maps(iphone, "SELECT Z_PK, ZCHATSESSION, ZMEMBERJID FROM ZWAGROUPMEMBER") {
			memberJID[toInt(r["Z_PK"])] = str(r["ZMEMBERJID"])
			p, ok := person.of(str(r["ZMEMBERJID"]))
			cs := toInt(r["ZCHATSESSION"])
			if ok && !own[p] && !containsHandle(groupMembers[cs], p) {
				groupMembers[cs] = append(groupMembers[cs], p)
			}
		}
		type session struct {
			jid  string
			conv int64
		}
		sessions := map[int64]session{}
		for _, s := range maps(iphone, "SELECT Z_PK, ZCONTACTJID, ZPARTNERNAME, ZSESSIONTYPE FROM ZWACHATSESSION") {
			pk := toInt(s["Z_PK"])
			jid := str(s["ZCONTACTJID"])
			if jid == "" {
				jid = fmt.Sprintf("session:%d", pk)
			}
			sessions[pk] = session{str(s["ZCONTACTJID"]), waConversation(a, person, jid, s["ZPARTNERNAME"], groupMembers[pk])}
		}
		metadata := map[int64][]byte{}
		for _, r := range maps(iphone, "SELECT m.Z_PK, i.ZMETADATA FROM ZWAMESSAGE m JOIN ZWAMEDIAITEM i ON i.Z_PK = m.ZMEDIAITEM "+
			"WHERE m.ZMESSAGETYPE IN "+metadataText) {
			metadata[toInt(r["Z_PK"])] = asBytes(r["ZMETADATA"])
		}
		eachMap(iphone, "SELECT m.*, i.ZMETADATA AS meta_blob, mi.ZRECEIPTINFO AS receipt_blob, "+
			"i.ZLATITUDE, i.ZLONGITUDE, i.ZTITLE, i.ZVCARDNAME, i.ZVCARDSTRING FROM ZWAMESSAGE m "+
			"LEFT JOIN ZWAMEDIAITEM i ON i.Z_PK = m.ZMEDIAITEM "+
			"LEFT JOIN ZWAMESSAGEINFO mi ON mi.Z_PK = m.ZMESSAGEINFO ORDER BY m.Z_PK", nil, func(r row) {
			s := sessions[toInt(r["ZCHATSESSION"])]
			outgoing := truthy(r["ZISFROMME"])
			var sender *archive.Handle
			if !outgoing {
				jid := memberJID[toInt(r["ZGROUPMEMBER"])]
				if jid == "" {
					jid = str(r["ZFROMJID"])
				}
				if p, ok := person.of(jid); ok {
					sender = &p
				}
			}
			mt, _ := r["ZMESSAGETYPE"].(int64)
			kind, ok := iphoneKinds[mt]
			if _, isInt := r["ZMESSAGETYPE"].(int64); !ok || !isInt {
				kind = "system"
			}
			text := str(r["ZTEXT"])
			if text == "" {
				text = metadataTextOf(metadata[toInt(r["Z_PK"])])
			}
			add("iphone", pyStr(r["Z_PK"]), whatsappExtras(r, asBytes(r["meta_blob"]), asBytes(r["receipt_blob"]), r),
				s.conv, appleMS(toFloat(r["ZMESSAGEDATE"])), outgoing, sender, kind, text, r["ZSTANZAID"])
		})
	}

	// Bridge
	names := map[string]any{}
	reactions := map[reactionKey][]bridgeReaction{}
	var reactionOrder []reactionKey
	if bridge != nil {
		for _, r := range maps(bridge, "SELECT jid, name FROM chats") {
			names[str(r["jid"])] = r["name"]
		}
		if hasTable(bridge, "reactions") {
			for _, r := range maps(bridge, "SELECT chat_jid, message_id, sender, is_from_me, emoji FROM reactions") {
				k := reactionKey{str(r["chat_jid"]), str(r["message_id"])}
				if _, ok := reactions[k]; !ok {
					reactionOrder = append(reactionOrder, k)
				}
				reactions[k] = append(reactions[k], bridgeReaction{r["sender"], truthy(r["is_from_me"]), str(r["emoji"])})
			}
		}
		convs := map[string]int64{}
		cols := columns(bridge, "messages")
		eachMap(bridge, "SELECT "+selectAll(bridge, "messages", "")+" FROM messages ORDER BY timestamp", nil, func(r row) {
			jid := str(r["chat_jid"])
			if _, ok := convs[jid]; !ok {
				convs[jid] = waConversation(a, person, jid, names[jid], nil)
			}
			outgoing := truthy(r["is_from_me"])
			var kind string
			if cols["kind"] && truthy(r["kind"]) {
				kind = bridgeKinds[str(r["kind"])]
				if kind == "" {
					kind = "file"
				}
			} else {
				var ok bool
				if kind, ok = bridgeMediaKinds[str(r["media_type"])]; !ok {
					kind = "file"
				}
			}
			var sender *archive.Handle
			if !outgoing {
				s := str(r["sender"])
				if s == "" {
					s = jid
				}
				if p, ok := person.of(s); ok {
					sender = &p
				}
			}
			t, ok := fromISO(str(r["timestamp"]))
			if !ok {
				panic(&db.Error{Query: "fromisoformat", Err: errBadTime(str(r["timestamp"]))})
			}
			add("bridge", jid+"/"+pyStr(r["id"]), whatsappBridgeExtras(r, reactions[reactionKey{jid, str(r["id"])}]), convs[jid],
				tsMS(t), outgoing, sender, kind, str(r["content"]), r["id"])
		})
		updated = bridgeChanges(a, bridge, person, own, reactionOrder, reactions)
		bridgeMembers(a, bridge, person, own)
		bridgeMentions(a, bridge, person)
		bridgeReceipts(a, bridge, person, own)
		bridgeBlocked(a, bridge, person, own)
	} else {
		updated = map[string]int{}
	}

	// The names WhatsApp shows, for the handles in the archive (the core orders them against an
	// address book and other services).
	seen := map[string]int64{}
	if iphone != nil {
		seen["iphone"] = int64(mtime(iphoneDB))
	}
	if bridge != nil {
		p := bridgeDB
		if storeDB != "" && exists(storeDB) {
			p = storeDB
		}
		seen["bridge"] = int64(mtime(p))
	}
	var newest int64
	for _, v := range seen {
		newest = max(newest, v)
	}
	for _, k := range person.order {
		if !own[k.h] {
			a.HandleName(k.h, "whatsapp", person.names[k], k.kind, newest)
		}
	}

	// The state of chats: archived (only the start of ours: see Archive.InitArchived), muted,
	// pinned, as each source last saw it.
	report := func(where, jid, field string, value, at int64) {
		conv, _ := a.FindConversation("whatsapp", person.conversationKey(jid))
		a.ReportState(src[where], conv, field, value, at*1000, 0)
	}
	if iphone != nil {
		for _, r := range maps(iphone, "SELECT ZCONTACTJID, ZARCHIVED FROM ZWACHATSESSION WHERE ZCONTACTJID IS NOT NULL") {
			report("iphone", str(r["ZCONTACTJID"]), "archived", int64(archive.B2I(truthy(r["ZARCHIVED"]))), seen["iphone"])
		}
		muted := map[string]any{}
		for _, r := range maps(iphone, "SELECT ZJID, ZMUTEDUNTIL FROM ZWACHATPUSHCONFIG WHERE ZJID IS NOT NULL") {
			muted[str(r["ZJID"])] = r["ZMUTEDUNTIL"]
		}
		for _, jid := range db.Strs(iphone, "SELECT ZCONTACTJID FROM ZWACHATSESSION WHERE ZCONTACTJID IS NOT NULL") {
			until := toFloat(muted[jid])
			var v int64
			switch {
			case until <= 0:
				v = 0
			case until > 32503680000: // past 3000: for ever
				v = -1
			default:
				v = int64((until + archive.AppleEpoch) * 1000)
			}
			report("iphone", jid, "muted", v, seen["iphone"])
		}
	}
	if bridge != nil {
		if store != nil {
			for _, r := range maps(store, "SELECT chat_jid, muted_until, pinned, archived FROM whatsmeow_chat_settings") {
				jid := str(r["chat_jid"])
				report("bridge", jid, "archived", int64(archive.B2I(truthy(r["archived"]))), seen["bridge"])
				report("bridge", jid, "pinned", int64(archive.B2I(truthy(r["pinned"]))), seen["bridge"])
				until := toInt(r["muted_until"])
				if until != -1 {
					until *= 1000
				}
				report("bridge", jid, "muted", until, seen["bridge"])
			}
		}
		// read up to: what the owner read on any device, as the bridge saw
		order, reads := bridgeRead(bridge)
		for _, jid := range order {
			conv, _ := a.FindConversation("whatsapp", person.conversationKey(jid))
			a.ReportState(src["bridge"], conv, "read_until", reads[jid].newest, reads[jid].when, reads[jid].when)
		}
	}

	a.Resolve()
	for _, s := range srcOrder {
		a.Imported(src[s])
	}
	a.Commit()
	for _, s := range srcOrder {
		p := map[string]any{"n": fmt.Sprintf("%7d", added[s]), "source": s, "skipped": skipped[s]}
		if skipped[s] > 0 {
			say(out, "new:     {n} {source} ({skipped} were already there from another source)", p)
		} else {
			say(out, "new:     {n} {source}", p)
		}
	}
	if len(updated) > 0 {
		var ks []string
		for k := range updated {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		var parts []string
		for _, k := range ks {
			parts = append(parts, fmt.Sprintf("%s %d", k, updated[k]))
		}
		say(out, "changes to what was there: {list}", map[string]any{"list": strings.Join(parts, ", ")})
	}
	return updated, nil
}

func containsHandle(hs []archive.Handle, h archive.Handle) bool {
	for _, x := range hs {
		if x == h {
			return true
		}
	}
	return false
}
