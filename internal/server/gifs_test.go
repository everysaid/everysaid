package server

import (
	"testing"

	_ "everysaid/internal/plugins/gifs" // History, as the app has it
)

// The GIFs: the History (there from the start, its name in the user's language) and the plugins that
// find them, in the order added; a search and its next page, a GIF's file; one that cannot now says
// why and is not asked.
func TestGIFs(t *testing.T) {
	c := newServer(t)
	c.login()
	list := items(c.getJSON("/api/gifs?lang=el"))
	must(t, len(list) == 2 && list[0]["history"] == true && list[0]["name"] == "Ιστορικό" &&
		list[1]["name"] == "Finder" && list[1]["why"] == nil && list[1]["wait_ms"].(float64) == 1000, "GIFs: %v", list)
	iid := int64(list[1]["instance"].(float64))
	base := "/api/gifs/" + itoa(iid)

	r := c.getJSON(base + "/search?q=dog")
	found := items(r)
	must(t, len(found) == 1 && found[0]["id"] == "dog-1" && found[0]["width"].(float64) == 2 && r["next"] == "2", "search: %v", r)
	r = c.getJSON(base + "/search?q=dog&offset=2")
	must(t, len(items(r)) == 1 && items(r)[0]["id"] == "dog-2" && r["next"] == "", "next page: %v", r)

	f := c.get(base + "/dog-1")
	must(t, f.status == 200 && string(f.body) == "mp4 of dog-1" && f.header.Get("Content-Type") == "video/mp4", "file: %d %v", f.status, f.header)
	must(t, c.get(base+"/gone").status == 404, "a GIF no longer found")
	must(t, c.get("/api/gifs/999/search?q=x").status == 404, "no such instance")

	noGIFs.Store(true)
	defer noGIFs.Store(false)
	list = items(c.getJSON("/api/gifs"))
	must(t, len(list) == 2 && list[1]["why"] == "Not signed in", "why not: %v", list)
	must(t, c.get(base+"/search?q=dog").status == 409, "not asked when it cannot")
}
