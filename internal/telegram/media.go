// The pictures, videos and voice messages of the stored messages (scripts/telegram-sync.py's
// wanted and media), downloaded as Telethon's download_media did.
package telegram

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

// wanted says whether a stored message carries a file that is downloaded (a picture, or any
// document: video, voice, sticker, a file sent as such), and its size.
func wanted(m map[string]any) (bool, int64) {
	media, _ := m["media"].(map[string]any)
	class, _ := media["_"].(string)
	if photo, ok := media["photo"].(map[string]any); class == "MessageMediaPhoto" && ok && truthy(photo) {
		var best int64
		sizes, _ := photo["sizes"].([]any)
		for _, s := range sizes {
			s, _ := s.(map[string]any)
			n := num(s["size"])
			if n == 0 {
				list, _ := s["sizes"].([]any)
				for _, x := range list {
					n = max(n, num(x))
				}
			}
			best = max(best, n)
		}
		return true, best
	}
	if doc, _ := media["document"].(map[string]any); class == "MessageMediaDocument" && truthy(doc) {
		return true, num(doc["size"])
	}
	return false, 0
}

// carries says whether a message just received has a file wanted would choose (not a link's
// preview).
func carries(m tg.MessageClass) bool {
	msg, ok := m.(*tg.Message)
	if !ok {
		return false
	}
	switch media := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		_, ok := media.Photo.(*tg.Photo)
		return ok
	case *tg.MessageMediaDocument:
		_, ok := media.Document.(*tg.Document)
		return ok
	}
	return false
}

func truthy(m map[string]any) bool { return len(m) > 0 }

