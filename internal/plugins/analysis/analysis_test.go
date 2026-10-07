package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/plugins"
)

type M = plugins.M

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "everysaid-analysis-test")
	for k, v := range map[string]string{"EVERYSAID_DATA": "data", "EVERYSAID_CACHE": "cache",
		"EVERYSAID_CONFIG": "config", "EVERYSAID_STATE": "state"} {
		os.Setenv(k, filepath.Join(dir, v))
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test-analysis")
	config.Load()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestOnlyALocalAddress(t *testing.T) {
	for u, want := range map[string]bool{"http://localhost:11434": true, "http://127.0.0.1:11434": true,
		"http://192.168.0.10:11434": true, "http://[::1]:11434": true, "http://ollama.local:11434": true,
		"https://8.8.8.8/": false, "": false, "localhost:11434": false, "http://[2001:4860::8888]/": false} {
		if LocalAddress(u) != want {
			t.Errorf("%q: %v", u, !want)
		}
	}
}

var (
	tones = []core.ModelLabel{{ID: 1, Word: "friendly", Meaning: "friends"}, {ID: 2, Word: "romantic", Meaning: "love", Sensitive: true},
		{ID: 3, Word: "professional", Meaning: "work"}}
	relations = []core.ModelLabel{{ID: 10, Word: "friend", Meaning: "a friend"}, {ID: 11, Word: "colleague", Meaning: "a colleague"}}
)

const voteText = "ME: Έλα Γιώργο, τι λες; THEM: Γιώργος Νικόλας εδώ. ME: σ' αγαπώ πολύ μωρό μου"

func answers(js string) []map[string]any {
	var out []map[string]any
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		panic(err)
	}
	return out
}

func TestTheModelsVote(t *testing.T) {
	own := map[string]bool{core.SoundKey("Petros"): true}
	as := answers(`[
		{"name": "Γιώργο", "evidence": "Έλα Γιώργο", "tone": ["friendly", "romantic"],
		 "sensitive_evidence": "σ' αγαπώ πολύ μωρό μου", "relationship": "friend"},
		{"name": "Γιώργος Νικόλας", "evidence": "Γιώργος Νικόλας εδώ", "tone": ["friendly", "romantic"],
		 "sensitive_evidence": "an invented line", "relationship": "friend"},
		{"name": "Γιώργος Νικόλας", "evidence": "Γιώργος Νικόλας εδώ", "tone": ["professional", "romantic"],
		 "sensitive_evidence": "σ' αγαπώ πολύ μωρό μου", "relationship": "colleague"}]`)
	name, chosen, relation := Vote(as, voteText, []string{"giorgos@x.org"}, own, tones, relations)
	if *name != (core.NameJudged{Name: "Γιώργος Νικόλας", Votes: 3, Of: 3, Evidence: "Γιώργος Νικόλας εδώ"}) {
		t.Fatalf("%+v", name)
	}
	// friendly: 2 of 3; romantic: two copied the line (the invented one does not count); professional: 1
	var ids []int64
	evidence := map[int64]string{}
	for _, j := range chosen {
		ids = append(ids, j.LabelID)
		evidence[j.LabelID] = j.Evidence
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if !reflect.DeepEqual(ids, []int64{1, 2}) || evidence[2] != "σ' αγαπώ πολύ μωρό μου" {
		t.Fatalf("%+v", chosen)
	}
	if *relation != (core.Judged{LabelID: 10, Votes: 2, Of: 3}) {
		t.Fatalf("%+v", relation)
	}
	// one model: a sensitive tone is never its alone
	name, chosen, relation = Vote(as[:1], voteText, nil, own, tones, relations)
	if name.Name != "Γιώργο" || len(chosen) != 1 || chosen[0].LabelID != 1 || relation.LabelID != 10 {
		t.Fatalf("%+v %+v %+v", name, chosen, relation)
	}
	// not a name: an email, a handle, a number, the owner's, one not in the texts
	for _, bad := range []any{"giorgos@x.org", "Petros", "Μαρία", "user_123", nil, "null", 7} {
		if n, _, _ := Vote([]map[string]any{{"name": bad}}, voteText+" Petros", []string{"giorgos@x.org"}, own, nil, nil); n != nil {
			t.Errorf("%v: %+v", bad, n)
		}
	}
}

func TestSchema(t *testing.T) {
	s := Schema("both", tones, relations)
	b, _ := json.Marshal(s)
	want := `{"properties":{"evidence":{"type":["string","null"]},"name":{"type":["string","null"]},` +
		`"relationship":{"enum":["friend","colleague"],"type":"string"},"sensitive_evidence":{"type":["string","null"]},` +
		`"tone":{"items":{"enum":["friendly","romantic","professional"],"type":"string"},"type":"array"}},` +
		`"required":["name","evidence","tone","sensitive_evidence","relationship"],"type":"object"}`
	if string(b) != want {
		t.Fatal(string(b))
	}
	if b, _ := json.Marshal(Schema("name", nil, nil)); !strings.Contains(string(b), `"required":["name","evidence"]`) {
		t.Fatal(string(b))
	}
	if b, _ := json.Marshal(Schema("tone", nil, nil)); string(b) != `{"properties":{},"required":[],"type":"object"}` {
		t.Fatal(string(b))
	}
}

type host struct{ store *core.Store }

func (h *host) Store() *core.Store { return h.store }
func (h *host) Emit(M)             {}
func (h *host) Alert(_, _ string)  {}

// knownByEmail is an archive where one person, known only by an email, wrote 24 messages, and an
// instance of the plugin on an Ollama of the test.
func knownByEmail(t *testing.T, url string) (*core.Store, *plugins.Context, int64) {
	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	src := a.Source("test", "/x", "", "")
	h := archive.H("email", "katerina.oikonomou@example.com")
	conv := a.Conversation("imessage", []archive.Handle{h}, "", "")
	sender := a.Address(h)
	base := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC).UnixMilli()
	for i := 0; i < 24; i++ {
		text := fmt.Sprintf("μήνυμα %d για τη δουλειά", i)
		if i == 13 {
			text = "Κατερίνα, τα λέμε αύριο"
		}
		a.AddMessage(src, fmt.Sprint(i), archive.Message{Service: "imessage", ConversationID: conv, TS: base + int64(i)*60000,
			Outgoing: i%2 == 0, SenderID: map[bool]int64{true: 0, false: sender}[i%2 == 0], Kind: "text", Text: text})
	}
	a.Imported(src)
	a.Resolve()
	a.Commit()
	a.Close()
	s, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	iid, err := plugins.Create(s, "ollama", "Local", M{"url": url, "models": "a, b, c, d"})
	if err != nil {
		t.Fatal(err)
	}
	pid := core.PeopleOf(s).PersonOf[sender]
	return s, plugins.NewContext(&host{s}, *plugins.GetInstance(s, iid)), pid
}

