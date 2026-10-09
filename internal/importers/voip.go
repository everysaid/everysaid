// Ports everysaid/voip.py: calls the phones' call logs do not have: WhatsApp's and Viber's, and
// missed calls known only from the carrier's text messages.
//
//   - WhatsApp: its own call log (CallHistory.sqlite in the backup, extracted as
//     whatsapp-calls.sqlite: start, duration, outcome, video, group, participants) and the call
//     bubbles in the chats (ZWAMESSAGE type 59: the same facts in the media item's metadata, field
//     87: 1.1 video, 1.2 outcome, 1.3 duration in seconds, 1.5 participants). Both describe the same
//     calls for the months they overlap; a call is kept once, the log's id as its key.
//   - Viber: the iPhone's recents (ZRECENT, with the number in ZRECENTSLINE).
//   - The carrier's missed-call notices, read from the archive's SMS by the parsers config
//     [import] carrier_notices enables ("gr" for the Greek ones): calls that came while the phone
//     was off or busy, each with how many times the number called. Those a call log already has
//     are left to it.
//
// A call is skipped when the archive already has it: the same row (call_origin), or from another
// source, for the same service, a call with the same other person in the same direction within a
// minute (the iPhone's CallHistory shows some WhatsApp calls too). Calls without a known person are
// never merged. Runs after `calls`, which does not look at the calls added here.
package importers

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"everysaid/internal/archive"
	"everysaid/internal/config"
)

// WhatsAppCallsDB is where the iPhone's WhatsApp call log is.
func WhatsAppCallsDB() string { return filepath.Join(archive.IphoneData(), "whatsapp-calls.sqlite") }

const sameCallMS = 60_000

// WhatsApp's outcome (ZWACDCALLEVENT.ZOUTCOME, field 87 / 1.2): 0 connected, 1 not answered; 4 and 5
// come only on incoming calls without duration (4 with the "missed" flag).
var whatsappOutcomes = map[int64]string{4: "missed", 5: "failed"}

// CallSet adds calls, each once, counting the new ones by service.
type CallSet struct {
	a     *archive.Archive
	Added map[string]int
}

func NewCallSet(a *archive.Archive) *CallSet { return &CallSet{a: a, Added: map[string]int{}} }

// existing is the same call from another source (calls of one source are all distinct, even a
// minute apart): same person and direction. Calls without a person (hidden numbers, groups) are
// never taken for one another.
func (c *CallSet) existing(sourceID int64, service string, addressID, ts int64, outgoing bool) (int64, bool, bool) {
	if addressID == 0 {
		return 0, false, false
	}
	var id int64
	var answered bool
	found := c.a.Row("SELECT id, answered FROM call WHERE service_id = ? AND address_id = ? AND outgoing = ? "+
		"AND abs(ts - ?) <= ? AND id NOT IN (SELECT call_id FROM call_origin WHERE source_id = ?) "+
		"ORDER BY abs(ts - ?)", []any{c.a.Service.ID(service), addressID, archive.B2I(outgoing), ts, sameCallMS, sourceID, ts},
		&id, &answered)
	return id, answered, found
}

// add adds a call; rowKeys: every source row that describes it. It gives the call's id, 0 where
// its row was there already.
func (c *CallSet) add(sourceID int64, rowKeys []string, call archive.Call, added ...func(id int64)) int64 {
	a := c.a
	for _, k := range rowKeys {
		if a.HasOrigin(sourceID, k, "call_origin") {
			return 0
		}
	}
	id, answered, found := c.existing(sourceID, call.Service, call.AddressID, call.TS, call.Outgoing)
	if found {
		// a call log that says answered is not overruled by "missed" or "busy" from elsewhere
		detail, code := call.Detail, call.DetailCode
		if answered {
			detail, code = "", ""
		}
		attempts := call.Attempts
		if attempts == 0 {
			attempts = 1
		}
		a.Exec("UPDATE call SET video = max(video, ?), conversation_id = coalesce(conversation_id, ?), "+
			"detail_code = CASE WHEN detail IS NULL THEN ? ELSE detail_code END, "+
			"detail = coalesce(detail, ?), key = coalesce(key, ?), attempts = max(attempts, ?) "+
			"WHERE id = ?", archive.B2I(call.Video), archive.NullID(call.ConversationID), archive.NullStr(code),
			archive.NullStr(detail), archive.NullStr(call.Key), attempts, id)
	} else {
		id = a.AddCall(sourceID, rowKeys[0], call)
		for _, f := range added {
			f(id)
		}
		rowKeys = rowKeys[1:]
		c.Added[call.Service]++
	}
	for _, k := range rowKeys {
		a.Exec("INSERT OR IGNORE INTO call_origin VALUES (?, ?, ?)", sourceID, k, id)
	}
	return id
}

