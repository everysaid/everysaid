package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// Querier is what reads and writes: a *sql.DB, a *sql.Tx or a *sql.Conn.
type Querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Error is a failed statement. The helpers below panic with it, as a script would stop: a database
// that fails a statement is not something the code around it can mend. Recover (in a request's
// handler, an import's run) turns it back into an error.
type Error struct {
	Query string
	Err   error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%v (in %.160s)", e.Err, strings.Join(strings.Fields(e.Query), " "))
}
func (e *Error) Unwrap() error { return e.Err }

// Recover turns a panic of these helpers (or any error panicked) back into an error:
// `defer db.Recover(&err)`.
func Recover(err *error) {
	if r := recover(); r != nil {
		if e, ok := r.(error); ok {
			*err = e
			return
		}
		panic(r)
	}
}

// Exec runs a statement.
func Exec(q Querier, query string, args ...any) sql.Result {
	r, err := q.Exec(query, args...)
	if err != nil {
		panic(&Error{query, err})
	}
	return r
}

// LastID runs an INSERT and gives the new row's id.
func LastID(q Querier, query string, args ...any) int64 {
	id, _ := Exec(q, query, args...).LastInsertId()
	return id
}

// Changed runs a statement and gives how many rows it changed.
func Changed(q Querier, query string, args ...any) int64 {
	n, _ := Exec(q, query, args...).RowsAffected()
	return n
}

// Rows is a result read row by row; a failed scan panics.
type Rows struct {
	*sql.Rows
	query string
}

func (r *Rows) Scan(dest ...any) {
	if err := r.Rows.Scan(dest...); err != nil {
		panic(&Error{r.query, err})
	}
}

// Query runs a question; the caller closes the rows.
func Query(q Querier, query string, args ...any) *Rows {
	rows, err := q.Query(query, args...)
	if err != nil {
		panic(&Error{query, err})
	}
	return &Rows{rows, query}
}

// Each calls fn for each row, with a scan of that row.
func Each(q Querier, query string, args []any, fn func(scan func(dest ...any))) {
	rows := Query(q, query, args...)
	defer rows.Close()
	for rows.Next() {
		fn(rows.Scan)
	}
	if err := rows.Err(); err != nil {
		panic(&Error{query, err})
	}
}

// Row reads one row into dest; false when there is none.
func Row(q Querier, query string, args []any, dest ...any) bool {
	err := q.QueryRow(query, args...).Scan(dest...)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		panic(&Error{query, err})
	}
	return true
}

// Int is the first column of the first row, 0 when there is none or it is NULL.
func Int(q Querier, query string, args ...any) int64 {
	var v sql.NullInt64
	Row(q, query, args, &v)
	return v.Int64
}

// IntOK is the first column of the first row, and whether there was one with a value.
func IntOK(q Querier, query string, args ...any) (int64, bool) {
	var v sql.NullInt64
	ok := Row(q, query, args, &v)
	return v.Int64, ok && v.Valid
}

// Str is the first column of the first row as text, "" when there is none or it is NULL.
func Str(q Querier, query string, args ...any) string {
	var v sql.NullString
	Row(q, query, args, &v)
	return v.String
}

// Ints is the first column of every row.
func Ints(q Querier, query string, args ...any) []int64 {
	var out []int64
	Each(q, query, args, func(scan func(...any)) {
		var v sql.NullInt64
		scan(&v)
		out = append(out, v.Int64)
	})
	return out
}

// Strs is the first column of every row, as text.
func Strs(q Querier, query string, args ...any) []string {
	var out []string
	Each(q, query, args, func(scan func(...any)) {
		var v sql.NullString
		scan(&v)
		out = append(out, v.String)
	})
	return out
}

// Exists says whether the question has a row.
func Exists(q Querier, query string, args ...any) bool {
	var x any
	return Row(q, query, args, &x)
}

// Maps is every row as column -> value (int64, float64, string, []byte or nil; text as string).
func Maps(q Querier, query string, args ...any) []map[string]any {
	rows := Query(q, query, args...)
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out
}

// Marks is "?,?,?" for n values.
func Marks(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// Args turns a slice of ids into query arguments.
func Args[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// NullStr is "" as NULL; NullID is 0 as NULL.
func NullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func NullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// B is a bool as SQLite's 0 or 1.
func B(b bool) int {
	if b {
		return 1
	}
	return 0
}
