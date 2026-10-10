package importers

// The services' photos of people and groups (handle_photo, chat_photo): files taken into the store
// (Store.Keep) and recorded with the time they were seen.
//
//   - groups' photos the archive's own messages tell of (ChatPhotosFromNotices: Telegram's new photo
//     as the notice's file, any service's removal);
//   - the iPhone's WhatsApp: Media/Profile/<user>-<time>.thumb (and .jpg, the whole picture),
//     extracted into <iPhone data>/whatsapp-photos/ (PhotosWhatsAppIphone);
//   - the iPhone's Viber: the files named by ZMEMBER.ZICONID / ZPREVICONID and ZCONVERSATION.ZICONID,
//     extracted into <iPhone data>/viber-photos/ (PhotosViberIphone);
//   - Viber Desktop: its Avatars folders, matched to the snapshot's contacts and chats by
//     Contact.DownloadID and ChatInfo.IconID (ViberDesktopPhotos).

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/telegramstore"
)

// ChatPhotosFromNotices records the group photos the archive's messages tell of: a notice of a new
// photo with the photo as its file (Telegram's), at the message's time; a notice of its removal
// (any service) as none from then.
func ChatPhotosFromNotices(a *archive.Archive, _ *Store) {
	type seen struct {
		conv    int64
		service string
		sha     string
		ts      int64
		removed bool
	}
	var all []seen
	a.Each("SELECT m.conversation_id, s.name, m.ts, coalesce(json_extract(x.value, '$.removed'), 0), "+
		"(SELECT at.sha256 FROM attachment at JOIN media md ON md.sha256 = at.sha256 WHERE at.message_id = m.id "+
		"AND md.mime LIKE 'image/%' ORDER BY at.id LIMIT 1) "+
		"FROM notice n JOIN message m ON m.id = n.message_id JOIN service s ON s.id = m.service_id "+
		"JOIN conversation c ON c.id = m.conversation_id, json_each(n.args, '$.actions') x "+
		"WHERE n.code = 'group' AND c.is_group AND json_extract(x.value, '$.type') = 'avatar' ORDER BY m.ts", nil,
		func(scan func(...any)) {
			var r seen
			var sha sql.NullString
			scan(&r.conv, &r.service, &r.ts, &r.removed, &sha)
			r.sha = sha.String
			all = append(all, r)
		})
	for _, r := range all {
		switch {
		case r.removed:
			a.ChatPhoto(r.conv, r.service, "", r.ts/1000)
		case r.sha != "":
			a.ChatPhoto(r.conv, r.service, r.sha, r.ts/1000)
		}
	}
}

// WhatsAppProfilePhoto reads the name of a file of the iPhone's WhatsApp Media/Profile:
// "<user>-<Unix time>.thumb" (or .jpg), a group's user being "<creator>-<created>". It gives the
// jid's user part, the time and whether it is the whole picture (not the .thumb).
func WhatsAppProfilePhoto(name string) (user string, at int64, whole bool, ok bool) {
	base, ext := splitext(name)
	switch strings.ToLower(ext) {
	case ".thumb":
	case ".jpg", ".jpeg":
		whole = true
	default:
		return "", 0, false, false
	}
	i := strings.LastIndexByte(base, '-')
	if i <= 0 {
		return "", 0, false, false
	}
	user, stamp := base[:i], base[i+1:]
	t, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || len(stamp) < 9 || len(stamp) > 13 {
		return "", 0, false, false
	}
	if t >= 1e12 { // milliseconds
		t /= 1000
	}
	for _, part := range strings.Split(user, "-") {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return "", 0, false, false
		}
	}
	return user, t, whole, true
}

// WhatsAppPhotoUsers are the jids' user parts the iPhone's WhatsApp database knows (its chats and
// groups' members): the profile photos worth extracting.
func WhatsAppPhotoUsers(database string) map[string]bool {
	out := map[string]bool{}
	if !exists(database) {
		return out
	}
	d := ro(database)
	defer d.Close()
	for table, q := range map[string]string{"ZWACHATSESSION": "SELECT ZCONTACTJID AS j FROM ZWACHATSESSION",
		"ZWAGROUPMEMBER": "SELECT ZMEMBERJID AS j FROM ZWAGROUPMEMBER"} {
		if !hasTable(d, table) {
			continue
		}
		for _, r := range maps(d, q) {
			if jid := str(r["j"]); jid != "" && !isWAChannel(jid) {
				out[user(jid)] = true
			}
		}
	}
	return out
}

