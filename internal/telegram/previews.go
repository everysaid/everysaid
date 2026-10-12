package telegram

// The pictures of link previews. Telegram gives a link's preview with its message (media.webpage,
// kept whole in the store: the importer takes its words from there), its picture a photo of its
// own: downloaded here by the live connection (a size of at most previewSide px, one at a time with
// a pause between) into telegram_webpage_photo, for the importer to give the archive's preview;
// never as the message's file. Messages the store no longer keeps (taken out once a week old) that
// the archive has as links without a picture, and that were never looked at (those of before there
// were previews), are fetched again from Telegram, a few hundred a round, so that the import gives
// them their preview and the next round their picture.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

var (
	previewScan  = 10 * time.Minute // the stored messages looked over (and at each new one with a preview)
	previewPause = 2 * time.Second  // between two downloads, two fetches of old messages
	previewRetry = 24 * time.Hour   // a download that failed tried again after
	previewBatch = 300              // old messages fetched again in a round
	previewGet   = downloadPreview  // a preview's picture (a test's own)
)

// previewSide is the longest side of the size of a preview's picture downloaded.
const previewSide = 800

// webpagePhoto is the photo of a message's link preview, nil if none.
func webpagePhoto(m tg.MessageClass) *tg.Photo {
	msg, ok := m.(*tg.Message)
	if !ok {
		return nil
	}
	media, ok := msg.Media.(*tg.MessageMediaWebPage)
	if !ok {
		return nil
	}
	w, ok := media.Webpage.(*tg.WebPage)
	if !ok {
		return nil
	}
	p, _ := w.Photo.(*tg.Photo)
	return p
}

// previewSize is the size of a photo downloaded for a preview: the largest of at most previewSide
// px, else the smallest; its type, or its bytes where the photo carries them; "" and nil if none
// (an inline thumbnail only, too small to show).
func previewSize(p *tg.Photo) (string, []byte) {
	type size struct {
		typ   string
		side  int
		bytes []byte
	}
	var sizes []size
	for _, s := range p.Sizes {
		switch s := s.(type) {
		case *tg.PhotoSize:
			sizes = append(sizes, size{s.Type, max(s.W, s.H), nil})
		case *tg.PhotoSizeProgressive:
			sizes = append(sizes, size{s.Type, max(s.W, s.H), nil})
		case *tg.PhotoCachedSize:
			sizes = append(sizes, size{s.Type, max(s.W, s.H), s.Bytes})
		}
	}
	if len(sizes) == 0 {
		return "", nil
	}
	sort.SliceStable(sizes, func(i, j int) bool { return sizes[i].side < sizes[j].side })
	best := sizes[0]
	for _, s := range sizes {
		if s.side <= previewSide {
			best = s
		}
	}
	return best.typ, best.bytes
}

// downloadPreview is a preview's picture, the size previewSize chooses; nil if it has none.
func downloadPreview(ctx context.Context, api *tg.Client, p *tg.Photo) ([]byte, error) {
	typ, inline := previewSize(p)
	if inline != nil || typ == "" {
		return inline, nil
	}
	loc := &tg.InputPhotoFileLocation{ID: p.ID, AccessHash: p.AccessHash, FileReference: p.FileReference, ThumbSize: typ}
	pr, pw := io.Pipe()
	go func() {
		_, err := downloader.NewDownloader().Download(api, loc).Stream(ctx, pw)
		pw.CloseWithError(err)
	}()
	return io.ReadAll(io.LimitReader(pr, 10<<20))
}

// notePreview records a message's preview picture as looked at: its photo (0: none), the picture,
// or that it failed.
func notePreview(q db.Querier, chat int64, id int, photoID int64, data []byte, failed bool) {
	var at any
	if failed {
		at = time.Now().Unix()
	}
	db.Exec(q, "INSERT INTO telegram_webpage_photo (chat_id, id, photo_id, data, failed_at) VALUES (?, ?, ?, ?, ?) "+
		"ON CONFLICT (chat_id, id) DO UPDATE SET photo_id = excluded.photo_id, data = excluded.data, failed_at = excluded.failed_at",
		chat, id, photoID, data, at)
}

