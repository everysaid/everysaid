// Documents shown rather than downloaded: a PDF as it is, an office document (Word, Excel,
// PowerPoint, their OpenDocument kin) as a PDF made by a program found on this machine. Which
// programs are here is looked for once, when the server starts, and for each kind of document the
// best of them is kept until it stops; with none, such a document is only downloaded.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/i18n"
)

// officeTypes are the documents a program may turn into a PDF, by type, with the extension the
// programs read them by.
var officeTypes = map[string]string{
	"application/msword": "doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx",
	"application/vnd.oasis.opendocument.text":                                 "odt",
	"application/rtf":          "rtf",
	"text/rtf":                 "rtf",
	"application/vnd.ms-excel": "xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.oasis.opendocument.spreadsheet":                            "ods",
	"application/vnd.ms-powerpoint":                                             "ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"application/vnd.oasis.opendocument.presentation":                           "odp",
}

// converter is a program that makes a PDF of a document: found (find) or not, and how it is run.
type converter struct {
	name string
	find func() (run func(ctx context.Context, in, out string) error, ok bool)
}

// preferred is, for each extension, the programs in the order their PDFs are best: OnlyOffice for
// Microsoft's formats (its own), LibreOffice for OpenDocument's and RTF.
var preferred = map[string][]string{
	"doc": {"onlyoffice", "libreoffice"}, "docx": {"onlyoffice", "libreoffice"},
	"xls": {"onlyoffice", "libreoffice"}, "xlsx": {"onlyoffice", "libreoffice"},
	"ppt": {"onlyoffice", "libreoffice"}, "pptx": {"onlyoffice", "libreoffice"},
	"odt": {"libreoffice", "onlyoffice"}, "ods": {"libreoffice", "onlyoffice"},
	"odp": {"libreoffice", "onlyoffice"}, "rtf": {"libreoffice", "onlyoffice"},
}

var converters = []converter{{"onlyoffice", findOnlyOffice}, {"libreoffice", findLibreOffice}}

// Documents makes and keeps the PDFs of documents, one at a time.
type Documents struct {
	run    map[string]func(ctx context.Context, in, out string) error // by extension
	chosen map[string][]string                                        // the extensions of each program found
	mu     sync.Mutex
	failed map[string]bool // documents no PDF could be made of, not tried again until a restart
}

// FindDocuments looks for the programs once and keeps, for each extension, the best one here.
func FindDocuments() *Documents {
	d := &Documents{run: map[string]func(context.Context, string, string) error{}, chosen: map[string][]string{},
		failed: map[string]bool{}}
	found := map[string]func(context.Context, string, string) error{}
	for _, c := range converters {
		if run, ok := c.find(); ok {
			found[c.name] = run
		}
	}
	for ext, names := range preferred {
		for _, n := range names {
			if run := found[n]; run != nil {
				d.run[ext] = run
				d.chosen[n] = append(d.chosen[n], ext)
				break
			}
		}
	}
	return d
}

// Said is what the server says at its start about them: which program shows which kinds, or that
// none is here.
func (d *Documents) Said() []string {
	if len(d.chosen) == 0 {
		return []string{i18n.Say("No program for office documents (OnlyOffice or LibreOffice): they are only downloaded", nil)}
	}
	var out []string
	for _, c := range converters {
		if exts := d.chosen[c.name]; exts != nil {
			sort.Strings(exts)
			out = append(out, i18n.Say("Office documents shown with {program}: {kinds}", M{"program": c.name, "kinds": strings.Join(exts, " ")}))
		}
	}
	return out
}

