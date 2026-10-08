// Package checks holds the tests that look over the whole code.
//
// Nothing the user reads skips translation. The interface's words are in web/src/lib/i18n.ts
// (Greek and English; tsc already fails when a key is in one and not the other); the server's
// (plugins, logs, notifications) are English in code with the Greek in internal/i18n. These tests
// check what tsc cannot: that every key the code uses exists and none is left unused, that the
// server's error codes and the archive's vocabularies have words, that no text in the interface
// bypasses t(), that the plugins' words have Greek and none of the Greek is left over, and that no
// Greek is left in the Go code outside the dictionaries.
package checks

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"everysaid/internal/all"
	"everysaid/internal/archive"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
)

var _ = all.Loaded // every plugin registered

func root(t *testing.T) string {
	wd, _ := os.Getwd()
	for d := wd; d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatal("no go.mod above", wd)
	return ""
}

var greek = regexp.MustCompile(`[\x{0370}-\x{03FF}\x{1F00}-\x{1FFF}]`)

// flatten is the keys of the object literal that opens at src[start] ('{'), as 'a.b.c'.
func flatten(src string, start int) []string {
	var keys, path []string
	key, hasKey := "", false
	keyRE := regexp.MustCompile(`^([A-Za-z_]\w*)\s*:`)
	for i := start + 1; i < len(src); {
		c := src[i]
		if c == '"' || c == '\'' || c == '`' {
			j := i + 1
			for src[j] != c {
				if src[j] == '\\' {
					j += 2
				} else {
					j++
				}
			}
			if hasKey {
				keys = append(keys, strings.Join(append(append([]string{}, path...), key), "."))
				hasKey = false
			}
			i = j + 1
			continue
		}
		if strings.HasPrefix(src[i:], "//") {
			i += strings.Index(src[i:], "\n")
			continue
		}
		switch c {
		case '{':
			path = append(path, key)
			hasKey = false
		case '}':
			if len(path) == 0 {
				return keys
			}
			path = path[:len(path)-1]
		default:
			if m := keyRE.FindStringSubmatch(src[i:]); m != nil && strings.ContainsRune(" \n{,", rune(src[i-1])) {
				key, hasKey = m[1], true
				i += len(m[0])
				continue
			}
		}
		i++
	}
	return keys
}

