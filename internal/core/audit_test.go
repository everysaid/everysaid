package core_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
)

// walk reads a whole stream page by page, older and older, then newer and newer from its oldest
// item: every cursor once each way, as the interface scrolls.
func walk(t *testing.T, s *core.Store, id string, limit int) (back, forth []string) {
	t.Helper()
	cursor := ""
	for i := 0; ; i++ {
		page := stream(t, s, id, core.StreamOptions{Before: cursor, Limit: limit})
		its := items(page)
		var got []string
		for _, it := range its {
			got = append(got, it["cursor"].(string))
		}
		back = append(got, back...)
		if !page["has_older"].(bool) || len(its) == 0 || i > 100000 {
			break
		}
		cursor = got[0]
	}
	if len(back) == 0 {
		return
	}
	forth = []string{back[0]}
	cursor = back[0]
	for i := 0; ; i++ {
		page := stream(t, s, id, core.StreamOptions{After: cursor, Limit: limit})
		its := items(page)
		for _, it := range its {
			forth = append(forth, it["cursor"].(string))
		}
		if !page["has_newer"].(bool) || len(its) == 0 || i > 100000 {
			break
		}
		cursor = forth[len(forth)-1]
	}
	return
}

// A message and a call at the same instant (a missed call and the carrier's text about it, two
// services at once) must neither hide each other nor end the paging early: a cursor of one kind
// bounded the other kind's rows by its own id, and rows the page then dropped took its places.
func TestAStreamWithCallsAndMessagesAtTheSameInstantIsWalkedWhole(t *testing.T) {
	s := store(t)
	ix := core.Index(s)
	var chat *core.Chat
	for _, id := range ix.Order {
		if c := ix.Chats[id]; c.Type == "person" && c.HasCalls && len(c.Conversations) > 0 {
			chat = c
			break
		}
	}
	if chat == nil {
		t.Fatal("no person with calls and messages")
	}
	addr := core.PeopleOf(s).Addresses(chat.PersonID)[0]
	conv := chat.Conversations[0]
	write(t, s, func(tx *sql.Tx) {
		var sid, kind int64
		var ts int64
		db.Row(tx, "SELECT service_id, kind_id, ts FROM message WHERE conversation_id = ? ORDER BY ts LIMIT 1 OFFSET 3",
			[]any{conv}, &sid, &kind, &ts)
		// ids of both kinds on either side of each other, at one instant
		for _, id := range []int64{900001, 900003, 900005} {
			db.Exec(tx, "INSERT INTO message (id, service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) "+
				"VALUES (?, ?, ?, ?, 0, ?, ?, 'x')", id, sid, conv, ts, addr, kind)
		}
		for _, id := range []int64{900000, 900002, 900004, 900006} {
			db.Exec(tx, "INSERT INTO call (id, service_id, address_id, ts, outgoing, answered, duration) "+
				"VALUES (?, (SELECT id FROM service WHERE name = 'phone'), ?, ?, 0, 0, 0)", id, addr, ts)
		}
	})
	want := db.Int(s.Read(), "SELECT count(*) FROM message WHERE conversation_id IN ("+db.Marks(len(chat.Conversations))+")",
		db.Args(chat.Conversations)...)
	want += db.Int(s.Read(), "SELECT count(*) FROM call WHERE conversation_id IS NULL AND address_id IN ("+
		db.Marks(len(core.PeopleOf(s).Addresses(chat.PersonID)))+")", db.Args(core.PeopleOf(s).Addresses(chat.PersonID))...)
	for _, limit := range []int{1, 2, 3, 5, 60} {
		back, forth := walk(t, s, chat.ID, limit)
		for name, got := range map[string][]string{"older": back, "newer": forth} {
			seen := map[string]bool{}
			for _, c := range got {
				if seen[c] {
					t.Fatalf("limit %d, %s: %s twice", limit, name, c)
				}
				seen[c] = true
			}
			if int64(len(got)) != want {
				t.Fatalf("limit %d, %s: %d of %d", limit, name, len(got), want)
			}
		}
		if fmt.Sprint(back) != fmt.Sprint(forth) {
			t.Fatalf("limit %d: the two ways differ", limit)
		}
	}
}

