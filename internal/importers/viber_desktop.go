package importers

// What the running Viber Desktop keeps beyond the Android phone's export: reactions as events of
// their own, edits, deletions, and mentions; followed on messages the archive already has, since the
// live source reads the same database again as it changes.

import (
	"database/sql"
	"fmt"
	"sort"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// desktopReactionColumns are the reaction columns of Messages where this Viber has them (an older
// export has none).
func desktopReactionColumns(desktop *sql.DB) string {
	have := map[string]bool{}
	for _, c := range db.Strs(desktop, "SELECT name FROM pragma_table_info('Messages')") {
		have[c] = true
	}
	out := ""
	for _, c := range []string{"MembersReactions", "AdminsReactions", "SelfReaction", "PGIsLiked"} {
		if have[c] {
			out += ", m." + c
		}
	}
	return out
}

// reactionCounts reads {"1": 3, "🙏": 1} (a quick reaction's number, or an emoji, and how many).
func reactionCounts(v any, into map[string]int) bool {
	s := str(v)
	if s == "" {
		return false
	}
	o, err := decodeJSON([]byte(s))
	m, isMap := o.(map[string]any)
	if err != nil || !isMap {
		return false
	}
	for k, n := range m {
		into[k] = max(into[k], int(toInt(n)))
	}
	return true
}

// desktopReactions are a message's reactions as the desktop has them now: each person's last
// reaction event (who, and the user's own), and the rest of the counts (Members and Admins
// Reactions, from before the events this database has) without a who. false: it says nothing of
// them (an older export, whose Info carries them).
func desktopReactions(r row, likes map[string]viberLike, contact map[any]*archive.Handle) ([]archive.Reaction, bool) {
	counts := map[string]int{}
	known := reactionCounts(r["MembersReactions"], counts)
	known = reactionCounts(r["AdminsReactions"], counts) || known
	if len(likes) == 0 { // the user's own, kept on the message itself
		if e := str(r["SelfReaction"]); e != "" {
			likes = map[string]viberLike{"me": {mine: true, emoji: e}}
		} else if q := toInt(r["PGIsLiked"]); q > 0 {
			likes = map[string]viberLike{"me": {mine: true, quick: q}}
		}
	}
	if !known && len(likes) == 0 {
		return nil, false
	}
	var out []archive.Reaction
	whos := make([]string, 0, len(likes))
	for w := range likes {
		whos = append(whos, w)
	}
	sort.Strings(whos)
	for _, w := range whos {
		l := likes[w]
		k := l.emoji
		if k == "" {
			if l.quick <= 0 {
				continue // taken back
			}
			k = fmt.Sprint(l.quick)
		}
		e, c := reaction(k)
		x := archive.Reaction{Emoji: e, Code: c, Count: 1, Outgoing: l.mine}
		if h := contact[l.who]; !l.mine && h != nil {
			x.Who = *h
		}
		out = append(out, x)
		counts[k]--
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if counts[k] > 0 {
			e, c := reaction(k)
			out = append(out, archive.Reaction{Emoji: e, Code: c, Count: counts[k]})
		}
	}
	return out, true
}

// reactionTotals is how many of each reaction, whoever gave them.
func reactionTotals(rs []archive.Reaction) string {
	n := map[string]int{}
	for _, r := range rs {
		n[r.Emoji+"\x00"+r.Code+"\x00"+fmt.Sprint(r.Outgoing)] += max(r.Count, 1)
	}
	keys := make([]string, 0, len(n))
	for k := range n {
		keys = append(keys, fmt.Sprint(k, "=", n[k]))
	}
	sort.Strings(keys)
	return fmt.Sprint(keys)
}

// followDesktop brings messages the archive has to what the desktop says now: an edit's text, a
// deletion, the reactions (where their totals changed: what the archive knows of who gave them
// stays otherwise), and the mentions.
func followDesktop(a *archive.Archive, follows []desktopFollow, person viberPeople) {
	for _, f := range follows {
		mid, ok := a.MessageByKey("viber", f.key, 0)
		if !ok || mid == 0 {
			continue
		}
		c := Change{Edited: f.edited, Deleted: f.deleted}
		if f.edited && f.text != "" {
			text := f.text
			c.Text = &text
		}
		ApplyChange(a, mid, c)
		if f.reactionsKnown {
			var have []archive.Reaction
			a.Each("SELECT emoji, code, count, outgoing FROM reaction WHERE message_id = ?", []any{mid},
				func(scan func(...any)) {
					var e, c sql.NullString
					var n, out sql.NullInt64
					scan(&e, &c, &n, &out)
					have = append(have, archive.Reaction{Emoji: e.String, Code: c.String, Count: int(n.Int64),
						Outgoing: out.Int64 != 0})
				})
			if reactionTotals(have) != reactionTotals(f.reactions) {
				ReplaceReactions(a, mid, f.reactions)
			}
		}
		if f.mentions != nil && !f.deleted {
			viberMentions(a, mid, f.text, f.mentions, person)
		}
	}
}
