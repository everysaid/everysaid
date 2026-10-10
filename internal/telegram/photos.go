package telegram

// The photos of people and groups (the account's own too), as the users and chats Telegram gives
// carry them (their photo's id): kept up to date by every message and member list, so a change is
// seen without asking. Every few minutes the store's users and chats are looked over; a photo not
// had yet is downloaded (its big size, one at a time with a pause between), into
// media/photos/<peer>/<photo id>.jpg, and recorded in telegram_photo (a row for each photo, its last
// sighting moved while it stays; photo_id 0 when there is none), for the importer
// (importers.PhotosTelegram) to take into the archive. Broadcast channels are not kept.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
)

var (
	photoScan  = 10 * time.Minute  // the users and chats looked over
	photoPause = 5 * time.Second   // between two downloads
	photoEvery = 24 * time.Hour    // a photo that stays: its sighting moved at most so often
	photoGet   = downloadPeerPhoto // a photo's bytes (a test's own)
	importPics = func(c *plugins.Context) error {
		return withArchive(c, func(a *archive.Archive) error { return importers.Media(a, func(string) {}, importers.PhotosTelegram) })
	}
)

// peerPhoto is the photo id a user's or chat's entity carries (0: none), and whether it has photos
// at all (a broadcast channel, an empty or forbidden chat: not looked at).
func peerPhoto(e any) (int64, bool) {
	switch e := e.(type) {
	case *tg.User:
		if p, ok := e.Photo.(*tg.UserProfilePhoto); ok {
			return p.PhotoID, true
		}
		return 0, !e.Min // a "min" user may only lack it
	case *tg.Chat:
		if p, ok := e.Photo.(*tg.ChatPhoto); ok {
			return p.PhotoID, true
		}
		return 0, true
	case *tg.Channel:
		if e.Broadcast && !e.Megagroup {
			return 0, false
		}
		if p, ok := e.Photo.(*tg.ChatPhoto); ok {
			return p.PhotoID, true
		}
		return 0, !e.Min
	}
	return 0, false
}

// photoWork is one peer whose photo is to be downloaded.
type photoWork struct {
	peer    int64
	photoID int64
	input   tg.InputPeerClass
}

// scanPhotos looks over the store's users and chats: a photo seen again moves its sighting (at most
// every photoEvery), a removal is recorded; it gives those to download, and how many rows changed.
func scanPhotos(store *sql.DB, now int64) (todo []photoWork, changed int, err error) {
	defer db.Recover(&err)
	type seen struct {
		id  int64
		raw string
	}
	var all []seen
	db.Each(store, "SELECT id, json FROM telegram_chat UNION ALL SELECT id, json FROM telegram_entity "+
		"WHERE id NOT IN (SELECT id FROM telegram_chat)", nil, func(scan func(...any)) {
		var s seen
		scan(&s.id, &s.raw)
		all = append(all, s)
	})
	for _, s := range all {
		o, err := Load([]byte(s.raw))
		if err != nil || EntityID(o) != s.id {
			continue
		}
		want, ok := peerPhoto(o)
		if !ok {
			continue
		}
		var rowid, had, last int64
		found := db.Row(store, "SELECT rowid, photo_id, last_seen FROM telegram_photo WHERE peer_id = ? "+
			"ORDER BY first_seen DESC, rowid DESC LIMIT 1", []any{s.id}, &rowid, &had, &last)
		switch {
		case found && had == want:
			if now-last >= int64(photoEvery/time.Second) {
				db.Exec(store, "UPDATE telegram_photo SET last_seen = ? WHERE rowid = ?", now, rowid)
				changed++
			}
		case want == 0:
			if found {
				db.Exec(store, "INSERT INTO telegram_photo (peer_id, photo_id, first_seen, last_seen) VALUES (?, 0, ?, ?)",
					s.id, now, now)
				changed++
			}
		default:
			if input, ok := InputPeer(o); ok {
				todo = append(todo, photoWork{s.id, want, input})
			}
		}
	}
	return todo, changed, nil
}

// downloadPeerPhoto is a user's or chat's photo, its big size.
func downloadPeerPhoto(ctx context.Context, api *tg.Client, w photoWork) ([]byte, error) {
	loc := &tg.InputPeerPhotoFileLocation{Big: true, Peer: w.input, PhotoID: w.photoID}
	pr, pw := io.Pipe()
	go func() {
		_, err := downloader.NewDownloader().Download(api, loc).Stream(ctx, pw)
		pw.CloseWithError(err)
	}()
	return io.ReadAll(io.LimitReader(pr, 20<<20))
}

// keepPhoto writes a downloaded photo into media/photos and records it.
func keepPhoto(store *sql.DB, w photoWork, data []byte, now int64) (err error) {
	defer db.Recover(&err)
	rel := filepath.ToSlash(filepath.Join("photos", fmt.Sprint(w.peer), fmt.Sprint(w.photoID)+".jpg"))
	path := filepath.Join(MediaPath(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path+".part", data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(path+".part", path); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	db.Exec(store, "INSERT INTO telegram_photo (peer_id, photo_id, file, sha256, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?)",
		w.peer, w.photoID, rel, hex.EncodeToString(sum[:]), now, now)
	return nil
}

// photos keeps the photos up to date while the connection lasts.
func photos(ctx context.Context, c *plugins.Context, cn *conn) {
	for {
		if n, err := photoRound(ctx, c, cn); err != nil && ctx.Err() == nil {
			c.Log("error: {e}", map[string]any{"e": err.Error()})
		} else if n > 0 {
			if err := importPics(c); err != nil {
				c.Log("error: {e}", map[string]any{"e": err.Error()})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(photoScan):
		}
	}
}

// photoRound is one look over the users and chats, with the downloads it asks for; it gives how
// many changed.
func photoRound(ctx context.Context, c *plugins.Context, cn *conn) (int, error) {
	store, err := openStore(DBPath())
	if err != nil {
		return 0, err
	}
	defer store.Close()
	todo, changed, err := scanPhotos(store, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	for i, w := range todo {
		if i > 0 {
			select {
			case <-ctx.Done():
				return changed, nil
			case <-time.After(photoPause):
			}
		}
		data, err := photoGet(ctx, cn.api, w)
		if d, flood := tgerr.AsFloodWait(err); flood { // asked to wait: the rest at the next round
			c.Log("error: {e}", map[string]any{"e": fmt.Sprintf("FLOOD_WAIT %s", d)})
			break
		}
		if err != nil || len(data) == 0 {
			if ctx.Err() != nil {
				return changed, nil
			}
			continue // not now (the photo gone meanwhile, the connection): the next round
		}
		if err := keepPhoto(store, w, data, time.Now().Unix()); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}