func TestThePluginReadsAndSuggests(t *testing.T) {
	var mu sync.Mutex
	var asked []M
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		asked = append(asked, req)
		mu.Unlock()
		if r.URL.Path != "/api/chat" || req["model"] == "c" {
			w.WriteHeader(404)
			return
		}
		answer, _ := json.Marshal(M{"name": "Κατερίνα", "evidence": "Κατερίνα, τα λέμε αύριο", "tone": []string{"professional"},
			"sensitive_evidence": nil, "relationship": "colleague"})
		json.NewEncoder(w).Encode(M{"model": req["model"], "message": M{"role": "assistant", "content": string(answer)}, "done": true})
	}))
	defer srv.Close()
	s, c, k := knownByEmail(t, srv.URL)
	p := Ollama{}
	if ok, why := plugins.Check(p, c); !ok {
		t.Fatal(why)
	}
	if todo := Todo(c); len(todo) != 1 || todo[0] != (core.ToRead{PersonID: k, Messages: 24}) {
		t.Fatalf("%+v", todo)
	}
	if models := Models(c); !reflect.DeepEqual(models, []string{"a", "b", "c"}) {
		t.Fatal(models) // three at most
	}
	more, err := p.Step(c)
	if !more || err != nil {
		t.Fatal(more, err)
	}
	if len(asked) != 3 {
		t.Fatal(len(asked))
	}
	req := asked[0]
	if req["stream"] != false || req["think"] != false || req["options"].(M)["temperature"] != float64(0) {
		t.Fatal(req)
	}
	enum := req["format"].(M)["properties"].(M)["tone"].(M)["items"].(M)["enum"].([]any)
	if !strings.Contains(fmt.Sprint(enum), "romantic") {
		t.Fatal(enum)
	}
	prompt := req["messages"].([]any)[0].(M)["content"].(string)
	if !strings.Contains(prompt, "Their handles: katerina.oikonomou@example.com") ||
		!strings.Contains(prompt, "THEM: Κατερίνα, τα λέμε αύριο") || !strings.Contains(prompt, "[2025-03-01] ME: ") {
		t.Fatal(prompt)
	}
	if !strings.Contains(strings.Join(c.LastLines(5), "\n"), "c: Ollama has no model c") {
		t.Fatal(c.LastLines(5))
	}
	g := core.Guess(s, k)
	if g["name"] != "Κατερίνα" || g["votes"] != int64(2) || g["models"] != int64(2) {
		t.Fatal(g)
	}
	var keys []string
	for _, l := range core.PersonLabels(s, k, true) {
		keys = append(keys, l["key"].(string))
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"colleague", "professional"}) {
		t.Fatal(keys)
	}
	if len(Todo(c)) != 0 {
		t.Fatal("read again")
	}
	if f := p.InfoFacts(c); f[0] != (plugins.Fact{Label: "Read", Value: "1"}) || f[1].Value != "0" || f[2].Value != "1" {
		t.Fatal(f)
	}
	if idle := p.IdleActions(c); !reflect.DeepEqual(idle, []string{"again"}) {
		t.Fatal(idle)
	}
	if err := p.Action(c, "forget"); err != nil {
		t.Fatal(err)
	}
	if len(core.PersonLabels(s, k, true)) != 0 || core.Guess(s, k)["how"] == "models" {
		t.Fatal(core.Guess(s, k))
	}
	if idle := p.IdleActions(c); !reflect.DeepEqual(idle, []string{"again", "forget"}) {
		t.Fatal(idle)
	}
	// one person now, asked from their chat
	if err := p.Action(c, fmt.Sprintf("person:%d", k)); err != nil {
		t.Fatal(err)
	}
	if core.Analysed(s, k) == nil {
		t.Fatal("not read")
	}
	if err := p.Action(c, "again"); err != nil {
		t.Fatal(err)
	}
	if err := p.Action(c, "nothing"); err == nil {
		t.Fatal("an unknown action")
	}
	// all down: no model answered
	srv.Close()
	core.AnalyseAgain(s, k)
	if _, err := p.Step(c); err == nil {
		t.Fatal("no model answered, yet no failure")
	}
}

