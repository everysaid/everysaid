package server

// The server's routes, end to end on a small archive, and the server's part of mentions and receipts.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"everysaid/internal/core"
	"everysaid/internal/db"
)

func must(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

func TestGuards(t *testing.T) {
	c := newServer(t)
	must(t, c.get("/api/chats").status == 401, "chats without a session")
	h := c.get("/api/health")
	must(t, string(h.body) == `{"ok":true}`, "health: %s", h.body)
	must(t, strings.Contains(h.header.Get("Content-Security-Policy"), "frame-ancestors 'none'"), "csp")
	must(t, h.header.Get("X-Frame-Options") == "DENY", "frame options")
	must(t, h.header.Get("Cache-Control") == "no-store", "no-store: %q", h.header.Get("Cache-Control"))
	evil := c.do("GET", "/api/health", nil, map[string]string{"X-Test-Host": "evil.example"})
	must(t, evil.status == 421 && evil.code() == "auth.unknown_host", "evil host: %d", evil.status)
	c.login()
	must(t, c.get("/api/chats").status == 200, "chats with a session")
	first := items(c.getJSON("/api/chats"))[0]["id"].(string)
	must(t, c.do("POST", "/api/chats/"+first+"/read", "{}", nil).status == 403, "no X-Everysaid")
	r := c.do("POST", "/api/chats/"+first+"/read", "{}", map[string]string{"X-Everysaid": "1", "Origin": "https://evil.example"})
	must(t, r.status == 403 && r.code() == "auth.foreign_origin", "foreign origin: %d", r.status)
	must(t, c.post("/api/chats/"+first+"/read", M{}).status == 200, "read")
	must(t, c.get("/api/nothing-here").status == 404 && c.get("/api/nothing-here").code() == "not_found", "unknown api")
	must(t, c.post("/api/chats", M{}).status == 405, "a route with another method")
}

func TestStatusAndSetup(t *testing.T) {
	c := newServer(t)
	s := c.getJSON("/api/auth/status")
	must(t, s["needs_setup"] == true && s["logged_in"] == false && s["rp_id"] == "localhost", "status: %v", s)
	must(t, c.post("/api/auth/register/options", M{"token": "wrong"}).status == 403, "wrong token")
	token := c.s.Auth.SetupLink(nil, 60)
	r := c.post("/api/auth/register/options", M{"token": token, "name": "Me"})
	must(t, r.status == 200, "register options: %d %s", r.status, r.body)
	opts := r.json()["options"].(map[string]any)
	must(t, opts["authenticatorSelection"].(map[string]any)["residentKey"] == "required", "resident key: %v", opts)
	must(t, opts["rp"].(map[string]any)["id"] == "localhost", "rp: %v", opts["rp"])
	login := c.post("/api/auth/login/options", nil).json()["options"].(map[string]any)
	must(t, login["rpId"] == "localhost", "login options: %v", login)
}

func TestRecovery(t *testing.T) {
	c := newServer(t)
	uid := c.s.Auth.CreateUser("Me", c.s.Store.Path)
	codes := c.s.Auth.NewRecoveryCodes(uid)
	must(t, c.post("/api/auth/recover", M{"code": "nope"}).status == 403, "bad code")
	r := c.post("/api/auth/recover", M{"code": codes[0]})
	must(t, r.status == 200 && num(r.json()["left"]) == 9, "recover: %d %s", r.status, r.body)
	must(t, c.getJSON("/api/auth/account")["user"].(map[string]any)["name"] == "Me", "account")
	c.clearCookies()
	must(t, c.post("/api/auth/recover", M{"code": codes[0]}).status == 403, "a code is used once")
}

func TestAPIFlow(t *testing.T) {
	c := newServer(t)
	c.login()
	var person M
	for _, x := range items(c.getJSON("/api/chats")) {
		if x["type"] == "person" {
			person = x
			break
		}
	}
	must(t, person != nil, "a person's chat")
	page := c.getJSON("/api/chats/" + person["id"].(string) + "/stream?limit=20")
	must(t, len(items(page)) <= 20 && len(items(page)) > 0, "stream")
	detail := c.getJSON("/api/chats/" + person["id"].(string))
	must(t, detail["person"].(map[string]any)["name"] == person["title"], "chat's person: %v", detail["person"])
	must(t, num(c.getJSON("/api/search?q=καλημερα")["total"]) > 0, "search")
	pid := num(person["person_id"])
	r := c.do("PATCH", fmt.Sprintf("/api/people/%d", pid), M{"name": "Νέο Όνομα"}, H)
	must(t, r.json()["name"] == "Νέο Όνομα", "rename: %s", r.body)
	m := items(c.getJSON("/api/media?kind=image"))[0]
	sha := m["sha256"].(string)
	th := c.get("/api/media/" + sha + "/thumb")
	must(t, th.status == 200 && th.header.Get("Content-Type") == "image/jpeg", "thumb: %d %s", th.status, th.header.Get("Content-Type"))
	must(t, strings.Contains(th.header.Get("Cache-Control"), "immutable"), "thumb cached")
	orig := c.get("/api/media/" + sha + "/original")
	must(t, orig.status == 200 && orig.header.Get("Content-Type") == "image/jpeg", "original: %d", orig.status)
	must(t, c.get("/api/media/"+sha+"/huge").status == 404, "unknown size")
	must(t, num(c.getJSON("/api/stats")["messages"]) > 10, "stats")
	must(t, len(items(c.getJSON("/api/plugins/catalog"))) >= 3, "catalog")
	must(t, len(items(c.getJSON("/api/plugins"))) > 0, "instances")
	must(t, c.get("/api/avatar/999999").code() == "no_photo", "no avatar")
	must(t, c.get("/api/messages/abc").status == 422, "a path that is not a number")
	must(t, c.get("/api/messages/999999").status == 404, "no message")
}

func TestLibraryFolder(t *testing.T) {
	c := newServer(t)
	c.login()
	lib := filepath.Join(t.TempDir(), "photos")
	os.MkdirAll(lib, 0o700)
	r := c.post("/api/plugins", M{"plugin": "test-folder", "label": "Test photos", "settings": M{"path": lib}})
	must(t, r.status == 200, "add library: %d %s", r.status, r.body)
	iid := num(r.json()["id"])
	sha := items(c.getJSON("/api/media?kind=image"))[0]["sha256"].(string)
	r = c.post("/api/media/"+sha+"/library", M{"instance_id": iid})
	must(t, r.status == 200 && r.json()["already"] == false, "to the library: %d %s", r.status, r.body)
	found, _ := filepath.Glob(filepath.Join(lib, "*", "*.jpg"))
	must(t, len(found) == 1, "stored: %v", found)
	r = c.post("/api/media/"+sha+"/library", M{"instance_id": iid})
	must(t, r.json()["already"] == true, "already there: %s", r.body)
	r = c.post("/api/media/0000/library", M{"instance_id": iid})
	must(t, r.status == 409 && r.code() == "file_gone", "a file the archive does not have: %d %s", r.status, r.body)
}

func dialEvents(c *client) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(c.ts.URL, "http") + "/api/events"
	return websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: c.hc})
}

