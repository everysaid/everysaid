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
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
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
		text := str(r["ZTEXT"])
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
// NoDesktop leaves the desktop export out (Python's desktop_db=None).
type ViberOptions struct {
	IphoneDB, DesktopDB string
	NoDesktop           bool
}

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
	desktopDB := opt.DesktopDB
	if opt.NoDesktop {
		desktopDB = ""
	} else if desktopDB == "" {
		desktopDB = config.ViberDesktop
	}
	var iphone, desktop *sql.DB
	if exists(iphoneDB) {
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
	src := map[string]int64{"iphone": a.Source(archive.Iphone()+"/viber", iphoneDB, archive.Iphone(), "")}
	srcOrder := []string{"iphone"}
	if desktopDB != "" {
		src["desktop"] = a.Source(archive.Android()+"/viber", desktopDB, archive.Android(), "")
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
		for _, r := range maps(iphone, "SELECT * FROM ZVIBERMESSAGE ORDER BY Z_PK") {
			outgoing := !(isStr(r["ZSTATE"]) && str(r["ZSTATE"]) == "received")
			system := str(r["ZSYSTEMTYPE"])
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
			iphoneRecs[pk] = viberMsg{viberIphoneExtras(r, locations), conv, tokenTime(r["ZDATE"], toInt(r["ZTOKEN"])),
				outgoing, sender, kind, str(r["ZTEXT"]), key}
			iphoneOrder = append(iphoneOrder, pk)
			if key != "" {
				iphoneKeys[key] = pk
			}
		}
	}

	takenFromIphone := map[int64]bool{}
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
		for _, c := range maps(desktop, "SELECT ChatID, Name, Token, PGType FROM ChatInfo") {
			mem := chatMembers[c["ChatID"]]
			pg, pgInt := c["PGType"].(int64)
			switch {
			case pgInt && pg == viberChannel:
				chats[c["ChatID"]] = 0
			case truthy(c["Token"]):
				cid := titled(a, "viber", mem, "group:"+pyStr(c["Token"]), c["Name"])
				chats[c["ChatID"]] = cid
				a.Exec("UPDATE conversation SET is_group = 1 WHERE id = ?", cid)
			default:
				if len(mem) == 0 {
					mem = []archive.Handle{archive.H("id", "chat:"+pyStr(c["ChatID"]), "viber")}
				}
				chats[c["ChatID"]] = a.Conversation("viber", mem, "", "")
			}
		}
		iph, android := archive.Iphone(), archive.Android()
		eachMap(desktop, "SELECT e.*, m.Type AS MessageType, m.Body, m.Info, m.Status, m.Subject, "+
			"m.Flag, m.PayloadPath, m.ThumbnailPath, m.StickerID, m.PttID, m.Duration "+
			"FROM Events e LEFT JOIN Messages m USING (EventID) ORDER BY e.EventID", nil, func(r row) {
			typ, typInt := r["Type"].(int64)
			system := typInt && typ == desktopSystemEvent
			key := ""
			if truthy(r["Token"]) && !system {
				key = pyStr(r["Token"])
			}
			ts := toInt(r["TimeStamp"])
			pk, inIphone := iphoneKeys[key]
			inIphone = inIphone && key != ""
			if inIphone && a.Keeper([]string{iph, android}, ts) != android {
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
			}
			var sender *archive.Handle
			if !outgoing {
				sender = contact[r["ContactID"]]
			}
			conv, known := chats[r["ChatID"]]
			if !known {
				panic(&db.Error{Query: "Events", Err: fmt.Errorf("KeyError: %v", r["ChatID"])})
			}
			add("desktop", pyStr(r["EventID"]), viberMsg{viberDesktopExtras(r), conv, ts, outgoing, sender, kind,
				str(r["Body"]), key})
		})
		events = fmt.Sprint(db.Int(desktop, "SELECT count(*) FROM Events"))
	}

	for _, pk := range iphoneOrder {
		if !takenFromIphone[pk] {
			add("iphone", fmt.Sprint(pk), iphoneRecs[pk])
		}
	}
	// the iPhone's copies of messages kept from the Android phone, as second origins: later imports
	// skip them without its export
	for _, pk := range takenOrder {
		if mid, ok := a.MessageByKey("viber", iphoneRecs[pk].key, 0); ok && mid != 0 {
			a.Exec("INSERT OR IGNORE INTO message_origin VALUES (?, ?, ?)", src["iphone"], fmt.Sprint(pk), mid)
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