// outcomeDetail is (detail, its code) of a WhatsApp outcome.
func outcomeDetail(outcome any, outgoing bool) (string, string) {
	o, isInt := outcome.(int64)
	if u, ok := outcome.(uint64); ok {
		o, isInt = int64(u), true
	}
	if !isInt {
		return "", ""
	}
	if o == 1 {
		if outgoing {
			return "unanswered", "whatsapp:1"
		}
		return "missed", "whatsapp:1"
	}
	if d, ok := whatsappOutcomes[o]; ok {
		return d, fmt.Sprintf("whatsapp:%d", o)
	}
	return "", ""
}

func isZero(v any) bool {
	switch x := v.(type) {
	case int64:
		return x == 0
	case uint64:
		return x == 0
	case float64:
		return x == 0
	case bool:
		return !x
	}
	return false
}

type bubble struct {
	pk              string
	ts              int64
	outgoing        bool
	jid             string
	isGroup         bool
	video, duration int64
	outcome         any
	members         []string
}

type logCall struct {
	pk       string
	ts       int64
	outgoing bool
	video    int64
	outcome  any
	duration int64
	group    string
	key      string
	members  [][2]any // (jid, outcome)
	creator  string
}

func whatsappCalls(a *archive.Archive, calls *CallSet) {
	if !exists(WhatsAppCallsDB()) || !exists(WhatsAppIphoneDB()) {
		return
	}
	iphone := ro(WhatsAppIphoneDB())
	defer iphone.Close()
	var closers []*sql.DB
	open := func(p string) *sql.DB {
		d := roBridge(p)
		if d != nil {
			closers = append(closers, d)
		}
		return d
	}
	person := newWAPeople(open(WhatsAppContactsDB()), open(BridgeStore()), iphone, open(BridgeDB()))
	for _, d := range closers {
		defer d.Close()
	}
	own := a.Own()
	logSrc := a.Source(archive.Iphone()+"/whatsapp-calls", WhatsAppCallsDB(), archive.Iphone(), "")
	chatSrc := a.Source(archive.Iphone()+"/whatsapp", WhatsAppIphoneDB(), archive.Iphone(), "")

	addr := func(jid string) int64 {
		if jid == "" {
			return 0
		}
		if p, ok := person.of(jid); ok && !own[p] {
			return a.Address(p)
		}
		return 0
	}
	group := func(jid string) int64 {
		if jid == "" {
			return 0
		}
		return a.Int("SELECT id FROM conversation WHERE key = ?", jid)
	}

	// the call bubbles in the chats
	var bubbles []*bubble
	for _, r := range maps(iphone, "SELECT m.Z_PK, m.ZMESSAGEDATE, m.ZISFROMME, s.ZCONTACTJID, s.ZSESSIONTYPE, i.ZMETADATA "+
		"FROM ZWAMESSAGE m JOIN ZWACHATSESSION s ON s.Z_PK = m.ZCHATSESSION "+
		"LEFT JOIN ZWAMEDIAITEM i ON i.Z_PK = m.ZMEDIAITEM WHERE m.ZMESSAGETYPE = 59") {
		info := protobufFields(asBytes(r["ZMETADATA"]))[87]
		var fields map[uint64][]any
		if len(info) > 0 {
			f1 := protobufFields(asBytes(info[0]))[1]
			first := []byte{}
			if len(f1) > 0 {
				first = asBytes(f1[0])
			}
			fields = protobufFields(first)
		}
		var members []string
		for _, p := range fields[5] {
			m := protobufFields(asBytes(p))[1]
			var v []byte
			if len(m) > 0 {
				v = asBytes(m[0])
			}
			if utf8.Valid(v) { // a member that is not text is no one known: left out, the call kept
				members = append(members, string(v))
			}
		}
		b := &bubble{pk: pyStr(r["Z_PK"]), ts: appleMS(toFloat(r["ZMESSAGEDATE"])), outgoing: truthy(r["ZISFROMME"]),
			jid: str(r["ZCONTACTJID"]), isGroup: toInt(r["ZSESSIONTYPE"]) == 1, members: members}
		if v := fields[1]; len(v) > 0 {
			b.video = toInt(v[0])
		}
		if v := fields[2]; len(v) > 0 {
			b.outcome = v[0]
		}
		if v := fields[3]; len(v) > 0 {
			b.duration = toInt(v[0])
		}
		bubbles = append(bubbles, b)
	}

	// the call log
	var log []*logCall
	logDB := ro(WhatsAppCallsDB())
	defer logDB.Close()
	parts := map[any][][2]any{}
	for _, r := range maps(logDB, "SELECT Z1PARTICIPANTS, ZJIDSTRING, ZOUTCOME FROM ZWACDCALLEVENTPARTICIPANT") {
		parts[r["Z1PARTICIPANTS"]] = append(parts[r["Z1PARTICIPANTS"]], [2]any{r["ZJIDSTRING"], r["ZOUTCOME"]})
	}
	for _, r := range maps(logDB, "SELECT e.*, a.ZINCOMING, a.ZMISSED, a.ZVIDEO FROM ZWACDCALLEVENT e "+
		"LEFT JOIN ZWAAGGREGATECALLEVENT a ON a.Z_PK = e.Z1CALLEVENTS") {
		log = append(log, &logCall{pk: pyStr(r["Z_PK"]), ts: appleMS(toFloat(r["ZDATE"])), outgoing: !truthy(r["ZINCOMING"]),
			video: toInt(r["ZVIDEO"]), outcome: r["ZOUTCOME"], duration: roundEven(toFloat(r["ZDURATION"])),
			// participants belong to the aggregate event (Z1PARTICIPANTS -> entity 1), not to each call
			group: str(r["ZGROUPJIDSTRING"]), key: str(r["ZCALLIDSTRING"]), members: parts[r["Z1CALLEVENTS"]],
			creator: str(r["ZGROUPCALLCREATORUSERJIDSTRING"])})
	}

	// One call in both: the bubble falls between the call's start (a minute's leeway) and its end
	// plus a minute, in the same direction; the nearest such. People are taken from the bubble's
	// chat, which names them by number (the log uses WhatsApp's internal ids).
	pairs := map[string]*bubble{}
	free := append([]*bubble(nil), bubbles...)
	sort.SliceStable(free, func(i, j int) bool { return free[i].ts < free[j].ts })
	sortedLog := append([]*logCall(nil), log...)
	sort.SliceStable(sortedLog, func(i, j int) bool { return sortedLog[i].ts < sortedLog[j].ts })
	for _, c := range sortedLog {
		best := -1
		for i, b := range free {
			if b.outgoing == c.outgoing && c.ts-sameCallMS <= b.ts && b.ts <= c.ts+c.duration*1000+sameCallMS &&
				(best < 0 || abs64(b.ts-c.ts) < abs64(free[best].ts-c.ts)) {
				best = i
			}
		}
		if best >= 0 {
			pairs[c.pk] = free[best]
			free = append(free[:best:best], free[best+1:]...)
		}
	}
	for _, c := range log {
		b := pairs[c.pk]
		groupJID := c.group
		if groupJID == "" && b != nil && b.isGroup {
			groupJID = b.jid
		}
		// who: the bubble's chat; else the log's participant; else who started it (the other
		// person on an incoming call; on an outgoing one that is the owner, and stays unknown)
		var peer int64
		switch {
		case b != nil && !b.isGroup:
			peer = addr(b.jid)
		case groupJID != "":
		default:
			if len(c.members) > 0 {
				peer = addr(str(c.members[0][0]))
			}
			if peer == 0 {
				peer = addr(c.creator)
			}
		}
		detail, code := outcomeDetail(c.outcome, c.outgoing)
		duration, video := c.duration, c.video
		if duration == 0 && b != nil {
			duration = b.duration
		}
		if video == 0 && b != nil {
			video = b.video
		}
		callID := calls.add(logSrc, []string{c.pk}, archive.Call{Service: "whatsapp", AddressID: peer, TS: c.ts,
			Outgoing: c.outgoing, Answered: isZero(c.outcome), Duration: duration, Key: c.key, Detail: detail,
			DetailCode: code, Video: video != 0, ConversationID: group(groupJID)})
		if callID != 0 && b != nil {
			a.Exec("INSERT OR IGNORE INTO call_origin VALUES (?, ?, ?)", chatSrc, b.pk, callID)
		}
		if callID != 0 && groupJID != "" {
			members := c.members
			if len(members) == 0 && b != nil {
				for _, j := range b.members {
					members = append(members, [2]any{j, nil})
				}
			}
			for _, m := range members {
				said, code := "joined", "whatsapp:0"
				if !isZero(m[1]) || m[1] == nil {
					said, code = outcomeDetail(m[1], false)
				}
				a.Exec("INSERT OR IGNORE INTO call_member VALUES (?, ?, ?, ?)", callID, archive.NullID(addr(str(m[0]))),
					archive.NullStr(said), archive.NullStr(code))
			}
		}
	}
	for _, b := range free {
		detail, code := outcomeDetail(b.outcome, b.outgoing)
		var peer, conv int64
		if b.isGroup {
			conv = group(b.jid)
		} else {
			peer = addr(b.jid)
		}
		callID := calls.add(chatSrc, []string{b.pk}, archive.Call{Service: "whatsapp", AddressID: peer, TS: b.ts,
			Outgoing: b.outgoing, Answered: isZero(b.outcome), Duration: b.duration, Detail: detail, DetailCode: code,
			Video: b.video != 0, ConversationID: conv})
		if callID != 0 && b.isGroup {
			for _, j := range b.members {
				a.Exec("INSERT OR IGNORE INTO call_member (call_id, address_id) VALUES (?, ?)", callID, archive.NullID(addr(j)))
			}
		}
	}
}

