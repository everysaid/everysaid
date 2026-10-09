// The core's queries and changes on the demo archive (the Host's part is in internal/server's tests).
package core_test

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"everysaid/internal/all"
	"everysaid/internal/archive"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/demo"
	"everysaid/internal/errs"
	"everysaid/internal/text"
)

var _ = all.Loaded

// pristine is the demo archive every test copies (built once).
var pristine string

func TestMain(m *testing.M) {
	if os.Getenv("EVERYSAID_PARITY_CALLS") != "" { // the parity run uses its own archive
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "everysaid-test-")
	if err != nil {
		panic(err)
	}
	if err := demo.Env(dir); err != nil {
		panic(err)
	}
	stdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	path, err := demo.BuildArchive(7)
	os.Stdout = stdout
	if err != nil {
		panic(err)
	}
	pristine = path + ".pristine"
	copyFile(path, pristine)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func copyFile(from, to string) {
	in, err := os.Open(from)
	if err != nil {
		panic(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		panic(err)
	}
	defer out.Close()
	io.Copy(out, in)
}

// store is a fresh copy of the demo archive for one test.
func store(t *testing.T) *core.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.db")
	copyFile(pristine, path)
	s, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func chats(s *core.Store, set func(o *core.ChatsOptions)) []core.M {
	o := core.DefaultChatsOptions()
	if set != nil {
		set(&o)
	}
	return core.Chats(s, o)
}

func ids(list []core.M) map[string]bool {
	out := map[string]bool{}
	for _, c := range list {
		out[c["id"].(string)] = true
	}
	return out
}

func firstChat(t *testing.T, list []core.M, ok func(core.M) bool) core.M {
	t.Helper()
	for _, c := range list {
		if ok(c) {
			return c
		}
	}
	t.Fatal("no such chat")
	return nil
}

func isPerson(c core.M) bool { return c["type"] == "person" }

func services(c core.M) []string { return c["services"].([]string) }

func stream(t *testing.T, s *core.Store, id string, o core.StreamOptions) core.M {
	t.Helper()
	r, err := core.Stream(s, id, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func items(m core.M) []core.M { return m["items"].([]core.M) }

func search(t *testing.T, s *core.Store, q string, o core.SearchOptions) core.M {
	t.Helper()
	r, err := core.Search(s, q, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func total(m core.M) int64 {
	switch v := m["total"].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	}
	return -1
}

func i64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

func ptr[T any](v T) *T { return &v }

func setState(t *testing.T, s *core.Store, chat string, always bool, f map[string]any) {
	t.Helper()
	if err := core.SetChatState(s, chat, always, f); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, s *core.Store, fn func(tx *sql.Tx)) {
	t.Helper()
	s.MustWrite(fn)
}

func TestFold(t *testing.T) {
	if got := text.Fold("Καλημέρα ΦΊΛΟΣ"); got != "καλημερα φιλοσ" {
		t.Fatal(got)
	}
}

func TestChatsAndStreams(t *testing.T) {
	s := store(t)
	list := chats(s, nil)
	kinds := map[string]bool{}
	for _, c := range list {
		kinds[c["type"].(string)] = true
	}
	if !kinds["person"] || !kinds["group"] {
		t.Fatal(kinds)
	}
	sorted := append([]core.M{}, list...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i]["pinned"] != sorted[j]["pinned"] {
			return sorted[i]["pinned"].(bool)
		}
		return i64(sorted[i]["last_ts"]) > i64(sorted[j]["last_ts"])
	})
	if !reflect.DeepEqual(sorted, list) {
		t.Fatal("not in order")
	}
	person := firstChat(t, list, func(c core.M) bool { return isPerson(c) && len(services(c)) > 1 })
	page := items(stream(t, s, person["id"].(string), core.StreamOptions{Limit: 40}))
	if len(page) == 0 {
		t.Fatal("empty")
	}
	allowed := map[string]bool{"phone": true}
	for _, sv := range services(person) {
		allowed[sv] = true
	}
	for i, it := range page {
		if i > 0 && i64(it["ts"]) < i64(page[i-1]["ts"]) {
			t.Fatal("not oldest first")
		}
		if !allowed[it["service"].(string)] {
			t.Fatal("service", it["service"])
		}
	}
	seen := map[string]bool{}
	for _, it := range page {
		seen[it["cursor"].(string)] = true
	}
	older := items(stream(t, s, person["id"].(string), core.StreamOptions{Before: page[0]["cursor"].(string), Limit: 40}))
	for _, it := range older {
		if seen[it["cursor"].(string)] || i64(it["ts"]) > i64(page[0]["ts"]) {
			t.Fatal("older overlaps")
		}
	}
	newer := items(stream(t, s, person["id"].(string), core.StreamOptions{After: older[len(older)-1]["cursor"].(string), Limit: 10}))
	if newer[0]["cursor"] != page[0]["cursor"] {
		t.Fatal("newer does not follow")
	}
}

func TestAStreamWithoutSomeServices(t *testing.T) {
	s := store(t)
	person := firstChat(t, chats(s, nil), func(c core.M) bool { return isPerson(c) && len(services(c)) > 1 })
	id := person["id"].(string)
	off := services(person)[0]
	page := items(stream(t, s, id, core.StreamOptions{Limit: 200, Hidden: []string{off}}))
	if len(page) == 0 {
		t.Fatal("empty")
	}
	for _, it := range page {
		if it["service"] == off {
			t.Fatal("hidden service shown")
		}
	}
	for _, it := range items(stream(t, s, id, core.StreamOptions{Before: page[0]["cursor"].(string), Limit: 200, Hidden: []string{off}})) {
		if it["service"] == off {
			t.Fatal("hidden service shown in older")
		}
	}
	a := stream(t, s, id, core.StreamOptions{Limit: 200, Hidden: []string{"no-such-service"}})
	b := stream(t, s, id, core.StreamOptions{Limit: 200})
	if !reflect.DeepEqual(a, b) {
		t.Fatal("an unknown service changed the stream")
	}
}

func TestWalkWholeStreamOnce(t *testing.T) {
	s := store(t)
	chat := firstChat(t, chats(s, nil), func(c core.M) bool { return c["type"] == "group" })
	var seen []core.M
	cursor := ""
	for {
		page := stream(t, s, chat["id"].(string), core.StreamOptions{Before: cursor, Limit: 97})
		seen = append(items(page), seen...)
		if !page["has_older"].(bool) {
			break
		}
		cursor = items(page)[0]["cursor"].(string)
	}
	n := db.Int(s.Read(), "SELECT count(*) FROM message WHERE conversation_id = ?", chat["conversation_id"])
	unique := map[string]bool{}
	for _, it := range seen {
		unique[it["cursor"].(string)] = true
	}
	if int64(len(seen)) != n || len(unique) != len(seen) {
		t.Fatalf("seen %d unique %d of %d", len(seen), len(unique), n)
	}
}

func TestSearchIgnoresAccents(t *testing.T) {
	s := store(t)
	a := search(t, s, "καλημερα", core.SearchOptions{})
	b := search(t, s, "ΚΑΛΗΜΈΡΑ", core.SearchOptions{})
	if total(a) != total(b) || total(a) == 0 {
		t.Fatal(total(a), total(b))
	}
	first := items(a)[0]
	marked := false
	for _, h := range first["highlight"].([][2]any) {
		if h[1].(bool) {
			marked = true
		}
	}
	if !marked || first["chat_id"] == nil {
		t.Fatal("no mark or chat")
	}
}

func TestPersonAndNames(t *testing.T) {
	s := store(t)
	chat := firstChat(t, chats(s, nil), isPerson)
	pid := i64(chat["person_id"])
	p := core.Person(s, pid)
	if p["name"] != chat["title"] || len(p["handles"].([]core.M)) == 0 {
		t.Fatal(p["name"], chat["title"])
	}
	if err := core.SetPerson(s, pid, core.To("Κάποιος Άλλος"), core.Opt[string]{}, core.Opt[string]{}); err != nil {
		t.Fatal(err)
	}
	if core.Person(s, pid)["name"] != "Κάποιος Άλλος" {
		t.Fatal("name not set")
	}
}

func persons(list []core.M) []core.M {
	var out []core.M
	for _, c := range list {
		if isPerson(c) {
			out = append(out, c)
		}
	}
	return out
}

func TestMergeAndSplit(t *testing.T) {
	s := store(t)
	ps := persons(chats(s, nil))[:2]
	a, b := i64(ps[0]["person_id"]), i64(ps[1]["person_id"])
	before := len(chats(s, nil))
	if _, err := core.MergePeople(s, a, b); err != nil {
		t.Fatal(err)
	}
	after := chats(s, nil)
	if len(after) != before-1 {
		t.Fatal(len(after), before)
	}
	merged := firstChat(t, after, func(c core.M) bool { return c["id"] == fmt.Sprintf("p%d", a) })
	have := map[string]bool{}
	for _, sv := range services(merged) {
		have[sv] = true
	}
	for _, sv := range services(ps[1]) {
		if !have[sv] {
			t.Fatal("service lost", sv)
		}
	}
	hs := core.Person(s, a)["handles"].([]core.M)
	moved := i64(hs[len(hs)-1]["address_id"])
	setState(t, s, fmt.Sprintf("p%d", a), false, map[string]any{"archived": true})
	pid, err := core.SplitAddress(s, moved)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(chats(s, func(o *core.ChatsOptions) { o.IncludeArchived = true })); n != before {
		t.Fatal(n, before)
	}
	if !core.GetChat(s, fmt.Sprintf("p%d", pid))["archived"].(bool) { // split off an archived chat: archived too
		t.Fatal("split chat not archived")
	}
}

func TestChatStateAndUnread(t *testing.T) {
	s := store(t)
	chat := firstChat(t, chats(s, nil), func(c core.M) bool { return i64(c["unread"]) > 0 })
	id := chat["id"].(string)
	setState(t, s, id, false, map[string]any{"pinned": true, "read_until": "now"})
	top := chats(s, nil)[0]
	if top["id"] != id || !top["pinned"].(bool) || i64(top["unread"]) != 0 {
		t.Fatal(top["id"], id, top["pinned"], top["unread"])
	}
	setState(t, s, id, false, map[string]any{"archived": true})
	if ids(chats(s, nil))[id] {
		t.Fatal("archived chat listed")
	}
}

func TestMediaAndDecisions(t *testing.T) {
	s := store(t)
	r, _ := core.Media(s, core.MediaOptions{Kind: "image"})
	m := items(r)
	if len(m) == 0 || m[0]["available"] != "local" {
		t.Fatal("no local image")
	}
	if err := core.DecideMedia(s, m[0]["sha256"].(string), "keep", nil); err != nil {
		t.Fatal(err)
	}
	r, _ = core.Media(s, core.MediaOptions{Kind: "image"})
	if items(r)[0]["decision"] != "keep" {
		t.Fatal("decision not kept")
	}
}

func TestCallsStatsTimeline(t *testing.T) {
	s := store(t)
	r, _ := core.Calls(s, core.CallsOptions{Missed: true, Unnamed: true, Short: true})
	if len(items(r)) == 0 {
		t.Fatal("no missed calls")
	}
	st := core.Stats(s, false)
	if i64(st["messages"]) <= 1000 || i64(st["people"]) <= 10 {
		t.Fatal(st["messages"], st["people"])
	}
	tops := st["top_groups"].([]core.M)
	if len(tops) == 0 {
		t.Fatal("no top groups")
	}
	for _, g := range tops {
		if core.GetChat(s, g["chat_id"].(string))["type"] != "group" {
			t.Fatal("not a group")
		}
	}
	last := *st["last"].(*int64)
	if len(items(core.Timeline(s, last-86400_000, last+1))) == 0 {
		t.Fatal("empty timeline")
	}
}

func TestASearchOfDatesAloneGivesEverythingOfThoseDays(t *testing.T) {
	s := store(t)
	q := s.Read()
	ts := db.Int(q, "SELECT max(ts) FROM message")
	since, until := ts-30*86400_000, ts+1
	r := search(t, s, "", core.SearchOptions{Since: &since, Until: &until, Limit: 1000})
	nm := db.Int(q, "SELECT count(*) FROM message WHERE ts >= ? AND ts < ?", since, until)
	nc := db.Int(q, "SELECT count(*) FROM call WHERE ts >= ? AND ts < ?", since, until)
	list := items(r)
	if total(r) != nm+nc || int64(len(list)) != total(r) || nm == 0 || nc == 0 {
		t.Fatal(total(r), nm, nc, len(list))
	}
	callChat := ""
	for i, it := range list {
		if i > 0 && i64(it["ts"]) < i64(list[i-1]["ts"]) {
			t.Fatal("not oldest first")
		}
		if it["type"] == "call" {
			if it["chat_id"] == nil || it["chat_title"] == nil {
				t.Fatal("call without chat")
			}
			if callChat == "" {
				callChat = it["chat_id"].(string)
			}
		}
	}
	var sum int64
	for _, c := range r["chats"].([]core.M) {
		sum += i64(c["count"])
	}
	if sum > total(r) {
		t.Fatal(sum, total(r))
	}
	one := search(t, s, "", core.SearchOptions{ChatID: callChat, Since: &since, Until: &until, Limit: 1000})
	if len(items(one)) == 0 {
		t.Fatal("chat has nothing")
	}
	for _, it := range items(one) {
		if it["chat_id"] != callChat {
			t.Fatal("other chat")
		}
	}
	for _, it := range items(search(t, s, "", core.SearchOptions{Kind: "text", Since: &since, Until: &until})) {
		if it["type"] != "message" {
			t.Fatal("a call with a kind of message")
		}
	}
	if len(items(search(t, s, "", core.SearchOptions{}))) != 0 { // no words, no dates: nothing
		t.Fatal("something without words or dates")
	}
}

func TestContext(t *testing.T) {
	s := store(t)
	mid := db.Int(s.Read(), "SELECT id FROM message ORDER BY id LIMIT 1 OFFSET 500")
	ctx, err := core.Context(s, mid, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items(ctx) {
		if it["type"] == "message" && i64(it["id"]) == mid {
			return
		}
	}
	t.Fatal("the message is not in its context")
}

func TestNameOrder(t *testing.T) {
	s := store(t)
	var pid, phone, other int64
	write(t, s, func(tx *sql.Tx) {
		pid = db.LastID(tx, "INSERT INTO person DEFAULT VALUES")
		phone = db.LastID(tx, "INSERT INTO address (kind_id, value) VALUES ((SELECT id FROM address_kind WHERE name = 'phone'), '+15559990000')")
		other = db.LastID(tx, "INSERT INTO address (kind_id, value) VALUES ((SELECT id FROM address_kind WHERE name = 'phone'), '+15559990001')")
		for _, aid := range []int64{phone, other} {
			db.Exec(tx, "INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", aid, pid)
		}
		for _, x := range []struct {
			aid             int64
			svc, kind, name string
		}{{phone, "telegram", "profile", "Tg Name"}, {phone, "whatsapp", "profile", "Push"},
			{phone, "whatsapp", "book", "Book Name"}, {other, "whatsapp", "book", "Book Name"}} {
			db.Exec(tx, "INSERT INTO handle_name (address_id, service_id, kind, name, first_seen, last_seen) "+
				"VALUES (?, (SELECT id FROM service WHERE name = ?), ?, ?, 1, 1)", x.aid, x.svc, x.kind, x.name)
		}
	})
	info := func() [2]string {
		n, src := core.PeopleOf(s).Info(pid)
		return [2]string{n, src}
	}
	if got := info(); got != [2]string{"Book Name", "whatsapp/book"} { // by the plugins' weights
		t.Fatal(got)
	}
	ppl := core.PeopleOf(s)
	if ppl.SelfNamed(pid) {
		t.Fatal("self named")
	}
	aka := map[string]bool{}
	for _, a := range ppl.Aka(pid) {
		aka[a["name"].(string)] = true
	}
	if !reflect.DeepEqual(aka, map[string]bool{"Tg Name": true, "Push": true}) {
		t.Fatal(aka)
	}
	core.SetSetting(s, "name_order", []any{"telegram/profile"})
	if got := info(); got != [2]string{"Tg Name", "telegram/profile"} {
		t.Fatal(got)
	}
	if !core.PeopleOf(s).SelfNamed(pid) {
		t.Fatal("not self named")
	}
	none := core.Opt[string]{}
	core.SetPerson(s, pid, none, none, core.To("whatsapp/profile")) // pinned to one source
	if core.PeopleOf(s).Name(pid) != "Push" {
		t.Fatal(core.PeopleOf(s).Name(pid))
	}
	core.SetPerson(s, pid, none, none, core.To(fmt.Sprintf("address:%d", other))) // pinned to one handle
	if core.PeopleOf(s).Name(pid) != "Book Name" {
		t.Fatal(core.PeopleOf(s).Name(pid))
	}
	core.SetPerson(s, pid, core.To("Mine"), none, none)
	if got := info(); got != [2]string{"Mine", "user"} { // the user's own name comes first
		t.Fatal(got)
	}
}

func TestChatStateFromServicesAndTheUser(t *testing.T) {
	s := store(t)
	chat := firstChat(t, chats(s, nil), isPerson)
	id := chat["id"].(string)
	conv := core.GetChat(s, id)["conversations"].([]int64)[0]
	now := time.Now().UnixMilli()
	write(t, s, func(tx *sql.Tx) {
		iid := db.Int(tx, "SELECT id FROM plugin_instance WHERE plugin = 'whatsapp-bridge'")
		db.Exec(tx, "INSERT OR REPLACE INTO state_report VALUES (?, ?, 'muted', -1, ?, ?)", conv, iid, now, now)
	})
	muted := func() bool { return core.GetChat(s, id)["muted"].(bool) }
	if !muted() { // muted by a service
		t.Fatal("not muted by the service")
	}
	setState(t, s, id, false, map[string]any{"muted": false}) // the user's later choice wins
	if muted() {
		t.Fatal("the user's choice lost")
	}
	write(t, s, func(tx *sql.Tx) { // the service changes it again, later
		db.Exec(tx, "UPDATE state_report SET value = -1, changed_at = ?, observed_at = ? WHERE conversation_id = ?",
			now+10_000_000, now+10_000_000, conv)
	})
	if !muted() {
		t.Fatal("the service's later change lost")
	}
	setState(t, s, id, true, map[string]any{"muted": false}) // unless the user said always
	if muted() {
		t.Fatal("always lost")
	}
	setState(t, s, id, false, map[string]any{"muted": nil}) // follow the services again
	if !muted() {
		t.Fatal("not following the service")
	}
}

func TestArchivedIsTheAppsOwnAndAMergeKeepsInView(t *testing.T) {
	s := store(t)
	ps := persons(chats(s, nil))
	a, b := ps[0], ps[1]
	setState(t, s, a["id"].(string), false, map[string]any{"archived": true})
	if !core.GetChat(s, a["id"].(string))["archived"].(bool) || core.GetChat(s, b["id"].(string))["archived"].(bool) {
		t.Fatal("archived wrong")
	}
	core.MergePeople(s, i64(a["person_id"]), i64(b["person_id"])) // one in view: the whole person in view
	if core.GetChat(s, a["id"].(string))["archived"].(bool) {
		t.Fatal("merged chat archived")
	}
	ps = persons(chats(s, nil))
	c, d := ps[0], ps[1]
	for _, x := range []core.M{c, d} {
		setState(t, s, x["id"].(string), false, map[string]any{"archived": true})
	}
	core.MergePeople(s, i64(c["person_id"]), i64(d["person_id"])) // both archived: archived
	if !core.GetChat(s, c["id"].(string))["archived"].(bool) {
		t.Fatal("both archived, merged not")
	}
}

func TestSearchInsideWordsWholeWordsAndCase(t *testing.T) {
	s := store(t)
	n := func(q string, o core.SearchOptions) int64 { return total(search(t, s, q, o)) }
	if n("λημερ", core.SearchOptions{}) == 0 { // inside a word (Καλημέρα), by default
		t.Fatal("inside a word")
	}
	if n("λημερ", core.SearchOptions{Whole: true}) != 0 { // not a word of its own
		t.Fatal("whole")
	}
	if a, b := n("καλημερα", core.SearchOptions{Whole: true}), n("Καλημέρα", core.SearchOptions{Whole: true}); a != b || a == 0 {
		t.Fatal(a, b)
	}
	if n("ΚΑΛΗΜΈΡΑ", core.SearchOptions{Case: true}) != 0 || n("ΚΑΛΗΜΈΡΑ", core.SearchOptions{}) == 0 { // as typed, or not
		t.Fatal("case")
	}
	if n("Καλημέρα", core.SearchOptions{Case: true}) == 0 {
		t.Fatal("case as typed")
	}
	if n("να", core.SearchOptions{}) < 0 { // two letters: at the start of words
		t.Fatal("two letters")
	}
	hit := items(search(t, s, "λημερ", core.SearchOptions{Limit: 1}))[0]
	for _, h := range hit["highlight"].([][2]any) {
		if h[1].(bool) && strings.Contains(h[0].(string), "λημέρ") { // the part is marked
			return
		}
	}
	t.Fatal("the part is not marked")
}

func TestSearchSaysWhereItFoundAndFiltersByIt(t *testing.T) {
	s := store(t)
	for _, c := range []bool{false, true} {
		r := search(t, s, "Καλημέρα", core.SearchOptions{Case: c})
		where := r["chats"].([]core.M)
		var sum int64
		for _, x := range where {
			sum += i64(x["count"])
		}
		if len(where) == 0 || sum != total(r) {
			t.Fatal(len(where), sum, total(r))
		}
		top := where[0]
		only := search(t, s, "Καλημέρα", core.SearchOptions{ChatID: top["chat_id"].(string), Case: c})
		if total(only) != i64(top["count"]) {
			t.Fatal(total(only), top["count"])
		}
		for _, it := range items(only) {
			if it["chat_id"] != top["chat_id"] {
				t.Fatal("other chat")
			}
		}
		var a, b []any
		for _, x := range only["chats"].([]core.M) {
			a = append(a, x["chat_id"])
		}
		for _, x := range where {
			b = append(b, x["chat_id"])
		}
		if !reflect.DeepEqual(a, b) { // still every chat
			t.Fatal("chats differ")
		}
	}
}

func TestAnArchivedChatIsFoundByNameOnlyAmongTheArchived(t *testing.T) {
	s := store(t)
	chat := firstChat(t, chats(s, nil), isPerson)
	id := chat["id"].(string)
	setState(t, s, id, false, map[string]any{"archived": true})
	q := string([]rune(chat["title"].(string))[:4])
	if ids(chats(s, nil))[id] || ids(chats(s, func(o *core.ChatsOptions) { o.Q = q }))[id] {
		t.Fatal("archived chat listed")
	}
	found := chats(s, func(o *core.ChatsOptions) { o.Q = q; o.IncludeArchived = true })
	c := firstChat(t, found, func(c core.M) bool { return c["id"] == id })
	if !c["archived"].(bool) {
		t.Fatal("not archived")
	}
}

func TestASearchIsInTheArchivedChatsOrInTheOthers(t *testing.T) {
	s := store(t)
	every := search(t, s, "καλημερα", core.SearchOptions{Limit: 200})
	chat := every["chats"].([]core.M)[0]["chat_id"].(string)
	setState(t, s, chat, false, map[string]any{"archived": true})
	out := search(t, s, "καλημερα", core.SearchOptions{Archived: ptr(false), Limit: 200})
	inside := search(t, s, "καλημερα", core.SearchOptions{Archived: ptr(true), Limit: 200})
	if total(out)+total(inside) != total(every) || total(inside) == 0 {
		t.Fatal(total(out), total(inside), total(every))
	}
	for _, it := range items(inside) {
		if it["chat_id"] != chat {
			t.Fatal("other chat inside")
		}
	}
	for _, it := range items(out) {
		if it["chat_id"] == chat {
			t.Fatal("archived chat outside")
		}
	}
	if w := inside["chats"].([]core.M); len(w) != 1 || w[0]["chat_id"] != chat {
		t.Fatal("chats inside")
	}
	if total(search(t, s, "καλημερα", core.SearchOptions{ChatID: chat, Archived: ptr(false)})) != total(inside) { // asked for: searched
		t.Fatal("a chat asked for")
	}
	ts := i64(items(inside)[0]["ts"]) // dates alone too, calls with them
	since, until := ts-400*86400_000, ts+1
	day := func(a *bool) core.M {
		return search(t, s, "", core.SearchOptions{Since: &since, Until: &until, Limit: 1000, Archived: a})
	}
	a, b, c := day(nil), day(ptr(false)), day(ptr(true))
	if total(a) != total(b)+total(c) || total(c) == 0 {
		t.Fatal(total(a), total(b), total(c))
	}
	for _, it := range items(c) {
		if it["chat_id"] != chat {
			t.Fatal("other chat among the archived")
		}
	}
}

func TestANameIsFoundByPartsOfItsWords(t *testing.T) {
	s := store(t)
	chat := firstChat(t, chats(s, nil), func(c core.M) bool {
		ws := strings.Fields(c["title"].(string))
		if !isPerson(c) || len(ws) < 2 {
			return false
		}
		for _, w := range ws {
			if len([]rune(w)) <= 3 {
				return false
			}
		}
		return true
	})
	ws := strings.Fields(chat["title"].(string))
	first, last := []rune(ws[0]), []rune(ws[1])
	q := string(last[:len(last)-1]) + " " + strings.ToUpper(string(first[:len(first)-1])) // each word cut short, the other way round
	if !ids(chats(s, func(o *core.ChatsOptions) { o.Q = q }))[chat["id"].(string)] {
		t.Fatal("not found in chats")
	}
	found := false
	for _, p := range core.PeopleList(s, core.PeopleListOptions{Q: q, Unnamed: true, Short: true})["items"].([]core.M) {
		if i64(p["id"]) == i64(chat["person_id"]) {
			found = true
		}
	}
	if !found {
		t.Fatal("not found in people")
	}
}

func TestArchivedIsDecidedOnceWhenAChatIsFirstSeen(t *testing.T) {
	s := store(t)
	list := chats(s, nil)
	p := firstChat(t, list, func(c core.M) bool { return isPerson(c) && len(services(c)) > 1 })
	convs := core.GetChat(s, p["id"].(string))["conversations"].([]int64)
	g := firstChat(t, list, func(c core.M) bool { return c["type"] == "group" })
	seen := firstChat(t, list, func(c core.M) bool { return isPerson(c) && c["id"] != p["id"] })
	seenConv := core.GetChat(s, seen["id"].(string))["conversations"].([]int64)[0]
	a, err := archive.Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	// every chat of the demo was seen when it was made: one no source reported on, not archived
	if v, ok := a.IntOK("SELECT value FROM chat_state WHERE chat = ? AND field = 'archived'", seen["id"]); !ok || v != 0 {
		t.Fatal("seen chat not decided")
	}
	a.Exec("DELETE FROM chat_state WHERE chat IN (?, ?)", p["id"], g["id"]) // these two: new
	src := a.Int("SELECT id FROM source WHERE instance_id IS NOT NULL LIMIT 1")
	now := time.Now().UnixMilli()
	// a person reached on two services, one archiving their chat: they stay in view
	a.ReportState(src, convs[0], "archived", 1, now, 0)
	a.ReportState(src, convs[1], "archived", 0, now, 0)
	a.ReportState(src, i64(g["conversation_id"]), "archived", 1, now, 0)
	a.ReportState(src, seenConv, "archived", 1, now, 0) // seen before: no change
	a.InitArchived()
	a.Commit()
	a.Close()
	archived := func(id any) bool { return core.GetChat(s, id.(string))["archived"].(bool) }
	if archived(p["id"]) || !archived(g["id"]) || archived(seen["id"]) {
		t.Fatal(archived(p["id"]), archived(g["id"]), archived(seen["id"]))
	}
	setState(t, s, g["id"].(string), false, map[string]any{"archived": false}) // then ours alone
	a, _ = archive.Open(s.Path)
	a.ReportState(src, i64(g["conversation_id"]), "archived", 1, now+1000, 0)
	a.InitArchived()
	a.Commit()
	a.Close()
	if archived(g["id"]) {
		t.Fatal("a service changed the app's own archived")
	}
}

func TestStatsLeaveArchivedChatsOutUnlessAsked(t *testing.T) {
	s := store(t)
	every := core.Stats(s, true)
	tops := map[any]bool{}
	for _, p := range every["top_people"].([]core.M) {
		tops[p["chat_id"]] = true
	}
	chat := firstChat(t, chats(s, nil), func(c core.M) bool { return isPerson(c) && tops[c["id"]] })
	if !reflect.DeepEqual(core.Stats(s, false), every) { // nothing archived yet
		t.Fatal("stats differ with nothing archived")
	}
	setState(t, s, chat["id"].(string), false, map[string]any{"archived": true})
	shown := core.Stats(s, false)
	if i64(shown["people"]) != i64(every["people"])-1 || i64(shown["messages"]) >= i64(every["messages"]) {
		t.Fatal(shown["people"], every["people"])
	}
	for _, p := range shown["top_people"].([]core.M) {
		if p["chat_id"] == chat["id"] {
			t.Fatal("archived chat among the top")
		}
	}
	if !reflect.DeepEqual(core.Stats(s, true), every) {
		t.Fatal("stats with the archived changed")
	}
}

func groups(list []core.M) []core.M {
	var out []core.M
	for _, c := range list {
		if c["type"] == "group" {
			out = append(out, c)
		}
	}
	return out
}

func TestGroupsMergeIntoOneChatAndSplitAgain(t *testing.T) {
	s := store(t)
	gs := groups(chats(s, func(o *core.ChatsOptions) { o.IncludeArchived = true }))[:3]
	a, b, c := gs[0]["id"].(string), gs[1]["id"].(string), gs[2]["id"].(string)
	before := map[string]core.M{}
	for _, g := range gs {
		before[g["id"].(string)] = core.GetChat(s, g["id"].(string))
	}
	setState(t, s, b, false, map[string]any{"archived": true, "pinned": true})
	if into, err := core.MergeGroups(s, a, b); err != nil || into != a {
		t.Fatal(into, err)
	}
	merged := core.GetChat(s, a)
	if core.GetChat(s, b) != nil {
		t.Fatal("the other group is still a chat")
	}
	want := append(append([]int64{}, before[a]["conversations"].([]int64)...), before[b]["conversations"].([]int64)...)
	got := append([]int64{}, merged["conversations"].([]int64)...)
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if !reflect.DeepEqual(want, got) {
		t.Fatal(want, got)
	}
	inGroups := map[int64]bool{}
	for _, g := range merged["groups"].([]core.M) {
		inGroups[i64(g["conversation_id"])] = true
	}
	if len(inGroups) != len(got) {
		t.Fatal("groups of the chat")
	}
	if !merged["pinned"].(bool) || merged["archived"].(bool) { // its choices kept; archived only if both were
		t.Fatal(merged["pinned"], merged["archived"])
	}
	latest := before[a]
	if i64(before[b]["last_ts"]) > i64(latest["last_ts"]) {
		latest = before[b]
	}
	if merged["title"] != latest["title"] { // the latest one's name
		t.Fatal(merged["title"], latest["title"])
	}
	names := map[any]bool{}
	for _, x := range []string{a, b} {
		for _, m := range before[x]["members"].([]core.M) {
			names[m["name"]] = true
		}
	}
	mnames := map[any]bool{}
	for _, m := range merged["members"].([]core.M) {
		mnames[m["name"]] = true
	}
	if !reflect.DeepEqual(names, mnames) {
		t.Fatal("members")
	}
	convs := map[int64]bool{}
	for _, it := range items(stream(t, s, a, core.StreamOptions{Limit: 1000})) {
		if it["type"] == "message" {
			convs[i64(it["conversation_id"])] = true
		}
	}
	if len(convs) != len(got) {
		t.Fatal("stream of the merged chat")
	}
	core.MergeGroups(s, a, c) // a third
	if n := len(core.GetChat(s, a)["groups"].([]core.M)); n != 3 {
		t.Fatal(n)
	}
	if _, err := core.MergeGroups(s, a, a); err == nil {
		t.Fatal("merged with itself")
	} else if _, ok := err.(*errs.UserError); !ok {
		t.Fatal(err)
	}
	// the one whose id the chat has leaves: the others keep the chat, under another id
	var head int64
	fmt.Sscanf(a[1:], "%d", &head)
	rest, err := core.SplitGroup(s, a, head)
	if err != nil || rest == a || !core.GetChat(s, rest)["pinned"].(bool) {
		t.Fatal(rest, err)
	}
	if !reflect.DeepEqual(core.GetChat(s, a)["conversations"], before[a]["conversations"]) {
		t.Fatal("the head's chat")
	}
	for _, g := range core.GetChat(s, rest)["groups"].([]core.M) {
		rest, _ = core.SplitGroup(s, rest, i64(g["conversation_id"])) // (its id changes with its head)
	}
	have := ids(groups(chats(s, func(o *core.ChatsOptions) { o.IncludeArchived = true })))
	for _, x := range []string{a, b, c} {
		if !have[x] {
			t.Fatal("group lost", x)
		}
	}
}

func TestGroupsAlikeAreSuggestedUntilTurnedDown(t *testing.T) {
	s := store(t)
	gs := groups(chats(s, func(o *core.ChatsOptions) { o.IncludeArchived = true }))[:2]
	a, b := gs[0], gs[1]
	ca, cb := i64(core.GetChat(s, a["id"].(string))["conversation_id"]), i64(core.GetChat(s, b["id"].(string))["conversation_id"])
	arch, err := archive.Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	// b gets a's members: the same people, on another group
	arch.Exec("INSERT OR IGNORE INTO conversation_member SELECT ?, address_id FROM conversation_member WHERE conversation_id = ?", cb, ca)
	arch.Exec("DELETE FROM conversation_member WHERE conversation_id = ? AND address_id NOT IN "+
		"(SELECT address_id FROM conversation_member WHERE conversation_id = ?)", cb, ca)
	arch.Commit()
	arch.Close()
	pair := func(sg core.M) bool {
		x := map[any]bool{}
		for _, c := range sg["chats"].([]core.M) {
			x[c["chat_id"]] = true
		}
		return len(x) == 2 && x[a["id"]] && x[b["id"]]
	}
	found := false
	for _, sg := range core.GroupSuggestions(s, a["id"].(string), 0) {
		for _, w := range sg["why"].([]string) {
			if w == "members" && pair(sg) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("not suggested")
	}
	core.DismissGroupMerge(s, []string{a["id"].(string), b["id"].(string)})
	for _, sg := range core.GroupSuggestions(s, "", 0) {
		if pair(sg) {
			t.Fatal("suggested after turned down")
		}
	}
}

func titles(list []core.M) map[string]bool {
	out := map[string]bool{}
	for _, c := range list {
		out[c["title"].(string)] = true
	}
	return out
}

func TestPeopleWithoutAName(t *testing.T) {
	s := store(t)
	every := titles(chats(s, nil))
	named := titles(chats(s, func(o *core.ChatsOptions) { o.Unnamed = false }))
	gone := map[string]bool{}
	for x := range every {
		if !named[x] {
			gone[x] = true
		}
	}
	// the numbers no source names: gone, but for the one with an unread message; those with calls
	// only are no chats at all (the calls have their page)
	if !reflect.DeepEqual(gone, map[string]bool{"+1 555-010-0010": true, "katerina.oikonomou@example.com": true}) {
		t.Fatal(gone)
	}
	if every["+1 555-010-0000"] || every["+1 555-010-0002"] || !named["+1 555-010-0011"] {
		t.Fatal("unnamed people")
	}
	only := chats(s, func(o *core.ChatsOptions) { o.Q = "555-010-0010"; o.Unnamed = false })
	if len(only) != 1 || only[0]["title"] != "+1 555-010-0010" {
		t.Fatal("asked for by number")
	}
	all, _ := core.Calls(s, core.CallsOptions{Limit: 10000, Unnamed: true, Short: true})
	some, _ := core.Calls(s, core.CallsOptions{Limit: 10000, Unnamed: false, Short: true})
	if len(items(all))-len(items(some)) != 6 { // five of theirs and the hidden number's
		t.Fatal(len(items(all)), len(items(some)))
	}
	for _, c := range items(some) {
		if c["chat_id"] == nil {
			t.Fatal("call without a chat")
		}
	}
	pid := i64(core.PeopleList(s, core.PeopleListOptions{Q: "555-010-0002", Unnamed: true, Short: true})["items"].([]core.M)[0]["id"])
	one, _ := core.Calls(s, core.CallsOptions{ChatID: fmt.Sprintf("p%d", pid), Unnamed: false, Short: true})
	if len(items(one)) != 2 { // one chat's: all
		t.Fatal(len(items(one)))
	}
	for _, p := range core.PeopleList(s, core.PeopleListOptions{Limit: 1000, Unnamed: false, Short: true})["items"].([]core.M) {
		if strings.HasPrefix(p["name"].(string), "+1 555-010-") {
			t.Fatal("unnamed person listed", p["name"])
		}
	}
	if n := core.PeopleList(s, core.PeopleListOptions{Q: "555-010-0003", Unnamed: false, Short: true})["total"]; n != 1 {
		t.Fatal(n)
	}
}

func TestMergeSuggestionsAndNotTheSame(t *testing.T) {
	s := store(t)
	found := map[string][]string{}
	for _, sg := range core.MergeSuggestions(s, 0, false) {
		var names []string
		for _, p := range sg["people"].([]core.M) {
			names = append(names, p["name"].(string))
		}
		sort.Strings(names)
		found[strings.Join(names, "|")] = sg["why"].([]string)
	}
	if !reflect.DeepEqual(found["Eleni Ioannou|Ελένη Ιωάννου"], []string{"similar"}) {
		t.Fatal(found)
	}
	if !reflect.DeepEqual(found["Νίκος Γεωργίου|Νίκος Γεωργίου"], []string{"book"}) {
		t.Fatal(found)
	}
	for _, sg := range core.MergeSuggestions(s, 0, false) {
		if reflect.DeepEqual(sg["why"], []string{"similar"}) {
			var pids []int64
			for _, p := range sg["people"].([]core.M) {
				pids = append(pids, i64(p["id"]))
			}
			core.DismissMerge(s, pids)
			break
		}
	}
	for _, sg := range core.MergeSuggestions(s, 0, false) {
		if reflect.DeepEqual(sg["why"], []string{"similar"}) {
			t.Fatal("suggested after turned down")
		}
	}
}

func TestManyMergeDecisionsAtOnce(t *testing.T) {
	s := store(t)
	found := map[string][]int64{}
	for _, sg := range core.MergeSuggestions(s, 0, false) {
		var pids []int64
		for _, p := range sg["people"].([]core.M) {
			pids = append(pids, i64(p["id"]))
		}
		found[sg["why"].([]string)[0]] = pids
	}
	book, similar := found["book"], found["similar"]
	// the book pair merged; the similar pair apart, one of them named through the merged person
	m, a, err := core.ApplyMerges(s, [][]int64{book}, [][2]int64{{similar[0], similar[1]}, {book[1], similar[0]}})
	if err != nil || m != 1 || a != 2 {
		t.Fatal(m, a, err)
	}
	if core.Person(s, book[1]) != nil {
		t.Fatal("merged person still there")
	}
	if len(core.MergeSuggestions(s, 0, false)) != 0 {
		t.Fatal("suggestions left")
	}
	pairs := map[[2]int64]bool{}
	for _, x := range core.MergesDismissed(s) {
		pairs[[2]int64{i64(x["a"].(core.M)["id"]), i64(x["b"].(core.M)["id"])}] = true
	}
	sortPair := func(x, y int64) [2]int64 { return [2]int64{min(x, y), max(x, y)} }
	if !reflect.DeepEqual(pairs, map[[2]int64]bool{sortPair(similar[0], similar[1]): true, sortPair(book[0], similar[0]): true}) {
		t.Fatal(pairs)
	}
	core.UndismissMerge(s, similar[0], similar[1]) // turned down by mistake
	left := core.MergeSuggestions(s, 0, false)
	if len(left) != 1 || !reflect.DeepEqual(left[0]["why"], []string{"similar"}) {
		t.Fatal(left)
	}
	if len(core.MergeSuggestions(s, 0, true)[0]["people"].([]core.M)[0]["recent"].([]core.M)) == 0 {
		t.Fatal("no recent")
	}
}

func TestNothingInItIsNotListed(t *testing.T) {
	s := store(t)
	var pid int64
	write(t, s, func(tx *sql.Tx) { // what a source may leave: an empty chat, a handle of no one
		sid := db.Int(tx, "SELECT id FROM service WHERE name = 'whatsapp'")
		kid := db.Int(tx, "SELECT id FROM address_kind WHERE name = 'id'")
		db.Exec(tx, "INSERT INTO conversation (service_id, key, title, is_group) VALUES (?, 'empty', 'Empty chat', 1)", sid)
		aid := db.LastID(tx, "INSERT INTO address (kind_id, value, service_id) VALUES (?, 'nobody', ?)", kid, sid)
		pid = db.LastID(tx, "INSERT INTO person DEFAULT VALUES")
		db.Exec(tx, "INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", aid, pid)
	})
	if titles(chats(s, nil))["Empty chat"] {
		t.Fatal("empty chat listed")
	}
	for _, p := range core.PeopleList(s, core.PeopleListOptions{Limit: 1000, Q: "nobody", Unnamed: true, Short: true})["items"].([]core.M) {
		if i64(p["id"]) == pid {
			t.Fatal("person of nothing listed")
		}
	}
}

func TestPeopleWithoutANameToName(t *testing.T) {
	s := store(t)
	r := core.UnnamedPeople(s, 100, 0, nil)
	list := r["items"].([]core.M)
	names := map[any]bool{}
	var sizes []int64
	for _, p := range list {
		names[p["name"]] = true
		sizes = append(sizes, i64(p["stats"].(core.M)["messages"]))
		if _, ok := p["recent"]; !ok {
			t.Fatal("no recent")
		}
	}
	if r["total"] != len(list) || !names["+1 555-010-0010"] {
		t.Fatal(r["total"], len(list))
	}
	if !sort.SliceIsSorted(sizes, func(i, j int) bool { return sizes[i] > sizes[j] }) {
		t.Fatal("not the most first")
	}
	first := i64(list[0]["id"])
	core.SetPerson(s, first, core.To("The courier"), core.Opt[string]{}, core.Opt[string]{})
	for _, p := range core.UnnamedPeople(s, 100, 0, nil)["items"].([]core.M) {
		if i64(p["id"]) == first {
			t.Fatal("named person still unnamed")
		}
	}
}

func TestTheChatListFiltered(t *testing.T) {
	s := store(t)
	every := chats(s, nil)
	sizes := map[int64]int64{}
	db.Each(s.Read(), "SELECT conversation_id, count(*) FROM message GROUP BY conversation_id", nil, func(scan func(...any)) {
		var c, n int64
		scan(&c, &n)
		sizes[c] = n
	})
	ix := core.Index(s)
	many := chats(s, func(o *core.ChatsOptions) { o.MinMessages = 100 })
	if len(many) == 0 || len(many) >= len(every) {
		t.Fatal(len(many), len(every))
	}
	for _, x := range many {
		var n int64
		for _, c := range ix.Chats[x["id"].(string)].Conversations {
			n += sizes[c]
		}
		if n < 100 {
			t.Fatal("too few", n)
		}
	}
	few := chats(s, func(o *core.ChatsOptions) { o.MaxMessages, o.HasMax = 99, true })
	union := ids(few)
	for k := range ids(many) {
		if union[k] {
			t.Fatal("in both")
		}
		union[k] = true
	}
	if !reflect.DeepEqual(union, ids(every)) {
		t.Fatal("few and many are not every chat")
	}
	wa := chats(s, func(o *core.ChatsOptions) { o.WithServices = []string{"whatsapp"} })
	if len(wa) == 0 {
		t.Fatal("no whatsapp chats")
	}
	for _, x := range wa {
		has := false
		for _, sv := range services(x) {
			has = has || sv == "whatsapp"
		}
		if !has {
			t.Fatal("not whatsapp")
		}
	}
	noWA := chats(s, func(o *core.ChatsOptions) { o.WithoutServices = []string{"whatsapp"} })
	both := ids(wa)
	for k := range ids(noWA) {
		both[k] = true
	}
	if !reflect.DeepEqual(both, ids(every)) {
		t.Fatal("with and without are not every chat")
	}
	one := every[0]
	if one["person_id"] != nil {
		only := chats(s, func(o *core.ChatsOptions) { o.PeopleOnly = map[int64]bool{i64(one["person_id"]): true} })
		if len(only) != 1 || only[0]["id"] != one["id"] {
			t.Fatal("people only")
		}
	}
}

func TestHiddenServicesShowNowhere(t *testing.T) {
	s := store(t)
	before := chats(s, nil)
	has := func(c core.M) bool {
		for _, sv := range services(c) {
			if sv == "whatsapp" {
				return true
			}
		}
		return false
	}
	firstChat(t, before, has)
	if total(search(t, s, "καλημερα", core.SearchOptions{Service: "whatsapp"})) == 0 {
		t.Fatal("nothing on whatsapp")
	}
	core.SetSetting(s, "hidden_services", []any{"whatsapp"})
	after := chats(s, nil)
	if len(after) == 0 {
		t.Fatal("no chats")
	}
	for _, c := range after {
		if has(c) {
			t.Fatal("hidden service in chats")
		}
	}
	if total(search(t, s, "καλημερα", core.SearchOptions{Service: "whatsapp"})) != 0 || total(search(t, s, "καλημερα", core.SearchOptions{})) == 0 {
		t.Fatal("search")
	}
	cs, _ := core.Calls(s, core.CallsOptions{Limit: 1000, Unnamed: true, Short: true})
	for _, c := range items(cs) {
		if c["service"] == "whatsapp" {
			t.Fatal("hidden service in calls")
		}
	}
	for _, c := range after[:min(10, len(after))] {
		for _, it := range items(stream(t, s, c["id"].(string), core.StreamOptions{})) {
			if it["service"] == "whatsapp" {
				t.Fatal("hidden service in a stream")
			}
		}
	}
	core.SetSetting(s, "hidden_services", []any{})
	if len(chats(s, nil)) != len(before) {
		t.Fatal("chats not back")
	}
}

// Viber's notes and a Telegram chat with oneself (no one else in either, ever) are one chat; a
// group everyone else left is not notes (others wrote in it).
func TestNotesToSelfAreOneChatAcrossServices(t *testing.T) {
	s := store(t)
	ix := core.Index(s)
	var notes *core.Chat
	for _, id := range ix.Order {
		if ix.Chats[id].Type == "conversation" {
			notes = ix.Chats[id]
			break
		}
	}
	if notes == nil {
		t.Fatal("no notes")
	}
	var cid, wid, nobody int64
	write(t, s, func(tx *sql.Tx) {
		tg := db.Int(tx, "SELECT id FROM service WHERE name = 'telegram'")
		own := db.Int(tx, "SELECT address_id FROM account LIMIT 1")
		cid = db.LastID(tx, "INSERT INTO conversation (service_id, key, is_group) VALUES (?, 'saved', 0)", tg)
		db.Exec(tx, "INSERT INTO conversation_member VALUES (?, ?)", cid, own)
		kind := db.Int(tx, "SELECT id FROM message_kind WHERE name = 'text'")
		db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, kind_id, text) VALUES (?, ?, 1700000000000, 1, ?, 'a note')", tg, cid, kind)
		// WhatsApp's chat with oneself, one message of it given as received from the owner (another device)
		wa := db.Int(tx, "SELECT id FROM service WHERE name = 'whatsapp'")
		wid = db.LastID(tx, "INSERT INTO conversation (service_id, key, is_group) VALUES (?, 'self', 0)", wa)
		db.Exec(tx, "INSERT INTO conversation_member VALUES (?, ?)", wid, own)
		db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) VALUES (?, ?, 1700000001000, 0, ?, ?, 'from my other phone')", wa, wid, own, kind)
		// and a notice of the service in it, given as received from its member (the owner)
		system := db.Int(tx, "SELECT id FROM message_kind WHERE name = 'system'")
		db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) VALUES (?, ?, 1700000002000, 0, ?, ?, 'end-to-end encrypted')", wa, wid, own, system)
		// a chat whose source listed no one, only the owner writing in it: not notes
		nobody = db.LastID(tx, "INSERT INTO conversation (service_id, key, is_group) VALUES (?, 'someone', 0)", tg)
		db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, kind_id, text) VALUES (?, ?, 1700000003000, 1, ?, 'hello?')", tg, nobody, kind)
	})
	ix = core.Index(s)
	if ix.ConvChat[cid] != notes.ID || ix.ConvChat[wid] != notes.ID || ix.ConvChat[nobody] == notes.ID {
		t.Fatal(ix.ConvChat[cid], ix.ConvChat[wid], ix.ConvChat[nobody], notes.ID)
	}
	one := ix.Chats[notes.ID]
	if !one.Services["viber"] || !one.Services["telegram"] {
		t.Fatal(one.Services)
	}
	if title := core.ChatTitle(s, one); title != "Notes" && title != "Σημειώσεις" {
		t.Fatal(title)
	}
	for _, it := range items(stream(t, s, notes.ID, core.StreamOptions{Limit: 200})) {
		if it["text"] == "a note" {
			return
		}
	}
	t.Fatal("the note is not in notes")
}

func TestHiddenAccountsHideTheChatsOnlyOnThem(t *testing.T) {
	s := store(t)
	var mine, other int64
	write(t, s, func(tx *sql.Tx) {
		mine = db.Int(tx, "SELECT address_id FROM account LIMIT 1")
		other = db.LastID(tx, "INSERT INTO address (kind_id, value) VALUES ((SELECT id FROM address_kind WHERE name = 'email'), 'me2@example.com')")
		db.Exec(tx, "INSERT INTO account (address_id) VALUES (?)", other)
	})
	gs := groups(chats(s, nil))
	only, both := i64(gs[0]["conversation_id"]), i64(gs[1]["conversation_id"])
	write(t, s, func(tx *sql.Tx) {
		db.Exec(tx, "INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", only, other)
		db.Exec(tx, "INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", both, other)
		db.Exec(tx, "INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", both, mine)
		db.Exec(tx, "DELETE FROM conversation_member WHERE conversation_id = ? AND address_id = ?", only, mine)
	})
	core.SetSetting(s, "hidden_accounts", []any{other})
	have := ids(chats(s, nil))
	if have[gs[0]["id"].(string)] || !have[gs[1]["id"].(string)] {
		t.Fatal("hidden accounts")
	}
	if total(search(t, s, "καλημερα", core.SearchOptions{ChatID: gs[1]["id"].(string)})) < 0 {
		t.Fatal("search")
	}
	core.SetSetting(s, "hidden_accounts", []any{})
	if !ids(chats(s, nil))[gs[0]["id"].(string)] {
		t.Fatal("not back")
	}
}

func TestGroupsWithNoOneElseHiddenWhenAsked(t *testing.T) {
	s := store(t)
	gs := groups(chats(s, nil))
	g := gs[0]
	write(t, s, func(tx *sql.Tx) {
		db.Exec(tx, "DELETE FROM conversation_member WHERE conversation_id = ? AND address_id NOT IN (SELECT address_id FROM account)", g["conversation_id"])
		db.Exec(tx, "INSERT OR REPLACE INTO chat_state VALUES (?, 'read_until', ?, 0, 1)", g["id"], int64(1)<<50)
	})
	id := g["id"].(string)
	if !ids(chats(s, nil))[id] {
		t.Fatal("not listed by default")
	}
	hidden := ids(chats(s, func(o *core.ChatsOptions) { o.EmptyGroups = false }))
	if hidden[id] {
		t.Fatal("listed")
	}
	for _, c := range gs[1:] {
		if !hidden[c["id"].(string)] {
			t.Fatal("another group hidden")
		}
	}
	if title, _ := g["title"].(string); title != "" {
		if !ids(chats(s, func(o *core.ChatsOptions) { o.Q = title; o.EmptyGroups = false }))[id] {
			t.Fatal("asked for by name")
		}
	}
}

func TestShortNumbersHiddenWhenAsked(t *testing.T) {
	s := store(t)
	var pid int64
	write(t, s, func(tx *sql.Tx) {
		aid := db.LastID(tx, "INSERT INTO address (kind_id, value) VALUES ((SELECT id FROM address_kind WHERE name = 'phone'), '13800')")
		pid = db.LastID(tx, "INSERT INTO person DEFAULT VALUES")
		db.Exec(tx, "INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", aid, pid)
		conv := db.LastID(tx, "INSERT INTO conversation (service_id, key) VALUES ((SELECT id FROM service WHERE name = 'sms'), '13800')")
		db.Exec(tx, "INSERT INTO conversation_member VALUES (?, ?)", conv, aid)
		db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) VALUES "+
			"((SELECT id FROM service WHERE name = 'sms'), ?, 1700000000000, 0, ?, (SELECT id FROM message_kind WHERE name = 'text'), 'Your code is 1234')", conv, aid)
	})
	id := fmt.Sprintf("p%d", pid)
	if !ids(chats(s, nil))[id] || ids(chats(s, func(o *core.ChatsOptions) { o.Short = false }))[id] ||
		!ids(chats(s, func(o *core.ChatsOptions) { o.Q = "13800"; o.Short = false }))[id] {
		t.Fatal("short numbers")
	}
	for _, p := range core.PeopleList(s, core.PeopleListOptions{Limit: 5000, Unnamed: true, Short: false})["items"].([]core.M) {
		if i64(p["id"]) == pid {
			t.Fatal("short number listed")
		}
	}
}

