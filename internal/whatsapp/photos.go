package whatsapp

// The profile photos of people and groups (and the account's own) the account has chats with
// (not the members of groups it never wrote with: not asked for, for now): asked of WhatsApp gently, one
// at a time with a pause between, each chat at most every few days, unless WhatsApp says one
// changed (events.Picture), which is asked at once. Each photo is downloaded into
// <store>/photos/<jid>/<picture id>.jpg and recorded in profile_photo (a row for each photo seen,
// its last sighting moved while it stays; picture_id "" when there is none), for the importer
// (importers.PhotosWhatsAppBridge) to take into the archive.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

var (
	photoPause = 30 * time.Second   // between two questions
	photoIdle  = 15 * time.Minute   // when nothing is due
	photoEvery = 3 * 24 * time.Hour // a chat's photo asked again after
	photoLimit = int64(10 << 20)    // the largest photo downloaded
	photoBusy  = 30 * time.Minute   // WhatsApp said too many questions
	photoFetch = fetchPhoto         // a photo's bytes from its URL (a test's own)
	photoHTTP  = &http.Client{Timeout: time.Minute}
)

func (store *MessageStore) migratePhotos() error {
	_, err := store.db.Exec(`
		CREATE TABLE IF NOT EXISTS profile_photo (   -- the photos seen of each jid, a row for each
			jid TEXT NOT NULL,
			picture_id TEXT NOT NULL,                -- WhatsApp's id of the photo; '': none
			path TEXT,                               -- the file, relative to the store (gone once the archive has it)
			sha256 TEXT,                             -- its content's
			first_seen INTEGER NOT NULL,             -- Unix seconds
			last_seen INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS profile_photo_jid ON profile_photo (jid, first_seen);
		CREATE TABLE IF NOT EXISTS profile_photo_check (  -- when each jid's photo was last asked for
			jid TEXT PRIMARY KEY,
			checked_at INTEGER,                      -- Unix seconds; NULL: never
			due INTEGER NOT NULL DEFAULT 0           -- WhatsApp said it changed: asked first
		)`)
	return err
}

// photoNow is the jid's latest photo id ("" for none or none known).
func (store *MessageStore) photoNow(jid string) string {
	var id string
	store.db.QueryRow("SELECT picture_id FROM profile_photo WHERE jid = ? ORDER BY first_seen DESC, rowid DESC LIMIT 1", jid).Scan(&id)
	return id
}

// notePhoto records a photo (pictureID "": none) seen at `at`: the latest row's sighting moved when it
// is the same, else a new row.
func (store *MessageStore) notePhoto(jid, pictureID, path, sha string, at int64) error {
	var rowid int64
	var latest string
	err := store.db.QueryRow("SELECT rowid, picture_id FROM profile_photo WHERE jid = ? ORDER BY first_seen DESC, rowid DESC LIMIT 1",
		jid).Scan(&rowid, &latest)
	if err == nil && latest == pictureID {
		_, err = store.db.Exec("UPDATE profile_photo SET last_seen = max(last_seen, ?) WHERE rowid = ?", at, rowid)
		return err
	}
	if errors.Is(err, sql.ErrNoRows) && pictureID == "" {
		return nil // never seen with one: nothing to say
	}
	_, err = store.db.Exec("INSERT INTO profile_photo (jid, picture_id, path, sha256, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?)",
		jid, pictureID, nullable(path), nullable(sha), at, at)
	return err
}

// photoChecked records that the jid's photo was asked for now.
func (store *MessageStore) photoChecked(jid string, at int64) {
	store.db.Exec(`INSERT INTO profile_photo_check (jid, checked_at, due) VALUES (?, ?, 0)
		ON CONFLICT (jid) DO UPDATE SET checked_at = excluded.checked_at, due = 0`, jid, at)
}

// photoDue marks the jid's photo to be asked for first (WhatsApp said it changed); now false: only
// added to those asked for, when it is not there yet.
func (store *MessageStore) photoDue(jid string, now bool) {
	if now {
		store.db.Exec(`INSERT INTO profile_photo_check (jid, due) VALUES (?, 1) ON CONFLICT (jid) DO UPDATE SET due = 1`, jid)
	} else {
		store.db.Exec(`INSERT OR IGNORE INTO profile_photo_check (jid) VALUES (?)`, jid)
	}
}