// The bridge's call-log outcomes (whatsmeow's CallLogMessage.CallOutcome) as our call.detail.
var bridgeOutcomes = map[string]string{"MISSED": "missed", "FAILED": "failed", "REJECTED": "rejected"}

// BridgeCalls: WhatsApp calls the bridge saw: the call-log message every device gets after a call
// (outcome, duration, video, participants), and the call signalling it receives itself (an incoming
// call offered, accepted, ended), for calls no log message came for. One call in several of these,
// or already in the archive from the iPhone, is kept once (same person and direction within a
// minute). bridgeDB and storeDB "" are none.
func BridgeCalls(a *archive.Archive, calls *CallSet, bridgeDB, storeDB string) (err error) {
	defer archive.Recover(&err)
	bridgeCalls(a, calls, bridgeDB, storeDB)
	a.PurgeSpam()
	return nil
}

func bridgeCalls(a *archive.Archive, calls *CallSet, bridgeDB, storeDB string) {
	bridge := roBridge(bridgeDB)
	if bridge == nil {
		return
	}
	defer bridge.Close()
	if !hasTable(bridge, "calls") {
		return
	}
	store := roBridge(storeDB)
	if store != nil {
		defer store.Close()
	}
	person := newWAPeople(nil, store, nil, bridge)
	own := a.Own()
	logSrc := a.Source("whatsapp-bridge/calls", bridgeDB, "whatsapp-bridge", "")
	eventSrc := a.Source("whatsapp-bridge/call-events", bridgeDB, "whatsapp-bridge", "")

	addr := func(jid string) int64 {
		if jid == "" {
			return 0
		}
		if p, ok := person.of(jid); ok && !own[p] {
			return a.Address(p)
		}
		return 0
	}
	ms := func(v any) (int64, bool) {
		if !truthy(v) {
			return 0, false
		}
		return isoMS(str(v)), true
	}

	parts := map[string][][2]any{}
	for _, r := range maps(bridge, "SELECT call_id, jid, outcome FROM call_participants") {
		parts[str(r["call_id"])] = append(parts[str(r["call_id"])], [2]any{r["jid"], r["outcome"]})
	}
	for _, r := range maps(bridge, "SELECT "+selectAll(bridge, "calls", "")+" FROM calls ORDER BY timestamp") {
		outgoing, isGroup := truthy(r["is_from_me"]), truthy(r["is_group"])
		var conv, peer int64
		if isGroup {
			conv, _ = a.FindConversation("whatsapp", str(r["chat_jid"]))
		} else {
			peer = addr(str(r["chat_jid"]))
		}
		ts, _ := ms(r["timestamp"])
		id := str(r["id"])
		if str(r["source"]) == "log" {
			outcome := str(r["outcome"])
			detail := bridgeOutcomes[outcome]
			if outcome == "MISSED" && outgoing {
				detail = "unanswered"
			}
			code := ""
			if detail != "" {
				code = "whatsmeow:" + outcome
			}
			callID := calls.add(logSrc, []string{id}, archive.Call{Service: "whatsapp", AddressID: peer, TS: ts,
				Outgoing: outgoing, Answered: outcome == "CONNECTED", Duration: toInt(r["duration"]), Detail: detail,
				DetailCode: code, Video: truthy(r["video"]), ConversationID: conv})
			if callID != 0 && isGroup {
				for _, p := range parts[id] {
					said := str(p[1])
					member := bridgeOutcomes[said]
					if said == "CONNECTED" {
						member = "joined"
					}
					var c any
					if said != "" {
						c = "whatsmeow:" + said
					}
					a.Exec("INSERT OR IGNORE INTO call_member VALUES (?, ?, ?, ?)", callID, archive.NullID(addr(str(p[0]))),
						archive.NullStr(member), c)
				}
			}
		} else {
			// still ringing, or under way: the next import has it; its end never seen (the bridge was
			// down then) and an hour gone: taken as it stood, with no end
			unended := r["ended_at"] == nil
			if unended && (time.Since(time.UnixMilli(ts)) < time.Hour || truthy(r["accepted_at"])) {
				continue // (one answered may go on for hours: its end, when seen, says how long)
			}
			accepted, wasAccepted := ms(r["accepted_at"])
			wasAccepted = wasAccepted && accepted != 0
			ended, _ := ms(r["ended_at"])
			reason := str(r["end_reason"])
			if unended {
				ended, reason = accepted, "unended"
			}
			detail := ""
			if !wasAccepted {
				switch {
				case reason == "reject":
					detail = "rejected"
				case outgoing:
					detail = "unanswered"
				default:
					detail = "missed"
				}
			}
			code := ""
			if detail != "" && reason != "" {
				code = "whatsmeow:" + reason
			}
			var duration int64
			if wasAccepted {
				duration = max(0, floorDiv(ended-accepted, 1000))
			}
			if peer == 0 && !isGroup {
				peer = addr(str(r["creator"]))
			}
			calls.add(eventSrc, []string{id}, archive.Call{Service: "whatsapp", AddressID: peer, TS: ts, Outgoing: outgoing,
				Answered: wasAccepted, Duration: duration, Key: id, Detail: detail, DetailCode: code,
				Video: truthy(r["video"]), ConversationID: conv})
		}
	}
	a.Imported(logSrc)
	a.Imported(eventSrc)
}

