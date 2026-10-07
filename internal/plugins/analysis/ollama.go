package analysis

// Ports Ollama, excerpt and mentions of everysaid/plugins/analysis.py.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
)

const prompt = `You read part of a private chat archive. The archive's owner is {owner}; lines marked ME are
the owner's, lines marked THEM are the other person's.
{asks}
Their handles: {handles}
Names they have shown: {aka}

How others name them in groups: {mentions}

Messages:
{lines}`

const askName = `Find the OTHER person's real name (first name, and surname if the texts give it): how the owner
calls them at the start of a message, how they sign or introduce themselves, how others name them in
groups, or a handle that is clearly a name. Give it in the nominative, as written in the chat's
language. The owner's own name is never the answer. If no line shows their name, name is null: do
not guess, do not invent, a common name that does not appear in the lines is wrong. evidence: the
exact short phrase, copied from the lines, that shows it.
`

const askTone = `Judge the chat: its tone (one or more of the list, the main one first) and who the person is to
the owner (relationship, one of the list). A tone marked sensitive only when lines clearly show it,
and then copy one such line, exactly, into sensitive_evidence (else null).
Tones:
{tones}
Relationships:
{relations}
`

// Ollama is the `ollama` analysis plugin.
type Ollama struct{}

func init() { plugins.Register(Ollama{}) }

func (Ollama) Info() *plugins.Info {
	return &plugins.Info{
		ID: "ollama", Name: "Local analysis (Ollama)", Kind: "analysis",
		Description: "Local models read the chats of people without a name and suggest who they are, and the " +
			"chats' tone. Nothing leaves this computer and its network; nothing is applied without you.",
		Modes: []string{"live"},
		Needs: []string{"Ollama, with a model"},
		Settings: []plugins.Setting{
			{Key: "url", Label: "Ollama's address", Type: "url", Required: true, Default: "http://localhost:11434",
				Help: "Only on this computer or its own network"},
			{Key: "models", Label: "Models", Type: "text", Required: true, Default: "qwen3:14b",
				Help: "One, or two or three separated by commas, to vote: e.g. qwen3:14b, gemma3:12b"},
			{Key: "what", Label: "What they look for", Type: "select", Default: "both",
				Options: []plugins.Option{{Value: "both", Label: "A name and the tone"}, {Value: "name", Label: "A name"},
					{Value: "tone", Label: "The tone"}}},
			{Key: "who", Label: "Whose chats", Type: "select", Default: "unnamed",
				Options: []plugins.Option{{Value: "unnamed", Label: "Those without a name"}, {Value: "all", Label: "Everyone's"}}},
			{Key: "min_messages", Label: "Messages at least", Type: "number", Default: 20},
		},
		Actions: []plugins.Action{{ID: "again", Label: "Judge again by today's labels"},
			{ID: "forget", Label: "Forget all the analysis"}},
	}
}

func (p Ollama) Check(c *plugins.Context) (bool, string) {
	ok, why := plugins.CheckSettings(p, c)
	if ok && !LocalAddress(c.Str("url")) {
		return false, "Ollama's address is not on this computer or its network"
	}
	return ok, why
}

// Models are the models that vote: up to three.
func Models(c *plugins.Context) []string {
	var out []string
	for _, m := range strings.Split(c.Str("models"), ",") {
		if m = strings.TrimSpace(m); m != "" && len(out) < 3 {
			out = append(out, m)
		}
	}
	return out
}

func what(c *plugins.Context) string {
	if w := c.Str("what"); w != "" {
		return w
	}
	return "both"
}

// Todo is the people to read, the largest chat first.
func Todo(c *plugins.Context) []core.ToRead {
	min := int64(c.Num("min_messages"))
	if min == 0 {
		min = 1
	}
	return core.ToAnalyse(c.Store(), c.Str("who") != "all", min)
}

func (Ollama) InfoFacts(c *plugins.Context) []plugins.Fact {
	s := c.Store()
	read := db.Int(s.Read(), "SELECT count(*) FROM analysis")
	names := db.Int(s.Read(), "SELECT count(*) FROM name_guess WHERE how = 'models' AND NOT dismissed")
	out := []plugins.Fact{{Label: "Read", Value: strconv.FormatInt(read, 10)},
		{Label: "To read", Value: strconv.Itoa(len(Todo(c)))},
		{Label: "Names found", Value: strconv.FormatInt(names, 10)}}
	if n := core.Stale(s); n > 0 {
		out = append(out, plugins.Fact{Label: "Read by an older list of labels", Value: strconv.FormatInt(n, 10)})
	}
	return out
}