// Types are the types of the documents shown: a PDF always, the others where a program is here.
func (d *Documents) Types() []string {
	out := []string{"application/pdf"}
	for t, ext := range officeTypes {
		if d.run[ext] != nil {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// Extensions are the same by the file's name, for a document whose type is not known.
func (d *Documents) Extensions() []string {
	out := []string{"pdf"}
	for ext := range d.run {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

func documentDir() string { return filepath.Join(config.Cache, "documents") }

// errNoDocument: this file is not a document shown, or no PDF could be made of it.
var errNoDocument = errors.New("no PDF of this document")

// PDF is the path of the document's PDF: the file itself when it is one, else the PDF made of it
// (once, kept in the cache). typ is its type, name its file name (for its extension when the type
// is not known); src is the file, or "" with data its content.
func (d *Documents) PDF(sha, typ, name, src string, data []byte) (string, error) {
	ext := officeTypes[typ]
	switch typ {
	case "application/pdf":
		ext = "pdf"
	case "", "application/octet-stream":
		ext = strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	}
	if ext == "pdf" && src != "" {
		return src, nil
	}
	out := filepath.Join(documentDir(), sha+".pdf")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	run := d.run[ext]
	if ext != "pdf" && run == nil {
		return "", errNoDocument
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := os.Stat(out); err == nil { // made while this one waited
		return out, nil
	}
	if d.failed[sha] {
		return "", errNoDocument
	}
	if err := os.MkdirAll(documentDir(), 0o700); err != nil {
		return "", err
	}
	if ext == "pdf" { // a PDF that came as bytes: kept as it is
		if err := writeFile(out, data); err != nil {
			return "", err
		}
		return out, nil
	}
	work, err := os.MkdirTemp(documentDir(), "work-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	in := filepath.Join(work, "document."+ext)
	if src != "" {
		data, err = os.ReadFile(src)
		if err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return "", err
	}
	made := filepath.Join(work, "document.pdf")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, in, made); err != nil {
		d.failed[sha] = true
		return "", fmt.Errorf("%w: %v", errNoDocument, err)
	}
	if st, err := os.Stat(made); err != nil || st.Size() == 0 {
		d.failed[sha] = true
		return "", errNoDocument
	}
	if err := os.Rename(made, out); err != nil {
		return "", err
	}
	return out, nil
}

func writeFile(p string, b []byte) error {
	tmp := p + ".part"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// findOnlyOffice is OnlyOffice's converter (x2t, part of its desktop editors), with the list of
// fonts the editors make on their first start: without it x2t makes nothing.
func findOnlyOffice() (func(context.Context, string, string) error, bool) {
	home, _ := os.UserHomeDir()
	var x2t, fonts string
	switch runtime.GOOS {
	case "linux":
		x2t = "/opt/onlyoffice/desktopeditors/converter/x2t"
		fonts = filepath.Join(home, ".local/share/onlyoffice/desktopeditors/data/fonts")
	case "windows":
		x2t = filepath.Join(os.Getenv("ProgramFiles"), `ONLYOFFICE\DesktopEditors\converter\x2t.exe`)
		fonts = filepath.Join(os.Getenv("LOCALAPPDATA"), `ONLYOFFICE\DesktopEditors\data\fonts`)
	default:
		return nil, false
	}
	if st, err := os.Stat(x2t); err != nil || st.IsDir() {
		return nil, false
	}
	if _, err := os.Stat(filepath.Join(fonts, "AllFonts.js")); err != nil {
		return nil, false
	}
	dir := filepath.Dir(x2t)
	return func(ctx context.Context, in, out string) error {
		job := filepath.Join(filepath.Dir(in), "job.xml")
		x := `<?xml version="1.0" encoding="utf-8"?><TaskQueueDataConvert>` +
			"<m_sFileFrom>" + xmlText(in) + "</m_sFileFrom><m_sFileTo>" + xmlText(out) + "</m_sFileTo>" +
			"<m_nFormatTo>513</m_nFormatTo>" + // PDF
			"<m_sAllFontsPath>" + xmlText(filepath.Join(fonts, "AllFonts.js")) + "</m_sAllFontsPath>" +
			"<m_sFontDir>" + xmlText(fonts) + "</m_sFontDir>" +
			// without parameters of its own a workbook's PDF has only its first sheet; with them every
			// sheet, each as wide as the page and as long as it needs
			`<m_sJsonParams>{"spreadsheetLayout":{"ignorePrintArea":true,"fitToWidth":1,"fitToHeight":0}}</m_sJsonParams>` +
			"</TaskQueueDataConvert>"
		if err := os.WriteFile(job, []byte(x), 0o600); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, x2t, job)
		cmd.Dir = dir // it finds its libraries and data beside itself
		if runtime.GOOS == "linux" {
			cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+dir)
		}
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(b)))
		}
		return nil
	}, true
}

func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// findLibreOffice is LibreOffice without a window, with a profile of its own in the cache (one
// that a LibreOffice the user has open does not share).
func findLibreOffice() (func(context.Context, string, string) error, bool) {
	var bin string
	for _, n := range []string{"soffice", "libreoffice"} {
		if p, err := exec.LookPath(n); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		for _, p := range []string{"/Applications/LibreOffice.app/Contents/MacOS/soffice",
			filepath.Join(os.Getenv("ProgramFiles"), `LibreOffice\program\soffice.exe`)} {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				bin = p
				break
			}
		}
	}
	if bin == "" {
		return nil, false
	}
	return func(ctx context.Context, in, out string) error {
		profile := filepath.ToSlash(filepath.Join(documentDir(), "libreoffice-profile"))
		if !strings.HasPrefix(profile, "/") {
			profile = "/" + profile // C:/… on Windows
		}
		u := url.URL{Scheme: "file", Path: profile}
		cmd := exec.CommandContext(ctx, bin, "--headless", "--norestore", "-env:UserInstallation="+u.String(),
			"--convert-to", "pdf", "--outdir", filepath.Dir(out), in)
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(b)))
		}
		// it names the PDF after the document
		made := strings.TrimSuffix(in, filepath.Ext(in)) + ".pdf"
		if made != out {
			return os.Rename(made, out)
		}
		return nil
	}, true
}

// document serves a file's PDF (GET /api/media/{sha}/document).
func (s *Server) document(q *req, sha string) (any, error) {
	r := s.Store.Read()
	typ := db.Str(r, "SELECT coalesce(mime, '') FROM media WHERE sha256 = ?", sha)
	name := db.Str(r, "SELECT coalesce(name, '') FROM attachment WHERE sha256 = ? AND coalesce(name, '') != '' LIMIT 1", sha)
	src := s.Host.LocalFile(sha)
	var data []byte
	if src == "" {
		got := s.Host.Fetch(sha, "original")
		if got == nil {
			return nil, notFound("file_gone")
		}
		src, data = got.Path, got.Data
		if typ == "" {
			typ = got.Type
		}
	}
	if name == "" {
		name = src
	}
	p, err := s.Docs.PDF(sha, typ, name, src, data)
	if err != nil {
		if err != errNoDocument { // the reason, when there is one
			s.log.Warn("no PDF of a document", "sha256", pyCut(sha, 12), "error", err)
		}
		return nil, notFound("no_preview")
	}
	return done, serveFile(q.w, q.r, p, "application/pdf", cacheImmutable)
}
