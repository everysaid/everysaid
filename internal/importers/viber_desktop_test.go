package importers

// The running Viber Desktop's database, as the live source reads it: reactions as events of their
// own, an edit event, a deletion, the notes, mentions; and each of them followed when it changes
// after the message came in.

import (
	"database/sql"
	"path/filepath"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

const desktopSchema = `
CREATE TABLE "Contact" ( ContactID integer primary key autoincrement, Name TEXT, ABContact smallint (0,1) default 0 not null, Number TEXT unique, MID TEXT unique, EncryptedMID TEXT unique, ClientName TEXT, DownloadID TEXT, ContactFlags long default 0, SortName TEXT, Timestamp longint default 0, DateOfBirth TEXT);
CREATE TABLE "ChatInfo" ( ChatID integer primary key autoincrement, Name varchar(200), Token varchar(50) unique, Flags integer default 0, TimeStamp longint not null, IconID varchar(255), BackgroundID varchar(255), LastReadMessageToken integer default 0, LastReadMessageId integer default 0, LastSeenMessageToken integer default 0, PGType integer, PGUri varchar(255), PGRevision integer, PGLongtitude integer, PGLatitude integer, PGCountry varchar(255), PGTabLine varchar(255), PGTags varchar(255), PGLastMessageID integer, PGWatchersCount integer, PGSearchFlags integer, PGSearchExFlags integer, MetaData varchar(255) );
CREATE TABLE "ChatRelation" ( ChatID integer references ChatInfo (ChatID) NOT NULL, ContactID integer references Contact (ContactID) NOT NULL, PGRole integer, primary key (ChatID, ContactID) );
CREATE TABLE "Events" ( EventID integer primary key autoincrement, TimeStamp longint not null, Direction unsigned integer not null, Type smallint not null, ContactLongitude signed long default 0, ContactLatitude signed long default 0, ChatID integer references ChatInfo (ChatID) on update cascade, ContactID integer references Contact (ContactID) on update cascade, IsSessionLifeTime unsigned integer (0, 1) default 0, Flags integer default 0, Token unsigned long not null, IsRead smallint (0,1) not null default 0, SortOrder unsigned long NOT NULL DEFAULT 0, Seq integer NOT NULL DEFAULT 0 );
CREATE TABLE "Messages" ( EventID integer references Events (EventID) on update cascade, Type unsigned integer not null, Status integer not null, Subject varchar(500), Body varchar(5000), Flag unsigned integer default 0, PayloadPath varchar(1000), ThumbnailPath varchar(100), StickerID unsigned long default 0, PttID varchar(100), PttStatus unsigned short default 0, Duration signed default 0, PGMessageId unsigned long default 0, PGIsLiked integer, PGLikeCount integer, Info varchar(7000), AppId integer default 0, ClientFlag unsigned integer default 0, FollowersLikeCount unsigned integer default 0, AdminsReactions varchar(255), MembersReactions varchar(255), PrevReaction integer, SelfReaction varchar(10), PrevSelfReaction varchar(10), primary key (EventID) );
CREATE TABLE "LikeRelation" (MessageToken INTEGER NOT NULL, LikeEventID INTEGER REFERENCES Events(EventID) ON DELETE CASCADE NOT NULL, primary key(MessageToken, LikeEventID));
`

const (
	dtGroup, dtNotes          = 2, 1
	dtMe, dtMaria, dtBob      = 1, 2, 3
	dtHello, dtNote, dtFixed  = 6200000000000000001, 6288698136818094900, 6288698136818095000
	dtGone, dtT0              = 6288698136818095100, int64(1_791_000_000_000)
	dtNotesFlag               = 524292
	dtLikeQuick, dtLikeRemove = 1, 0
)

type desktopFixture struct {
	t  *testing.T
	d  *sql.DB
	ts int64
}

func newDesktop(t *testing.T, path string) *desktopFixture {
	d, err := db.Open(path)
	must(t, err)
	t.Cleanup(func() { d.Close() })
	db.Exec(d, desktopSchema)
	db.Exec(d, "INSERT INTO Contact (ContactID, Number, MID) VALUES (1, '+15550000000', 'mid-me'), "+
		"(2, '+15557770001', 'mid-maria'), (3, '+15557770002', 'mid-bob')")
	db.Exec(d, "INSERT INTO ChatInfo (ChatID, Name, Token, Flags, TimeStamp, PGType) VALUES "+
		"(1, NULL, '5501', ?, 0, 255), (2, 'Friends', '7701', 0, 0, 255)", dtNotesFlag)
	db.Exec(d, "INSERT INTO ChatRelation (ChatID, ContactID) VALUES (1, 1), (2, 1), (2, 2), (2, 3)")
	return &desktopFixture{t: t, d: d, ts: dtT0}
}

// message adds one; it returns its EventID.
func (f *desktopFixture) message(chat, contact int64, mine bool, token int64, typ int, body, info string) int64 {
	f.ts += 1000
	dir := 0
	if mine {
		dir = 1
	}
	r := db.Exec(f.d, "INSERT INTO Events (TimeStamp, Direction, Type, ChatID, ContactID, Token) VALUES (?, ?, 0, ?, ?, ?)",
		f.ts, dir, chat, contact, token)
	id, _ := r.LastInsertId()
	db.Exec(f.d, "INSERT INTO Messages (EventID, Type, Status, Body, Info, MembersReactions) VALUES (?, ?, 0, ?, ?, '')",
		id, typ, body, info)
	return id
}

// like is a reaction event of who on the message of token (quick: Viber's 1-5, 0 taken back).
func (f *desktopFixture) like(chat, contact int64, mine bool, target int64, quick int, emoji string) {
	f.ts += 1000
	dir := 0
	if mine {
		dir = 1
	}
	r := db.Exec(f.d, "INSERT INTO Events (TimeStamp, Direction, Type, ChatID, ContactID, Token) VALUES (?, ?, 3, ?, ?, ?)",
		f.ts, dir, chat, contact, f.ts)
	id, _ := r.LastInsertId()
	db.Exec(f.d, "INSERT INTO Messages (EventID, Type, Status, Body, PGIsLiked, SelfReaction) VALUES (?, 0, 129, ?, ?, ?)",
		id, target, quick, emoji)
	db.Exec(f.d, "INSERT INTO LikeRelation VALUES (?, ?)", target, id)
}

func (f *desktopFixture) counts(token int64, counts string) {
	db.Exec(f.d, "UPDATE Messages SET MembersReactions = ? WHERE EventID = (SELECT EventID FROM Events WHERE Token = ?)",
		counts, token)
}

// desktopReactionsOf are a message's reactions, as emoji/who/mine/count lines in order.
func desktopReactionsOf(a *archive.Archive, key string) []string {
	var out []string
	a.Each("SELECT coalesce(r.emoji, ''), coalesce(ad.value, ''), coalesce(r.outgoing, 0), r.count FROM reaction r "+
		"JOIN message m ON m.id = r.message_id LEFT JOIN address ad ON ad.id = r.address_id WHERE m.key = ? "+
		"ORDER BY r.outgoing, ad.value, r.emoji", []any{key}, func(scan func(...any)) {
		var e, who string
		var mine, n int64
		scan(&e, &who, &mine, &n)
		s := e + " " + who
		if mine != 0 {
			s += " me"
		}
		if n != 1 {
			s += " x" + string(rune('0'+n))
		}
		out = append(out, s)
	})
	return out
}

func TestViberDesktopLive(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "desktop.db")
	f := newDesktop(t, path)
	f.message(dtGroup, dtMaria, false, dtHello, 1, "hello ‪@Bob‬",
		js(M{"textMetaInfo": []M{{"type": 0, "memberId": "mid-bob", "start": 6, "end": 12}}}))
	f.like(dtGroup, dtMaria, false, dtHello, dtLikeQuick, "")
	f.like(dtGroup, dtMe, true, dtHello, 2, "")
	f.like(dtGroup, dtMe, true, dtHello, 1, "") // changed: the last is the one
	f.counts(dtHello, `{"1":2,"2":1}`)
	f.message(dtNotes, dtMe, true, dtNote, 1, "a note", "{}")
	f.message(dtGroup, dtMe, true, dtFixed, 1, "fixed text", js(M{"desktop_info": M{"edit_token": 99}}))
	f.message(dtGroup, dtMe, true, 99, 1, "fixed text", js(M{"edit": M{"token": dtFixed}}))
	f.message(dtGroup, dtBob, false, dtGone, desktopDeleted, "", "")

	opt := ViberOptions{DesktopDB: path, NoIphone: true, DesktopSource: "viber-desktop/viber"}
	must(t, Viber(a, nil, opt))
	// the reactions and the edit event are no lines of their own
	eq(t, "messages", db.Strs(a.Tx(), "SELECT key FROM message ORDER BY ts"),
		[]string{"6200000000000000001", "6288698136818094900", "6288698136818095000", "6288698136818095100"})
	eq(t, "reactions", desktopReactionsOf(a, "6200000000000000001"),
		[]string{"😂 ", "❤️ +15557770001", "❤️  me"})
	eq(t, "mention", db.Strs(a.Tx(), "SELECT a.value FROM mention n JOIN address a ON a.id = n.address_id"),
		[]string{"+15557770002"})
	eq(t, "edited", msgRow(a, "6288698136818095000", "text, edited"), []any{"fixed text", int64(1)})
	eq(t, "deleted", msgRow(a, "6288698136818095100", "deleted"), []any{int64(1)})
	notes := a.Int("SELECT conversation_id FROM message WHERE key = '6288698136818094900'")
	eq(t, "notes no group", a.Int("SELECT is_group FROM conversation WHERE id = ?", notes), int64(0))
	eq(t, "notes, the owner's", db.Strs(a.Tx(), "SELECT a.value FROM conversation_member m JOIN address a "+
		"ON a.id = m.address_id WHERE m.conversation_id = ?", notes), []string{"+15550000000"})

	// later: the user's reaction taken back, the other's changed, the note edited, hello deleted
	f.like(dtGroup, dtMe, true, dtHello, dtLikeRemove, "")
	f.like(dtGroup, dtMaria, false, dtHello, 0, "🙏")
	f.counts(dtHello, `{"2":1,"🙏":1}`)
	db.Exec(f.d, "UPDATE Messages SET Body = 'a note, fixed', Info = ? WHERE EventID = "+
		"(SELECT EventID FROM Events WHERE Token = ?)", js(M{"desktop_info": M{"edit_token": 98}}), dtNote)
	f.message(dtNotes, dtMe, true, 98, 1, "a note, fixed", js(M{"edit": M{"token": dtNote}}))
	db.Exec(f.d, "UPDATE Messages SET Type = 72, Body = '', Info = '' WHERE EventID = "+
		"(SELECT EventID FROM Events WHERE Token = ?)", dtHello)
	must(t, Viber(a, nil, opt))
	must(t, Viber(a, nil, opt)) // again: nothing changes
	eq(t, "messages after", a.Int("SELECT count(*) FROM message"), int64(4))
	eq(t, "reactions after", desktopReactionsOf(a, "6200000000000000001"), []string{"😂 ", "🙏 +15557770001"})
	eq(t, "note edited", msgRow(a, "6288698136818094900", "text, edited"), []any{"a note, fixed", int64(1)})
	eq(t, "hello deleted, its text kept", msgRow(a, "6200000000000000001", "text, deleted"),
		[]any{"hello ‪@Bob‬", int64(1)})
}

