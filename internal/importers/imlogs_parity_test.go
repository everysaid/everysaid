package importers

import (
	"os"
	"strings"
	"testing"

	"everysaid/internal/config"
)

func TestImlogsParityRun(t *testing.T) {
	db := os.Getenv("IMLOGS_PARITY_DB")
	if db == "" {
		t.Skip()
	}
	// the package's TestMain points the folders at a test root: here they are the parity run's
	for _, v := range []string{"DATA", "CACHE", "CONFIG", "STATE", "KEYRING"} {
		t.Setenv("EVERYSAID_"+v, os.Getenv("IMLOGS_PARITY_"+v))
	}
	config.Load()
	t.Cleanup(config.Load)
	var lines []string
	ok, err := ImlogsImport(db, os.Getenv("IMLOGS_PARITY_ADIUM"), os.Getenv("IMLOGS_PARITY_PIDGIN"), false,
		func(s string) { lines = append(lines, s) })
	os.WriteFile(db+".out", []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
}
