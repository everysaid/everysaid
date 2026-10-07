// Ports everysaid/sms.py: SMS, MMS, iMessage and RCS from the iPhone's sms.db, and SMS and MMS from
// an Android phone's export (android-export.py). Either may be missing.
//
// Two phones may carry the same SMS history, copied from phone to phone. A pair (same
// direction, same text, times at most 2 s apart; one to one) becomes one message with a single
// origin: the device in use at the time (`device` in the archive), else the newer one. Unpaired
// rows come in from wherever they are. Exact repeats within one source (same second, direction,
// sender and text) are taken once.
//
// SMS and MMS are paired together: an Android phone may keep plain texts as MMS (long ones, or with
// a link) that the iPhone has as SMS. Both live in one conversation per counterpart (service 'sms').
package importers

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"everysaid/internal/archive"
	"everysaid/internal/config"
)

// SMSIphoneDB is where the iPhone's sms.db is.
func SMSIphoneDB() string { return filepath.Join(archive.IphoneData(), "sms.db") }

const (
	pairMS          = 2000
	groupStyle      = 43
	androidMMSSent  = "2"
	androidMMSFrom  = "137"
	smsNoSourceLine = "no source: neither {db} nor an Android export"
)

type smsRec struct {
	source   string // source name: 'iphone/sms', '<device>/sms', '<device>/mms'
	rowKey   string
	raw      row
	service  string
	ts       int64
	outgoing bool
	sender   *archive.Handle // nil when outgoing
	members  []archive.Handle
	kind     string
	text     string // "" is none
	key      string
}

// attributedText is the plain text of an NSAttributedString typedstream (sms.db attributedBody).
func attributedText(blob []byte) (string, bool) {
	if len(blob) == 0 {
		return "", false
	}
	i := bytes.Index(blob, []byte("NSString"))
	if i < 0 {
		return "", false
	}
	j := bytes.Index(blob[i:], []byte("\x84\x01+"))
	if j < 0 {
		return "", false
	}
	i += j + 3
	if i >= len(blob) {
		return "", false
	}
	n := int(blob[i])
	switch {
	case n == 0x81 && i+3 <= len(blob):
		n, i = int(binary.LittleEndian.Uint16(blob[i+1:i+3])), i+3
	case n == 0x82 && i+5 <= len(blob):
		n, i = int(binary.LittleEndian.Uint32(blob[i+1:i+5])), i+5
	default:
		i++
	}
	end := min(i+n, len(blob))
	return decodeReplace(blob[min(i, end):end]), true
}

// cleanText is the text without object marks, stripped; "" for none.
func cleanText(text string) string {
	return pyStrip(strings.ReplaceAll(text, "\ufffc", ""))
}

func kindOfMime(mime string) string {
	top, _, _ := strings.Cut(mime, "/")
	switch top {
	case "image":
		return "image"
	case "video":
		return "video"
	case "audio":
		return "voice"
	}
	return "file"
}

// floorDiv is Python's //.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func address(raw string) archive.Handle {
	k, v := archive.Address(raw, config.Region)
	return archive.H(k, v)
}

func readIphoneSMS(path string) []*smsRec {
	d := ro(path)
	defer d.Close()
	handles := map[any]string{}
	for _, r := range maps(d, "SELECT ROWID, id FROM handle") {
		handles[r["ROWID"]] = str(r["id"])
	}
	chats := map[any]row{}
	for _, r := range maps(d, "SELECT ROWID, guid, style, display_name FROM chat") {
		chats[r["ROWID"]] = r
	}
	chatOf := map[any]any{}
	for _, r := range maps(d, "SELECT message_id, chat_id FROM chat_message_join") {
		chatOf[r["message_id"]] = r["chat_id"]
	}
	members := map[any][]archive.Handle{}
	for _, r := range maps(d, "SELECT chat_id, handle_id FROM chat_handle_join") {
		members[r["chat_id"]] = append(members[r["chat_id"]], address(handles[r["handle_id"]]))
	}
	mime := map[any]string{}
	for _, r := range maps(d, "SELECT j.message_id, a.mime_type FROM message_attachment_join j "+
		"JOIN attachment a ON a.ROWID = j.attachment_id ORDER BY a.ROWID") {
		if _, ok := mime[r["message_id"]]; !ok {
			mime[r["message_id"]] = str(r["mime_type"])
		}
	}
	services := map[string]string{"SMS": "sms", "iMessage": "imessage", "RCS": "rcs"}
	var recs []*smsRec
	eachMap(d, "SELECT * FROM message ORDER BY ROWID", nil, func(r row) {
		id := r["ROWID"]
		var chat row
		if c, ok := chatOf[id]; ok {
			chat = chats[c]
		}
		var sender *archive.Handle
		if !truthy(r["is_from_me"]) && truthy(r["handle_id"]) {
			h := address(handles[r["handle_id"]])
			sender = &h
		}
		var mem []archive.Handle
		if chat != nil {
			mem = members[chat["ROWID"]]
		}
		if len(mem) == 0 && sender != nil {
			mem = []archive.Handle{*sender}
		}
		service, ok := services[str(r["service"])]
		if !ok {
			panic(fmt.Errorf("sms.db: unknown service %q", str(r["service"])))
		}
		if service == "sms" && (truthy(r["cache_has_attachments"]) || (chat != nil && toInt(chat["style"]) == groupStyle)) {
			service = "mms"
		}
		date := toInt(r["date"])
		var ts int64
		if float64(date) > 1e11 {
			ts = floorDiv(date, 1_000_000)
		} else {
			ts = date * 1000
		}
		ts += archive.AppleEpoch * 1000
		kind := "text"
		switch {
		case truthy(r["associated_message_type"]):
			kind = "reaction"
		case truthy(r["item_type"]):
			kind = "system"
		default:
			if m, ok := mime[id]; ok {
				kind = kindOfMime(m)
			}
		}
		var text string
		if r["text"] != nil {
			text = str(r["text"])
		} else {
			text, _ = attributedText(asBytes(r["attributedBody"]))
		}
		guid := str(r["guid"])
		key := ""
		if service == "imessage" {
			key = guid
		}
		recs = append(recs, &smsRec{archive.Iphone() + "/sms", guid, r, service, ts, truthy(r["is_from_me"]), sender, mem,
			kind, cleanText(text), key})
	})
	return recs
}