// A message the desktop says nothing of the reactions of (no counts, no events) keeps those the
// archive has from the iPhone, with who gave them.
func TestViberDesktopKeepsWhatItDoesNotKnow(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "desktop.db")
	f := newDesktop(t, path)
	f.message(dtGroup, dtMaria, false, dtHello, 1, "hello", "{}")
	opt := ViberOptions{DesktopDB: path, NoIphone: true, DesktopSource: "viber-desktop/viber"}
	must(t, Viber(a, nil, opt))
	mid := a.Int("SELECT id FROM message WHERE key = '6200000000000000001'")
	a.AddReactions(mid, []archive.Reaction{{Emoji: "😮", Code: "viber:3", Count: 1, Who: archive.H("phone", "+15557770002")}})
	must(t, Viber(a, nil, opt))
	eq(t, "kept", desktopReactionsOf(a, "6200000000000000001"), []string{"😮 +15557770002"})
}

// How far the owner read each chat, on any device, and how far the other person in a chat with one
// saw the owner's messages: from the desktop's chats, as from the iPhone's.
func TestViberDesktopReads(t *testing.T) {
	a, _ := newArchive(t)
	path := filepath.Join(t.TempDir(), "desktop.db")
	f := newDesktop(t, path)
	db.Exec(f.d, "INSERT INTO ChatInfo (ChatID, Name, Token, Flags, TimeStamp, PGType) VALUES (3, NULL, NULL, 0, 0, 255)")
	db.Exec(f.d, "INSERT INTO ChatRelation (ChatID, ContactID) VALUES (3, 1), (3, 2)")
	f.message(3, dtMaria, false, 7001, 1, "are you there?", "{}")
	f.message(3, dtMe, true, 7002, 1, "yes", "{}")
	f.message(3, dtMe, true, 7003, 1, "and now?", "{}")
	f.message(3, dtMaria, false, 7004, 1, "later", "{}")
	db.Exec(f.d, "UPDATE ChatInfo SET LastReadMessageToken = 7001, LastSeenMessageToken = 7002 WHERE ChatID = 3")
	a.Exec("INSERT INTO plugin_instance (plugin, kind, label, created_at) VALUES ('viber-desktop', 'source', 'V', 0)")
	a.Exec("INSERT INTO source (name, path, instance_id) VALUES ('viber-desktop/viber', ?, ?)", path,
		a.Int("SELECT max(id) FROM plugin_instance"))
	opt := ViberOptions{DesktopDB: path, NoIphone: true, DesktopSource: "viber-desktop/viber"}
	must(t, Viber(a, nil, opt))
	conv := a.Int("SELECT conversation_id FROM message WHERE key = '7001'")
	eq(t, "read up to the first", a.Int("SELECT value FROM state_report WHERE field = 'read_until' AND conversation_id = ?", conv),
		a.Int("SELECT ts FROM message WHERE key = '7001'"))
	eq(t, "seen: the first of the owner's", db.Strs(a.Tx(), "SELECT m.key FROM receipt r JOIN message m ON m.id = r.message_id "+
		"WHERE r.read_at IS NOT NULL ORDER BY m.key"), []string{"7002"})

	// read and seen on the phone since
	db.Exec(f.d, "UPDATE ChatInfo SET LastReadMessageToken = 7004, LastSeenMessageToken = 7003 WHERE ChatID = 3")
	must(t, Viber(a, nil, opt))
	eq(t, "read up to the last", a.Int("SELECT max(value) FROM state_report WHERE field = 'read_until' AND conversation_id = ?", conv),
		a.Int("SELECT ts FROM message WHERE key = '7004'"))
	eq(t, "seen: both", db.Strs(a.Tx(), "SELECT m.key FROM receipt r JOIN message m ON m.id = r.message_id "+
		"WHERE r.read_at IS NOT NULL ORDER BY m.key"), []string{"7002", "7003"})
}
