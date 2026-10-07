// Ports everysaid/media.py: media files linked to their messages and stored once each, by content,
// in the archive.
//
// Files are hard-linked (no extra space; copied where the source is on another file system) to
// <media store>/media/<ab>/<sha256><ext> (config [media] store, the data folder by default). Each
// source records the folder its files are in (source.media_root); attachment.source_path is
// relative to it. The links to messages, each step skipped when its source is not there:
//
//   - iPhone WhatsApp: ZWAMEDIAITEM.ZMEDIALOCALPATH, message by stanza id;
//   - iPhone Viber: ZATTACHMENT.ZNAME, message by token;
//   - Android MMS (android-export.py): mms-parts/<part _id>, message by MMS id, or by time and
//     direction where the iPhone's copy of the message was the one kept;
//   - Telegram (telegram-sync --media): <chat>/<message><ext>, message by its row key (TelegramMedia);
//   - the WhatsApp bridge: the files it downloaded (messages.media_path, relative to its store
//     folder), message by its row key, else by its id (the iPhone's copy of the same message).
//
// A file already linked from the same source path is not hashed again while it is the stored one
// (same size and time).
package importers

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"everysaid/internal/archive"
	"everysaid/internal/i18n"
)

// linkOrCopy makes a hard link to src at dest, or a copy wherever a link cannot be made: another
// file system, or one without hard links (each system says so with its own error). A missing
// source or an existing destination is still an error.
func linkOrCopy(src, dest string) error {
	err := os.Link(src, dest)
	if err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrExist) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	outF, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(outF, in); err != nil {
		outF.Close()
		return err
	}
	if err := outF.Close(); err != nil {
		return err
	}
	os.Chmod(dest, fi.Mode().Perm())
	return os.Chtimes(dest, fi.ModTime(), fi.ModTime()) // as shutil.copy2 keeps them
}

func sha256File(path string) string {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		panic(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		panic(err)
	}
	return fi.Size()
}

// sameFile says whether two paths hold the same file as far as size and time tell (false when
// either is not there).
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && (os.SameFile(fa, fb) || fa.Size() == fb.Size() && fa.ModTime().Equal(fb.ModTime()))
}

type missingKey struct{ source, why string }

// Store links files to messages, each file stored once.
type Store struct {
	a       *archive.Archive
	Root    string
	Added   map[string]int
	Missing map[missingKey]int
}

func NewStore(a *archive.Archive) *Store {
	return &Store{a: a, Root: archive.MediaRoot(), Added: map[string]int{}, Missing: map[missingKey]int{}}
}

// Link links the file at path (rel: its path within the source's folder) to a message (0: none
// found).
func (s *Store) Link(source string, sourceID int64, path, rel string, messageID int64) {
	a := s.a
	if messageID == 0 {
		s.Missing[missingKey{source, "no message"}]++
		return
	}
	if !isFile(path) {
		s.Missing[missingKey{source, "no file"}]++
		return
	}
	if a.Exists("SELECT 1 FROM attachment WHERE source_id = ? AND source_path = ? AND message_id = ?", sourceID, rel, messageID) {
		return
	}
	// the file of this source path hashed before, while it is the same file: the stored copy (a hard
	// link to it, or a copy keeping its time) of the same size and time; else (another file now, as
	// in a new export whose part numbers start again, or the copy gone to the library) read again
	var digest string
	var known, stored sql.NullString
	if a.Row("SELECT a.sha256, md.path FROM attachment a JOIN media md ON md.sha256 = a.sha256 "+
		"WHERE a.source_id = ? AND a.source_path = ? AND md.size = ?", []any{sourceID, rel, fileSize(path)}, &known, &stored) &&
		known.Valid && sameFile(path, filepath.Join(s.Root, stored.String)) {
		digest = known.String
	} else {
		digest = sha256File(path)
	}
	if !a.Exists("SELECT 1 FROM media WHERE sha256 = ?", digest) {
		_, ext := splitext(path)
		stored := "media/" + digest[:2] + "/" + digest + strings.ToLower(ext)
		target := filepath.Join(s.Root, stored)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			panic(err)
		}
		if !exists(target) {
			if err := linkOrCopy(path, target); err != nil {
				panic(err)
			}
		}
		a.Exec("INSERT INTO media VALUES (?, ?, ?, ?)", digest, fileSize(path), archive.NullStr(guessType(path)), stored)
	}
	a.Exec("INSERT INTO attachment (message_id, sha256, source_id, source_path) VALUES (?, ?, ?, ?)",
		messageID, digest, sourceID, rel)
	s.Added[source]++
}

