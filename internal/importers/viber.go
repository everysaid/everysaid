// Ports everysaid/viber.py: Viber from the iPhone's viber.sqlite and a Viber Desktop export (an
// Android phone's history, config [viber] desktop_export). Either may be missing.
//
// Messages are matched by token, the same id on every device. A message in both sources is kept
// once, from the desktop export where its device (the Android phone it was synced with) was the one
// in use at the time, from the iPhone otherwise; a message whose token is already in the archive is
// skipped. Rows without a token (system events) are skipped when the same conversation already has
// one at the same moment with the same text. People are matched by Viber member id and stored by
// phone number when either source knows it, so they meet their SMS and calls; groups by group
// token. Channels (news, government broadcasts) are not wanted and not imported.
package importers

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"unicode/utf16"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
)

// ViberIphoneDB is where the iPhone's viber.sqlite is.
func ViberIphoneDB() string { return filepath.Join(archive.IphoneData(), "viber.sqlite") }

var desktopKinds = map[int64]string{1: "text", 2: "image", 3: "video", 4: "sticker", 5: "location", 6: "voice",
	9: "text", 10: "contact", 11: "file", 15: "system"}

const desktopSystemEvent = 3

var iphoneAttachmentKinds = map[string]string{"picture": "image", "gif": "image", "video": "video", "audio": "voice",
	"file": "file", "sticker": "sticker", "customLocation": "location"}

var iphoneTextTypes = map[string]bool{"": true, "url": true, "formatted": true}

const (
	viberChannel = 3 // ZCONVERSATION.ZSUBTYPE on the iPhone, ChatInfo.PGType in the desktop export: not wanted
	viberNotes   = 5 // ZCONVERSATION.ZSUBTYPE on the iPhone: "My Notes", the chat with oneself (a group of one)
	// A token carries its send time: (token >> 22) + this offset is Unix ms (measured on the iPhone's
	// messages: nearly all within 2 s). Used only where the date itself is missing.
	tokenEpochMS = 292057776050
)

// tokenTime is a message's time: its date, else (none, or 0: no Viber message is from 2001) the
// time its token carries.
func tokenTime(date any, token int64) int64 {
	var ts int64
	if date != nil && toFloat(date) != 0 {
		ts = appleMS(toFloat(date))
	}
	if ts > 0 || token == 0 {
		return ts
	}
	return (token >> 22) + tokenEpochMS
}

// viberPhones is Viber member id -> phone number, from whichever source knows it. What is learnt
// is kept in the archive (viber_member), so it is not lost when a source is gone.
func viberPhones(iphone, desktop *sql.DB, a *archive.Archive) map[string]string {
	known := map[string]string{}
	var order []string
	set := func(k, v string) {
		if _, ok := known[k]; !ok {
			order = append(order, k)
		}
		known[k] = v
	}
	a.Each("SELECT mid, number FROM viber_member", nil, func(scan func(...any)) {
		var m, n sql.NullString
		scan(&m, &n)
		set(m.String, n.String)
	})
	if desktop != nil {
		for _, r := range maps(desktop, "SELECT MID, Number FROM Contact WHERE MID != '' AND Number != ''") {
			set(pyStr(r["MID"]), pyStr(r["Number"]))
		}
	}
	if iphone != nil {
		for _, r := range maps(iphone, "SELECT m.ZMEMBERID AS mid, coalesce(p.ZCANONIZEDPHONENUM, p.ZPHONE) AS number FROM ZMEMBER m "+
			"JOIN ZPHONENUMBER p ON p.ZMEMBER = m.Z_PK WHERE m.ZMEMBERID IS NOT NULL") {
			if truthy(r["number"]) {
				if _, ok := known[pyStr(r["mid"])]; !ok {
					set(pyStr(r["mid"]), pyStr(r["number"]))
				}
			}
		}
	}
	for _, k := range order {
		a.Exec("INSERT OR REPLACE INTO viber_member VALUES (?, ?)", k, known[k])
	}
	return known
}

type viberPeople struct{ known map[string]string }

