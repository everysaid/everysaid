package server

// Every call the interface makes to the server (`api.get/post/patch/
// put/del` with an "/api/..." address in web/src) has a route of that method on the server: an
// address or a method that does not match is a button that does nothing.

import (
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	apiCall = regexp.MustCompile("(?s)api\\.(get|post|patch|put|del)\\b[^(`\"]{0,200}?\\(\\s*[`\"](/api/[^`\"]*)[`\"]")
	qsPart  = regexp.MustCompile(`\?|\$\{qs\(`)
	value   = regexp.MustCompile(`\$\{[^}]*\}`)
)

type uiCall struct{ method, path, file string }

func interfaceCalls(t *testing.T) []uiCall {
	root := filepath.Join("..", "..", "web", "src")
	if _, err := os.Stat(root); err != nil {
		t.Skip("no web/src")
	}
	seen := map[uiCall]bool{}
	var out []uiCall
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.Contains(filepath.Ext(p), ".ts") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, m := range apiCall.FindAllStringSubmatch(string(b), -1) {
			method := strings.ToUpper(m[1])
			if method == "DEL" {
				method = "DELETE"
			}
			path := qsPart.Split(m[2], 2)[0] // the query is not the route
			path = value.ReplaceAllString(path, "{x}")
			c := uiCall{method, path, filepath.Base(p)}
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
		return nil
	})
	return out
}

func TestEveryCallOfTheInterfaceHasARoute(t *testing.T) {
	c := newServer(t)
	found := interfaceCalls(t)
	if len(found) <= 50 { // the pattern still finds them
		t.Fatalf("only %d calls found", len(found))
	}
	var missing []string
	for _, call := range found {
		r := httptest.NewRequest(call.method, strings.ReplaceAll(call.path, "{x}", "1"), nil)
		_, pattern := c.s.Mux().Handler(r)
		if pattern == "" || pattern == "GET /{path...}" {
			missing = append(missing, call.method+" "+call.path+" ("+call.file+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("calls of the interface with no route:\n%s", strings.Join(missing, "\n"))
	}
}