func num(v any) int64 {
	switch v := v.(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

// fileOf is Telethon's Message.file: the photo or document a message carries (also in a link's
// preview), nil and nil if none.
func fileOf(m tg.MessageClass) (*tg.Photo, *tg.Document) {
	msg, ok := m.(*tg.Message)
	if !ok {
		if s, ok := m.(*tg.MessageService); ok {
			if a, ok := s.Action.(*tg.MessageActionChatEditPhoto); ok {
				p, _ := a.Photo.(*tg.Photo)
				return p, nil
			}
		}
		return nil, nil
	}
	switch media := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		p, _ := media.Photo.(*tg.Photo)
		return p, nil
	case *tg.MessageMediaDocument:
		d, _ := media.Document.(*tg.Document)
		return nil, d
	case *tg.MessageMediaWebPage:
		if w, ok := media.Webpage.(*tg.WebPage); ok {
			p, _ := w.Photo.(*tg.Photo)
			d, _ := w.Document.(*tg.Document)
			if p != nil || d != nil {
				return p, d
			}
		}
	}
	return nil, nil
}

// mimeExt is Python's mimetypes.guess_extension for the types a media file has (its own table, as
// Python has one built in).
var mimeExt = map[string]string{
	"audio/3gpp": ".3gp", "audio/3gpp2": ".3g2", "audio/aac": ".aac", "audio/basic": ".au", "audio/flac": ".flac",
	"audio/matroska": ".mka", "audio/mp4": ".m4a", "audio/mpeg": ".mp3", "audio/ogg": ".ogg", "audio/opus": ".opus",
	"audio/vnd.wave": ".wav", "audio/webm": ".weba", "audio/x-aiff": ".aif", "audio/x-pn-realaudio": ".ra",
	"audio/x-wav": ".wav", "image/avif": ".avif", "image/bmp": ".bmp", "image/gif": ".gif", "image/heic": ".heic",
	"image/heif": ".heif", "image/jp2": ".jp2", "image/jpeg": ".jpg", "image/png": ".png", "image/svg+xml": ".svg",
	"image/tiff": ".tiff", "image/vnd.microsoft.icon": ".ico", "image/webp": ".webp", "image/x-ms-bmp": ".bmp",
	"video/matroska": ".mkv", "video/x-matroska": ".mkv", "video/mp4": ".mp4", "video/mpeg": ".mpeg",
	"video/ogg": ".ogv", "video/quicktime": ".mov", "video/vnd.avi": ".avi", "video/x-msvideo": ".avi",
	"video/webm": ".webm", "video/x-m4v": ".m4v", "video/x-ms-wmv": ".wmv", "video/3gpp": ".3gp",
	"application/octet-stream": ".bin", "application/pdf": ".pdf", "application/zip": ".zip", "application/json": ".json", "text/plain": ".txt",
}

// fileExt is Telethon's File.ext: from the mime type, else from the file's name, else "".
func fileExt(p *tg.Photo, d *tg.Document) string {
	if p != nil {
		return ".jpg"
	}
	if d == nil {
		return ""
	}
	if e := mimeExt[strings.ToLower(d.MimeType)]; e != "" {
		return e
	}
	for _, a := range d.Attributes {
		if f, ok := a.(*tg.DocumentAttributeFilename); ok {
			// the sender's name for it: only an extension made of letters and digits becomes part of
			// the path (not ":", "?", a control character, which some file systems refuse or read
			// otherwise)
			if ext := path.Ext(strings.ReplaceAll(f.FileName, `\`, "/")); plainExt.MatchString(ext) {
				return ext
			}
			return ""
		}
	}
	return ""
}

var plainExt = regexp.MustCompile(`^\.[A-Za-z0-9]{1,16}$`)

// strippedHeader is the JPEG header Telegram strips from inline thumbnails (Telethon's
// stripped_photo_to_jpg, from Telegram Desktop).
var strippedHeader, _ = hex.DecodeString("ffd8ffe000104a46494600010100000100010000ffdb004300281c1e231e19282321232d2b28303c64413c37373c7b585d4964918099968f808c8aa0b4e6c3a0aadaad8a8cc8ffcbdaeef5ffffff9bc1fffffffaffe6fdfff8ffdb0043012b2d2d3c353c76414176f8a58ca5f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8ffc00011080000000003012200021101031101ffc4001f0000010501010101010100000000000000000102030405060708090a0bffc400b5100002010303020403050504040000017d01020300041105122131410613516107227114328191a1082342b1c11552d1f02433627282090a161718191a25262728292a3435363738393a434445464748494a535455565758595a636465666768696a737475767778797a838485868788898a92939495969798999aa2a3a4a5a6a7a8a9aab2b3b4b5b6b7b8b9bac2c3c4c5c6c7c8c9cad2d3d4d5d6d7d8d9dae1e2e3e4e5e6e7e8e9eaf1f2f3f4f5f6f7f8f9faffc4001f0100030101010101010101010000000000000102030405060708090a0bffc400b51100020102040403040705040400010277000102031104052131061241510761711322328108144291a1b1c109233352f0156272d10a162434e125f11718191a262728292a35363738393a434445464748494a535455565758595a636465666768696a737475767778797a82838485868788898a92939495969798999aa2a3a4a5a6a7a8a9aab2b3b4b5b6b7b8b9bac2c3c4c5c6c7c8c9cad2d3d4d5d6d7d8d9dae2e3e4e5e6e7e8e9eaf2f3f4f5f6f7f8f9faffda000c03010002110311003f00")

func strippedToJPEG(b []byte) []byte {
	if len(b) < 3 || b[0] != 1 {
		return b
	}
	out := append([]byte{}, strippedHeader...)
	out[164], out[166] = b[1], b[2]
	out = append(out, b[3:]...)
	return append(out, 0xff, 0xd9)
}

// largest is Telethon's _get_thumb(sizes, None): the biggest size (a video one before any picture),
// never an outline (PhotoPathSize); nil if none.
func largest(sizes []any) any {
	key := func(s any) (int, int) {
		switch s := s.(type) {
		case *tg.PhotoStrippedSize:
			return 1, len(s.Bytes)
		case *tg.PhotoCachedSize:
			return 1, len(s.Bytes)
		case *tg.PhotoSize:
			return 1, s.Size
		case *tg.PhotoSizeProgressive:
			n := 0
			for _, x := range s.Sizes {
				n = max(n, x)
			}
			return 1, n
		case *tg.VideoSize:
			return 2, s.Size
		}
		return 0, 0
	}
	sort.SliceStable(sizes, func(i, j int) bool {
		a1, a2 := key(sizes[i])
		b1, b2 := key(sizes[j])
		return a1 < b1 || (a1 == b1 && a2 < b2)
	})
	var kept []any
	for _, s := range sizes {
		if _, outline := s.(*tg.PhotoPathSize); !outline {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept[len(kept)-1]
}

// download is download_media(m, file=dest): false when there was nothing to write.
func download(ctx context.Context, api *tg.Client, m tg.MessageClass, dest string) (bool, error) {
	photo, doc := fileOf(m)
	var loc tg.InputFileLocationClass
	switch {
	case photo != nil:
		var sizes []any
		for _, s := range photo.Sizes {
			sizes = append(sizes, s)
		}
		for _, s := range photo.VideoSizes {
			sizes = append(sizes, s)
		}
		var typ string
		switch s := largest(sizes).(type) {
		case nil, *tg.PhotoSizeEmpty:
			return false, nil
		case *tg.PhotoStrippedSize:
			return true, os.WriteFile(dest, strippedToJPEG(s.Bytes), 0o600)
		case *tg.PhotoCachedSize:
			return true, os.WriteFile(dest, s.Bytes, 0o600)
		case *tg.PhotoSize:
			typ = s.Type
		case *tg.PhotoSizeProgressive:
			typ = s.Type
		case *tg.VideoSize:
			typ = s.Type
		default:
			return false, nil
		}
		loc = &tg.InputPhotoFileLocation{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: typ}
	case doc != nil:
		loc = &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
	default:
		return false, nil
	}
	_, err := downloader.NewDownloader().Download(api, loc).ToPath(ctx, dest)
	return err == nil, err
}

// mediaSel is which stored messages' files a media run downloads.
type mediaSel struct {
	only  map[int64]bool // these chats only (nil: all)
	skip  map[int64]bool // not these
	since int64          // sent since (Unix seconds; 0: all)
}

// explicit: the app's choice (some chats, or the recent days); then config's `media = false` and
// `no_media` do not apply.
func (s mediaSel) explicit() bool { return s.only != nil || s.since != 0 }

// mediaRun is the sync's --media: the files of the stored messages not downloaded yet, of the
// messages sel chooses. c nil: dryRun only.
func mediaRun(ctx context.Context, c *conn, dryRun bool, sel mediaSel, out *printer) error {
	if !sel.explicit() && !config.Bool("telegram", "media", true) {
		return fmt.Errorf("%s", out.say("[telegram] media = false in config: no media are downloaded", nil))
	}
	store, err := openStore(DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	skip := map[int64]bool{}
	for id := range sel.skip {
		skip[id] = true
	}
	if !sel.explicit() {
		if vs, ok := config.Get("telegram", "no_media").([]any); ok {
			for _, v := range vs {
				if n, err := strconv.ParseInt(fmt.Sprint(v), 10, 64); err == nil {
					skip[n] = true
				}
			}
		}
	}
	todo := map[int64][]int{}
	var order []int64
	var size int64
	db.Each(store, "SELECT chat_id, id, json FROM message WHERE file IS NULL AND date >= ?", []any{sel.since}, func(scan func(...any)) {
		var chatID int64
		var id int
		var js string
		scan(&chatID, &id, &js)
		if skip[chatID] || (sel.only != nil && !sel.only[chatID]) {
			return
		}
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(js))
		dec.UseNumber()
		dec.Decode(&m)
		if ok, n := wanted(m); ok {
			if _, had := todo[chatID]; !had {
				order = append(order, chatID)
			}
			todo[chatID] = append(todo[chatID], id)
			size += n
		}
	})
	total := 0
	for _, ids := range todo {
		total += len(ids)
	}
	fmt.Fprintln(out, out.say("{n} files, {gb} GB, in {chats} chats",
		map[string]any{"n": total, "gb": fmt.Sprintf("%.2f", float64(size)/1e9), "chats": len(todo)}))
	if dryRun || total == 0 {
		return nil
	}
	if _, err := c.dialogs(ctx); err != nil { // the chats' access hashes: a session keeps none
		return err
	}
	done := 0
	for _, chatID := range order {
		err := fetchFiles(ctx, c, store, chatID, todo[chatID], func() {
			done++
			fmt.Fprintf(out, "\r%d/%d", done, total)
		}, func(rel string, err error) {
			fmt.Fprintf(out, "\n%s\n", out.say("{file}: not downloaded ({e})", map[string]any{"file": rel, "e": err.Error()}))
		})
		if err != nil {
			return err
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, out.say("{n} downloaded into {folder}", map[string]any{"n": done, "folder": MediaPath()}))
	return nil
}

// fetchFiles downloads the files of a chat's messages into the media folder and records each in
// the store (telegram.db); wrote is told of each file written, failed of each one Telegram would
// not give, which does not keep the others back (it is tried again another time). A flood wait or
// the end of ctx stops it.
func fetchFiles(ctx context.Context, c *conn, store *sql.DB, chatID int64, ids []int, wrote func(),
	failed func(rel string, err error)) error {
	peer, err := c.peer(ctx, chatID)
	if err != nil {
		return err
	}
	folder := filepath.Join(MediaPath(), strconv.FormatInt(chatID, 10))
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	for i := 0; i < len(ids); i += 100 { // fetched again: the file references expire
		msgs, err := c.byIDs(ctx, peer, ids[i:min(i+100, len(ids))])
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if m == nil {
				continue // deleted since
			}
			p, d := fileOf(m)
			if p == nil && d == nil {
				continue // no longer carries it
			}
			rel := fmt.Sprintf("%d/%d%s", chatID, m.GetID(), fileExt(p, d))
			dest := filepath.Join(MediaPath(), filepath.FromSlash(rel))
			ok, err := download(ctx, c.api, m, dest+".part")
			if err != nil {
				if _, flood := tgerr.AsFloodWait(err); flood || ctx.Err() != nil {
					return err
				}
				os.Remove(dest + ".part")
				failed(rel, err)
				continue
			}
			if !ok {
				continue // a picture without a size to download
			}
			if err := os.Rename(dest+".part", dest); err != nil {
				return err
			}
			db.Exec(store, "UPDATE message SET file = ? WHERE chat_id = ? AND id = ?", rel, chatID, m.GetID())
			wrote()
		}
	}
	return nil
}

// FetchMedia downloads a message's file now through the live connection (only while it runs), into
// the place the sync's --media uses, and records it there as the sync does: the file's path; "" when
// not connected, not a Telegram message, or one that carries no file (any more).
func (Plugin) FetchMedia(ctx context.Context, c *plugins.Context, messageID int64) (path string, err error) {
	cn, done := one.borrow()
	if cn == nil {
		return "", nil
	}
	defer done()
	if cn != liveConn.Load() {
		return "", nil // a sync's connection: the live one is not running
	}
	var key, chatKey sql.NullString
	if !db.Row(c.Store().Read(), "SELECT m.key, cv.key FROM message m JOIN service s ON s.id = m.service_id "+
		"JOIN conversation cv ON cv.id = m.conversation_id WHERE m.id = ? AND s.name = 'telegram'",
		[]any{messageID}, &key, &chatKey) {
		return "", nil
	}
	id, err1 := strconv.Atoi(key.String)
	chatID, err2 := strconv.ParseInt(chatKey.String, 10, 64)
	if err1 != nil || err2 != nil {
		return "", nil
	}
	defer db.Recover(&err)
	store, err := openStore(DBPath())
	if err != nil {
		return "", err
	}
	defer store.Close()
	var have sql.NullString
	if db.Row(store, "SELECT file FROM message WHERE chat_id = ? AND id = ?", []any{chatID, id}, &have) && have.Valid {
		p := filepath.Join(MediaPath(), filepath.FromSlash(have.String))
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	peer, err := cn.peer(ctx, chatID)
	if err != nil {
		return "", err
	}
	msgs, err := cn.byIDs(ctx, peer, []int{id}) // fetched again: the file references expire
	if err != nil || msgs[0] == nil {
		return "", err
	}
	p, d := fileOf(msgs[0])
	if p == nil && d == nil {
		return "", nil
	}
	rel := fmt.Sprintf("%d/%d%s", chatID, id, fileExt(p, d))
	dest := filepath.Join(MediaPath(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	wrote, err := download(ctx, cn.api, msgs[0], dest+".part")
	if err != nil || !wrote {
		os.Remove(dest + ".part")
		return "", err
	}
	if err := os.Rename(dest+".part", dest); err != nil {
		return "", err
	}
	db.Exec(store, "UPDATE message SET file = ? WHERE chat_id = ? AND id = ?", rel, chatID, id)
	return dest, nil
}

// mediaBackfill is how far back the files not downloaded yet are looked for, at each connection
// and each import (as the WhatsApp bridge does).
const mediaBackfill = 7 * 24 * time.Hour

// downloads says whether the files of new messages are downloaded: the setting as it is now, on
// unless turned off.
func downloads(c *plugins.Context) bool {
	on, set := currentSettings(c)["media"].(bool)
	return on || !set
}

// fileQueue downloads the files of the messages the live connection stores, apart from it (a
// video takes a while), and brings them into the archive.
type fileQueue struct {
	mu   sync.Mutex
	msgs map[int64]map[int]bool
	wake chan struct{}
}

func newFileQueue() *fileQueue {
	return &fileQueue{msgs: map[int64]map[int]bool{}, wake: make(chan struct{}, 1)}
}

func (q *fileQueue) add(chat int64, id int) {
	q.mu.Lock()
	if q.msgs[chat] == nil {
		q.msgs[chat] = map[int]bool{}
	}
	q.msgs[chat][id] = true
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// recent queues the stored messages of the last mediaBackfill whose files are not downloaded yet,
// of the chats imported: what arrived while not connected, or failed before.
func (q *fileQueue) recent(c *plugins.Context) (err error) {
	if !downloads(c) {
		return nil
	}
	defer db.Recover(&err)
	store, err := openStore(DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	skip := idSet(currentSettings(c)["skip_chats"])
	db.Each(store, "SELECT chat_id, id, json FROM message WHERE file IS NULL AND date >= ?",
		[]any{time.Now().Add(-mediaBackfill).Unix()}, func(scan func(...any)) {
			var chat int64
			var id int
			var js string
			scan(&chat, &id, &js)
			var m map[string]any
			dec := json.NewDecoder(strings.NewReader(js))
			dec.UseNumber()
			if skip[chat] || dec.Decode(&m) != nil {
				return
			}
			if ok, _ := wanted(m); ok {
				q.add(chat, id)
			}
		})
	return nil
}

// run downloads what is queued until ctx ends; a failure is logged, the connection goes on.
func (q *fileQueue) run(ctx context.Context, c *plugins.Context, cn *conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		}
		q.mu.Lock()
		todo := q.msgs
		q.msgs = map[int64]map[int]bool{}
		q.mu.Unlock()
		if err := fetchQueued(ctx, c, cn, todo); err != nil && ctx.Err() == nil {
			c.Log("error: {e}", map[string]any{"e": err.Error()})
		}
	}
}

// fetchQueued downloads the files of the queued messages still without one, then brings those
// downloaded into the archive, where the chats shown take them.
func fetchQueued(ctx context.Context, c *plugins.Context, cn *conn, todo map[int64]map[int]bool) error {
	done := 0
	err := func() (err error) {
		defer db.Recover(&err)
		store, err := openStore(DBPath())
		if err != nil {
			return err
		}
		defer store.Close()
		chats := make([]int64, 0, len(todo))
		for chat := range todo {
			chats = append(chats, chat)
		}
		sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
		for _, chat := range chats {
			var ids []int
			for id := range todo[chat] {
				var none int
				if db.Row(store, "SELECT 1 FROM message WHERE chat_id = ? AND id = ? AND file IS NULL", []any{chat, id}, &none) {
					ids = append(ids, id)
				}
			}
			sort.Ints(ids)
			if len(ids) == 0 {
				continue
			}
			if err := fetchFiles(ctx, cn, store, chat, ids, func() { done++ }, func(rel string, err error) {
				c.Log("{file}: not downloaded ({e})", map[string]any{"file": rel, "e": err.Error()})
			}); err != nil {
				return err
			}
		}
		return nil
	}()
	if done > 0 {
		if err := withArchive(c, func(a *archive.Archive) error { return importMedia(a, func(string) {}) }); err != nil {
			return err
		}
		c.Emit(M{"type": "changed"})
	}
	return err
}
