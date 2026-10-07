// Ports scripts/android-export.py: export the Android call log, SMS, MMS and blocked numbers over
// adb (read only) into SQLite.
//
// The phone is the only one adb sees, or the one with that serial (`adb devices`). Everything goes
// to `<export>/<device>/` (config `[android] export`, default `<data>/android`), the device named
// by its maker and model and the end of its serial, e.g. `acme-phone1-1a2b`: `android.db`, the
// raw query outputs and `mms-parts/`. Each content provider becomes a table with exactly the
// provider's columns, all as text (NULL stays NULL). The raw `content query` output is kept next to
// it, compressed. Tables already in the database are left alone, so a later run only adds what is
// missing. MMS addresses (one query per message) go to `mms_addr`; the binary MMS parts (pictures,
// audio, ...) are saved under `mms-parts/<part _id>`, except those the archive has already taken
// (its record stays after the file has gone to the photo library or been removed).
package android

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/phones"
	"everysaid/internal/text"
)

// providers: table, content URI, in the order they are exported.
var providers = [][2]string{
	{"calls", "content://call_log/calls"},
	{"sms", "content://sms"},
	{"mms", "content://mms"},
	{"mms_part", "content://mms/part"},
	{"blocked", "content://com.android.blockednumber/blocked"},
}

// ExportOptions are what the app gives an export.
type ExportOptions struct {
	Serial string     // the phone's serial (adb devices), when adb sees more than one
	Say    phones.Say // the run's lines (Context.Log); nil: none
}

type exporter struct {
	ExportOptions
	adbPath string
}

// adb runs adb with the arguments; its output, or the failure with what it said.
func (e *exporter) adb(args ...string) ([]byte, error) {
	cmd := []string{}
	if e.Serial != "" {
		cmd = append(cmd, "-s", e.Serial)
	}
	var stderr bytes.Buffer
	c := exec.Command(e.adbPath, append(cmd, args...)...)
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, err
		}
		return nil, phones.Fail("adb {cmd}: {error}", map[string]any{
			"cmd": strings.Join(args[:min(3, len(args))], " "), "error": strings.TrimSpace(decode(stderr.Bytes()))})
	}
	return out, nil
}

func decode(b []byte) string { return strings.ToValidUTF8(string(b), "�") }

var notName = regexp.MustCompile(`[^a-z0-9.-]+`)

// device is a folder name for the phone: maker, model and the last 4 characters of its serial.
func (e *exporter) device() (string, error) {
	if e.Serial == "" {
		out, err := e.adb("devices")
		if err != nil {
			return "", err
		}
		var listed []string
		lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
		for _, l := range lines[min(1, len(lines)):] {
			if strings.HasSuffix(strings.TrimSpace(l), "device") {
				listed = append(listed, strings.Fields(l)[0])
			}
		}
		if len(listed) == 0 {
			return "", phones.Fail("adb sees no device: choose one with -s.", nil)
		}
		if len(listed) != 1 {
			return "", phones.Fail("adb sees {n} devices: choose one with -s ({list}).",
				map[string]any{"n": len(listed), "list": strings.Join(listed, ", ")})
		}
	}
	var props []string
	for _, p := range []string{"ro.product.manufacturer", "ro.product.model", "ro.serialno"} {
		out, err := e.adb("shell", "getprop", p)
		if err != nil {
			return "", err
		}
		props = append(props, strings.TrimSpace(string(out)))
	}
	serial := []rune(props[2])
	parts := []string{}
	for _, p := range []string{props[0], props[1], string(serial[max(0, len(serial)-4):])} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	name := strings.Trim(notName.ReplaceAllString(text.Lower(strings.Join(parts, "-")), "-"), "-")
	if name == "" {
		name = "android"
	}
	return name, nil
}

func (e *exporter) query(uri string, projection []string) (string, error) {
	cmd := []string{"exec-out", "content", "query", "--uri", uri}
	if len(projection) > 0 {
		cmd = append(cmd, "--projection", strings.Join(projection, ":"))
	}
	out, err := e.adb(cmd...)
	return string(out), err
}

var columnName = regexp.MustCompile(`(?:^Row: 0 |, )([A-Za-z0-9_]+)=`)

