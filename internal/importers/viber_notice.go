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