// of is the handle of a member (by number where known, else by member id); false for none.
func (p viberPeople) of(mid any, number any) (archive.Handle, bool) {
	n := ""
	if truthy(number) {
		n = pyStr(number)
	} else if truthy(mid) {
		n = p.known[pyStr(mid)]
	}
	if n != "" {
		return address(n), true
	}
	if truthy(mid) {
		return archive.H("id", pyStr(mid), "viber"), true
	}
	return archive.Handle{}, false
}

// utf16Slice is the text between two UTF-16 positions, as Python's encode/decode does it.
func utf16Slice(s string, start, end int) string {
	units := utf16.Encode([]rune(s))
	start, end = max(0, min(start, len(units))), max(0, min(end, len(units)))
	if start >= end {
		return ""
	}
	return utf16Decode(units[start:end])
}

// utf16Decode decodes UTF-16 units, a lone surrogate as U+FFFD (Python's "replace").
func utf16Decode(units []uint16) string {
	return string(utf16.Decode(units))
}

// iphoneMarks reads, from the iPhone: whom texts name (textMetaInfo type 0: the member and where
// the text names them, "@Name", in UTF-16 units); how far the owner read each chat
// (ZLASTREADTOKEN, its read_until); and, in a chat with one person, how far they saw the owner's
// messages (ZSEENSTATUSLASTTOKEN: receipts, known but not when). Its ZSTATE "delivered" is on every
// message sent, at the time it was sent: it says nothing of delivery.
func iphoneMarks(a *archive.Archive, iphone *sql.DB, src int64, convs map[int64]int64,
	convMembers map[int64][]archive.Handle, person viberPeople, observed int64) {
	for _, r := range maps(iphone, "SELECT ZTOKEN, ZTEXT, ZMETADATA FROM ZVIBERMESSAGE "+
		"WHERE ZTOKEN AND ZMETADATA LIKE '%textMetaInfo%'") {
		info := jsonObj(r["ZMETADATA"])["textMetaInfo"]
		if s, ok := info.(string); ok { // a JSON text of its own: its list
			info = map[string]any{}
			if v, err := decodeJSON([]byte(s)); err == nil && s != "" {
				info = v
			}
		}
		var mid int64
		var ok bool
		if truthy(info) {
			mid, ok = a.MessageByKey("viber", pyStr(r["ZTOKEN"]), 0)
		}
		l, isList := info.([]any)
		if !ok || mid == 0 || !isList {
			continue
		}
		viberMentions(a, mid, str(r["ZTEXT"]), l, person)
	}
	for _, r := range maps(iphone, "SELECT Z_PK, ZGROUPID, ZLASTREADTOKEN, ZSEENSTATUSLASTTOKEN FROM ZCONVERSATION") {
		pk := toInt(r["Z_PK"])
		conv, ok := convs[pk]
		if !ok || conv == 0 {
			continue
		}
		if read := r["ZLASTREADTOKEN"]; truthy(read) {
			var ts int64
			if mid, ok := a.MessageByKey("viber", pyStr(read), 0); ok && mid != 0 {
				ts = a.Int("SELECT ts FROM message WHERE id = ?", mid)
			} else {
				ts = tokenTime(nil, toInt(read))
			}
			a.ReportState(src, conv, "read_until", ts, observed, 0)
		}
		if seen := r["ZSEENSTATUSLASTTOKEN"]; truthy(seen) && !truthy(r["ZGROUPID"]) && len(convMembers[pk]) == 1 {
			peer := a.Address(convMembers[pk][0])
			mine := "SELECT id FROM message WHERE conversation_id = ? AND outgoing AND key IS NOT NULL " +
				"AND CAST(key AS INTEGER) <= ?"
			a.Exec("INSERT OR IGNORE INTO receipt (message_id, address_id, read_at) SELECT id, ?, 0 FROM ("+mine+")",
				peer, conv, seen)
			a.Exec("UPDATE receipt SET read_at = 0 WHERE address_id = ? AND read_at IS NULL "+
				"AND message_id IN ("+mine+")", peer, conv, seen)
		}
	}
}