// PhotosWhatsAppIphone: the profile photos of people and groups the iPhone's WhatsApp kept, at the
// time in their names; the whole picture where there is one, else its thumbnail. The files stay in
// the extract (the next sync does not bring them again).
func PhotosWhatsAppIphone(a *archive.Archive, s *Store) {
	folder := filepath.Join(archive.IphoneData(), "whatsapp-photos")
	entries, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	type photo struct {
		user  string
		at    int64
		whole bool
		path  string
	}
	best := map[[2]string]photo{}
	for _, e := range entries {
		u, at, whole, ok := WhatsAppProfilePhoto(e.Name())
		if !ok || e.IsDir() {
			continue
		}
		k := [2]string{u, strconv.FormatInt(at, 10)}
		if old, seen := best[k]; !seen || whole && !old.whole {
			best[k] = photo{u, at, whole, filepath.Join(folder, e.Name())}
		}
	}
	var photos []photo
	for _, p := range best {
		photos = append(photos, p)
	}
	sort.Slice(photos, func(i, j int) bool {
		if photos[i].at != photos[j].at {
			return photos[i].at < photos[j].at
		}
		return photos[i].user < photos[j].user
	})
	var contacts, iphone *sql.DB
	if exists(WhatsAppContactsDB()) {
		contacts = ro(WhatsAppContactsDB())
		defer contacts.Close()
	}
	if exists(WhatsAppIphoneDB()) {
		iphone = ro(WhatsAppIphoneDB())
		defer iphone.Close()
	}
	person := newWAPeople(contacts, nil, iphone, nil)
	for _, p := range photos {
		if strings.Contains(p.user, "-") {
			if conv, ok := a.FindConversation("whatsapp", p.user+"@g.us"); ok {
				a.ChatPhoto(conv, "whatsapp", s.Keep(p.path), p.at)
			}
			continue
		}
		jid := p.user + "@s.whatsapp.net"
		if person.lids[p.user] {
			jid = p.user + "@lid"
		}
		if h, ok := person.of(jid); ok && a.Known(h) {
			a.HandlePhoto(h, "whatsapp", s.Keep(p.path), p.at)
		}
	}
}

// photoFiles finds the files of photos by the id a database gives them: a file named by the id
// (with or without an extension), or by its MD5 (as Viber names its downloads).
type photoFiles map[string]string

func newPhotoFiles(dirs ...string) photoFiles {
	out := photoFiles{}
	for _, d := range dirs {
		filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err == nil && e.Type().IsRegular() {
				base, _ := splitext(e.Name())
				for _, k := range []string{e.Name(), base} {
					if _, seen := out[k]; !seen {
						out[k] = p
					}
				}
			}
			return nil
		})
	}
	return out
}

func (f photoFiles) of(id string) string {
	if id == "" {
		return ""
	}
	if p := f[id]; p != "" {
		return p
	}
	sum := md5.Sum([]byte(id))
	return f[hex.EncodeToString(sum[:])]
}

// ViberPhotoFile says whether a file's name is one of the ids (by the id or its MD5, as photoFiles
// finds them): for the extraction from the iPhone's backup.
func ViberPhotoFile(name string, ids map[string]bool) bool {
	base, _ := splitext(name)
	return ids[name] || ids[base]
}

// ViberIconIDs are the photos' ids the iPhone's Viber database names (members' now and before,
// groups'), with the MD5 of each: the file names worth extracting.
func ViberIconIDs(database string) map[string]bool {
	out := map[string]bool{}
	if !exists(database) {
		return out
	}
	d := ro(database)
	defer d.Close()
	for _, tc := range [][2]string{{"ZMEMBER", "ZICONID"}, {"ZMEMBER", "ZPREVICONID"}, {"ZCONVERSATION", "ZICONID"}} {
		if !columns(d, tc[0])[tc[1]] {
			continue
		}
		for _, r := range maps(d, "SELECT "+tc[1]+" AS id FROM "+tc[0]+" WHERE coalesce("+tc[1]+", '') != ''") {
			id := pyStr(r["id"])
			sum := md5.Sum([]byte(id))
			out[id], out[hex.EncodeToString(sum[:])] = true, true
		}
	}
	return out
}

// fileTime is a file's modification time, Unix seconds (an iPhone's extract keeps the phone's).
func fileTime(path string) int64 {
	if st, err := os.Stat(path); err == nil {
		return st.ModTime().Unix()
	}
	return 0
}