// readAndroidSMS is one Android export's SMS and MMS; own: the owner's addresses, left out of MMS
// members.
func readAndroidSMS(path string, own map[archive.Handle]bool, device string) []*smsRec {
	d := ro(path)
	defer d.Close()
	var recs []*smsRec
	for _, r := range maps(d, "SELECT * FROM sms ORDER BY CAST(_id AS INTEGER)") {
		outgoing := !(isStr(r["type"]) && str(r["type"]) == "1")
		addr := address(str(r["address"]))
		var sender *archive.Handle
		if !outgoing {
			sender = &addr
		}
		date, _ := pyInt(r["date"])
		recs = append(recs, &smsRec{device + "/sms", pyStr(r["_id"]), r, "sms", date, outgoing, sender,
			[]archive.Handle{addr}, "text", cleanText(str(r["body"])), ""})
	}
	parts, addrs := map[any][]row{}, map[any][]row{}
	for _, p := range maps(d, "SELECT * FROM mms_part ORDER BY CAST(seq AS INTEGER), CAST(_id AS INTEGER)") {
		parts[p["mid"]] = append(parts[p["mid"]], p)
	}
	for _, a := range maps(d, "SELECT * FROM mms_addr ORDER BY CAST(_id AS INTEGER)") {
		addrs[a["msg_id"]] = append(addrs[a["msg_id"]], a)
	}
	notContent := map[string]bool{"text/plain": true, "application/smil": true}
	for _, r := range maps(d, "SELECT * FROM mms ORDER BY CAST(_id AS INTEGER)") {
		ps, as := parts[r["_id"]], addrs[r["_id"]]
		outgoing := isStr(r["msg_box"]) && str(r["msg_box"]) == androidMMSSent
		var members []archive.Handle
		for _, a := range as {
			if m := address(str(a["address"])); !own[m] && !containsHandle(members, m) {
				members = append(members, m)
			}
		}
		var sender *archive.Handle
		if !outgoing {
			for _, a := range as {
				if isStr(a["type"]) && str(a["type"]) == androidMMSFrom {
					h := address(str(a["address"]))
					sender = &h
					break
				}
			}
		}
		var media []string
		var text strings.Builder
		for _, p := range ps {
			ct := str(p["ct"])
			if !notContent[ct] {
				media = append(media, ct)
			}
			if ct == "text/plain" {
				text.WriteString(str(p["text"]))
			}
		}
		kind := "text"
		if len(media) > 0 {
			kind = kindOfMime(media[0])
		}
		date, _ := pyInt(r["date"])
		recs = append(recs, &smsRec{device + "/mms", pyStr(r["_id"]), r, "mms", date * 1000, outgoing, sender, members,
			kind, cleanText(text.String()), ""})
	}
	return recs
}

func isStr(v any) bool { _, ok := v.(string); return ok }

