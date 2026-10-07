// Ports scripts/telegram-sync.py: the owner's Telegram history read through the Telegram API, with
// their own account.
//
//	everysaid telegram-sync --save-credentials   api_id and api_hash
//	everysaid telegram-sync --login              makes the session
//	everysaid telegram-sync --survey             survey of the chats
//	everysaid telegram-sync                      the messages
//	everysaid telegram-sync --media [--dry-run]  their pictures
//
// --save-credentials asks once for the api_id and api_hash from my.telegram.org (hidden) and keeps
// them as secrets (`telegram-api-id`, `telegram-api-hash`: the keyring, else files of those names
// in the config folder, mode 600). --login asks for the phone, the code and the 2FA password and
// makes the session, which gives full access to the account: kept as a secret too
// (`telegram-session-go`; made from Telethon's `telegram-session` when that is there, see
// session.go). The survey lists every chat with its kind, size and dates, no content.
//
// With no option, every chat but channels and bots is read into `<cache>/telegram/telegram.db`,
// each message whole (Telethon's own fields, as JSON, without the binary ones: file references and
// inline thumbnails), with the chats and the people seen. A later run brings only what came after
// the last message read in each chat; edits and deletions of older messages are not followed.
// --media then downloads the pictures, videos, GIFs, video notes and voice messages of those
// messages into `<cache>/telegram/media/<chat>/<message><ext>` (stickers and other files are not
// downloaded; the message keeps their name and size), except in the chats config `[telegram]
// no_media` lists (their ids, as the survey gives them), and not at all with `[telegram] media =
// false`; --dry-run says only how many and how big.
//
// How far each chat was read, by the owner and by the others, is kept too (`chat_read`).
//
// Read only: nothing is sent, nothing is marked read. Nothing secret is ever printed. Secret chats
// live only on the devices and are not reachable through the API.
package telegram

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gotd/td/tdp"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"golang.org/x/term"

	"everysaid/internal/db"
	"everysaid/internal/i18n"
)

// stdin is where --save-credentials and --login read what is typed (a test's reader in tests).
var stdin io.Reader = os.Stdin

// SyncOptions are the sync's flags.
type SyncOptions struct {
	SaveCredentials, Login, Survey, Media, DryRun bool
	Chats                                         []int64 // with Media: only these chats (nil: all)
	Lang                                          string  // of what it says ("": the system's, as on the command line)
}

// printer is where the sync says what it does, in its language.
type printer struct {
	io.Writer
	lang string
}

func (p *printer) say(text string, params map[string]any) string { return i18n.T(text, p.lang, params) }

// SyncMain is the command line: flags as telegram-sync.py's (--chats takes the ids after it,
// negative ones too, as argparse does).
func SyncMain(args []string, out io.Writer) error {
	var o SyncOptions
	flags := map[string]*bool{"--save-credentials": &o.SaveCredentials, "--login": &o.Login,
		"--survey": &o.Survey, "--media": &o.Media, "--dry-run": &o.DryRun}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if f, ok := flags[a]; ok {
			*f = true
			continue
		}
		if a != "--chats" {
			return fmt.Errorf("unrecognized arguments: %s", a)
		}
		o.Chats = []int64{}
		for i+1 < len(args) {
			n, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				if strings.HasPrefix(args[i+1], "--") {
					break
				}
				return fmt.Errorf("argument --chats: invalid int value: %q", args[i+1])
			}
			o.Chats = append(o.Chats, n)
			i++
		}
	}
	return Sync(context.Background(), o, out)
}

// Sync does what the options ask (the command line's and the plugin's way in).
func Sync(ctx context.Context, o SyncOptions, w io.Writer) error {
	if o.Lang == "" {
		o.Lang = i18n.CLILang()
	}
	out := &printer{w, o.Lang}
	if o.SaveCredentials {
		return saveCredentials(out)
	}
	var only map[int64]bool
	if o.Chats != nil {
		only = map[int64]bool{}
		for _, c := range o.Chats {
			only[c] = true
		}
	}
	if o.Media && o.DryRun {
		return mediaRun(ctx, nil, true, only, out)
	}
	store := &keyringSession{}
	if _, _, ok := credentials(); !ok {
		return errors.New(out.say(noCredentials, nil))
	}
	run := func(ctx context.Context, c *conn) error {
		switch {
		case o.Survey:
			return survey(ctx, c, out)
		case o.Media:
			return mediaRun(ctx, c, false, only, out)
		}
		return syncRun(ctx, c, out)
	}
	open, done, err := one.take(ctx, !o.Login)
	if err != nil {
		return err
	}
	defer done()
	if open != nil { // the live connection, or another sync's: never a second client
		return run(withThreshold(ctx, syncThreshold), open)
	}
	client, err := newClient(syncThreshold, store, nil)
	if err != nil {
		return err
	}
	return client.Run(ctx, func(ctx context.Context) error {
		if o.Login {
			flow := auth.NewFlow(terminalAuth{out}, auth.SendCodeOptions{})
			if err := client.Auth().IfNecessary(ctx, flow); err != nil {
				return err
			}
			me, err := client.Self(ctx)
			if err != nil {
				return err
			}
			where := store.where()
			if where == "" {
				where = out.say("unchanged", nil)
			}
			fmt.Fprintln(out, out.say("logged in as {name} (session: {where})", map[string]any{"name": me.FirstName, "where": where}))
			return nil
		}
		ok, err := authorized(ctx, client)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New(out.say("not logged in: run with --login first", nil))
		}
		c := newConn(client.API())
		defer one.share(c)()
		return run(ctx, c)
	})
}

