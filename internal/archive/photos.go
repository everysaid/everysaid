package archive

import (
	"database/sql"
	"time"
)

// The services' photos of people and groups, with their history (handle_photo, chat_photo): a row
// for each photo a handle or a group had on a service, never overwritten; a new one when it
// changes, its last_seen moved while it stays, removed_at when the service says it has none any
// more (then a row without a photo, until one comes). Sightings may come in any order (a history
// read after the live connection saw the current one): each goes beside the row of its time.

// HandlePhoto records the photo (sha256, already in media; "" for none) a service shows for a
// handle the archive has, the owner's own too, seen at seenAt (Unix seconds, 0 for now). It says
// whether the handle is known.
func (a *Archive) HandlePhoto(h Handle, service, sha string, seenAt int64) bool {
	if !a.Known(h) {
		return false
	}
	a.photo("handle_photo", "address_id", a.Address(h), service, sha, seenAt)
	return true
}

// ChatPhoto records the photo of a group (a conversation of the archive), as HandlePhoto.
func (a *Archive) ChatPhoto(conversationID int64, service, sha string, seenAt int64) {
	if conversationID == 0 {
		return
	}
	a.photo("chat_photo", "conversation_id", conversationID, service, sha, seenAt)
}

type photoRow struct {
	id      int64
	sha     sql.NullString
	last    int64
	removed sql.NullInt64
}

func (a *Archive) photo(table, column string, id int64, service, sha string, t int64) {
	if t == 0 {
		t = time.Now().Unix()
	}
	sid := a.Service.ID(service)
	where := " FROM " + table + " WHERE " + column + " = ? AND service_id = ? "
	var prev, next photoRow
	hasPrev := a.Row("SELECT id, sha256, last_seen, removed_at"+where+"AND first_seen <= ? ORDER BY first_seen DESC, id DESC LIMIT 1",
		[]any{id, sid, t}, &prev.id, &prev.sha, &prev.last, &prev.removed)
	hasNext := a.Row("SELECT id, sha256, last_seen, removed_at"+where+"AND first_seen > ? ORDER BY first_seen, id LIMIT 1",
		[]any{id, sid, t}, &next.id, &next.sha, &next.last, &next.removed)
	same := func(r photoRow) bool { return r.sha.Valid == (sha != "") && r.sha.String == sha }
	if hasPrev && same(prev) && !(prev.removed.Valid && prev.removed.Int64 <= t) {
		a.Exec("UPDATE "+table+" SET last_seen = max(last_seen, ?) WHERE id = ?", t, prev.id)
		return
	}
	if sha == "" && !hasPrev && !hasNext {
		return // never seen with a photo: nothing to say
	}
	// another photo seen within the one before's sightings (a history read late, two sources'
	// times): that one is split around it, its last sighting a row of its own after this one
	if hasPrev && prev.last > t {
		a.Exec("UPDATE "+table+" SET last_seen = first_seen, removed_at = NULL WHERE id = ?", prev.id)
		a.Exec("INSERT INTO "+table+" ("+column+", service_id, sha256, first_seen, last_seen, removed_at) VALUES (?, ?, ?, ?, ?, ?)",
			id, sid, prev.sha, prev.last, prev.last, prev.removed)
		prev.removed = sql.NullInt64{}
		hasNext, next = true, photoRow{sha: prev.sha}
	}
	if hasNext && same(next) && next.id != 0 {
		a.Exec("UPDATE "+table+" SET first_seen = ? WHERE id = ?", t, next.id)
	} else {
		a.Exec("INSERT INTO "+table+" ("+column+", service_id, sha256, first_seen, last_seen) VALUES (?, ?, ?, ?, ?)",
			id, sid, NullStr(sha), t, t)
	}
	// none now: the photo before it was removed then
	if sha == "" && hasPrev && prev.sha.Valid && (!prev.removed.Valid || prev.removed.Int64 > t) {
		a.Exec("UPDATE "+table+" SET removed_at = ? WHERE id = ?", t, prev.id)
	}
}
