// WhatsApp's calls on the iPhone (its call log and the call bubbles in the chats), on small
// databases made here.
package importers

import (
	"os"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
)

func pbVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func pbInt(field, v uint64) []byte { return pbVarint(pbVarint(nil, field<<3), v) }

func pbBytes(field uint64, data []byte) []byte {
	return append(pbVarint(pbVarint(nil, field<<3|2), uint64(len(data))), data...)
}

// TestWhatsAppCallBubbleWithABrokenMember: a call bubble whose list of members has one that is not
// text (a broken protobuf) still comes in, with the members that are; before, it stopped the
// import of every call (WhatsApp's, Viber's and the carrier's notices).
func TestWhatsAppCallBubbleWithABrokenMember(t *testing.T) {
	t.Cleanup(config.Load)
	t.Setenv("EVERYSAID_CACHE", t.TempDir())
	config.Load()
	must(t, os.MkdirAll(archive.IphoneData(), 0o700))
	a, _ := newArchive(t)
	chats := sqliteAt(t, WhatsAppIphoneDB(), `
CREATE TABLE ZWACHATSESSION (Z_PK INTEGER PRIMARY KEY, ZCONTACTJID TEXT, ZPARTNERNAME TEXT, ZSESSIONTYPE INTEGER);
CREATE TABLE ZWAGROUPMEMBER (Z_PK INTEGER PRIMARY KEY, ZCHATSESSION INTEGER, ZMEMBERJID TEXT);
CREATE TABLE ZWAMEDIAITEM (Z_PK INTEGER PRIMARY KEY, ZMETADATA BLOB);
CREATE TABLE ZWAPROFILEPUSHNAME (ZJID TEXT, ZPUSHNAME TEXT);
CREATE TABLE ZWAMESSAGE (Z_PK INTEGER PRIMARY KEY, ZCHATSESSION INTEGER, ZMESSAGEDATE REAL, ZISFROMME INTEGER,
    ZMESSAGETYPE INTEGER, ZMEDIAITEM INTEGER, ZFROMJID TEXT);`)
	sqliteAt(t, WhatsAppCallsDB(), `
CREATE TABLE ZWACDCALLEVENTPARTICIPANT (Z1PARTICIPANTS INTEGER, ZJIDSTRING TEXT, ZOUTCOME INTEGER);
CREATE TABLE ZWAAGGREGATECALLEVENT (Z_PK INTEGER PRIMARY KEY, ZINCOMING INTEGER, ZMISSED INTEGER, ZVIDEO INTEGER);
CREATE TABLE ZWACDCALLEVENT (Z_PK INTEGER PRIMARY KEY, Z1CALLEVENTS INTEGER, ZDATE REAL, ZOUTCOME INTEGER, ZDURATION REAL,
    ZGROUPJIDSTRING TEXT, ZCALLIDSTRING TEXT, ZGROUPCALLCREATORUSERJIDSTRING TEXT);`)
	group := "120363000000000009@g.us"
	db.Exec(chats, "INSERT INTO ZWACHATSESSION VALUES (1, ?, 'Friends', 1)", group)
	call := pbInt(1, 0)
	call = append(call, pbInt(2, 0)...)
	call = append(call, pbInt(3, 60)...)
	call = append(call, pbBytes(5, pbBytes(1, []byte("15557770001@s.whatsapp.net")))...)
	call = append(call, pbBytes(5, pbBytes(1, []byte{0xff, 0xfe, '@'}))...)
	db.Exec(chats, "INSERT INTO ZWAMEDIAITEM VALUES (1, ?)", pbBytes(87, pbBytes(1, call)))
	db.Exec(chats, "INSERT INTO ZWAMESSAGE VALUES (1, 1, ?, 1, 59, 1, NULL)", float64(pairTS/1000-archive.AppleEpoch))

	must(t, VoIP(a, nil, VoIPOptions{NoBridge: true, Carriers: []string{}}))
	eq(t, "calls", a.Int("SELECT count(*) FROM call"), int64(1))
	eq(t, "members", db.Strs(a.Tx(), "SELECT ad.value FROM call_member m JOIN address ad ON ad.id = m.address_id"),
		[]string{"+15557770001"})
}

// TestViberTokenTime: a Viber message's time is its date, or, where the date is missing or 0 (no
// Viber message is from 2001), the time its token carries.
func TestViberTokenTime(t *testing.T) {
	token := int64(1_400_000_000_000) << 22 // sent at 1,400,000,000,000 - 292,057,776,050 Unix ms
	want := int64(1_400_000_000_000 + tokenEpochMS)
	eq(t, "no date", tokenTime(nil, token), want)
	eq(t, "date 0", tokenTime(int64(0), token), want)
	eq(t, "date 0.0", tokenTime(float64(0), token), want)
	eq(t, "a date", tokenTime(float64(800_000_000), token), int64(800_000_000+archive.AppleEpoch)*1000)
	eq(t, "no token", tokenTime(int64(0), 0), int64(0))
}

// TestMediaSameSizeAnotherFile: a source path linked before may hold another file now (a new
// export whose part numbers start again); one of the same size is not taken for the old one: its
// hash is reused only while the file is the stored one (same size and time), else read again.
func TestMediaSameSizeAnotherFile(t *testing.T) {
	a, _ := newArchive(t)
	s := NewStore(a)
	s.Root = t.TempDir()
	dir := t.TempDir()
	path := dir + "/part-1"
	src := a.Source("test/mms", "test.db", "", dir)
	conv := a.Conversation("sms", []archive.Handle{archive.H("phone", "+15557770001")}, "", "")
	m1 := a.AddMessage(src, "1", archive.Message{Service: "sms", ConversationID: conv, TS: 1, Kind: "image"})
	m2 := a.AddMessage(src, "2", archive.Message{Service: "sms", ConversationID: conv, TS: 2, Kind: "image"})
	must(t, os.WriteFile(path, []byte("first picture"), 0o600))
	s.Link("test/mms", src, path, "part-1", m1)
	must(t, os.Remove(path))
	must(t, os.WriteFile(path, []byte("other picture"), 0o600)) // the same size
	future := time.Now().Add(time.Hour)
	must(t, os.Chtimes(path, future, future))
	s.Link("test/mms", src, path, "part-1", m2)
	hashes := db.Strs(a.Tx(), "SELECT sha256 FROM attachment ORDER BY message_id")
	if len(hashes) != 2 || hashes[0] == hashes[1] {
		t.Fatalf("two files of the same size at one path: hashes %v", hashes)
	}
	eq(t, "the second's", hashes[1], sha256File(path))
}
