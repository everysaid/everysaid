package db_test

import (
	"everysaid/internal/db"
	"os"
	"testing"
)

func TestURI(t *testing.T) {
	dir := t.TempDir() + "/a b#c?d%e"
	os.MkdirAll(dir, 0o700)
	d, err := db.Open(dir+"/x.db", "journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("CREATE TABLE t(a); INSERT INTO t VALUES(1); CREATE VIRTUAL TABLE f USING fts5(text, content='', contentless_delete=1, tokenize='trigram')"); err != nil {
		t.Fatal(err)
	}
	var fk int
	d.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	var jm string
	d.QueryRow("PRAGMA journal_mode").Scan(&jm)
	t.Log(fk, jm)
	if _, err := os.Stat(dir + "/x.db"); err != nil {
		t.Fatal(err)
	}
	r, _ := db.ReadOnly(dir + "/x.db")
	if _, err := r.Exec("INSERT INTO t VALUES(2)"); err == nil {
		t.Fatal("wrote")
	}
}
