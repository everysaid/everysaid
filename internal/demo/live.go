// Ports demo_message, demo_receipt and DemoSender of everysaid/demo.py: what the demo does while it
// is served (sending into the demo archive, answers, receipts, messages arriving).

package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
)

// ImportLocker is a host that has a lock for writing into the archive from outside the store (the
// importers' lock); Message and Receipt take it where the host has one.
type ImportLocker interface {
	ImportLock() sync.Locker
}

func lock(h plugins.Host) func() {
	if l, ok := h.(ImportLocker); ok {
		m := l.ImportLock()
		m.Lock()
		return m.Unlock
	}
	return func() {}
}

// splitext is os.path.splitext's extension: from the last dot of the last part, unless that part
// starts with its only dots.
func splitext(p string) string {
	base := p[strings.LastIndexAny(p, `/`+string(os.PathSeparator))+1:]
	i := strings.LastIndex(base, ".")
	if i <= 0 || strings.Trim(base[:i], ".") == "" {
		return ""
	}
	return base[i:]
}

// Message writes a message into the demo archive as if a service had brought it (sent by the user,
// or from the other side of the conversation), and tells the apps; it returns its id. mentions:
// where in the text (in characters) each member is named; file: attached.
func Message(h plugins.Host, conversationID int64, text string, outgoing bool, replyKey string,
	mentions []plugins.Mention, file *plugins.File) (id int64, err error) {
	return message(h, conversationID, text, outgoing, replyKey, mentions, file, "")
}

// message is Message, of the kind given ("": the file's, else text).
func message(h plugins.Host, conversationID int64, text string, outgoing bool, replyKey string,
	mentions []plugins.Mention, file *plugins.File, kind string) (id int64, err error) {
	var m0 any
	func() {
		defer lock(h)()
		var a *archive.Archive
		a, err = archive.Open(h.Store().Path)
		if err != nil {
			return
		}
		defer a.Close()
		defer archive.Recover(&err)
		src := a.Source("demo/live", "demo", "", "")
		var service string
		a.Row("SELECT s.name FROM conversation c JOIN service s ON s.id = c.service_id WHERE c.id = ?",
			[]any{conversationID}, &service)
		var who int64
		if !outgoing {
			who = a.Int("SELECT address_id FROM conversation_member WHERE conversation_id = ? AND "+
				"address_id NOT IN (SELECT address_id FROM account) LIMIT 1", conversationID)
		}
		if v, ok := a.IntOK("SELECT max(id) FROM message"); ok {
			m0 = v
		}
		n := time.Now().UnixNano()
		if kind == "" {
			kind = "text"
		}
		if file != nil {
			switch {
			case strings.HasPrefix(file.MimeType, "image/"):
				kind = "image"
			case strings.HasPrefix(file.MimeType, "video/"):
				kind = "video"
			default:
				kind = "file"
			}
		}
		key := ""
		if service != "sms" {
			key = fmt.Sprintf("demo-live-%d", n)
		}
		var x *archive.Extras
		if replyKey != "" || file != nil && file.Animated {
			x = &archive.Extras{ReplyKey: replyKey}
			if file != nil && file.Animated {
				x.Subtype = "gif"
			}
		}
		mid := a.AddMessage(src, fmt.Sprintf("live-%d", n), archive.Message{Service: service, ConversationID: conversationID,
			TS: n / 1_000_000, Outgoing: outgoing, SenderID: who, Kind: kind, Text: text, Key: key, Extras: x})
		runes := []rune(text)
		for _, m := range mentions {
			from, to := min(max(m.Start, 0), len(runes)), min(max(m.Start+m.Length, 0), len(runes))
			a.Exec("INSERT OR IGNORE INTO mention VALUES (?, ?, ?)", mid, m.AddressID, string(runes[from:max(from, to)]))
		}
		if file != nil {
			folder := filepath.Join(config.Cache, "demo-src")
			if err := os.MkdirAll(folder, 0o777); err != nil {
				panic(err)
			}
			rel := fmt.Sprintf("live-%d%s", n, strings.ToLower(splitext(file.Filename)))
			if err := os.WriteFile(filepath.Join(folder, rel), file.Data, 0o666); err != nil {
				panic(err)
			}
			importers.NewStore(a).Link("demo/live", src, filepath.Join(folder, rel), rel, mid)
		}
		a.Resolve()
		a.Commit()
		id = a.Int("SELECT max(id) FROM message")
	}()
	if err != nil {
		return 0, err
	}
	h.Emit(core.M{"type": "new", "messages": []any{m0, id}, "calls": []any{0, 0}})
	return id, nil
}