func uiKeys(t *testing.T, lang string) map[string]bool {
	b, err := os.ReadFile(filepath.Join(root(t), "web", "src", "lib", "i18n.ts"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	at := strings.Index(src, "const "+lang)
	start := at + strings.Index(src[at:], "{")
	out := map[string]bool{}
	for _, k := range flatten(src, start) {
		out[k] = true
	}
	return out
}

func webSources(t *testing.T) map[string]string {
	out := map[string]string{}
	base := filepath.Join(root(t), "web", "src")
	filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".tsx") || strings.HasSuffix(p, ".ts")) &&
			!strings.HasSuffix(p, filepath.Join("lib", "i18n.ts")) {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

// keys the code builds from data: each whole namespace is used
var dynamic = []string{"errors.", "audit.", "kind.", "call.", "nav.", "settings.via.", "sources.", "people.why", "chat.state",
	"labels.builtin.", "settings.tab."}

func TestBothLanguagesHaveTheSameKeys(t *testing.T) {
	el, en := uiKeys(t, "el"), uiKeys(t, "en")
	for k := range el {
		if !en[k] {
			t.Errorf("only in el: %s", k)
		}
	}
	for k := range en {
		if !el[k] {
			t.Errorf("only in en: %s", k)
		}
	}
}

func TestEveryKeyUsedExistsAndNoneIsUnused(t *testing.T) {
	keys := uiKeys(t, "el")
	used := map[string]bool{}
	call := regexp.MustCompile(`\bt\(\s*["']([\w.]+)["']`)
	table := regexp.MustCompile(`["']((?:[a-z]+\.)+[a-zA-Z_]+)["']`)
	for path, src := range webSources(t) {
		for _, m := range call.FindAllStringSubmatch(src, -1) {
			k := m[1]
			used[k] = true
			ok := keys[k]
			for x := range keys {
				if strings.HasPrefix(x, k+"_") {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s: t('%s') is not in i18n.ts", filepath.Base(path), k)
			}
		}
		for _, m := range table.FindAllStringSubmatch(src, -1) { // keys in tables (WHY, STATE_FIELDS)
			if keys[m[1]] {
				used[m[1]] = true
			}
		}
	}
	var unused []string
outer:
	for k := range keys {
		if used[k] {
			continue
		}
		for _, d := range dynamic {
			if strings.HasPrefix(k, d) {
				continue outer
			}
		}
		for u := range used {
			if k == u+"_one" || k == u+"_other" {
				continue outer
			}
		}
		unused = append(unused, k)
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		t.Errorf("unused keys in i18n.ts: %v", unused)
	}
}

// goFiles is every Go file of the module (not tests), parsed.
func goFiles(t *testing.T) map[string]*ast.File {
	out := map[string]*ast.File{}
	base := root(t)
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		filepath.WalkDir(filepath.Join(base, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err == nil {
				rel, _ := filepath.Rel(base, p)
				out[filepath.ToSlash(rel)] = f
			}
			return nil
		})
	}
	return out
}

func TestServerErrorCodesHaveWords(t *testing.T) {
	keys := uiKeys(t, "el")
	codes := map[string]bool{}
	for _, f := range goFiles(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || len(c.Args) == 0 {
				return true
			}
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "New" {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "errs" {
					if lit, ok := c.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, _ := strconv.Unquote(lit.Value)
						codes[v] = true
					}
				}
			}
			return true
		})
	}
	if len(codes) == 0 {
		t.Skip("no error codes in the Go code yet")
	}
	for c := range codes {
		if !keys["errors."+c] {
			t.Errorf("error code without words: %s", c)
		}
	}
}

func TestTheArchivesVocabulariesHaveWords(t *testing.T) {
	keys := uiKeys(t, "el")
	for _, k := range archive.MessageKinds {
		if !keys["kind."+k] {
			t.Errorf("no words for kind.%s", k)
		}
	}
	for _, d := range archive.VocabularyOf("call.detail") {
		if !keys["call."+d] {
			t.Errorf("no words for call.%s", d)
		}
	}
}

func TestNoTextInTheInterfaceBypassesT(t *testing.T) {
	allowed := map[string]bool{"Everysaid": true, "QR": true, "Ελληνικά": true, "English": true, "Passkey": true,
		"abcd-ef01-2345-6789": true}
	text := regexp.MustCompile(`>\s*([^<>{}\n]*[A-Za-zΑ-Ωα-ωά-ώ]{2,}[^<>{}\n]*?)\s*<`)
	code := regexp.MustCompile(`[()=;?:|&]|^[\w.]+$|^,`)
	attr := regexp.MustCompile(`\b(aria-label|title|placeholder|alt)="([^"]*[A-Za-zΑ-Ωα-ω]{2,}[^"]*)"`)
	for path, src := range webSources(t) {
		if !strings.HasSuffix(path, ".tsx") {
			continue
		}
		for _, m := range text.FindAllStringSubmatch(src, -1) {
			s := strings.TrimSpace(m[1])
			if s != "" && !allowed[s] && !code.MatchString(s) {
				t.Errorf("%s: text outside t(): >%s<", filepath.Base(path), s)
			}
		}
		for _, m := range attr.FindAllStringSubmatch(src, -1) {
			if !allowed[m[2]] {
				t.Errorf("%s: %s=%q outside t()", filepath.Base(path), m[1], m[2])
			}
		}
	}
}

var proper = map[string]bool{"Telegram": true, "WhatsApp": true, "Viber": true, "iMessage": true, "SMS": true, "MMS": true,
	"RCS": true, "FaceTime": true, "Messenger": true, "Signal": true, "UDID": true, "API key": true, "adb": true,
	"Android (adb)": true, "immich": true, "Immich": true, "CardDAV": true, "URL": true, "Passkey": true,
	"Viber Desktop": true, "libimobiledevice (idevicebackup2)": true}

