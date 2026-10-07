// Ports everysaid/demo.py: a demo archive of invented people, for trying the app and for its tests:
// nothing in it is real.
//
//	everysaid demo [--dir DIR] [--seed N] [--serve]
//
// Everything goes under DIR (default `./demo`): `data/` (the archive, the media), `cache/`,
// `config/` (its own config.toml), `state/`. Main points EVERYSAID_DATA, EVERYSAID_CACHE,
// EVERYSAID_CONFIG and EVERYSAID_STATE there (and a keyring name of its own) before building, so
// neither the user's archive nor their settings are touched. With --serve it then starts the app on
// it, through Serve, which the command line fills (the server is a package the demo does not
// import).
//
// The archive is the Python's, row for row and byte for byte (pictures too) for the same seed and
// the same hour: the same random numbers (package pyrandom), drawn in the same order, and the
// pictures drawn and encoded as Pillow and libjpeg-turbo do (pictures.go, jpeg.go).
//
// For tests: Build(t) makes a fresh demo archive in a temporary folder, as tests/conftest.py does,
// and gives its folder and the archive's path; the environment and the settings point there until
// the test ends.
package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/i18n"
	"everysaid/internal/importers"
	"everysaid/internal/pyjson"
	"everysaid/internal/pyrandom"
)

var first = []string{"Ελένη", "Νίκος", "Μαρία", "Γιάννης", "Κατερίνα", "Δημήτρης", "Σοφία", "Αλέξης", "Άννα", "Κώστας",
	"Δέσποινα", "Στέλιος", "Ειρήνη", "Θανάσης", "Χριστίνα", "Μιχάλης", "Emma", "Lucas", "Olivia", "Noah",
	"Mia", "Leo", "Clara", "Hugo", "Ingrid", "Mateo"}
var last = []string{"Παπαδοπούλου", "Γεωργίου", "Οικονόμου", "Αντωνίου", "Νικολάου", "Βασιλείου", "Μακρή", "Ιωάννου",
	"Schmidt", "Rossi", "Dubois", "Novak", "Larsen", "García"}
var lines = map[string][]string{
	"el": {"Καλημέρα! Τι κάνεις;", "Όλα καλά, εσύ;", "Θα βρεθούμε το Σάββατο;", "Ναι, στις 8 στο γνωστό μέρος",
		"Τέλεια 👍", "Πήρες τα εισιτήρια;", "Ακόμα όχι, αύριο", "Χρόνια πολλά!! 🎉", "Ευχαριστώ πολύ ❤️",
		"Πού είσαι;", "Έρχομαι σε 10'", "Δες αυτό 😂", "Πολύ ωραίο!", "Θα σε πάρω τηλέφωνο αργότερα",
		"Καληνύχτα", "Έφτασες;", "Ναι, μόλις", "Μην ξεχάσεις το ψωμί", "Το απόγευμα είμαι ελεύθερος",
		"Τι ώρα κλείνει το φαρμακείο;", "Νομίζω στις 9", "Φιλιά στα παιδιά", "Εντάξει, τα λέμε",
		"Ο καιρός αύριο λέει βροχή", "Πάμε για καφέ;", "Συγγνώμη, ήμουν σε σύσκεψη", "Μπράβο σου!",
		"Στείλε μου τη διεύθυνση", "Κλείσαμε για Ιούλιο στη Νάξο", "Πόσο έκανε τελικά;"},
	"en": {"Good morning! How are you?", "All good, you?", "Are we meeting on Saturday?", "Yes, 8pm at the usual place",
		"Perfect 👍", "Did you get the tickets?", "Not yet, tomorrow", "Happy birthday!! 🎉", "Thanks so much ❤️",
		"Where are you?", "Coming in 10", "Look at this 😂", "Lovely!", "I'll call you later", "Good night",
		"Did you arrive?", "Yes, just now", "Don't forget the bread", "I'm free in the afternoon",
		"Coffee?", "Sorry, I was in a meeting", "Well done!", "Send me the address", "How much was it in the end?"},
}

type group struct{ title, service, lang string }