// A year is the owner's: a message at half past midnight on New Year's Day in Athens (still the
// old year in UTC) counts in the new one, and one at ten in the evening of New Year's Eve in New
// York (already the new year in UTC) in the old one.
func TestStatsCountYearsInTheOwnersTimeZone(t *testing.T) {
	saved := config.Timezone
	defer func() { config.Timezone = saved }()
	for _, c := range []struct {
		zone, at  string
		year, not string
	}{
		{"Europe/Athens", "2020-12-31T22:30:00Z", "2021", "2020"},
		{"America/New_York", "2021-01-01T03:00:00Z", "2020", "2021"},
	} {
		loc, err := time.LoadLocation(c.zone)
		if err != nil {
			t.Fatal(err)
		}
		config.Timezone = loc
		s := store(t)
		chat := firstChat(t, chats(s, nil), isPerson)
		before := core.Stats(s, true)["by_year"].(map[string]int64)
		at, _ := time.Parse(time.RFC3339, c.at)
		write(t, s, func(tx *sql.Tx) {
			db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, kind_id, text) "+
				"SELECT service_id, conversation_id, ?, 1, kind_id, 'x' FROM message WHERE conversation_id = ? LIMIT 1",
				at.UnixMilli(), core.GetChat(s, chat["id"].(string))["conversations"].([]int64)[0])
		})
		after := core.Stats(s, true)["by_year"].(map[string]int64)
		if after[c.year] != before[c.year]+1 || after[c.not] != before[c.not] {
			t.Fatalf("%s: %s %d -> %d, %s %d -> %d", c.zone, c.year, before[c.year], after[c.year], c.not, before[c.not], after[c.not])
		}
	}
}

// Many requests at once, reading while others write (the server's way): no data race (go test
// -race), every answer whole, and the readers' connections kept between requests rather than
// closed and opened again for each (database/sql keeps two idle unless told).
func TestManyRequestsAtOnce(t *testing.T) {
	s := store(t)
	list := chats(s, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				c := list[(g*5+i)%len(list)]
				id := c["id"].(string)
				switch i % 5 {
				case 0:
					if len(core.Chats(s, core.DefaultChatsOptions())) == 0 {
						errs <- fmt.Errorf("no chats")
					}
				case 1:
					if _, err := core.Stream(s, id, core.StreamOptions{}); err != nil {
						errs <- err
					}
				case 2:
					if _, err := core.Search(s, "και", core.SearchOptions{}); err != nil {
						errs <- err
					}
				case 3:
					if err := core.SetChatState(s, id, false, map[string]any{"read_until": "now"}); err != nil {
						errs <- err
					}
				case 4:
					if core.GetChat(s, id) == nil {
						errs <- fmt.Errorf("no chat %s", id)
					}
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := s.Read().Stats().MaxIdleClosed; n > 0 {
		t.Fatalf("%d reading connections closed and opened again", n)
	}
}

// What many requests ask for at once is built once, not once by each; and what was built for an
// older version of the archive is let go, not kept for ever (keys that carry the time, a minute's,
// would otherwise pile up in a server that runs for months).
func TestACacheIsBuiltOnceAndLetGo(t *testing.T) {
	s := store(t)
	var builds atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			core.Cached(s, "slow", func() int { builds.Add(1); time.Sleep(50 * time.Millisecond); return 1 })
		}()
	}
	wg.Wait()
	if builds.Load() != 1 {
		t.Fatalf("built %d times", builds.Load())
	}
	for i := 0; i < 5; i++ {
		core.Cached(s, fmt.Sprintf("minute:%d", i), func() int { return i })
		if err := core.SetSetting(s, "x", i); err != nil {
			t.Fatal(err)
		}
	}
	core.Cached(s, "now", func() int { return 0 })
	if n := core.CacheSize(s); n != 1 {
		t.Fatalf("%d kept", n)
	}
}

