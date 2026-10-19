package whatsapp

// Ports the plugin WhatsappBridge of everysaid/plugins/sources.py. What it asked the bridge's REST
// API it now asks the Bridge running in this process (Running); what the bridge keeps of its state
// it reads in keys.db.

import (
	"context"
	"database/sql"
	"errors"
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
	"everysaid/internal/importers"
	"everysaid/internal/keys"
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
		Settings: []plugins.Setting{
			{Key: "store", Label: "The bridge's store folder", Type: "path", Required: true, Default: defaultStore()},
			{Key: "send", Label: "Sending messages", Type: "bool", Default: false,
				Help: "A risk for the account; needs [whatsapp] send = true in config.toml. Turned off by itself " +
					"when WhatsApp warns the account"},
			{Key: "read_receipts", Label: "Send read receipts", Type: "bool", Default: false,
				Help: "When a chat is opened here, the others see it read (it is marked read on the phone in any case)"},
			{Key: "interval", Label: "Check every (seconds)", Type: "number", Default: 10},
			{Key: "send_per_minute", Label: "Messages sent at most a minute", Type: "number", Default: 15,
				Help: "Bulk sending is what WhatsApp blocks accounts for; 0 in any of these, no limit"},
			{Key: "send_per_hour", Label: "Messages sent at most an hour", Type: "number", Default: 300},
			{Key: "send_per_day", Label: "Messages sent at most a day", Type: "number", Default: 1000},
			{Key: "send_same_text", Label: "The same longer text into at most so many chats an hour", Type: "number", Default: 3},
			{Key: "send_new_chats", Label: "New chats started from here at most a day", Type: "number", Default: 5,
				Help: "A first message to someone found on WhatsApp, only to people in your contacts or in a group with you"},
		},
		CanSend: true, CanReply: true, CanMention: true, CanMarkRead: true, CanSendFiles: true,
		// any emoji, the app's six quick ones first
		CanReact: true, Reactions: []string{"👍", "❤️", "😂", "😮", "😢", "🙏"}, FreeReactions: true,
		CanEdit: true, CanDelete: true, CanDeleteForMe: true, EditWindow: editWindow, DeleteWindow: deleteWindow, CanReportSpam: true,
		Actions: []plugins.Action{{ID: "link", Label: "Link a device (QR code)"},
			{ID: "unblock", Label: "Allow sending again"}},
	}
}

func storeDir(c *plugins.Context) string { return c.Str("store") }

// limits are the limits of sending, as the source's settings say.
func limits(c *plugins.Context) SendLimits {
	return SendLimits{PerMinute: int(c.Num("send_per_minute")), PerHour: int(c.Num("send_per_hour")),
		PerDay: int(c.Num("send_per_day")), SameText: int(c.Num("send_same_text")), NewChats: int(c.Num("send_new_chats"))}
}

// keysDB is keys.db where the bridge's tables are there (nil before it ever ran, or where keys.db
// cannot be opened).
func keysDB() *sql.DB {
	d, err := keys.Open()
	if err != nil || !db.Exists(d, "SELECT 1 FROM sqlite_master WHERE name = 'whatsapp_state'") {
		return nil
	}
	return d
}

// bridgeState is what the bridge last recorded about its connection (whatsapp_state: connection,
// send_enabled, send_blocked, ban_until); empty before it ever ran.
func bridgeState(c *plugins.Context) (out map[string]string) {
	out = map[string]string{}
	d := keysDB()
	if d == nil {
		return out
	}
	rows, err := d.Query("SELECT key, value FROM whatsapp_state WHERE store = ?", key(storeDir(c)))
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
			s["limits"] = limits(c) // as set now: the bridge takes them at the next message
			return s
		}
	}
	st := bridgeState(c)
	opts := ConfigOptions()
	sent := map[string]int{}
	if d := keysDB(); d != nil {
		for word, since := range map[string]time.Duration{"minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour} {
			var n int
			d.QueryRow("SELECT count(*) FROM whatsapp_sent WHERE store = ? AND at > ?", key(storeDir(c)),
				time.Now().Add(-since).Unix()).Scan(&n)
			sent[word] = n
		}
	}
	return map[string]any{"connected": false, "connection": st["connection"], "send_blocked": st["send_blocked"],
		"send_enabled": opts.Send, "limits": limits(c), "sent": sent, "linked": HasDevice(storeDir(c))}
}

