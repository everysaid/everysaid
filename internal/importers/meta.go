// Messenger and Instagram (Meta's services) into the archive: what Meta's export ("Download your
// information", MetaExport) and the live source (internal/meta) bring, written the same way by a
// MetaWriter.
//
// A person is their id on the service where it is known (the live source: an FBID, an Instagram
// id), with the names the service shows them by; the export knows people only by their names, so
// there a person is a name within the service, joined to the one id the archive knows by that
// name, if there is exactly one. A conversation is the service's thread: its key the thread's id
// (a one-to-one thread's is the other person's id), or for the export "dyi:" and the thread's
// folder.
//
// The same message from the export and the live source is kept once: a message is the same when
// its time (Meta gives milliseconds), direction and text are (the export has no ids). The export's
// copy then gets no row of its own, only its origin; a live message finding the export's copy gives
// it its key.
package importers

import (
	"strings"

	"everysaid/internal/archive"
)

// MetaUser is a person as Meta names them: their id ("" where only the name is known), the name
// they go by, their user name.
type MetaUser struct {
	ID, Name, Username string
}

// Handle is the person's handle on the service.
func (u MetaUser) Handle(service string) archive.Handle {
	if u.ID != "" {
		return archive.H("id", u.ID, service)
	}
	return archive.H("name", u.Name, service)
}

func (u MetaUser) none() bool { return u.ID == "" && u.Name == "" }

// MetaThread is a chat: its key, whether it is a group, its title (a group's), the others in it.
type MetaThread struct {
	Key     string
	Group   bool
	Title   string
	Members []MetaUser
}

// MetaFile is a file of a message on this machine, with the name it was sent with ("": not known).
// Rel is its path within the source's folder (the key of its link), the base name where "".
type MetaFile struct {
	Path, Name, Rel string
}

// MetaReaction is someone's reaction (Mine: the owner's).
type MetaReaction struct {
	Emoji string
	By    MetaUser
	Mine  bool
}

// MetaCall is a call a message tells of: how long (seconds), whether missed, with video.
type MetaCall struct {
	Duration      int64
	Missed, Video bool
}

// MetaMessage is a message of a thread.
type MetaMessage struct {
	Thread   string
	ID       string // the service's id ("": none, as in the export)
	RowKey   string // its key in the source ("": the thread and the id)
	TS       int64  // Unix ms
	From     MetaUser
	Outgoing bool
	Kind     string // text, image, video, voice, file, sticker, location, call, system
	Text     string
	Subtype  string
	Code     string // the service's own code for the subtype
	ReplyTo  string // the id of the message it answers
	Reply    string // the text it quotes, where that message is not known

	Forwarded, Deleted, Edited bool
	Lat, Lon                   *float64
	Place                      string
	Reactions                  []MetaReaction
	Files                      []MetaFile
	Call                       *MetaCall
	Notice                     *archive.Notice
	Mentions                   []MetaMention
}

// MetaMention is someone a text names, and how it names them.
type MetaMention struct {
	Who   MetaUser
	Token string
}

// MetaWriter writes what a Meta source brings into the archive.
type MetaWriter struct {
	A          *archive.Archive
	Service    string
	SourceName string
	Source     int64
	Store      *Store // the files' store (nil: files are not linked)
	// Names are the names known of people by id, for the handles the archive does not have yet.
	Names map[string]MetaUser

	convs map[string]int64
}

// NewMetaWriter is a writer into the archive's source of this name (made where new).
func NewMetaWriter(a *archive.Archive, service, source, path, mediaRoot string) *MetaWriter {
	return &MetaWriter{A: a, Service: service, SourceName: source, Source: a.Source(source, path, "", mediaRoot),
		Names: map[string]MetaUser{}, convs: map[string]int64{}}
}

// Self records the owner's own handle on the service.
func (w *MetaWriter) Self(u MetaUser) {
	if u.none() {
		return
	}
	w.A.Account(u.Handle(w.Service), w.Service, "")
	w.name(u)
}

// User is the address of a person, made where new: with their name and user name, and an export's
// name joined to the one person the service's ids know by it.
func (w *MetaWriter) User(u MetaUser) int64 {
	if u.ID != "" {
		if k, ok := w.Names[u.ID]; ok {
			if u.Name == "" {
				u.Name = k.Name
			}
			if u.Username == "" {
				u.Username = k.Username
			}
		}
	}
	h := u.Handle(w.Service)
	if u.ID == "" && !w.A.Known(h) {
		if ids := w.A.Ints("SELECT DISTINCT hn.address_id FROM handle_name hn JOIN address a ON a.id = hn.address_id "+
			"JOIN address_kind k ON k.id = a.kind_id WHERE hn.service_id = ? AND hn.name = ? AND hn.current AND k.name = 'id'",
			w.A.Service.ID(w.Service), u.Name); len(ids) == 1 {
			var v string
			w.A.Row("SELECT value FROM address WHERE id = ?", []any{ids[0]}, &v)
			w.A.Alias(h, archive.H("id", v, w.Service))
		}
	}
	aid := w.A.Address(h)
	w.name(u)
	return aid
}