// scanPreviews looks over the stored messages with a link preview: those whose preview has no
// picture are marked so; it gives those whose picture is to be downloaded (not yet, or failed a
// while ago), by chat, with the photo the store says. A preview Telegram has not made yet
// (WebPagePending) waits for the edit that brings it, a day at most.
func scanPreviews(store *sql.DB, skip map[int64]bool, now int64) (todo map[int64]map[int]int64, err error) {
	defer db.Recover(&err)
	type row struct {
		chat     int64
		id       int
		date     int64
		class    sql.NullString
		want     int64
		had      sql.NullInt64
		data     bool
		failedAt sql.NullInt64
	}
	var rows []row
	db.Each(store, "SELECT m.chat_id, m.id, m.date, json_extract(m.json, '$.media.webpage._'), "+
		"CASE WHEN json_extract(m.json, '$.media.webpage.photo._') = 'Photo' THEN coalesce(json_extract(m.json, '$.media.webpage.photo.id'), 0) ELSE 0 END, "+
		"w.photo_id, w.data IS NOT NULL, w.failed_at FROM telegram_message m "+
		"LEFT JOIN telegram_webpage_photo w ON w.chat_id = m.chat_id AND w.id = m.id "+
		"WHERE json_extract(m.json, '$.media._') = 'MessageMediaWebPage' "+
		"AND NOT EXISTS (SELECT 1 FROM telegram_deleted d WHERE d.chat_id = m.chat_id AND d.id = m.id)", nil,
		func(scan func(...any)) {
			var r row
			scan(&r.chat, &r.id, &r.date, &r.class, &r.want, &r.had, &r.data, &r.failedAt)
			rows = append(rows, r)
		})
	todo = map[int64]map[int]int64{}
	for _, r := range rows {
		if skip[r.chat] {
			continue
		}
		want := r.want
		if r.class.String == "WebPagePending" {
			if r.date > now-int64(24*time.Hour/time.Second) {
				continue
			}
			want = 0 // never made
		}
		if r.had.Valid && r.had.Int64 == want {
			if want == 0 || r.data || !r.failedAt.Valid || r.failedAt.Int64 > now-int64(previewRetry/time.Second) {
				continue // looked at: none, downloaded, or failed lately
			}
		}
		if want == 0 {
			notePreview(store, r.chat, r.id, 0, nil, false)
			continue
		}
		if todo[r.chat] == nil {
			todo[r.chat] = map[int]int64{}
		}
		todo[r.chat][r.id] = want
	}
	return todo, nil
}

// oldLinks are the Telegram messages the archive has as links with no preview picture that the
// store neither keeps nor has looked at, newest first, at most previewBatch, by chat; none where
// the archive has no previews yet.
func oldLinks(c *plugins.Context, store *sql.DB, skip map[int64]bool) (out map[int64][]int, err error) {
	defer db.Recover(&err)
	type key struct {
		chat int64
		id   int
	}
	var all []key
	db.Each(c.Store().Read(), "SELECT cv.key, m.key FROM message m JOIN service s ON s.id = m.service_id "+
		"JOIN conversation cv ON cv.id = m.conversation_id WHERE s.name = 'telegram' AND m.subtype = 'link' "+
		"AND m.key IS NOT NULL AND NOT m.deleted "+
		"AND NOT EXISTS (SELECT 1 FROM link_preview p WHERE p.message_id = m.id AND p.image IS NOT NULL) ORDER BY m.ts DESC",
		nil, func(scan func(...any)) {
			var chat, id string
			scan(&chat, &id)
			ch, err1 := strconv.ParseInt(chat, 10, 64)
			i, err2 := strconv.Atoi(id)
			if err1 == nil && err2 == nil && !skip[ch] {
				all = append(all, key{ch, i})
			}
		})
	out = map[int64][]int{}
	n := 0
	for _, k := range all {
		if n >= previewBatch {
			break
		}
		if db.Exists(store, "SELECT 1 FROM telegram_message WHERE chat_id = ? AND id = ?", k.chat, k.id) ||
			db.Exists(store, "SELECT 1 FROM telegram_webpage_photo WHERE chat_id = ? AND id = ?", k.chat, k.id) {
			continue
		}
		out[k.chat] = append(out[k.chat], k.id)
		n++
	}
	return out, nil
}

// previews keeps the previews' pictures while the connection lasts: a round every previewScan, or
// when wake says a message with one came; old messages are fetched again until none is left.
func previews(ctx context.Context, c *plugins.Context, cn *conn, wake <-chan struct{}) {
	old := true
	for {
		left, err := previewRound(ctx, c, cn, old)
		if err != nil && ctx.Err() == nil {
			c.Log("error: {e}", map[string]any{"e": err.Error()})
		}
		if err == nil {
			old = left
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-time.After(previewScan):
		}
	}
}