func TestWebSocket(t *testing.T) {
	c := newServer(t)
	if _, r, err := dialEvents(c); err == nil || r == nil || r.StatusCode != 403 {
		t.Fatalf("a WebSocket without a session: %v", err)
	}
	c.login()
	ws, _, err := dialEvents(c)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var hello M
	if err := wsjson.Read(ctx, ws, &hello); err != nil || hello["type"] != "hello" {
		t.Fatalf("hello: %v %v", hello, err)
	}
	// new messages: the chats they are in
	m0 := db.Int(c.s.Store.Read(), "SELECT max(id) FROM message") - 1
	c.s.Host.Emit(M{"type": "new", "messages": []int64{m0, m0 + 1}, "calls": []int64{0, 0}})
	var ev M
	if err := wsjson.Read(ctx, ws, &ev); err != nil || ev["type"] != "new" || len(ev["chats"].(map[string]any)) != 1 {
		t.Fatalf("new: %v %v", ev, err)
	}
}

func TestPasswordWithCode(t *testing.T) {
	c := newServer(t)
	token := c.s.Auth.SetupLink(nil, 60)
	o := c.post("/api/auth/password/options", M{"token": token, "name": "Me"}).json()
	must(t, strings.HasPrefix(o["uri"].(string), "otpauth://totp/"), "uri: %v", o)
	pw := "a long enough password"
	must(t, c.post("/api/auth/password/set", M{"nonce": "nope", "password": pw, "code": "000000"}).status == 410, "no such nonce")
	must(t, c.post("/api/auth/password/set", M{"nonce": o["nonce"], "password": "short", "code": "000000"}).status == 400, "short")
	must(t, c.post("/api/auth/password/set", M{"nonce": o["nonce"], "password": pw, "code": "000000"}).status == 400, "wrong code")
	secret := o["secret"].(string)
	code := TOTPAt(secret, time.Now().Unix()/30)
	r := c.post("/api/auth/password/set", M{"nonce": o["nonce"], "password": pw, "code": code})
	must(t, r.status == 200 && len(r.json()["recovery_codes"].([]any)) == 10, "set: %d %s", r.status, r.body) // mistakes did not use the setup up
	must(t, c.post("/api/auth/password/set", M{"nonce": o["nonce"], "password": pw, "code": code}).status == 410, "once")
	must(t, c.getJSON("/api/auth/account")["has_password"] == true, "has password")
	c.clearCookies()
	must(t, c.post("/api/auth/password/login", M{"name": "Me", "password": "wrong password!", "code": code}).status == 403, "wrong password")
	must(t, c.post("/api/auth/password/login", M{"name": "me", "password": pw, "code": code}).status == 403, "a code is accepted once")
	next := TOTPAt(secret, time.Now().Unix()/30+1)
	r = c.post("/api/auth/password/login", M{"name": "me", "password": pw, "code": next})
	must(t, r.status == 200, "login: %d %s", r.status, r.body)
	must(t, c.get("/api/chats").status == 200, "signed in")
}

func TestPasswordLockout(t *testing.T) {
	c := newServer(t)
	a := c.s.Auth
	uid := a.CreateUser("Lock", c.s.Store.Path)
	secret := TOTPSecret()
	a.SetPassword(uid, "a long enough password", secret, nil)
	for range 5 {
		must(t, a.CheckPassword("Lock", "wrong password!!", "000000", "") == 0, "wrong")
	}
	code := TOTPAt(secret, time.Now().Unix()/30)
	must(t, a.CheckPassword("Lock", "a long enough password", code, "") == 0, "locked now")
	must(t, a.Locked(uid, ""), "locked")
	must(t, a.CheckPassword("Lock", "a long enough password", code, "10.0.0.9") == uid, "another address is not locked out")
}