// isNumber says whether a JSON value is a number (not a bool).
func isNumber(v any) bool {
	switch v.(type) {
	case int64, float64, json.Number:
		return true
	}
	return false
}

// jsonInt is a JSON value that is an int (Python's isinstance(x, int): a bool too).
func jsonInt(v any) (int64, bool) {
	switch x := v.(type) {
	case bool:
		return int64(archive.B2I(x)), true
	case json.Number:
		i, err := x.Int64()
		return i, err == nil
	case int64:
		return x, true
	}
	return 0, false
}

// ViberOptions: IphoneDB "" is the default; DesktopDB "" is config [viber] desktop_export, and
// NoDesktop leaves the desktop export out (Python's desktop_db=None); NoIphone leaves the iPhone out.
// DesktopSource is the source the desktop's rows are of ("" the Android phone's export,
// "<android device>/viber"), DesktopDevice the device whose time of use decides between its copy of a
// message and the iPhone's ("" with a DesktopSource: none, the iPhone's is kept).
type ViberOptions struct {
	IphoneDB, DesktopDB          string
	NoDesktop, NoIphone          bool
	DesktopSource, DesktopDevice string
}

const (
	desktopDeleted = 72      // Messages.Type of a message deleted by its sender (Body and Info emptied)
	desktopNotes   = 1 << 19 // ChatInfo.Flags of "My Notes"
)

// viberLike is one reaction event of the desktop: who (the user's own: Direction 1), and the
// reaction, a quick one's number (PGIsLiked; 0 takes it back) or any emoji (SelfReaction).
type viberLike struct {
	mine  bool
	who   any
	quick int64
	emoji string
}

// desktopFollow is what the desktop says now of a message: brought to it where the archive has it.
type desktopFollow struct {
	key, text       string
	edited, deleted bool
	reactions       []archive.Reaction
	reactionsKnown  bool // the desktop says what they are (else they are left as they are)
	mentions        any  // Info.textMetaInfo
}

// viberEdit is an edit event of the iPhone: the token of the message edited, and its new text.
type viberEdit struct{ key, text string }

type viberMsg struct {
	extra    *archive.Extras
	conv     int64
	ts       int64
	outgoing bool
	sender   *archive.Handle
	kind     string
	text     string
	key      string // "" for none
}