func TestNotOnAnotherNetwork(t *testing.T) {
	_, c, k := knownByEmail(t, "http://8.8.8.8:11434")
	if ok, why := plugins.Check(Ollama{}, c); ok || why != "Ollama's address is not on this computer or its network" {
		t.Fatal(why)
	}
	asked := false
	old := ask
	ask = func(*plugins.Context, string, string, map[string]any) (map[string]any, error) {
		asked = true
		return nil, nil
	}
	defer func() { ask = old }()
	if err := (Ollama{}).Analyse(c, k, 24); err == nil || asked {
		t.Fatal("asked a model out of the network")
	}
}

func TestTheLiveLoop(t *testing.T) {
	_, c, _ := knownByEmail(t, "http://localhost:1")
	n := 0
	old := step
	step = func(Ollama, *plugins.Context) (bool, error) { n++; return n < 3, nil }
	defer func() { step = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := (Ollama{}).Live(ctx, c); err != nil || n != 3 {
		t.Fatal(err, n)
	}
}

func TestExcerpt(t *testing.T) {
	s, _, k := knownByEmail(t, "http://localhost:1")
	rows := Excerpt(s, k, map[string]string{core.SoundKey("Κατερίνα"): "Κατερίνα"})
	var has bool
	for _, r := range rows {
		has = has || r.text == "Κατερίνα, τα λέμε αύριο"
	}
	if !has || len(rows) < 10 || len(rows) > 24 {
		t.Fatal(len(rows), has)
	}
	if core.SoundKey("Γιώργος") != core.SoundKey("giorgos") || core.SoundKey("Γιώργο") != core.SoundKey("giorgos") {
		t.Fatal(core.SoundKey("Γιώργος"), core.SoundKey("giorgos"))
	}
	if Excerpt(s, k+1000, nil) != nil {
		t.Fatal("no chat, no lines")
	}
	if !shown("Γιάννης", "ο Γιάννη ήρθε") || shown("Μαρία", "ο Γιάννης") || !copied("τι  λες ρε", "ΤΙ ΛΕΣ ΡΕ φίλε") || copied("λες", "λες") {
		t.Fatal("shown, copied")
	}
}

// What the models are sent goes to the address checked and no further: not after a redirect, and
// not to a connection that is not local (the name may point elsewhere by the time it connects).
func TestAskStaysLocal(t *testing.T) {
	reached := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect) // the body goes along
	}))
	defer srv.Close()
	_, c, _ := knownByEmail(t, srv.URL)
	if _, err := ask(c, "a", "a private chat", M{}); err == nil || reached {
		t.Fatalf("the chat followed a redirect (%v)", err)
	}
	for address, ok := range map[string]bool{"127.0.0.1:11434": true, "[::1]:11434": true, "192.168.0.10:11434": true,
		"[fe80::1]:11434": true, "203.0.113.9:11434": false, "[2001:db8::1]:11434": false, "[::ffff:8.8.8.8]:80": false} {
		if err := onlyLocal("tcp", address, nil); (err == nil) != ok {
			t.Errorf("%s: %v", address, err)
		}
	}
}