// syncThreshold is the longest FLOOD_WAIT the sync sleeps through (telegram-sync.py's).
const syncThreshold = 300 * time.Second

// --- credentials and login -------------------------------------------------------------------------

func readLine(r *bufio.Reader) string {
	s, _ := r.ReadString('\n')
	return strings.TrimSpace(s)
}

// readHidden reads a line without showing it where stdin is a terminal.
func readHidden(r *bufio.Reader, out io.Writer) string {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, _ := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(out)
		return strings.TrimSpace(string(b))
	}
	return readLine(r)
}

var input *bufio.Reader

func reader() *bufio.Reader {
	if input == nil {
		input = bufio.NewReader(stdin)
	}
	return input
}

func saveCredentials(out *printer) error {
	r := reader()
	fmt.Fprint(out, "api_id: ")
	id := readLine(r)
	fmt.Fprint(out, out.say("api_hash (hidden): ", nil))
	hash := readHidden(r, out)
	digits := id != ""
	for _, c := range id {
		digits = digits && unicode.IsDigit(c)
	}
	if !digits || hash == "" {
		return errors.New(out.say("api_id must be a number and api_hash not empty", nil))
	}
	if _, err := secrets.save(SecretAPIID, id); err != nil {
		return err
	}
	where, err := secrets.save(SecretAPIHash, hash)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, out.say("saved in {where}", map[string]any{"where": where}))
	return nil
}

// terminalAuth asks on the terminal what a login needs, as Telethon's start() does.
type terminalAuth struct{ out *printer }

func (t terminalAuth) Phone(context.Context) (string, error) {
	fmt.Fprint(t.out, t.out.say("Please enter your phone: ", nil))
	return readLine(reader()), nil
}

func (t terminalAuth) Code(context.Context, *tg.AuthSentCode) (string, error) {
	fmt.Fprint(t.out, t.out.say("Please enter the code you received: ", nil))
	return readLine(reader()), nil
}

func (t terminalAuth) Password(context.Context) (string, error) {
	fmt.Fprint(t.out, t.out.say("Please enter your password: ", nil))
	return readHidden(reader(), t.out), nil
}

