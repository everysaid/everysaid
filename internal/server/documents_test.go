package server

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"everysaid/internal/config"
)

// A PDF is served as it is; an office document as the PDF its kind's program made, once (then from
// the cache); one no PDF could be made of is not tried again; a kind with no program is not shown.
func TestDocumentsPDF(t *testing.T) {
	old := config.Cache
	config.Cache = t.TempDir()
	t.Cleanup(func() { config.Cache = old })
	runs := 0
	d := &Documents{failed: map[string]bool{}, run: map[string]func(context.Context, string, string) error{
		"docx": func(_ context.Context, in, out string) error {
			runs++
			if filepath.Ext(in) != ".docx" {
				t.Errorf("the program got %s", in)
			}
			return os.WriteFile(out, []byte("%PDF-1.4 made"), 0o600)
		},
		"xlsx": func(context.Context, string, string) error { runs++; return errors.New("broken") },
	}}
	dir := t.TempDir()
	pdf := filepath.Join(dir, "a.pdf")
	os.WriteFile(pdf, []byte("%PDF-1.4"), 0o600)
	if p, err := d.PDF("aa", "application/pdf", "", pdf, nil); err != nil || p != pdf {
		t.Fatalf("a PDF: %q %v", p, err)
	}
	doc := filepath.Join(dir, "b")
	os.WriteFile(doc, []byte("PK"), 0o600)
	word := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	for range 2 {
		p, err := d.PDF("bb", word, "", doc, nil)
		if err != nil || filepath.Dir(p) != documentDir() {
			t.Fatalf("a Word document: %q %v", p, err)
		}
	}
	if runs != 1 {
		t.Errorf("made %d times", runs)
	}
	// by its name, its type not known
	if _, err := d.PDF("cc", "", "Report.DOCX", "", []byte("PK")); err != nil {
		t.Errorf("by name: %v", err)
	}
	sheet := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	for range 2 {
		if _, err := d.PDF("dd", sheet, "", doc, nil); !errors.Is(err, errNoDocument) {
			t.Errorf("a failed one: %v", err)
		}
	}
	if runs != 3 {
		t.Errorf("tried %d times, the failed one again", runs)
	}
	if _, err := d.PDF("ee", "application/vnd.ms-powerpoint", "", doc, nil); err != errNoDocument {
		t.Errorf("no program: %v", err)
	}
	if got := d.Types(); !slices.Contains(got, word) || !slices.Contains(got, "application/pdf") ||
		slices.Contains(got, "application/vnd.ms-powerpoint") {
		t.Errorf("types %v", got)
	}
	if left, _ := filepath.Glob(filepath.Join(documentDir(), "work-*")); len(left) > 0 {
		t.Errorf("left behind %v", left)
	}
}

// Each program here makes a PDF of each kind of document (web/e2e/files/hello.<ext>), with the
// document's words in it (where pdftotext is here to read them).
func TestConvertersMakePDFs(t *testing.T) {
	old := config.Cache
	config.Cache = t.TempDir() // LibreOffice's profile there
	t.Cleanup(func() { config.Cache = old })
	text, _ := exec.LookPath("pdftotext")
	found := false
	for _, c := range converters {
		run, ok := c.find()
		if !ok {
			t.Logf("%s: not here", c.name)
			continue
		}
		found = true
		for ext := range preferred {
			src := filepath.Join("../../web/e2e/files", "hello."+ext)
			b, err := os.ReadFile(src)
			if err != nil {
				t.Errorf("%s: no %s", c.name, src)
				continue
			}
			t.Run(c.name+"/"+ext, func(t *testing.T) {
				dir := t.TempDir()
				in, out := filepath.Join(dir, "document."+ext), filepath.Join(dir, "document.pdf")
				os.WriteFile(in, b, 0o600)
				if err := run(context.Background(), in, out); err != nil {
					t.Fatal(err)
				}
				got, _ := os.ReadFile(out)
				if len(got) < 5 || string(got[:5]) != "%PDF-" {
					t.Fatalf("not a PDF: %d bytes", len(got))
				}
				if text != "" {
					words, _ := exec.Command(text, out, "-").Output()
					if !strings.Contains(strings.ToLower(string(words)), "hello from "+ext) { // a title may be in capitals
						t.Errorf("its words: %q", words)
					}
				}
			})
		}
	}
	if !found {
		t.Skip("no program for office documents here")
	}
}
