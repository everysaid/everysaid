// Package meta is Messenger (and Instagram's history) as sources of the archive: Meta's export
// ("Download your information") imported, and Messenger live through mautrix-meta's messagix
// client (third_party/mautrix-meta), linked into the binary: its chats as they change, end-to-end
// encrypted ones too (through whatsmeow), their files, the earlier history of each chat, and
// sending, reactions, edits, unsends and read receipts.
//
// The login is the browser's: the user logs in to facebook.com (or messenger.com) and pastes its
// cookies into the source's settings; they are checked with Meta, kept in keys.db (internal/keys,
// tables meta_*) with what the connection needs, and taken out of the settings' secret.
//
// The library's types stay in this package: the archive sees only what importers.MetaWriter takes.
package meta

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"go.mau.fi/whatsmeow/types"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	tps "go.mau.fi/mautrix-meta/pkg/messagix/types"

	"everysaid/internal/archive"
	"everysaid/internal/errs"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

type M = plugins.M

// Plugin is a Meta service as a source: messenger, or instagram (its export only).
type Plugin struct{ service string }

func init() {
	plugins.Register(Plugin{"messenger"})
	plugins.Register(Plugin{"instagram"})
}

// The emoji Messenger offers first.
var reactions = []string{"❤️", "😆", "😮", "😢", "😠", "👍"}

var exportSettings = []plugins.Setting{
	{Key: "export", Label: "The export (Download your information), unpacked", Type: "path",
		Help: "Its JSON form, one folder or a folder of its parts; imported when the source runs"},
	{Key: "owner", Label: "Your name as the export writes it",
		Help: "Only where the export does not say whose it is"},
}

var messengerInfo = &plugins.Info{
	ID: "messenger", Name: "Messenger", Kind: "source", Services: []string{"messenger"},
	ServiceInfo:  sourcekit.Looks("messenger"),
	NameWeights:  []plugins.Weight{{Key: "messenger/profile", Weight: 40}},
	StateWeights: map[string]int{"muted": 60},
	Description: "Facebook Messenger with the user's own account, logged in with the browser's cookies: " +
		"every chat as it changes, the encrypted ones too, with each chat's earlier history; and Meta's export " +
		"(Download your information) for what is older. Unofficial: Meta may ask to check the account.",
	Modes:       []string{"import", "live"},
	LiveDefault: true,
	Needs:       []string{"the cookies of facebook.com or messenger.com, where you are logged in", "or Meta's export"},
	Settings: append([]plugins.Setting{
		{Key: "cookies", Label: "Cookies of facebook.com or messenger.com", Type: "secret",
			Help: "From the browser where you are logged in: a JSON object of them, a request copied as cURL from the " +
				"developer tools, or the Cookie header (c_user, xs and datr are needed). Checked with Meta and kept in " +
				"keys.db; give them again when Meta logs the source out"},
		{Key: "site", Label: "The cookies are of", Type: "select", Default: "facebook",
			Options: []plugins.Option{{Value: "facebook", Label: "facebook.com"}, {Value: "messenger", Label: "messenger.com"}}},
		{Key: "media", Label: "Download pictures, videos and files", Type: "bool", Default: true,
			Help: "As they arrive (Meta's addresses for them expire)"},
		{Key: "history", Label: "Bring each chat's earlier messages", Type: "bool", Default: true,
			Help: "Back to what the archive has, a page at a time"},
		{Key: "read_receipts", Label: "Send read receipts", Type: "bool", Default: false,
			Help: "When a chat is opened here, the others see it read, and it is read on the phone too"},
	}, exportSettings...),
	CanSend: true, CanReply: true, CanMention: true, CanMarkRead: true, CanSendFiles: true,
	CanReact: true, Reactions: reactions, FreeReactions: true, CanEdit: true, CanDelete: true,
	EditWindow: 15 * time.Minute, // Messenger's own: edits within 15 minutes
	Actions: []plugins.Action{{ID: "logout", Label: "Log out",
		Confirm: "The source forgets the account's cookies and its encrypted chats' device; the archive keeps everything"}},
}

var instagramInfo = &plugins.Info{
	ID: "instagram", Name: "Instagram", Kind: "source", Services: []string{"instagram"},
	ServiceInfo: sourcekit.Looks("instagram"),
	Description: "Instagram's direct messages from Meta's export (Download your information), with their files.",
	Modes:       []string{"import"},
	Needs:       []string{"Meta's export"},
	Settings:    exportSettings,
}