// columns are the column names, read from the first row of an unrestricted query.
func (e *exporter) columns(uri string) ([]string, error) {
	out, err := e.query(uri, nil)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(out, "Row: 0 ") { // no rows ("No result found."): no columns to read
		return nil, nil
	}
	first, _, _ := strings.Cut(out, "\nRow: 1 ")
	var names []string
	seen := map[string]bool{}
	dup := false
	for _, m := range columnName.FindAllStringSubmatch(first, -1) {
		dup = dup || seen[m[1]]
		seen[m[1]] = true
		names = append(names, m[1])
	}
	if len(names) == 0 || dup {
		return nil, phones.Fail("The columns of {uri} were not found correctly", map[string]any{"uri": uri})
	}
	return names, nil
}

// Parse splits rows on "Row: N " with N counted up, and values on ", <next column>=". Both markers
// are exact, so commas and newlines inside values (message bodies) are kept. A value NULL is nil.
func Parse(text string, cols []string) ([][]*string, error) {
	var rows [][]*string
	pos, n := 0, 0
	for {
		head := fmt.Sprintf("Row: %d ", n)
		if !strings.HasPrefix(text[pos:], head) {
			break
		}
		next := fmt.Sprintf("\nRow: %d ", n+1)
		nxt := strings.Index(text[pos:], next)
		end := len(text)
		if nxt >= 0 {
			nxt += pos
			end = nxt
		}
		block := strings.TrimRight(text[pos+len(head):end], "\n")
		values := make([]*string, 0, len(cols))
		p := 0
		for i, col := range cols {
			if !strings.HasPrefix(block[min(p, len(block)):], col+"=") {
				return nil, phones.Fail("Row {n}: expected the column {col}", map[string]any{"n": n, "col": col})
			}
			p += len(col) + 1
			var stop int
			if i+1 < len(cols) {
				k := strings.Index(block[p:], ", "+cols[i+1]+"=")
				if k < 0 {
					return nil, phones.Fail("Row {n}: the column {col} was not found", map[string]any{"n": n, "col": cols[i+1]})
				}
				stop = p + k
			} else {
				stop = len(block)
			}
			if v := block[p:stop]; v != "NULL" {
				values = append(values, &v)
			} else {
				values = append(values, nil)
			}
			p = stop + 2
		}
		rows = append(rows, values)
		if nxt < 0 {
			break
		}
		pos = nxt + 1
		n++
	}
	return rows, nil
}

func (e *exporter) count(uri string) (int, error) {
	out, err := e.query(uri, []string{"_id"})
	return strings.Count(out, "Row: "), err
}

func exists(d *sql.DB, table string) bool {
	return db.Exists(d, "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", table)
}

