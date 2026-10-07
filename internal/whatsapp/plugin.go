package whatsapp

// Ports the plugin WhatsappBridge of everysaid/plugins/sources.py. What it asked the bridge's REST
// API it now asks the Bridge running in this process (Running); what it read of the bridge's
// databases it still reads there, read only.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

type M = plugins.M

// Plugin is the `whatsapp-bridge` source.
type Plugin struct{}

func init() { plugins.Register(Plugin{}) }

func defaultStore() string {
	if config.WhatsappBridge != "" {
		return config.WhatsappBridge
	}
	return filepath.Join(config.Data, "whatsapp-bridge")
}

func (Plugin) Info() *plugins.Info {
	return &plugins.Info{
		ID: "whatsapp-bridge", Name: "WhatsApp (live bridge)", Kind: "source",
		Services:    []string{"whatsapp"},
		ServiceInfo: sourcekit.Looks("whatsapp"),
		NameWeights: []plugins.Weight{{Key: "whatsapp/book", Weight: 80}, {Key: "whatsapp/chat", Weight: 50},
			{Key: "whatsapp/profile", Weight: 30}},
		StateWeights: map[string]int{"muted": 60, "pinned": 0},
		Description: "WhatsApp as it arrives, through a whatsmeow client inside Everysaid, linked as a device " +
			"(like WhatsApp Web). Unofficial: WhatsApp may block accounts that use one; sending raises that risk.",
		Modes:       []string{"import", "live"},
		LiveDefault: true,
		Needs:       []string{"a link from the phone (a QR code)", "the bridge's store folder"},
		// The bridge's REST API (setting `api`) is gone: a value stored for it stays, unused but for
		// telling whether the standalone bridge is still running (Live).
		Settings: []plugins.Setting{
			{Key: "store", Label: "The bridge's store folder", Type: "path", Required: true, Default: defaultStore()},
			{Key: "send", Label: "Sending messages", Type: "bool", Default: false,
				Help: "A risk for the account; needs [whatsapp] send = true in config.toml. Turned off by itself " +
					"when WhatsApp warns the account"},
			{Key: "read_receipts", Label: "Send read receipts", Type: "bool", Default: false,
				Help: "When a chat is opened here, the others see it read, and it is read on the phone too"},
			{Key: "interval", Label: "Check every (seconds)", Type: "number", Default: 10},
		},
		CanSend: true, CanReply: true, CanMention: true, CanMarkRead: true, CanSendFiles: true,
		Actions: []plugins.Action{{ID: "link", Label: "Link a device (QR code)"},
			{ID: "unblock", Label: "Allow sending again"}},
	}
}

func storeDir(c *plugins.Context) string { return c.Str("store") }

func paths(c *plugins.Context) (string, string) {
	d := storeDir(c)
	return filepath.Join(d, "messages.db"), filepath.Join(d, "whatsapp.db")
}

// bridgeState is what the bridge last recorded about its connection (bridge_state: connection,
// send_enabled, send_blocked, ban_until); empty for a store from before that table.
func bridgeState(c *plugins.Context) (out map[string]string) {
	out = map[string]string{}
	path, _ := paths(c)
	if _, err := os.Stat(path); err != nil {
		return out
	}
	d, err := sql.Open("sqlite", readOnly(path))
	if err != nil {
		return out
	}
	defer d.Close()
	rows, err := d.Query("SELECT key, value FROM bridge_state")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v sql.NullString
		if rows.Scan(&k, &v) == nil {
			out[k] = v.String
		}
	}
	return out
}

// offInConfig: sending not allowed by config.toml, what the bridge's -send was.
const offInConfig = "off in config.toml ([whatsapp] send)"

func (p Plugin) NotSending(c *plugins.Context) string {
	if blocked := bridgeState(c)["send_blocked"]; blocked != "" {
		return "blocked by the bridge: " + blocked
	}
	if !ConfigOptions().Send {
		return offInConfig
	}
	if !c.Bool("send") {
		return "off in this source's settings"
	}
	if ok, why := plugins.CheckSettings(p, c); !ok {
		return why
	}
	return ""
}

func (p Plugin) Check(c *plugins.Context) (bool, string) {
	ok, why := plugins.CheckSettings(p, c)
	if blocked := bridgeState(c)["send_blocked"]; ok && blocked != "" {
		return ok, "sending blocked by the bridge: " + blocked
	}
	return ok, why
}