// coming are the demo's pictures still on their way (IncomingPicture), until PictureArrives.
var coming = struct {
	sync.Mutex
	ids map[int64]bool
}{ids: map[int64]bool{}}

// IncomingPicture: a picture arrives in a conversation from the other side, its file not yet (as a
// live source's still downloading it): on its way until PictureArrives.
func IncomingPicture(h plugins.Host, conversationID int64) (int64, error) {
	coming.Lock()
	defer coming.Unlock()
	id, err := message(h, conversationID, "", false, "", nil, nil, "image")
	if err == nil {
		coming.ids[id] = true
	}
	return id, err
}

// PictureArrives brings the file of a picture IncomingPicture left on its way, and tells the apps.
func PictureArrives(h plugins.Host, messageID int64) (err error) {
	coming.Lock()
	defer coming.Unlock()
	if !coming.ids[messageID] {
		return errs.New("not_found", 404, nil)
	}
	func() {
		defer lock(h)()
		var a *archive.Archive
		a, err = archive.Open(h.Store().Path)
		if err != nil {
			return
		}
		defer a.Close()
		defer archive.Recover(&err)
		folder := filepath.Join(config.Cache, "demo-src")
		if err := os.MkdirAll(folder, 0o777); err != nil {
			panic(err)
		}
		rel := fmt.Sprintf("arrived-%d.jpg", messageID)
		im := newCanvas(480, 360, [3]byte{70, 130, 180})
		im.ellipse(140, 80, 340, 280, [3]byte{250, 210, 90})
		if err := os.WriteFile(filepath.Join(folder, rel), encodeJPEG(im, 85), 0o666); err != nil {
			panic(err)
		}
		src := a.Source("demo/live", "demo", "", "")
		importers.NewStore(a).Link("demo/live", src, filepath.Join(folder, rel), rel, messageID)
		a.Commit()
	}()
	if err != nil {
		return err
	}
	delete(coming.ids, messageID)
	h.Emit(core.M{"type": "changed"})
	return nil
}

// FilesPending: the demo's pictures still on their way.
func (Sender) FilesPending(ctx context.Context, c *plugins.Context, messageIDs []int64) map[int64]bool {
	coming.Lock()
	defer coming.Unlock()
	out := map[int64]bool{}
	for _, id := range messageIDs {
		if coming.ids[id] {
			out[id] = true
		}
	}
	return out
}

// Receipt: the members of the message's conversation got (field "delivered") or read ("read") it now.
func Receipt(h plugins.Host, messageID int64, field string) (err error) {
	if field != "delivered" && field != "read" {
		return fmt.Errorf("demo: no receipt field %q", field)
	}
	func() {
		defer lock(h)()
		var a *archive.Archive
		a, err = archive.Open(h.Store().Path)
		if err != nil {
			return
		}
		defer a.Close()
		defer archive.Recover(&err)
		for _, aid := range a.Ints("SELECT address_id FROM conversation_member WHERE conversation_id = "+
			"(SELECT conversation_id FROM message WHERE id = ?) AND address_id NOT IN "+
			"(SELECT address_id FROM account)", messageID) {
			a.Exec("INSERT OR IGNORE INTO receipt (message_id, address_id) VALUES (?, ?)", messageID, aid)
			a.Exec("UPDATE receipt SET "+field+"_at = ? WHERE message_id = ? AND address_id = ?",
				time.Now().UnixMilli(), messageID, aid)
		}
		a.Commit()
	}()
	if err != nil {
		return err
	}
	h.Emit(core.M{"type": "changed"})
	return nil
}

// Sender is a source that "sends" by writing into the demo archive, and gets an answer a moment
// later: only in the demo (EVERYSAID_DEMO), so that sending and receiving, and what the interface
// does after them, can be tried and tested. (The demo also takes incoming messages at
// /api/demo/incoming, through Message.)
type Sender struct{}

var senderInfo = &plugins.Info{
	ID:           "demo-sender",
	Name:         "Demo (sends into the demo archive)",
	Kind:         "source",
	Services:     []string{"whatsapp", "viber", "sms", "telegram"}, // not iMessage: a chat it cannot send to
	Description:  "Invented: what is sent is only written into the demo archive.",
	CanSend:      true,
	CanReply:     true,
	CanMention:   true,
	CanMarkRead:  true,
	CanSendFiles: true,
	// as the services with the most, carried as given
	Styles:    plugins.StyleKinds,
	CanReact:  true, // any emoji
	CanEdit:   true,
	CanDelete: true,
	// any message, for the user alone
	CanDeleteForMe: true,
}