// nextPhoto is the jid whose photo is to be asked for now: one WhatsApp said changed, else the chat
// (a person's or a group's) asked for longest ago, if that was more than photoEvery ago; "" for none.
func (store *MessageStore) nextPhoto(now time.Time) string {
	rows, err := store.db.Query(`SELECT j.jid FROM (SELECT jid FROM chats UNION SELECT jid FROM group_info
			UNION SELECT jid FROM profile_photo_check) j LEFT JOIN profile_photo_check c ON c.jid = j.jid
		WHERE coalesce(c.due, 0) OR c.checked_at IS NULL OR c.checked_at < ?
		ORDER BY coalesce(c.due, 0) DESC, coalesce(c.checked_at, 0), j.jid LIMIT 200`, now.Add(-photoEvery).Unix())
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var jid string
		if rows.Scan(&jid) != nil {
			continue
		}
		if j, err := types.ParseJID(jid); err == nil && photoJID(j) {
			return jid
		}
	}
	return ""
}

// photoJID: a person's or a group's chat (not a channel, a broadcast, a status, nor a bot).
func photoJID(j types.JID) bool {
	switch j.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
		return j.User != "" && !isChannel(j)
	}
	return false
}

// photoAsker is what asking for photos takes of whatsmeow's client.
type photoAsker interface {
	GetProfilePictureInfo(ctx context.Context, jid types.JID, params *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error)
}

// askPhoto asks WhatsApp for one jid's photo and records what it says. It gives how long to wait
// before the next question.
func askPhoto(ctx context.Context, client photoAsker, store *MessageStore, jidStr string, logger waLog.Logger) time.Duration {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		store.photoChecked(jidStr, time.Now().Unix())
		return 0
	}
	current := store.photoNow(jidStr)
	info, err := client.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{ExistingID: current})
	now := time.Now().Unix()
	switch {
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet):
		err = store.notePhoto(jidStr, "", "", "", now)
	case errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		err = nil // hidden from the account: nothing known, nothing changed
	case errors.Is(err, whatsmeow.ErrIQRateOverLimit):
		return photoBusy
	case err != nil:
		if ctx.Err() == nil {
			logger.Warnf("Failed to get a profile photo: %v", err)
		}
		return photoIdle // not now (the connection, WhatsApp's answer): asked again later
	case info == nil: // the same one
		if current != "" {
			err = store.notePhoto(jidStr, current, "", "", now)
		}
	case info.ID == current && current != "":
		err = store.notePhoto(jidStr, current, "", "", now)
	default:
		data, ferr := photoFetch(ctx, info.URL)
		if ferr != nil {
			logger.Warnf("Failed to download a profile photo: %v", ferr)
			return photoIdle
		}
		rel := filepath.Join("photos", safeName(jidStr), safeName(info.ID)+".jpg")
		if err = store.writeMedia(rel, data); err == nil {
			sum := sha256.Sum256(data)
			err = store.notePhoto(jidStr, info.ID, filepath.ToSlash(rel), hex.EncodeToString(sum[:]), now)
		}
	}
	if err != nil {
		logger.Warnf("Failed to keep a profile photo: %v", err)
	}
	store.photoChecked(jidStr, now)
	return 0
}

// fetchPhoto downloads a photo from WhatsApp's URL (a plain request, as whatsmeow says).
func fetchPhoto(ctx context.Context, url string) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("not a photo's address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := photoHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, photoLimit+1))
	if err == nil && int64(len(data)) > photoLimit {
		err = fmt.Errorf("larger than %d bytes", photoLimit)
	}
	return data, err
}

// Photos asks for the photos as they are due, until ctx ends; wake (a changed photo) cuts a pause
// short.
type Photos struct {
	wake chan struct{}
}

func startPhotos(ctx context.Context, client photoAsker, store *MessageStore, own string, logger waLog.Logger) *Photos {
	p := &Photos{wake: make(chan struct{}, 1)}
	if own != "" {
		store.photoDue(own, false) // the account's own, as anyone's
	}
	go func() {
		wait := photoPause
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
			case <-time.After(wait):
			}
			jid := store.nextPhoto(time.Now())
			if jid == "" {
				wait = photoIdle
				continue
			}
			wait = max(askPhoto(ctx, client, store, jid, logger), photoPause)
		}
	}()
	return p
}

// changed is WhatsApp's word that a photo changed: a removal recorded at once, a new photo asked for
// first.
func (p *Photos) changed(store *MessageStore, v *events.Picture) {
	jid := v.JID.ToNonAD().String()
	if v.Remove {
		at := v.Timestamp.Unix()
		if v.Timestamp.IsZero() {
			at = time.Now().Unix()
		}
		store.notePhoto(jid, "", "", "", at)
		store.photoChecked(jid, at)
		return
	}
	store.photoDue(jid, true)
	if p != nil {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}