func (p Plugin) Info() *plugins.Info {
	if p.service == "instagram" {
		return instagramInfo
	}
	return messengerInfo
}

func (p Plugin) Check(c *plugins.Context) (bool, string) {
	if p.service == "messenger" {
		sess, _ := loadSession(c.ID)
		if loggedIn(sess) || c.Secret("cookies") != "" {
			return true, "ready"
		}
	}
	if c.Str("export") != "" {
		return true, "ready"
	}
	if p.service == "messenger" {
		return false, "missing: the cookies or the export"
	}
	return false, "missing: the export"
}

// platform is messagix's platform of the cookies given.
func platform(c *plugins.Context) tps.Platform {
	if c.Str("site") == "messenger" {
		return tps.Messenger
	}
	return tps.Facebook
}

// login checks pasted cookies with Meta and keeps the session in keys.db; the settings' secret goes.
func login(ctx context.Context, c *plugins.Context, text string) (*session, error) {
	values, err := parseCookies(text)
	if err != nil {
		return nil, errs.Plugin("No cookies found in what was pasted", 400)
	}
	plat := platform(c)
	ck := &cookies.Cookies{Platform: plat}
	named := map[cookies.MetaCookieName]string{}
	for k, v := range values {
		named[cookies.MetaCookieName(k)] = v
	}
	ck.UpdateValues(named)
	if missing := ck.GetMissingCookieNames(); len(missing) > 0 {
		var names []string
		for _, n := range missing {
			names = append(names, string(n))
		}
		return nil, &errs.UserError{Code: "plugin", Status: 400, Text: "Missing cookies: {names}",
			Params: map[string]any{"names": names}}
	}
	logger, closer := libraryLog(c.ID)
	defer closer.Close()
	cli := newClient(ck, logger)
	user, _, err := cli.LoadMessagesPage(ctx)
	old, _ := loadSession(c.ID)
	if old == nil {
		old = &session{}
	}
	if err != nil {
		return nil, connectError(err, &session{}, 0)
	}
	if user.GetFBID() != ck.GetUserID() {
		return nil, errs.Plugin("The cookies are of another account than the one logged in", 409)
	}
	id := strconv.FormatInt(user.GetFBID(), 10)
	if old.UserID != "" && old.UserID != id {
		return nil, errs.Plugin("The cookies are of another account than this source's", 409)
	}
	b, _ := json.Marshal(cli.GetCookies())
	sess := &session{Platform: plat.String(), UserID: id, Name: user.GetName(), Cookies: b, WADevice: old.WADevice}
	if err := saveSession(c.ID, sess); err != nil {
		return nil, err
	}
	c.DeleteSecret("cookies")
	c.Log("{service}: logged in as {name}", map[string]any{"service": "Messenger", "name": sess.Name})
	return sess, nil
}

// pendingLogin logs in with cookies given in the settings, if any.
func pendingLogin(ctx context.Context, c *plugins.Context) error {
	text := c.Secret("cookies")
	if text == "" {
		return nil
	}
	_, err := login(ctx, c, text)
	return err
}

func (p Plugin) RunImport(c *plugins.Context) error {
	ctx := context.Background()
	if p.service == "messenger" {
		if err := pendingLogin(ctx, c); err != nil {
			return err
		}
		if sess, _ := loadSession(c.ID); loggedIn(sess) {
			// the account's own handle, at once (the live connection brings the rest)
			if _, _, err := sourcekit.RunImporters(c, []sourcekit.Step{{Label: "Messenger", Run: func(a *archive.Archive, _ func(string)) error {
				writer(a, c, p.service).Self(importers.MetaUser{ID: sess.UserID, Name: sess.Name})
				return nil
			}}}); err != nil {
				return err
			}
		}
	}
	if dir := c.Str("export"); dir != "" {
		_, _, err := sourcekit.RunImporters(c, []sourcekit.Step{{Label: exportLabel(p.service), Run: func(a *archive.Archive, out func(string)) error {
			err := importers.MetaExport(a, out, importers.MetaExportOptions{Dir: dir, Service: p.service,
				Owner: c.Str("owner"), Instance: c.ID})
			if err == importers.ErrMetaOwner {
				return errs.Plugin(err.Error(), 409)
			}
			return err
		}}})
		return err
	}
	return nil
}