func (terminalAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error { return nil }

func (terminalAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("this phone number has no Telegram account")
}

// --- survey and sync -------------------------------------------------------------------------------

// dialogPeer is get_input_entity of a dialog's entity: oneself as InputPeerSelf.
func dialogPeer(d dialog) (tg.InputPeerClass, error) {
	if u, ok := d.Entity.(*tg.User); ok && u.Self {
		return &tg.InputPeerSelf{}, nil
	}
	if p, ok := InputPeer(d.Entity); ok {
		return p, nil
	}
	return nil, fmt.Errorf("cannot address chat %d", d.ID)
}

func isoDate(unix int) string { return time.Unix(int64(unix), 0).UTC().Format("2006-01-02") }

// tsvField is a field as Python's csv writes it (QUOTE_MINIMAL, tab-separated).
func tsvField(s string) string {
	if strings.ContainsAny(s, "\t\"\r\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func survey(ctx context.Context, c *conn, out *printer) error {
	if err := os.MkdirAll(Folder(), 0o700); err != nil {
		return err
	}
	dialogs, err := c.dialogs(ctx)
	if err != nil {
		return err
	}
	cols := []string{"kind", "id", "title", "archived", "messages", "photos_videos", "voice_round", "first", "last"}
	var rows [][]string
	type sum struct{ chats, msgs int }
	sums := map[string]*sum{}
	var kinds []string
	for _, d := range dialogs {
		peer, err := dialogPeer(d)
		if err != nil {
			return err
		}
		first, err := c.first(ctx, peer)
		if err != nil {
			return err
		}
		n, err := c.count(ctx, peer, nil)
		if err != nil {
			return err
		}
		pv, err := c.count(ctx, peer, &tg.InputMessagesFilterPhotoVideo{})
		if err != nil {
			return err
		}
		rv, err := c.count(ctx, peer, &tg.InputMessagesFilterRoundVoice{})
		if err != nil {
			return err
		}
		firstDate, lastDate := "", ""
		if first != nil {
			firstDate = isoDate(messageDate(first))
		}
		if d.Date != 0 {
			lastDate = isoDate(d.Date)
		}
		k := EntityKind(d.Entity)
		rows = append(rows, []string{k, strconv.FormatInt(d.ID, 10), d.Name, strconv.Itoa(boolInt(d.Archived)),
			strconv.Itoa(n), strconv.Itoa(pv), strconv.Itoa(rv), firstDate, lastDate})
		if sums[k] == nil {
			sums[k] = &sum{}
			kinds = append(kinds, k)
		}
		sums[k].chats++
		sums[k].msgs += n
		fmt.Fprintf(out, "\r%s", out.say("{n} chats", map[string]any{"n": len(rows)}))
	}
	fmt.Fprintln(out)
	path := filepath.Join(Folder(), "survey.tsv")
	var b strings.Builder
	if len(rows) == 0 {
		cols = cols[:1]
	}
	for _, row := range append([][]string{cols}, rows...) {
		for i, f := range row {
			if i > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(tsvField(f))
		}
		b.WriteString("\r\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return err
	}
	for _, k := range kinds {
		fmt.Fprintln(out, out.say("{kind} {chats} chats {messages} messages", map[string]any{
			"kind": fmt.Sprintf("%-11s", k), "chats": fmt.Sprintf("%5d", sums[k].chats), "messages": fmt.Sprintf("%9d", sums[k].msgs)}))
	}
	fmt.Fprintln(out, out.say("details: {path}", map[string]any{"path": path}))
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// batch is a store's writes in one transaction until commit, as Python's sqlite3 makes them.
type batch struct {
	d  *sql.DB
	tx *sql.Tx
}

func (b *batch) q() *sql.Tx {
	if b.tx == nil {
		tx, err := b.d.Begin()
		if err != nil {
			panic(&db.Error{Query: "BEGIN", Err: err})
		}
		b.tx = tx
	}
	return b.tx
}

func (b *batch) commit() error {
	if b.tx == nil {
		return nil
	}
	err := b.tx.Commit()
	b.tx = nil
	return err
}

func (b *batch) rollback() {
	if b.tx != nil {
		b.tx.Rollback()
		b.tx = nil
	}
}

// syncRun reads every chat but channels and bots into the store: what came after its last message.
func syncRun(ctx context.Context, c *conn, out *printer) (err error) {
	defer db.Recover(&err)
	store, err := openStore(DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	b := &batch{d: store}
	defer b.rollback()
	dialogs, err := c.dialogs(ctx)
	if err != nil {
		return err
	}
	total := 0
	for _, d := range dialogs {
		k := EntityKind(d.Entity)
		if k == "channel" || k == "bot" {
			continue
		}
		db.Exec(b.q(), "INSERT INTO chat (id, kind, title, archived, json) VALUES (?, ?, ?, ?, ?) "+
			"ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, title = excluded.title, "+
			"archived = excluded.archived, json = excluded.json",
			d.ID, k, d.Name, boolInt(d.Archived), Dump(d.Entity.(tdp.Object)))
		noteRead(b.q(), d.ID, intp(d.Dialog.ReadInboxMaxID), intp(d.Dialog.ReadOutboxMaxID), false)
		last := db.Int(b.q(), "SELECT max(id) FROM message WHERE chat_id = ?", d.ID)
		peer, err := dialogPeer(d)
		if err != nil {
			return err
		}
		label := d.Name
		if label == "" {
			label = strconv.FormatInt(d.ID, 10)
		}
		n := 0
		// nothing held while Telegram is asked (a flood wait can last minutes): the live connection
		// writes to the same store meanwhile
		if err := b.commit(); err != nil {
			return err
		}
		err = c.history(ctx, peer, int(last), func(chunk []sent) error {
			for _, s := range chunk {
				db.Exec(b.q(), "INSERT OR IGNORE INTO message (chat_id, id, date, json) VALUES (?, ?, ?, ?)",
					d.ID, s.Message.GetID(), messageDate(s.Message), Dump(s.Message.(tdp.Object)))
				if isEntity(s.Sender) {
					db.Exec(b.q(), "INSERT OR REPLACE INTO entity VALUES (?, ?)", EntityID(s.Sender), Dump(s.Sender.(tdp.Object)))
				}
				n++
				if n%1000 == 0 {
					fmt.Fprintf(out, "\r%s: %d", label, n)
				}
			}
			return b.commit()
		})
		if err != nil {
			return err
		}
		db.Exec(b.q(), "UPDATE chat SET synced_at = ? WHERE id = ?", time.Now().Unix(), d.ID)
		if err := b.commit(); err != nil {
			return err
		}
		total += n
		fmt.Fprintf(out, "\r%-10s %7d %s  %s\n", k, n, out.say("new", nil), label)
	}
	fmt.Fprintln(out, out.say("{n} new messages in {db}", map[string]any{"n": total, "db": DBPath()}))
	return nil
}
