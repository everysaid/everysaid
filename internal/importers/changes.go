// What happens to a message after it was imported, as its source says it later: an edit (its new
// text), a deletion, its reactions as they are now. As other messengers show them: the new text
// replaces the old (and its search rows), edited marked; a deleted message keeps its text in the
// archive, marked deleted (the interface says so). Nothing else of the message changes, and a
// source that says nothing new changes nothing.
package importers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"everysaid/internal/archive"
	"everysaid/internal/text"
)

// Change is what a source says now of a message already in the archive. Text nil: the text is not
// said (kept); "" is no text.
type Change struct {
	Text            *string
	Edited, Deleted bool
}

// ApplyChange brings a message (by id) to what its source says now; it says what changed
// ("text", "edited", "deleted"). Flags are only ever set, never taken back.
func ApplyChange(a *archive.Archive, messageID int64, c Change) []string {
	var changed []string
	if c.Text != nil && setText(a, messageID, *c.Text) {
		changed = append(changed, "text")
	}
	for _, f := range []struct {
		name string
		on   bool
	}{{"edited", c.Edited}, {"deleted", c.Deleted}} {
		if f.on {
			if n, _ := a.Exec("UPDATE message SET "+f.name+" = 1 WHERE id = ? AND NOT "+f.name, messageID).RowsAffected(); n > 0 {
				changed = append(changed, f.name)
			}
		}
	}
	return changed
}

// Meta is what a source says of a message's origin when forwarded, its album and its pin; ""
// says nothing (kept), Pinned nil says nothing of the pin, else 0 not pinned, -1 for ever, or until
// when (Unix milliseconds).
type Meta struct {
	ForwardFrom, Album string
	Pinned             *int64
}

// ApplyMeta brings a message's origin, album and pin to what its source says now (a message
// imported before they were kept; a pin taken back); it says what changed ("forward_from",
// "album", "pinned").
func ApplyMeta(a *archive.Archive, messageID int64, m Meta) []string {
	var changed []string
	set := func(col string, v any) {
		if n, _ := a.Exec("UPDATE message SET "+col+" = ? WHERE id = ? AND "+col+" IS NOT ?", v, messageID, v).RowsAffected(); n > 0 {
			changed = append(changed, col)
		}
	}
	if m.ForwardFrom != "" {
		set("forward_from", m.ForwardFrom)
	}
	if m.Album != "" {
		set("album", m.Album)
	}
	if m.Pinned != nil {
		set("pinned", archive.NullID(*m.Pinned))
	}
	return changed
}

// ApplyPins: what a service's pin and unpin notices say of the messages they are about, as each
// one's pin now (its last notice's; a pin for a time lasts until then, from the notice's time).
func ApplyPins(a *archive.Archive, service string) map[string]int {
	type pin struct {
		conv      int64
		replyTo   sql.NullInt64
		replyKey  sql.NullString
		code, arg string
		ts        int64
	}
	var pins []pin
	a.Each("SELECT m.conversation_id, m.reply_to, m.reply_key, n.code, n.args, m.ts FROM notice n "+
		"JOIN message m ON m.id = n.message_id WHERE m.service_id = ? AND n.code IN ('pin', 'unpin') "+
		"AND (m.reply_to IS NOT NULL OR m.reply_key IS NOT NULL) ORDER BY m.ts, m.id",
		[]any{a.Service.ID(service)}, func(scan func(...any)) {
			var p pin
			scan(&p.conv, &p.replyTo, &p.replyKey, &p.code, &p.arg, &p.ts)
			pins = append(pins, p)
		})
	until := map[int64]int64{}
	var order []int64
	for _, p := range pins {
		target, ok := p.replyTo.Int64, p.replyTo.Valid
		if !ok {
			target, ok = a.MessageByKey(service, p.replyKey.String, p.conv)
		}
		if !ok || target == 0 {
			continue
		}
		if _, seen := until[target]; !seen {
			order = append(order, target)
		}
		var args struct{ Seconds float64 }
		json.Unmarshal([]byte(p.arg), &args)
		switch {
		case p.code == "unpin":
			until[target] = 0
		case args.Seconds > 0:
			until[target] = p.ts + int64(args.Seconds*1000)
		default:
			until[target] = -1
		}
	}
	out := map[string]int{}
	for _, id := range order {
		v := until[id]
		for _, c := range ApplyMeta(a, id, Meta{Pinned: &v}) {
			out[c]++
		}
	}
	return out
}

// setText replaces a message's text and its row in the search index (taken out by its rowid and put
// in again folded, as Archive.AddMessage does).
func setText(a *archive.Archive, messageID int64, newText string) bool {
	var old sql.NullString
	if !a.Row("SELECT text FROM message WHERE id = ?", []any{messageID}, &old) || old.String == newText {
		return false
	}
	if old.String != "" {
		a.UnindexText(messageID)
	}
	a.Exec("UPDATE message SET text = ? WHERE id = ?", archive.NullStr(newText), messageID)
	if newText != "" {
		a.IndexText(messageID, text.Fold(newText))
	}
	return true
}

// ReplaceReactions makes a message's reactions those given (a source that says all of them: its
// reactions as they are now); it says whether they changed. Who is a Handle or nil.
func ReplaceReactions(a *archive.Archive, messageID int64, want []archive.Reaction) bool {
	row := func(emoji, code any, count int64, who any, outgoing bool) string {
		return fmt.Sprint(emoji, "\x00", code, "\x00", count, "\x00", who, "\x00", outgoing)
	}
	var wantRows []string
	for _, r := range want {
		var who any
		switch w := r.Who.(type) {
		case archive.Handle:
			who = a.Address(w)
		case int64:
			who = w
		}
		wantRows = append(wantRows, row(archive.NullStr(r.Emoji), archive.NullStr(r.Code), int64(max(r.Count, 1)), who, r.Outgoing))
	}
	var have []string
	a.Each("SELECT emoji, code, count, address_id, outgoing FROM reaction WHERE message_id = ?", []any{messageID},
		func(scan func(...any)) {
			var emoji, code sql.NullString
			var count int64
			var who, outgoing sql.NullInt64
			scan(&emoji, &code, &count, &who, &outgoing)
			var e, c, w any
			if emoji.Valid {
				e = emoji.String
			}
			if code.Valid {
				c = code.String
			}
			if who.Valid {
				w = who.Int64
			}
			have = append(have, row(e, c, count, w, outgoing.Valid && outgoing.Int64 != 0))
		})
	sort.Strings(wantRows)
	sort.Strings(have)
	if strings.Join(wantRows, "\n") == strings.Join(have, "\n") {
		return false
	}
	a.Exec("DELETE FROM reaction WHERE message_id = ?", messageID)
	a.AddReactions(messageID, want)
	return true
}