func viberCalls(a *archive.Archive, calls *CallSet) {
	if !exists(ViberIphoneDB()) {
		return
	}
	d := ro(ViberIphoneDB())
	defer d.Close()
	src := a.Source(archive.Iphone()+"/viber-calls", ViberIphoneDB(), archive.Iphone(), "")
	for _, r := range maps(d, "SELECT r.Z_PK, r.ZDATE, r.ZDURATION, r.ZCALLTYPE, r.ZCALLTOKEN, l.ZPHONENUMBER "+
		"FROM ZRECENT r LEFT JOIN ZRECENTSLINE l ON l.Z_PK = r.ZRECENTSLINE") {
		kind := str(r["ZCALLTYPE"])
		outgoing := strings.HasPrefix(kind, "outgoing")
		var aid int64
		if truthy(r["ZPHONENUMBER"]) {
			aid = a.Address(address(str(r["ZPHONENUMBER"])))
		}
		key := ""
		if truthy(r["ZCALLTOKEN"]) {
			key = pyStr(r["ZCALLTOKEN"])
		}
		detail, code := "", ""
		switch {
		case kind == "missed":
			detail, code = "missed", "viber:missed"
		case outgoing && !truthy(r["ZDURATION"]):
			detail = "unanswered"
		}
		calls.add(src, []string{pyStr(r["Z_PK"])}, archive.Call{Service: "viber", AddressID: aid, TS: appleMS(toFloat(r["ZDATE"])),
			Outgoing: outgoing, Answered: toFloat(r["ZDURATION"]) > 0, Duration: toInt(r["ZDURATION"]), Key: key,
			Detail: detail, DetailCode: code, Video: strings.Contains(kind, "video")}, func(id int64) {
			// the duration as the source has it: a fraction of a second stays (Call takes whole seconds)
			if f, ok := r["ZDURATION"].(float64); ok && f != float64(int64(f)) {
				a.Exec("UPDATE call SET duration = ? WHERE id = ?", f, id)
			}
		})
	}
}

