package server

// Every error code the server answers with has its words in the interface (web/src/lib/i18n.ts,
// errors.<code>; English has Greek's keys, which tsc checks): a code without them is shown raw.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// tsKeys is the keys of a TypeScript object literal, by their dotted path.
func tsKeys(src string) map[string]bool {
	out := map[string]bool{}
	var stack []string
	last := ""
	ident := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*:`)
	for i := 0; i < len(src); i++ {
		switch ch := src[i]; {
		case ch == '"' || ch == '\'' || ch == '`':
			for i++; i < len(src) && src[i] != ch; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case ch == '{':
			stack = append(stack, last)
		case ch == '}':
			if len(stack) == 0 {
				return out
			}
			stack = stack[:len(stack)-1]
		default:
			if m := ident.FindString(src[i:]); m != "" && (i == 0 || !isIdentByte(src[i-1])) {
				last = strings.TrimSpace(strings.TrimSuffix(m, ":"))
				out[strings.Join(append(append([]string{}, stack[1:]...), last), ".")] = true
				i += len(m) - 1
			}
		}
	}
	return out
}

func isIdentByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func TestEveryErrorCodeHasWords(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "lib", "i18n.ts"))
	if err != nil {
		t.Skip("no web/src/lib/i18n.ts")
	}
	src := string(b)
	at := strings.Index(src, "errors: {")
	if at < 0 {
		t.Fatal("no errors in i18n.ts")
	}
	known := tsKeys(src[at+len("errors: "):])
	if !known["auth.bad_link"] || !known["not_found"] {
		t.Fatalf("the parse of i18n.ts found %d keys", len(known))
	}
	used := map[string]bool{"failed": true, "not_found": true}
	code := regexp.MustCompile(`(?:errs\.New|notFound|detail)\("([a-z_.]+)"`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, m := range code.FindAllStringSubmatch(string(b), -1) {
			used[m[1]] = true
		}
	}
	if len(used) < 30 {
		t.Fatalf("only %d codes found: the pattern no longer finds them", len(used))
	}
	for c := range used {
		if !known[c] {
			t.Errorf("errors.%s is not in web/src/lib/i18n.ts", c)
		}
	}
}