func TestPluginsDeclareNamesServicesAndSending(t *testing.T) {
	c := newServer(t)
	c.login()
	looks := c.getJSON("/api/services?lang=el")
	must(t, len(looks) > 0, "services")
	for k, v := range looks {
		m := v.(map[string]any)
		for _, f := range []string{"name", "color", "short", "messages"} {
			_, ok := m[f]
			must(t, ok, "service %s without %s", k, f)
		}
	}
	names := c.getJSON("/api/names")
	ids := func(m M) []string {
		var out []string
		for _, x := range m["order"].([]any) {
			out = append(out, x.(map[string]any)["id"].(string))
		}
		return out
	}
	order := ids(names)
	must(t, names["custom"] == false && len(order) == 2 && order[0] == "whatsapp/book", "default order: %v", names)
	mine := []string{order[1], order[0]}
	c.do("PUT", "/api/settings", M{"name_order": mine}, H)
	must(t, slices.Equal(ids(c.getJSON("/api/names")), mine), "the user's order")
	must(t, c.do("PUT", "/api/settings", M{"name_order": []string{"no-such-source"}}, H).status == 200, "refused quietly")
	must(t, slices.Equal(ids(c.getJSON("/api/names")), mine), "refused, kept")
	c.do("PUT", "/api/settings", M{"name_order": nil}, H)
	back := c.getJSON("/api/names")
	must(t, back["custom"] == false && slices.Equal(ids(back), order), "back to the plugins' order: %v", back)
	var chat string
	for _, x := range items(c.getJSON("/api/chats")) {
		if x["type"] == "person" && slices.Contains(x["services"].([]any), any("whatsapp")) {
			chat = x["id"].(string)
			break
		}
	}
	detail := c.getJSON("/api/chats/" + chat)
	must(t, len(detail["sendable"].([]any)) > 0, "sendable: %v", detail["sendable"])
	for _, p := range items(c.getJSON("/api/plugins")) {
		if p["kind"] == "source" {
			c.do("PATCH", fmt.Sprintf("/api/plugins/%d", num(p["id"])), M{"enabled": false}, H)
		}
	}
	must(t, len(c.getJSON("/api/chats/" + chat)["sendable"].([]any)) == 0, "no source can send now")
}

func TestChangingWaysInNeedsARecentSignIn(t *testing.T) {
	c := newServer(t)
	c.login()
	must(t, c.post("/api/auth/password/options", M{}).status == 200, "recent: allowed")
	db.Exec(c.s.Auth.DB(), "UPDATE session SET created_at = created_at - 3600") // signed in an hour ago
	for _, x := range [][2]string{{"POST", "/api/auth/password/options"}, {"POST", "/api/auth/recovery-codes"},
		{"POST", "/api/auth/register/options"}, {"DELETE", "/api/auth/password"}, {"POST", "/api/auth/mcp-token"}} {
		var body any
		if x[0] == "POST" {
			body = M{}
		}
		r := c.do(x[0], x[1], body, H)
		must(t, r.status == 403 && r.code() == "auth.recent_sign_in", "%s: %d %s", x[1], r.status, r.body)
	}
	must(t, c.get("/api/chats").status == 200, "everything else still works")
}

func TestASettingThatIsNotWhatItMustBeIsRefused(t *testing.T) {
	c := newServer(t)
	c.login()
	made := c.post("/api/plugins", M{"plugin": "test-sender", "label": "Test iPhone"}).json()
	iid := num(made["id"])
	bad := c.do("PATCH", fmt.Sprintf("/api/plugins/%d", iid), M{"settings": M{"udid": "Somebody"}},
		map[string]string{"X-Everysaid": "1", "X-Lang": "el"})
	must(t, bad.status == 400 && bad.code() == "settings.invalid", "invalid: %d %s", bad.status, bad.body)
	d := bad.json()["detail"].(map[string]any)["params"].(map[string]any)
	must(t, d["value"] == "Somebody" && d["field"] == "Σειριακός αριθμός", "params: %v", d)
	good := c.do("PATCH", fmt.Sprintf("/api/plugins/%d", iid), M{"settings": M{"udid": "00008110-001A2B3C4D5E6F70"}}, H)
	must(t, good.status == 200 && good.json()["settings"].(map[string]any)["udid"] == "00008110-001A2B3C4D5E6F70", "valid: %s", good.body)
	must(t, c.do("PATCH", fmt.Sprintf("/api/plugins/%d", iid), M{"settings": M{"udid": ""}}, H).status == 200, "empty: found by itself")
}

func drain() {
	for {
		select {
		case <-askedCh:
		default:
			return
		}
	}
}

func next(t *testing.T) asked {
	t.Helper()
	select {
	case a := <-askedCh:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("the plugin was not asked")
	}
	return asked{}
}

// What was sent, as the chat shows it, where the plugin says what went (none where it cannot).
func TestSendSaysWhatWent(t *testing.T) {
	c := newServer(t)
	c.login()
	drain()
	var group string
	for _, x := range items(c.getJSON("/api/chats?kind=group")) {
		group = x["id"].(string)
	}
	r := c.post("/api/chats/"+group+"/send", M{"text": "write: hello", "service": "whatsapp"})
	must(t, r.status == 200, "send: %d %s", r.status, r.body)
	next(t)
	went := r.json()["messages"].([]any)
	must(t, len(went) == 1, "messages: %v", went)
	m := went[0].(map[string]any)
	must(t, m["text"] == "write: hello" && m["outgoing"] == true && num(m["conversation_id"]) == num(r.json()["conversation_id"]) &&
		strings.Contains(m["cursor"].(string), ":m:"), "message: %v", m)
	r = c.post("/api/chats/"+group+"/send", M{"text": "unsaid", "service": "whatsapp"})
	must(t, r.status == 200, "send: %d %s", r.status, r.body)
	next(t)
	went = r.json()["messages"].([]any)
	must(t, len(went) == 0, "messages where the plugin cannot say: %v", went)
}

