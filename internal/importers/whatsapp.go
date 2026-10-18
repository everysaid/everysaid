// Ports everysaid/whatsapp.py: WhatsApp from the iPhone's ChatStorage.sqlite; the live connection
// writes its own straight into the archive (whatsapp_live.go).
//
// Messages are matched by stanza id (the message id WhatsApp sends, the same on every device): the
// iPhone's and the live connection's copy of a message are one. People are stored by phone number:
// WhatsApp's LIDs (`...@lid`) are mapped to numbers through the iPhone's WhatsApp contacts, the live
// connection's whatsmeow_lid_map and the groups' members it read; LIDs with no known number stay as
// they are. Groups are keyed by their jid. Channels (`@newsletter`) and status (`status@broadcast`)
// are not wanted and not imported.
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
	secrets "everysaid/internal/keys"
)

// WhatsAppIphoneDB and the others are where the iPhone's WhatsApp databases are.
func WhatsAppIphoneDB() string { return filepath.Join(archive.IphoneData(), "whatsapp.sqlite") }
func WhatsAppContactsDB() string {
	return filepath.Join(archive.IphoneData(), "whatsapp-contacts.sqlite")
}

// Devices is whatsmeow's device store (in keys.db), where config names the live connection's store
// folder; nil otherwise, or where keys.db holds no WhatsApp device. It is shared: never closed here.
func Devices() *sql.DB {
	if config.WhatsappBridge == "" {
		return nil
	}
	d, err := secrets.Open()
	if err != nil || !hasTable(d, "whatsmeow_device") {
		return nil
	}
	return d
}

// ZWAMESSAGE.ZMESSAGETYPE, from the files each type carries (`file`, `ffprobe`) and its metadata:
// 11 are GIFs (silent mp4), 54 round video notes (square, with sound), 14 deleted messages; 19, 20,
// 25, 30, 31 and 41 are business messages (templates, buttons) whose text is only in the media
// item's protobuf metadata. Anything else (10, 28, 59, 66, ...) carries nothing and is 'system'.
var iphoneKinds = map[int64]string{0: "text", 7: "text", 1: "image", 11: "image", 38: "image", 2: "video", 39: "video",
	54: "video", 3: "voice", 4: "contact", 5: "location", 8: "file", 15: "sticker",
	19: "text", 20: "text", 25: "text", 30: "text", 31: "text", 41: "text"}

const metadataText = "(19, 20, 25, 30, 31, 41)"

// The live connection's kinds of messages (WAMessage.Kind); a message with none says only its file's.
var bridgeKinds = map[string]string{"text": "text", "image": "image", "video": "video", "audio": "voice", "voice": "voice",
	"document": "file", "sticker": "sticker", "location": "location", "contact": "contact", "poll": "text"}
var bridgeMediaKinds = map[string]string{"": "text", "image": "image", "video": "video", "audio": "voice",
	"document": "file", "sticker": "sticker"}