func (e *exporter) save(d *sql.DB, table string, cols []string, rows [][]*string) error {
	defs := make([]string, len(cols))
	for i, c := range cols {
		defs[i] = `"` + c + `" TEXT`
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(fmt.Sprintf("CREATE TABLE %s (%s)", table, strings.Join(defs, ", "))); err != nil {
		return err
	}
	stmt, err := tx.Prepare(fmt.Sprintf("INSERT INTO %s VALUES (%s)", table, db.Marks(len(cols))))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		args := make([]any, len(r))
		for i, v := range r {
			if v != nil {
				args[i] = *v
			}
		}
		if _, err := stmt.Exec(args...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	e.Say("OK {table}: {n} rows, {cols} columns", map[string]any{"table": table, "n": len(rows), "cols": len(cols)})
	return nil
}

// Export reads the phone over adb into <export>/<device>/ (see the package's words).
func Export(o ExportOptions) (err error) {
	defer db.Recover(&err)
	if o.Say == nil {
		o.Say = func(string, map[string]any) {}
	}
	e := &exporter{ExportOptions: o}
	if e.adbPath, err = phones.Tool("adb"); err != nil {
		return err
	}
	device, err := e.device()
	if err != nil {
		return err
	}
	out := filepath.Join(config.AndroidExport, device)
	dbPath := filepath.Join(out, "android.db")
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	// made private before SQLite makes it (its journal takes the same mode)
	if f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.Close()
	} else {
		return err
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer d.Close()
	d.SetMaxOpenConns(1)

	for _, p := range providers {
		table, uri := p[0], p[1]
		if exists(d, table) {
			e.Say("-- {table}: already there", map[string]any{"table": table})
			continue
		}
		cols, err := e.columns(uri)
		if err != nil {
			return err
		}
		if cols == nil { // left for a later run, which may find some
			e.Say("-- {table}: nothing on the phone", map[string]any{"table": table})
			continue
		}
		text, err := e.query(uri, cols)
		if err != nil {
			return err
		}
		if err := writeGzip(filepath.Join(out, table+".txt.gz"), text); err != nil {
			return err
		}
		rows, err := Parse(text, cols)
		if err != nil {
			return err
		}
		expected, err := e.count(uri)
		if err != nil {
			return err
		}
		idCol := slices.Index(cols, "_id")
		if idCol < 0 {
			return phones.Fail("{table}: no _id column", map[string]any{"table": table})
		}
		ids := map[string]bool{}
		for _, r := range rows {
			if r[idCol] != nil {
				ids[*r[idCol]] = true
			} else {
				ids["\x00NULL"] = true
			}
		}
		if len(rows) != expected || len(ids) != len(rows) {
			return phones.Fail("{table}: {n} rows, {expected} expected ({ids} unique _id)",
				map[string]any{"table": table, "n": len(rows), "expected": expected, "ids": len(ids)})
		}
		if err := e.save(d, table, cols, rows); err != nil {
			return err
		}
	}

	if exists(d, "mms") && !exists(d, "mms_addr") {
		var cols []string
		var rows [][]*string
		for _, mid := range db.Strs(d, "SELECT _id FROM mms") {
			uri := "content://mms/" + mid + "/addr"
			text, err := e.query(uri, nil)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(text, "Row: 0 ") {
				continue
			}
			if cols == nil {
				if cols, err = e.columns(uri); err != nil {
					return err
				}
			}
			if text, err = e.query(uri, cols); err != nil {
				return err
			}
			got, err := Parse(text, cols)
			if err != nil {
				return err
			}
			rows = append(rows, got...)
		}
		if cols != nil { // no MMS with an address: no table, a later run looks again
			if err := e.save(d, "mms_addr", cols, rows); err != nil {
				return err
			}
		}
	}

	parts := filepath.Join(out, "mms-parts")
	if err := os.MkdirAll(parts, 0o700); err != nil {
		return err
	}
	// parts the archive has already taken (`attachment.source_path`, kept after the file went to
	// the photo library or was removed) are not fetched again
	taken := map[string]bool{}
	if archive := filepath.Join(config.Data, "archive.db"); fileExists(archive) {
		a, err := db.ReadOnly(archive)
		if err != nil {
			return err
		}
		for _, p := range db.Strs(a, "SELECT a.source_path FROM attachment a JOIN source s ON s.id = a.source_id WHERE s.name = ?",
			filepath.Base(out)+"/mms") {
			taken[p] = true
		}
		a.Close()
	}
	saved := 0
	type part struct{ id, ct string }
	var todo []part
	if exists(d, "mms_part") {
		db.Each(d, "SELECT _id, ct FROM mms_part WHERE _data IS NOT NULL", nil, func(scan func(...any)) {
			var id, ct sql.NullString
			scan(&id, &ct)
			todo = append(todo, part{id.String, ct.String})
		})
	}
	for _, p := range todo {
		path := filepath.Join(parts, p.id)
		if fileExists(path) || taken["mms-parts/"+p.id] {
			continue
		}
		data, err := e.adb("exec-out", "content", "read", "--uri", "content://mms/part/"+p.id)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path+".part", data, 0o600); err != nil {
			return err
		}
		if err := os.Rename(path+".part", path); err != nil {
			return err
		}
		saved++
	}
	e.Say("OK mms-parts: {n} new files", map[string]any{"n": saved})
	e.Say("Done: {out}", map[string]any{"out": dbPath})
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func writeGzip(path, text string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	z := gzip.NewWriter(f)
	z.Name = strings.TrimSuffix(filepath.Base(path), ".gz")
	z.ModTime = time.Now()
	if _, err := io.WriteString(z, text); err != nil {
		f.Close()
		return err
	}
	if err := z.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ExportMain is android-export.py's command line: everysaid android-export [-s SERIAL].
func ExportMain(args []string, out io.Writer) error {
	ap := phones.NewArgs("android-export", "Export an Android phone's calls, SMS, MMS and blocked numbers over adb.")
	serial := ap.String("-s,--serial", "SERIAL", "", "the phone's serial (adb devices), when adb sees more than one")
	if err := ap.Parse(args, out); err != nil {
		return err
	}
	return Export(ExportOptions{Serial: *serial, Say: phones.Printer(out)})
}