// carrierAlerts: the calls a carrier's notices tell of.
func carrierAlerts(a *archive.Archive, calls *CallSet, carrier Carrier) {
	src := a.Source(carrier.Source, "the archive's SMS", "", "")
	type sms struct {
		id, ts int64
		text   string
	}
	var rows []sms
	a.Each("SELECT id, ts, text FROM message WHERE service_id = ? AND NOT outgoing AND text IS NOT NULL",
		[]any{a.Service.ID("sms")}, func(scan func(...any)) {
			var s sms
			scan(&s.id, &s.ts, &s.text)
			rows = append(rows, s)
		})
	for _, m := range rows {
		sent := fromTimestamp(m.ts, config.Timezone) // the owner's time zone ([owner] timezone)
		for i, al := range carrier.Alerts(m.text, sent) {
			aid, ts := a.Address(address(al.Number)), tsMS(al.When)
			rowKey := fmt.Sprintf("%d/%d", m.id, i)
			// the carrier sometimes sends the same notice twice: the same number at the same minute
			// is one call, whichever message told of it
			if same, ok := a.IntOK("SELECT c.id FROM call c JOIN call_origin o ON o.call_id = c.id WHERE o.source_id = ? "+
				"AND c.address_id = ? AND c.ts = ?", src, aid, ts); ok {
				a.Exec("INSERT OR IGNORE INTO call_origin VALUES (?, ?, ?)", src, rowKey, same)
				continue
			}
			detail := "missed"
			if al.Busy {
				detail = "busy"
			}
			calls.add(src, []string{rowKey}, archive.Call{Service: "phone", AddressID: aid, TS: ts, Detail: detail,
				DetailCode: "carrier:" + carrier.Name, Attempts: al.Attempts})
		}
	}
}