// roBridge is a database that may not be there (the iPhone's), or nil.
func roBridge(path string) *sql.DB {
	if path == "" || !exists(path) {
		return nil
	}
	return ro(path)
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

// waPeople maps a jid (or a bare user part) to a handle.
type waPeople struct {
	lidPhone map[string]string
	lids     map[string]bool
	names    map[waName]string
	order    []waName
	keys     *sql.DB // the live connection's: a LID's number asked of it as the LID comes
}

// newWAPeople: each may be nil: the iPhone's contacts and messages, keys.db (whatsmeow's store and
// the live connection's).
func newWAPeople(contacts, store, iphone *sql.DB) *waPeople {
	return newWAPeopleOf(contacts, store, iphone, "", "")
}

// ownFilter and ownArgs limit a query of whatsmeow's per-device tables to one device ("": all).
func ownFilter(own string) string {
	if own == "" {
		return ""
	}
	return " WHERE our_jid = ?"
}

func ownArgs(own string) []any {
	if own == "" {
		return nil
	}
	return []any{own}
}

// newWAPeopleOf is newWAPeople with the device store's contacts limited to own's ("": all), and the
// names of the live connection's chats of the store folder live ("": none).
func newWAPeopleOf(contacts, store, iphone *sql.DB, live, own string) *waPeople {
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
	if store != nil && hasTable(store, "whatsapp_group_member") { // the groups' members, named both ways
		for _, r := range maps(store, "SELECT lid, phone FROM whatsapp_group_member WHERE lid != '' AND phone != ''") {
			if _, ok := p.lidPhone[user(str(r["lid"]))]; !ok {
				p.lidPhone[user(str(r["lid"]))] = user(str(r["phone"]))
			}
		}
	}
	// Every LID seen anywhere, to tell them from phone numbers in bare senders.
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
	// The names WhatsApp shows for people, by kind (book: its copy of the phone's address book;
	// chat: a chat's name; profile: the name they chose, a push name): one per handle and kind,
	// the first found.
	type named struct{ jid, name any }
	var rows []struct {
		named
		kind string
	}
	add := func(q db.Querier, query, kind string, args ...any) {
		for _, r := range maps(q, query, args...) {
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
		add(store, "SELECT their_jid AS j, full_name AS n FROM whatsmeow_contacts"+ownFilter(own), "book", ownArgs(own)...)
	}
	if iphone != nil {
		add(iphone, "SELECT ZCONTACTJID AS j, ZPARTNERNAME AS n FROM ZWACHATSESSION "+
			"WHERE ZCONTACTJID LIKE '%@s.whatsapp.net' OR ZCONTACTJID LIKE '%@lid'", "chat")
		add(iphone, "SELECT ZJID AS j, ZPUSHNAME AS n FROM ZWAPROFILEPUSHNAME", "profile")
	}
	if store != nil && live != "" && hasTable(store, "whatsapp_chat") {
		add(store, "SELECT jid AS j, name AS n FROM whatsapp_chat WHERE store = ? AND (jid LIKE '%@s.whatsapp.net' OR jid LIKE '%@lid')",
			"chat", live)
	}
	if store != nil {
		add(store, "SELECT their_jid AS j, push_name AS n FROM whatsmeow_contacts"+ownFilter(own), "profile", ownArgs(own)...)
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
		if pn, ok := p.phone(u); ok {
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

// phone is a LID's number, where it is known.
func (p *waPeople) phone(lid string) (string, bool) {
	if pn, ok := p.lidPhone[lid]; ok {
		return pn, true
	}
	if p.keys == nil {
		return "", false
	}
	pn := ""
	if hasTable(p.keys, "whatsmeow_lid_map") {
		pn = db.Str(p.keys, "SELECT pn FROM whatsmeow_lid_map WHERE lid = ?", lid)
	}
	if pn == "" && hasTable(p.keys, "whatsapp_group_member") {
		pn = user(db.Str(p.keys, "SELECT phone FROM whatsapp_group_member WHERE lid = ? AND coalesce(phone, '') != '' LIMIT 1",
			lid+"@lid"))
	}
	if pn == "" {
		return "", false
	}
	p.lidPhone[lid] = pn
	return pn, true
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
// Devices: whatsmeow's device store (keys.db; nil: Devices(), unless NoStore), for the LIDs' numbers
// and the names in the address book it keeps.
type WhatsAppOptions struct {
	IphoneDB, ContactsDB string
	Devices              *sql.DB
	NoContacts, NoStore  bool
}

// devicesOf is the device store an import reads.
func devicesOf(given *sql.DB, none bool) *sql.DB {
	switch {
	case none:
		return nil
	case given != nil:
		return given
	}
	return Devices()
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

// WhatsApp imports the iPhone's WhatsApp; it gives what changed on messages already there (their
// previews, notices: counts).
func WhatsApp(a *archive.Archive, out func(string), opt WhatsAppOptions) (updated map[string]int, err error) {
	defer archive.Recover(&err)
	iphoneDB := WhatsAppIphoneDB()
	if opt.IphoneDB != "" {
		iphoneDB = opt.IphoneDB
	}
	contactsDB := pick(opt.ContactsDB, opt.NoContacts, WhatsAppContactsDB)
	iphone := roBridge(iphoneDB)
	if iphone == nil {
		say(out, "no source: {a}", map[string]any{"a": iphoneDB})
		return map[string]int{}, nil
	}
	defer iphone.Close()
	contacts, store := roBridge(contactsDB), devicesOf(opt.Devices, opt.NoStore)
	if contacts != nil {
		defer contacts.Close()
	}
	person := newWAPeople(contacts, store, iphone)
	own := a.Own()
	src := map[string]int64{"iphone": a.Source(archive.Iphone()+"/whatsapp", iphoneDB, archive.Iphone(), "")}
	srcOrder := []string{"iphone"}
	added, skipped := map[string]int{}, map[string]int{}
	previews := 0 // of messages the archive had

	// add adds a message; false: it was there (or is a channel's)
	add := func(source, rowKey string, extra *archive.Extras, conv int64, ts int64, outgoing bool, sender *archive.Handle,
		kind, text string, key any) bool {
		if conv == 0 { // a channel
			return false
		}
		// a message is known by its stanza id: the row key alone is not enough, as the iPhone gives the
		// number of a deleted row (Core Data's Z_PK) to a new one, which was then taken as had
		k, isStr := key.(string)
		if isStr {
			if mid, ok := a.MessageByKey("whatsapp", k, 0); ok && mid != 0 {
				if !a.HasOrigin(src[source], rowKey, "") {
					// the archive has it from another source: this one's row is recorded as had too
					a.Exec("INSERT OR IGNORE INTO message_origin (source_id, row_key, message_id) VALUES (?, ?, ?)",
						src[source], rowKey, mid)
					skipped[source]++
				}
				return false
			}
			// the row taken before it had its stanza id (a backup made as it was being sent): the same
			// message, which has its key now
			if pending := a.Int("SELECT m.id FROM message_origin o JOIN message m ON m.id = o.message_id "+
				"WHERE o.source_id = ? AND o.row_key = ? AND m.key IS NULL AND m.conversation_id = ?",
				src[source], rowKey, conv); pending != 0 {
				a.Exec("UPDATE message SET key = ? WHERE id = ?", k, pending)
				return false
			}
			// the row key was another message's, deleted at the source since: now it is this one's
			a.Exec("DELETE FROM message_origin WHERE source_id = ? AND row_key = ?", src[source], rowKey)
		} else if a.HasOrigin(src[source], rowKey, "") {
			return false
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
		return true
	}

	// iPhone
	{
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
		linkPreviews := iphonePreviews(iphone, filepath.Join(filepath.Dir(iphoneDB), WhatsAppPreviewFolder))
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
			x := whatsappExtras(r, asBytes(r["meta_blob"]), asBytes(r["receipt_blob"]), r)
			// the archive's copy of this row: by its stanza id where it has one (a row key may be a
			// deleted row's, given again to this one)
			had := func() (int64, bool) {
				if k, isStr := r["ZSTANZAID"].(string); isStr && k != "" {
					mid, ok := a.MessageByKey("whatsapp", k, 0)
					return mid, ok && mid != 0
				}
				return a.IntOK("SELECT message_id FROM message_origin WHERE source_id = ? AND row_key = ?",
					src["iphone"], pyStr(r["Z_PK"]))
			}
			if mt == 6 {
				if _, known := had(); !known && a.Exists("SELECT 1 FROM message "+
					"WHERE conversation_id = ? AND subtype_code = 'whatsmeow:group_event' AND abs(ts - ?) <= 5000",
					s.conv, appleMS(toFloat(r["ZMESSAGEDATE"]))) {
					return // the bridge's copy of the same change is there already
				}
			}
			if mt == 6 { // a group's change: those whose meaning is known, as notices
				var by map[string]any
				if outgoing {
					by = map[string]any{"self": true}
				} else if sender != nil {
					by = NoticePerson(a, *sender, own)
				}
				x.Notice = iphoneGroupNotice(a, person, own, toInt(r["ZGROUPEVENTTYPE"]), str(r["ZTEXT"]), by)
				if x.Notice != nil {
					if mid, ok := had(); ok {
						setNoticeIfChanged(a, mid, x.Notice) // imported before there were notices
					}
				}
			}
			x.Preview = linkPreviews[toInt(r["Z_PK"])]
			if !add("iphone", pyStr(r["Z_PK"]), x, s.conv, appleMS(toFloat(r["ZMESSAGEDATE"])), outgoing, sender, kind, text,
				r["ZSTANZAID"]) && x.Preview != nil { // imported before there were previews
				if mid, ok := had(); ok && setPreview(a, mid, x.Preview, true) {
					previews++
				}
			}
		})
	}

	updated = map[string]int{}
	if previews > 0 {
		updated["preview"] += previews
	}

	// The names WhatsApp shows, for the handles in the archive (the core orders them against an
	// address book and other services).
	seen := int64(mtime(iphoneDB))
	for _, k := range person.order {
		if !own[k.h] {
			a.HandleName(k.h, "whatsapp", person.names[k], k.kind, seen)
		}
	}

	// The state of chats: archived (only the start of ours: see Archive.InitArchived), muted, as the
	// iPhone last saw it.
	report := func(jid, field string, value int64) {
		conv, _ := a.FindConversation("whatsapp", person.conversationKey(jid))
		a.ReportState(src["iphone"], conv, field, value, seen*1000, 0)
	}
	for _, r := range maps(iphone, "SELECT ZCONTACTJID, ZARCHIVED FROM ZWACHATSESSION WHERE ZCONTACTJID IS NOT NULL") {
		report(str(r["ZCONTACTJID"]), "archived", int64(archive.B2I(truthy(r["ZARCHIVED"]))))
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
		report(jid, "muted", v)
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
		a.Changed = true
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

// iphoneGroupNotice is a group's change on the iPhone (ZWAMESSAGE type 6) as a notice, for the kinds
// of change (ZGROUPEVENTTYPE) whose meaning the data shows: 12, the group made (ZTEXT its name; a
// list of jids is no name); 50, people added (ZTEXT "adder;added,added,..."). nil for the rest,
// which stay as they are.
func iphoneGroupNotice(a *archive.Archive, person *waPeople, own map[archive.Handle]bool, event int64, text string,
	by map[string]any) *archive.Notice {
	who := func(jid string) map[string]any {
		if h, ok := person.of(strings.TrimSpace(jid)); ok {
			return NoticePerson(a, h, own)
		}
		return nil
	}
	switch event {
	case 12:
		if text == "" || strings.Contains(text, "@") {
			return nil
		}
		return groupNotice(by, map[string]any{"type": "created", "title": text})
	case 50:
		adder, added, ok := strings.Cut(text, ";")
		if !ok || added == "" {
			return nil
		}
		if p := who(adder); p != nil {
			by = p
		}
		var actions []map[string]any
		for _, j := range strings.Split(added, ",") {
			if p := who(j); p != nil {
				actions = append(actions, map[string]any{"type": "added", "who": p})
			}
		}
		if len(actions) == 0 {
			return nil
		}
		return groupNotice(by, actions...)
	}
	return nil
}