func byKey(a *archive.Archive, service string) map[string]int64 {
	out := map[string]int64{}
	a.Each("SELECT key, id FROM message WHERE service_id = ? AND key IS NOT NULL", []any{a.Service.ID(service)},
		func(scan func(...any)) {
			var k string
			var id int64
			scan(&k, &id)
			out[k] = id
		})
	return out
}

func byOrigin(a *archive.Archive, sourceName string) map[string]int64 {
	out := map[string]int64{}
	a.Each("SELECT o.row_key, o.message_id FROM message_origin o JOIN source s ON s.id = o.source_id "+
		"WHERE s.name = ?", []any{sourceName}, func(scan func(...any)) {
		var k string
		var id int64
		scan(&k, &id)
		out[k] = id
	})
	return out
}

// MediaStep is one source's files, linked through the store.
type MediaStep func(a *archive.Archive, s *Store)

// MediaWhatsApp: the iPhone's WhatsApp files.
func MediaWhatsApp(a *archive.Archive, s *Store) {
	path := WhatsAppIphoneDB()
	if !exists(path) {
		return
	}
	data := archive.IphoneData()
	src := a.Source(archive.Iphone()+"/whatsapp", path, archive.Iphone(), data+"/whatsapp-media")
	messages := byKey(a, "whatsapp")
	d := ro(path)
	defer d.Close()
	for _, r := range maps(d, "SELECT m.ZSTANZAID, i.ZMEDIALOCALPATH FROM ZWAMEDIAITEM i JOIN ZWAMESSAGE m ON m.Z_PK = i.ZMESSAGE "+
		"WHERE i.ZMEDIALOCALPATH IS NOT NULL ORDER BY i.Z_PK") {
		rel := strings.TrimPrefix(str(r["ZMEDIALOCALPATH"]), "Media/")
		var mid int64
		if k, ok := r["ZSTANZAID"].(string); ok {
			mid = messages[k]
		}
		s.Link(archive.Iphone()+"/whatsapp", src, data+"/whatsapp-media/"+rel, rel, mid)
	}
}

// MediaWhatsAppBridge: the files the bridge downloaded; database "" is config's bridge.
func MediaWhatsAppBridge(database string) MediaStep {
	return func(a *archive.Archive, s *Store) {
		if database == "" {
			database = BridgeDB()
		}
		if database == "" || !exists(database) {
			return
		}
		d := ro(database)
		defer d.Close()
		if !columns(d, "messages")["media_path"] {
			return // a bridge from before it downloaded files
		}
		folder := filepath.Dir(database)
		src := a.Source("whatsapp-bridge", database, "whatsapp-bridge", folder)
		origins, messages := byOrigin(a, "whatsapp-bridge"), byKey(a, "whatsapp")
		absFolder, _ := filepath.Abs(folder)
		for _, r := range maps(d, "SELECT chat_jid, id, media_path FROM messages "+
			"WHERE coalesce(media_path, '') != '' ORDER BY timestamp") {
			rel := str(r["media_path"])
			path := rel
			if !filepath.IsAbs(rel) {
				path = filepath.Join(folder, rel)
			}
			path, _ = filepath.Abs(path)
			if path != absFolder && !strings.HasPrefix(path, strings.TrimSuffix(absFolder, string(filepath.Separator))+string(filepath.Separator)) {
				continue // only files within the bridge's folder
			}
			mid := origins[str(r["chat_jid"])+"/"+pyStr(r["id"])]
			if mid == 0 {
				if k, ok := r["id"].(string); ok {
					mid = messages[k]
				}
			}
			s.Link("whatsapp-bridge", src, path, rel, mid)
		}
	}
}