// A person looked for on the services their chat has none of: each answer as it comes, then a first
// message there, which brings the service into the chat; only a person's chat, with a number.
func TestFindAndFirstMessage(t *testing.T) {
	c := newServer(t)
	c.login()
	drain()
	var maria, group, katerina string
	for _, x := range items(c.getJSON("/api/chats")) {
		switch {
		case x["title"] == "Μαρία Ελένη":
			maria = x["id"].(string)
		case x["type"] == "group":
			group = x["id"].(string)
		case strings.HasPrefix(fmt.Sprint(x["title"]), "Katerina") || strings.Contains(fmt.Sprint(x["title"]), "katerina"):
			katerina = x["id"].(string)
		}
	}
	must(t, maria != "" && group != "" && katerina != "", "chats: %q %q %q", maria, group, katerina)
	detail := c.getJSON("/api/chats/" + maria)
	must(t, detail["findable"] == true, "findable: %v", detail["findable"])

	r := c.post("/api/chats/"+maria+"/find", M{})
	must(t, r.status == 200, "find: %d %s", r.status, r.body)
	asked := r.json()["services"].([]any)
	must(t, fmt.Sprint(asked) == "[telegram viber]", "asked (not what the chat has): %v", asked)
	var reach map[string]any
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		reach = c.getJSON("/api/chats/" + maria)["reach"].(map[string]any)
		if reach["telegram"] == "found" && reach["viber"] == "found" {
			break
		}
	}
	must(t, reach["telegram"] == "found" && reach["viber"] == "found", "reach: %v", reach)
	must(t, c.getJSON("/api/chats/"+maria)["findable"] == false, "every service it lacks answered: nothing more to look for")

	r = c.post("/api/chats/"+maria+"/send", M{"text": "write: first", "service": "telegram"})
	must(t, r.status == 200, "send: %d %s", r.status, r.body)
	a := next(t)
	must(t, a.service == "telegram" && a.text == "write: first", "asked: %+v", a)
	must(t, num(r.json()["conversation_id"]) != 0 && len(r.json()["messages"].([]any)) == 1, "went: %s", r.body)
	detail = c.getJSON("/api/chats/" + maria)
	must(t, slices.Contains(detail["services"].([]any), any("telegram")), "services: %v", detail["services"])
	must(t, detail["reach"].(map[string]any)["telegram"] == nil, "reach after: %v", detail["reach"])

	// nowhere found: sent as before, through the conversations the chat has
	r = c.post("/api/chats/"+maria+"/send", M{"text": "x", "service": "messenger"})
	must(t, r.status == 409 && r.code() == "chat.no_sender", "not found: %d %s", r.status, r.body)
	r = c.post("/api/chats/"+group+"/find", M{})
	must(t, r.status == 409 && r.code() == "chat.not_a_person", "group: %d %s", r.status, r.body)

	// someone met only in the group: their chat, empty, to look for them and write first
	nick := fmt.Sprintf("p%d", db.Int(c.s.Store.Read(), "SELECT person_id FROM person_address WHERE address_id = ?", fx.nick))
	detail = c.getJSON("/api/chats/" + nick)
	must(t, len(detail["services"].([]any)) == 0 && detail["findable"] == true, "empty chat: %v", detail)
	must(t, len(items(c.getJSON("/api/chats/"+nick+"/stream"))) == 0, "nothing in it")
	r = c.post("/api/chats/"+nick+"/find", M{})
	must(t, r.status == 200, "find: %d %s", r.status, r.body)
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && c.getJSON("/api/chats/" + nick)["reach"].(map[string]any)["telegram"] != "found"; time.Sleep(20 * time.Millisecond) {
	}
	r = c.post("/api/chats/"+nick+"/send", M{"text": "write: hello Nick", "service": "telegram"})
	must(t, r.status == 200, "first: %d %s", r.status, r.body)
	next(t)
	must(t, slices.Contains(c.getJSON("/api/chats/" + nick)["services"].([]any), any("telegram")), "now a chat")
	must(t, c.do("GET", "/api/chats/p999999", nil, H).status == 404, "no such person")
	r = c.post("/api/chats/"+katerina+"/find", M{})
	must(t, r.status == 409 && r.code() == "chat.no_phone", "email only: %d %s", r.status, r.body)
}

// A message that cannot go now is kept and sent by itself once it can, in the order written; one sent
// again with its id is not sent twice; a refusal is said at once and not kept; a kept one can be
// taken out.
func TestOutbox(t *testing.T) {
	c := newServer(t)
	c.login()
	drain()
	var group string
	for _, x := range items(c.getJSON("/api/chats?kind=group")) {
		group = x["id"].(string)
	}
	send := func(id, text string) resp {
		return c.post("/api/chats/"+group+"/send", M{"text": text, "service": "whatsapp", "client_id": id})
	}
	kept := func() []M { return items(c.getJSON("/api/chats/" + group + "/outbox")) }

	r := send("a", "write: once")
	must(t, r.status == 200, "send: %d %s", r.status, r.body)
	next(t)
	r = send("a", "write: once")
	must(t, r.status == 200 && num(r.json()["conversation_id"]) != 0, "again: %d %s", r.status, r.body)
	select {
	case a := <-askedCh:
		t.Fatalf("sent twice: %+v", a)
	case <-time.After(300 * time.Millisecond):
	}

	busy.Store(true)
	r = send("b", "write: later")
	must(t, r.status == 202 && r.json()["queued"] == true && r.json()["error"] != nil, "kept: %d %s", r.status, r.body)
	r = send("c", "write: after")
	must(t, r.status == 202, "behind it: %d %s", r.status, r.body)
	k := kept()
	must(t, len(k) == 2 && k[0]["id"] == "b" && k[1]["id"] == "c" && k[0]["error"] != nil, "outbox: %v", k)
	r = send("b", "write: later")
	must(t, r.status == 202, "asked again while kept: %d %s", r.status, r.body)

	busy.Store(false)
	r = c.post("/api/outbox/b/retry", M{})
	must(t, r.status == 200, "retry: %d %s", r.status, r.body)
	must(t, next(t).text == "write: later" && next(t).text == "write: after", "in order")
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && len(kept()) > 0; time.Sleep(50 * time.Millisecond) {
	}
	must(t, len(kept()) == 0, "sent: %v", kept())

	r = send("d", "refuse: no")
	must(t, r.status == 409 && len(kept()) == 0, "refused: %d %s %v", r.status, r.body, kept())

	busy.Store(true)
	send("e", "write: never")
	must(t, len(kept()) == 1, "kept: %v", kept())
	r = c.do("DELETE", "/api/outbox/e", nil, H)
	must(t, r.status == 200 && len(kept()) == 0, "discarded: %d %v", r.status, kept())
	busy.Store(false)
}

