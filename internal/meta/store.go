package meta

// What the Meta sources keep, in keys.db (internal/keys) in tables of the prefix meta_: each
// instance's session (the cookies, which are the account; messagix's state for reconnecting; the
// number of its end-to-end encryption device, whose keys are whatsmeow's in keys.db too), what
// it learned of the threads (their kind, and an encrypted group's id), and the files still to
// download (their addresses expire), until the archive has them: with them the pictures of links'
// previews, which are not files of their messages.

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/db"
	"everysaid/internal/keys"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta_session (     -- an instance's account: what logs it in
    instance INTEGER PRIMARY KEY,              -- the plugin instance
    platform TEXT NOT NULL,                    -- messagix's platform: facebook, messenger, instagram
    user_id TEXT NOT NULL,                     -- the account's id
    name TEXT,                                 -- its name, as Meta gives it
    cookies TEXT NOT NULL,                     -- the browser's cookies (JSON), as they are now
    state TEXT,                                -- messagix's state, to connect again without loading the page
    wa_device INTEGER,                         -- its encryption device (whatsmeow_device <user_id>.<n>@msgr)
    seq INTEGER,                               -- Instagram: the last change the archive has (its sequence number)
    seq_at INTEGER,                            -- and when it was had, Unix ms
    saved_at INTEGER NOT NULL                  -- Unix s
);
CREATE TABLE IF NOT EXISTS meta_thread (      -- what the source learned of a thread
    instance INTEGER NOT NULL,
    key TEXT NOT NULL,                         -- the archive's conversation key
    type INTEGER NOT NULL,                     -- Meta's thread type (15, 16: end-to-end encrypted)
    fb_key TEXT,                               -- an encrypted group's Facebook thread key
    ig_id TEXT,                                -- Instagram: the thread's short id (its changes name it so)
    ig_thread TEXT,                            -- and its long one
    PRIMARY KEY (instance, key)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS meta_ig_reaction (  -- Instagram: whose reaction on which message an id is
    instance INTEGER NOT NULL,                 -- (a reaction taken away names only its id)
    log_id TEXT NOT NULL,
    thread TEXT NOT NULL,
    message TEXT NOT NULL,
    sender TEXT NOT NULL,
    PRIMARY KEY (instance, log_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS meta_file (        -- a message's file still to download
    instance INTEGER NOT NULL,
    thread TEXT NOT NULL,
    message TEXT NOT NULL,                     -- the message's id
    n INTEGER NOT NULL,                        -- its place among the message's files (-1: its link preview's picture)
    url TEXT,                                  -- where it is (it expires)
    wa BLOB,                                   -- or an encrypted chat's file: its transport (JSON)
    name TEXT,                                 -- the name it was sent with (a preview's picture: the link)
    mime TEXT,
    at INTEGER NOT NULL,                       -- when it arrived, Unix s
    tries INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (instance, message, n)
) WITHOUT ROWID;
`

// keysDB is keys.db with the tables of the prefix meta_ (and whatsmeow's) made. It is shared: never
// closed here.
var keysDB = func() (*sql.DB, error) {
	d, err := keys.Open()
	if err != nil {
		return nil, err
	}
	if _, err := d.Exec(schema); err != nil {
		return nil, err
	}
	return d, nil
}

// devices is whatsmeow's container over keys.db (the encrypted chats' device keys), its tables up
// to date.
func devices(ctx context.Context) (*sqlstore.Container, error) {
	d, err := keysDB()
	if err != nil {
		return nil, err
	}
	c := sqlstore.NewWithDB(d, keys.Driver, waLog.Noop)
	return c, c.Upgrade(ctx)
}

// session is an instance's account.
type session struct {
	Platform string
	UserID   string
	Name     string
	Cookies  json.RawMessage
	State    json.RawMessage
	WADevice uint16
	Seq      int64 // Instagram's sequence number of the last change had, and when (Unix ms)
	SeqAt    int64
}

// loadSession is an instance's session (nil: not logged in).
func loadSession(iid int64) (*session, error) {
	d, err := keysDB()
	if err != nil {
		return nil, err
	}
	var s session
	var name, state sql.NullString
	var cookies string
	var dev, seq, seqAt sql.NullInt64
	err = d.QueryRow("SELECT platform, user_id, name, cookies, state, wa_device, seq, seq_at FROM meta_session WHERE instance = ?", iid).
		Scan(&s.Platform, &s.UserID, &name, &cookies, &state, &dev, &seq, &seqAt)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	s.Name, s.Cookies, s.WADevice = name.String, json.RawMessage(cookies), uint16(dev.Int64)
	s.Seq, s.SeqAt = seq.Int64, seqAt.Int64
	if state.Valid && state.String != "" {
		s.State = json.RawMessage(state.String)
	}
	return &s, nil
}

// saveSession keeps an instance's session.
func saveSession(iid int64, s *session) error {
	d, err := keysDB()
	if err != nil {
		return err
	}
	var state any
	if len(s.State) > 0 {
		state = string(s.State)
	}
	var dev any
	if s.WADevice != 0 {
		dev = int64(s.WADevice)
	}
	var seq, seqAt any
	if s.Seq != 0 {
		seq, seqAt = s.Seq, s.SeqAt
	}
	_, err = d.Exec("INSERT INTO meta_session (instance, platform, user_id, name, cookies, state, wa_device, seq, seq_at, saved_at) "+
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (instance) DO UPDATE SET platform = excluded.platform, "+
		"user_id = excluded.user_id, name = excluded.name, cookies = excluded.cookies, state = excluded.state, "+
		"wa_device = excluded.wa_device, seq = excluded.seq, seq_at = excluded.seq_at, saved_at = excluded.saved_at",
		iid, s.Platform, s.UserID, s.Name, string(s.Cookies), state, dev, seq, seqAt, time.Now().Unix())
	return err
}

// forgetSession takes an instance's account away (logged out, or the instance removed): its session,
// what it learned of threads, the files it had still to download.
func forgetSession(iid int64) error {
	d, err := keysDB()
	if err != nil {
		return err
	}
	for _, t := range []string{"meta_session", "meta_thread", "meta_file", "meta_ig_reaction"} {
		if _, err := d.Exec("DELETE FROM "+t+" WHERE instance = ?", iid); err != nil {
			return err
		}
	}
	return nil
}

// threadInfo is what the source learned of a thread.
type threadInfo struct {
	Type           int64
	FBKey          string
	IGID, IGThread string // Instagram's short and long ids of the thread
}

func loadThreads(iid int64) (map[string]threadInfo, error) {
	d, err := keysDB()
	if err != nil {
		return nil, err
	}
	out := map[string]threadInfo{}
	err = func() (err error) {
		defer db.Recover(&err)
		db.Each(d, "SELECT key, type, coalesce(fb_key, ''), coalesce(ig_id, ''), coalesce(ig_thread, '') FROM meta_thread "+
			"WHERE instance = ?", []any{iid},
			func(scan func(...any)) {
				var k string
				var t threadInfo
				scan(&k, &t.Type, &t.FBKey, &t.IGID, &t.IGThread)
				out[k] = t
			})
		return nil
	}()
	return out, err
}

func saveThread(iid int64, key string, t threadInfo) error {
	d, err := keysDB()
	if err != nil {
		return err
	}
	_, err = d.Exec("INSERT INTO meta_thread (instance, key, type, fb_key, ig_id, ig_thread) VALUES (?, ?, ?, ?, ?, ?) "+
		"ON CONFLICT DO UPDATE SET type = excluded.type, fb_key = coalesce(excluded.fb_key, meta_thread.fb_key), "+
		"ig_id = coalesce(excluded.ig_id, meta_thread.ig_id), ig_thread = coalesce(excluded.ig_thread, meta_thread.ig_thread)",
		iid, key, t.Type, nullStr(t.FBKey), nullStr(t.IGID), nullStr(t.IGThread))
	return err
}

// previewN is the place (pendingFile.N) of a message's link preview picture, which is not one of its
// files: downloaded as they are, into the preview (link_preview).
const previewN = -1

// pendingFile is a message's file still to download (or its link preview's picture: N previewN,
// Name the link).
type pendingFile struct {
	Thread, Message string
	N               int
	URL             string
	WA              []byte
	Name, Mime      string
	At              int64
	Tries           int
}

func queueFiles(iid int64, files []pendingFile) error {
	if len(files) == 0 {
		return nil
	}
	d, err := keysDB()
	if err != nil {
		return err
	}
	for _, f := range files {
		if _, err := d.Exec("INSERT OR IGNORE INTO meta_file (instance, thread, message, n, url, wa, name, mime, at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", iid, f.Thread, f.Message, f.N, nullStr(f.URL), f.WA, nullStr(f.Name),
			nullStr(f.Mime), f.At); err != nil {
			return err
		}
	}
	return nil
}

// pendingFiles are the files still to download, of one message where it is given.
func pendingFiles(iid int64, message string) ([]pendingFile, error) {
	d, err := keysDB()
	if err != nil {
		return nil, err
	}
	q := "SELECT thread, message, n, coalesce(url, ''), wa, coalesce(name, ''), coalesce(mime, ''), at, tries FROM meta_file WHERE instance = ?"
	args := []any{iid}
	if message != "" {
		q += " AND message = ?"
		args = append(args, message)
	}
	var out []pendingFile
	err = func() (err error) {
		defer db.Recover(&err)
		db.Each(d, q+" ORDER BY at, message, n", args, func(scan func(...any)) {
			var f pendingFile
			scan(&f.Thread, &f.Message, &f.N, &f.URL, &f.WA, &f.Name, &f.Mime, &f.At, &f.Tries)
			out = append(out, f)
		})
		return nil
	}()
	return out, err
}

// fileDone takes a file off the list (downloaded and in the archive, or given up).
func fileDone(iid int64, f pendingFile) error {
	d, err := keysDB()
	if err != nil {
		return err
	}
	_, err = d.Exec("DELETE FROM meta_file WHERE instance = ? AND message = ? AND n = ?", iid, f.Message, f.N)
	return err
}

func fileTried(iid int64, f pendingFile) error {
	d, err := keysDB()
	if err != nil {
		return err
	}
	_, err = d.Exec("UPDATE meta_file SET tries = tries + 1 WHERE instance = ? AND message = ? AND n = ?", iid, f.Message, f.N)
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// noteReaction keeps whose an Instagram reaction is, for when it is taken away by its id alone.
func noteReaction(iid int64, logID, thread, message, sender string) error {
	if logID == "" {
		return nil
	}
	d, err := keysDB()
	if err != nil {
		return err
	}
	_, err = d.Exec("INSERT OR REPLACE INTO meta_ig_reaction (instance, log_id, thread, message, sender) VALUES (?, ?, ?, ?, ?)",
		iid, logID, thread, message, sender)
	return err
}

// reactionOf is whose a reaction is, and on which message ("" when not known); the record goes.
func reactionOf(iid int64, logID string) (thread, message, sender string) {
	d, err := keysDB()
	if err != nil || logID == "" {
		return "", "", ""
	}
	if d.QueryRow("SELECT thread, message, sender FROM meta_ig_reaction WHERE instance = ? AND log_id = ?", iid, logID).
		Scan(&thread, &message, &sender) == nil {
		d.Exec("DELETE FROM meta_ig_reaction WHERE instance = ? AND log_id = ?", iid, logID)
	}
	return
}

// saveSeq keeps how far Instagram's changes were had.
func saveSeq(iid, seq, at int64) error {
	d, err := keysDB()
	if err != nil {
		return err
	}
	_, err = d.Exec("UPDATE meta_session SET seq = ?, seq_at = ? WHERE instance = ?", seq, at, iid)
	return err
}