// PhotosViberIphone: the photos of members and groups of the iPhone's Viber, extracted by their ids
// into <iPhone data>/viber-photos/: a member's photo before (ZPREVICONID) at its file's time, the
// one now (ZICONID) from its file's time to the database's.
func PhotosViberIphone(a *archive.Archive, s *Store) {
	data := archive.IphoneData()
	database := ViberIphoneDB()
	files := newPhotoFiles(filepath.Join(data, "viber-photos"))
	if len(files) == 0 || !exists(database) {
		return
	}
	d := ro(database)
	defer d.Close()
	now := fileTime(database)
	person := viberPeople{viberPhones(d, nil, a)}
	cols := columns(d, "ZMEMBER")
	if cols["ZICONID"] {
		prev := "NULL"
		if cols["ZPREVICONID"] {
			prev = "ZPREVICONID"
		}
		for _, r := range maps(d, "SELECT ZMEMBERID, "+prev+" AS prev, ZICONID AS icon FROM ZMEMBER ORDER BY Z_PK") {
			h, ok := person.of(r["ZMEMBERID"], nil)
			if !ok || !a.Known(h) {
				continue
			}
			if p := files.of(strOrEmpty(r["prev"])); p != "" && strOrEmpty(r["prev"]) != strOrEmpty(r["icon"]) {
				a.HandlePhoto(h, "viber", s.Keep(p), fileTime(p))
			}
			if p := files.of(strOrEmpty(r["icon"])); p != "" {
				sha := s.Keep(p)
				a.HandlePhoto(h, "viber", sha, fileTime(p))
				a.HandlePhoto(h, "viber", sha, now)
			}
		}
	}
	if columns(d, "ZCONVERSATION")["ZICONID"] {
		for _, r := range maps(d, "SELECT ZGROUPID, ZICONID FROM ZCONVERSATION WHERE ZGROUPID IS NOT NULL ORDER BY Z_PK") {
			conv, ok := a.FindConversation("viber", "group:"+pyStr(r["ZGROUPID"]))
			if p := files.of(strOrEmpty(r["ZICONID"])); ok && p != "" {
				sha := s.Keep(p)
				a.ChatPhoto(conv, "viber", sha, fileTime(p))
				a.ChatPhoto(conv, "viber", sha, now)
			}
		}
	}
}

// ViberDesktopPhotos: the photos in Viber Desktop's Avatars folders (avatars: each account's),
// matched to its contacts by Contact.DownloadID and to its group chats by ChatInfo.IconID (a file
// named by the id, or by its MD5): from the file's time to the snapshot's. The file's time is when
// Viber Desktop downloaded it, not when the photo was set: the first sighting known, nothing more.
// Viber's own files are left where they are (it makes them again).
func ViberDesktopPhotos(database string, avatars ...string) MediaStep {
	return func(a *archive.Archive, s *Store) {
		files := newPhotoFiles(avatars...)
		if len(files) == 0 || !exists(database) {
			return
		}
		d := ro(database)
		defer d.Close()
		now := fileTime(database)
		if now == 0 {
			now = time.Now().Unix()
		}
		if columns(d, "Contact")["DownloadID"] {
			person := viberPeople{viberPhones(nil, d, a)}
			for _, r := range maps(d, "SELECT MID, Number, DownloadID FROM Contact WHERE coalesce(DownloadID, '') != '' ORDER BY ContactID") {
				h, ok := person.of(r["MID"], r["Number"])
				if p := files.of(pyStr(r["DownloadID"])); ok && p != "" && a.Known(h) {
					sha := s.Keep(p)
					a.HandlePhoto(h, "viber", sha, fileTime(p))
					a.HandlePhoto(h, "viber", sha, now)
				}
			}
		}
		if columns(d, "ChatInfo")["IconID"] {
			for _, r := range maps(d, "SELECT Token, IconID FROM ChatInfo WHERE coalesce(Token, '') != '' "+
				"AND coalesce(IconID, '') != '' ORDER BY ChatID") {
				conv, ok := a.FindConversation("viber", "group:"+pyStr(r["Token"]))
				if p := files.of(pyStr(r["IconID"])); ok && p != "" {
					sha := s.Keep(p)
					a.ChatPhoto(conv, "viber", sha, fileTime(p))
					a.ChatPhoto(conv, "viber", sha, now)
				}
			}
		}
	}
}