// A group's members to name with @, a file sent with its caption, who got and read the user's
// messages, and the services told the chat was read: through a plugin that records what it is asked.
func TestMentionsFilesReceiptsAndReadReceipts(t *testing.T) {
	c := newServer(t)
	c.login()
	drain()
	var group string
	for _, x := range items(c.getJSON("/api/chats?kind=group")) {
		group = x["id"].(string)
	}
	detail := c.getJSON("/api/chats/" + group)
	must(t, slices.Contains(detail["mentionable"].([]any), any("whatsapp")) && slices.Contains(detail["fileable"].([]any), any("whatsapp")),
		"mentionable, fileable: %v %v", detail["mentionable"], detail["fileable"])
	members := detail["members"].([]any)
	member := members[0].(map[string]any)
	must(t, num(member["address_id"]) != 0 && slices.Contains(member["services"].([]any), any("whatsapp")), "member: %v", member)

	// the stream: mentions with their tokens, and ticks of the user's messages
	stream := items(c.getJSON("/api/chats/" + group + "/stream?limit=200"))
	var named, mine M
	for _, i := range stream {
		if ms, ok := i["mentions"].([]any); ok && len(ms) > 0 && named == nil {
			named = i
		}
		if i["type"] == "message" && i["outgoing"] == true && i["receipts"] != nil {
			mine = i
		}
	}
	must(t, named != nil, "a mention in the stream")
	tok := named["mentions"].([]any)[0].(map[string]any)
	must(t, strings.HasPrefix(named["text"].(string), tok["token"].(string)) && tok["name"] != nil, "mention: %v", tok)
	must(t, mine != nil, "the user's message with receipts")
	rc := mine["receipts"].(map[string]any)
	must(t, num(rc["to"]) >= num(rc["delivered"]) && num(rc["delivered"]) >= num(rc["read"]) && num(rc["to"]) == 3, "receipts: %v", rc)
	who := items(c.getJSON(fmt.Sprintf("/api/messages/%d/receipts", num(mine["id"]))))
	must(t, int64(len(who)) == num(rc["to"]), "who: %v", who)
	for _, w := range who {
		must(t, w["delivered_at"] != nil, "delivered: %v", w)
	}

	// @ and a file
	name := "@" + member["name"].(string)
	text := "🙂 " + name + " look"
	length := len([]rune(name))
	aid := num(member["address_id"])
	r := c.post("/api/chats/"+group+"/send", M{"text": text, "service": "whatsapp",
		"mentions": []M{{"start": 2, "length": length, "address_id": aid}}})
	must(t, r.status == 200, "send: %d %s", r.status, r.body)
	a := next(t)
	must(t, len(a.mentions) == 1 && a.mentions[0].Start == 2 && a.mentions[0].Length == length && a.mentions[0].AddressID == aid, "mentions: %v", a.mentions)
	r = c.post("/api/chats/"+group+"/send", M{"text": "@x hi", "service": "whatsapp", "mentions": []M{{"start": 0, "length": 2, "address_id": fx.unnamed}}})
	must(t, r.status == 400 && r.code() == "chat.not_a_member", "not a member: %d %s", r.status, r.body)
	for _, bad := range []any{
		[]M{{"start": 3, "length": 2, "address_id": aid}},  // not an "@"
		[]M{{"start": -5, "length": 2, "address_id": aid}}, // outside the text
		[]M{{"start": 0, "length": 9, "address_id": aid}},
		[]M{{"start": 0, "length": 2, "address_id": aid}, {"start": 1, "length": 2, "address_id": aid}},
		[]M{{"start": 0}}, "nonsense"} {
		r := c.post("/api/chats/"+group+"/send", M{"text": "@x hi", "service": "whatsapp", "mentions": bad})
		must(t, r.status == 400, "bad mentions %v: %d", bad, r.status)
	}
	must(t, c.post("/api/chats/"+group+"/send", []string{"not", "an", "object"}).status == 400, "not an object")
	r = c.form("/api/chats/"+group+"/send", map[string]string{"text": "a\r\n@x b", "service": "whatsapp",
		"mentions": fmt.Sprintf(`[{"start": 2, "length": 2, "address_id": %d}]`, aid)}, "file", "a.txt", "text/plain", []byte("t"))
	must(t, r.status == 200, "form: %d %s", r.status, r.body)
	must(t, next(t).text == "a\n@x b", "the form's CRLF as one line break")
	r = c.form("/api/chats/"+group+"/send", map[string]string{"text": "", "service": "whatsapp"}, "file", "photo.jpg", "image/jpeg", []byte("\xff\xd8 picture"))
	must(t, r.status == 200, "a file alone: %d %s", r.status, r.body)
	a = next(t)
	must(t, a.text == "" && a.file != nil && string(a.file.Data) == "\xff\xd8 picture" && a.file.Filename == "photo.jpg" && a.file.MimeType == "image/jpeg",
		"file: %+v", a.file)
	must(t, c.post("/api/chats/"+group+"/send", M{"text": " "}).code() == "empty_message", "empty")

	// read here: the services told, where something of the others is newer than what they said was read
	drain()
	must(t, c.post("/api/chats/"+group+"/read", M{}).status == 200, "read")
	a = next(t)
	must(t, a.what == "read" && a.service == "whatsapp", "read receipts: %+v", a)
}

// People without a name show (a new number that writes is not hidden: it is to be named or merged).
func TestPeopleWithoutANameShown(t *testing.T) {
	c := newServer(t)
	c.login()
	titles := func() []string {
		var out []string
		for _, x := range items(c.getJSON("/api/chats")) {
			out = append(out, x["title"].(string))
		}
		return out
	}
	unnamed := core.PrettyPhone("+15550100010")
	must(t, slices.Contains(titles(), unnamed), "shown: %v", titles())
	must(t, !slices.Contains(titles(), core.PrettyPhone("+15550100000")), "calls only: on the calls' page, not a chat")
}

