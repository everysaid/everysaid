// What happens to a message after it was imported, as its source says it later: an edit (its new
// text), a deletion, its reactions as they are now. As other messengers show them: the new text
// replaces the old (and its search rows), edited marked; a deleted message keeps its text in the
// archive, marked deleted (the interface says so). Nothing else of the message changes, and a
// source that says nothing new changes nothing.
package importers

import (
	"database/sql"
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
