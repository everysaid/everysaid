// Python's semantics where the importers depend on them: the importers are ported from Python
// (everysaid/*.py), whose truthiness, str(), round(), float timestamps, ISO dates and decoding of
// bad UTF-8 decide what goes into the archive, so they are matched here.
package importers

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

// row is a source row as column -> value (int64, float64, string, []byte or nil), as db.Maps
// gives it.
type row map[string]any

func (r row) has(k string) bool { _, ok := r[k]; return ok }

// truthy is Python's bool(value).
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case uint64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []byte:
		return len(x) > 0
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	case map[string]any:
		_, ordered := x[orderKey]
		return len(x) > 0 && !(ordered && len(x) == 1)
	case []any:
		return len(x) > 0
	}
	return true
}

// str is a value as text where it is text ("" for nil and for anything else).
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

// pyStr is Python's str(value).
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return pyFloat(x)
	case string:
		return x
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return strconv.FormatInt(i, 10)
		}
		f, _ := x.Float64()
		return pyFloat(f)
	case []byte:
		return string(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// pyFloat is Python's repr of a float.
func pyFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	exp, _ := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	if exp < -4 || exp >= 16 {
		return e
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

// num is a number as Python would have it: (int, float, is it an int, is it a number at all).
func num(v any) (int64, float64, bool, bool) {
	switch x := v.(type) {
	case int64:
		return x, float64(x), true, true
	case int:
		return int64(x), float64(x), true, true
	case uint64: // a protobuf varint
		return int64(x), float64(x), true, true
	case bool:
		if x {
			return 1, 1, true, true
		}
		return 0, 0, true, true
	case float64:
		return int64(x), x, false, true
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return i, float64(i), true, true
		}
		f, err := x.Float64()
		return int64(f), f, false, err == nil
	}
	return 0, 0, false, false
}

// toInt is a number's integer value (Python's int() of an int or a float), 0 for anything else.
func toInt(v any) int64 {
	i, _, _, _ := num(v)
	return i
}

func toFloat(v any) float64 {
	_, f, _, _ := num(v)
	return f
}

// pyInt is Python's int(value) of a number or a text: false where Python raises.
func pyInt(v any) (int64, bool) {
	if s, ok := v.(string); ok {
		t := strings.TrimFunc(s, archive.IsSpace)
		t = strings.ReplaceAll(t, "_", "")
		if t == "" || strings.HasPrefix(s, "_") {
			return 0, false
		}
		if i, err := strconv.ParseInt(t, 10, 64); err == nil {
			return i, true
		}
		return 0, false
	}
	i, _, _, ok := num(v)
	return i, ok
}

// roundEven is Python's round() of a float: halves to the even integer.
func roundEven(f float64) int64 { return int64(math.RoundToEven(f)) }

// appleMS is Unix ms of Apple seconds (as round((date + APPLE_EPOCH) * 1000) does).
func appleMS(date float64) int64 { return roundEven((date + archive.AppleEpoch) * 1000) }

// tsMS is Python's int(dt.timestamp() * 1000) of a time: the float of the seconds, times 1000,
// truncated.
func tsMS(t time.Time) int64 {
	micros := t.Unix()*1_000_000 + int64(t.Nanosecond()/1000)
	return int64(float64(micros) / 1e6 * 1000)
}

// mtime is Python's os.path.getmtime: seconds as a float.
func mtime(path string) float64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	t := fi.ModTime()
	return float64(t.Unix()) + float64(t.Nanosecond())*1e-9
}

func exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// decodeReplace is Python's bytes.decode("utf-8", "replace"): each maximal invalid part one U+FFFD.
func decodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || n > 1 {
			out.WriteRune(r)
			i += n
			continue
		}
		out.WriteRune(utf8.RuneError)
		i += invalidLen(b[i:])
	}
	return out.String()
}

// invalidLen is how many bytes an invalid sequence's maximal part takes (at least 1).
func invalidLen(b []byte) int {
	c := b[0]
	var need int
	lo, hi := byte(0x80), byte(0xBF)
	switch {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c == 0xE0:
		need, lo = 2, 0xA0
	case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
		need = 2
	case c == 0xED:
		need, hi = 2, 0x9F
	case c == 0xF0:
		need, lo = 3, 0x90
	case c >= 0xF1 && c <= 0xF3:
		need = 3
	case c == 0xF4:
		need, hi = 3, 0x8F
	default:
		return 1
	}
	n := 1
	for k := 0; k < need && n < len(b); k++ {
		x := b[n]
		if k == 0 {
			if x < lo || x > hi {
				break
			}
		} else if x < 0x80 || x > 0xBF {
			break
		}
		n++
	}
	return n
}

// splitLines is Python's str.splitlines().
func splitLines(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(rs[start:i]))
			start = i + 1
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// upper is Python's str.upper().
func upper(s string) string { return cases.Upper(language.Und).String(s) }

// pyStrip is Python's str.strip().
func pyStrip(s string) string { return strings.TrimFunc(s, archive.IsSpace) }

// --- times --------------------------------------------------------------------------------------

// wall is a local time as Python's datetime holds it with a ZoneInfo: the clock's reading, its
// zone applied only when it becomes an instant (fold 0: an hour said twice is its first, an hour
// skipped is read with the offset before the change).
type wall struct {
	t   time.Time // the reading, as if in UTC
	loc *time.Location
}

func newWall(y int, mo time.Month, d, h, mi, s int, loc *time.Location) (wall, bool) {
	t := time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	if t.Year() != y || t.Month() != mo || t.Day() != d || t.Hour() != h || t.Minute() != mi || t.Second() != s ||
		y < 1 || y > 9999 {
		return wall{}, false // Python's ValueError: no such day or time
	}
	return wall{t, loc}, true
}

