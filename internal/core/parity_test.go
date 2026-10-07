package core_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"everysaid/internal/core"
)

// TestParity answers the calls in EVERYSAID_PARITY_CALLS (JSON lines: {"fn", "args"}) on the archive
// EVERYSAID_PARITY_DB, one JSON line each into EVERYSAID_PARITY_OUT, for a comparison with the
// Python's answers. EVERYSAID_PARITY_WEIGHTS gives the plugins' weights as the Python has them.
func TestParity(t *testing.T) {
	calls := os.Getenv("EVERYSAID_PARITY_CALLS")
	if calls == "" {
		t.Skip("no EVERYSAID_PARITY_CALLS")
	}
	var w struct {
		Names [][2]any                  `json:"names"`
		State map[string]map[string]int `json:"state"`
	}
	json.Unmarshal([]byte(os.Getenv("EVERYSAID_PARITY_WEIGHTS")), &w)
	core.NameWeights = func() []core.Weight {
		var out []core.Weight
		for _, p := range w.Names {
			out = append(out, core.Weight{Key: p[0].(string), Weight: int(p[1].(float64))})
		}
		return out
	}
	core.StateWeight = func(plugin, field string) int { return w.State[plugin][field] }
	s, err := core.Open(os.Getenv("EVERYSAID_PARITY_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in, _ := os.Open(calls)
	defer in.Close()
	out, _ := os.Create(os.Getenv("EVERYSAID_PARITY_OUT"))
	defer out.Close()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	enc := json.NewEncoder(out)
	var tf *os.File
	if p := os.Getenv("EVERYSAID_PARITY_TIMES"); p != "" {
		tf, _ = os.Create(p)
		defer tf.Close()
	}
	for sc.Scan() {
		var c struct {
			Fn   string         `json:"fn"`
			Args map[string]any `json:"args"`
		}
		json.Unmarshal(sc.Bytes(), &c)
		t0 := time.Now()
		res, err := call(s, c.Fn, c.Args)
		if tf != nil {
			fmt.Fprintf(tf, "%s %d\n", c.Fn, time.Since(t0).Microseconds())
		}
		if err != nil {
			res = map[string]any{"error": err.Error()}
		}
		enc.Encode(res)
	}
}

func str(a map[string]any, k string) string { v, _ := a[k].(string); return v }
func num(a map[string]any, k string) int64  { v, _ := a[k].(float64); return int64(v) }
func flag(a map[string]any, k string, def bool) bool {
	if v, ok := a[k].(bool); ok {
		return v
	}
	return def
}
func ptr(a map[string]any, k string) *int64 {
	if v, ok := a[k].(float64); ok {
		x := int64(v)
		return &x
	}
	return nil
}
func strs(a map[string]any, k string) []string {
	var out []string
	if vs, ok := a[k].([]any); ok {
		for _, v := range vs {
			out = append(out, v.(string))
		}
	}
	return out
}

func call(s *core.Store, fn string, a map[string]any) (any, error) {
	switch fn {
	case "chats":
		o := core.DefaultChatsOptions()
		o.IncludeArchived = flag(a, "include_archived", false)
		o.Kind, o.Q = str(a, "kind"), str(a, "q")
		o.Limit, o.Offset = int(num(a, "limit")), int(num(a, "offset"))
		o.Unnamed, o.EmptyGroups, o.Short = flag(a, "unnamed", true), flag(a, "empty_groups", true), flag(a, "short", true)
		o.MinMessages = num(a, "min_messages")
		if p := ptr(a, "max_messages"); p != nil {
			o.MaxMessages, o.HasMax = *p, true
		}
		o.WithServices, o.WithoutServices = strs(a, "with_services"), strs(a, "without_services")
		return core.Chats(s, o), nil
	case "chat":
		return core.GetChat(s, str(a, "chat_id")), nil
	case "stream":
		return core.Stream(s, str(a, "chat_id"), core.StreamOptions{Before: str(a, "before"), After: str(a, "after"),
			Around: ptr(a, "around"), Limit: int(num(a, "limit")), Hidden: strs(a, "hidden")})
	case "message":
		return core.GetMessage(s, num(a, "message_id")), nil
	case "receipts":
		return core.Receipts(s, num(a, "message_id")), nil
	case "context":
		return core.Context(s, num(a, "message_id"), int(num(a, "n")))
	case "search", "between":
		o := core.SearchOptions{ChatID: str(a, "chat_id"), Service: str(a, "service"), Kind: str(a, "kind"),
			Since: ptr(a, "since"), Until: ptr(a, "until"), Limit: int(num(a, "limit")), Offset: int(num(a, "offset")),
			Case: flag(a, "case", false), Whole: flag(a, "whole", false)}
		if v, ok := a["outgoing"].(bool); ok {
			o.Outgoing = &v
		}
		if v, ok := a["archived"].(bool); ok {
			o.Archived = &v
		}
		if fn == "between" {
			return core.Between(s, o)
		}
		return core.Search(s, str(a, "q"), o)
	case "person":
		return core.Person(s, num(a, "person_id")), nil
	case "people_list":
		return core.PeopleList(s, core.PeopleListOptions{Q: str(a, "q"), Limit: int(num(a, "limit")), Offset: int(num(a, "offset")),
			Unnamed: flag(a, "unnamed", true), Short: flag(a, "short", true)}), nil
	case "merge_suggestions":
		return core.MergeSuggestions(s, int(num(a, "limit")), flag(a, "recent", false)), nil
	case "unnamed_people":
		return core.UnnamedPeople(s, int(num(a, "limit")), int(num(a, "offset")), nil), nil
	case "merges_dismissed":
		return core.MergesDismissed(s), nil
	case "group_suggestions":
		return core.GroupSuggestions(s, str(a, "chat_id"), int(num(a, "limit"))), nil
	case "calls":
		return core.Calls(s, core.CallsOptions{ChatID: str(a, "chat_id"), Missed: flag(a, "missed", false),
			Service: str(a, "service"), Before: num(a, "before"), Limit: int(num(a, "limit")),
			Unnamed: flag(a, "unnamed", true), Short: flag(a, "short", true)})
	case "media":
		return core.Media(s, core.MediaOptions{ChatID: str(a, "chat_id"), Kind: strOr(a, "kind", "all"), Before: num(a, "before"),
			Limit: int(num(a, "limit")), AvailableOnly: flag(a, "available_only", false)})
	case "timeline":
		return core.Timeline(s, num(a, "day_start"), num(a, "day_end")), nil
	case "stats":
		return core.Stats(s, flag(a, "include_archived", false)), nil
	case "labels":
		return core.Labels(s, str(a, "kind")), nil
	case "digest":
		return core.Digest(s), nil
	case "person_labels":
		return core.PersonLabels(s, num(a, "person_id"), flag(a, "suggested", true)), nil
	case "guess":
		return core.Guess(s, num(a, "person_id")), nil
	case "guessed":
		ids := []int64{}
		for id := range core.Guessed(s) {
			ids = append(ids, id)
		}
		return sortInts(ids), nil
	case "to_analyse":
		var out [][2]int64
		for _, r := range core.ToAnalyse(s, flag(a, "only_unnamed", true), num(a, "min_messages")) {
			out = append(out, [2]int64{r.PersonID, r.Messages})
		}
		return out, nil
	case "settings":
		return core.Settings(s), nil
	case "devices":
		return core.Devices(s), nil
	}
	return nil, &unknown{fn}
}

func strOr(a map[string]any, k, def string) string {
	if v := str(a, k); v != "" {
		return v
	}
	return def
}

func sortInts(xs []int64) []int64 {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
	return xs
}

type unknown struct{ fn string }

func (u *unknown) Error() string { return "unknown fn " + strings.TrimSpace(u.fn) }
