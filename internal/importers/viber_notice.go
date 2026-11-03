package importers

import "everysaid/internal/archive"

// What Viber's system rows and polls say, as notices (docs/design.md, "Notices").

// viberPin is a pin or an unpin of a message (Info/ZMETADATA `pin`: action create or delete, the
// message's token): its notice and the message it is about.
func viberPin(md map[string]any, by map[string]any) (*archive.Notice, string) {
	pin, ok := md["pin"].(map[string]any)
	if !ok {
		return nil, ""
	}
	code := "pin"
	if pyStr(pin["action"]) == "delete" {
		code = "unpin"
	}
	args := map[string]any{"by": by}
	if code == "pin" {
		args["seconds"] = nil
	}
	target := ""
	if truthy(pin["token"]) {
		target = pyStr(pin["token"])
	}
	return &archive.Notice{Code: code, Args: args}, target
}

// viberSetNotice writes a message's notice from a row of Viber's, but never one of a member removed
// that does not say whom (the iPhone's) over the notice the message has (another source may say whom).
func viberSetNotice(a *archive.Archive, mid int64, n *archive.Notice) bool {
	if n != nil && n.Code == "group" && a.Exists("SELECT 1 FROM notice WHERE message_id = ?", mid) {
		for _, x := range list(n.Args["actions"]) {
			if who, ok := obj(x)["who"]; ok && who == nil {
				return false
			}
		}
	}
	return setNoticeIfChanged(a, mid, n)
}

// viberIphoneText is an iPhone row's text: none for a member removed, whose ZTEXT is Viber's own
// word (in the phone's language) for someone it does not know.
func viberIphoneText(r row) string {
	if str(r["ZSYSTEMTYPE"]) == "systemUserRemovedFromGroup" {
		return ""
	}
	return str(r["ZTEXT"])
}

// viberIphoneNotice is what an iPhone row says as a notice (nil: none), and the message it is about.
func viberIphoneNotice(r row, by map[string]any) (*archive.Notice, string) {
	md, cm := jsonObj(r["ZMETADATA"]), jsonObj(r["ZCLIENTMETADATA"])
	switch str(r["ZSYSTEMTYPE"]) {
	case "systemPinnedMessageCreated", "systemPinnedMessageDeleted":
		return viberPin(md, by)
	case "systemIconChanged":
		return groupNotice(by, map[string]any{"type": "avatar"}), ""
	case "systemSelfAdded":
		return groupNotice(by, map[string]any{"type": "added", "who": map[string]any{"self": true}}), ""
	case "systemUserRemovedFromGroup": // whom is not kept: ZTEXT is only Viber's word for "unknown"
		return groupNotice(by, map[string]any{"type": "removed", "who": nil}), ""
	}
	if poll, ok := cm["Poll"].([]any); ok && len(poll) > 0 {
		options := []any{}
		var total int64
		for _, o := range poll {
			om := obj(o)
			options = append(options, pollOption(pyStr(om["title"]), toInt(om["count"])))
			total += toInt(om["count"])
		}
		// how many voted: each one vote where only one may be chosen; not known where several may (the
		// vote rows, pollMessageInvisible, are only some of the votes)
		multiple := truthy(obj(md["poll"])["multiple"])
		var voters any = total
		if multiple {
			voters = nil
		}
		return &archive.Notice{Code: "poll", Args: map[string]any{"question": str(r["ZTEXT"]), "options": options,
			"multiple": multiple, "voters": voters}}, ""
	}
	return nil, ""
}
