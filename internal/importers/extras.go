// Ports everysaid/extras.py: what a message carries beyond its text: the message it answers,
// reactions, edits and deletions, forwarding, a star, a place, a shared contact or poll, and its
// kind as the service names it.
//
// Each function reads one source's row (as the importers see it) and gives an archive.Extras for
// AddMessage; fields are left empty when there is nothing to say:
//
//   - ReplyKey, ReplyText: the service key of the message answered, or reacted to by a tapback
//     (resolved to message.reply_to by Archive.Resolve), and the quoted text, kept only where that
//     message is not in the archive;
//   - Subtype: the service's own kind where ours is coarser (link, gif, video note, poll, pin...;
//     the archive's vocabulary), and SubtypeCode, the source's own code for it ('whatsapp:54',
//     'viber:url');
//   - Edited, Deleted, Forwarded, Starred;
//   - Lat, Lon, Place: a location sent; on any other message (older Viber), where the sender was
//     when sending (AddMessage keeps that as sender_lat, sender_lon);
//   - Text: the content of a shared contact or a poll, where the message has no text of its own;
//   - Reactions: the emoji where known, the service's code ('viber:6'); the sender as a Handle,
//     "peer" for the other person of a one-to-one chat, a jid the importer maps, or nil;
//     Outgoing for the owner's own reaction;
//   - EditsKey: for an edit event (Viber on the iPhone), the key of the message it edited;
//   - ReactsTo: for a reaction sent as a message of its own (iMessage tapback).
//
// Found by reading every field of every source (October 2026); left out on purpose: read and
// delivery times, the names apps show, link previews (the link is in the text), the sender's time
// zone, language guesses, and iMessage's reply_to_guid, which iOS sets on ordinary messages too
// (the previous one in the chat), so it is not a reply (its thread_originator_guid is).
package importers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"everysaid/internal/archive"
)

// Viber's reaction codes, as its apps show them; 6 and later ones are kept as codes only.
var viberReactions = map[int64]string{1: "❤️", 2: "😂", 3: "😮", 4: "😢", 5: "😡"}

// iMessage tapbacks (associated_message_type); 2006 carries its own emoji.
var tapbacks = map[int64]string{2000: "❤️", 2001: "👍", 2002: "👎", 2003: "😂", 2004: "‼️", 2005: "❓"}

var viberDesktopSubtypes = map[int64]string{9: "link", 15: "pin"}

var viberMedia = map[int64]bool{2: true, 3: true, 11: true} // picture, video, file: a Subject on these is their caption

var viberIphoneSubtypes = map[string]string{"url": "link", "systemGeneralMessageRemoved": "deleted",
	"systemPinnedMessageCreated": "pin", "systemCallLog": "call",
	"systemInvalidMessage": "invalid", "customLocation": "location"}

// ZWAMESSAGE.ZMESSAGETYPE (see whatsapp.go); only those our kind does not already say. 10:
// security code, number changed...; 59: a call.
var whatsappSubtypes = map[int64]string{7: "link", 11: "gif", 14: "deleted", 54: "video note", 6: "group event",
	10: "notice", 66: "poll", 59: "call"}

// orderKey holds, in a decoded JSON object, the order of its keys (no key of JSON has a NUL).
const orderKey = "\x00order"

// decodeJSON is a JSON text decoded with numbers as json.Number and each object's key order kept.
func decodeJSON(data []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := decodeValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err == nil {
		return nil, fmt.Errorf("extra data")
	}
	return v, nil
}

func decodeValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch x := t.(type) {
	case json.Delim:
		if x == '[' {
			out := []any{}
			for d.More() {
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			_, err := d.Token()
			return out, err
		}
		out := map[string]any{}
		var order []string
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeValue(d)
			if err != nil {
				return nil, err
			}
			key := k.(string)
			if _, seen := out[key]; !seen {
				order = append(order, key)
			}
			out[key] = v
		}
		out[orderKey] = order
		_, err := d.Token()
		return out, err
	}
	return t, nil
}

