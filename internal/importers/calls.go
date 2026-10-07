// Ports everysaid/calls.py: calls from the iPhone's CallHistory.storedata and an Android phone's
// call log (in its export). Either may be missing.
//
// The iPhone's calls may be copies of an Android phone's (same number, direction and second). As
// with SMS, a pair becomes one call with a single origin: the device in use at the time, else the
// newer one. The iPhone's log also has the calls of apps that use CallKit; each goes to its own
// service.
package importers

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"everysaid/internal/archive"
	"everysaid/internal/text"
)

// CallsIphoneDB is where the iPhone's call history is.
func CallsIphoneDB() string { return filepath.Join(archive.IphoneData(), "CallHistory.storedata") }

// ZSERVICE_PROVIDER: Apple's own, or the bundle id of an app that uses CallKit (with or without its
// team id in front, e.g. 'UKFA9XBX6K.net.whatsapp.WhatsApp'). An app not known here keeps its
// bundle id. In order: a variant of a known app is found by the first that fits.
var callServices = [][2]string{{"com.apple.Telephony", "phone"}, {"com.apple.FaceTime", "facetime"},
	{"net.whatsapp.WhatsApp", "whatsapp"}, {"net.whatsapp.WhatsAppSMB", "whatsapp"},
	{"com.viber", "viber"}, {"ph.telegra.Telegraph", "telegram"}, {"org.telegram.Telegram", "telegram"},
	{"org.whispersystems.signal", "signal"}, {"com.facebook.Messenger", "messenger"},
	{"com.microsoft.skype.teams", "teams"}, {"com.skype.skype", "skype"}, {"com.skype.SkypeForiPhone", "skype"},
	{"us.zoom.videomeetings", "zoom"}, {"com.google.Duo", "meet"}, {"com.google.meetings", "meet"},
	{"com.hammerandchisel.discord", "discord"}, {"com.tinyspeck.chatlyio", "slack"},
	{"jp.naver.line", "line"}, {"com.tencent.xin", "wechat"}}

const androidOutgoing = "2"

var androidAnswered = map[string]bool{"1": true, "7": true} // incoming, answered on another device

type callRec struct {
	source   string
	rowKey   string
	raw      row
	service  string
	ts       int64
	outgoing bool
	answered bool
	duration int64
	address  *archive.Handle
}

// serviceOf is the service of a ZSERVICE_PROVIDER: by bundle id, with or without the team id
// before it.
func serviceOf(provider string) string {
	if provider == "" {
		return "phone"
	}
	tail := provider
	if _, after, ok := strings.Cut(provider, "."); ok {
		tail = after
	}
	for _, p := range []string{provider, tail} {
		for _, s := range callServices {
			if s[0] == p {
				return s[1]
			}
		}
	}
	low := text.Lower(provider)
	for _, s := range callServices { // a variant of a known app (e.g. com.viber.voip)
		b := text.Lower(s[0])
		if strings.HasPrefix(low, b+".") || strings.Contains(low, "."+b) {
			return s[1]
		}
	}
	return provider
}

func readIphoneCalls(path string) []*callRec {
	d := ro(path)
	defer d.Close()
	handle := map[any]any{}
	for _, r := range maps(d, "SELECT j.Z_2REMOTEPARTICIPANTCALLS AS c, h.ZVALUE AS v FROM Z_2REMOTEPARTICIPANTHANDLES j "+
		"JOIN ZHANDLE h ON h.Z_PK = j.Z_4REMOTEPARTICIPANTHANDLES ORDER BY h.Z_PK") {
		if _, ok := handle[r["c"]]; !ok {
			handle[r["c"]] = r["v"]
		}
	}
	var recs []*callRec
	for _, r := range maps(d, "SELECT * FROM ZCALLRECORD ORDER BY Z_PK") {
		number := r["ZADDRESS"]
		if !truthy(number) {
			number = handle[r["Z_PK"]]
		}
		duration := roundEven(toFloat(r["ZDURATION"]))
		outgoing := truthy(r["ZORIGINATED"])
		answered := truthy(r["ZANSWERED"])
		if outgoing {
			answered = duration > 0
		}
		var addr *archive.Handle
		if truthy(number) {
			h := address(str(number))
			addr = &h
		}
		recs = append(recs, &callRec{archive.Iphone(), str(r["ZUNIQUE_ID"]), r, serviceOf(str(r["ZSERVICE_PROVIDER"])),
			appleMS(toFloat(r["ZDATE"])), outgoing, answered, duration, addr})
	}
	return recs
}