// name records the names a person goes by (an export's name is the one its handle is).
func (w *MetaWriter) name(u MetaUser) {
	h := u.Handle(w.Service)
	if u.Name != "" {
		w.A.HandleName(h, w.Service, u.Name, "profile", 0)
	}
	if u.Username != "" {
		w.A.Alias(archive.H("username", strings.ToLower(u.Username), w.Service), h)
	}
}

// Known notes what the service says of a person (a contact): their name now where the archive has
// them, else for when it does.
func (w *MetaWriter) Known(u MetaUser) {
	if u.ID == "" {
		return
	}
	w.Names[u.ID] = u
	if w.A.Known(u.Handle(w.Service)) {
		w.name(u)
	}
}

// Conv is a thread's conversation, made where new; a group's title and members are brought up to
// what is said (members are only added).
func (w *MetaWriter) Conv(t MetaThread) int64 {
	var members []archive.Handle
	for _, m := range t.Members {
		if !m.none() {
			w.User(m)
			members = append(members, m.Handle(w.Service))
		}
	}
	title := ""
	if t.Group {
		title = t.Title
	}
	cid, existed := w.A.FindConversation(w.Service, t.Key)
	if !existed {
		cid = w.A.Conversation(w.Service, members, t.Key, title)
		if t.Group && len(members) < 2 {
			w.A.Exec("UPDATE conversation SET is_group = 1 WHERE id = ?", cid)
		}
	} else {
		for _, h := range members {
			w.A.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", cid, w.A.Address(h))
		}
		if title != "" {
			w.A.Exec("UPDATE conversation SET title = ? WHERE id = ? AND title IS NOT ?", title, cid, title)
		}
		if t.Group {
			w.A.Exec("UPDATE conversation SET is_group = 1 WHERE id = ? AND NOT is_group", cid)
		}
	}
	w.convs[t.Key] = cid
	return cid
}

// conv is a thread's conversation (0: none yet).
func (w *MetaWriter) conv(key string) int64 {
	if id, ok := w.convs[key]; ok {
		return id
	}
	id, _ := w.A.FindConversation(w.Service, key)
	if id != 0 {
		w.convs[key] = id
	}
	return id
}

// Message is the id of a message of the service by its id, within the thread (0: not there).
func (w *MetaWriter) Message(thread, id string) int64 {
	if id == "" {
		return 0
	}
	mid, _ := w.A.MessageByKey(w.Service, id, w.conv(thread))
	return mid
}

// twin is the message the archive already has that is this one from another source: the same time,
// direction and text (a live message: only one without a key). A message of the export, which has
// no id, may also be one of a log that kept only seconds (Adium's and Pidgin's of Facebook's chat):
// the one such message within two seconds.
func (w *MetaWriter) twin(m MetaMessage) int64 {
	q := "SELECT id FROM message WHERE service_id = ? AND ts = ? AND outgoing = ? AND text IS ?"
	if m.ID != "" {
		q += " AND key IS NULL"
	}
	sid := w.A.Service.ID(w.Service)
	ids := w.A.Ints(q+" ORDER BY id LIMIT 2", sid, m.TS, archive.B2I(m.Outgoing), archive.NullStr(m.Text))
	if len(ids) == 0 && m.ID == "" && m.Text != "" {
		ids = w.A.Ints("SELECT id FROM message WHERE service_id = ? AND ts BETWEEN ? AND ? AND ts % 1000 = 0 "+
			"AND outgoing = ? AND text = ? AND key IS NULL ORDER BY id LIMIT 2", sid, m.TS-2000, m.TS+2000,
			archive.B2I(m.Outgoing), m.Text)
	}
	if len(ids) != 1 {
		return 0
	}
	return ids[0]
}