// keys are an object's keys in their order.
func keys(m map[string]any) []string {
	if o, ok := m[orderKey].([]string); ok {
		return o
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// jsonObj is Python's _json(value): an object as it is, a JSON text decoded, else empty.
func jsonObj(value any) map[string]any {
	switch x := value.(type) {
	case map[string]any:
		return x
	case nil:
		return map[string]any{}
	}
	s := str(value)
	if s == "" {
		return map[string]any{}
	}
	v, err := decodeJSON([]byte(s))
	if m, ok := v.(map[string]any); ok && err == nil {
		return m
	}
	return map[string]any{}
}

// obj is a value as an object (empty for anything else: Python's `x or {}` on a dict).
func obj(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func list(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

// reaction is (emoji or "", code or "") of a Viber reaction: a number, or an emoji itself.
func reaction(code any) (string, string) {
	n, ok := pyInt(code)
	if !ok {
		return pyStr(code), ""
	}
	return viberReactions[n], fmt.Sprintf("viber:%d", n)
}

// strOf is Python's _str: bytes decoded, anything else as it is (as text).
func strOf(v any) string {
	switch x := v.(type) {
	case []byte:
		return decodeReplace(x)
	case nil:
		return ""
	}
	return pyStr(v)
}

func contactText(name, number string) string {
	var parts []string
	for _, p := range []string{strings.Trim(name, "\u2068\u2069 "), number} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "📇 " + strings.Join(parts, ", ")
}

func fp(f float64) *float64 { return &f }

// viberDesktopExtras reads a row of the Viber Desktop export (Events joined with Messages).
func viberDesktopExtras(raw row) *archive.Extras {
	out, info := &archive.Extras{}, jsonObj(raw["Info"])
	mt, mtOK := raw["MessageType"].(int64)
	if s, ok := viberDesktopSubtypes[mt]; ok && mtOK {
		out.Subtype, out.SubtypeCode = s, fmt.Sprintf("viber:%d", mt)
	}
	if truthy(info["ivmInfo"]) {
		out.Subtype, out.SubtypeCode = "video note", "viber:ivmInfo"
	}
	if s, ok := info["ClientInnerMessageType"].(string); ok && s == "EXPRESSION_PANEL_GIF" {
		out.Subtype, out.SubtypeCode = "gif", "viber:EXPRESSION_PANEL_GIF"
	}
	if mtOK && viberMedia[mt] && truthy(raw["Subject"]) && !truthy(raw["Body"]) {
		out.Text = str(raw["Subject"])
	}
	if quote, ok := info["quote"].(map[string]any); ok && truthy(quote["token"]) {
		out.ReplyKey = pyStr(quote["token"])
		if truthy(quote["text"]) {
			out.ReplyText = pyStr(quote["text"])
		}
	}
	if _, ok := info["edit"]; ok {
		out.Edited = true
	}
	if truthy(info["generalFwdInfo"]) {
		out.Forwarded = true
	}
	var reactions []archive.Reaction
	for _, r := range list(info["messageReactions"]) {
		rm := obj(r)
		if truthy(rm["count"]) {
			e, c := reaction(rm["type"])
			reactions = append(reactions, archive.Reaction{Emoji: e, Code: c, Count: int(toInt(rm["count"]))})
		}
	}
	meta := obj(info["reaction_meta_info"])
	if len(reactions) == 0 { // one-to-one chats: who reacted is known, the emoji is not kept
		if truthy(meta["talker_current_reaction_token"]) {
			reactions = append(reactions, archive.Reaction{Code: "viber:?", Count: 1, Who: "peer"})
		}
		if truthy(meta["my_current_reaction_token"]) {
			reactions = append(reactions, archive.Reaction{Code: "viber:?", Count: 1, Outgoing: true})
		}
	}
	out.Reactions = reactions
	if truthy(raw["ContactLatitude"]) || truthy(raw["ContactLongitude"]) {
		// a location sent, or on any other message where the sender was (older Viber)
		out.Lat, out.Lon = fp(toFloat(raw["ContactLatitude"])/1e7), fp(toFloat(raw["ContactLongitude"])/1e7)
		if a := obj(info["locationInfo"])["address"]; truthy(a) {
			out.Place = pyStr(a)
		}
	}
	if name := obj(info["fileInfo"])["FileName"]; truthy(name) && !truthy(raw["Body"]) && out.Text == "" {
		out.Text = "📎 " + pyStr(name) // a file without words: its name, as Viber shows it
	}
	if mtOK && mt == 10 && !truthy(raw["Body"]) {
		number := info["PhoneNumber"]
		if !truthy(number) {
			number = info["ViberNumber"]
		}
		if t := contactText(strOrEmpty(info["Name"]), strOrEmpty(number)); t != "" {
			out.Text = t
		}
	}
	return out
}

func strOrEmpty(v any) string {
	if !truthy(v) {
		return ""
	}
	return pyStr(v)
}

// location is a place of the iPhone's ZVIBERLOCATION.
type location struct {
	lat, lon any
	place    any
}

// viberIphoneExtras reads a ZVIBERMESSAGE row of the iPhone's viber.sqlite; locations: ZLOCATION ->
// its place.
func viberIphoneExtras(raw row, locations map[int64]location) *archive.Extras {
	out, md, cm := &archive.Extras{}, jsonObj(raw["ZMETADATA"]), jsonObj(raw["ZCLIENTMETADATA"])
	system, _ := raw["ZSYSTEMTYPE"].(string)
	if s, ok := viberIphoneSubtypes[system]; ok {
		out.Subtype, out.SubtypeCode = s, "viber:"+system
	}
	if s, ok := md["ClientInnerMessageType"].(string); ok && s == "EXPRESSION_PANEL_GIF" {
		out.Subtype, out.SubtypeCode = "gif", "viber:EXPRESSION_PANEL_GIF"
	}
	if system == "systemCallLog" && truthy(raw["ZCALLTYPE"]) { // incoming, missed, outgoing_viber, ..._with_video
		count := raw["ZCALLSCOUNT"]
		if !truthy(count) {
			count = int64(1)
		}
		out.SubtypeCode = "viber:" + pyStr(raw["ZCALLTYPE"])
		if toFloat(count) > 1 {
			out.SubtypeCode += " ×" + pyStr(count)
		}
	}
	if system == "systemGeneralMessageRemoved" {
		out.Deleted = true
	}
	if quote, ok := md["quote"].(map[string]any); ok && truthy(quote["token"]) {
		out.ReplyKey = pyStr(quote["token"])
		if truthy(quote["text"]) {
			out.ReplyText = pyStr(quote["text"])
		}
	}
	if edit, ok := md["edit"].(map[string]any); ok && truthy(edit["token"]) {
		out.EditsKey = pyStr(edit["token"]) // this row is the edit event; the token is the message
	}
	if truthy(cm["EditDate"]) || truthy(cm["EditToken"]) {
		out.Edited = true
	}
	if truthy(md["generalFwdInfo"]) || truthy(raw["ZFORWARDTYPE"]) {
		out.Forwarded = true
	}
	var reactions []archive.Reaction
	rs := obj(obj(cm["Reactions"])["reactions"])
	for _, code := range keys(rs) {
		if count := rs[code]; truthy(count) {
			e, c := reaction(code)
			reactions = append(reactions, archive.Reaction{Emoji: e, Code: c, Count: int(toInt(count))})
		}
	}
	one := obj(cm["Reactions1on1OnMessageMetadata"])
	if truthy(one["interlocutorReaction"]) || truthy(one["stableReaction"]) {
		// one-to-one: whose reaction it is (the other person's, or the owner's), instead of a bare count
		reactions = nil
		if t := obj(one["interlocutorReaction"])["type"]; truthy(t) {
			e, c := reaction(t)
			reactions = append(reactions, archive.Reaction{Emoji: e, Code: c, Count: 1, Who: "peer"})
		}
		if t := obj(one["stableReaction"])["type"]; truthy(t) {
			e, c := reaction(t)
			reactions = append(reactions, archive.Reaction{Emoji: e, Code: c, Count: 1, Outgoing: true})
		}
	}
	if len(reactions) == 0 && truthy(raw["ZLIKESCOUNT"]) {
		t := raw["ZLIKESTYPE"]
		if !truthy(t) {
			t = int64(1)
		}
		e, c := reaction(t)
		reactions = append(reactions, archive.Reaction{Emoji: e, Code: c, Count: int(toInt(raw["ZLIKESCOUNT"]))})
	}
	out.Reactions = reactions
	if loc, ok := raw["ZLOCATION"].(int64); ok && loc != 0 && locations != nil {
		if l, ok := locations[loc]; ok {
			out.Lat, out.Lon = floatPtr(l.lat), floatPtr(l.lon)
			if truthy(l.place) {
				out.Place = pyStr(l.place)
			}
		}
	}
	if name := obj(md["fileInfo"])["FileName"]; truthy(name) && !truthy(raw["ZTEXT"]) {
		out.Text = "📎 " + pyStr(name) // a file without words: its name, as Viber shows it
	}
	if poll, ok := cm["Poll"].([]any); ok && len(poll) > 0 {
		var options []string
		for _, o := range poll {
			om := obj(o)
			title, ok := om["title"]
			if !ok {
				title = ""
			}
			count, ok := om["count"]
			if !ok {
				count = int64(0)
			}
			options = append(options, fmt.Sprintf("• %s (%s)", pyStr(title), pyStr(count)))
		}
		out.Subtype, out.SubtypeCode = "poll", "viber:Poll"
		out.Text = pyStrip(str(raw["ZTEXT"]) + "\n" + strings.Join(options, "\n"))
	}
	return out
}

// floatPtr is a number as a column's value (nil for none).
func floatPtr(v any) *float64 {
	if _, f, _, ok := num(v); ok {
		return &f
	}
	return nil
}

var androidCallDetail = map[string]string{"3": "missed", "5": "rejected", "6": "blocked"}

const facetimeVideo = 8 // ZCALLRECORD.ZCALLTYPE: 8 video, 16 audio

// callExtras is what a call record says beyond answered and duration: (detail: missed, rejected
// or blocked; its code; video).
func callExtras(raw row) (string, string, bool) {
	kind := pyStr(raw["type"])
	detail := androidCallDetail[kind]
	code := ""
	if detail != "" {
		code = "android:" + kind
	}
	t, isInt := raw["ZCALLTYPE"].(int64)
	return detail, code, isInt && t == facetimeVideo
}

// imessageExtras reads a message row of the iPhone's sms.db.
func imessageExtras(raw row) *archive.Extras {
	out := &archive.Extras{}
	kind := raw["associated_message_type"]
	if truthy(kind) && truthy(raw["associated_message_guid"]) {
		g := str(raw["associated_message_guid"])
		guid := g
		if i := strings.Index(g, "/"); i >= 0 {
			guid = g[i+1:]
		}
		k, _ := pyInt(kind)
		emoji := str(raw["associated_message_emoji"])
		if emoji == "" {
			emoji = tapbacks[k]
		}
		if emoji != "" && k < 3000 { // 3000s take a tapback back
			code := fmt.Sprintf("imessage:%s", pyStr(kind))
			out.ReactsTo = &archive.ReactsTo{Key: guid, Emoji: emoji, Code: code}
			out.ReplyKey = guid // kept even where it cannot be resolved (SMS have no key)
			out.Subtype, out.SubtypeCode = "tapback", code
		}
	}
	// an inline reply (iOS 14 on) names the message its thread began with; edits and unsending
	// (iOS 16 on) leave their times (older databases have none of these columns)
	if g := str(raw["thread_originator_guid"]); g != "" && out.ReplyKey == "" {
		out.ReplyKey = g
	}
	if truthy(raw["date_retracted"]) {
		out.Deleted = true
	} else if truthy(raw["date_edited"]) {
		out.Edited = true
	}
	return out
}

// whatsappExtras reads a ZWAMESSAGE row with what the iPhone keeps beside it: its media item's
// protobuf metadata, its message info's receipt protobuf, and the media item (lat, lon, vcard).
// Reactions' senders are jids (strings) or nil: the importer maps them to people.
func whatsappExtras(r row, meta, receipt []byte, media row) *archive.Extras {
	out := &archive.Extras{}
	mtype, isInt := r["ZMESSAGETYPE"].(int64)
	if s, ok := whatsappSubtypes[mtype]; ok && isInt {
		out.Subtype, out.SubtypeCode = s, fmt.Sprintf("whatsapp:%d", mtype)
	}
	if isInt && mtype == 14 {
		out.Deleted = true
	}
	if truthy(r["ZSTARRED"]) {
		out.Starred = true
	}
	var fields map[uint64][]any
	if len(meta) > 0 {
		fields = protobufFields(meta)
	}
	if f5 := fields[5]; len(f5) > 0 {
		out.ReplyKey = strOf(f5[0])
		var quoted map[uint64][]any
		if f19 := fields[19]; len(f19) > 0 {
			if b, ok := f19[0].([]byte); ok {
				quoted = protobufFields(b)
			}
		}
		if q := quoted[1]; len(q) > 0 {
			if b, ok := q[0].([]byte); ok {
				out.ReplyText = decodeReplace(b)
			}
		}
	}
	if f46 := fields[46]; len(f46) > 0 {
		if n, ok := f46[0].(uint64); ok && n > 0 {
			out.Forwarded = true
		}
	}
	if len(receipt) > 0 {
		var reactions []archive.Reaction
		for _, block := range protobufFields(receipt)[7] {
			for _, item := range protobufFields(asBytes(block))[1] {
				rf := protobufFields(asBytes(item))
				emoji := ""
				if v := rf[3]; len(v) > 0 {
					emoji = strOf(v[0])
				}
				if emoji != "" {
					var jid any // nil where the receipt names no one: the owner's own
					if v := rf[2]; len(v) > 0 {
						jid = strOf(v[0])
					}
					reactions = append(reactions, archive.Reaction{Emoji: emoji, Count: 1, Who: jid, Outgoing: jid == nil})
				}
			}
		}
		out.Reactions = reactions // senders as jids: the importer maps them to people
	}
	if media != nil {
		if isInt && mtype == 5 && (truthy(media["ZLATITUDE"]) || truthy(media["ZLONGITUDE"])) {
			out.Lat, out.Lon = floatPtr(media["ZLATITUDE"]), floatPtr(media["ZLONGITUDE"])
			if truthy(media["ZTITLE"]) {
				out.Place = pyStr(media["ZTITLE"])
			}
		}
		if isInt && mtype == 4 && !truthy(r["ZTEXT"]) {
			number := ""
			for _, line := range splitLines(str(media["ZVCARDSTRING"])) {
				if strings.HasPrefix(upper(line), "TEL") && strings.Contains(line, ":") {
					number = pyStrip(line[strings.Index(line, ":")+1:])
					break
				}
			}
			if t := contactText(str(media["ZVCARDNAME"]), number); t != "" {
				out.Text = t
			}
		}
	}
	return out
}

func asBytes(v any) []byte {
	b, _ := v.([]byte)
	return b
}

// The bridge's subtypes (whatsapp-bridge's messages.subtype) our vocabulary names.
var bridgeSubtypes = map[string]string{"gif": "gif", "video_note": "video note", "link": "link"}

// bridgeReaction is a row of the bridge's reactions table.
type bridgeReaction struct {
	jid   any // a string, or nil
	mine  bool
	emoji string
}

// whatsappBridgeExtras reads a row of the WhatsApp bridge's messages table, and the reactions to it
// in its reactions table. A bridge from before these columns has only text and media.
func whatsappBridgeExtras(r row, reactions []bridgeReaction) *archive.Extras {
	out := &archive.Extras{}
	sub, kind := str(r["subtype"]), str(r["kind"])
	if sub != "" {
		if s, ok := bridgeSubtypes[sub]; ok {
			out.Subtype = s
		}
		out.SubtypeCode = "whatsmeow:" + sub
	}
	if kind == "poll" {
		out.Subtype, out.SubtypeCode = "poll", "whatsmeow:poll"
	}
	if truthy(r["reply_to"]) {
		out.ReplyKey = pyStr(r["reply_to"])
		if truthy(r["reply_text"]) {
			out.ReplyText = pyStr(r["reply_text"])
		}
	}
	out.Forwarded, out.Edited, out.Deleted = truthy(r["forwarded"]), truthy(r["edited"]), truthy(r["deleted"])
	out.ForwardFrom, out.Album = strOrEmpty(r["forward_from"]), strOrEmpty(r["album"])
	if r["lat"] != nil && r["lon"] != nil {
		out.Lat, out.Lon = floatPtr(r["lat"]), floatPtr(r["lon"])
		if truthy(r["place"]) {
			out.Place = pyStr(r["place"])
		}
	}
	if kind == "contact" && truthy(r["content"]) {
		var lines []string
		for _, l := range splitLines(str(r["content"])) {
			lines = append(lines, "📇 "+l)
		}
		out.Text = strings.Join(lines, "\n")
	}
	for _, x := range reactions {
		if x.emoji == "" {
			continue
		}
		if x.mine {
			out.Reactions = append(out.Reactions, archive.Reaction{Emoji: x.emoji, Count: 1, Outgoing: true})
		} else {
			out.Reactions = append(out.Reactions, archive.Reaction{Emoji: x.emoji, Count: 1, Who: x.jid})
		}
	}
	return out // senders as jids: the importer maps them to people
}