// pause waits previewPause; false when ctx ended.
func pause(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(previewPause):
		return true
	}
}

// previewRound is one look: old messages fetched again (when old), then the pictures downloaded,
// and the messages that have theirs imported again. It says whether old messages may be left.
func previewRound(ctx context.Context, c *plugins.Context, cn *conn, old bool) (left bool, err error) {
	defer db.Recover(&err)
	skip := idSet(currentSettings(c)["skip_chats"])
	store, err := openStore(DBPath())
	if err != nil {
		return old, err
	}
	defer store.Close()
	if old {
		if left, err = previewOld(ctx, c, cn, store, skip); err != nil {
			return true, err
		}
	}
	todo, err := scanPreviews(store, skip, time.Now().Unix())
	if err != nil {
		return left, err
	}
	keys := map[[2]int64]bool{}
	defer func() { // what was downloaded goes into the archive, even when the round stopped
		if len(keys) == 0 {
			return
		}
		_, _, ierr := sourcekit.RunImporters(c, []sourcekit.Step{{Label: "Telegram live",
			Run: func(a *archive.Archive, out func(string)) error { return importTelegram(a, out, keys, nil) }}})
		if ierr != nil && err == nil {
			err = ierr
		} else if ierr == nil {
			c.Emit(M{"type": "changed"})
		}
	}()
	chats := make([]int64, 0, len(todo))
	for chat := range todo {
		chats = append(chats, chat)
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
	first := true
	for _, chat := range chats {
		ids := make([]int, 0, len(todo[chat]))
		for id := range todo[chat] {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		failAll := func(ids []int) {
			for _, id := range ids {
				notePreview(store, chat, id, todo[chat][id], nil, true)
			}
		}
		peer, err := cn.peer(ctx, chat)
		if err != nil {
			if stopsAll(ctx, err) {
				return left, err
			}
			failAll(ids)
			continue
		}
		for i := 0; i < len(ids); i += 100 { // fetched again: the file references expire
			part := ids[i:min(i+100, len(ids))]
			msgs, err := cn.byIDs(ctx, peer, part)
			if err != nil {
				if stopsAll(ctx, err) {
					return left, err
				}
				failAll(part)
				continue
			}
			for j, m := range msgs {
				id := part[j]
				p := webpagePhoto(m)
				if p == nil { // gone, or no longer with it: again once the store has it as it is now
					notePreview(store, chat, id, todo[chat][id], nil, true)
					continue
				}
				if !first && !pause(ctx) {
					return left, nil
				}
				first = false
				data, err := previewGet(ctx, cn.api, p)
				if d, flood := tgerr.AsFloodWait(err); flood { // asked to wait: the rest at the next round
					return left, fmt.Errorf("FLOOD_WAIT %s", d)
				}
				if ctx.Err() != nil {
					return left, nil
				}
				if err != nil || len(data) == 0 {
					notePreview(store, chat, id, todo[chat][id], nil, true)
					continue
				}
				notePreview(store, chat, id, todo[chat][id], data, false)
				keys[[2]int64{chat, int64(id)}] = true
			}
		}
	}
	return left, nil
}

// previewOld fetches again the old messages oldLinks gives (refetch stores and imports them, with
// their preview's words); those Telegram no longer gives are marked looked at. It says whether
// more may be left.
func previewOld(ctx context.Context, c *plugins.Context, cn *conn, store *sql.DB, skip map[int64]bool) (bool, error) {
	todo, err := oldLinks(c, store, skip)
	if err != nil {
		return true, err
	}
	n := 0
	chats := make([]int64, 0, len(todo))
	for chat, ids := range todo {
		chats = append(chats, chat)
		n += len(ids)
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
	first := true
	for _, chat := range chats {
		ids := todo[chat]
		sort.Ints(ids)
		for i := 0; i < len(ids); i += 100 {
			part := ids[i:min(i+100, len(ids))]
			if !first && !pause(ctx) {
				return true, nil
			}
			first = false
			if err := refetch(ctx, c, cn, chat, part); err != nil && stopsAll(ctx, err) {
				return true, err
			}
			// not given (gone, a chat no longer reachable): looked at, with no picture
			for _, id := range part {
				if !db.Exists(store, "SELECT 1 FROM telegram_message WHERE chat_id = ? AND id = ?", chat, id) {
					notePreview(store, chat, id, 0, nil, false)
				}
			}
		}
	}
	return n >= previewBatch, nil
}