// connections are the bridge's connection states, as said on the card.
var connections = map[string]string{"connected": "connected to WhatsApp", "disconnected": "not connected to WhatsApp",
	"logged_out": "logged out of WhatsApp", "temp_banned": "temporarily banned by WhatsApp",
	"replaced": "another client took the connection", "outdated": "WhatsApp rejected the bridge's version",
	"failed": "the connection to WhatsApp failed"}

// cardStatus is the running bridge's word, or what the store last recorded when it does not run.
func cardStatus(c *plugins.Context) map[string]any {
	if b := Running(storeDir(c)); b != nil {
		if s := b.Status(); s != nil {
			return s
		}
	}
	st := bridgeState(c)
	opts := ConfigOptions()
	sent := map[string]int{}
	path, _ := paths(c)
	if d, err := sql.Open("sqlite", readOnly(path)); err == nil {
		for word, since := range map[string]time.Duration{"minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour} {
			var n int
			d.QueryRow("SELECT count(*) FROM sent WHERE at > ?", time.Now().Add(-since).Unix()).Scan(&n)
			sent[word] = n
		}
		d.Close()
	}
	return map[string]any{"connected": false, "connection": st["connection"], "send_blocked": st["send_blocked"],
		"send_enabled": opts.Send, "limits": opts.Limits, "sent": sent, "linked": HasDevice(storeDir(c))}
}

func (p Plugin) InfoFacts(c *plugins.Context) []plugins.Fact {
	s := cardStatus(c)
	str := func(k string) string { v, _ := s[k].(string); return v }
	var sending string
	switch {
	case str("send_blocked") != "":
		sending = "blocked by the bridge: " + str("send_blocked")
	case s["send_enabled"] != true:
		sending = offInConfig
	case !c.Bool("send"):
		sending = "off in this source's settings"
	default:
		day := 0
		if sent, ok := s["sent"].(map[string]int); ok {
			day = sent["day"]
		}
		limits, _ := s["limits"].(SendLimits)
		sending = i18n.T("on, {day} of {limit} today", c.Lang(), map[string]any{"day": day, "limit": limits.PerDay})
	}
	connection := str("connection") // why, when it is not connected
	if connection == "" {
		connection = "disconnected"
	}
	if s["connected"] == true {
		connection = "connected"
	} else if connection == "connected" {
		connection = "disconnected" // its last record, but not so now
	}
	said, ok := connections[connection]
	if !ok {
		said = connection
	}
	if s["linked"] == false {
		said = "not linked yet (Link a device)"
	}
	// sending's own key and its limits are config.toml's ([whatsapp]), what the bridge's flags were
	limits, _ := s["limits"].(SendLimits)
	set := i18n.T("{minute} a minute, {hour} an hour, {day} a day; the same text into {same} chats an hour (config.toml, [whatsapp])",
		c.Lang(), map[string]any{"minute": limits.PerMinute, "hour": limits.PerHour, "day": limits.PerDay, "same": limits.SameText})
	return []plugins.Fact{{Label: "Connection", Value: said}, {Label: "Sending", Value: sending},
		{Label: "Sending limits", Value: set}}
}

// watchState: once the bridge blocks sending (WhatsApp warned the account), this instance's sending
// goes off too, said in its log and to the user's devices. Turning it on again is the user's.
func watchState(c *plugins.Context) {
	blocked := bridgeState(c)["send_blocked"]
	was, _ := c.State["send_blocked"].(string)
	if blocked == was {
		return
	}
	c.SaveState(M{"send_blocked": blocked})
	if blocked == "" {
		return
	}
	c.Log("WhatsApp warned the account, sending is off: {why}", map[string]any{"why": blocked})
	if c.Bool("send") {
		plugins.Update(c.Store(), c.ID, nil, M{"send": false}, nil, false)
		c.Settings["send"] = false
	}
	c.Host().Alert(i18n.Tr("WhatsApp warned the account", c.Lang()), blocked)
	c.Emit(M{"type": "changed"})
}

func (p Plugin) RunImport(c *plugins.Context) error { return runImport(c) }

