// Ports everysaid/core/labels.py.
//
// Labels: the words people are described by, the tone of their chats (many to a person) and who
// they are to the owner (one), and the names found for people no source names.
//
// The lists are the user's: the app starts them with a few (archive.Labels, their words in the
// app's languages), and the user renames, adds, merges and removes them. A label's meaning is what
// the local models read to judge by; one without a meaning (”) is only given by the user.
//
// A person's label is the user's (yes, or no: never suggested again) or the local models'
// (suggested, with their votes and a line of the chat that shows it). The models' never touch the
// user's.
//
// A name for someone without one comes from the local models (`name_guess` 'models') or from their
// handles, read here without a model ('handle': "first.last@..." is "First Last" where
// the archive knows those names). Either is shown to the user, never applied by itself.
package core

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/pyjson"
	"everysaid/internal/text"
)

// LabelKinds are the kinds of labels.
var LabelKinds = []string{"tone", "relation"}

func defaultMeaning(key string) any {
	for _, l := range archive.Labels {
		if l.Key == key {
			return l.Meaning
		}
	}
	return nil
}

// --- the lists -----------------------------------------------------------------------------------

// Labels is [{id, kind, key, name, meaning, default_meaning, sensitive, uses: {yes, suggested}}],
// in the user's order. name: the user's (nil for one the app brings, said in the app's words by
// key); meaning: what the models read (nil: the app's own, default_meaning).
func Labels(s *Store, kind string) []M {
	q := s.Read()
	uses := map[int64]map[string]int64{}
	db.Each(q, "SELECT label_id, state, count(*) FROM person_label GROUP BY 1, 2", nil, func(scan func(...any)) {
		var lid, n int64
		var state string
		scan(&lid, &state, &n)
		if uses[lid] == nil {
			uses[lid] = map[string]int64{}
		}
		uses[lid][state] = n
	})
	sqlq, args := "SELECT id, kind, key, name, meaning, sensitive FROM label", []any{}
	if kind != "" {
		sqlq += " WHERE kind = ?"
		args = append(args, kind)
	}
	out := []M{}
	db.Each(q, sqlq+" ORDER BY kind DESC, position, id", args, func(scan func(...any)) {
		var id int64
		var k string
		var key, name, meaning sql.NullString
		var sens bool
		scan(&id, &k, &key, &name, &meaning, &sens)
		var dm any
		if key.Valid {
			dm = defaultMeaning(key.String)
		}
		out = append(out, M{"id": id, "kind": k, "key": nullString(key), "name": nullString(name),
			"meaning": nullString(meaning), "default_meaning": dm, "sensitive": sens,
			"uses": M{"yes": uses[id]["yes"], "suggested": uses[id]["suggested"]}})
	})
	return out
}

// ModelLabel is a label as the models judge by it: the word being the app's key or the user's name.
type ModelLabel struct {
	ID        int64
	Word      string
	Meaning   string
	Sensitive bool
}

// ForModels is the labels the models judge by.
func ForModels(s *Store, kind string) []ModelLabel {
	var out []ModelLabel
	for _, lb := range Labels(s, kind) {
		meaning := lb["meaning"]
		if meaning == nil {
			meaning = lb["default_meaning"]
		}
		m, _ := meaning.(string)
		if m == "" {
			continue
		}
		word, _ := lb["key"].(string)
		if word == "" {
			word, _ = lb["name"].(string)
		}
		out = append(out, ModelLabel{lb["id"].(int64), word, m, lb["sensitive"].(bool)})
	}
	return out
}

// Digest is what the lists the models judge by are now: another digest, another list (the same
// as the Python's: json.dumps of [[id, word, meaning, sensitive], ...] per kind, sha1, 12 digits).
func Digest(s *Store) string {
	var words []any
	for _, k := range LabelKinds {
		list := []any{}
		for _, l := range ForModels(s, k) {
			list = append(list, []any{l.ID, l.Word, l.Meaning, l.Sensitive})
		}
		words = append(words, list)
	}
	h := sha1.Sum([]byte(pyjson.Dumps(words, false)))
	return hex.EncodeToString(h[:])[:12]
}