// takeTapbacksBack removes the tapbacks taken back: an iMessage reaction (associated_message_type
// 2000-2006) is taken back by one of the same sender, on the same message, 1000 higher; the last
// of them says whether it stands. The tapbacks themselves come in as reactions through
// Archive.Resolve.
func takeTapbacksBack(a *archive.Archive, iphone []*smsRec) {
	type tapback struct {
		target, sender string
		kind           int64
		emoji          string
	}
	removed := map[tapback]bool{}
	var order []tapback
	for _, r := range iphone {
		kind, _ := pyInt(r.raw["associated_message_type"])
		g := str(r.raw["associated_message_guid"])
		if kind < 2000 || kind >= 4000 || g == "" {
			continue
		}
		if i := strings.Index(g, "/"); i >= 0 {
			g = g[i+1:]
		}
		sender := "me"
		if !r.outgoing && r.sender != nil {
			sender = r.sender.Kind + ":" + r.sender.Value
		}
		k := tapback{g, sender, 2000 + kind%1000, ""}
		if k.kind == 2006 { // its own emoji: each one apart
			k.emoji = str(r.raw["associated_message_emoji"])
		}
		if _, seen := removed[k]; !seen {
			order = append(order, k)
		}
		removed[k] = kind >= 3000
	}
	imessage := a.Service.ID("imessage")
	for _, k := range order {
		if !removed[k] {
			continue
		}
		target, ok := a.IntOK("SELECT id FROM message WHERE service_id = ? AND key = ?", imessage, k.target)
		if !ok {
			continue
		}
		q, args := "DELETE FROM reaction WHERE message_id = ? AND code = ? AND outgoing = 1",
			[]any{target, fmt.Sprintf("imessage:%d", k.kind)}
		if k.sender != "me" {
			kind, value, _ := strings.Cut(k.sender, ":")
			q, args = "DELETE FROM reaction WHERE message_id = ? AND code = ? AND address_id = ?",
				append(args, a.Address(archive.H(kind, value)))
		}
		if k.emoji != "" {
			q, args = q+" AND emoji = ?", append(args, k.emoji)
		}
		a.Exec(q, args...)
	}
}

// bothWays is each pair (kept, other) and also (other, kept): Archive.RecordPairs records the
// second copy where the first is in the archive, and the copy in the archive is the one kept on
// this import, or the other one where that came in on an earlier import.
func bothWays(pairs [][2]archive.Origin) [][2]archive.Origin {
	out := append([][2]archive.Origin(nil), pairs...)
	for _, p := range pairs {
		out = append(out, [2]archive.Origin{p[1], p[0]})
	}
	return out
}

// collapse drops exact repeats within one source; it gives (kept, dropped).
func collapse(recs []*smsRec) ([]*smsRec, int) {
	type k struct {
		service   string
		outgoing  bool
		sender    archive.Handle
		hasSender bool
		text      string
		second    int64
	}
	seen := map[k]bool{}
	var kept []*smsRec
	for _, r := range recs {
		key := k{service: r.service, outgoing: r.outgoing, text: r.text, second: floorDiv(r.ts, 1000)}
		if r.sender != nil {
			key.sender, key.hasSender = *r.sender, true
		}
		if !seen[key] {
			seen[key] = true
			kept = append(kept, r)
		}
	}
	return kept, len(recs) - len(kept)
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// pairSMS: one-to-one pairs of iPhone and Android SMS: same direction and text, nearest time
// within 2 s.
func pairSMS(iphone, android []*smsRec) (map[*smsRec]*smsRec, map[*smsRec]bool) {
	type k struct {
		outgoing bool
		text     string
	}
	index := map[k][]*smsRec{}
	for _, h := range android {
		index[k{h.outgoing, h.text}] = append(index[k{h.outgoing, h.text}], h)
	}
	used, pairs := map[*smsRec]bool{}, map[*smsRec]*smsRec{}
	sorted := append([]*smsRec(nil), iphone...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].ts < sorted[b].ts })
	for _, i := range sorted {
		if i.service != "sms" && i.service != "mms" {
			continue
		}
		var best *smsRec
		for _, h := range index[k{i.outgoing, i.text}] {
			if !used[h] && abs64(h.ts-i.ts) <= pairMS && (best == nil || abs64(h.ts-i.ts) < abs64(best.ts-i.ts)) {
				best = h
			}
		}
		if best != nil {
			used[best] = true
			pairs[i] = best
		}
	}
	return pairs, used
}

// SMSOptions: IphoneDB "" is the default; Exports nil is every Android export there is (an empty,
// non-nil list is none).
type SMSOptions struct {
	IphoneDB string
	Exports  []archive.AndroidExport
}