func (Plugin) Connected(c *plugins.Context) bool {
	b := Running(storeDir(c))
	if b == nil {
		return false
	}
	s := b.Status()
	return s != nil && s["connected"] == true
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
	limits, _ := s["limits"].(SendLimits)
	set := i18n.T("{minute} a minute, {hour} an hour, {day} a day; the same text into {same} chats an hour; {new} new chats a day",
		c.Lang(), map[string]any{"minute": limits.PerMinute, "hour": limits.PerHour, "day": limits.PerDay, "same": limits.SameText,
			"new": limits.NewChats})
	return []plugins.Fact{{Label: "Connection", Value: said}, {Label: "Sending", Value: sending},
		{Label: "Sending limits", Value: set}}
}

// watchState: once the bridge blocks sending (WhatsApp warned the account), this instance's sending
// goes off too, said in its log and to the user's devices. Turning it on again is the user's ("unblock").
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

// Live runs the connection, which writes what arrives into the archive as it comes, and imports what
// it keeps of chats and people (keys.db: the chats' state, archived, pinned, muted, the names, the
// groups' members) as it changes.
func (p Plugin) Live(ctx context.Context, c *plugins.Context) error {
	opts := ConfigOptions()
	opts.Limits = limits(c)
	b := New(storeDir(c), opts, Hooks{
		// on the bridge's goroutines: a log that fails is not the server's end
		QR: func(code, drawn string) {
			c.Safe("QR", func() {
				c.Log("Scan this QR code in WhatsApp on the phone (Linked devices):", nil)
				c.Logf("%s", drawn)
				c.Emit(M{"type": "plugin_qr", "instance": c.ID, "code": code})
			})
		},
		Event: func(event, code, detail string) {
			c.Safe("event", func() { c.Logf("%s", strings.TrimSpace("[bridge] "+event+" "+code+" "+detail)) })
		},
		Warn:  func(text string) { c.Safe("warning", func() { c.Logf("%s", text) }) },
		Write: writeTo(c),
	})
	if !HasDevice(storeDir(c)) {
		c.Log("Not linked to WhatsApp yet: use “Link a device” and scan the QR code with the phone", nil)
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		err := errors.New("the WhatsApp bridge failed")
		c.Safe("the bridge", func() { err = b.Run(ctx) })
		done <- err
	}()

	devices := keys.Path()
	ps := []string{devices, devices + "-wal"}
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
// "unblock" lets sending go again after WhatsApp's warning (a person's decision): the bridge's block
// cleared and this source's sending on.
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
		if !c.Bool("send") {
			if err := plugins.Update(c.Store(), c.ID, nil, M{"send": true}, nil, false); err != nil {
				return err
			}
			c.Settings["send"] = true
			c.Emit(M{"type": "changed"})
		}
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

// MarkRead marks the chat's messages up to `until` (Unix ms) read: on the account's own devices
// always, and for the others too where the user turned read receipts on. It returns how many
// messages were marked.
func (p Plugin) MarkRead(ctx context.Context, c *plugins.Context, conv plugins.Conversation, until int64) (int, error) {
	b := Running(storeDir(c))
	if b == nil {
		return 0, nil // not connected: nothing marked (the bridge answered so)
	}
	code, answer := b.MarkRead(ReadRequest{Recipient: strings.TrimPrefix(conv.Key, "+"), Until: floorDiv(until, 1000),
		Self: !c.Bool("read_receipts")})
	if code != http.StatusOK {
		return 0, nil // a chat the bridge does not know (nothing of it to mark)
	}
	n, _ := answer["marked"].(int)
	return n, nil
}

// FetchMedia downloads a message's file now through the bridge running here (only while the live
// connection runs): the message by its id in what the bridge keeps to download, in whichever chat
// it has it.
func (p Plugin) FetchMedia(ctx context.Context, c *plugins.Context, messageID int64) (string, error) {
	b := Running(storeDir(c))
	if b == nil {
		return "", nil
	}
	var msgKey sql.NullString
	if !db.Row(c.Store().Read(), "SELECT m.key FROM message m JOIN service s ON s.id = m.service_id "+
		"WHERE m.id = ? AND s.name = 'whatsapp'", []any{messageID}, &msgKey) || msgKey.String == "" {
		return "", nil
	}
	if p := importers.StoredFile(c.Store().Read(), messageID); p != "" {
		return p, nil
	}
	d := keysDB()
	if d == nil {
		return "", nil
	}
	chats := db.Strs(d, "SELECT chat_jid FROM whatsapp_media WHERE store = ? AND id = ? AND media_type IS NOT NULL",
		key(storeDir(c)), msgKey.String)
	var last error
	for _, jid := range chats {
		// into the archive's store at once, where it stays the only copy
		if last = b.Download(msgKey.String, jid); last == nil {
			return importers.StoredFile(c.Store().Read(), messageID), nil
		}
	}
	return "", last
}

// FilesPending: the messages whose file the connection still downloads (as it does at each start,
// those of the last week): a row of whatsapp_media with no file taken yet, and none given up on but
// for the phone asked again for it; none where files are not downloaded.
func (p Plugin) FilesPending(ctx context.Context, c *plugins.Context, messageIDs []int64) map[int64]bool {
	out := map[int64]bool{}
	d := keysDB()
	if d == nil || !ConfigOptions().Download || len(messageIDs) == 0 {
		return out
	}
	byKey := map[string][]int64{}
	db.Each(c.Store().Read(), "SELECT m.id, m.key FROM message m JOIN service s ON s.id = m.service_id "+
		"WHERE s.name = 'whatsapp' AND m.key IS NOT NULL AND m.id IN ("+db.Marks(len(messageIDs))+")", db.Args(messageIDs),
		func(scan func(...any)) {
			var mid int64
			var k string
			scan(&mid, &k)
			byKey[k] = append(byKey[k], mid)
		})
	if len(byKey) == 0 {
		return out
	}
	args := []any{key(storeDir(c)), askedAgain, time.Now().Add(-downloadBackfill).UnixMilli()}
	for k := range byKey {
		args = append(args, k)
	}
	db.Each(d, "SELECT id FROM whatsapp_media WHERE store = ? AND media_type IS NOT NULL AND (error IS NULL OR error = ?) "+
		"AND at > ? AND id IN ("+db.Marks(len(byKey))+")", args, func(scan func(...any)) {
		var k string
		scan(&k)
		for _, mid := range byKey[k] {
			out[mid] = true
		}
	})
	return out
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
		req.Media, req.Filename, req.MimeType, req.Animated = file.Data, file.Filename, file.MimeType, file.Animated
	}
	if reply != nil {
		// the bridge quotes the archive's copy (who wrote it as its own record of the last week names
		// them, where it has it)
		req.ReplyTo = reply.Key
		var outgoing sql.NullInt64
		var sender, txt, kind, name sql.NullString
		if db.Row(c.Store().Read(), "SELECT m.outgoing, a.value, m.text, k.name, (SELECT t.name FROM attachment t "+
			"WHERE t.message_id = m.id ORDER BY t.rowid LIMIT 1) FROM message m LEFT JOIN address a ON a.id = m.sender_id "+
			"LEFT JOIN message_kind k ON k.id = m.kind_id WHERE m.id = ?", []any{reply.ID}, &outgoing, &sender, &txt, &kind, &name) {
			if outgoing.Int64 != 0 {
				req.ReplySender = "me"
			} else {
				req.ReplySender = sender.String
			}
			req.ReplyText, req.ReplyKind, req.ReplyFilename = txt.String, waKind(kind.String), name.String
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
	if conv.ID == 0 { // a first message, to someone found: only to someone the owner knows
		if !known(c, conv.Key) {
			return nil, errs.Plugin(notAContact, 0)
		}
		req.First = true
	}
	b := Running(storeDir(c))
	if b == nil {
		return nil, errs.Plugin("not connected to WhatsApp", 503)
	}
	b.SetLimits(limits(c)) // as the settings are now
	code, answer := b.Send(req)
	if code != http.StatusOK { // its refusals: off, blocked, a limit, not a chat they wrote in
		watchState(c)
		// a limit or no connection may pass (kept and sent again), as may a failure on the way
		status := 0
		switch {
		case code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable:
			status = code
		case code >= 500:
			status = http.StatusBadGateway
		}
		if answer.Message == "" {
			return nil, errs.Plugin("Sending failed", status)
		}
		return nil, errs.Plugin(answer.Message, status)
	}
	return plugins.Sent{Keys: []string{answer.ID}}, nil
}

// notAContact: a chat is started from here only with someone the owner knows.
const notAContact = "A new WhatsApp chat starts from here only with someone in your contacts or in a group with you"

// known says whether the person of a number is in the owner's contacts, or in a group with them (on
// any service): one to start a chat with, not a stranger.
func known(c *plugins.Context, phone string) bool {
	return db.Exists(c.Store().Read(), "SELECT 1 FROM address a JOIN address_kind k ON k.id = a.kind_id "+
		"JOIN person_address pa ON pa.address_id = a.id JOIN person p ON p.id = pa.person_id "+
		"WHERE k.name = 'phone' AND a.value = ? AND (p.contact_uid IS NOT NULL OR EXISTS ("+
		"SELECT 1 FROM person_address mine JOIN conversation_member cm ON cm.address_id = mine.address_id "+
		"JOIN conversation g ON g.id = cm.conversation_id WHERE mine.person_id = p.id AND g.is_group))", phone)
}

// Find asks WhatsApp which of the numbers it has (nothing is sent to anyone).
func (Plugin) Find(ctx context.Context, c *plugins.Context, phones []string) ([]plugins.Found, error) {
	b := Running(storeDir(c))
	if b == nil {
		return nil, errs.Plugin("not connected to WhatsApp", 503)
	}
	on, err := b.Find(ctx, phones)
	if err != nil {
		return nil, err
	}
	var out []plugins.Found
	for _, p := range phones {
		if on[p] {
			out = append(out, plugins.Found{Phone: p, Service: "whatsapp", Key: p})
		}
	}
	return out, nil
}

func (p Plugin) React(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, emoji string) error {
	return change(c, conv, msg, ChangeRequest{Kind: "react", Emoji: emoji})
}

func (p Plugin) Edit(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, text string) error {
	return change(c, conv, msg, ChangeRequest{Kind: "edit", Text: text})
}

func (p Plugin) Delete(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref) error {
	return change(c, conv, msg, ChangeRequest{Kind: "delete"})
}

// DeleteForMe deletes any message for the user alone, on all their devices.
func (p Plugin) DeleteForMe(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref) error {
	return change(c, conv, msg, ChangeRequest{Kind: "delete_me"})
}

// change asks the bridge to react, edit or delete (for everyone, or for the user) under the gates of
// sending (each is a message to WhatsApp); the bridge writes it into the archive, as Send does.
func change(c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, req ChangeRequest) error {
	if !c.Bool("send") {
		return errs.Plugin("Sending is off in this source's settings", 0)
	}
	req.Recipient, req.ID, req.FromMe, req.TS = recipient(conv.Key), msg.Key, msg.Outgoing, msg.TS
	// the message as the archive has it: who wrote it, its kind, whom it names
	var sender, kind sql.NullString
	req.Known = db.Row(c.Store().Read(), "SELECT a.value, k.name FROM message m LEFT JOIN address a ON a.id = m.sender_id "+
		"LEFT JOIN message_kind k ON k.id = m.kind_id WHERE m.id = ?", []any{msg.ID}, &sender, &kind)
	if !msg.Outgoing {
		req.Sender = sender.String
	}
	req.MessageKind = waKind(kind.String)
	db.Each(c.Store().Read(), "SELECT coalesce(n.token, ''), a.value FROM mention n JOIN address a ON a.id = n.address_id WHERE n.message_id = ?",
		[]any{msg.ID}, func(scan func(...any)) {
			var token, value string
			scan(&token, &value)
			if jid := mentionedJID(token, value); jid != "" {
				req.Mentions = append(req.Mentions, jid)
			}
		})
	b := Running(storeDir(c))
	if b == nil {
		return errs.Plugin("not connected to WhatsApp", 503)
	}
	code, answer := b.Change(req)
	if code != http.StatusOK {
		watchState(c)
		if answer.Message == "" {
			return errs.Plugin("Sending failed", 0)
		}
		return errs.Plugin(answer.Message, 0)
	}
	return nil
}

// waKind is the archive's kind of a message as WhatsApp names it (a file is a document).
func waKind(kind string) string {
	if kind == "file" {
		return "document"
	}
	return kind
}

// mentionedJID is the jid a mention names, from its token in the text (@<number or LID>) and the
// archive's address of whom it names: a number's when the token is their number, else a LID's.
func mentionedJID(token, value string) string {
	user := strings.TrimPrefix(token, "@")
	switch {
	case user == "":
		return ""
	case value == "+"+user:
		return user + "@s.whatsapp.net"
	case strings.HasSuffix(value, "@lid") || strings.Trim(user, "0123456789") == "":
		return user + "@lid"
	}
	return ""
}