func validKind(kind string) bool { return kind == "tone" || kind == "relation" }

// AddLabel adds a label of the user's; it returns its id.
func AddLabel(s *Store, kind, name, meaning string, sensitive bool) (int64, error) {
	if !validKind(kind) {
		return 0, fmt.Errorf("unknown kind %q", kind)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errs.New("labels.no_name", 0, nil)
	}
	var id int64
	err := s.Write(func(tx *sql.Tx) error {
		for _, n := range db.Strs(tx, "SELECT name FROM label WHERE kind = ?", kind) {
			if n != "" && text.Fold(n) == text.Fold(name) {
				return errs.New("labels.exists", 0, nil)
			}
		}
		last := db.Int(tx, "SELECT max(position) FROM label")
		id = db.LastID(tx, "INSERT INTO label (kind, name, meaning, sensitive, position) VALUES (?, ?, ?, ?, ?)",
			kind, name, strings.TrimSpace(meaning), db.B(sensitive), last+1)
		return nil
	})
	return id, err
}

// EditLabel: name nil or "": back to the app's words (for one it brings); meaning nil: back to the
// app's.
func EditLabel(s *Store, labelID int64, name, meaning Opt[string], sensitive Opt[bool]) error {
	return s.Write(func(tx *sql.Tx) error {
		var key sql.NullString
		if !db.Row(tx, "SELECT key FROM label WHERE id = ?", []any{labelID}, &key) {
			return ErrNotFound
		}
		if name.Set {
			n := trimmedOrNil(name.Value)
			if n == nil && !key.Valid {
				return errs.New("labels.no_name", 0, nil)
			}
			db.Exec(tx, "UPDATE label SET name = ? WHERE id = ?", n, labelID)
		}
		if meaning.Set {
			var m any
			if meaning.Value == nil && key.Valid {
				m = nil
			} else if meaning.Value == nil {
				m = ""
			} else {
				m = strings.TrimSpace(*meaning.Value)
			}
			db.Exec(tx, "UPDATE label SET meaning = ? WHERE id = ?", m, labelID)
		}
		if sensitive.Set {
			v := sensitive.Value != nil && *sensitive.Value
			db.Exec(tx, "UPDATE label SET sensitive = ? WHERE id = ?", db.B(v), labelID)
		}
		return nil
	})
}

// OrderLabels puts the labels in the user's order.
func OrderLabels(s *Store, ids []int64) error {
	return s.Write(func(tx *sql.Tx) error {
		for i, x := range ids {
			db.Exec(tx, "UPDATE label SET position = ? WHERE id = ?", i, x)
		}
		return nil
	})
}