func (Sender) Info() *plugins.Info { return senderInfo }

// change writes what the user did to a message into the demo archive, as a source's import brings it.
func change(c *plugins.Context, fn func(a *archive.Archive)) (err error) {
	h := c.Host()
	func() {
		defer lock(h)()
		var a *archive.Archive
		a, err = archive.Open(h.Store().Path)
		if err != nil {
			return
		}
		defer a.Close()
		defer archive.Recover(&err)
		fn(a)
		a.Commit()
	}()
	return err
}

// React puts the user's reaction in place of theirs ("" takes it back); the others' stay.
func (Sender) React(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, emoji string) error {
	return change(c, func(a *archive.Archive) {
		var keep []archive.Reaction
		a.Each("SELECT emoji, code, count, address_id FROM reaction WHERE message_id = ? AND NOT coalesce(outgoing, 0)",
			[]any{msg.ID}, func(scan func(...any)) {
				var r archive.Reaction
				var e, code *string
				var who *int64
				scan(&e, &code, &r.Count, &who)
				if e != nil {
					r.Emoji = *e
				}
				if code != nil {
					r.Code = *code
				}
				if who != nil {
					r.Who = *who
				}
				keep = append(keep, r)
			})
		if emoji != "" {
			keep = append(keep, archive.Reaction{Emoji: emoji, Count: 1, Outgoing: true})
		}
		importers.ReplaceReactions(a, msg.ID, keep)
	})
}

func (Sender) Edit(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, text string) error {
	return change(c, func(a *archive.Archive) {
		importers.ApplyChange(a, msg.ID, importers.Change{Text: &text, Edited: true})
	})
}

func (Sender) Delete(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref) error {
	return change(c, func(a *archive.Archive) { importers.ApplyChange(a, msg.ID, importers.Change{Deleted: true}) })
}

// DeleteForMe marks the message deleted, as a deletion for everyone: the archive keeps it the same way.
func (Sender) DeleteForMe(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref) error {
	return change(c, func(a *archive.Archive) { importers.ApplyChange(a, msg.ID, importers.Change{Deleted: true}) })
}

// Forward writes the message, marked forwarded, into each conversation (of its own service).
func (Sender) Forward(ctx context.Context, c *plugins.Context, from plugins.Conversation, msgs []plugins.Ref, to []plugins.Conversation) error {
	for _, conv := range to {
		for _, m := range msgs {
			text := db.Str(c.Store().Read(), "SELECT coalesce(text, '') FROM message WHERE id = ?", m.ID)
			id, err := Message(c.Host(), conv.ID, text, true, "", nil, nil)
			if err != nil {
				return err
			}
			if err := change(c, func(a *archive.Archive) { a.Exec("UPDATE message SET forwarded = 1 WHERE id = ?", id) }); err != nil {
				return err
			}
		}
	}
	return nil
}

func (Sender) Check(c *plugins.Context) (bool, string) { return true, "ready" }

// after runs fn a moment later, as Python's threading.Timer; its failure goes to the instance's log.
func after(c *plugins.Context, d time.Duration, fn func() error) {
	time.AfterFunc(d, func() {
		if err := fn(); err != nil {
			c.Logf("%v", err)
		}
	})
}

// SendStyled sends with the text's styles as the service has them: written as marks where its apps
// read them so (WhatsApp, Viber: the archive reads them back), else as ranges kept beside the text.
func (s Sender) SendStyled(ctx context.Context, c *plugins.Context, conv plugins.Conversation, text string, reply *plugins.Reply,
	mentions []plugins.Mention, styles []plugins.Style, file *plugins.File) (any, error) {
	if archive.Marked[conv.Service] {
		text, mentions = plugins.MarkStyles(text, mentions, styles)
		return s.Send(ctx, c, conv, text, reply, mentions, file)
	}
	out, err := s.Send(ctx, c, conv, text, reply, mentions, file)
	if sent, ok := out.(plugins.Sent); ok && err == nil {
		err = change(c, func(a *archive.Archive) {
			for _, id := range sent.IDs {
				a.SetStyles(id, unitStyles(text, styles))
			}
		})
	}
	return out, err
}