// Pages that go on "before the last one's time" lose nothing: a page ends between two instants
// (several files of one message, calls of one moment, all on one page), and one read past many
// left out (files gone, when only those there are asked for) is not taken for the last. Before,
// a page was cut inside an instant, and more than three pages' worth of gone files in a row
// ended the list.
func TestPagesBeforeATimeLoseNothing(t *testing.T) {
	s := store(t)
	chat := firstChat(t, chats(s, nil), isPerson)
	conv := core.GetChat(s, chat["id"].(string))["conversations"].([]int64)[0]
	write(t, s, func(tx *sql.Tx) {
		var sid, src int64
		db.Row(tx, "SELECT service_id FROM message WHERE conversation_id = ? LIMIT 1", []any{conv}, &sid)
		src = db.Int(tx, "SELECT id FROM source LIMIT 1")
		img := db.Int(tx, "SELECT id FROM message_kind WHERE name = 'image'")
		top := db.Int(tx, "SELECT max(ts) FROM message")
		add := func(ts int64, files int, gone bool) {
			mid := db.LastID(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, kind_id) VALUES (?, ?, ?, 1, ?)",
				sid, conv, ts, img)
			for f := 0; f < files; f++ {
				sha := fmt.Sprintf("%064x", mid*100+int64(f))
				path := "media/none/" + sha
				if !gone { // one that is there: any file of the store
					path = db.Str(tx, "SELECT path FROM media LIMIT 1")
				}
				db.Exec(tx, "INSERT INTO media (sha256, size, path) VALUES (?, 1, ?)", sha, path)
				db.Exec(tx, "INSERT INTO attachment (message_id, sha256, source_id, source_path) VALUES (?, ?, ?, ?)",
					mid, sha, src, sha)
			}
		}
		add(top+1000, 5, false) // five files of one message
		for i := int64(0); i < 400; i++ {
			add(top+2000+i, 1, true) // newer than all, and gone
		}
		add(top+5000, 4, false)
		phone := db.Int(tx, "SELECT id FROM service WHERE name = 'phone'")
		addr := db.Int(tx, "SELECT address_id FROM call WHERE address_id IS NOT NULL LIMIT 1")
		for i := 0; i < 5; i++ { // five calls of one moment
			db.Exec(tx, "INSERT INTO call (service_id, address_id, ts, outgoing, answered, duration) VALUES (?, ?, ?, 0, 0, 0)",
				phone, addr, top+3000)
		}
	})
	for _, avail := range []bool{true, false} {
		want := map[string]bool{}
		r, _ := core.Media(s, core.MediaOptions{Limit: 100000, AvailableOnly: avail})
		for _, it := range items(r) {
			want[it["sha256"].(string)] = true
		}
		got := map[string]bool{}
		var before int64
		for i := 0; ; i++ {
			r, _ := core.Media(s, core.MediaOptions{Before: before, Limit: 3, AvailableOnly: avail})
			its := items(r)
			for _, it := range its {
				got[it["sha256"].(string)] = true
			}
			if !r["has_more"].(bool) || len(its) == 0 || i > 100000 {
				break
			}
			before = i64(its[len(its)-1]["ts"])
		}
		if len(got) != len(want) || len(want) < 9 {
			t.Fatalf("available only %v: %d of %d files", avail, len(got), len(want))
		}
	}
	all := db.Int(s.Read(), "SELECT count(*) FROM call")
	seen := map[int64]bool{}
	var before int64
	for i := 0; ; i++ {
		r, err := core.Calls(s, core.CallsOptions{Before: before, Limit: 2, Unnamed: true, Short: true})
		if err != nil {
			t.Fatal(err)
		}
		its := items(r)
		for _, it := range its {
			seen[i64(it["id"])] = true
		}
		if !r["has_more"].(bool) || len(its) == 0 || i > 100000 {
			break
		}
		before = i64(its[len(its)-1]["ts"])
	}
	if int64(len(seen)) != all {
		t.Fatalf("%d of %d calls", len(seen), all)
	}
}

// Statistics of an archive whose messages are all of one year, or that has none.
func TestStatsOfOneYearAndOfNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Commit()
	a.DB.Close()
	s, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := i64(core.Stats(s, true)["messages"]); n != 0 {
		t.Fatal(n)
	}
	write(t, s, func(tx *sql.Tx) {
		db.Exec(tx, "INSERT INTO conversation (service_id, key) VALUES (1, 'x')")
		db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, kind_id) VALUES (1, 1, 1600000000000, 1, 1)")
	})
	st := core.Stats(s, true)
	if len(st["by_year"].(map[string]int64)) != 1 {
		t.Fatal(st["by_year"])
	}
}