var groups = []group{{"Οικογένεια", "whatsapp", "el"}, {"Ποδόσφαιρο Τετάρτης", "viber", "el"},
	{"Book club", "telegram", "en"}, {"Γειτονιά", "whatsapp", "el"}}
var services = []string{"whatsapp", "viber", "sms", "telegram", "imessage"}
var reactions = []string{"❤️", "😂", "👍", "😮", "🙏", "🎉"}

// Now is the clock the demo reads (its "now" is this hour); a test may fix it.
var Now = time.Now

type person struct {
	name, number, lang string
	services           []string
	weight             int
	contact            bool
}

func phone(number string) archive.Handle { return archive.H("phone", number) }

func days(d float64) time.Duration {
	return time.Duration(math.Round(d*86400*1e6)) * time.Microsecond // as timedelta keeps it: microseconds
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// firstWord is name.split()[0].
func firstWord(name string) string { return strings.Fields(name)[0] }

// BuildArchive makes the demo archive in config.Data (the folders must already point at the demo's:
// see Env), from the seed; it returns its path.
func BuildArchive(seed int64) (path string, err error) {
	defer archive.Recover(&err)
	rnd := pyrandom.New(seed)
	path = filepath.Join(config.Data, "archive.db")
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return "", err
		}
	}
	a, err := archive.Open(path)
	if err != nil {
		return "", err
	}
	defer a.Close()
	a.Account(phone("+15550000000"), "", "")
	now := Now().UTC().Truncate(time.Hour)
	start := now.Add(-days(3 * 365))
	nowSec := now.Unix()
	store := importers.NewStore(a)
	pics := filepath.Join(config.Cache, "demo-src")
	if err := os.MkdirAll(pics, 0o777); err != nil {
		return "", err
	}
	type instKey struct{ plugin, label string }
	instances := map[instKey]int64{}

	instance := func(plugin, label, kind string) int64 {
		k := instKey{plugin, label}
		if _, ok := instances[k]; !ok {
			id, _ := a.Exec("INSERT INTO plugin_instance (plugin, kind, label, created_at, last_run, last_status) VALUES (?, ?, ?, ?, ?, 'ok')",
				plugin, kind, label, nowSec, nowSec).LastInsertId()
			instances[k] = id
		}
		return instances[k]
	}
	source := func(service string) int64 {
		plugin := map[string]string{"sms": "iphone-backup", "imessage": "iphone-backup", "whatsapp": "whatsapp-bridge",
			"viber": "iphone-backup", "telegram": "telegram"}[service]
		sid := a.Source("demo/"+service, "demo", "demo-phone", pics)
		a.Exec("UPDATE source SET instance_id = ? WHERE id = ?", instance(plugin, "Demo phone", "source"), sid)
		return sid
	}

	var people []*person
	used := map[string]bool{}
	for i := 0; i < 26; i++ {
		var name string
		for {
			f := pyrandom.Pick(rnd, first)
			name = f + " " + pyrandom.Pick(rnd, last)
			if !used[name] {
				used[name] = true
				break
			}
		}
		number := fmt.Sprintf("+1555%07d", 1000000+i*7919)
		lang := "en"
		for _, ch := range name {
			if ch >= 'Ͱ' && ch <= 'Ͽ' {
				lang = "el"
				break
			}
		}
		k := pyrandom.Pick(rnd, []int{1, 2, 2, 3})
		svc := pyrandom.Sample(rnd, services, k)
		weight := pyrandom.Pick(rnd, []int{1, 1, 2, 3, 8, 20})
		people = append(people, &person{name, number, lang, svc, weight, i < 20})
	}
	// contacts: an address book instance with most people in it, some with photos
	instance("demo-sender", "Demo sender", "source") // sending, in the demo (see Sender)
	book := instance("vcard-file", "Contacts", "contacts")
	avatars := filepath.Join(config.Cache, "avatars")
	if err := os.MkdirAll(avatars, 0o777); err != nil {
		return "", err
	}
	for i, p := range people {
		if !p.contact {
			continue
		}
		var photo any
		if i%3 != 2 {
			f := filepath.Join(avatars, fmt.Sprintf("demo%d.jpg", i))
			initials := ""
			for _, w := range strings.Fields(p.name) {
				initials += string([]rune(w)[0])
			}
			if err := avatar(f, seed+int64(i), initials); err != nil {
				return "", err
			}
			b, err := os.ReadFile(f)
			if err != nil {
				return "", err
			}
			h := sha256.Sum256(b)
			name := hex.EncodeToString(h[:]) + ".jpg"
			if err := os.Rename(f, filepath.Join(avatars, name)); err != nil {
				return "", err
			}
			photo = name
		}
		cid, _ := a.Exec("INSERT INTO contact (instance_id, uid, name, photo, updated_at) VALUES (?, ?, ?, ?, ?)",
			book, fmt.Sprintf("demo-%d", i), p.name, photo, nowSec).LastInsertId()
		a.Exec("INSERT INTO contact_address (contact_id, address_id, label) VALUES (?, ?, 'mobile')",
			cid, a.Address(phone(p.number)))
	}
	// the names services show: WhatsApp's copy of the address book (for those in it) and the names
	// people chose (a first name), Telegram profile names, so the ones not in the book have a name too
	for _, p := range people {
		a.Address(phone(p.number))
		if contains(p.services, "whatsapp") {
			if p.contact {
				a.HandleName(phone(p.number), "whatsapp", p.name, "book", 0)
			}
			a.HandleName(phone(p.number), "whatsapp", firstWord(p.name), "profile", 0)
		}
		if contains(p.services, "telegram") {
			a.HandleName(phone(p.number), "telegram", firstWord(p.name), "profile", 0)
		}
	}
	picN := 0
	// The pictures are drawn alongside (they take most of the time) and linked to their messages at
	// the end, in the order Python links them as it goes: nothing between reads the attachments, so
	// the rows are the same.
	type link struct {
		service   string
		src, mid  int64
		path, rel string
		done      chan error
	}
	var links []link
	workers := make(chan struct{}, runtime.NumCPU())

	// add is a message; text "" draws one of the language's lines.
	add := func(service string, conv, ts int64, outgoing bool, sender int64, lang string, row int, kind, text string,
		x *archive.Extras) int64 {
		src := source(service)
		key := ""
		if service != "sms" {
			key = fmt.Sprintf("demo-%s-%d", service, row)
		}
		txt := text
		if text == "" {
			txt = pyrandom.Pick(rnd, lines[lang])
		}
		if kind == "image" {
			inner := pyrandom.Pick(rnd, lines[lang])
			txt = []string{"", "", "📷", inner}[rnd.Choice(4)]
		}
		mid := a.AddMessage(src, fmt.Sprintf("r%d", row), archive.Message{Service: service, ConversationID: conv, TS: ts,
			Outgoing: outgoing, SenderID: sender, Kind: kind, Text: txt, Key: key, Extras: x})
		if kind == "image" {
			picN++
			f := filepath.Join(pics, fmt.Sprintf("p%d.jpg", picN))
			label := time.UnixMilli(ts).Local().Format("02/01/2006")
			l := link{service, src, mid, f, fmt.Sprintf("p%d.jpg", picN), make(chan error, 1)}
			workers <- struct{}{}
			go func(n int64) {
				defer func() { <-workers }()
				l.done <- picture(f, n, label)
			}(seed*1000 + int64(picN))
			links = append(links, l)
		}
		return mid
	}

	row := 0
	for _, p := range people {
		aid := a.Address(phone(p.number))
		for _, service := range p.services {
			conv := a.Conversation(service, []archive.Handle{phone(p.number)}, "", "")
			n := p.weight * rnd.Randrange(20, 60)
			t := start.Add(days(float64(rnd.Randrange(0, 700))))
			prev := 0
			for k := 0; k < n; k++ {
				t = t.Add(time.Duration(pyrandom.Pick(rnd, []int{1, 2, 5, 30, 120, 600, 1440, 4000})) * time.Minute)
				if t.After(now) {
					break
				}
				ts := ms(t)
				out := rnd.Random() < .45
				kind := "text"
				if service != "sms" && rnd.Random() < .04 {
					kind = "image"
				}
				x := &archive.Extras{}
				r := rnd.Random()
				if prev != 0 && r < .06 && service != "sms" {
					x.ReplyKey = fmt.Sprintf("demo-%s-%d", service, prev)
				}
				if contains([]string{"whatsapp", "viber", "telegram", "imessage"}, service) && rnd.Random() < .07 {
					var who any
					if out {
						who = phone(p.number)
					}
					x.Reactions = []archive.Reaction{{Emoji: pyrandom.Pick(rnd, reactions), Count: 1, Who: who, Outgoing: !out}}
				}
				if rnd.Random() < .01 && service == "whatsapp" {
					x.Edited = true
				}
				if rnd.Random() < .004 && service != "sms" {
					kind = "location"
					lat := 37.97 + rnd.Random()/10
					lon := 23.72 + rnd.Random()/10
					x.Lat, x.Lon, x.Place = &lat, &lon, "Αθήνα"
				}
				row++
				var sender int64
				if !out {
					sender = aid
				}
				add(service, conv, ts, out, sender, p.lang, row, kind, "", x)
				prev = row
			}
		}
		// calls
		for k, n := 0, p.weight*rnd.Randrange(2, 8); k < n; k++ {
			ts := ms(start.Add(time.Duration(rnd.Randrange(0, 3*365*1440)) * time.Minute))
			out := rnd.Random() < .5
			answered := rnd.Random() < .7
			service := pyrandom.Pick(rnd, []string{"phone", "phone", "whatsapp", "viber"})
			row++
			src := source("sms")
			var duration int64
			detail := ""
			if answered {
				duration = int64(rnd.Randrange(20, 1800))
			} else if out {
				detail = "unanswered"
			} else {
				detail = "missed"
			}
			video := service != "phone" && rnd.Random() < .3
			key := ""
			if service != "phone" {
				key = fmt.Sprintf("demo-call-%d", row)
			}
			a.AddCall(src, fmt.Sprintf("call%d", row), archive.Call{Service: service, AddressID: aid, TS: ts, Outgoing: out,
				Answered: answered, Duration: duration, Detail: detail, Video: video, Key: key})
		}
	}
	// groups
	for _, g := range groups {
		members := pyrandom.Sample(rnd, people, rnd.Randrange(4, 9))
		handles := make([]archive.Handle, len(members))
		for i, m := range members {
			handles[i] = phone(m.number)
		}
		conv := a.Conversation(g.service, handles, "demo-group-"+g.title, g.title)
		a.Exec("UPDATE conversation SET is_group = 1 WHERE id = ?", conv)
		t := start.Add(days(float64(rnd.Randrange(0, 300))))
		for k, n := 0, rnd.Randrange(300, 900); k < n; k++ {
			t = t.Add(time.Duration(pyrandom.Pick(rnd, []int{1, 3, 10, 60, 300, 1440})) * time.Minute)
			if t.After(now) {
				break
			}
			out := rnd.Random() < .2
			m := pyrandom.Pick(rnd, members)
			row++
			kind := "text"
			if rnd.Random() < .03 {
				kind = "image"
			}
			var named *person
			if kind == "text" && rnd.Random() < .05 {
				var others []*person
				for _, x := range members {
					if x != m {
						others = append(others, x)
					}
				}
				named = pyrandom.Pick(rnd, others)
			}
			token, text := "", ""
			if named != nil {
				token = "@" + firstWord(named.name)
			}
			var sender int64
			if !out {
				sender = a.Address(phone(m.number))
			}
			if named != nil {
				text = token + " " + pyrandom.Pick(rnd, lines[g.lang])
			}
			mid := add(g.service, conv, ms(t), out, sender, g.lang, row, kind, text, nil)
			if named != nil {
				a.Exec("INSERT INTO mention VALUES (?, ?, ?)", mid, a.Address(phone(named.number)), token)
			}
		}
	}
	// a few new messages of the last hours, unread
	for _, p := range pyrandom.Sample(rnd, people, 6) {
		service := p.services[0]
		conv := a.Conversation(service, []archive.Handle{phone(p.number)}, "", "")
		for k, n := 0, rnd.Randrange(1, 4); k < n; k++ {
			row++
			ts := ms(now.Add(-(time.Duration(rnd.Randrange(1, 30))*time.Hour + time.Duration(k)*time.Minute)))
			add(service, conv, ts, false, a.Address(phone(p.number)), p.lang, row, "text", "", nil)
		}
	}
	// chats' state as services report it: one muted for ever, one pinned, one archived (which starts
	// the app's own archived: Archive.InitArchived, at Resolve)
	var told []*person
	for _, p := range people {
		if len(told) < 3 && len(p.services) == 1 && p.services[0] == "whatsapp" {
			told = append(told, p)
		}
	}
	stamp := ms(now)
	for i, fv := range []struct {
		field string
		value int64
	}{{"muted", -1}, {"pinned", 1}, {"archived", 1}} {
		if i >= len(told) {
			break
		}
		conv := a.Conversation("whatsapp", []archive.Handle{phone(told[i].number)}, "", "")
		a.ReportState(source("whatsapp"), conv, fv.field, fv.value, stamp-86400000, 0)
	}
	// who got and read what the owner sent, where the service tells: WhatsApp (when), Telegram (in a
	// chat with one person, read but not when); the latest of each chat delivered, not read yet
	whatsapp, telegram := a.Service.ID("whatsapp"), a.Service.ID("telegram")
	lastOut := map[int64]int64{}
	a.Each("SELECT conversation_id, max(id) FROM message WHERE outgoing GROUP BY 1", nil, func(scan func(...any)) {
		var c, m int64
		scan(&c, &m)
		lastOut[c] = m
	})
	type sent struct {
		mid, conv, ts, sid int64
		group              bool
	}
	var sents []sent
	a.Each("SELECT m.id, m.conversation_id, m.ts, m.service_id, c.is_group FROM message m "+
		"JOIN conversation c ON c.id = m.conversation_id WHERE m.outgoing AND m.service_id IN (?, ?)",
		[]any{whatsapp, telegram}, func(scan func(...any)) {
			var s sent
			scan(&s.mid, &s.conv, &s.ts, &s.sid, &s.group)
			sents = append(sents, s)
		})
	for _, s := range sents {
		if s.sid == telegram && s.group {
			continue
		}
		for _, aid := range a.Ints("SELECT address_id FROM conversation_member WHERE conversation_id = ? AND "+
			"address_id NOT IN (SELECT address_id FROM account)", s.conv) {
			var read any
			if lm, ok := lastOut[s.conv]; !(ok && lm == s.mid) && !(s.group && rnd.Random() < .25) {
				read = s.ts + int64(rnd.Randrange(5, 3600))*1000
			}
			if s.sid == telegram {
				var r any
				if read != nil {
					r = 0
				}
				a.Exec("INSERT INTO receipt (message_id, address_id, read_at) VALUES (?, ?, ?)", s.mid, aid, r)
			} else {
				a.Exec("INSERT INTO receipt VALUES (?, ?, ?, ?, NULL)", s.mid, aid, s.ts+2000, read)
			}
		}
	}
	// notes to self
	conv := a.Conversation("viber", []archive.Handle{phone("+15550000000")}, "demo-notes", "")
	for i, txt := range []string{"Λίστα: γάλα, αυγά, καφές", "Κωδικός Wi-Fi γραφείου στο συρτάρι", "Ιδέα για δώρο: βιβλίο μαγειρικής"} {
		row++
		add("viber", conv, ms(now.Add(-days(float64(30-i*7)))), true, 0, "el", row, "text", txt, nil)
	}
	// numbers no source names: a few old calls, a hidden number, a courier's SMS read long ago, and
	// one unread (shown even when people without a name are not)
	for i, nCalls := range []int{1, 1, 2, 1} {
		aid := a.Address(phone(fmt.Sprintf("+1555010%04d", i)))
		for k := 0; k < nCalls; k++ {
			row++
			answered := i%2 == 1
			var duration int64
			detail := "missed"
			if answered {
				duration, detail = 95, ""
			}
			a.AddCall(source("sms"), fmt.Sprintf("call%d", row), archive.Call{Service: "phone", AddressID: aid,
				TS: ms(now.Add(-days(float64(200 + 40*i + k)))), Outgoing: k == 1, Answered: answered,
				Duration: duration, Detail: detail})
		}
	}
	row++
	a.AddCall(source("sms"), fmt.Sprintf("call%d", row), archive.Call{Service: "phone", TS: ms(now.Add(-days(150))),
		Detail: "missed"})
	for _, c := range []struct {
		number string
		days   float64
		text   string
	}{{"+15550100010", 180, "Your parcel will be delivered today between 10:00 and 14:00"},
		{"+15550100011", 1.6, "Hello, this is the clinic: your results are ready"}} {
		conv := a.Conversation("sms", []archive.Handle{phone(c.number)}, "", "")
		row++
		add("sms", conv, ms(now.Add(-days(c.days))), false, a.Address(phone(c.number)), "en", row, "text", c.text, nil)
	}
	// someone no source names, with a long chat: their email says who they are (a name found for them)
	handle := archive.H("email", "katerina.oikonomou@example.com")
	conv = a.Conversation("imessage", []archive.Handle{handle}, "", "")
	for k := 0; k < 24; k++ {
		row++
		out := k%2 == 0
		text := "Κατερίνα, τα λέμε αύριο στο γραφείο;"
		if k != 4 {
			text = pyrandom.Pick(rnd, lines["el"])
		}
		var sender int64
		if !out {
			sender = a.Address(handle)
		}
		add("imessage", conv, ms(now.Add(-days(float64(90-k*3)))), out, sender, "el", row, "text", text, nil)
	}
	// people who are likely one: a second number with the same name in WhatsApp's contacts, and a
	// Telegram account whose name sounds like someone's (merge suggestions)
	for _, l := range []struct {
		handle              archive.Handle
		service, name, kind string
	}{{phone("+15550100020"), "whatsapp", "Νίκος Γεωργίου", "book"},
		{archive.H("username", "eleni_i", "telegram"), "telegram", "Eleni Ioannou", "profile"}} {
		conv := a.Conversation(l.service, []archive.Handle{l.handle}, "", "")
		a.HandleName(l.handle, l.service, l.name, l.kind, 0)
		for k, text := range []string{"Hi, it's my new number", "Talk later?"} {
			row++
			add(l.service, conv, ms(now.Add(-days(float64(60-k)))), false, a.Address(l.handle), "en", row, "text", text, nil)
		}
	}
	// a library: a folder
	lib := filepath.Join(config.Data, "library")
	if err := os.MkdirAll(lib, 0o777); err != nil {
		return "", err
	}
	a.Exec("INSERT INTO plugin_instance (plugin, kind, label, settings, is_default, created_at) VALUES "+
		"('folder', 'library', 'Photos folder', ?, 1, ?)", pyjson.Dumps(pyjson.OrderedMap{{Key: "path", Value: lib}}, true), nowSec)
	var failed error
	for _, l := range links {
		if err := <-l.done; err != nil && failed == nil {
			failed = err
		}
	}
	if failed != nil {
		return "", failed
	}
	for _, l := range links {
		store.Link("demo/"+l.service, l.src, l.path, l.rel, l.mid)
	}
	a.Resolve()
	a.Exec("INSERT OR REPLACE INTO setting VALUES ('unread_since', ?)", pyjson.Dumps(ms(now.Add(-days(2))), true))
	a.Commit()
	n := a.Int("SELECT count(*) FROM message")
	c := a.Int("SELECT count(*) FROM call")
	fmt.Println(i18n.Say("demo: {messages} messages, {calls} calls, {people} people, {groups} groups, {pictures} pictures -> {path}",
		map[string]any{"messages": n, "calls": c, "people": len(people), "groups": len(groups), "pictures": picN, "path": path}))
	return path, nil
}
