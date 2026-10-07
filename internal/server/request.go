// What every handler needs: the request's body and query as FastAPI gave them, and the answer as
// JSON (errors in the shape the interface knows: {"detail": {"code", "params", "text"?}}).
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"unicode"

	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
)

// req is one request as a handler sees it.
type req struct {
	w       http.ResponseWriter
	r       *http.Request
	s       *Server
	uid     int64  // the signed-in user (0 on the open routes)
	session string // the session's hash
	body    map[string]any
}

// done is what a handler returns when it wrote its answer itself (a file, a cookie).
type doneT struct{}

var done = doneT{}

// invalid is a request FastAPI would refuse before the handler (422): a value of the wrong type.
type invalid struct {
	where, name, msg string
}

func (e *invalid) Error() string { return e.msg }

func (q *req) lang() string {
	if l := q.r.URL.Query().Get("lang"); l != "" {
		return l
	}
	if l := q.r.Header.Get("X-Lang"); l != "" {
		return l
	}
	return "en"
}

func (q *req) xlang() string {
	if l := q.r.Header.Get("X-Lang"); l != "" {
		return l
	}
	return "en"
}

// --- path and query ------------------------------------------------------------------------------

func (q *req) pathInt(name string) (int64, error) {
	v := q.r.PathValue(name)
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, &invalid{"path", name, "Input should be a valid integer"}
	}
	return n, nil
}

func (q *req) has(name string) bool { return q.r.URL.Query().Has(name) }

func (q *req) str(name string) string { return q.r.URL.Query().Get(name) }

func (q *req) intQ(name string, def int64) (int64, error) {
	if !q.has(name) {
		return def, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(q.str(name)), 10, 64)
	if err != nil {
		return 0, &invalid{"query", name, "Input should be a valid integer"}
	}
	return n, nil
}

// optInt is an optional integer of the query (nil when not given).
func (q *req) optInt(name string) (*int64, error) {
	if !q.has(name) {
		return nil, nil
	}
	n, err := q.intQ(name, 0)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// boolQ reads a bool as Pydantic does: true/false, 1/0, yes/no, on/off, t/f, y/n.
func (q *req) boolQ(name string, def bool) (bool, error) {
	if !q.has(name) {
		return def, nil
	}
	b, ok := parseBool(q.str(name))
	if !ok {
		return false, &invalid{"query", name, "Input should be a valid boolean"}
	}
	return b, nil
}

func (q *req) optBool(name string) (*bool, error) {
	if !q.has(name) {
		return nil, nil
	}
	b, err := q.boolQ(name, false)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func parseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "t", "yes", "y", "on":
		return true, true
	case "0", "false", "f", "no", "n", "off":
		return false, true
	}
	return false, false
}

// --- the body ------------------------------------------------------------------------------------

// decodeJSON reads JSON as Python's json.loads: integers stay integers (int64), the rest float64.
func decodeJSON(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, errors.New("extra data")
	}
	return numbers(v), nil
}

func numbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if !strings.ContainsAny(string(x), ".eE") {
			if n, err := x.Int64(); err == nil {
				return n
			}
		}
		f, _ := x.Float64()
		return f
	case []any:
		for i := range x {
			x[i] = numbers(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = numbers(x[k])
		}
	}
	return v
}

// readBody reads a JSON object body; required: an empty or other body is refused (422) as FastAPI's
// Body(...) does; else it is {}.
func (q *req) readBody(required bool) error {
	b, err := io.ReadAll(http.MaxBytesReader(q.w, q.r.Body, 10<<20))
	if err != nil {
		return &invalid{"body", "", "Body too large or unreadable"}
	}
	if len(bytes.TrimSpace(b)) == 0 {
		if required {
			return &invalid{"body", "", "Field required"}
		}
		q.body = map[string]any{}
		return nil
	}
	v, err := decodeJSON(b)
	if err != nil {
		return &invalid{"body", "", "JSON decode error"}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return &invalid{"body", "", "Input should be a valid dictionary"}
	}
	q.body = m
	return nil
}

// get is body.get(k) (nil when missing).
func (q *req) get(k string) any { return q.body[k] }

// text is (body.get(k) or "") as text.
func (q *req) text(k string) string {
	switch v := q.body[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		if !truthy(v) {
			return ""
		}
		return pyStr(v)
	}
}

