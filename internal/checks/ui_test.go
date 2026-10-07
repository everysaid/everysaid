// Ports tests/test_ui_rules.py: rules of the interface's code that a whole kind of mistake breaks.
//
// A button of a list that does something to its row (`x.mutate(row)`) spins only while that row is
// being done (`loading={x.isPending && x.variables ... row}`): with `loading={x.isPending}` every
// row's button spins at once, as the merge dialog's once did.
package checks

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type tag struct {
	text string
	line int
}

// buttons is each <Button ...> opening tag whole (its props may hold arrow functions and braces).
func buttons(text string) []tag {
	var out []tag
	for _, m := range regexp.MustCompile(`<Button\b`).FindAllStringIndex(text, -1) {
		depth := 0
		for i := m[1]; i < len(text); i++ {
			switch c := text[i]; {
			case c == '{':
				depth++
			case c == '}':
				depth--
			case c == '>' && depth == 0 && text[i-1] != '=':
				out = append(out, tag{text[m[0] : i+1], strings.Count(text[:m[0]], "\n") + 1})
				i = len(text)
			}
		}
	}
	return out
}

func TestARowsButtonSpinsOnlyForItsRow(t *testing.T) {
	base := root(t)
	mutate := regexp.MustCompile(`(\w+)\.mutate\(([^)]*)`)
	filepath.WalkDir(filepath.Join(base, "web", "src"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".tsx") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, tg := range buttons(string(b)) {
			for _, m := range mutate.FindAllStringSubmatch(tg.text, -1) {
				spins := regexp.MustCompile(`loading=\{\s*` + regexp.QuoteMeta(m[1]) + `\.isPending\s*\}`)
				if strings.TrimSpace(m[2]) != "" && spins.MatchString(tg.text) {
					rel, _ := filepath.Rel(base, p)
					t.Error(fmt.Sprintf("%s:%d: %s.mutate(%.30s) spins for every row", rel, tg.line, m[1], strings.TrimSpace(m[2])))
				}
			}
		}
		return nil
	})
}