// Add adds a message (its conversation made where the thread is not known yet, with its sender as
// the other person); it says its id and whether it is new.
func (w *MetaWriter) Add(m MetaMessage) (int64, bool) {
	a := w.A
	rowKey := m.RowKey
	if rowKey == "" {
		rowKey = m.Thread + "/" + m.ID
	}
	var mid int64
	if a.Row("SELECT message_id FROM message_origin WHERE source_id = ? AND row_key = ?", []any{w.Source, rowKey}, &mid) {
		return mid, false
	}
	cid := w.conv(m.Thread)
	if cid == 0 {
		var members []MetaUser
		if !m.Outgoing && !m.From.none() {
			members = append(members, m.From)
		}
		cid = w.Conv(MetaThread{Key: m.Thread, Members: members})
	}
	if m.ID != "" {
		if id, ok := a.MessageByKey(w.Service, m.ID, cid); ok {
			a.Exec("INSERT OR IGNORE INTO message_origin VALUES (?, ?, ?)", w.Source, rowKey, id)
			w.files(id, m.Files)
			return id, false
		}
	}
	if id := w.twin(m); id != 0 {
		if m.ID != "" {
			a.Exec("UPDATE message SET key = ? WHERE id = ? AND key IS NULL", m.ID, id) // the service's keys are its own
		}
		a.Exec("INSERT OR IGNORE INTO message_origin VALUES (?, ?, ?)", w.Source, rowKey, id)
		w.files(id, m.Files)
		return id, false
	}
	var sender int64
	if !m.Outgoing && !m.From.none() {
		sender = w.User(m.From)
		a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", cid, sender)
	}
	x := &archive.Extras{Subtype: m.Subtype, SubtypeCode: m.Code, ReplyKey: m.ReplyTo, ReplyText: m.Reply,
		Forwarded: m.Forwarded, Deleted: m.Deleted, Edited: m.Edited, Lat: m.Lat, Lon: m.Lon, Place: m.Place,
		Notice: m.Notice}
	for _, r := range m.Reactions {
		x.Reactions = append(x.Reactions, w.reaction(r))
	}
	kind := m.Kind
	if kind == "" {
		kind = "text"
	}
	mid = a.AddMessage(w.Source, rowKey, archive.Message{Service: w.Service, ConversationID: cid, TS: m.TS,
		Outgoing: m.Outgoing, SenderID: sender, Kind: kind, Text: m.Text, Key: m.ID, Extras: x})
	for _, mn := range m.Mentions {
		if !mn.Who.none() {
			a.Exec("INSERT OR IGNORE INTO mention (message_id, address_id, token) VALUES (?, ?, ?)", mid, w.User(mn.Who),
				archive.NullStr(mn.Token))
		}
	}
	w.files(mid, m.Files)
	if m.Call != nil {
		w.call(m, cid, rowKey)
	}
	return mid, true
}

func (w *MetaWriter) reaction(r MetaReaction) archive.Reaction {
	out := archive.Reaction{Emoji: r.Emoji, Outgoing: r.Mine}
	if !r.Mine && !r.By.none() {
		out.Who = w.User(r.By)
	}
	return out
}