// pyStr is Python's str() of a JSON value.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return x
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e16 {
			return strconv.FormatFloat(x, 'f', 1, 64)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// pyInt is Python's int() of a JSON value: whole numbers, floats cut toward zero, text of digits.
func pyInt(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, fmt.Errorf("cannot convert float %v to integer", x)
		}
		return int64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case string:
		s := strings.ReplaceAll(strings.TrimFunc(x, unicode.IsSpace), "_", "")
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid literal for int() with base 10: %q", x)
		}
		return n, nil
	case nil:
		return 0, errors.New("int() argument must be a string, a bytes-like object or a real number, not 'NoneType'")
	}
	return 0, fmt.Errorf("int() argument must be a string or a number, not %T", v)
}

// ints is a list of whole numbers (each as pyInt).
func ints(v any) ([]int64, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		if !truthy(v) {
			return nil, nil
		}
		return nil, fmt.Errorf("not a list: %s", pyStr(v))
	}
	out := make([]int64, 0, len(list))
	for _, x := range list {
		n, err := pyInt(x)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// truthy is Python's truth of a JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int64:
		return x != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// --- answers -------------------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		status = 500
		b.Reset()
		b.WriteString(`{"detail":"Internal Server Error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(bytes.TrimRight(b.Bytes(), "\n"))
}

func userErrorDetail(e *errs.UserError, lang string) map[string]any {
	params := e.Params
	if params == nil {
		params = map[string]any{}
	}
	d := map[string]any{"code": e.Code, "params": params}
	if e.Text != "" {
		d["text"] = i18n.Tr(e.Text, lang)
	}
	return d
}

// writeError answers a failure: a UserError as its code, a value FastAPI would have refused as 422,
// anything else as 500 (written to the server's log).
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ue *errs.UserError
	var bad *invalid
	switch {
	case errors.As(err, &ue):
		lang := r.Header.Get("X-Lang")
		if lang == "" {
			lang = "en"
		}
		writeJSON(w, ue.Status, map[string]any{"detail": userErrorDetail(ue, lang)})
	case errors.As(err, &bad):
		loc := []any{bad.where}
		if bad.name != "" {
			loc = append(loc, bad.name)
		}
		writeJSON(w, 422, map[string]any{"detail": []any{map[string]any{"loc": loc, "msg": bad.msg, "type": "value_error"}}})
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeJSON(w, 500, map[string]any{"detail": "Internal Server Error"})
	}
}

// failed is UserError("failed", status, reason=...): a change that could not be made, and why.
func failed(status int, reason string) error {
	return errs.New("failed", status, map[string]any{"reason": reason})
}

// notFound is the 404 of a missing thing (code not_found unless said).
func notFound(code string) error {
	if code == "" {
		code = "not_found"
	}
	return errs.New(code, 404, nil)
}

// passUser is err as it is when the user is to be told it in its own words, else what other() makes.
func passUser(err error, other func(error) error) error {
	var ue *errs.UserError
	var bad *invalid
	if err == nil || errors.As(err, &ue) || errors.As(err, &bad) {
		return err
	}
	return other(err)
}

// is404 makes core.ErrNotFound a 404, and leaves the rest to other (nil: as they are).
func is404(err error, other func(error) error) error {
	if errors.Is(err, core.ErrNotFound) {
		return notFound("")
	}
	if other == nil {
		return err
	}
	return passUser(err, other)
}

// handler is a route's work: the answer (JSON), or done when it wrote it.
type handler func(q *req) (any, error)

type routeOpt int

const (
	bodyNone     routeOpt = iota
	bodyRequired          // body: dict = Body(...)
	bodyOptional          // body: dict = Body(default={})
)

func (s *Server) wrap(body routeOpt, fn handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := &req{w: w, r: r, s: s, body: map[string]any{}}
		if a, ok := r.Context().Value(authKey{}).(*authInfo); ok {
			q.uid, q.session = a.uid, a.session
		}
		var out any
		err := func() (err error) {
			defer func() {
				if rec := recover(); rec != nil {
					if e, ok := rec.(error); ok {
						var dbe *db.Error
						if errors.As(e, &dbe) {
							err = e
							return
						}
					}
					err = fmt.Errorf("panic: %v\n%s", rec, debug.Stack())
				}
			}()
			if body != bodyNone {
				if err := q.readBody(body == bodyRequired); err != nil {
					return err
				}
			}
			out, err = fn(q)
			return err
		}()
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if _, ok := out.(doneT); ok {
			return
		}
		writeJSON(w, 200, out)
	}
}