func (p Plugin) Live(ctx context.Context, c *plugins.Context) error {
	if err := pendingLogin(ctx, c); err != nil {
		return err
	}
	if sess, _ := loadSession(c.ID); !loggedIn(sess) {
		// set up for the export only: nothing to connect, until cookies are given
		c.Log("{service}: not logged in (the cookies in its settings)", map[string]any{"service": "Messenger"})
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(30 * time.Second):
			}
			if c.Secret("cookies") != "" {
				return nil // the host connects again, with them
			}
		}
	}
	return live(ctx, c, p.service)
}

func (p Plugin) Action(c *plugins.Context, name string) error {
	if name != "logout" || p.service != "messenger" {
		return nil
	}
	sess, err := loadSession(c.ID)
	if err != nil {
		return err
	}
	if s := running(c.ID); s != nil {
		s.cli.Disconnect()
		if e := s.waClient(); e != nil {
			e.Disconnect()
		}
	}
	if sess != nil && sess.WADevice != 0 {
		ctx := context.Background()
		if ct, err := devices(ctx); err == nil {
			if dev, err := ct.GetDevice(ctx, types.JID{User: sess.UserID, Device: sess.WADevice, Server: types.MessengerServer}); err == nil && dev != nil {
				dev.Delete(ctx)
			}
		}
	}
	c.DeleteSecret("cookies")
	if err := forgetSession(c.ID); err != nil {
		return err
	}
	c.SaveState(M{"threads_listed": false})
	c.Log("{service}: logged out", map[string]any{"service": "Messenger"})
	return nil
}

func (p Plugin) IdleActions(c *plugins.Context) []string {
	if p.service != "messenger" {
		return nil
	}
	if sess, _ := loadSession(c.ID); sess == nil {
		return []string{"logout"}
	}
	return nil
}

func (p Plugin) InfoFacts(c *plugins.Context) []plugins.Fact {
	if p.service != "messenger" {
		return nil
	}
	sess, _ := loadSession(c.ID)
	if !loggedIn(sess) {
		return []plugins.Fact{{Label: "Account", Value: "not logged in"}}
	}
	facts := []plugins.Fact{{Label: "Account", Value: sess.Name + " (" + sess.UserID + ")"}}
	if s := running(c.ID); s != nil && s.connected.Load() {
		e2ee := "not connected"
		if s.waClient() != nil {
			e2ee = "connected"
		}
		facts = append(facts, plugins.Fact{Label: "Connection", Value: "connected"},
			plugins.Fact{Label: "Encrypted chats", Value: e2ee})
	} else {
		facts = append(facts, plugins.Fact{Label: "Connection", Value: "not connected"})
	}
	if files, _ := pendingFiles(c.ID, ""); len(files) > 0 {
		facts = append(facts, plugins.Fact{Label: "Files to download", Value: strconv.Itoa(len(files))})
	}
	return facts
}

func (p Plugin) NotSending(c *plugins.Context) string {
	if p.service != "messenger" {
		return "only the export"
	}
	if sess, _ := loadSession(c.ID); !loggedIn(sess) {
		return "not logged in"
	}
	return ""
}

func (p Plugin) Send(ctx context.Context, c *plugins.Context, conv plugins.Conversation, text string, reply *plugins.Reply,
	mentions []plugins.Mention, file *plugins.File) (any, error) {
	return send(ctx, c, p.service, conv, text, reply, mentions, file)
}

func (p Plugin) React(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, emoji string) error {
	return react(ctx, c, p.service, conv, msg, emoji)
}

func (p Plugin) Edit(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, text string) error {
	return edit(ctx, c, p.service, conv, msg, text)
}

func (p Plugin) Delete(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref) error {
	return unsend(ctx, c, p.service, conv, msg)
}

// MarkRead sends read receipts for the chat, where the user turned them on; nothing otherwise.
func (p Plugin) MarkRead(ctx context.Context, c *plugins.Context, conv plugins.Conversation, until int64) (int, error) {
	if !c.Bool("read_receipts") {
		return 0, nil
	}
	return markRead(ctx, c, p.service, conv, until)
}

func (p Plugin) FetchMedia(ctx context.Context, c *plugins.Context, messageID int64) (string, error) {
	return fetchMedia(ctx, c, messageID)
}

// liveLabel, exportLabel: the import steps' names, as the log says them.
func liveLabel(service string) string {
	if service == "instagram" {
		return "Instagram live"
	}
	return "Messenger live"
}

func exportLabel(service string) string {
	if service == "instagram" {
		return "Instagram export"
	}
	return "Messenger export"
}