// RemoveLabel: the label goes, and with it every person's (the user's too: the UI asks first).
func RemoveLabel(s *Store, labelID int64) error {
	return s.Write(func(tx *sql.Tx) error {
		if db.Changed(tx, "DELETE FROM label WHERE id = ?", labelID) == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// MergeLabels: `other` becomes `into`: the people it was given to have `into` (the user's word over
// the models'), and it goes.
func MergeLabels(s *Store, into, other int64) error {
	if into == other {
		return errs.New("labels.same", 0, nil)
	}
	return s.Write(func(tx *sql.Tx) error {
		kinds := map[int64]string{}
		db.Each(tx, "SELECT id, kind FROM label WHERE id IN (?, ?)", []any{into, other}, func(scan func(...any)) {
			var id int64
			var k string
			scan(&id, &k)
			kinds[id] = k
		})
		if len(kinds) != 2 {
			return ErrNotFound
		}
		if kinds[into] != kinds[other] {
			return errs.New("labels.other_kind", 0, nil)
		}
		moveLabels(tx, "label_id", into, other)
		db.Exec(tx, "DELETE FROM label WHERE id = ?", other)
		return nil
	})
}

// moveLabels moves the person_label rows of `other` (a label or a person, by column) to `into`:
// where both have one, the user's word wins over the models', else the one there stays.
func moveLabels(tx *sql.Tx, column string, into, other int64) {
	who := "?, label_id"
	if column == "label_id" {
		who = "person_id, ?"
	}
	db.Exec(tx, "INSERT INTO person_label (person_id, label_id, state, votes, models, evidence, at) "+
		"SELECT "+who+", state, votes, models, evidence, at FROM person_label WHERE "+column+" = ? "+
		"ON CONFLICT (person_id, label_id) DO UPDATE SET state = excluded.state, votes = excluded.votes, "+
		"models = excluded.models, evidence = excluded.evidence, at = excluded.at "+
		"WHERE person_label.state = 'suggested' AND excluded.state != 'suggested'", into, other)
	db.Exec(tx, "DELETE FROM person_label WHERE "+column+" = ?", other)
}

// movedPerson: when the person `other` becomes part of `into` (inside the merge's transaction):
// their labels go over, a name found for them too where `into` has none, and both are to be read
// again (their chat is now one).
func movedPerson(tx *sql.Tx, into, other int64) {
	moveLabels(tx, "person_id", into, other)
	db.Exec(tx, "UPDATE OR IGNORE name_guess SET person_id = ? WHERE person_id = ?", into, other)
	db.Exec(tx, "DELETE FROM name_guess WHERE person_id = ?", other)
	db.Exec(tx, "DELETE FROM analysis WHERE person_id IN (?, ?)", into, other)
}

// --- a person's ----------------------------------------------------------------------------------

// PersonLabels is [{id, kind, key, name, sensitive, state, votes, models, evidence}]: the user's,
// and the models' unless suggested is false; those the user said no to are left out.
func PersonLabels(s *Store, personID int64, suggested bool) []M {
	extra := ""
	if !suggested {
		extra = "AND pl.state = 'yes' "
	}
	out := []M{}
	db.Each(s.Read(), "SELECT l.id, l.kind, l.key, l.name, l.sensitive, pl.state, pl.votes, pl.models, pl.evidence "+
		"FROM person_label pl JOIN label l ON l.id = pl.label_id WHERE pl.person_id = ? AND pl.state != 'no' "+
		extra+"ORDER BY l.kind DESC, l.position, l.id", []any{personID}, func(scan func(...any)) {
		var id int64
		var kind, state string
		var key, name, ev sql.NullString
		var sens bool
		var votes, models sql.NullInt64
		scan(&id, &kind, &key, &name, &sens, &state, &votes, &models, &ev)
		out = append(out, M{"id": id, "kind": kind, "key": nullString(key), "name": nullString(name), "sensitive": sens,
			"state": state, "votes": nullInt(votes), "models": nullInt(models), "evidence": nullString(ev)})
	})
	return out
}

// ByPerson is every person's labels at once, for lists: {person id: [{id, kind, key, name, state}]},
// the user's and (unless suggested is false) the models'.
func ByPerson(s *Store, suggested bool) map[int64][]M {
	cond := "WHERE pl.state = 'yes'"
	if suggested {
		cond += " OR pl.state = 'suggested'"
	}
	out := map[int64][]M{}
	db.Each(s.Read(), "SELECT pl.person_id, l.id, l.kind, l.key, l.name, pl.state FROM person_label pl JOIN label l ON l.id = pl.label_id "+
		cond+" ORDER BY l.kind DESC, l.position, l.id", nil, func(scan func(...any)) {
		var pid, id int64
		var kind, state string
		var key, name sql.NullString
		scan(&pid, &id, &kind, &key, &name, &state)
		out[pid] = append(out[pid], M{"id": id, "kind": kind, "key": nullString(key), "name": nullString(name), "state": state})
	})
	return out
}

// SetPersonLabel is the user's word on a label of a person: "yes" (it is so: one relation only),
// "no" (it is not: the models do not suggest it again), or "" (no word: gone, the models may
// suggest it).
func SetPersonLabel(s *Store, personID, labelID int64, state string) error {
	if state != "yes" && state != "no" && state != "" {
		return fmt.Errorf("unknown state %q", state)
	}
	return s.Write(func(tx *sql.Tx) error {
		if !db.Exists(tx, "SELECT 1 FROM person WHERE id = ?", personID) {
			return ErrNotFound
		}
		var kind string
		if !db.Row(tx, "SELECT kind FROM label WHERE id = ?", []any{labelID}, &kind) {
			return ErrNotFound
		}
		if state == "" {
			db.Exec(tx, "DELETE FROM person_label WHERE person_id = ? AND label_id = ?", personID, labelID)
			return nil
		}
		if state == "yes" && kind == "relation" { // one relation: the others it was are no more
			db.Exec(tx, "DELETE FROM person_label WHERE person_id = ? AND state = 'yes' AND label_id IN "+
				"(SELECT id FROM label WHERE kind = 'relation')", personID)
		}
		db.Exec(tx, "INSERT INTO person_label (person_id, label_id, state, at) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT (person_id, label_id) DO UPDATE SET state = excluded.state, at = excluded.at",
			personID, labelID, state, time.Now().Unix())
		return nil
	})
}

// Analysed is when the local models last read the person's chat, and how many messages it had:
// {at, messages} or nil.
func Analysed(s *Store, personID int64) M {
	var at, n int64
	if !db.Row(s.Read(), "SELECT at, messages FROM analysis WHERE person_id = ?", []any{personID}, &at, &n) {
		return nil
	}
	return M{"at": at, "messages": n}
}

// AnalyseAgain: the person's chat to be read again by the local analysis, next time it runs.
func AnalyseAgain(s *Store, personID int64) error {
	return s.Write(func(tx *sql.Tx) error {
		db.Exec(tx, "DELETE FROM analysis WHERE person_id = ?", personID)
		return nil
	})
}

// ForgetAnalysis: everything the models said goes (their labels, their names, what they read); the
// user's word stays (their labels, and the names they turned down). It returns how many went.
func ForgetAnalysis(s *Store) (int64, error) {
	var n int64
	err := s.Write(func(tx *sql.Tx) error {
		n = db.Changed(tx, "DELETE FROM person_label WHERE state = 'suggested'")
		n += db.Changed(tx, "DELETE FROM name_guess WHERE how = 'models' AND NOT dismissed")
		db.Exec(tx, "DELETE FROM analysis")
		return nil
	})
	return n, err
}

// --- names for people without one ----------------------------------------------------------------

// soundKey is one word as it sounds, the ending's s off: a Greek first name, its Latin spelling and its other case are one.
func soundKey(word string) string {
	sk := skeleton(word)
	if len([]rune(sk)) > 3 && strings.HasSuffix(sk, "s") {
		return sk[:len(sk)-1]
	}
	return sk
}

var nameSplit = regexp.MustCompile(`[,\s\x0b\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{1c}-\x{1f}]+`)

func isAlpha(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

type counter struct {
	counts map[string]int
	order  []string
}

func (c *counter) add(w string) {
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	if _, ok := c.counts[w]; !ok {
		c.order = append(c.order, w)
	}
	c.counts[w]++
}

// mostCommon is Counter.most_common(1): the most counted, the first seen of equals.
func (c *counter) mostCommon() string {
	best := ""
	for i, w := range c.order {
		if i == 0 || c.counts[w] > c.counts[best] {
			best = w
		}
	}
	return best
}

type knownNames struct{ firsts, lasts map[string]string }

// known is the names of the people the archive names: {sound of a first name: its usual spelling},
// {sound of a surname: its usual spelling}, the owner's left out.
func known(s *Store) knownNames {
	return Cached(s, "known_names", func() knownNames {
		ppl := PeopleOf(s)
		own := map[string]bool{}
		for _, w := range pyFields(OwnName()) {
			own[soundKey(w)] = true
		}
		firsts, lasts := map[string]*counter{}, map[string]*counter{}
		var firstOrder, lastOrder []string
		for _, pid := range ppl.PeopleOrder {
			name, source := ppl.Info(pid)
			if ppl.Me[pid] || source == "handle" {
				continue
			}
			var words []string
			for _, w := range nameSplit.Split(name, -1) {
				if isAlpha(w) && len([]rune(w)) > 2 {
					words = append(words, w)
				}
			}
			if len(words) == 0 {
				continue
			}
			k := soundKey(words[0])
			if firsts[k] == nil {
				firsts[k] = &counter{}
				firstOrder = append(firstOrder, k)
			}
			firsts[k].add(words[0])
			if len(words) > 1 {
				w := words[len(words)-1]
				k := soundKey(w)
				if lasts[k] == nil {
					lasts[k] = &counter{}
					lastOrder = append(lastOrder, k)
				}
				lasts[k].add(w)
			}
		}
		pick := func(d map[string]*counter, order []string) map[string]string {
			out := map[string]string{}
			for _, k := range order {
				if !own[k] {
					out[k] = d[k].mostCommon()
				}
			}
			return out
		}
		return knownNames{pick(firsts, firstOrder), pick(lasts, lastOrder)}
	})
}

var notASCIILetters = regexp.MustCompile(`[^A-Za-z]+`)

// handleWords is the words of a handle: first.last, FirstL_82, last-first.
func handleWords(handle string) []string {
	var out []string
	isUp := func(b byte) bool { return b >= 'A' && b <= 'Z' }
	isLow := func(b byte) bool { return b >= 'a' && b <= 'z' }
	for _, part := range notASCIILetters.Split(handle, -1) {
		// [A-Z]?[a-z]+|[A-Z]+(?![a-z])
		for i := 0; i < len(part); {
			switch {
			case isUp(part[i]) && i+1 < len(part) && isLow(part[i+1]):
				j := i + 1
				for j < len(part) && isLow(part[j]) {
					j++
				}
				out = append(out, part[i:j])
				i = j
			case isLow(part[i]):
				j := i
				for j < len(part) && isLow(part[j]) {
					j++
				}
				out = append(out, part[i:j])
				i = j
			default: // a run of capitals not followed by a small letter
				j := i
				for j < len(part) && isUp(part[j]) {
					j++
				}
				if j < len(part) && isLow(part[j]) {
					j-- // the last capital starts the next word
				}
				out = append(out, part[i:j])
				i = j
			}
		}
	}
	var words []string
	for _, w := range out {
		if len(w) > 2 {
			words = append(words, strings.ToLower(w))
		}
	}
	return words
}

func title(w string) string {
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
}

// FromHandles is a name read from the person's handles (an email's or a username's words) where one
// is a first name the archive knows: the name and the handle it is read from, or "".
func FromHandles(s *Store, personID int64) (string, string) {
	kn := known(s)
	for _, h := range PeopleOf(s).Handles[personID] {
		if h.Kind != "email" && h.Kind != "username" {
			continue
		}
		words := handleWords(strings.SplitN(h.Value, "@", 2)[0])
		first := ""
		for _, w := range words {
			if _, ok := kn.firsts[soundKey(w)]; ok {
				first = w
				break
			}
		}
		if first == "" {
			continue
		}
		var rest []string
		for _, w := range words {
			if w != first && isAlpha(w) && len(w) > 3 {
				rest = append(rest, w)
			}
		}
		for _, w := range rest {
			if l, ok := kn.lasts[soundKey(w)]; ok {
				return kn.firsts[soundKey(first)] + " " + l, h.Value
			}
		}
		if len(rest) > 0 { // a surname the archive does not know: both as written
			return title(first) + " " + title(rest[0]), h.Value
		}
		return kn.firsts[soundKey(first)], h.Value
	}
	return "", ""
}

// Guess is the name suggested for a person without one, or nil: the models' (when they agree),
// else one read from their handles; one the user turned down is not suggested again.
// {name, how, votes, models, evidence}
func Guess(s *Store, personID int64) M {
	if !unnamedPeople(s).People[personID] {
		return nil
	}
	type row struct {
		name      string
		votes     sql.NullInt64
		models    sql.NullInt64
		ev        sql.NullString
		dismissed bool
	}
	rows := map[string]row{}
	db.Each(s.Read(), "SELECT how, name, votes, models, evidence, dismissed FROM name_guess WHERE person_id = ?",
		[]any{personID}, func(scan func(...any)) {
			var how string
			var r row
			scan(&how, &r.name, &r.votes, &r.models, &r.ev, &r.dismissed)
			rows[how] = r
		})
	if m, ok := rows["models"]; ok && !m.dismissed {
		return M{"name": m.name, "how": "models", "votes": nullInt(m.votes), "models": nullInt(m.models), "evidence": nullString(m.ev)}
	}
	if name, from := FromHandles(s, personID); name != "" {
		h, ok := rows["handle"]
		if !(ok && h.dismissed && h.name == name) {
			return M{"name": name, "how": "handle", "votes": nil, "models": nil, "evidence": from}
		}
	}
	return nil
}

// DecideGuess is the user's word on a suggested name: accepted, it is their name; turned down, it
// is not suggested again (until another name is found). It returns the name.
func DecideGuess(s *Store, personID int64, how string, accept bool) (string, error) {
	g := Guess(s, personID)
	if g == nil || g["how"] != how {
		return "", errs.New("people.no_guess", 0, nil)
	}
	name := g["name"].(string)
	err := s.Write(func(tx *sql.Tx) error {
		if accept {
			db.Exec(tx, "UPDATE person SET name = ? WHERE id = ?", name, personID)
		}
		db.Exec(tx, "INSERT INTO name_guess (person_id, how, name, votes, models, evidence, dismissed, at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (person_id, how) DO UPDATE SET "+
			"name = excluded.name, dismissed = excluded.dismissed, at = excluded.at",
			personID, how, name, g["votes"], g["models"], g["evidence"], db.B(!accept), time.Now().Unix())
		return nil
	})
	return name, err
}

// Guessed is the people without a name who have a suggested one.
func Guessed(s *Store) map[int64]bool {
	return Cached(s, "guessed", func() map[int64]bool {
		out := map[int64]bool{}
		act := active(s)
		for pid := range unnamedPeople(s).People {
			if act[pid] && Guess(s, pid) != nil {
				out[pid] = true
			}
		}
		return out
	})
}

// --- what the local analysis reads ---------------------------------------------------------------

// ToRead is a person to read, and how many messages their chat has.
type ToRead struct {
	PersonID int64
	Messages int64
}

// ToAnalyse is the people whose chats the local analysis has yet to read, the largest first: those
// it has not read, and those whose chat has grown by half since.
func ToAnalyse(s *Store, onlyUnnamed bool, minMessages int64) []ToRead {
	ix := Index(s)
	perConv := conversationSizes(s)
	done := map[int64]int64{}
	db.Each(s.Read(), "SELECT person_id, messages FROM analysis", nil, func(scan func(...any)) {
		var p, n int64
		scan(&p, &n)
		done[p] = n
	})
	ppl := PeopleOf(s)
	act := active(s)
	var who []int64
	if onlyUnnamed {
		for pid := range unnamedPeople(s).People {
			who = append(who, pid)
		}
	} else {
		for pid := range ppl.Handles {
			if !ppl.Me[pid] {
				who = append(who, pid)
			}
		}
	}
	// Python goes through a set (its order is not ours); ties go by person id here
	sort.Slice(who, func(i, j int) bool { return who[i] < who[j] })
	var out []ToRead
	for _, pid := range who {
		if !act[pid] {
			continue
		}
		var n int64
		if c := ix.Chats[fmt.Sprintf("p%d", pid)]; c != nil {
			for _, cid := range c.Conversations {
				n += perConv[cid]
			}
		}
		d, read := done[pid]
		if n >= minMessages && (!read || float64(n) >= float64(d)*1.5) {
			out = append(out, ToRead{pid, n})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Messages > out[j].Messages })
	return out
}

// MessagesOf is how many messages a person's chat has.
func MessagesOf(s *Store, personID int64) int64 {
	c := Index(s).Chats[fmt.Sprintf("p%d", personID)]
	if c == nil || len(c.Conversations) == 0 {
		return 0
	}
	return db.Int(s.Read(), "SELECT count(*) FROM message WHERE conversation_id IN ("+db.Marks(len(c.Conversations))+")",
		db.Args(c.Conversations)...)
}

// Judged is what the models said of one label: the label, votes of how many, and a line that shows it.
type Judged struct {
	LabelID   int64
	Votes, Of int
	Evidence  string
}

// NameJudged is the name the models found: votes of how many, and the phrase that shows it.
type NameJudged struct {
	Name      string
	Votes, Of int
	Evidence  string
}

// SaveAnalysis records what the models made of a person's chat. Their earlier suggestions for the
// person go; the user's word stays, and a label they said no to is not suggested.
func SaveAnalysis(s *Store, personID, messages int64, models []string, name *NameJudged, tones []Judged, relation *Judged) error {
	now := time.Now().Unix()
	digest := Digest(s)
	return s.Write(func(tx *sql.Tx) error {
		if !db.Exists(tx, "SELECT 1 FROM person WHERE id = ?", personID) {
			return nil // merged away while it was read
		}
		db.Exec(tx, "DELETE FROM person_label WHERE person_id = ? AND state = 'suggested'", personID)
		if relation != nil && db.Exists(tx, "SELECT 1 FROM person_label pl JOIN label l ON l.id = pl.label_id "+
			"WHERE pl.person_id = ? AND pl.state = 'yes' AND l.kind = 'relation'", personID) {
			relation = nil // the user has said who they are
		}
		all := append([]Judged{}, tones...)
		if relation != nil {
			all = append(all, *relation)
		}
		for _, j := range all {
			db.Exec(tx, "INSERT OR IGNORE INTO person_label (person_id, label_id, state, votes, models, evidence, at) "+
				"VALUES (?, ?, 'suggested', ?, ?, ?, ?)", personID, j.LabelID, j.Votes, j.Of, db.NullStr(j.Evidence), now)
		}
		var oldName sql.NullString
		var oldDismissed bool
		had := db.Row(tx, "SELECT name, dismissed FROM name_guess WHERE person_id = ? AND how = 'models'",
			[]any{personID}, &oldName, &oldDismissed)
		if name != nil && !(had && oldDismissed && soundKey(oldName.String) == soundKey(name.Name)) { // not one turned down again
			db.Exec(tx, "INSERT OR REPLACE INTO name_guess (person_id, how, name, votes, models, evidence, dismissed, at) "+
				"VALUES (?, 'models', ?, ?, ?, ?, 0, ?)", personID, name.Name, name.Votes, name.Of, db.NullStr(name.Evidence), now)
		} else if name == nil && had && !oldDismissed {
			db.Exec(tx, "DELETE FROM name_guess WHERE person_id = ? AND how = 'models'", personID)
		}
		db.Exec(tx, "INSERT OR REPLACE INTO analysis VALUES (?, ?, ?, ?, ?)", personID, messages, digest,
			strings.Join(models, ","), now)
		return nil
	})
}

// Stale is how many people were read by other lists of labels than today's.
func Stale(s *Store) int64 {
	return db.Int(s.Read(), "SELECT count(*) FROM analysis WHERE labels != ?", Digest(s))
}

// JudgeAgain: the people read by other lists of labels, to be read again; it returns how many.
func JudgeAgain(s *Store) (int64, error) {
	d := Digest(s)
	var n int64
	err := s.Write(func(tx *sql.Tx) error {
		n = db.Changed(tx, "DELETE FROM analysis WHERE labels != ?", d)
		return nil
	})
	return n, err
}