// MediaViberIphone: the iPhone's Viber files.
func MediaViberIphone(a *archive.Archive, s *Store) {
	data := archive.IphoneData()
	database := data + "/viber.sqlite"
	if !exists(database) {
		return
	}
	src := a.Source(archive.Iphone()+"/viber", database, archive.Iphone(), data+"/viber-media")
	messages, origins := byKey(a, "viber"), byOrigin(a, archive.Iphone()+"/viber")
	files := map[string]string{}
	for _, folder := range []string{"Attachments", "FileMessages", "VoiceMessages"} {
		entries, _ := os.ReadDir(data + "/viber-media/" + folder) // none yet, or all taken and removed
		for _, e := range entries {
			files[e.Name()] = folder + "/" + e.Name()
		}
	}
	d := ro(database)
	defer d.Close()
	for _, r := range maps(d, "SELECT m.Z_PK, m.ZTOKEN, a.ZNAME FROM ZVIBERMESSAGE m JOIN ZATTACHMENT a ON a.Z_PK = m.ZATTACHMENT "+
		"WHERE a.ZNAME IS NOT NULL ORDER BY m.Z_PK") {
		f, ok := files[str(r["ZNAME"])]
		if !ok {
			continue // never downloaded on the phone
		}
		var mid int64
		if truthy(r["ZTOKEN"]) {
			mid = messages[pyStr(r["ZTOKEN"])]
		} else {
			mid = origins[pyStr(r["Z_PK"])]
		}
		s.Link(archive.Iphone()+"/viber", src, data+"/viber-media/"+f, f, mid)
	}
}

// MediaMMSAndroid: the MMS parts of every Android export.
func MediaMMSAndroid(a *archive.Archive, s *Store) {
	for _, e := range archive.AndroidExports("") {
		MediaMMSExport(a, s, e.Device, e.DB, e.Folder)
	}
}

// MediaMMSExport: the MMS parts of one Android export.
func MediaMMSExport(a *archive.Archive, s *Store, device, database, folder string) {
	name := device + "/mms"
	src := a.Source(name, database, device, folder)
	origins := byOrigin(a, name)
	d := ro(database)
	defer d.Close()
	sms, mms := a.Service.ID("sms"), a.Service.ID("mms")
	for _, r := range maps(d, "SELECT p._id, p.mid, m.date, m.msg_box FROM mms_part p JOIN mms m ON m._id = p.mid "+
		"WHERE p._data IS NOT NULL ORDER BY CAST(p._id AS INTEGER)") {
		path := folder + "/mms-parts/" + pyStr(r["_id"])
		if isFile(path) && fileSize(path) == 0 {
			continue // parts the provider no longer returns
		}
		var mid int64
		if k, ok := r["mid"].(string); ok {
			mid = origins[k]
		}
		if mid == 0 { // the iPhone's copy was kept: same time and direction
			date, _ := pyInt(r["date"])
			ts := date * 1000
			box := isStr(r["msg_box"]) && str(r["msg_box"]) == "2"
			ids := a.Ints("SELECT id FROM message WHERE service_id IN (?, ?) AND outgoing = ? AND ts BETWEEN ? AND ?",
				sms, mms, archive.B2I(box), ts-pairMS, ts+pairMS)
			if len(ids) == 1 {
				mid = ids[0]
			}
		}
		s.Link(name, src, path, "mms-parts/"+pyStr(r["_id"]), mid)
	}
}

// Phones are what a phone's sources bring.
var Phones = []MediaStep{MediaWhatsApp, MediaViberIphone, MediaMMSAndroid}

// Media links the files of the steps given (all of them when none is).
func Media(a *archive.Archive, out func(string), steps ...MediaStep) (err error) {
	defer archive.Recover(&err)
	if len(steps) == 0 {
		steps = append(append([]MediaStep{}, Phones...), TelegramMedia, MediaWhatsAppBridge(""))
	}
	s := NewStore(a)
	for _, step := range steps {
		step(a, s)
		a.Commit()
	}
	s.report(a, out)
	return nil
}

func (s *Store) report(a *archive.Archive, out func(string)) {
	var sources []string
	for k := range s.Added {
		sources = append(sources, k)
	}
	sort.Strings(sources)
	for _, k := range sources {
		say(out, "new:    {n} {source}", map[string]any{"n": fmt.Sprintf("%6d", s.Added[k]), "source": k})
	}
	var missing []missingKey
	for k := range s.Missing {
		missing = append(missing, k)
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].source != missing[j].source {
			return missing[i].source < missing[j].source
		}
		return missing[i].why < missing[j].why
	})
	for _, k := range missing {
		say(out, "without: {n} {source} ({why})", map[string]any{"n": fmt.Sprintf("%6d", s.Missing[k]), "source": k.source,
			"why": i18n.Tr(k.why, Lang())})
	}
	var count, size int64
	a.Row("SELECT count(*), coalesce(sum(size), 0) FROM media", nil, &count, &size)
	say(out, "total: {count} files, {size} GB, {links} links to messages", map[string]any{"count": count,
		"size": fmt.Sprintf("%.1f", float64(size)/1e9), "links": a.Int("SELECT count(*) FROM attachment")})
}
