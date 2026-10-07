package server

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"everysaid/internal/config"
	"everysaid/internal/core"
)

// TestServerParity answers the addresses in EVERYSAID_SERVER_PARITY/urls.txt on the archive go.db
// there, as the Python server answered them on its copy (py.jsonl): one JSON line each into
// go.jsonl, and the chats in go-chats.jsonl, for a comparison. The folders are the Python's
// (EVERYSAID_SERVER_PARITY/env), the plugins' weights its own (weights.json).
func TestServerParity(t *testing.T) {
	dir := os.Getenv("EVERYSAID_SERVER_PARITY")
	if dir == "" {
		t.Skip("no EVERYSAID_SERVER_PARITY")
	}
	for _, n := range []string{"DATA", "CACHE", "CONFIG", "STATE"} {
		old := os.Getenv("EVERYSAID_" + n)
		os.Setenv("EVERYSAID_"+n, filepath.Join(dir, "env", strings.ToLower(n)))
		defer os.Setenv("EVERYSAID_"+n, old)
	}
	config.Load()
	defer config.Load()
	var w struct {
		Names [][2]any                  `json:"names"`
		State map[string]map[string]int `json:"state"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "weights.json"))
	json.Unmarshal(b, &w)
	names, state := core.NameWeights, core.StateWeight
	defer func() { core.NameWeights, core.StateWeight = names, state }()
	core.NameWeights = func() []core.Weight {
		var out []core.Weight
		for _, p := range w.Names {
			out = append(out, core.Weight{Key: p[0].(string), Weight: int(p[1].(float64))})
		}
		return out
	}
	core.StateWeight = func(plugin, field string) int { return w.State[plugin][field] }

	s, err := New(Options{Archive: filepath.Join(dir, "go.db"), AuthDB: filepath.Join(t.TempDir(), "server.db"),
		Origin: "http://localhost:8520", ExtraOrigins: []string{}, Out: io.Discard, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &client{t: t, s: s}
	c.ts = httptestServer(s)
	defer c.ts.Close()
	c.hc, c.jar = newHTTPClient()
	c.login()
	urls, _ := os.ReadFile(filepath.Join(dir, "urls.txt"))
	out, _ := os.Create(filepath.Join(dir, "go.jsonl"))
	defer out.Close()
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	sc := bufio.NewScanner(strings.NewReader(string(urls)))
	for sc.Scan() {
		r := c.get(sc.Text())
		var body any
		json.Unmarshal(r.body, &body)
		enc.Encode(M{"url": sc.Text(), "status": r.status, "body": body})
	}
	chats, _ := os.Create(filepath.Join(dir, "go-chats.jsonl"))
	defer chats.Close()
	enc = json.NewEncoder(chats)
	enc.SetEscapeHTML(false)
	for _, x := range items(c.getJSON("/api/chats?archived=true&unnamed=true")) {
		b := c.getJSON("/api/chats/" + x["id"].(string))
		for _, k := range []string{"sendable", "replyable", "mentionable", "fileable", "unsendable"} {
			delete(b, k)
		}
		enc.Encode(M{"url": x["id"], "body": b})
	}
}