func SMS(a *archive.Archive, out func(string), opt SMSOptions) (err error) {
	defer archive.Recover(&err)
	iphoneDB := opt.IphoneDB
	if iphoneDB == "" {
		iphoneDB = SMSIphoneDB()
	}
	exports := opt.Exports
	if exports == nil {
		exports = archive.AndroidExports("")
	}
	var iphone []*smsRec
	if exists(iphoneDB) {
		iphone = readIphoneSMS(iphoneDB)
	}
	if len(iphone) == 0 && len(exports) == 0 {
		say(out, smsNoSourceLine, map[string]any{"db": iphoneDB})
		return nil
	}
	total := len(iphone)
	iphone, iDup := collapse(iphone)
	var android []*smsRec
	aDup, aTotal := 0, 0
	for _, e := range exports { // repeats are dropped within each phone
		recs := readAndroidSMS(e.DB, a.Own(), e.Device)
		aTotal += len(recs)
		kept, dup := collapse(recs)
		android, aDup = append(android, kept...), aDup+dup
	}
	pairs, used := pairSMS(iphone, android)
	iph := archive.Iphone()

	fromAndroid := func(h *smsRec) bool {
		device, _, _ := strings.Cut(h.source, "/")
		return a.Keeper([]string{iph, device}, h.ts) == device
	}

	var chosen []*smsRec
	other := map[*smsRec]*smsRec{} // the copy chosen -> the other phone's copy
	for _, i := range iphone {
		if h := pairs[i]; h != nil && fromAndroid(h) {
			chosen = append(chosen, h)
			other[h] = i
		} else {
			chosen = append(chosen, i)
			if h != nil {
				other[i] = h
			}
		}
	}
	for _, h := range android {
		if !used[h] {
			chosen = append(chosen, h)
		}
	}

	sources := map[string]int64{iph + "/sms": a.Source(iph+"/sms", iphoneDB, iph, "")}
	order := []string{iph + "/sms"}
	for _, e := range exports {
		for _, kind := range []string{"sms", "mms"} {
			name := e.Device + "/" + kind
			if _, ok := sources[name]; !ok {
				order = append(order, name)
			}
			sources[name] = a.Source(name, e.DB, e.Device, "")
		}
	}
	type addedKey struct{ source, service string }
	added := map[addedKey]int{}
	sort.SliceStable(chosen, func(x, y int) bool { return chosen[x].ts < chosen[y].ts })
	for _, r := range chosen {
		sid := sources[r.source]
		if a.HasOrigin(sid, r.rowKey, "") {
			continue
		}
		// the other phone's copy came in on an earlier import (this phone's rows were not read
		// then): that one stays, and this row becomes its second origin below
		if o := other[r]; o != nil && a.HasOrigin(sources[o.source], o.rowKey, "") {
			continue
		}
		service := r.service
		if service == "mms" {
			service = "sms"
		}
		conv := a.Conversation(service, r.members, "", "")
		var senderID int64
		if r.sender != nil {
			senderID = a.Address(*r.sender)
		}
		var x *archive.Extras
		if r.source == iph+"/sms" {
			x = imessageExtras(r.raw)
		}
		a.AddMessage(sid, r.rowKey, archive.Message{Service: r.service, ConversationID: conv, TS: r.ts, Outgoing: r.outgoing,
			SenderID: senderID, Kind: r.kind, Text: r.text, Key: r.key, Extras: x})
		added[addedKey{r.source, r.service}]++
	}
	// the copy not kept, as a second origin: later imports skip it without the other phone
	var pairList [][2]archive.Origin
	fromAndroidN := 0
	for _, i := range iphone {
		if h := pairs[i]; h != nil {
			if fromAndroid(h) {
				pairList = append(pairList, [2]archive.Origin{{Source: h.source, RowKey: h.rowKey}, {Source: i.source, RowKey: i.rowKey}})
				fromAndroidN++
			} else {
				pairList = append(pairList, [2]archive.Origin{{Source: i.source, RowKey: i.rowKey}, {Source: h.source, RowKey: h.rowKey}})
			}
		}
	}
	a.RecordPairs(sources, bothWays(pairList), "")
	a.Resolve()
	takeTapbacksBack(a, iphone)
	for _, s := range order {
		a.Imported(sources[s])
	}
	a.Commit()

	var devices []string
	for _, e := range exports {
		devices = append(devices, e.Device)
	}
	devs := strings.Join(devices, ", ")
	if devs == "" {
		devs = "—"
	}
	say(out, "iPhone: {n} rows ({dup} repeats)", map[string]any{"n": total, "dup": iDup})
	say(out, "Android ({devices}): {n} rows of SMS and MMS ({dup} repeats)", map[string]any{"devices": devs, "n": aTotal, "dup": aDup})
	say(out, "pairs:  {n} (from Android in its time: {m})", map[string]any{"n": len(pairs), "m": fromAndroidN})
	var keys []addedKey
	for k := range added {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(x, y int) bool {
		if keys[x].source != keys[y].source {
			return keys[x].source < keys[y].source
		}
		return keys[x].service < keys[y].service
	})
	for _, k := range keys {
		say(out, "new:    {n} {source} {service}", map[string]any{"n": fmt.Sprintf("%6d", added[k]), "source": k.source, "service": k.service})
	}
	if len(added) == 0 {
		say(out, "new:    none", nil)
	}
	return nil
}