func Viber(a *archive.Archive, out func(string), opt ViberOptions) (err error) {
	defer archive.Recover(&err)
	iphoneDB := opt.IphoneDB
	if iphoneDB == "" {
		iphoneDB = ViberIphoneDB()
	}
	if opt.NoIphone {
		iphoneDB = ""
	}
	desktopDB := opt.DesktopDB
	if opt.NoDesktop {
		desktopDB = ""
	} else if desktopDB == "" {
		desktopDB = config.ViberDesktop
	}
	var iphone, desktop *sql.DB
	if iphoneDB != "" && exists(iphoneDB) {
		iphone = ro(iphoneDB)
		defer iphone.Close()
	}
	if desktopDB != "" && exists(desktopDB) { // an Android phone's: may be gone
		desktop = ro(desktopDB)
		defer desktop.Close()
	}
	if iphone == nil && desktop == nil {
		d := desktopDB
		if d == "" {
			d = "[viber] desktop_export"
		}
		say(out, "no source: neither {a} nor {b}", map[string]any{"a": iphoneDB, "b": d})
		return nil
	}
	person := viberPeople{viberPhones(iphone, desktop, a)}
	own := a.Own()
	src := map[string]int64{}
	var srcOrder []string
	if iphoneDB != "" {
		src["iphone"] = a.Source(archive.Iphone()+"/viber", iphoneDB, archive.Iphone(), "")
		srcOrder = append(srcOrder, "iphone")
	}
	desktopDevice := opt.DesktopDevice
	if desktopDB != "" {
		name := opt.DesktopSource
		if name == "" {
			name, desktopDevice = archive.Android()+"/viber", archive.Android()
		}
		src["desktop"] = a.Source(name, desktopDB, desktopDevice, "")
		srcOrder = append(srcOrder, "desktop")
	}
	added, skipped := map[string]int{}, map[string]int{}
	viberID := a.Service.ID("viber")

	add := func(source, rowKey string, m viberMsg) {
		if m.conv == 0 { // a channel
			return
		}
		if a.HasOrigin(src[source], rowKey, "") {
			return
		}
		if m.key != "" {
			if mid, ok := a.MessageByKey("viber", m.key, 0); ok && mid != 0 {
				skipped[source]++
				return
			}
		}
		// Without a token (system events) the row key is all there is, and the desktop's EventID is
		// local to one profile: a later export from another profile would bring them again.
		if m.key == "" && a.Exists("SELECT 1 FROM message WHERE conversation_id = ? AND ts = ? AND key IS NULL AND text IS ? "+
			"AND service_id = ?", m.conv, m.ts, archive.NullStr(m.text), viberID) {
			skipped[source]++
			return
		}
		var senderID int64
		if m.sender != nil {
			senderID = a.Address(*m.sender)
		}
		a.AddMessage(src[source], rowKey, archive.Message{Service: "viber", ConversationID: m.conv, TS: m.ts,
			Outgoing: m.outgoing, SenderID: senderID, Kind: m.kind, Text: m.text, Key: m.key, Extras: m.extra})
		added[source]++
	}

	// iPhone: read first (small), so that its tokens are known while the desktop rows stream by.
	members := map[int64]*archive.Handle{}
	convs := map[int64]int64{} // 0: a channel
	var convOrder []int64
	convMembers := map[int64][]archive.Handle{}
	var iphoneRecs map[int64]viberMsg
	var edits []viberEdit // in the order they were made
	var iphoneOrder []int64
	iphoneKeys := map[string]int64{}
	if iphone != nil {
		for _, r := range maps(iphone, "SELECT Z_PK, ZMEMBERID FROM ZMEMBER") {
			if h, ok := person.of(r["ZMEMBERID"], nil); ok {
				members[toInt(r["Z_PK"])] = &h
			} else {
				members[toInt(r["Z_PK"])] = nil
			}
		}
		for _, r := range maps(iphone, "SELECT Z_5CONVERSATIONS AS c, Z_10PHONENUMINDEXES AS m FROM Z_5PHONENUMINDEXES") {
			if m := members[toInt(r["m"])]; m != nil && !own[*m] {
				convMembers[toInt(r["c"])] = append(convMembers[toInt(r["c"])], *m)
			}
		}
		for _, c := range maps(iphone, "SELECT Z_PK, ZGROUPID, ZNAME, ZSUBTYPE FROM ZCONVERSATION") {
			pk := toInt(c["Z_PK"])
			mem := convMembers[pk]
			sub, subInt := c["ZSUBTYPE"].(int64)
			convOrder = append(convOrder, pk)
			switch {
			case subInt && sub == viberChannel:
				convs[pk] = 0
			case truthy(c["ZGROUPID"]):
				cid := titled(a, "viber", mem, "group:"+pyStr(c["ZGROUPID"]), c["ZNAME"])
				convs[pk] = cid
				// the notes are not a group: the app shows them as the user's notes, with the other services'
				notes := subInt && sub == viberNotes
				a.Exec("UPDATE conversation SET is_group = ? WHERE id = ?", archive.B2I(!notes), cid)
				if notes { // the owner is its member: theirs, said by the source
					for h := range own {
						if h.Kind == "phone" {
							a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", cid, a.Address(h))
						}
					}
				}
			default:
				if len(mem) == 0 {
					mem = []archive.Handle{archive.H("id", fmt.Sprintf("conversation:%d", pk), "viber")}
				}
				convs[pk] = a.Conversation("viber", mem, "", "")
			}
		}
		attachment := map[int64]string{}
		for _, r := range maps(iphone, "SELECT Z_PK, ZTYPE FROM ZATTACHMENT") {
			attachment[toInt(r["Z_PK"])] = str(r["ZTYPE"])
		}
		locations := map[int64]location{}
		for _, r := range maps(iphone, "SELECT Z_PK, ZLATITUDE, ZLONGITUDE, ZADDRESS FROM ZVIBERLOCATION") {
			locations[toInt(r["Z_PK"])] = location{r["ZLATITUDE"], r["ZLONGITUDE"], r["ZADDRESS"]}
		}
		iphoneRecs = map[int64]viberMsg{}
		// each one's reaction, as the iPhone keeps them (ZLIKE: one row for each person and message, its
		// sender none for the owner's; 0: taken back)
		likes := map[string][]archive.Reaction{}
		if hasTable(iphone, "ZLIKE") {
			for _, l := range maps(iphone, "SELECT ZMESSAGETOKEN, ZSENDER, ZLIKEVALUE, ZUNICODEREACTION FROM ZLIKE "+
				"WHERE ZLIKEVALUE != 0 OR coalesce(ZUNICODEREACTION, '') != '' ORDER BY ZMESSAGETOKEN, ZSENDER") {
				k := l["ZLIKEVALUE"]
				if truthy(l["ZUNICODEREACTION"]) {
					k = l["ZUNICODEREACTION"]
				}
				e, c := reaction(k)
				x := archive.Reaction{Emoji: e, Code: c, Count: 1, Outgoing: l["ZSENDER"] == nil}
				if !x.Outgoing {
					h := members[toInt(l["ZSENDER"])]
					if h == nil || own[*h] {
						continue // someone not known: left in the counts
					}
					x.Who = *h
				}
				token := pyStr(l["ZMESSAGETOKEN"])
				likes[token] = append(likes[token], x)
			}
		}
		for _, r := range maps(iphone, "SELECT * FROM ZVIBERMESSAGE ORDER BY Z_PK") {
			outgoing := !(isStr(r["ZSTATE"]) && str(r["ZSTATE"]) == "received")
			system := str(r["ZSYSTEMTYPE"])
			if system == "pollMessageInvisible" { // a vote: counted on its poll
				continue
			}
			var kind string
			switch {
			case truthy(r["ZATTACHMENT"]):
				var ok bool
				if kind, ok = iphoneAttachmentKinds[attachment[toInt(r["ZATTACHMENT"])]]; !ok {
					kind = "file"
				}
			case iphoneTextTypes[system]:
				kind = "text"
			case system == "customLocation":
				kind = "location"
			case system == "systemCallLog":
				kind = "call"
			default:
				kind = "system"
			}
			pk := toInt(r["Z_PK"])
			var sender *archive.Handle
			if !outgoing {
				sender = members[toInt(r["ZPHONENUMINDEX"])]
			}
			key := ""
			if truthy(r["ZTOKEN"]) {
				key = pyStr(r["ZTOKEN"])
			}
			conv, known := convs[toInt(r["ZCONVERSATION"])]
			if !known {
				panic(&db.Error{Query: "ZVIBERMESSAGE", Err: fmt.Errorf("KeyError: %v", r["ZCONVERSATION"])})
			}
			extra := viberIphoneExtras(r, locations)
			if ls := likes[key]; len(ls) > 0 {
				extra.Reactions = withWho(extra.Reactions, ls)
			}
			var by map[string]any
			if outgoing {
				by = map[string]any{"self": true}
			} else if sender != nil {
				by = NoticePerson(a, *sender, own)
			}
			if n, about := viberIphoneNotice(r, by); n != nil {
				extra.Notice = n
				if about != "" && extra.ReplyKey == "" {
					extra.ReplyKey = about
				}
			}
			if extra.EditsKey != "" { // an edit: the message edited takes its text, it is no line of its own
				edits = append(edits, viberEdit{extra.EditsKey, str(r["ZTEXT"])})
				continue
			}
			iphoneRecs[pk] = viberMsg{extra, conv, tokenTime(r["ZDATE"], toInt(r["ZTOKEN"])),
				outgoing, sender, kind, str(r["ZTEXT"]), key}
			iphoneOrder = append(iphoneOrder, pk)
			if key != "" {
				iphoneKeys[key] = pk
			}
		}
	}

	takenFromIphone := map[int64]bool{}
	var noticed []viberMsg // the desktop export's rows with a notice (iphoneRecs has the iPhone's)
	var takenOrder []int64
	events := "—"
	if desktop != nil {
		contact := map[any]*archive.Handle{}
		for _, r := range maps(desktop, "SELECT ContactID, MID, Number FROM Contact") {
			if h, ok := person.of(r["MID"], r["Number"]); ok {
				contact[r["ContactID"]] = &h
			} else {
				contact[r["ContactID"]] = nil
			}
		}
		chatMembers := map[any][]archive.Handle{}
		for _, r := range maps(desktop, "SELECT ChatID, ContactID FROM ChatRelation") {
			if c := contact[r["ContactID"]]; c != nil && !own[*c] {
				chatMembers[r["ChatID"]] = append(chatMembers[r["ChatID"]], *c)
			}
		}
		chats := map[any]int64{}
		for _, c := range maps(desktop, "SELECT ChatID, Name, Token, PGType, Flags FROM ChatInfo") {
			mem := chatMembers[c["ChatID"]]
			pg, pgInt := c["PGType"].(int64)
			switch {
			case pgInt && pg == viberChannel:
				chats[c["ChatID"]] = 0
			case truthy(c["Token"]):
				cid := titled(a, "viber", mem, "group:"+pyStr(c["Token"]), c["Name"])
				chats[c["ChatID"]] = cid
				// the notes are the iPhone's ZSUBTYPE 5, the same conversation: no group, the owner its member
				notes := toInt(c["Flags"])&desktopNotes != 0
				a.Exec("UPDATE conversation SET is_group = ? WHERE id = ?", archive.B2I(!notes), cid)
				if notes {
					for h := range own {
						if h.Kind == "phone" {
							a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", cid, a.Address(h))
						}
					}
				}
			default:
				if len(mem) == 0 {
					mem = []archive.Handle{archive.H("id", "chat:"+pyStr(c["ChatID"]), "viber")}
				}
				chats[c["ChatID"]] = a.Conversation("viber", mem, "", "")
			}
		}
		// reactions are events of their own (Type 3), tied to their message's token by LikeRelation: the
		// last of each person is theirs now
		likeEvents := map[int64]bool{}
		likes := map[string]map[string]viberLike{}
		for _, r := range maps(desktop, "SELECT l.MessageToken, e.EventID, e.Direction, e.ContactID, m.PGIsLiked, "+
			"m.SelfReaction FROM LikeRelation l JOIN Events e ON e.EventID = l.LikeEventID "+
			"LEFT JOIN Messages m ON m.EventID = e.EventID ORDER BY e.TimeStamp, e.EventID") {
			likeEvents[toInt(r["EventID"])] = true
			target := pyStr(r["MessageToken"])
			l := viberLike{mine: toInt(r["Direction"]) == 1, quick: toInt(r["PGIsLiked"]), emoji: str(r["SelfReaction"])}
			who := "me"
			if !l.mine {
				l.who, who = r["ContactID"], pyStr(r["ContactID"])
			}
			if likes[target] == nil {
				likes[target] = map[string]viberLike{}
			}
			likes[target][who] = l
		}
		var follows []desktopFollow
		iph := archive.Iphone()
		eachMap(desktop, "SELECT e.*, m.Type AS MessageType, m.Body, m.Info, m.Status, m.Subject, "+
			"m.Flag, m.PayloadPath, m.ThumbnailPath, m.StickerID, m.PttID, m.Duration"+desktopReactionColumns(desktop)+
			" FROM Events e LEFT JOIN Messages m USING (EventID) ORDER BY e.EventID", nil, func(r row) {
			typ, typInt := r["Type"].(int64)
			if likeEvents[toInt(r["EventID"])] {
				return // a reaction: on its message
			}
			system := typInt && typ == desktopSystemEvent
			key := ""
			if truthy(r["Token"]) && !system {
				key = pyStr(r["Token"])
			}
			info := jsonObj(r["Info"])
			if edit, ok := info["edit"].(map[string]any); ok && truthy(edit["token"]) {
				// an edit event: the message edited has the new text already (and an edit_token)
				follows = append(follows, desktopFollow{key: pyStr(edit["token"]), text: str(r["Body"]), edited: true})
				return
			}
			ts := toInt(r["TimeStamp"])
			pk, inIphone := iphoneKeys[key]
			inIphone = inIphone && key != ""
			if inIphone && (desktopDevice == "" || a.Keeper([]string{iph, desktopDevice}, ts) != desktopDevice) {
				return // the iPhone's copy is the one kept
			}
			if inIphone && !takenFromIphone[pk] {
				takenFromIphone[pk] = true
				takenOrder = append(takenOrder, pk)
			}
			outgoing := toInt(r["Direction"]) == 1 && isNumber(r["Direction"])
			kind := "text"
			if system {
				kind = "system"
			} else if mt, ok := r["MessageType"].(int64); ok {
				if k, ok := desktopKinds[mt]; ok {
					kind = k
				}
				if kind == "file" && truthy(jsonObj(r["Info"])["audio_ptt"]) { // a voice message sent as a file
					kind = "voice"
				}
			}
			var sender *archive.Handle
			if !outgoing {
				sender = contact[r["ContactID"]]
			}
			conv, known := chats[r["ChatID"]]
			if !known {
				panic(&db.Error{Query: "Events", Err: fmt.Errorf("KeyError: %v", r["ChatID"])})
			}
			extra := viberDesktopExtras(r)
			mt, _ := r["MessageType"].(int64)
			deleted := mt == desktopDeleted
			if deleted {
				extra.Deleted, kind = true, "text"
			}
			if truthy(obj(info["desktop_info"])["edit_token"]) {
				extra.Edited = true
			}
			var by map[string]any
			if outgoing {
				by = map[string]any{"self": true}
			} else if sender != nil {
				by = NoticePerson(a, *sender, own)
			}
			if n, about := viberPin(info, by); n != nil {
				extra.Notice = n
				if about != "" && extra.ReplyKey == "" {
					extra.ReplyKey = about
				}
			}
			rs, reactionsKnown := desktopReactions(r, likes[key], contact)
			if reactionsKnown {
				extra.Reactions = rs
			}
			add("desktop", pyStr(r["EventID"]), viberMsg{extra, conv, ts, outgoing, sender, kind, str(r["Body"]), key})
			if extra.Notice != nil && key != "" {
				noticed = append(noticed, viberMsg{extra: extra, key: key})
			}
			if key != "" && conv != 0 {
				follows = append(follows, desktopFollow{key: key, text: str(r["Body"]), edited: extra.Edited,
					deleted: deleted, reactions: rs, reactionsKnown: reactionsKnown, mentions: info["textMetaInfo"]})
			}
		})
		events = fmt.Sprint(db.Int(desktop, "SELECT count(*) FROM Events"))
		followDesktop(a, follows, person)
	}

	for _, pk := range iphoneOrder {
		if !takenFromIphone[pk] {
			add("iphone", fmt.Sprint(pk), iphoneRecs[pk])
		}
	}
	// notices on rows imported before there were notices (a poll's counts also change)
	for _, pk := range iphoneOrder {
		if r := iphoneRecs[pk]; r.extra.Notice != nil && r.key != "" {
			noticed = append(noticed, r)
		}
	}
	// reactions kept as bare counts before, now known by whom (the iPhone's ZLIKE)
	for _, pk := range iphoneOrder {
		r := iphoneRecs[pk]
		if r.key == "" || !slices.ContainsFunc(r.extra.Reactions, func(x archive.Reaction) bool { return x.Who != nil }) {
			continue
		}
		if mid, ok := a.MessageByKey("viber", r.key, 0); ok && mid != 0 &&
			!a.Exists("SELECT 1 FROM reaction WHERE message_id = ? AND address_id IS NOT NULL", mid) {
			ReplaceReactions(a, mid, r.extra.Reactions)
		}
	}
	for _, r := range noticed {
		if mid, ok := a.MessageByKey("viber", r.key, 0); ok && mid != 0 {
			if setNoticeIfChanged(a, mid, r.extra.Notice) && r.extra.ReplyKey != "" { // what it pinned
				a.Exec("UPDATE message SET reply_key = ? WHERE id = ? AND reply_key IS NULL", r.extra.ReplyKey, mid)
			}
		}
	}
	// the iPhone's copies of messages kept from the Android phone, as second origins: later imports
	// skip them without its export
	for _, pk := range takenOrder {
		if mid, ok := a.MessageByKey("viber", iphoneRecs[pk].key, 0); ok && mid != 0 {
			a.Exec("INSERT OR IGNORE INTO message_origin VALUES (?, ?, ?)", src["iphone"], fmt.Sprint(pk), mid)
		}
	}
	for _, e := range edits {
		if mid, ok := a.MessageByKey("viber", e.key, 0); ok && mid != 0 {
			c := Change{Edited: true}
			if e.text != "" {
				c.Text = &e.text
			}
			ApplyChange(a, mid, c)
		}
	}
	if iphone != nil { // observed: when the backup's copy was made
		iphoneMarks(a, iphone, src["iphone"], convs, convMembers, person, int64(mtime(iphoneDB)*1000))
	}
	a.Resolve()
	for _, s := range srcOrder {
		a.Imported(src[s])
	}
	a.Commit()

	say(out, "iPhone:  {n} messages, Desktop (Android): {events}", map[string]any{"n": len(iphoneRecs), "events": events})
	for _, s := range srcOrder {
		p := map[string]any{"n": fmt.Sprintf("%7d", added[s]), "source": s, "skipped": skipped[s]}
		if skipped[s] > 0 {
			say(out, "new:     {n} {source} ({skipped} were already there from another source)", p)
		} else {
			say(out, "new:     {n} {source}", p)
		}
	}
	return nil
}

