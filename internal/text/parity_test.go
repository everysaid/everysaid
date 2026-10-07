package text_test

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/text"
)

// TestParity folds the JSON strings of EVERYSAID_PARITY_IN (one per line) into EVERYSAID_PARITY_OUT,
// for a comparison with the Python's.
func TestParity(t *testing.T) {
	in, out := os.Getenv("EVERYSAID_PARITY_IN"), os.Getenv("EVERYSAID_PARITY_OUT")
	if in == "" {
		t.Skip("no EVERYSAID_PARITY_IN")
	}
	f, _ := os.Open(in)
	defer f.Close()
	w, _ := os.Create(out)
	defer w.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for sc.Scan() {
		var s string
		json.Unmarshal(sc.Bytes(), &s)
		k, v := archive.Address(s, os.Getenv("EVERYSAID_PARITY_REGION"))
		enc.Encode([]string{text.Fold(s), k, v})
	}
}
