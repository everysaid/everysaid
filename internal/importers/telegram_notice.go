package importers

import "everysaid/internal/archive"

// telegramNotice is what a Telegram service message or poll says, as a notice (nil: none): the
// group's changes, a pin, a group call, a timer, a payment or a gift; a poll with its results.
// `from` is who did it (the message's sender).
func telegramNotice(a *archive.Archive, m map[string]any, person *tgPeople, own map[archive.Handle]bool, from int64,
	fromOK bool) *archive.Notice {
	who := func(pid int64) map[string]any { return NoticePerson(a, person.of(pid, true), own) }
	var by map[string]any
	if truthy(m["out"]) {
		by = map[string]any{"self": true}
	} else if fromOK && from != 0 {
		by = who(from)
	}
	notice := func(code string, args map[string]any) *archive.Notice {
		if args == nil {
			args = map[string]any{}
		}
		args["by"] = by
		return &archive.Notice{Code: code, Args: args}
	}
	if pyStr(m["_"]) != "MessageService" {
		media := obj(m["media"])
		switch pyStr(media["_"]) {
		case "MessageMediaUnsupported": // made by a newer version of Telegram
			return notice("unsupported", nil)
		case "MessageMediaStory": // a story shared, or one that mentions the owner
			return notice("story", map[string]any{"mention": truthy(media["via_mention"])})
		}
		if pyStr(obj(m["reply_to"])["_"]) == "MessageReplyStoryHeader" {
			return notice("story_reply", nil)
		}
		if pyStr(media["_"]) != "MessageMediaPoll" {
			return nil
		}
		poll, results := obj(media["poll"]), obj(media["results"])
		answers, counts := list(poll["answers"]), list(results["results"])
		// each answer's voters, where Telegram gave them all (not hidden until one votes; `min` only
		// leaves out which the owner chose): in the answers' order, as the store keeps no `option` to
		// match them by; else unknown
		known := len(counts) == len(answers)
		options := []any{}
		for i, ans := range answers {
			o := pollOption(pyStr(textOf(obj(ans)["text"])), 0)
			if known {
				o["votes"] = toInt(obj(counts[i])["voters"])
			} else {
				o["votes"] = nil
			}
			options = append(options, o)
		}
		return &archive.Notice{Code: "poll", Args: map[string]any{"question": pyStr(textOf(poll["question"])),
			"options": options, "multiple": truthy(poll["multiple_choice"]), "voters": toInt(results["total_voters"]),
			"ended": truthy(poll["closed"])}}
	}
	action := obj(m["action"])
	act := func(t string, pid int64) map[string]any { return map[string]any{"type": t, "who": who(pid)} }
	switch pyStr(action["_"]) {
	case "MessageActionChannelCreate":
		return groupNotice(by, map[string]any{"type": "created", "title": pyStr(action["title"])})
	case "MessageActionChatCreate":
		actions := []map[string]any{{"type": "created", "title": pyStr(action["title"])}}
		for _, u := range list(action["users"]) {
			if id := toInt(u); !fromOK || id != from {
				actions = append(actions, act("added", id))
			}
		}
		return groupNotice(by, actions...)
	case "MessageActionChatAddUser":
		var actions []map[string]any
		for _, u := range list(action["users"]) {
			if id := toInt(u); fromOK && id == from { // added themselves: joined
				actions = append(actions, act("joined", id))
			} else {
				actions = append(actions, act("added", id))
			}
		}
		return groupNotice(by, actions...)
	case "MessageActionChatDeleteUser":
		id := toInt(action["user_id"])
		if fromOK && id == from {
			return groupNotice(by, act("left", id))
		}
		return groupNotice(by, act("removed", id))
	case "MessageActionChatJoinedByLink":
		if !fromOK {
			return nil
		}
		return groupNotice(by, act("joined_link", from))
	case "MessageActionChatJoinedByRequest", "MessageActionChatJoinedViaCommunity":
		if !fromOK {
			return nil
		}
		return groupNotice(by, act("joined", from))
	case "MessageActionTopicCreate":
		return groupNotice(by, map[string]any{"type": "topic", "created": true, "title": pyStr(action["title"])})
	case "MessageActionTopicEdit":
		var actions []map[string]any // renamed, closed or opened again (or both)
		if truthy(action["title"]) {
			actions = append(actions, map[string]any{"type": "topic", "title": pyStr(action["title"])})
		}
		if c, ok := action["closed"].(bool); ok {
			actions = append(actions, map[string]any{"type": "topic", "closed": c})
		}
		if len(actions) == 0 {
			return nil // an icon changed, a topic hidden: nothing said
		}
		return groupNotice(by, actions...)
	case "MessageActionChatEditTitle":
		return groupNotice(by, map[string]any{"type": "title", "title": pyStr(action["title"])})
	case "MessageActionChatEditPhoto":
		return groupNotice(by, map[string]any{"type": "avatar"})
	case "MessageActionChatDeletePhoto":
		return groupNotice(by, map[string]any{"type": "avatar", "removed": true})
	case "MessageActionPinMessage": // the message pinned is the one it answers
		return notice("pin", map[string]any{"seconds": nil})
	case "MessageActionGroupCall": // the message is edited when it ends: how long it lasted
		if d, ok := action["duration"]; ok && truthy(d) {
			return notice("group_call", map[string]any{"seconds": toInt(d)})
		}
		return notice("group_call", nil)
	case "MessageActionGroupCallScheduled":
		return notice("group_call", map[string]any{"scheduled": toInt(action["schedule_date"])})
	case "MessageActionInviteToGroupCall":
		var invited []any
		for _, u := range list(action["users"]) {
			invited = append(invited, who(toInt(u)))
		}
		return notice("group_call_invite", map[string]any{"who": invited})
	case "MessageActionSetMessagesTTL":
		return notice("timer", map[string]any{"seconds": toInt(action["period"])})
	case "MessageActionPaymentSent", "MessageActionPaymentSentMe", "MessageActionPaymentRefunded",
		"MessageActionPaidMessagesRefunded":
		return notice("payment", nil)
	case "MessageActionGiftPremium", "MessageActionGiftCode", "MessageActionStarGift", "MessageActionGiftStars",
		"MessageActionGiftTon", "MessageActionStarGiftUnique", "MessageActionPrizeStars":
		return notice("gift", nil)
	case "MessageActionContactSignUp": // someone in the owner's contacts joined Telegram
		return notice("signed_up", nil)
	case "MessageActionScreenshotTaken":
		return notice("screenshot", nil)
	case "MessageActionEmpty", "MessageActionSecureValuesSentMe", "MessageActionWebViewDataSentMe":
		return notice("unsupported", nil)
	}
	return nil
}