// A group call is the group's, as messengers show it: in the group's stream (walked whole, at
// the same instant as a message too), in that chat's calls and said to be its; not in the calls
// of the one who started it.
func TestAGroupCallIsInItsGroup(t *testing.T) {
	s := store(t)
	group := firstChat(t, chats(s, nil), func(c core.M) bool { return c["type"] == "group" })
	id := group["id"].(string)
	convs := core.GetChat(s, id)["conversations"].([]int64)
	var caller int64
	write(t, s, func(tx *sql.Tx) {
		var sid, ts int64
		db.Row(tx, "SELECT service_id, ts, sender_id FROM message WHERE conversation_id = ? AND sender_id IS NOT NULL "+
			"ORDER BY ts LIMIT 1 OFFSET 5", []any{convs[0]}, &sid, &ts, &caller)
		for i, at := range []int64{ts, ts, ts + 1} {
			db.Exec(tx, "INSERT INTO call (id, service_id, address_id, ts, outgoing, answered, duration, conversation_id) "+
				"VALUES (?, ?, ?, ?, 0, 1, 60, ?)", 800000+i, sid, caller, at, convs[0])
		}
	})
	want := db.Int(s.Read(), "SELECT count(*) FROM message WHERE conversation_id IN ("+db.Marks(len(convs))+")", db.Args(convs)...) + 3
	for _, limit := range []int{1, 2, 7, 60} {
		back, forth := walk(t, s, id, limit)
		calls := 0
		for _, c := range back {
			if strings.Contains(c, ":c:") {
				calls++
			}
		}
		if int64(len(back)) != want || int64(len(forth)) != want || calls != 3 {
			t.Fatalf("limit %d: %d and %d of %d, %d calls", limit, len(back), len(forth), want, calls)
		}
	}
	r, err := core.Calls(s, core.CallsOptions{ChatID: id, Unnamed: true, Short: true})
	if err != nil || len(items(r)) != 3 {
		t.Fatalf("%v %v", err, r)
	}
	for _, it := range items(r) {
		if it["chat_id"] != id {
			t.Fatalf("said to be %v's", it["chat_id"])
		}
	}
	if pid, ok := core.PeopleOf(s).PersonOf[caller]; ok {
		if r, err := core.Calls(s, core.CallsOptions{ChatID: fmt.Sprintf("p%d", pid), Limit: 100000}); err == nil {
			for _, it := range items(r) {
				if i64(it["id"]) >= 800000 {
					t.Fatal("a group call among a person's")
				}
			}
		}
	}
}

// The chat list finds a person by their number, as messengers find a contact: typed with spaces,
// with or without its +, or only part of it.
func TestTheChatListFindsAPersonByNumber(t *testing.T) {
	s := store(t)
	ppl := core.PeopleOf(s)
	var chat core.M
	var number string
	for _, c := range chats(s, nil) {
		if !isPerson(c) {
			continue
		}
		for _, h := range ppl.Handles[i64(c["person_id"])] {
			if h.Kind == "phone" && len(h.Value) > 10 && !strings.Contains(c["title"].(string), h.Value) {
				chat, number = c, h.Value
			}
		}
		if chat != nil {
			break
		}
	}
	if chat == nil {
		t.Fatal("no named person with a number")
	}
	digits := strings.TrimPrefix(number, "+")
	for _, q := range []string{number, digits, digits[:3] + " " + digits[3:7] + " " + digits[7:], digits[len(digits)-6:],
		"(" + digits[:3] + ") " + digits[3:]} {
		if !ids(chats(s, func(o *core.ChatsOptions) { o.Q = q }))[chat["id"].(string)] {
			t.Fatalf("not found by %q", q)
		}
	}
	if ids(chats(s, func(o *core.ChatsOptions) { o.Q = digits[:4] + "x" }))[chat["id"].(string)] {
		t.Fatal("found by what is not its number")
	}
}

// What depends on more than the archive is built again after its time, without a time in its key.
func TestACacheWithAnAge(t *testing.T) {
	s := store(t)
	n := 0
	build := func() int { n++; return n }
	core.CachedFor(s, "aged", 30*time.Millisecond, build)
	core.CachedFor(s, "aged", 30*time.Millisecond, build)
	time.Sleep(40 * time.Millisecond)
	core.CachedFor(s, "aged", 30*time.Millisecond, build)
	if n != 2 {
		t.Fatalf("built %d times", n)
	}
}

// The statistics count only the messages added since they were last counted: what they say after
// messages come (in a new year too) or go is what a count of the whole archive says.
func TestStatsCountedSinceAreTheWholeCount(t *testing.T) {
	s := store(t)
	whole := func() core.M {
		fresh, err := core.Open(s.Path)
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		return core.Stats(fresh, true)
	}
	same := func(when string) {
		t.Helper()
		if got, want := core.Stats(s, true), whole(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\n%v\n%v", when, got, want)
		}
	}
	same("at first")
	later := time.Now().AddDate(3, 0, 0).UnixMilli()
	write(t, s, func(tx *sql.Tx) {
		for _, at := range []int64{db.Int(tx, "SELECT max(ts) FROM message") + 1, later} {
			db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, kind_id, text) "+
				"SELECT service_id, conversation_id, ?, 1, kind_id, 'x' FROM message GROUP BY conversation_id", at)
		}
	})
	same("after messages came")
	write(t, s, func(tx *sql.Tx) {
		db.Exec(tx, "DELETE FROM message WHERE text = 'x' AND ts < ?", later) // the newest stay
	})
	same("after messages went")
}
