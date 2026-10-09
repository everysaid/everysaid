// The Adium and Pidgin logs importer, on invented logs of both programs.
package importers

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"howett.net/plist"

	"everysaid/internal/archive"
	"everysaid/internal/config"
)

const imAdiumXML = `<?xml version="1.0" encoding="UTF-8" ?>
<chat xmlns="http://purl.org/net/ulf/ns/0.4-02" account="me@hotmail.com" service="MSN">
<event type="windowOpened" sender="me@hotmail.com" time="2008-06-16T13:58:55+03:00"/>
<message sender="friend@hotmail.com" time="2008-06-16T13:58:55+03:00" alias="Ο &quot;Φίλος&quot; &amp; co"><div>hello &amp; welcome<br/>second line</div></message>
<message sender="me@hotmail.com" time="2008-06-16T13:59:01+03:00" alias="me"><div><span>hi</span></div></message>
<status type="away" sender="friend@hotmail.com" time="2008-06-16T14:00:00+03:00"/>
<message sender="friend@hotmail.com" time="2008-06-16T14:01:02+0300"><div><img src="pic.png" alt=""/></div></message>
<message sender="friend@hotmail.com" time="2008-06-16T14:01:02+0300"><div><img src="pic.png" alt=""/></div></message>
</chat>
`

const imAdiumHTML = "\ufeff" + `<div class="receive"><span class="timestamp">11:59:47 PM</span> <span class="sender">pal: </span>` +
	`<pre class="message">kalhspera!</pre></div>` + "\n" +
	`<div class="send"><span class="timestamp">12:00:01 AM</span> <span class="sender">myaim: </span>` +
	`<pre class="message">kalws ton</pre></div>` + "\n" +
	`<div class="status"><span class="timestamp">12:00:05 AM</span> pal went away</div>` + "\n"

const imPidginTxt = `Conversation with friend@hotmail.com at 2005-06-07 21:06:30 on me@hotmail.com (msn)
(21:06:32) alex: hello
(21:06:35) O Filos: hi there
and a second line
(21:07:14) The privacy status of the current conversation is now: Unverified
(21:08:00) Ο χρήστης O Filos έχει κλείσει το παράθυρο συνομιλίας.
`

const imPidginTxt2 = `Conversation with other@hotmail.com at 2005-06-08 10:00:00 on me@hotmail.com (msn)
(10:00:01) alex: hey
(10:00:05) Other One: yo
`

const imPidginTxt3 = `Conversation with third@hotmail.com at 2005-06-09 10:00:00 on me@hotmail.com (msn)
(10:00:01) alex: hey
(10:00:05) Third: yo
`

const imBlist = `<?xml version='1.0' encoding='UTF-8' ?>
<purple version='1.0'><blist><group name='Buddies'><contact>
<buddy account='me@hotmail.com' proto='prpl-msn'><name>friend@hotmail.com</name><alias>Φίλιππος</alias></buddy>
</contact><contact>
<buddy account='me@hotmail.com' proto='prpl-msn'><name>other@hotmail.com</name></buddy>
<buddy account='me@hotmail.com' proto='prpl-msn'><name>third@hotmail.com</name></buddy>
</contact></group></blist></purple>
`

const imAccounts = `<?xml version='1.0' encoding='UTF-8' ?>
<account version='1.0'><account><protocol>prpl-msn</protocol><name>me@hotmail.com</name><alias>alex</alias></account></account>
`

var imContactList = map[string]any{"MetaContact Ownership": map[string]any{
	"MetaContact-1": []any{map[string]any{"UID": "friend@hotmail.com", "ServiceID": "MSN"}, map[string]any{"UID": "pal", "ServiceID": "AIM"}},
	"MetaContact-2": []any{map[string]any{"UID": "nobody@hotmail.com", "ServiceID": "MSN"}}}}

// imSetup points Everysaid's folders into the test's own, with an [owner] as the Python tests have.
func imSetup(t *testing.T) string {
	t.Helper()
	t.Cleanup(config.Load) // after t.Setenv has put the folders back
	dir := t.TempDir()
	for _, v := range []string{"DATA", "CACHE", "CONFIG", "STATE"} {
		p := filepath.Join(dir, strings.ToLower(v))
		os.MkdirAll(p, 0o700)
		t.Setenv("EVERYSAID_"+v, p)
	}
	t.Setenv("EVERYSAID_KEYRING", "everysaid-test")
	os.WriteFile(filepath.Join(dir, "config", "config.toml"), []byte("[owner]\nnumbers = [\"+15550000000\"]\nregion = \"US\"\n"), 0o600)
	config.Load()
	return dir
}

func imWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// imBuild makes the invented logs: (Adium 2.0 folder, .purple folder).
func imBuild(t *testing.T, tmp string) (string, string) {
	adium := filepath.Join(tmp, "Adium 2.0", "Users", "Default", "Logs")
	log := filepath.Join(adium, "MSN.me@hotmail.com", "friend@hotmail.com", "friend@hotmail.com (2008-06-16T13.58.55+0300).chatlog")
	imWrite(t, filepath.Join(log, "friend@hotmail.com (2008-06-16T13.58.55+0300).xml"), imAdiumXML)
	imWrite(t, filepath.Join(log, "pic.png"), "\x89PNG\r\n\x1a\n"+strings.Repeat("0", 20))
	imWrite(t, filepath.Join(adium, "AIM.myaim", "pal", "pal (2006-11-06).AdiumHTMLLog"), imAdiumHTML)
	// the same account folders, no logs
	os.MkdirAll(filepath.Join(filepath.Dir(adium), "Contact Album", "MSN.me@hotmail.com"), 0o700)
	data, err := plist.Marshal(imContactList, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	imWrite(t, filepath.Join(filepath.Dir(adium), "Contact List.plist"), string(data))
	purple := filepath.Join(tmp, "purple")
	acc := filepath.Join(purple, "logs", "msn", "me@hotmail.com")
	imWrite(t, filepath.Join(acc, "friend@hotmail.com", "2005-06-07.210630.txt"), imPidginTxt)
	imWrite(t, filepath.Join(acc, "other@hotmail.com", "2005-06-08.100000+0300EEST.txt"), imPidginTxt2)
	imWrite(t, filepath.Join(acc, "third@hotmail.com", "2005-06-08.100000+0300EEST.txt"), imPidginTxt3)
	imWrite(t, filepath.Join(purple, "blist.xml"), imBlist)
	imWrite(t, filepath.Join(purple, "accounts.xml"), imAccounts)
	return filepath.Join(tmp, "Adium 2.0"), purple
}

func imOpen(t *testing.T, path string) *archive.Archive {
	t.Helper()
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func sumInts(m map[ImlogsKey]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func TestImlogsAdiumAndPidginLogs(t *testing.T) {
	tmp := imSetup(t)
	adium, purple := imBuild(t, tmp)
	a := imOpen(t, filepath.Join(tmp, "archive.db"))
	stats, err := Imlogs(a, nil, ImlogsOptions{Adium: adium, Pidgin: purple, NoMedia: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := stats.Added[ImlogsKey{"adium", "msn"}]; n != 3 { // two texts and one picture; its repeat is a dupe
		t.Errorf("adium/msn added %d", n)
	}
	if n := stats.Dupes[ImlogsKey{"adium", "msn"}]; n != 1 {
		t.Errorf("adium/msn dupes %d", n)
	}
	if n := stats.Added[ImlogsKey{"adium", "aim"}]; n != 2 {
		t.Errorf("adium/aim added %d", n)
	}
	if n := stats.Added[ImlogsKey{"pidgin", "msn"}]; n != 6 {
		t.Errorf("pidgin/msn added %d", n)
	}
	if len(stats.Problems) != 0 {
		t.Errorf("problems %v", stats.Problems)
	}
	if n := a.Int("SELECT count(*) FROM message_origin"); n != 0 { // known by their fingerprint
		t.Errorf("%d origins", n)
	}
	tx := a.Tx()
	// one MSN conversation with the friend, from both programs
	type msg struct {
		ts       int64
		outgoing int
		text     sql.NullString
		kind     string
	}
	var rows []msg
	a.Each("SELECT m.ts, m.outgoing, m.text, k.name FROM message m JOIN conversation c ON c.id = m.conversation_id "+
		"JOIN message_kind k ON k.id = m.kind_id WHERE c.key = 'friend@hotmail.com' ORDER BY m.ts", nil, func(scan func(...any)) {
		var r msg
		scan(&r.ts, &r.outgoing, &r.text, &r.kind)
		rows = append(rows, r)
	})
	var dirs []int
	for _, r := range rows {
		dirs = append(dirs, r.outgoing)
	}
	if len(rows) != 5 || dirs[0] != 1 || dirs[1] != 0 || dirs[2] != 0 || dirs[3] != 1 || dirs[4] != 0 {
		t.Fatalf("directions %v", dirs)
	}
	// Pidgin: owner by alias, lines joined
	if rows[0].text.String != "hello" || rows[1].text.String != "hi there\nand a second line" {
		t.Errorf("pidgin texts %q %q", rows[0].text.String, rows[1].text.String)
	}
	// Adium: entities and breaks
	if rows[2].text.String != "hello & welcome\nsecond line" || rows[4].kind != "image" {
		t.Errorf("adium %q %q", rows[2].text.String, rows[4].kind)
	}
	if rows[2].ts != 1213613935000 { // 13:58:55 +03:00
		t.Errorf("ts %d", rows[2].ts)
	}
	// the legacy HTML log: AM/PM, past midnight, direction by class
	var aim []msg
	a.Each("SELECT m.ts, m.outgoing FROM message m JOIN service s ON s.id = m.service_id WHERE s.name = 'aim' ORDER BY m.ts",
		nil, func(scan func(...any)) {
			var r msg
			scan(&r.ts, &r.outgoing)
			aim = append(aim, r)
		})
	if len(aim) != 2 || aim[0].outgoing != 0 || aim[1].outgoing != 1 || aim[1].ts-aim[0].ts != 14000 {
		t.Errorf("aim %v", aim)
	}
	// names: Adium's alias (chat), Pidgin's buddy list (book), entities read
	names := map[string]string{}
	a.Each("SELECT kind, name FROM handle_name", nil, func(scan func(...any)) {
		var k, n string
		scan(&k, &n)
		names[k] = n
	})
	if len(names) != 2 || names["chat"] != `Ο "Φίλος" & co` || names["book"] != "Φίλιππος" {
		t.Errorf("names %v", names)
	}
	// the owner's old groupings: the friend and the AIM pal are one person, as are "other" and "third"
	personOf := func(kind, value string, service any) int64 {
		var id int64
		if err := tx.QueryRow("SELECT pa.person_id FROM person_address pa JOIN address a ON a.id = pa.address_id "+
			"JOIN address_kind k ON k.id = a.kind_id LEFT JOIN service s ON s.id = a.service_id "+
			"WHERE k.name = ? AND a.value = ? AND s.name IS ?", kind, value, service).Scan(&id); err != nil {
			t.Fatalf("person of %s %s: %v", kind, value, err)
		}
		return id
	}
	if personOf("email", "friend@hotmail.com", nil) != personOf("id", "pal", "aim") {
		t.Error("friend and pal not merged")
	}
	if personOf("email", "other@hotmail.com", nil) != personOf("email", "third@hotmail.com", nil) {
		t.Error("other and third not merged")
	}
	if personOf("email", "friend@hotmail.com", nil) == personOf("email", "other@hotmail.com", nil) {
		t.Error("friend and other merged")
	}
	if len(stats.Merged) != 2 || stats.Merged["adium"] != 1 || stats.Merged["pidgin"] != 1 {
		t.Errorf("merged %v", stats.Merged)
	}
	// the owner's accounts
	own := map[string]bool{}
	a.Each("SELECT ad.value FROM account x JOIN address ad ON ad.id = x.address_id", nil, func(scan func(...any)) {
		var v string
		scan(&v)
		own[v] = true
	})
	if !own["me@hotmail.com"] || !own["myaim"] {
		t.Errorf("accounts %v", own)
	}
	// the owner's account each chat was on is one of its members, also when a later run finds every
	// message already there
	on := func() []string {
		var out []string
		a.Each("SELECT ad.value FROM conversation_member cm JOIN conversation c ON c.id = cm.conversation_id "+
			"JOIN address ad ON ad.id = cm.address_id WHERE c.key = 'friend@hotmail.com' "+
			"AND cm.address_id IN (SELECT address_id FROM account)", nil, func(scan func(...any)) {
			var v string
			scan(&v)
			out = append(out, v)
		})
		return out
	}
	if got := on(); len(got) != 1 || got[0] != "me@hotmail.com" {
		t.Errorf("account member %v", got)
	}
	a.Exec("DELETE FROM conversation_member WHERE address_id IN (SELECT address_id FROM account)")
	// a second run adds nothing
	again, err := Imlogs(a, nil, ImlogsOptions{Adium: adium, Pidgin: purple, NoMedia: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := on(); len(got) != 1 || got[0] != "me@hotmail.com" {
		t.Errorf("account member again %v", got)
	}
	if sumInts(again.Added) != 0 || sumInts(again.Seen) != 12 { // the 11 and the repeat of the picture
		t.Errorf("again: added %d seen %d", sumInts(again.Added), sumInts(again.Seen))
	}
}

func TestImlogsHandles(t *testing.T) {
	imSetup(t)
	for _, c := range []struct {
		service, raw string
		want         archive.Handle
	}{
		{"jabber", "x@gmail.com/Pidgin", archive.H("email", "x@gmail.com")},
		{"jabber", "-123@chat.facebook.com", archive.H("id", "123", "messenger")},
		{"messenger", "100000000000001", archive.H("id", "100000000000001", "messenger")},
		{"icq", "123456789", archive.H("id", "123456789", "icq")},
		{"aim", "Some Name", archive.H("id", "somename", "aim")},
		{"whatsapp", "15555550123", archive.H("phone", "+15555550123")},
	} {
		if got := imHandle(c.service, c.raw); got != c.want {
			t.Errorf("imHandle(%q, %q) = %v, want %v", c.service, c.raw, got, c.want)
		}
	}
	for _, c := range []struct {
		service, raw string
		want         bool
	}{
		{"msn", "msn%20chat@hotmail.com.chat", true},
		{"jabber", "-600000001@chat.facebook.com", true},
		{"aim", "chat100000000000000000", true},
		{"jabber", "friend@jabber.org", false},
	} {
		if got := imIsGroup(c.service, c.raw); got != c.want {
			t.Errorf("imIsGroup(%q, %q) = %v", c.service, c.raw, got)
		}
	}
}

func TestImlogsFoldersAreFound(t *testing.T) {
	tmp := imSetup(t)
	adium, purple := imBuild(t, tmp)
	if got := adiumLogs(adium); !strings.HasSuffix(got, filepath.Join("Users", "Default", "Logs")) {
		t.Errorf("adium logs %q", got)
	}
	if got := adiumLogs(filepath.Join(adium, "Users", "Default", "Logs")); !strings.HasSuffix(got, "Logs") {
		t.Errorf("adium logs given %q", got)
	}
	if got := adiumLogs(filepath.Join(tmp, "nowhere")); got != "" {
		t.Errorf("nowhere %q", got)
	}
	if p, l := pidginRoot(purple); p != purple || l != filepath.Join(purple, "logs") {
		t.Errorf("pidgin root %q %q", p, l)
	}
	if p, l := pidginRoot(filepath.Join(purple, "logs")); p != "" || l != filepath.Join(purple, "logs") {
		t.Errorf("pidgin logs %q %q", p, l)
	}
}

func TestImlogsALogWithNoMessagesMakesNoChat(t *testing.T) {
	tmp := imSetup(t)
	adium, purple := imBuild(t, tmp)
	quiet := filepath.Join(adium, "Users", "Default", "Logs", "MSN.me@hotmail.com", "quiet@hotmail.com",
		"quiet@hotmail.com (2008-06-17T10.00.00+0300).chatlog")
	imWrite(t, filepath.Join(quiet, "quiet@hotmail.com (2008-06-17T10.00.00+0300).xml"),
		`<?xml version="1.0" encoding="UTF-8" ?>`+"\n"+`<chat xmlns="http://purl.org/net/ulf/ns/0.4-02" `+
			`account="me@hotmail.com" service="MSN"><event type="windowOpened" sender="me@hotmail.com" `+
			`time="2008-06-17T10:00:00+03:00"/></chat>`+"\n")
	a := imOpen(t, filepath.Join(tmp, "archive.db"))
	if _, err := Imlogs(a, nil, ImlogsOptions{Adium: adium, Pidgin: purple, NoMedia: true}); err != nil {
		t.Fatal(err)
	}
	read := map[string][]string{}
	for _, c := range adiumConversations(adiumLogs(adium)) {
		read[c.contact] = c.files
	}
	if len(read["quiet@hotmail.com"]) == 0 { // read, with nothing in it
		t.Error("quiet log not read")
	}
	if a.Exists("SELECT 1 FROM conversation_member cm JOIN address ad ON ad.id = cm.address_id WHERE ad.value = 'quiet@hotmail.com'") {
		t.Error("a member for the quiet log")
	}
	if n := a.Int("SELECT count(*) FROM conversation c WHERE NOT EXISTS (SELECT 1 FROM message m WHERE m.conversation_id = c.id)"); n != 0 {
		t.Errorf("%d empty conversations", n)
	}
}

// Python's library as these logs read it: entities, %-escapes, Python's \d and \w.
func TestImlogsPythonSemantics(t *testing.T) {
	for in, want := range map[string]string{
		"&amp;&lt;": "&<", "&#0;": "\ufffd", "&#x80;": "\u20ac", "&#x81;": "\u0081", "&#1;": "", "&#13;": "\r",
		"&#xD800;": "\ufffd", "&#99999999999999999999;": "\ufffd", "&ampx": "&x", "&notit;": "\u00acit;",
		"&foo-bar;": "&foo-bar;", "&#65": "A", "&#;": "&#;", "&#xfffe;": "",
	} {
		if got := pyUnescape(in); got != want {
			t.Errorf("unescape(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"a%20b": "a b", "%zz": "%zz", "%e2%82%ac": "\u20ac", "%e2%82": "\ufffd", "%": "%", "%4": "%4",
		"\u00e9%20": "\u00e9 ",
	} {
		if got := pyUnquote(in); got != want {
			t.Errorf("unquote(%q) = %q, want %q", in, got, want)
		}
	}
	if m := imMessage.FindAllStringSubmatch(`<message/><messageX a="1">x</messageX><message a="b">c</message>`, -1); len(m) != 2 ||
		m[0][1] != "" || m[1][1] != ` a="b"` || m[1][2] != "c" {
		t.Errorf("message %q", m)
	}
	if m := imImg.FindStringSubmatch(`<img alt="" src="a.png">`); m == nil || m[1] != "a.png" {
		t.Errorf("img %q", m)
	}
	if imImg.MatchString(`<img xsrc="a.png">`) || imImg.MatchString(`<imgsrc="a.png">`) {
		t.Error("img without a boundary")
	}
}

// TestImlogsGroupingsKeepTheOwnersSplits: the old programs' groupings of handles into one person are
// the owner's decision of then; a handle the owner has placed since (split off in the app, how
// 'manual') stays where the owner put it when the logs are imported again.
func TestImlogsGroupingsKeepTheOwnersSplits(t *testing.T) {
	tmp := imSetup(t)
	adium, purple := imBuild(t, tmp)
	a := imOpen(t, filepath.Join(tmp, "archive.db"))
	opt := ImlogsOptions{Adium: adium, Pidgin: purple, NoMedia: true}
	if _, err := Imlogs(a, nil, opt); err != nil {
		t.Fatal(err)
	}
	third := a.Address(archive.H("email", "third@hotmail.com"))
	other := a.Address(archive.H("email", "other@hotmail.com"))
	person := func(aid int64) int64 { return a.Int("SELECT person_id FROM person_address WHERE address_id = ?", aid) }
	if person(third) != person(other) {
		t.Fatal("other and third not merged")
	}
	// the owner splits third off, as core.SplitAddress does
	a.Exec("INSERT INTO person DEFAULT VALUES")
	split := a.Int("SELECT max(id) FROM person")
	a.Exec("UPDATE person_address SET person_id = ?, how = 'manual' WHERE address_id = ?", split, third)
	a.Commit()
	again, err := Imlogs(a, nil, opt)
	if err != nil {
		t.Fatal(err)
	}
	if person(third) != split || person(other) == split {
		t.Errorf("the owner's split undone: third in %d, other in %d, split %d", person(third), person(other), split)
	}
	if len(again.Merged) != 0 {
		t.Errorf("merged again %v", again.Merged)
	}
}

// TestImlogsDryRunSeesTheWholeArchive: the dry run's copy of the archive holds what is committed
// and still in its write-ahead log (the server keeps it open), so what it says "new" is what a
// real run would add.
func TestImlogsDryRunSeesTheWholeArchive(t *testing.T) {
	tmp := imSetup(t)
	adium, purple := imBuild(t, tmp)
	path := filepath.Join(tmp, "archive.db")
	a := imOpen(t, path) // kept open: what it committed is in archive.db-wal
	if _, err := Imlogs(a, nil, ImlogsOptions{Adium: adium, Pidgin: purple, NoMedia: true}); err != nil {
		t.Fatal(err)
	}
	var lines []string
	ok, err := ImlogsImport(path, adium, purple, true, func(s string) { lines = append(lines, s) })
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	total := ""
	for _, l := range lines {
		if strings.HasPrefix(l, "total") || strings.HasPrefix(l, "σύνολο") {
			total = l
		}
	}
	if f := strings.Fields(total); len(f) < 4 || f[1] != "0" || f[3] != "12" { // the repeat of the picture too
		t.Errorf("dry run after a full import: %q, want 0 new, 12 already there", total)
	}
}

// TestImlogsGroupingsChain: groupings that share a handle make one person, in one run, also where
// the shared handle was moved by an earlier grouping of the same run (Adium's, then Pidgin's).
func TestImlogsGroupingsChain(t *testing.T) {
	tmp := imSetup(t)
	adium, purple := imBuild(t, tmp)
	imWrite(t, filepath.Join(purple, "blist.xml"), `<?xml version='1.0' encoding='UTF-8' ?>
<purple version='1.0'><blist><group name='Buddies'><contact>
<buddy account='me@hotmail.com' proto='prpl-msn'><name>other@hotmail.com</name></buddy>
<buddy account='me@hotmail.com' proto='prpl-msn'><name>friend@hotmail.com</name></buddy>
</contact></group></blist></purple>
`)
	a := imOpen(t, filepath.Join(tmp, "archive.db"))
	if _, err := Imlogs(a, nil, ImlogsOptions{Adium: adium, Pidgin: purple, NoMedia: true}); err != nil {
		t.Fatal(err)
	}
	person := func(h archive.Handle) int64 {
		return a.Int("SELECT person_id FROM person_address WHERE address_id = ?", a.Address(h))
	}
	friend, pal, other := person(archive.H("email", "friend@hotmail.com")), person(archive.H("id", "pal", "aim")),
		person(archive.H("email", "other@hotmail.com"))
	if friend != pal || pal != other {
		t.Errorf("friend %d, pal %d, other %d: want one person", friend, pal, other)
	}
}

// TestImlogsGroupingsAfterBothPrograms: an Adium grouping that names a handle only Pidgin's logs
// know is applied on the first run, not only on the next one: the groupings are applied once both
// programs' logs are in.
func TestImlogsGroupingsAfterBothPrograms(t *testing.T) {
	tmp := imSetup(t)
	adium, purple := imBuild(t, tmp)
	data, err := plist.Marshal(map[string]any{"MetaContact Ownership": map[string]any{
		"MetaContact-1": []any{map[string]any{"UID": "pal", "ServiceID": "AIM"},
			map[string]any{"UID": "third@hotmail.com", "ServiceID": "MSN"}}}}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	imWrite(t, filepath.Join(adium, "Users", "Default", "Contact List.plist"), string(data))
	a := imOpen(t, filepath.Join(tmp, "archive.db"))
	if _, err := Imlogs(a, nil, ImlogsOptions{Adium: adium, Pidgin: purple, NoMedia: true}); err != nil {
		t.Fatal(err)
	}
	person := func(h archive.Handle) int64 {
		return a.Int("SELECT person_id FROM person_address WHERE address_id = ?", a.Address(h))
	}
	pal, third, other := person(archive.H("id", "pal", "aim")), person(archive.H("email", "third@hotmail.com")),
		person(archive.H("email", "other@hotmail.com"))
	if pal != third || third != other { // Adium's grouping, and Pidgin's of other and third
		t.Errorf("pal %d, third %d, other %d: want one person", pal, third, other)
	}
}