// storedPhoto is the digest of a photo a live source recorded (sha: the digest it noted, path: its
// file, "" for none): the archive's copy where it has one (the source's file let go), else the
// file taken in. ok false: its file went before the archive had it.
func storedPhoto(a *archive.Archive, s *Store, sha, path string) (string, bool) {
	if sha != "" && a.Exists("SELECT 1 FROM media WHERE sha256 = ?", sha) {
		if path != "" {
			s.held(path, sha) // had: the source's copy goes
		}
		return sha, true
	}
	if path == "" {
		return "", false
	}
	got := s.Keep(path)
	return got, got != ""
}

// localPath is a file of a source's folder by its relative path, "" for none or one leading out.
func localPath(folder, rel string) string {
	rel = filepath.FromSlash(rel)
	if rel == "" || !filepath.IsLocal(rel) {
		return ""
	}
	return filepath.Join(folder, rel)
}

// PhotosWhatsAppBridge: the profile photos the WhatsApp bridge asked for (messages.db's
// profile_photo; devices: whatsmeow's store, for the LIDs' numbers), each at its first and last
// sighting, of the people and groups the archive has (the others wait in the bridge's store for a
// later import); the files taken into the store (Move), a row whose file has gone known by its
// digest.
func PhotosWhatsAppBridge(database string, devices *sql.DB) MediaStep {
	return func(a *archive.Archive, s *Store) {
		bridge := roBridge(database)
		if bridge == nil {
			return
		}
		defer bridge.Close()
		if !hasTable(bridge, "profile_photo") {
			return
		}
		s.Move = true // the bridge's downloads: kept in the store only
		folder := filepath.Dir(database)
		person := newWAPeople(nil, devices, nil, bridge)
		for _, r := range maps(bridge, "SELECT jid, picture_id, path, sha256, first_seen, last_seen FROM profile_photo "+
			"ORDER BY first_seen, rowid") {
			jid := str(r["jid"])
			var record func(sha string, at int64)
			if strings.HasSuffix(jid, "@g.us") {
				if conv, ok := a.FindConversation("whatsapp", jid); ok {
					record = func(sha string, at int64) { a.ChatPhoto(conv, "whatsapp", sha, at) }
				}
			} else if h, ok := person.of(jid); ok && a.Known(h) {
				record = func(sha string, at int64) { a.HandlePhoto(h, "whatsapp", sha, at) }
			}
			if record == nil {
				continue
			}
			sha := ""
			if str(r["picture_id"]) != "" {
				var ok bool
				if sha, ok = storedPhoto(a, s, strOrEmpty(r["sha256"]), localPath(folder, strOrEmpty(r["path"]))); !ok {
					continue
				}
			}
			record(sha, toInt(r["first_seen"]))
			record(sha, toInt(r["last_seen"]))
		}
	}
}

// PhotosTelegram: the photos the Telegram connection recorded (telegram_photo of its store), each at
// its first and last sighting: a user's to their handle, a group's to its conversation, those the
// archive has (the others wait in the store for a later import); the files taken into the store
// (Move), a row whose file has gone known by its digest.
func PhotosTelegram(a *archive.Archive, s *Store) {
	if !exists(telegramstore.DB()) {
		return
	}
	d := tgStore(telegramstore.DB())
	defer d.Close()
	s.Move = true // the connection's downloads: kept in the store only
	person := newTGPeople(d)
	root := telegramstore.Media()
	for _, r := range maps(d, "SELECT peer_id, photo_id, file, sha256, first_seen, last_seen FROM telegram_photo ORDER BY first_seen, rowid") {
		peer := toInt(r["peer_id"])
		var record func(sha string, at int64)
		if peer < 0 {
			if conv, ok := a.FindConversation("telegram", fmt.Sprint(peer)); ok {
				record = func(sha string, at int64) { a.ChatPhoto(conv, "telegram", sha, at) }
			}
		} else if h := person.of(peer, true); a.Known(h) {
			record = func(sha string, at int64) { a.HandlePhoto(h, "telegram", sha, at) }
		}
		if record == nil {
			continue
		}
		sha := ""
		if toInt(r["photo_id"]) != 0 {
			var ok bool
			if sha, ok = storedPhoto(a, s, strOrEmpty(r["sha256"]), localPath(root, strOrEmpty(r["file"]))); !ok {
				continue
			}
		}
		record(sha, toInt(r["first_seen"]))
		record(sha, toInt(r["last_seen"]))
	}
}