func readAndroidCalls(path, device string) []*callRec {
	d := ro(path)
	defer d.Close()
	var recs []*callRec
	for _, r := range maps(d, "SELECT * FROM calls ORDER BY CAST(_id AS INTEGER)") {
		var duration int64
		if truthy(r["duration"]) {
			duration, _ = pyInt(r["duration"])
		}
		t, _ := r["type"].(string)
		outgoing := t == androidOutgoing
		answered := androidAnswered[t]
		if outgoing {
			answered = duration > 0
		}
		var addr *archive.Handle
		if truthy(r["number"]) {
			h := address(str(r["number"]))
			addr = &h
		}
		date, _ := pyInt(r["date"])
		recs = append(recs, &callRec{device, pyStr(r["_id"]), r, "phone", date, outgoing, answered, duration, addr})
	}
	return recs
}

func pairCalls(iphone, android []*callRec) (map[*callRec]*callRec, map[*callRec]bool) {
	type k struct {
		address  archive.Handle
		has      bool
		outgoing bool
	}
	keyOf := func(r *callRec) k {
		if r.address == nil {
			return k{outgoing: r.outgoing}
		}
		return k{*r.address, true, r.outgoing}
	}
	index := map[k][]*callRec{}
	for _, h := range android {
		index[keyOf(h)] = append(index[keyOf(h)], h)
	}
	used, pairs := map[*callRec]bool{}, map[*callRec]*callRec{}
	sorted := append([]*callRec(nil), iphone...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].ts < sorted[b].ts })
	for _, i := range sorted {
		if i.service != "phone" {
			continue
		}
		var best *callRec
		for _, h := range index[keyOf(i)] {
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

// CallsOptions: IphoneDB "" is the default; Exports nil is every Android export there is (an
// empty, non-nil list is none).
type CallsOptions struct {
	IphoneDB string
	Exports  []archive.AndroidExport
}

func Calls(a *archive.Archive, out func(string), opt CallsOptions) (err error) {
	defer archive.Recover(&err)
	iphoneDB := opt.IphoneDB
	if iphoneDB == "" {
		iphoneDB = CallsIphoneDB()
	}
	exports := opt.Exports
	if exports == nil {
		exports = archive.AndroidExports("")
	}
	var iphone []*callRec
	if exists(iphoneDB) {
		iphone = readIphoneCalls(iphoneDB)
	}
	if len(iphone) == 0 && len(exports) == 0 {
		say(out, smsNoSourceLine, map[string]any{"db": iphoneDB})
		return nil
	}
	var android []*callRec
	for _, e := range exports {
		android = append(android, readAndroidCalls(e.DB, e.Device)...)
	}
	pairs, used := pairCalls(iphone, android)
	iph := archive.Iphone()

	fromAndroid := func(h *callRec) bool { return a.Keeper([]string{iph, h.source}, h.ts) == h.source }

	var chosen []*callRec
	other := map[*callRec]*callRec{} // the copy chosen -> the other phone's copy
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

	sources := map[string]int64{iph: a.Source(iph+"/calls", iphoneDB, iph, "")}
	order := []string{iph}
	for _, e := range exports {
		if _, ok := sources[e.Device]; !ok {
			order = append(order, e.Device)
		}
		sources[e.Device] = a.Source(e.Device+"/calls", e.DB, e.Device, "")
	}
	type addedKey struct{ source, service string }
	added := map[addedKey]int{}
	sort.SliceStable(chosen, func(x, y int) bool { return chosen[x].ts < chosen[y].ts })
	for _, r := range chosen {
		sid := sources[r.source]
		if a.HasOrigin(sid, r.rowKey, "call_origin") {
			continue
		}
		// the other phone's copy came in on an earlier import: that one stays (a second origin below)
		if o := other[r]; o != nil && a.HasOrigin(sources[o.source], o.rowKey, "call_origin") {
			continue
		}
		detail, code, video := callExtras(r.raw)
		var aid int64
		if r.address != nil {
			aid = a.Address(*r.address)
		}
		a.AddCall(sid, r.rowKey, archive.Call{Service: r.service, AddressID: aid, TS: r.ts, Outgoing: r.outgoing,
			Answered: r.answered, Duration: r.duration, Detail: detail, DetailCode: code, Video: video})
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
	a.RecordPairs(sources, bothWays(pairList), "call_origin")
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
	say(out, "iPhone: {n} calls", map[string]any{"n": len(iphone)})
	say(out, "Android ({devices}): {n} calls", map[string]any{"devices": devs, "n": len(android)})
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
		say(out, "new:   {n} {source} {service}", map[string]any{"n": fmt.Sprintf("%6d", added[k]), "source": k.source, "service": k.service})
	}
	if len(added) == 0 {
		say(out, "new:   none", nil)
	}
	return nil
}