func TestLabelsAndNamesFound(t *testing.T) {
	c := newServer(t)
	c.login()
	keys := map[string]M{}
	for _, x := range items(c.getJSON("/api/labels")) {
		if k, ok := x["key"].(string); ok {
			keys[k] = x
		}
	}
	_, romantic := keys["romantic"]
	_, sexual := keys["sexual"]
	must(t, romantic && !sexual, "labels: %v", keys)
	r := c.post("/api/labels", M{"kind": "tone", "name": "Acme", "meaning": "work at Acme"})
	acme := num(r.json()["id"])
	must(t, c.post("/api/labels", M{"kind": "tone", "name": "acme"}).code() == "labels.exists", "exists")
	must(t, c.post("/api/labels", M{"kind": "mood", "name": "x"}).code() == "failed", "a kind there is not")
	c.do("PATCH", fmt.Sprintf("/api/labels/%d", acme), M{"name": "Acme SA"}, H)
	found := false
	for _, x := range items(c.getJSON("/api/labels")) {
		found = found || x["name"] == "Acme SA"
	}
	must(t, found, "renamed")
	un := items(c.getJSON("/api/people/unnamed?guessed=true"))
	var k M
	for _, p := range un {
		if g, ok := p["guess"].(map[string]any); ok && g != nil {
			k = p
			break
		}
	}
	must(t, k != nil, "someone with a name found: %v", un)
	g := k["guess"].(map[string]any)
	must(t, g["name"] == "Κατερίνα Οικονόμου" && g["how"] == "handle", "guess: %v", g)
	pid := num(k["id"])
	if err := core.SaveAnalysis(c.s.Store, pid, 24, []string{"m"}, nil, []core.Judged{{LabelID: num(keys["professional"]["id"]), Votes: 1, Of: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	c.do("PUT", fmt.Sprintf("/api/people/%d/labels/%d", pid, acme), M{"state": "yes"}, H)
	labelNames := func() []string {
		var out []string
		for _, x := range c.getJSON(fmt.Sprintf("/api/people/%d", pid))["labels"].([]any) {
			m := x.(map[string]any)
			if n, ok := m["name"].(string); ok {
				out = append(out, n)
			} else {
				out = append(out, m["key"].(string))
			}
		}
		slices.Sort(out)
		return out
	}
	must(t, slices.Equal(labelNames(), []string{"Acme SA"}), "the models' only when shown: %v", labelNames())
	c.do("PUT", "/api/settings", M{"show_tone": true}, H)
	must(t, slices.Equal(labelNames(), []string{"Acme SA", "professional"}), "shown: %v", labelNames())
	got := c.getJSON(fmt.Sprintf("/api/people/%d", pid))
	must(t, num(got["analysed"].(map[string]any)["messages"]) == 24, "analysed: %v", got["analysed"])
	must(t, c.post(fmt.Sprintf("/api/labels/%d/merge", acme), M{"into": keys["professional"]["id"]}).status == 200, "merge labels")
	ls := c.getJSON(fmt.Sprintf("/api/people/%d", pid))["labels"].([]any)
	must(t, len(ls) == 1 && ls[0].(map[string]any)["key"] == "professional" && ls[0].(map[string]any)["state"] == "yes", "merged: %v", ls)
	must(t, c.post(fmt.Sprintf("/api/people/%d/analyse", pid), nil).json()["analysed"] == nil, "analyse again")
	p := c.post(fmt.Sprintf("/api/people/%d/guess", pid), M{"how": "handle", "accept": true}).json()
	must(t, p["name"] == "Κατερίνα Οικονόμου" && p["guess"] == nil, "accepted: %v", p)
	must(t, c.post(fmt.Sprintf("/api/people/%d/guess", pid), M{"how": "handle", "accept": true}).code() == "people.no_guess", "no guess now")
	must(t, c.do("DELETE", fmt.Sprintf("/api/labels/%d", num(keys["formal"]["id"])), nil, H).status == 200, "remove")
	for _, x := range items(c.getJSON("/api/labels")) {
		must(t, x["key"] != "formal", "removed")
	}
}

func TestPeopleByLabel(t *testing.T) {
	c := newServer(t)
	c.login()
	var friend int64
	for _, x := range items(c.getJSON("/api/labels")) {
		if x["key"] == "friend" {
			friend = num(x["id"])
		}
	}
	people := items(c.getJSON("/api/people"))
	first, second := num(people[0]["id"]), num(people[1]["id"])
	core.SetPersonLabel(c.s.Store, first, friend, "yes")
	core.SaveAnalysis(c.s.Store, second, 30, []string{"m"}, nil, nil, &core.Judged{LabelID: friend, Votes: 1, Of: 1})
	byID := map[int64]M{}
	for _, p := range items(c.getJSON("/api/people")) {
		byID[num(p["id"])] = p
	}
	must(t, len(byID[first]["labels"].([]any)) == 1, "the user's: %v", byID[first]["labels"])
	must(t, len(byID[second]["labels"].([]any)) == 0, "the models' only when shown")
	ids := func() []int64 {
		var out []int64
		for _, p := range items(c.getJSON(fmt.Sprintf("/api/people?label=%d", friend))) {
			out = append(out, num(p["id"]))
		}
		slices.Sort(out)
		return out
	}
	must(t, slices.Equal(ids(), []int64{first}), "by label: %v", ids())
	c.do("PUT", "/api/settings", M{"show_tone": true}, H)
	want := []int64{first, second}
	slices.Sort(want)
	must(t, slices.Equal(ids(), want), "with the models': %v", ids())
}

func TestOnePersonAnalysedNow(t *testing.T) {
	c := newServer(t)
	c.login()
	var pid int64
	for _, p := range items(c.getJSON("/api/people/unnamed")) {
		for _, h := range p["handles"].([]any) {
			if h.(map[string]any)["value"] == "katerina.oikonomou@example.com" {
				pid = num(p["id"])
			}
		}
	}
	must(t, pid != 0, "Κατερίνα")
	must(t, c.post(fmt.Sprintf("/api/people/%d/analyse/now", pid), nil).code() == "analysis.none", "no analysis yet")
	r := c.post("/api/plugins", M{"plugin": "test-analysis", "label": "Local", "settings": M{"models": "m"}})
	must(t, r.status == 200, "add: %s", r.body)
	must(t, c.getJSON("/api/analysis")["instance"] != nil, "an analysis")
	must(t, c.post(fmt.Sprintf("/api/people/%d/analyse/now", pid), nil).status == 200, "now")
	select {
	case got := <-analysed:
		must(t, got == pid, "read %d", got)
	case <-time.After(5 * time.Second):
		t.Fatal("not read")
	}
	// the run ends: its status and log, its log file
	iid := num(r.json()["id"])
	var st M
	for range 50 {
		if st = c.getJSON(fmt.Sprintf("/api/plugins/%d", iid)); st["running"] == nil && st["last_status"] == "ok" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	must(t, st["last_status"] == "ok" && len(st["log"].([]any)) == 2, "status: %v", st)
	logs := items(c.getJSON(fmt.Sprintf("/api/plugins/%d/logs", iid)))
	must(t, len(logs) == 1 && strings.HasSuffix(logs[0]["name"].(string), "-person-"+fmt.Sprint(pid)+".log"), "logs: %v", logs)
	f := c.get(fmt.Sprintf("/api/plugins/%d/logs/%s", iid, logs[0]["name"]))
	must(t, f.status == 200 && strings.Contains(string(f.body), "reading"), "log file: %d %s", f.status, f.body)
}

func TestAccountsInSettings(t *testing.T) {
	c := newServer(t)
	c.login()
	byID := map[string]M{}
	for _, s := range items(c.getJSON("/api/services/used")) {
		byID[s["id"].(string)] = s
	}
	wa, ok := byID["whatsapp"]
	must(t, ok && wa["accounts"] != nil, "whatsapp: %v", byID)
	r := c.do("PUT", "/api/settings", M{"hidden_accounts": []int{1, 2}}, H).json()
	must(t, fmt.Sprint(r["hidden_accounts"]) == "[1 2]", "hidden: %v", r["hidden_accounts"])
	r = c.do("PUT", "/api/settings", M{"hidden_accounts": []string{"x"}}, H).json()
	must(t, fmt.Sprint(r["hidden_accounts"]) == "[1 2]", "not ids: kept as it was: %v", r["hidden_accounts"])
	raw := db.Str(c.s.Store.Read(), "SELECT value FROM setting WHERE key = 'hidden_accounts'")
	must(t, raw == "[1, 2]", "stored as the Python stores it: %s", raw)
}

func TestAPersonWithTheirLatestMessages(t *testing.T) {
	c := newServer(t)
	c.login()
	pid := num(items(c.getJSON("/api/people"))[0]["id"])
	_, has := c.getJSON(fmt.Sprintf("/api/people/%d", pid))["recent"]
	must(t, !has, "not unless asked")
	var found bool
	for _, p := range items(c.getJSON("/api/people")) {
		got, _ := c.getJSON(fmt.Sprintf("/api/people/%d?recent=3", num(p["id"])))["recent"].([]any)
		if len(got) > 0 {
			must(t, len(got) <= 3, "at most 3")
			m := got[0].(map[string]any)
			_, a := m["ts"]
			_, b := m["outgoing"]
			_, c := m["text"]
			must(t, a && b && c, "recent: %v", m)
			found = true
			break
		}
	}
	must(t, found, "someone with messages")
}

func TestSPA(t *testing.T) {
	c := newServer(t)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "assets"), 0o700)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>app</html>"), 0o600)
	os.WriteFile(filepath.Join(dir, "assets", "a.js"), []byte("1"), 0o600)
	c.s.web = os.DirFS(dir)
	r := c.get("/chats/p1")
	must(t, r.status == 200 && string(r.body) == "<html>app</html>" && r.header.Get("Cache-Control") == "no-cache", "fallback: %d", r.status)
	r = c.get("/assets/a.js")
	must(t, r.status == 200 && strings.Contains(r.header.Get("Cache-Control"), "immutable") &&
		strings.HasPrefix(r.header.Get("Content-Type"), "text/javascript"), "asset: %v", r.header)
	must(t, c.get("/../../etc/passwd").status == 200, "outside the build: the app")
	c.s.web = nil
	r = c.get("/")
	must(t, r.status == 503 && strings.Contains(string(r.body), "go generate"), "not built: %d", r.status)
}

// A plugin is told when the user ends its live connection, not when the server stops.
func TestLiveStoppedByTheUserIsTold(t *testing.T) {
	c := newServer(t)
	c.login()
	iid := num(c.post("/api/plugins", M{"plugin": "test-live", "label": "Live"}).json()["id"])
	connected := func() {
		t.Helper()
		select {
		case got := <-liveRuns:
			must(t, got == iid, "connected")
		case <-time.After(5 * time.Second):
			t.Fatal("not connected")
		}
	}
	must(t, c.post(fmt.Sprintf("/api/plugins/%d/live", iid), M{"on": true}).status == 200, "on")
	connected()
	must(t, c.post(fmt.Sprintf("/api/plugins/%d/live", iid), M{"on": false}).status == 200, "off")
	select {
	case got := <-liveStopped:
		must(t, got == iid, "told")
	case <-time.After(5 * time.Second):
		t.Fatal("not told")
	}
	must(t, c.post(fmt.Sprintf("/api/plugins/%d/live", iid), M{"on": true}).status == 200, "on again")
	connected()
	c.s.Host.Wait(5 * time.Second) // the server stopping
	select {
	case <-liveStopped:
		t.Fatal("told when the server stopped")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestLiveConnectionsAndRuns(t *testing.T) {
	c := newServer(t)
	c.login()
	iid := num(c.post("/api/plugins", M{"plugin": "test-live", "label": "Live"}).json()["id"])
	st := c.post(fmt.Sprintf("/api/plugins/%d/live", iid), M{"on": true}).json()
	must(t, st["live"] == true && st["live_capable"] == true, "live: %v", st)
	select {
	case got := <-liveRuns:
		must(t, got == iid, "connected")
	case <-time.After(5 * time.Second):
		t.Fatal("not connected")
	}
	must(t, strings.Contains(db.Str(c.s.Store.Read(), "SELECT settings FROM plugin_instance WHERE id = ?", iid), `"_live":true`), "remembered")
	// a failure: connected again after a pause, and said in its log
	liveFailing <- fmt.Errorf("the network went")
	var st2 M
	for range 50 {
		st2 = c.getJSON(fmt.Sprintf("/api/plugins/%d", iid))
		if len(st2["log"].([]any)) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	must(t, strings.Contains(fmt.Sprint(st2["log"]), "the network went") && strings.Contains(fmt.Sprint(st2["log"]), "5"), "log: %v", st2["log"])
	st = c.post(fmt.Sprintf("/api/plugins/%d/live", iid), M{"on": false}).json()
	must(t, st["live"] == false, "stopped: %v", st)
	must(t, strings.Contains(db.Str(c.s.Store.Read(), "SELECT settings FROM plugin_instance WHERE id = ?", iid), `"_live":false`), "off, remembered")
	must(t, c.post(fmt.Sprintf("/api/plugins/%d/live", num(items(c.getJSON("/api/plugins"))[0]["id"])), M{"on": true}).code() == "host.no_live", "a source without a live connection")

	// one run at a time
	must(t, c.post(fmt.Sprintf("/api/plugins/%d/run", iid), nil).status == 200, "run")
	r := c.post(fmt.Sprintf("/api/plugins/%d/run", iid), nil)
	must(t, r.status == 409 && r.code() == "host.running", "again while running: %d %s", r.status, r.body)
	must(t, c.getJSON(fmt.Sprintf("/api/plugins/%d", iid))["running"] == "import", "running")
	importGoOn <- struct{}{}
	for range 100 {
		if st = c.getJSON(fmt.Sprintf("/api/plugins/%d", iid)); st["running"] == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	must(t, st["running"] == nil && st["last_status"] == "ok", "done: %v", st)
	must(t, c.do("DELETE", fmt.Sprintf("/api/plugins/%d", iid), nil, H).status == 200, "removed")
	must(t, c.get(fmt.Sprintf("/api/plugins/%d", iid)).status == 404, "gone")
}

func TestTheSameLabelTwiceIsRefused(t *testing.T) {
	c := newServer(t)
	c.login()
	must(t, c.post("/api/plugins", M{"plugin": "test-live", "label": "Twice"}).status == 200, "first")
	r := c.post("/api/plugins", M{"plugin": "test-live", "label": "Twice"})
	must(t, r.status == 409 && r.code() == "failed", "second: %d %s", r.status, r.body)
	must(t, c.post("/api/plugins", M{"plugin": "nothing"}).code() == "unknown_plugin", "unknown plugin")
}

func TestMCPOverHTTP(t *testing.T) {
	c := newServer(t)
	uid := c.login()
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	hd := map[string]string{"Accept": "application/json, text/event-stream"}
	r := c.do("POST", "/mcp", call, hd)
	must(t, r.status == 401 && strings.HasPrefix(r.header.Get("WWW-Authenticate"), "Bearer"), "no token: %d", r.status)
	hd["Authorization"] = "Bearer " + c.s.Auth.NewMCPToken(uid, "test")
	r = c.do("POST", "/mcp", call, hd)
	must(t, r.status == 200 && strings.Contains(string(r.body), "search"), "tools: %d %s", r.status, r.body)
	hd["Authorization"] = "Bearer chk_wrong"
	must(t, c.do("POST", "/mcp", call, hd).status == 401, "a wrong token")
}

func TestDemoIncoming(t *testing.T) {
	os.Setenv("EVERYSAID_DEMO", "1")
	c := newServer(t)
	os.Unsetenv("EVERYSAID_DEMO")
	c.login()
	ws, _, err := dialEvents(c)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ev M
	wsjson.Read(ctx, ws, &ev) // hello
	var group string
	for _, x := range items(c.getJSON("/api/chats?kind=group")) {
		group = x["id"].(string)
	}
	r := c.post("/api/demo/incoming", M{"chat": group, "text": "from the other side", "service": "whatsapp"})
	must(t, r.status == 200 && num(r.json()["id"]) > 0, "incoming: %d %s", r.status, r.body)
	if err := wsjson.Read(ctx, ws, &ev); err != nil || ev["type"] != "new" || ev["chats"].(map[string]any)[group] == nil {
		t.Fatalf("new: %v %v", ev, err)
	}
	must(t, c.post("/api/demo/incoming", M{"chat": "nope"}).status == 404, "no chat")
}

// What breaks in a goroutine of the host (outside a request) is logged; the server goes on.
func TestABrokenLiveConnectionDoesNotEndTheServer(t *testing.T) {
	c := newServer(t)
	c.login()
	iid := num(c.post("/api/plugins", M{"plugin": "test-broken", "label": "Broken"}).json()["id"])
	must(t, c.post(fmt.Sprintf("/api/plugins/%d/live", iid), M{"on": true}).status == 200, "on")
	c.s.Store.MustWrite(func(tx *sql.Tx) {
		db.Exec(tx, `UPDATE plugin_instance SET settings = '{"broken": true, "_live": true}' WHERE id = ?`, iid)
	})
	c.s.Host.StopLive(iid, false)
	for len(brokenChecks) > 0 {
		<-brokenChecks
	}
	c.s.Host.StartLive(iid) // its check now breaks inside the connection's goroutine
	select {
	case <-brokenChecks:
	case <-time.After(5 * time.Second):
		t.Fatal("not checked")
	}
	time.Sleep(100 * time.Millisecond)
	must(t, c.get("/api/health").status == 200, "the server goes on")
}
