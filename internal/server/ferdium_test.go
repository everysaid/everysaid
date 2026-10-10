package server

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"everysaid/internal/webui"
)

// The Ferdium recipe, to the user only, with this server's address in it.
func TestFerdiumRecipe(t *testing.T) {
	if webui.FS() == nil {
		t.Skip("no interface in this build (its icon is the recipe's)")
	}
	c := newServer(t)
	must(t, c.get("/api/ferdium/recipe.zip").status == 401, "not without signing in")
	c.login()
	r := c.get("/api/ferdium/recipe.zip")
	must(t, r.status == 200 && r.header.Get("Content-Type") == "application/zip", "zip: %d", r.status)
	z, err := zip.NewReader(bytes.NewReader(r.body), int64(len(r.body)))
	must(t, err == nil, "zip: %v", err)
	for _, f := range z.File {
		if f.Name == "everysaid/package.json" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			must(t, strings.Contains(string(b), `"serviceURL": "`+c.s.Origin()+`"`), "the address: %s", b)
			return
		}
	}
	t.Fatal("no package.json")
}