// standaloneAnswers says whether the standalone bridge answers on its REST API: it may be running
// on this store, and a second connection with the same device would take its session over. Only
// its own answer counts (its status, as JSON): anything else on that port (another program) does
// not keep the connection from starting.
func standaloneAnswers(c *plugins.Context) bool {
	api := strings.TrimRight(c.Str("api"), "/")
	if api == "" {
		api = "http://127.0.0.1:8080"
	}
	cl := http.Client{Timeout: 1500 * time.Millisecond}
	r, err := cl.Get(api + "/api/status")
	if err != nil {
		return false
	}
	defer r.Body.Close()
	var status map[string]any
	if r.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&status) != nil {
		return false
	}
	_, connected := status["connected"]
	_, blocked := status["send_blocked"]
	return connected && blocked
}

// Live runs the connection, and imports what it keeps (messages.db) and the chats' state, archived,
// pinned, muted (whatsapp.db) as they change.
func (p Plugin) Live(ctx context.Context, c *plugins.Context) error {
	if standaloneAnswers(c) {
		return errs.Plugin("The standalone WhatsApp bridge is running: stop it first (both on one device would end its session)", 409)
	}
	b := New(storeDir(c), ConfigOptions(), Hooks{
		QR: func(code, drawn string) {
			c.Log("Scan this QR code in WhatsApp on the phone (Linked devices):", nil)
			c.Logf("%s", drawn)
			c.Emit(M{"type": "plugin_qr", "instance": c.ID, "code": code})
		},
		Event: func(event, code, detail string) {
			c.Logf("%s", strings.TrimSpace("[bridge] "+event+" "+code+" "+detail))
		},
		Warn: func(text string) { c.Logf("%s", text) },
	})
	if !HasDevice(storeDir(c)) {
		c.Log("Not linked to WhatsApp yet: use “Link a device” and scan the QR code with the phone", nil)
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	messages, devices := paths(c)
	ps := []string{messages, messages + "-wal", devices, devices + "-wal"}
	var last int64
	for {
		var m int64
		for _, f := range ps {
			if st, err := os.Stat(f); err == nil && st.ModTime().UnixNano() > m {
				m = st.ModTime().UnixNano()
			}
		}
		if m != 0 && m != last {
			if last != 0 {
				if err := runImport(c); err != nil {
					c.Log("error: {e}", map[string]any{"e": err})
				}
			}
			watchState(c)
			last = m
		}
		wait := int(c.Num("interval"))
		if wait == 0 {
			wait = 10
		}
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			<-done
			return nil
		case <-time.After(time.Duration(max(2, wait)) * time.Second):
		}
	}
}

// Action: "link" shows QR codes for the phone to link this device (through the live connection);
// "unblock" lets sending go again after WhatsApp's warning (a person's decision).
func (p Plugin) Action(c *plugins.Context, name string) error {
	switch name {
	case "link":
		b := Running(storeDir(c))
		if b == nil {
			return errs.Plugin("Turn the live connection on first: linking goes through it", 0)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		switch err := b.Pair(ctx); err {
		case nil:
			c.Log("Linked to WhatsApp", nil)
			return nil
		case ErrLinked:
			return errs.Plugin("Already linked to WhatsApp", 0)
		case ErrPairTimeout:
			return errs.Plugin("No QR code was scanned in time", 0)
		default:
			return err
		}
	case "unblock":
		old, err := Unblock(storeDir(c))
		if err != nil {
			return err
		}
		c.Log("Sending allowed again at the bridge (blocked for: {why})", map[string]any{"why": old})
		watchState(c)
		return nil
	}
	return errs.Plugin("unknown action", 0)
}

func (p Plugin) IdleActions(c *plugins.Context) []string {
	var idle []string
	b := Running(storeDir(c))
	if HasDevice(storeDir(c)) || (b != nil && b.Linked()) {
		idle = append(idle, "link")
	}
	if bridgeState(c)["send_blocked"] == "" {
		idle = append(idle, "unblock")
	}
	return idle
}

// mentions is the text with each mention (where it is in the text, in characters, as the user saw
// it, e.g. "@name") written as WhatsApp has it, @<number or LID>, and whom it names, as the
// bridge takes them.
func mentions(c *plugins.Context, text string, ms []plugins.Mention) (string, []string, error) {
	sorted := append([]plugins.Mention(nil), ms...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start > sorted[j].Start })
	runes := []rune(text)
	var who []string
	for _, m := range sorted {
		var value string
		if !db.Row(c.Store().Read(), "SELECT value FROM address WHERE id = ?", []any{m.AddressID}, &value) {
			return "", nil, errs.Plugin("Unknown person to mention", 0)
		}
		value = strings.TrimPrefix(value, "+") // +E.164, or a LID's jid
		user, _, _ := strings.Cut(value, "@")
		start, end := clamp(m.Start, len(runes)), clamp(m.Start+m.Length, len(runes))
		runes = append(append(append([]rune{}, runes[:start]...), []rune("@"+user)...), runes[max(start, end):]...)
		who = append(who, value)
	}
	// each once: the bridge rewrites all of its places
	var out []string
	seen := map[string]bool{}
	for i := len(who) - 1; i >= 0; i-- {
		if !seen[who[i]] {
			seen[who[i]] = true
			out = append(out, who[i])
		}
	}
	return string(runes), out, nil
}