func (Ollama) IdleActions(c *plugins.Context) []string {
	s := c.Store()
	out := []string{}
	if core.Stale(s) == 0 {
		out = append(out, "again")
	}
	if !(db.Exists(s.Read(), "SELECT 1 FROM analysis") ||
		db.Exists(s.Read(), "SELECT 1 FROM person_label WHERE state = 'suggested'") ||
		db.Exists(s.Read(), "SELECT 1 FROM name_guess WHERE how = 'models' AND NOT dismissed")) {
		out = append(out, "forget")
	}
	return out
}

func (p Ollama) Action(c *plugins.Context, name string) error {
	switch {
	case name == "forget":
		n, err := core.ForgetAnalysis(c.Store())
		if err != nil {
			return err
		}
		c.Log("the analysis is forgotten: {n} suggestions; your own labels stay", map[string]any{"n": n})
	case strings.HasPrefix(name, "person:"): // one person, now (asked from their chat)
		pid, err := strconv.ParseInt(strings.TrimPrefix(name, "person:"), 10, 64)
		if err != nil {
			return errs.Plugin("Unknown action", 400)
		}
		n := core.MessagesOf(c.Store(), pid)
		c.Log("reading {name} ({n} messages), {left} to go", map[string]any{
			"name": core.PeopleOf(c.Store()).Name(pid), "n": n, "left": 0})
		return p.Analyse(c, pid, n)
	case name == "again":
		n, err := core.JudgeAgain(c.Store())
		if err != nil {
			return err
		}
		c.Log("{n} people to read again; they are read while the analysis runs", map[string]any{"n": n})
	default:
		return errs.Plugin("Unknown action", 400)
	}
	return nil
}

// pause: nothing to read, a look again after it.
var pause = 300 * time.Second

func (p Ollama) Live(ctx context.Context, c *plugins.Context) error {
	for {
		more, err := step(p, c)
		if err != nil {
			return err
		}
		if !more {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(pause):
			}
		} else if ctx.Err() != nil {
			return nil
		}
	}
}

// step reads one person (a test's own stands in for it).
var step = func(p Ollama, c *plugins.Context) (bool, error) { return p.Step(c) }

// Step reads one person, the largest chat first: whether there was one.
func (p Ollama) Step(c *plugins.Context) (bool, error) {
	todo := Todo(c)
	if len(todo) == 0 {
		c.Redrawn(i18n.T("all read", c.Lang(), nil))
		return false, nil
	}
	t := todo[0]
	c.Redrawn(i18n.T("reading {name} ({n} messages), {left} to go", c.Lang(), map[string]any{
		"name": core.PeopleOf(c.Store()).Name(t.PersonID), "n": t.Messages, "left": len(todo)}))
	return true, p.Analyse(c, t.PersonID, t.Messages)
}

// line is a message as the models read it.
type line struct {
	ts       int64
	outgoing bool
	text     string
}

var (
	wordRE    = regexp.MustCompile(`[\p{L}\p{Nl}\p{No}]{3,}`)
	openingRE = regexp.MustCompile(`^[^\p{L}\p{N}_]*[\p{L}\p{N}_]+[ ,!]+([\p{L}\p{N}_])`)
)

