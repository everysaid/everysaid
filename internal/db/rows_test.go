package db_test

import (
	"errors"
	"testing"

	"everysaid/internal/db"
)

// A statement that fails half way through its rows (an I/O error, a lock, here an overflow on
// the third row) stops whoever reads it, as a failing statement does: it is not taken for the end
// of the rows, which left a list cut short with no word of it.
func TestAStatementFailingHalfWayIsNotTheEndOfItsRows(t *testing.T) {
	d, err := db.Open(t.TempDir() + "/x.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const q = "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM n WHERE x < 5) " +
		"SELECT CASE WHEN x > 2 THEN abs(-9223372036854775808) ELSE x END AS v FROM n"
	ways := map[string]func(){
		"Maps": func() { db.Maps(d, q) },
		"Ints": func() { db.Ints(d, q) },
		"Query": func() {
			rows := db.Query(d, q)
			defer rows.Close()
			for rows.Next() {
				var v int64
				rows.Scan(&v)
			}
		},
	}
	for name, read := range ways {
		func() {
			defer func() {
				var e *db.Error
				if r, _ := recover().(error); !errors.As(r, &e) {
					t.Errorf("%s: the rows ended without the failure (%v)", name, r)
				}
			}()
			read()
		}()
	}
}