// fromTimestamp is Python's datetime.fromtimestamp(ms / 1000, zone).
func fromTimestamp(ms int64, loc *time.Location) time.Time {
	f := float64(ms) / 1000
	sec := int64(f)
	if float64(sec) > f {
		sec--
	}
	micro := roundEven((f - float64(sec)) * 1e6)
	return time.Unix(sec, micro*1000).In(loc)
}

// VoIPOptions: NoBridge leaves the WhatsApp bridge's calls out (the plugins read it apart);
// Carriers nil is config [import] carrier_notices.
type VoIPOptions struct {
	NoBridge bool
	Carriers []string
}

func VoIP(a *archive.Archive, out func(string), opt VoIPOptions) (err error) {
	defer archive.Recover(&err)
	names := opt.Carriers
	if names == nil {
		names = config.Strings("import", "carrier_notices")
	}
	enabled, err := EnabledCarriers(names)
	if err != nil {
		return err
	}
	calls := NewCallSet(a)
	whatsappCalls(a, calls)
	if !opt.NoBridge && BridgeDB() != "" {
		bridgeCalls(a, calls, BridgeDB(), BridgeStore())
	}
	viberCalls(a, calls)
	for _, c := range enabled {
		carrierAlerts(a, calls, c)
	}
	a.PurgeSpam()
	a.Commit()
	var services []string
	for s := range calls.Added {
		services = append(services, s)
	}
	sort.Strings(services)
	for _, s := range services {
		say(out, "new:   {n} {service}", map[string]any{"n": fmt.Sprintf("%6d", calls.Added[s]), "service": s})
	}
	if len(calls.Added) == 0 {
		say(out, "new:   none", nil)
	}
	return nil
}