// EditStyled changes the text with its styles, as SendStyled sends them.
func (s Sender) EditStyled(ctx context.Context, c *plugins.Context, conv plugins.Conversation, msg plugins.Ref, text string,
	styles []plugins.Style) error {
	kept := unitStyles(text, styles)
	if archive.Marked[conv.Service] { // read back from the marks by the archive
		text, _ = plugins.MarkStyles(text, nil, styles)
		kept = nil
	}
	return change(c, func(a *archive.Archive) {
		importers.ApplyChange(a, msg.ID, importers.Change{Text: &text, Styles: kept, Edited: true})
	})
}

// unitStyles are styles (in characters) as the archive keeps them, in UTF-16 units.
func unitStyles(text string, styles []plugins.Style) []archive.Style {
	runes := []rune(text)
	var out []archive.Style
	for _, s := range styles {
		if s.Start+s.Length <= len(runes) {
			off, n := plugins.UTF16(runes, s.Start, s.Length)
			out = append(out, archive.Style{Start: off, Length: n, Kind: s.Kind, URL: s.URL})
		}
	}
	return out
}

// busyOnce: the "busy:" texts refused once already.
var busyOnce sync.Map

func (Sender) Send(ctx context.Context, c *plugins.Context, conv plugins.Conversation, text string, reply *plugins.Reply,
	mentions []plugins.Mention, file *plugins.File) (any, error) {
	select { // as a real service takes a moment
	case <-time.After(800 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// "busy:" cannot go the first time (a limit of pace): the server keeps it and sends it again
	if strings.Contains(text, "busy:") {
		if _, again := busyOnce.LoadOrStore(text, true); !again {
			return nil, errs.Plugin("limit reached: 15 messages a minute", 429)
		}
	}
	// "nowhere:" can never go there (the other side has left the service): refused for good
	if strings.Contains(text, "nowhere:") {
		e := errs.Plugin("They are no longer on this service", 0)
		e.Final = true
		return nil, e
	}
	replyKey := ""
	if reply != nil {
		replyKey = reply.Key
	}
	h := c.Host()
	if conv.ID == 0 { // a first message, to someone found (Find): the conversation with their number
		if err := change(c, func(a *archive.Archive) {
			conv.ID = a.Conversation(conv.Service, []archive.Handle{archive.H("phone", conv.Key)}, conv.Key, "")
		}); err != nil {
			return nil, err
		}
	}
	mid, err := Message(h, conv.ID, text, true, replyKey, mentions, file)
	if err != nil {
		return nil, err
	}
	if conv.Service == "whatsapp" || conv.Service == "telegram" { // they tell who got and read it
		after(c, time.Second, func() error { return Receipt(h, mid, "delivered") })
		after(c, 2500*time.Millisecond, func() error { return Receipt(h, mid, "read") })
	}
	// a test's words: "quiet:" no answer from the other side, "late:" the sending said done only
	// a while after the message is in, "lost:" said failed though it went
	if !strings.Contains(text, "quiet:") {
		after(c, 1500*time.Millisecond, func() error {
			_, err := Message(h, conv.ID, "↩ "+text, false, "", nil, nil)
			return err
		})
	}
	if strings.Contains(text, "late:") {
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if strings.Contains(text, "lost:") {
		return nil, errs.Plugin("Sending failed", 0)
	}
	return plugins.Sent{IDs: []int64{mid}}, nil
}

// Find: every number is on WhatsApp and Viber, one ending in an even digit on Telegram too (never
// SMS: it is not looked for).
func (Sender) Find(ctx context.Context, c *plugins.Context, phones []string) ([]plugins.Found, error) {
	select { // as a real service takes a moment, each its own
	case <-time.After(600 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var out []plugins.Found
	for _, p := range phones {
		out = append(out, plugins.Found{Phone: p, Service: "whatsapp", Key: p}, plugins.Found{Phone: p, Service: "viber", Key: p})
		if strings.IndexByte("02468", p[len(p)-1]) >= 0 {
			out = append(out, plugins.Found{Phone: p, Service: "telegram", Key: p})
		}
	}
	return out, nil
}

func (Sender) MarkRead(ctx context.Context, c *plugins.Context, conv plugins.Conversation, until int64) (int, error) {
	return 1, nil
}

// RegisterIfDemo adds the demo's sender to the plugins when EVERYSAID_DEMO is set (as Python's
// plugins/__init__.py does). The command line calls it at its start, after every other plugin
// has registered (so it comes last, as in Python), and Main again once it has set the variable.
func RegisterIfDemo() {
	if os.Getenv("EVERYSAID_DEMO") != "" {
		plugins.Register(Sender{})
		plugins.Register(GIFs{})
	}
}