func TestNamesThatSoundTheSame(t *testing.T) {
	same := [][2]string{{"Ελένη Ιωάννου", "Eleni Ioannou"}, {"Θανάσης Σπύρος", "Spyros Thanasis"},
		{"Ευάγγελος Φίλιππος", "evangelos filippos"}, {"Ντίνος Μπάμπης", "Dinos Babis"}}
	for _, p := range same {
		if core.Skeleton(p[0]) != core.Skeleton(p[1]) {
			t.Fatal(p, core.Skeleton(p[0]), core.Skeleton(p[1]))
		}
	}
	if core.Skeleton("Ελένη Ιωάννου") == core.Skeleton("Olivia Ιωάννου") {
		t.Fatal("different names sound the same")
	}
}

// A message deleted for everyone is kept, but not offered: not found by search, not among the media.
func TestDeletedForEveryoneIsNotOffered(t *testing.T) {
	s := store(t)
	found := items(search(t, s, "καλημερα", core.SearchOptions{}))
	before := total(search(t, s, "καλημερα", core.SearchOptions{}))
	media, _ := core.Media(s, core.MediaOptions{Kind: "image", Limit: 100000})
	pics := media["items"].([]core.M)
	gone := i64(found[0]["id"])
	pic := i64(pics[0]["message_id"])
	if err := s.Write(func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE message SET deleted = 1 WHERE id IN (?, ?)", gone, pic)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if after := total(search(t, s, "καλημερα", core.SearchOptions{})); after != before-1 {
		t.Fatal(before, after)
	}
	media, _ = core.Media(s, core.MediaOptions{Kind: "image", Limit: 100000})
	for _, m := range media["items"].([]core.M) {
		if i64(m["message_id"]) == pic {
			t.Fatal("a deleted message's picture offered")
		}
	}
}

// The people without a name come after all those with one: their numbers ("+30…") and handles would
// otherwise sort first, among the names.
func TestPeopleWithoutANameLast(t *testing.T) {
	s := store(t)
	without := map[int64]bool{}
	for _, p := range core.UnnamedPeople(s, 100000, 0, nil)["items"].([]core.M) {
		without[i64(p["id"])] = true
	}
	list := core.PeopleList(s, core.PeopleListOptions{Unnamed: true, Short: true, Limit: 100000})["items"].([]core.M)
	seen := 0
	for _, p := range list {
		if without[i64(p["id"])] {
			seen++
		} else if seen > 0 {
			t.Fatalf("%v, with a name, after %d without one", p["name"], seen)
		}
	}
	if seen == 0 || seen == len(list) {
		t.Fatal("the demo should have both", seen, len(list))
	}
}
