package android

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"everysaid/internal/config"
	"everysaid/internal/db"
)

func str(s string) *string { return &s }

func TestParse(t *testing.T) {
	text := "Row: 0 _id=1, body=hello, world, address=+1\n" +
		"Row: 1 _id=2, body=line one\nline two, body2, address=NULL\n" +
		"Row: 2 _id=3, body=, address=x\n"
	rows, err := Parse(text, []string{"_id", "body", "address"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]*string{{str("1"), str("hello, world"), str("+1")},
		{str("2"), str("line one\nline two, body2"), nil},
		{str("3"), str(""), str("x")}}
	if len(rows) != len(want) {
		t.Fatalf("%d rows", len(rows))
	}
	for i := range want {
		for j := range want[i] {
			if (rows[i][j] == nil) != (want[i][j] == nil) || (rows[i][j] != nil && *rows[i][j] != *want[i][j]) {
				t.Errorf("row %d col %d: %v", i, j, rows[i][j])
			}
		}
	}
	if _, err := Parse("Row: 0 _id=1, other=2", []string{"_id", "body"}); err == nil ||
		err.Error() != "Row 0: the column body was not found" {
		t.Errorf("a missing column: %v", err)
	}
	if rows, _ := Parse("No result found.", []string{"_id"}); len(rows) != 0 {
		t.Errorf("no rows: %v", rows)
	}
}

// fakeADB is a shell script answering as adb does for a phone with a few calls, SMS, one MMS with
// an address and a part, and a blocked number.
const fakeADB = `#!/bin/sh
if [ "$1" = "-s" ]; then shift 2; fi
case "$*" in
"exec-out content read --uri content://mms/part/9") printf 'JPEGDATA' ;;
devices) printf 'List of devices attached\nSER1234ABCD\tdevice\n\n' ;;
"shell getprop ro.product.manufacturer") echo ACME ;;
"shell getprop ro.product.model") echo "PHONE1" ;;
"shell getprop ro.serialno") echo SER1234ABCD ;;
*"--uri content://call_log/calls --projection _id") printf 'Row: 0 _id=1\nRow: 1 _id=2\n' ;;
*"--uri content://call_log/calls"*) printf 'Row: 0 _id=1, number=+301, duration=10\nRow: 1 _id=2, number=NULL, duration=0\n' ;;
*"--uri content://sms --projection _id") printf 'Row: 0 _id=7\n' ;;
*"--uri content://sms"*) printf 'Row: 0 _id=7, address=+302, body=hi, there\nsecond line\n' ;;
*"--uri content://mms/5/addr"*) printf 'Row: 0 _id=1, msg_id=5, address=+303, type=137\n' ;;
*"--uri content://mms/part --projection _id") printf 'Row: 0 _id=9\nRow: 1 _id=10\n' ;;
*"--uri content://mms/part"*) printf 'Row: 0 _id=9, ct=image/jpeg, _data=/data/x\nRow: 1 _id=10, ct=text/plain, _data=NULL\n' ;;
*"--uri content://mms --projection _id") printf 'Row: 0 _id=5\n' ;;
*"--uri content://mms --projection"*|*"--uri content://mms") printf 'Row: 0 _id=5, m_type=128\n' ;;
*"--uri content://com.android.blockednumber/blocked --projection _id") printf 'Row: 0 _id=1\n' ;;
*"--uri content://com.android.blockednumber/blocked"*) printf 'Row: 0 _id=1, original_number=+309\n' ;;
*) echo "unknown: $*" >&2; exit 1 ;;
esac
`

func TestExportWithFakeADB(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o700)
	if err := os.WriteFile(filepath.Join(bin, "adb"), []byte(fakeADB), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, v := range []string{"DATA", "CACHE", "CONFIG", "STATE"} {
		t.Setenv("EVERYSAID_"+v, filepath.Join(dir, strings.ToLower(v)))
	}
	config.Load()
	t.Cleanup(config.Load)

	var lines []string
	say := func(text string, params map[string]any) { lines = append(lines, text) }
	if err := Export(ExportOptions{Say: say}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(config.AndroidExport, "acme-phone1-abcd")
	d, err := db.ReadOnly(filepath.Join(out, "android.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for table, n := range map[string]int64{"calls": 2, "sms": 1, "mms": 1, "mms_part": 2, "blocked": 1, "mms_addr": 1} {
		if got := db.Int(d, "SELECT count(*) FROM "+table); got != n {
			t.Errorf("%s: %d rows", table, got)
		}
	}
	if body := db.Str(d, "SELECT body FROM sms"); body != "hi, there\nsecond line" {
		t.Errorf("body %q", body)
	}
	if db.Int(d, "SELECT count(*) FROM calls WHERE number IS NULL") != 1 {
		t.Error("NULL did not stay NULL")
	}
	if got, _ := os.ReadFile(filepath.Join(out, "mms-parts", "9")); string(got) != "JPEGDATA" {
		t.Errorf("part 9: %q", got)
	}
	if st, _ := os.Stat(filepath.Join(out, "android.db")); st.Mode().Perm() != 0o600 {
		t.Errorf("android.db mode %o", st.Mode().Perm())
	}
	for _, f := range []string{"calls.txt.gz", "sms.txt.gz", "blocked.txt.gz"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}

	// again: every table is there, the part too
	lines = nil
	if err := Export(ExportOptions{Serial: "SER1234ABCD", Say: say}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.Join(lines, "\n"), "-- {table}: already there") != 5 {
		t.Errorf("second run: %q", lines)
	}

	// adb failing says what it said
	t.Setenv("PATH", t.TempDir())
	if err := Export(ExportOptions{}); err == nil || !strings.Contains(err.Error(), "adb (Android platform-tools) is needed") {
		t.Errorf("no adb: %v", err)
	}
}

func TestDeviceCount(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'List of devices attached\\nA1\\tdevice\\nB2\\tdevice\\nC3\\tunauthorized\\n'\n"
	os.WriteFile(filepath.Join(dir, "adb"), []byte(script), 0o700)
	e := &exporter{adbPath: filepath.Join(dir, "adb")}
	_, err := e.device()
	if err == nil || err.Error() != "adb sees 2 devices: choose one with -s (A1, B2)." {
		t.Errorf("%v", err)
	}
}

// fakePhone puts an adb answering as fakeADB, with some answers replaced, first in PATH, and the
// folders in the test's own.
func fakePhone(t *testing.T, replace ...string) string {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o700)
	script := strings.NewReplacer(replace...).Replace(fakeADB)
	if err := os.WriteFile(filepath.Join(bin, "adb"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, v := range []string{"DATA", "CACHE", "CONFIG", "STATE"} {
		t.Setenv("EVERYSAID_"+v, filepath.Join(dir, strings.ToLower(v)))
	}
	config.Load()
	t.Cleanup(config.Load)
	return filepath.Join(config.AndroidExport, "acme-phone1-abcd")
}

// exec-out does not carry the exit code of `content`: what the phone says instead of rows is a
// refusal, not "nothing on the phone".
const refusal = `echo 'Error while accessing provider:x'; echo 'java.lang.SecurityException: Permission Denial'`

// TestRefusalsAreNotEmpty: a provider that refuses the shell is said; an optional one (blocked
// numbers) is left for a later run, the others stop the export.
func TestRefusalsAreNotEmpty(t *testing.T) {
	out := fakePhone(t, `printf 'Row: 0 _id=1, original_number=+309\n'`, refusal)
	var lines []string
	say := func(text string, params map[string]any) { lines = append(lines, text) }
	if err := Export(ExportOptions{Say: say}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "-- {table}: not readable now ({error})") || strings.Contains(joined, "nothing on the phone") {
		t.Errorf("lines: %s", joined)
	}
	d, _ := db.ReadOnly(filepath.Join(out, "android.db"))
	defer d.Close()
	if exists(d, "blocked") {
		t.Error("a refused provider became an empty table, never asked again")
	}

	fakePhone(t, `printf 'Row: 0 _id=7, address=+302, body=hi, there\nsecond line\n'`, refusal)
	err := Export(ExportOptions{})
	if err == nil || !strings.Contains(err.Error(), "the phone said: Error while accessing provider:x") {
		t.Errorf("sms refused: %v", err)
	}
}

// TestPartsReadSafely: an MMS part the phone cannot read is not kept as its file, and an _id that
// is not a number never reaches a path or a URI.
func TestPartsReadSafely(t *testing.T) {
	out := fakePhone(t, `printf 'JPEGDATA'`, refusal)
	var lines []string
	say := func(text string, params map[string]any) { lines = append(lines, text) }
	if err := Export(ExportOptions{Say: say}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "mms-parts", "9")); err == nil {
		t.Error("the phone's error was kept as the part")
	}
	if !strings.Contains(strings.Join(lines, "\n"), "-- mms-parts/{id}: not readable ({error})") {
		t.Errorf("lines: %q", lines)
	}

	fakePhone(t, `_id=9, ct=image/jpeg`, `_id=../../evil, ct=image/jpeg`, `_id=9\nRow: 1 _id=10`, `_id=../../evil\nRow: 1 _id=10`)
	if err := Export(ExportOptions{}); err == nil || !strings.Contains(err.Error(), "not a number") {
		t.Errorf("a bad _id: %v", err)
	}
}

// TestPartsTakenByTheGivenArchive: the parts not fetched again are those of the archive the app
// serves, not of the default one.
func TestPartsTakenByTheGivenArchive(t *testing.T) {
	out := fakePhone(t)
	other := filepath.Join(t.TempDir(), "other.db")
	a, _ := db.Open(other)
	db.Exec(a, "CREATE TABLE source (id INTEGER PRIMARY KEY, name TEXT)")
	db.Exec(a, "CREATE TABLE attachment (source_id INTEGER, source_path TEXT)")
	db.Exec(a, "INSERT INTO source VALUES (1, 'acme-phone1-abcd/mms')")
	db.Exec(a, "INSERT INTO attachment VALUES (1, 'mms-parts/9')")
	a.Close()
	if err := Export(ExportOptions{Archive: other}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "mms-parts", "9")); err == nil {
		t.Error("a part the given archive has was fetched again")
	}
}

func TestUnauthorized(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'List of devices attached\\nC3\\tunauthorized\\n'\n"
	os.WriteFile(filepath.Join(dir, "adb"), []byte(script), 0o700)
	e := &exporter{adbPath: filepath.Join(dir, "adb")}
	if _, err := e.device(); err == nil || !strings.Contains(err.Error(), "has not allowed USB debugging") {
		t.Errorf("%v", err)
	}
}