var threeLower = regexp.MustCompile(`[a-z]{3}`)

func TestThePluginsWordsHaveGreek(t *testing.T) {
	for _, p := range plugins.All() {
		el, en := plugins.Manifest(p, "el"), plugins.Manifest(p, "en")
		type pair struct{ a, b string }
		var pairs []pair
		pairs = append(pairs, pair{el["name"].(string), en["name"].(string)},
			pair{el["description"].(string), en["description"].(string)})
		for i, n := range en["needs"].([]string) {
			pairs = append(pairs, pair{el["needs"].([]string)[i], n})
		}
		for i, s := range en["settings"].([]plugins.M) {
			e := el["settings"].([]plugins.M)[i]
			pairs = append(pairs, pair{e["label"].(string), s["label"].(string)})
			if h := s["help"].(string); h != "" {
				pairs = append(pairs, pair{e["help"].(string), h})
			}
		}
		for i, a := range en["actions"].([]plugins.M) {
			pairs = append(pairs, pair{el["actions"].([]plugins.M)[i]["label"].(string), a["label"].(string)})
			if q, ok := a["confirm"].(string); ok {
				pairs = append(pairs, pair{el["actions"].([]plugins.M)[i]["confirm"].(string), q})
			}
		}
		for _, x := range pairs {
			if x.a == x.b && !proper[x.b] && threeLower.MatchString(x.b) {
				t.Errorf("%s: no Greek for %q", p.Info().ID, x.b)
			}
		}
	}
	for _, x := range plugins.NameSources("el") {
		if x["id"] != "contacts" && !strings.Contains(x["label"].(string), "(") {
			t.Errorf("name source %s: %q", x["id"], x["label"])
		}
	}
}

// Greek is said through internal/i18n; these keep Greek of their own on purpose: the dictionary
// itself, the folding of text, the carriers' notices (they are Greek SMS), the words Pidgin wrote
// in its logs, the demo's invented people.
var greekAllowed = []string{"internal/i18n/", "internal/text/", "internal/demo/", "internal/importers/carriers",
	"internal/importers/imlogs", "internal/importers/voip", "internal/pyrandom/"}

func TestNoGreekInTheGoCode(t *testing.T) {
	for rel, f := range goFiles(t) {
		skip := false
		for _, a := range greekAllowed {
			if strings.HasPrefix(rel, a) {
				skip = true
			}
		}
		if skip {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, err := strconv.Unquote(lit.Value); err == nil && greek.MatchString(v) {
					t.Errorf("%s: Greek outside the dictionaries: %.60q", rel, v)
				}
			}
			return true
		})
	}
}

// constString is the value of a string literal, or of literals joined with +.
func constString(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			v, err := strconv.Unquote(x.Value)
			return v, err == nil
		}
	case *ast.ParenExpr:
		return constString(x.X)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			a, ok1 := constString(x.X)
			b, ok2 := constString(x.Y)
			return a + b, ok1 && ok2
		}
	}
	return "", false
}

// The Greek of internal/i18n is for words the Go code says: one that nothing says any more is left
// from code that changed, and only hides that its English is gone.
func TestNoGreekWordIsUnused(t *testing.T) {
	texts := map[string]bool{}
	for rel, f := range goFiles(t) {
		if strings.HasPrefix(rel, "internal/i18n/") {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok {
				if s, ok := constString(e); ok {
					texts[s] = true
					if head, rest, ok := strings.Cut(s, ": "); ok { // "missing: X, Y", said by parts (i18n.Tr)
						texts[head] = true
						for _, part := range strings.Split(rest, ", ") {
							texts[part] = true
						}
					}
				}
			}
			return true
		})
	}
	var unused []string
	for k := range i18n.EL {
		if !texts[k] {
			unused = append(unused, k)
		}
	}
	sort.Strings(unused)
	for _, k := range unused {
		t.Errorf("a Greek word nothing says: %.80q", k)
	}
}
