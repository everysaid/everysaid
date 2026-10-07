// Ports the core's part of tests/test_labels.py (the analysis plugin's part is in its package).
package core_test

import (
	"database/sql"
	"reflect"
	"sort"
	"testing"

	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
)

func byKey(s *core.Store) map[string]core.M {
	out := map[string]core.M{}
	for _, lb := range core.Labels(s, "") {
		if k, ok := lb["key"].(string); ok {
			out[k] = lb
		}
	}
	return out
}

func lid(m core.M) int64 { return i64(m["id"]) }

// someone is the first person (by id) who is named, or not.
func someone(s *core.Store, unnamed bool) int64 {
	ppl := core.PeopleOf(s)
	var pids []int64
	for pid := range ppl.Handles {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	for _, pid := range pids {
		_, src := ppl.Info(pid)
		if !ppl.Me[pid] && (src == "handle") == unnamed {
			return pid
		}
	}
	return 0
}

func knownByEmail(s *core.Store) int64 {
	ppl := core.PeopleOf(s)
	for pid, hs := range ppl.Handles {
		for _, h := range hs {
			if h.Value == "katerina.oikonomou@example.com" {
				return pid
			}
		}
	}
	return 0
}

func states(s *core.Store, pid int64, suggested bool) map[string]string {
	out := map[string]string{}
	for _, x := range core.PersonLabels(s, pid, suggested) {
		out[x["key"].(string)] = x["state"].(string)
	}
	return out
}

func code(err error) string {
	if e, ok := err.(*errs.UserError); ok {
		return e.Code
	}
	return ""
}

func TestTheListsTheAppStartsWith(t *testing.T) {
	s := store(t)
	keys := byKey(s)
	for _, k := range []string{"friendly", "professional", "romantic", "friend", "client"} {
		if keys[k] == nil {
			t.Fatal("missing", k)
		}
	}
	if keys["sexual"] != nil { // one with the romantic
		t.Fatal("sexual")
	}
	if !keys["romantic"]["sensitive"].(bool) || keys["friendly"]["sensitive"].(bool) {
		t.Fatal("sensitive")
	}
	if keys["romantic"]["kind"] != "tone" || keys["friend"]["kind"] != "relation" {
		t.Fatal("kinds")
	}
	words := map[string]bool{}
	for _, l := range core.ForModels(s, "tone") {
		words[l.Word] = true
	}
	if !words["romantic"] || words["friend"] {
		t.Fatal(words)
	}
}

func TestTheUserShapesTheLists(t *testing.T) {
	s := store(t)
	keys := byKey(s)
	mine, _ := core.AddLabel(s, "tone", "Acme", "work talk about the Acme project", false)
	quiet, _ := core.AddLabel(s, "tone", "Holidays", "", false) // no meaning: never the models'
	words := map[string]string{}
	for _, l := range core.ForModels(s, "tone") {
		words[l.Word] = l.Meaning
	}
	if words["Acme"] != "work talk about the Acme project" {
		t.Fatal(words)
	}
	if _, ok := words["Holidays"]; ok {
		t.Fatal("Holidays for the models")
	}
	before := core.Digest(s)
	formal := lid(keys["formal"])
	core.EditLabel(s, formal, core.To("Ψυχρή"), core.To("cold"), core.Opt[bool]{})
	if core.Digest(s) == before {
		t.Fatal("digest unchanged")
	}
	get := func(id int64) core.M {
		for _, x := range core.Labels(s, "") {
			if lid(x) == id {
				return x
			}
		}
		return nil
	}
	if lb := get(formal); lb["name"] != "Ψυχρή" || lb["meaning"] != "cold" {
		t.Fatal(lb)
	}
	core.EditLabel(s, formal, core.Clear[string](), core.Clear[string](), core.Opt[bool]{}) // back to the app's
	if lb := get(formal); lb["name"] != nil || lb["meaning"] != nil {
		t.Fatal(lb)
	}
	if _, err := core.AddLabel(s, "tone", "acme", "", false); code(err) != "labels.exists" {
		t.Fatal("the same name twice:", err)
	}
	var order []int64
	order = append(order, quiet)
	for _, x := range core.Labels(s, "tone") {
		if lid(x) != quiet {
			order = append(order, lid(x))
		}
	}
	core.OrderLabels(s, order)
	if lid(core.Labels(s, "tone")[0]) != quiet {
		t.Fatal("order")
	}
	core.RemoveLabel(s, mine)
	if get(mine) != nil {
		t.Fatal("not removed")
	}
}

func j(label core.M, votes, of int, ev string) core.Judged {
	return core.Judged{LabelID: lid(label), Votes: votes, Of: of, Evidence: ev}
}

func TestMergingLabelsKeepsTheUsersWord(t *testing.T) {
	s := store(t)
	keys := byKey(s)
	a, b := someone(s, false), someone(s, true)
	core.SaveAnalysis(s, a, 50, []string{"m"}, nil, []core.Judged{j(keys["romantic"], 2, 3, "a line")}, nil)
	core.SetPersonLabel(s, a, lid(keys["personal"]), "yes")
	core.SaveAnalysis(s, b, 50, []string{"m"}, nil, []core.Judged{j(keys["personal"], 2, 3, "")}, nil)
	// "romantic" into "personal": a's own yes stays, b's suggestion stays a suggestion
	if err := core.MergeLabels(s, lid(keys["personal"]), lid(keys["romantic"])); err != nil {
		t.Fatal(err)
	}
	if byKey(s)["romantic"] != nil {
		t.Fatal("romantic still there")
	}
	if got := states(s, a, true); !reflect.DeepEqual(got, map[string]string{"personal": "yes"}) {
		t.Fatal(got)
	}
	if got := states(s, b, true); !reflect.DeepEqual(got, map[string]string{"personal": "suggested"}) {
		t.Fatal(got)
	}
	if err := core.MergeLabels(s, lid(keys["friend"]), lid(keys["friendly"])); code(err) != "labels.other_kind" {
		t.Fatal("a tone is not a relation:", err)
	}
}

func TestAPersonsLabelsAndTheModelsSuggestions(t *testing.T) {
	s := store(t)
	keys := byKey(s)
	pid := someone(s, false)
	core.SetPersonLabel(s, pid, lid(keys["friend"]), "yes")
	core.SetPersonLabel(s, pid, lid(keys["colleague"]), "yes") // one relation: the last
	core.SetPersonLabel(s, pid, lid(keys["formal"]), "no")
	if got := states(s, pid, true); !reflect.DeepEqual(got, map[string]string{"colleague": "yes"}) { // "no" is not shown
		t.Fatal(got)
	}
	// the models: their relation gives way to the user's, a "no" is not suggested again
	rel := j(keys["friend"], 2, 2, "")
	core.SaveAnalysis(s, pid, 40, []string{"m1", "m2"}, nil,
		[]core.Judged{j(keys["formal"], 2, 2, ""), j(keys["friendly"], 2, 2, "")}, &rel)
	if got := states(s, pid, true); !reflect.DeepEqual(got, map[string]string{"colleague": "yes", "friendly": "suggested"}) {
		t.Fatal(got)
	}
	if got := states(s, pid, false); !reflect.DeepEqual(got, map[string]string{"colleague": "yes"}) {
		t.Fatal(got)
	}
	if a := core.Analysed(s, pid); a == nil || i64(a["messages"]) != 40 {
		t.Fatal(a)
	}
	core.ForgetAnalysis(s) // forgetting the analysis keeps the user's word
	if got := states(s, pid, true); !reflect.DeepEqual(got, map[string]string{"colleague": "yes"}) {
		t.Fatal(got)
	}
	if core.Analysed(s, pid) != nil {
		t.Fatal("still analysed")
	}
	core.SetPersonLabel(s, pid, lid(keys["colleague"]), "")
	if len(core.PersonLabels(s, pid, true)) != 0 {
		t.Fatal("labels left")
	}
}

func TestLabelsFollowAMergeAndASplit(t *testing.T) {
	s := store(t)
	keys := byKey(s)
	k := knownByEmail(s)
	other := someone(s, true)
	core.SaveAnalysis(s, k, 24, []string{"m"}, &core.NameJudged{Name: "Κατερίνα", Votes: 1, Of: 1, Evidence: "Κατερίνα, τα λέμε"},
		[]core.Judged{j(keys["professional"], 1, 1, "")}, nil)
	core.SetPersonLabel(s, k, lid(keys["friend"]), "yes")
	core.MergePeople(s, other, k)
	if got := states(s, other, true); !reflect.DeepEqual(got, map[string]string{"friend": "yes", "professional": "suggested"}) {
		t.Fatal(got)
	}
	if core.Analysed(s, other) != nil { // their chat is one now: to be read again
		t.Fatal("analysed after a merge")
	}
	if g := core.Guess(s, other); g == nil || g["how"] != "models" {
		t.Fatal(g)
	}
	core.SaveAnalysis(s, other, 30, []string{"m"}, nil, nil, nil)
	core.SplitAddress(s, core.PeopleOf(s).Addresses(other)[0])
	if core.Analysed(s, other) != nil {
		t.Fatal("analysed after a split")
	}
}

func TestANameFromAnEmail(t *testing.T) {
	s := store(t)
	k := knownByEmail(s)
	g := core.Guess(s, k)
	want := core.M{"name": "Κατερίνα Οικονόμου", "how": "handle", "votes": nil, "models": nil,
		"evidence": "katerina.oikonomou@example.com"}
	if !reflect.DeepEqual(g, want) {
		t.Fatal(g)
	}
	if !core.Guessed(s)[k] {
		t.Fatal("not guessed")
	}
	core.DecideGuess(s, k, "handle", false) // wrong: not suggested again
	if core.Guess(s, k) != nil {
		t.Fatal("suggested again")
	}
	// the models find one: it is suggested before the handle's
	core.SaveAnalysis(s, k, 24, []string{"a", "b"}, &core.NameJudged{Name: "Κατερίνα", Votes: 2, Of: 2, Evidence: "Κατερίνα, τα λέμε αύριο"}, nil, nil)
	if g := core.Guess(s, k); g == nil || g["name"] != "Κατερίνα" {
		t.Fatal(g)
	}
	if name, err := core.DecideGuess(s, k, "models", true); err != nil || name != "Κατερίνα" {
		t.Fatal(name, err)
	}
	if core.PeopleOf(s).Name(k) != "Κατερίνα" || core.Guess(s, k) != nil { // named now
		t.Fatal("not named")
	}
	first := someone(s, true)
	r := core.UnnamedPeople(s, 0, 0, map[int64]bool{first: true})
	if i64(r["items"].([]core.M)[0]["id"]) != first {
		t.Fatal("first not first")
	}
}

func TestHandlesWords(t *testing.T) {
	if got := core.HandleWords("maria.eleni"); !reflect.DeepEqual(got, []string{"maria", "eleni"}) {
		t.Fatal(got)
	}
	if got := core.HandleWords("MariaK_82"); !reflect.DeepEqual(got, []string{"maria"}) {
		t.Fatal(got)
	}
	if got := core.HandleWords("nick80"); !reflect.DeepEqual(got, []string{"nick"}) {
		t.Fatal(got)
	}
	if core.SoundKey("Γιώργος") != core.SoundKey("giorgos") || core.SoundKey("giorgos") != core.SoundKey("Γιώργο") {
		t.Fatal(core.SoundKey("Γιώργος"), core.SoundKey("giorgos"), core.SoundKey("Γιώργο"))
	}
}

func TestWhatTheAnalysisReads(t *testing.T) {
	s := store(t)
	k := knownByEmail(s)
	todo := map[int64]int64{}
	for _, r := range core.ToAnalyse(s, true, 20) {
		todo[r.PersonID] = r.Messages
	}
	if todo[k] != 24 {
		t.Fatal(todo[k])
	}
	for _, n := range todo {
		if n < 20 {
			t.Fatal("too few", n)
		}
	}
	in := func() bool {
		for _, r := range core.ToAnalyse(s, true, 20) {
			if r.PersonID == k {
				return true
			}
		}
		return false
	}
	core.SaveAnalysis(s, k, 24, []string{"m"}, nil, nil, nil)
	if in() {
		t.Fatal("read again at once")
	}
	write(t, s, func(tx *sql.Tx) { db.Exec(tx, "UPDATE analysis SET messages = 10 WHERE person_id = ?", k) }) // it grew
	if !in() {
		t.Fatal("grown, not read again")
	}
	write(t, s, func(tx *sql.Tx) {
		db.Exec(tx, "UPDATE analysis SET messages = 24, labels = 'old' WHERE person_id = ?", k)
	})
	if core.Stale(s) != 1 {
		t.Fatal(core.Stale(s))
	}
	if n, _ := core.JudgeAgain(s); n != 1 || !in() {
		t.Fatal(n)
	}
}