func (w wall) add(d time.Duration) wall { return wall{w.t.Add(d), w.loc} }
func (w wall) before(o wall) bool       { return w.t.Before(o.t) }

// instant is the moment the reading names (Python's dt.timestamp(), fold 0).
func (w wall) instant() time.Time { return fold0(w.t, w.loc) }

func offsetAt(u int64, loc *time.Location) int64 {
	_, off := time.Unix(u, 0).In(loc).Zone()
	return int64(off)
}

// fold0 is the instant of a reading in a zone, as Python's zoneinfo with fold 0.
func fold0(reading time.Time, loc *time.Location) time.Time {
	l := reading.Unix()
	before, after := offsetAt(l-86400, loc), offsetAt(l+86400, loc)
	var best int64
	found := false
	for _, o := range []int64{before, after} {
		u := l - o
		if offsetAt(u, loc) == o && (!found || u < best) {
			best, found = u, true
		}
	}
	if !found {
		best = l - before
	}
	return time.Unix(best, int64(reading.Nanosecond())).In(loc)
}

var isoRE = regexp.MustCompile(`^(\d{4})-?(\d{2})-?(\d{2})(?:.(\d{2})(?::?(\d{2})(?::?(\d{2})(?:[.,](\d+))?)?)?)?` +
	`(Z|[+-]\d{2}(?::?\d{2}(?::?\d{2})?)?)?$`)

// fromISO is Python's datetime.fromisoformat(s): the instant, false where Python raises. A time
// without an offset is the system's local time (as Python's timestamp() takes a naive one).
func fromISO(s string) (time.Time, bool) {
	m := isoRE.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	at := func(i int) int {
		if m[i] == "" {
			return 0
		}
		n, _ := strconv.Atoi(m[i])
		return n
	}
	frac := m[7]
	if len(frac) > 6 {
		frac = frac[:6]
	}
	micro := 0
	if frac != "" {
		micro, _ = strconv.Atoi(frac + strings.Repeat("0", 6-len(frac)))
	}
	hour, nextDay := at(4), false
	if hour == 24 && at(5) == 0 && at(6) == 0 && micro == 0 { // 24:00 is the next day's start
		hour, nextDay = 0, true
	}
	w, ok := newWall(at(1), time.Month(at(2)), at(3), hour, at(5), at(6), time.Local)
	if !ok {
		return time.Time{}, false
	}
	reading := w.t.Add(time.Duration(micro) * time.Microsecond)
	if nextDay {
		reading = reading.AddDate(0, 0, 1)
	}
	z := m[8]
	if z == "" {
		return fold0(reading, time.Local), true
	}
	if z == "Z" {
		return reading, true
	}
	digits := strings.ReplaceAll(z[1:], ":", "")
	h, _ := strconv.Atoi(digits[0:2])
	mi, sec := 0, 0
	if len(digits) >= 4 {
		mi, _ = strconv.Atoi(digits[2:4])
	}
	if len(digits) >= 6 {
		sec, _ = strconv.Atoi(digits[4:6])
	}
	off := time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(sec)*time.Second
	if off >= 24*time.Hour {
		return time.Time{}, false
	}
	if z[0] == '-' {
		off = -off
	}
	return reading.Add(-off), true
}

// isoMS is a time as the bridge writes it, as Unix ms (0 for none), as whatsapp.ms() reads it.
func isoMS(stamp string) int64 {
	if stamp == "" {
		return 0
	}
	t, ok := fromISO(stamp)
	if !ok {
		panic(&db.Error{Query: "fromisoformat", Err: errBadTime(stamp)})
	}
	return tsMS(t)
}

type errBadTime string

func (e errBadTime) Error() string { return "Invalid isoformat string: " + string(e) }

// --- source databases ----------------------------------------------------------------------------

// ro opens a source's database read only.
func ro(path string) *sql.DB {
	d, err := db.ReadOnly(path)
	if err != nil {
		panic(&db.Error{Query: "open " + path, Err: err})
	}
	d.SetMaxOpenConns(1)
	return d
}

// maps is every row of a question as column -> value.
func maps(q db.Querier, query string, args ...any) []row {
	ms := db.Maps(q, query, args...)
	out := make([]row, len(ms))
	for i, m := range ms {
		out[i] = row(m)
	}
	return out
}

// eachMap calls fn with each row as column -> value, without holding them all.
func eachMap(q db.Querier, query string, args []any, fn func(row)) {
	rows := db.Query(q, query, args...)
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		m := make(row, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		fn(m)
	}
	if err := rows.Err(); err != nil {
		panic(&db.Error{Query: query, Err: err})
	}
}

func hasTable(q db.Querier, name string) bool {
	return db.Exists(q, "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", name)
}

func columns(q db.Querier, table string) map[string]bool {
	out := map[string]bool{}
	for _, c := range maps(q, "PRAGMA table_info("+table+")") {
		out[str(c["name"])] = true
	}
	return out
}

// isDigit is Python's \d: any decimal digit.
func isDigit(r rune) bool { return unicode.Is(unicode.Nd, r) }

// digitValue is a decimal digit's value (digits come in runs of ten, from 0).
func digitValue(r rune) int {
	start := r
	for unicode.Is(unicode.Nd, start-1) {
		start--
	}
	return int(r-start) % 10
}

// atoiPy is Python's int() of a string of \d digits.
func atoiPy(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + digitValue(r)
	}
	return n
}