// viberMentions are the people a message names, from its textMetaInfo (type 0: a member, by id;
// start and end in UTF-16 units of its text), on the iPhone and the desktop alike.
func viberMentions(a *archive.Archive, mid int64, text string, info any, person viberPeople) {
	l, _ := info.([]any)
	for _, e := range l {
		em, isMap := e.(map[string]any)
		if !isMap {
			continue
		}
		if _, f, _, isNum := num(em["type"]); !isNum || f != 0 { // Python's == 0: a 0.0 or a false too
			continue
		}
		who, wok := person.of(em["memberId"], nil)
		if !wok {
			continue
		}
		var said any
		start, sInt := jsonInt(em["start"])
		end, eInt := jsonInt(em["end"])
		if sInt && eInt {
			if s := utf16Slice(text, int(start), int(end)); s != "" {
				said = s
			}
		}
		a.Exec("INSERT OR IGNORE INTO mention VALUES (?, ?, ?)", mid, a.Address(who), said)
	}
}

// withWho is a message's reactions with each one's known (from), and the rest of the counts as
// they were, no one's.
func withWho(counts, from []archive.Reaction) []archive.Reaction {
	id := func(r archive.Reaction) string { return cmp.Or(r.Code, r.Emoji) } // an emoji of one's own has no code
	left := map[string]int{}
	for _, r := range counts {
		if r.Who == nil && !r.Outgoing {
			left[id(r)] += r.Count
		}
	}
	out := slices.Clone(from)
	for _, r := range from {
		left[id(r)]--
	}
	for _, r := range counts {
		if r.Who == nil && !r.Outgoing && left[id(r)] > 0 {
			out = append(out, archive.Reaction{Emoji: r.Emoji, Code: r.Code, Count: left[id(r)]})
			left[id(r)] = 0
		}
	}
	return out
}
