package importers

// What the live WhatsApp connection keeps of its chats and people in keys.db, brought into the
// archive from time to time (the plugin's import), and the files it downloaded.

import (
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// Files takes the files the connection downloaded (whatsapp_media's rows with a path, within the
// store folder) into the archive's store, linked to their messages; those whose message the
// archive does not have yet wait. It gives how many it took.
func (w *WhatsAppWriter) Files(folder string) int {
	type file struct{ chat, id, rel, name string }
	var files []file
	db.Each(w.Keys, "SELECT chat_jid, id, path, CASE WHEN media_type = 'document' THEN coalesce(filename, '') ELSE '' END "+
		"FROM whatsapp_media WHERE store = ? AND coalesce(path, '') != '' ORDER BY at", []any{w.Store}, func(scan func(...any)) {
		var f file
		scan(&f.chat, &f.id, &f.rel, &f.name)
		files = append(files, f)
	})
	if len(files) == 0 {
		return 0
	}
	st := NewStore(w.A)
	st.Move = true // the connection's downloads: kept in the archive's store only
	var taken []file
	for _, f := range files {
		path := localPath(folder, f.rel)
		if path == "" {
			continue
		}
		mid, ok := w.A.IntOK("SELECT message_id FROM message_origin WHERE source_id = ? AND row_key = ?", w.Source, f.chat+"/"+f.id)
		if !ok {
			mid = w.message(f.id)
		}
		if mid == 0 {
			continue // its message is still to come
		}
		if !isFile(path) {
			// gone from the folder before the archive had it: to be downloaded again
			db.Exec(w.Keys, "UPDATE whatsapp_media SET path = NULL WHERE store = ? AND chat_jid = ? AND id = ?", w.Store, f.chat, f.id)
			continue
		}
		st.LinkNamed(WhatsAppSource, w.Source, path, f.rel, mid, f.name)
		taken = append(taken, f)
	}
	w.A.Commit()
	st.Release()
	for _, f := range taken { // a preview's picture may still be to come
		db.Exec(w.Keys, "DELETE FROM whatsapp_media WHERE store = ? AND chat_jid = ? AND id = ? AND coalesce(preview_direct_path, '') = ''",
			w.Store, f.chat, f.id)
		db.Exec(w.Keys, "UPDATE whatsapp_media SET path = NULL, media_type = NULL WHERE store = ? AND chat_jid = ? AND id = ?",
			w.Store, f.chat, f.id)
	}
	return len(taken)
}

// WhatsAppState brings into the archive what the live connection of the writer's store folder keeps
// of its chats and people in keys.db (ownJID: its device's jid, whose contacts and chats' settings
// are read; "": all): the names WhatsApp shows, the chats' state (archived, pinned, muted), the
// groups' members, the people blocked; and writes what waited: what names messages the archive has
// now, the messages that could not be read, a day on, the calls whose end was never seen. It gives
// what changed on messages already there.
func WhatsAppState(w *WhatsAppWriter, ownJID string) (updated map[string]int, err error) {
	defer archive.Recover(&err)
	a, keys, store := w.A, w.Keys, w.Store
	person := newWAPeopleOf(nil, keys, nil, store, ownJID)
	person.keys = keys
	w.person = person
	now := time.Now().Unix()
	for _, k := range person.order {
		if !w.own[k.h] {
			a.HandleName(k.h, "whatsapp", person.names[k], k.kind, now)
		}
	}
	report := func(jid, field string, value int64) {
		conv, _ := a.FindConversation("whatsapp", person.conversationKey(jid))
		a.ReportState(w.Source, conv, field, value, now*1000, 0)
	}
	if hasTable(keys, "whatsmeow_chat_settings") {
		for _, r := range maps(keys, "SELECT chat_jid, muted_until, pinned, archived FROM whatsmeow_chat_settings"+
			ownFilter(ownJID), ownArgs(ownJID)...) {
			jid := str(r["chat_jid"])
			report(jid, "archived", int64(archive.B2I(truthy(r["archived"]))))
			report(jid, "pinned", int64(archive.B2I(truthy(r["pinned"]))))
			until := toInt(r["muted_until"])
			if until != -1 {
				until *= 1000
			}
			report(jid, "muted", until)
		}
	}
	// the groups' members as the connection last read them (one who left stays: a member once)
	for _, r := range maps(keys, "SELECT group_jid, jid FROM whatsapp_group_member WHERE store = ?", store) {
		conv, ok := a.FindConversation("whatsapp", str(r["group_jid"]))
		p, pok := person.of(str(r["jid"]))
		if ok && conv != 0 && pok && !w.own[p] {
			a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", conv, a.Address(p))
		}
	}
	// the people the account blocked, as WhatsApp last said (once it said)
	if db.Exists(keys, "SELECT 1 FROM whatsapp_state WHERE store = ? AND key = 'blocklist'", store) {
		var handles []archive.Handle
		for _, jid := range db.Strs(keys, "SELECT jid FROM whatsapp_blocked WHERE store = ? ORDER BY jid", store) {
			if p, ok := person.of(jid); ok && !w.own[p] {
				handles = append(handles, p)
			}
		}
		a.SetBlocked("whatsapp", handles, nil)
	}
	w.Pending(nil)
	w.Unreadable()
	w.settleCalls("")
	for c, n := range ApplyPins(a, "whatsapp") {
		w.Updated[c] += n
	}
	a.Resolve() // as every import: the people blocked now, among them
	w.Done()
	return w.Updated, nil
}
