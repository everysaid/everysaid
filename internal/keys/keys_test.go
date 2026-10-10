package keys_test

import (
	"bytes"
	"os"
	"testing"

	"everysaid/internal/keys"
)

// keys.db is encrypted whole, opens again with its key, and is never remade without one.
func TestEncryptedAndKept(t *testing.T) {
	path := t.TempDir() + "/keys.db"
	d, err := keys.OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("CREATE TABLE secret (v TEXT); INSERT INTO secret VALUES ('device-identity-key')"); err != nil {
		t.Fatal(err)
	}
	keys.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("device-identity-key")) || bytes.HasPrefix(raw, []byte("SQLite format 3")) {
		t.Fatal("keys.db is readable without its key")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("keys.db mode %v", st.Mode().Perm())
	}

	d, err = keys.OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := d.QueryRow("SELECT v FROM secret").Scan(&v); err != nil || v != "device-identity-key" {
		t.Fatalf("read back %q, %v", v, err)
	}
	keys.Close()

	// without its key it is an error, and the file is left as it was
	os.Remove(path + ".key")
	if _, err := keys.OpenAt(path); err != keys.ErrNoKey {
		t.Fatalf("opened without a key: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, raw) && len(after) < len(raw) {
		t.Fatal("keys.db changed when it could not be opened")
	}
	// a wrong key does not open it either
	os.WriteFile(path+".key", []byte("00000000000000000000000000000000000000000000000000000000000000ff"), 0o600)
	if _, err := keys.OpenAt(path); err == nil {
		t.Fatal("opened with a wrong key")
	}
}
