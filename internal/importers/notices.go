package importers

import "everysaid/internal/archive"

// What the importers share to write notices (the archive's `notice` table; the codes are in
// docs/design.md, "Notices").

// NoticePerson is someone in a notice: the owner, or their address.
func NoticePerson(a *archive.Archive, h archive.Handle, own map[archive.Handle]bool) map[string]any {
	if own[h] {
		return map[string]any{"self": true}
	}
	return map[string]any{"address": a.Address(h)}
}

// groupNotice is a group's change of these actions, made by `by`.
func groupNotice(by map[string]any, actions ...map[string]any) *archive.Notice {
	list := make([]any, 0, len(actions))
	for _, x := range actions {
		list = append(list, x)
	}
	return &archive.Notice{Code: "group", Args: map[string]any{"by": by, "actions": list}}
}

// pollOption is one option of a poll's notice, with how many chose it.
func pollOption(text string, votes int64) map[string]any {
	return map[string]any{"text": text, "votes": votes}
}

// setNoticeIfChanged writes a message's notice where it differs from the one it has.
func setNoticeIfChanged(a *archive.Archive, mid int64, n *archive.Notice) bool {
	if n == nil {
		return false
	}
	js := archive.NoticeJSON(n)
	var now string
	a.Row("SELECT args FROM notice WHERE message_id = ? AND code = ?", []any{mid, n.Code}, &now)
	if now == js {
		return false
	}
	a.SetNotice(mid, n)
	return true
}
