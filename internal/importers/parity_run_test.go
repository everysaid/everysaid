package importers

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"everysaid/internal/archive"
)

// TestParityRun imports into EVERYSAID_PARITY_DB (a temporary check against the Python importers).
func TestParityRun(t *testing.T) {
	path := os.Getenv("EVERYSAID_PARITY_DB")
	if path == "" {
		t.Skip()
	}
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := Run(a, Chosen(strings.Fields(os.Getenv("EVERYSAID_PARITY_NAMES"))), func(s string) { fmt.Println(s) }); err != nil {
		t.Fatal(err)
	}
}