// files links a message's files.
func (w *MetaWriter) files(mid int64, files []MetaFile) {
	if w.Store == nil {
		return
	}
	for _, f := range files {
		rel := f.Rel
		if rel == "" {
			rel = baseName(f.Path)
		}
		w.Store.LinkNamed(w.SourceName, w.Source, f.Path, rel, mid, f.Name)
	}
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// call adds the call a message tells of: with the other person of a one-to-one thread, a group's
// in its conversation.
func (w *MetaWriter) call(m MetaMessage, cid int64, rowKey string) {
	a := w.A
	if a.HasOrigin(w.Source, rowKey, "call_origin") {
		return
	}
	c := archive.Call{Service: w.Service, TS: m.TS, Outgoing: m.Outgoing, Duration: m.Call.Duration,
		Answered: m.Call.Duration > 0, Video: m.Call.Video, Key: m.ID}
	var group bool
	a.Row("SELECT is_group FROM conversation WHERE id = ?", []any{cid}, &group)
	if group {
		c.ConversationID = cid
	} else if ids := a.Ints("SELECT address_id FROM conversation_member WHERE conversation_id = ?", cid); len(ids) == 1 {
		c.AddressID = ids[0]
	}
	switch {
	case m.Call.Missed || (!m.Outgoing && c.Duration == 0):
		c.Detail = "missed"
	case m.Outgoing && c.Duration == 0:
		c.Detail = "unanswered"
	}
	if c.Key != "" && a.Exists("SELECT 1 FROM call WHERE service_id = ? AND key = ?", a.Service.ID(w.Service), c.Key) {
		return
	}
	a.AddCall(w.Source, rowKey, c)
}

// React puts someone's reaction on a message in place of theirs before; "" takes it away. It says
// whether the message is in the archive.
func (w *MetaWriter) React(thread, id string, by MetaUser, mine bool, emoji string) bool {
	mid := w.Message(thread, id)
	if mid == 0 {
		return false
	}
	if mine {
		w.A.Exec("DELETE FROM reaction WHERE message_id = ? AND outgoing = 1", mid)
	} else {
		w.A.Exec("DELETE FROM reaction WHERE message_id = ? AND address_id = ? AND outgoing IS NOT 1", mid, w.User(by))
	}
	if emoji != "" {
		w.A.AddReactions(mid, []archive.Reaction{w.reaction(MetaReaction{Emoji: emoji, By: by, Mine: mine})})
	}
	return true
}

// Edit gives a message its new text, marked edited.
func (w *MetaWriter) Edit(thread, id, text string) bool {
	mid := w.Message(thread, id)
	if mid == 0 {
		mid = w.anyThread(id)
	}
	if mid == 0 {
		return false
	}
	ApplyChange(w.A, mid, Change{Text: &text, Edited: true})
	return true
}

// anyThread is a message by its id in whichever conversation (an edit names no thread).
func (w *MetaWriter) anyThread(id string) int64 {
	ids := w.A.Ints("SELECT id FROM message WHERE service_id = ? AND key = ? LIMIT 2", w.A.Service.ID(w.Service), id)
	if len(ids) == 1 {
		return ids[0]
	}
	return 0
}

// Delete marks a message deleted (for everyone, unsent); its text stays in the archive.
func (w *MetaWriter) Delete(thread, id string) bool {
	mid := w.Message(thread, id)
	if mid == 0 {
		mid = w.anyThread(id)
	}
	if mid == 0 {
		return false
	}
	ApplyChange(w.A, mid, Change{Deleted: true})
	return true
}

// Read records that someone read the owner's messages of a thread up to a time (Unix ms); at: when
// (0: not known).
func (w *MetaWriter) Read(thread string, by MetaUser, upTo, at int64) {
	cid := w.conv(thread)
	if cid == 0 || by.none() || upTo <= 0 {
		return
	}
	who := w.User(by)
	mine := "SELECT id FROM message WHERE conversation_id = ? AND outgoing AND ts <= ?"
	w.A.Exec("INSERT OR IGNORE INTO receipt (message_id, address_id) SELECT id, ? FROM ("+mine+")", who, cid, upTo)
	w.A.Exec("UPDATE receipt SET read_at = ? WHERE address_id = ? AND read_at IS NULL AND message_id IN ("+mine+")",
		at, who, cid, upTo)
}

// Delivered records that someone's device got the owner's messages up to a time.
func (w *MetaWriter) Delivered(thread string, by MetaUser, upTo, at int64) {
	cid := w.conv(thread)
	if cid == 0 || by.none() || upTo <= 0 {
		return
	}
	who := w.User(by)
	mine := "SELECT id FROM message WHERE conversation_id = ? AND outgoing AND ts <= ?"
	w.A.Exec("INSERT OR IGNORE INTO receipt (message_id, address_id) SELECT id, ? FROM ("+mine+")", who, cid, upTo)
	w.A.Exec("UPDATE receipt SET delivered_at = ? WHERE address_id = ? AND delivered_at IS NULL AND message_id IN ("+mine+")",
		at, who, cid, upTo)
}

// ReadKeys records that someone read these of the owner's messages (by id).
func (w *MetaWriter) ReadKeys(thread string, by MetaUser, ids []string, at int64, delivered bool) {
	cid := w.conv(thread)
	if cid == 0 || by.none() {
		return
	}
	who := w.User(by)
	col := "read_at"
	if delivered {
		col = "delivered_at"
	}
	for _, id := range ids {
		mid := w.Message(thread, id)
		if mid == 0 {
			continue
		}
		w.A.Exec("INSERT OR IGNORE INTO receipt (message_id, address_id) SELECT id, ? FROM message WHERE id = ? AND outgoing", who, mid)
		w.A.Exec("UPDATE receipt SET "+col+" = ? WHERE message_id = ? AND address_id = ? AND "+col+" IS NULL", at, mid, who)
	}
}

// State reports a thread's state as the service says it (read_until, muted: Unix ms, -1 for ever).
func (w *MetaWriter) State(thread, field string, value, observed int64) {
	if cid := w.conv(thread); cid != 0 {
		w.A.ReportState(w.Source, cid, field, value, observed, 0)
	}
}

// Title gives a group its new name.
func (w *MetaWriter) Title(thread, title string) {
	if cid := w.conv(thread); cid != 0 && title != "" {
		w.A.Exec("UPDATE conversation SET title = ? WHERE id = ? AND is_group AND title IS NOT ?", title, cid, title)
	}
}

// Member adds someone to a group's members.
func (w *MetaWriter) Member(thread string, u MetaUser) {
	if cid := w.conv(thread); cid != 0 && !u.none() {
		w.A.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", cid, w.User(u))
	}
}

// Keys are the ids of a conversation's messages the archive has, of those given.
func (w *MetaWriter) Keys(thread string, ids []string) map[string]bool {
	out := map[string]bool{}
	cid := w.conv(thread)
	if cid == 0 {
		return out
	}
	for _, id := range ids {
		if w.Message(thread, id) != 0 {
			out[id] = true
		}
	}
	return out
}