// clamp is a Python slice index: within 0..n.
func clamp(i, n int) int {
	if i < 0 {
		i += n
		if i < 0 {
			return 0
		}
	}
	return min(i, n)
}

// recipient is a chat's key as the bridge takes it: a number's digits, or a jid.
func recipient(key string) string {
	if strings.HasPrefix(key, "+") {
		return strings.TrimLeft(key, "+")
	}
	return key
}

// MarkRead sends read receipts for the chat's messages up to `until` (Unix ms), where the user
// turned them on; nothing otherwise. It returns how many messages were marked.
func (p Plugin) MarkRead(ctx context.Context, c *plugins.Context, conv plugins.Conversation, until int64) (int, error) {
	if !c.Bool("read_receipts") {
		return 0, nil
	}
	b := Running(storeDir(c))
	if b == nil {
		return 0, nil // not connected: nothing marked (the bridge answered so)
	}
	code, answer := b.MarkRead(ReadRequest{Recipient: strings.TrimPrefix(conv.Key, "+"), Until: floorDiv(until, 1000)})
	if code != http.StatusOK {
		return 0, nil // a chat the bridge does not know (nothing of it to mark)
	}
	n, _ := answer["marked"].(int)
	return n, nil
}

// floorDiv is Python's //.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// request is what the plugin asks the bridge to send.
func request(c *plugins.Context, conv plugins.Conversation, text string, reply *plugins.Reply, ms []plugins.Mention, file *plugins.File) (SendRequest, error) {
	req := SendRequest{Recipient: recipient(conv.Key), Message: text}
	if len(ms) > 0 {
		t, who, err := mentions(c, text, ms)
		if err != nil {
			return req, err
		}
		req.Message, req.Mentions = t, who
	}
	if file != nil {
		req.Media, req.Filename, req.MimeType = file.Data, file.Filename, file.MimeType
	}
	if reply != nil {
		// the bridge quotes from its own copy; one from before it was linked, from the archive's
		req.ReplyTo = reply.Key
		var outgoing sql.NullInt64
		var sender, txt sql.NullString
		if db.Row(c.Store().Read(), "SELECT m.outgoing, a.value, m.text FROM message m "+
			"LEFT JOIN address a ON a.id = m.sender_id WHERE m.id = ?", []any{reply.ID}, &outgoing, &sender, &txt) {
			if outgoing.Int64 != 0 {
				req.ReplySender = "me"
			} else {
				req.ReplySender = sender.String
			}
			req.ReplyText = txt.String
		}
	}
	return req, nil
}

func (p Plugin) Send(ctx context.Context, c *plugins.Context, conv plugins.Conversation, text string, reply *plugins.Reply, ms []plugins.Mention, file *plugins.File) (any, error) {
	if !c.Bool("send") {
		return nil, errs.Plugin("Sending is off in this source's settings", 0)
	}
	req, err := request(c, conv, text, reply, ms, file)
	if err != nil {
		return nil, err
	}
	b := Running(storeDir(c))
	if b == nil {
		return nil, errs.Plugin("not connected to WhatsApp", 503)
	}
	code, answer := b.Send(req)
	if code != http.StatusOK { // its refusals: off, blocked, a limit, not a chat they wrote in
		watchState(c)
		if answer.Message == "" {
			return nil, errs.Plugin("Sending failed", 0)
		}
		return nil, errs.Plugin(answer.Message, 0)
	}
	if err := runImport(c); err != nil { // the bridge stores what it sent
		c.Log("error: {e}", map[string]any{"e": err})
	}
	var out M
	raw, _ := json.Marshal(answer)
	json.Unmarshal(raw, &out)
	return out, nil
}