// Excerpt is what the models read of a person's chat: its first lines, lines spread over all of
// it, and lines that name someone (a first name the archive knows, or the owner's openings: "Hey"
// and a name).
func Excerpt(s *core.Store, pid int64, known map[string]string) []line {
	const nEven, nNamed, nFirst = 25, 30, 10
	c := core.Index(s).Chats[fmt.Sprintf("p%d", pid)]
	if c == nil || len(c.Conversations) == 0 {
		return nil
	}
	var rows []line
	db.Each(s.Read(), "SELECT ts, outgoing, text FROM message WHERE conversation_id IN ("+db.Marks(len(c.Conversations))+") "+
		"AND text IS NOT NULL AND text != '' ORDER BY ts", db.Args(c.Conversations), func(scan func(...any)) {
		var l line
		scan(&l.ts, &l.outgoing, &l.text)
		rows = append(rows, l)
	})
	if len(rows) == 0 {
		return nil
	}
	chosen := map[int]bool{}
	for i := 0; i < min(nFirst, len(rows)); i++ {
		chosen[i] = true
	}
	for i := 0; i < len(rows); i += max(1, len(rows)/nEven) {
		chosen[i] = true
	}
	var named, opening []int
	for i, r := range rows {
		for _, w := range wordRE.FindAllString(runes(r.text, 300), -1) {
			if _, ok := known[core.SoundKey(w)]; ok {
				named = append(named, i)
				break
			}
		}
		if r.outgoing {
			if m := openingRE.FindStringSubmatch(r.text); m != nil {
				if ch := []rune(m[1])[0]; unicode.IsLetter(ch) && unicode.IsUpper(ch) {
					opening = append(opening, i)
				}
			}
		}
	}
	// a sample of each that is the same each time the person is read
	rnd := rand.New(rand.NewPCG(uint64(pid), 0))
	rnd.Shuffle(len(named), func(i, j int) { named[i], named[j] = named[j], named[i] })
	rnd.Shuffle(len(opening), func(i, j int) { opening[i], opening[j] = opening[j], opening[i] })
	for _, i := range named[:min(nNamed, len(named))] {
		chosen[i] = true
	}
	for _, i := range opening[:min(15, len(opening))] {
		chosen[i] = true
	}
	idx := make([]int, 0, len(chosen))
	for i := range chosen {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := make([]line, len(idx))
	for k, i := range idx {
		out[k] = rows[i]
	}
	return out
}

// Mentions is how others name them in groups: texts that @mention one of their handles.
func Mentions(s *core.Store, pid int64, n int) []string {
	addrs := core.PeopleOf(s).Addresses(pid)
	if len(addrs) == 0 {
		return nil
	}
	return db.Strs(s.Read(), "SELECT m.text FROM mention x JOIN message m ON m.id = x.message_id WHERE x.address_id IN ("+
		db.Marks(len(addrs))+") AND m.text IS NOT NULL LIMIT ?", append(db.Args(addrs), n)...)
}

var (
	nonWord      = regexp.MustCompile(`[^\p{L}\p{N}_]+`)
	nonWordOrLow = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

func fill(template string, values ...string) string {
	return strings.NewReplacer(values...).Replace(template)
}

// Analyse reads a person's chat with the models and saves what they say.
func (p Ollama) Analyse(c *plugins.Context, pid, n int64) error {
	s := c.Store()
	w := what(c)
	ppl := core.PeopleOf(s)
	rows := Excerpt(s, pid, core.KnownFirsts(s))
	var handles []string
	for _, h := range ppl.Handles[pid] {
		handles = append(handles, h.Value)
	}
	own := map[string]bool{}
	for _, x := range nonWord.Split(core.OwnName(), -1) {
		if x != "" {
			own[core.SoundKey(x)] = true
		}
	}
	for me := range ppl.Me { // the owner's own handles are the owner too
		for _, h := range ppl.Handles[me] {
			local, _, _ := strings.Cut(h.Value, "@")
			for _, x := range nonWordOrLow.Split(local, -1) {
				if x != "" {
					own[core.SoundKey(x)] = true
				}
			}
		}
	}
	var tones, relations []core.ModelLabel
	if w != "name" {
		tones, relations = core.ForModels(s, "tone"), core.ForModels(s, "relation")
	}
	models := Models(c)
	if len(rows) == 0 {
		return core.SaveAnalysis(s, pid, n, models, nil, nil, nil)
	}
	var lines []string
	for _, r := range rows {
		who := "THEM"
		if r.outgoing {
			who = "ME"
		}
		lines = append(lines, fmt.Sprintf("[%s] %s: %s", time.UnixMilli(r.ts).In(config.Timezone).Format("2006-01-02"),
			who, runes(strings.ReplaceAll(r.text, "\n", " "), 220)))
	}
	var said []string
	for _, t := range Mentions(s, pid, 10) {
		said = append(said, runes(strings.ReplaceAll(t, "\n", " "), 200))
	}
	named := strings.Join(said, " | ")
	if named == "" {
		named = "none"
	}
	var akas []string
	for _, a := range ppl.Aka(pid) {
		akas = append(akas, a["name"].(string))
	}
	aka := strings.Join(akas, ", ")
	if aka == "" {
		aka = "none"
	}
	asks := ""
	if w != "tone" {
		asks += askName
	}
	if w != "name" {
		var tl, rl []string
		for _, t := range tones {
			l := "- " + t.Word + ": " + t.Meaning
			if t.Sensitive {
				l += " (sensitive)"
			}
			tl = append(tl, l)
		}
		for _, r := range relations {
			rl = append(rl, "- "+r.Word+": "+r.Meaning)
		}
		asks += fill(askTone, "{tones}", strings.Join(tl, "\n"), "{relations}", strings.Join(rl, "\n"))
	}
	owner := core.OwnName()
	if owner == "" {
		owner = "the owner"
	}
	text := fill(prompt, "{owner}", owner, "{asks}", asks, "{handles}", strings.Join(handles, ", "),
		"{aka}", aka, "{mentions}", named, "{lines}", strings.Join(lines, "\n"))
	schema := Schema(w, tones, relations)
	if !LocalAddress(c.Str("url")) { // the settings may have changed since the check
		return errs.Plugin("Ollama's address is not on this computer or its network", 0)
	}
	var answers []map[string]any
	for _, m := range models {
		a, err := ask(c, m, text, schema)
		if err != nil { // one model failing: the others still vote
			c.Log("{model}: {e}", map[string]any{"model": m, "e": err})
			continue
		}
		answers = append(answers, a)
	}
	if len(answers) == 0 {
		return errs.Plugin("no model answered", 0)
	}
	_, read, _ := strings.Cut(text, "Their handles:")
	name, chosen, relation := Vote(answers, read, handles, own, tones, relations)
	if w == "tone" {
		name = nil
	}
	return core.SaveAnalysis(s, pid, n, models, name, chosen, relation)
}

// Schema is the JSON schema of the models' answer.
func Schema(what string, tones, relations []core.ModelLabel) map[string]any {
	props, req := map[string]any{}, []string{}
	nullable := map[string]any{"type": []string{"string", "null"}}
	if what != "tone" {
		props["name"], props["evidence"] = nullable, nullable
		req = append(req, "name", "evidence")
	}
	if what != "name" {
		if len(tones) > 0 {
			words := []string{}
			for _, t := range tones {
				words = append(words, t.Word)
			}
			props["tone"] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": words}}
			props["sensitive_evidence"] = nullable
			req = append(req, "tone", "sensitive_evidence")
		}
		if len(relations) > 0 {
			words := []string{}
			for _, r := range relations {
				words = append(words, r.Word)
			}
			props["relationship"] = map[string]any{"type": "string", "enum": words}
			req = append(req, "relationship")
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": req}
}

// local is the client the models are asked with. What it sends stays on this computer and its
// network whatever the address checked before: each connection is to a local address (a name may
// point elsewhere by the time it connects), through no proxy the environment names, and after no
// redirect (one would carry the chat to wherever it points).
var local = &http.Client{
	Transport: &http.Transport{Proxy: nil,
		DialContext: (&net.Dialer{Timeout: 30 * time.Second, Control: onlyLocal}).DialContext},
	CheckRedirect: func(r *http.Request, _ []*http.Request) error {
		return fmt.Errorf("Ollama's address sends elsewhere (%s)", r.URL.Host)
	},
}

// onlyLocal lets a connection be made only to this computer or its network.
func onlyLocal(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !localIP(a) {
		return fmt.Errorf("%s is not on this computer or its network", host)
	}
	return nil
}

// ask is one model's answer (Ollama's /api/chat, the answer held to the schema).
var ask = func(c *plugins.Context, model, text string, schema map[string]any) (map[string]any, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model, "stream": false, "format": schema, "think": false,
		"options":  map[string]any{"temperature": 0, "num_ctx": 8192, "num_predict": 400},
		"messages": []map[string]any{{"role": "user", "content": text}}})
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Str("url"), "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := local.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode == 404 {
		return nil, fmt.Errorf("Ollama has no model %s", model)
	}
	if r.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(r.Body, 300))
		return nil, fmt.Errorf("HTTP %d: %s", r.StatusCode, strings.TrimSpace(string(msg)))
	}
	var answer struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&answer); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(answer.Message.Content), &out); err != nil {
		return nil, err
	}
	return out, nil
}
