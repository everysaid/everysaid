package whatsapp

// Ports bridges/whatsapp/media.go (and MediaDownloader of main.go).

// The files of messages: downloaded as messages arrive ([whatsapp] download in config.toml, on by
// default) or when asked (Bridge.Download), into <store>/media/<chat>/<message id><ext>. messages.media_path records the
// file (relative to the store), media_error why it could not be had. View-once media are left alone,
// as the sender meant them.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// How far back the bridge looks, at each start, for media it has not downloaded yet.
const downloadBackfill = 7 * 24 * time.Hour

var mediaTypes = map[string]whatsmeow.MediaType{
	"image": whatsmeow.MediaImage, "sticker": whatsmeow.MediaImage, "video": whatsmeow.MediaVideo,
	"audio": whatsmeow.MediaAudio, "document": whatsmeow.MediaDocument,
}

var mediaExtensions = map[string]string{"image": ".jpg", "sticker": ".webp", "video": ".mp4", "audio": ".ogg"}

// safeName keeps a jid or a message id usable as a file name everywhere.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ':', '/', '\\', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, s)
}

func mediaFile(mediaType, filename, chatJID, id string) string {
	ext, ok := mediaExtensions[mediaType]
	if !ok {
		ext = strings.ToLower(filepath.Ext(filename))
	}
	return filepath.Join("media", safeName(chatJID), safeName(id)+ext)
}

// writeMedia puts a file in its place whole (a temporary file renamed), private to the user.
func (store *MessageStore) writeMedia(rel string, data []byte) error {
	path := filepath.Join(store.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// download returns the absolute path of a message's file, downloading it unless it is there.
func (store *MessageStore) download(client *whatsmeow.Client, id, chatJID string) (string, error) {
	var mediaType, filename, url, directPath, mediaPath sql.NullString
	var mediaKey, fileSHA256, fileEncSHA256 []byte
	var fileLength sql.NullInt64
	err := store.db.QueryRow(`SELECT media_type, filename, url, direct_path, media_key, file_sha256, file_enc_sha256,
		file_length, media_path FROM messages WHERE id = ? AND chat_jid = ?`, id, chatJID).Scan(
		&mediaType, &filename, &url, &directPath, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength, &mediaPath)
	if err != nil {
		return "", fmt.Errorf("no such message: %v", err)
	}
	if mediaPath.String != "" {
		if path, err := filepath.Abs(filepath.Join(store.dir, mediaPath.String)); err == nil {
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}
	waType, ok := mediaTypes[mediaType.String]
	if !ok {
		return "", fmt.Errorf("not a media message")
	}
	if (url.String == "" && directPath.String == "") || len(mediaKey) == 0 || len(fileEncSHA256) == 0 {
		return "", fmt.Errorf("incomplete media information for download")
	}
	d := &MediaDownloader{URL: url.String, DirectPath: directPath.String, MediaKey: mediaKey,
		FileLength: uint64(fileLength.Int64), FileSHA256: fileSHA256, FileEncSHA256: fileEncSHA256, MediaType: waType}
	if d.DirectPath == "" {
		d.DirectPath = extractDirectPathFromURL(url.String)
	}
	data, err := client.Download(context.Background(), d)
	if err != nil {
		store.db.Exec("UPDATE messages SET media_error = ? WHERE id = ? AND chat_jid = ?", err.Error(), id, chatJID)
		return "", fmt.Errorf("failed to download media: %v", err)
	}
	rel := mediaFile(mediaType.String, filename.String, chatJID, id)
	if err := store.writeMedia(rel, data); err != nil {
		return "", fmt.Errorf("failed to save media file: %v", err)
	}
	store.db.Exec("UPDATE messages SET media_path = ?, media_error = NULL WHERE id = ? AND chat_jid = ?", rel, id, chatJID)
	return filepath.Abs(filepath.Join(store.dir, rel))
}

type mediaJob struct{ id, chatJID string }

// Downloads fetches files one at a time, in the order messages arrived.
type Downloads struct {
	client *whatsmeow.Client
	store  *MessageStore
	logger waLog.Logger
	jobs   chan mediaJob
	done   chan struct{} // closed when the connection ends: what is queued waits for the next start
}

func startDownloads(client *whatsmeow.Client, store *MessageStore, logger waLog.Logger) *Downloads {
	d := &Downloads{client: client, store: store, logger: logger, jobs: make(chan mediaJob, 1000), done: make(chan struct{})}
	go d.run()
	// what arrived while it was not running, or before this version
	rows, err := store.db.Query(`SELECT id, chat_jid FROM messages WHERE coalesce(media_type, '') != ''
		AND coalesce(media_path, '') = '' AND media_error IS NULL AND coalesce(subtype, '') != 'view_once'
		AND timestamp > ? ORDER BY timestamp`, time.Now().Add(-downloadBackfill))
	if err == nil {
		var jobs []mediaJob
		for rows.Next() {
			var j mediaJob
			if rows.Scan(&j.id, &j.chatJID) == nil {
				jobs = append(jobs, j)
			}
		}
		rows.Close()
		for _, j := range jobs {
			d.queue(j.id, j.chatJID)
		}
	}
	return d
}

// queue asks for a message's file; a message without one, or with one already, is passed over.
// When the queue is full the job is dropped: the next start picks it up.
func (d *Downloads) queue(id, chatJID string) {
	select {
	case <-d.done:
	case d.jobs <- mediaJob{id, chatJID}:
	default:
	}
}

func (d *Downloads) wanted(j mediaJob) bool {
	var n int
	d.store.db.QueryRow(`SELECT count(*) FROM messages WHERE id = ? AND chat_jid = ? AND coalesce(media_type, '') != ''
		AND coalesce(media_path, '') = '' AND coalesce(subtype, '') != 'view_once'`, j.id, j.chatJID).Scan(&n)
	return n > 0
}

// stop ends the downloads after the one going on.
func (d *Downloads) stop() { close(d.done) }

func (d *Downloads) run() {
	for {
		var j mediaJob
		select {
		case <-d.done:
			return
		case j = <-d.jobs:
		}
		if !d.wanted(j) {
			continue
		}
		var err error
		for attempt, wait := 1, 10*time.Second; ; attempt, wait = attempt+1, wait*3 {
			if _, err = d.store.download(d.client, j.id, j.chatJID); err == nil || attempt == 3 {
				break
			}
			select {
			case <-d.done:
				return
			case <-time.After(wait):
			}
		}
		if err != nil {
			d.logger.Warnf("Media of %s not downloaded: %v", j.id, err)
		}
	}
}

// MediaDownloader is a message's file as whatsmeow downloads it (whatsmeow.DownloadableMessage).
type MediaDownloader struct {
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileLength    uint64
	FileSHA256    []byte
	FileEncSHA256 []byte
	MediaType     whatsmeow.MediaType
}

func (d *MediaDownloader) GetDirectPath() string             { return d.DirectPath }
func (d *MediaDownloader) GetURL() string                    { return d.URL }
func (d *MediaDownloader) GetMediaKey() []byte               { return d.MediaKey }
func (d *MediaDownloader) GetFileLength() uint64             { return d.FileLength }
func (d *MediaDownloader) GetFileSHA256() []byte             { return d.FileSHA256 }
func (d *MediaDownloader) GetFileEncSHA256() []byte          { return d.FileEncSHA256 }
func (d *MediaDownloader) GetMediaType() whatsmeow.MediaType { return d.MediaType }

// extractDirectPathFromURL is the path of a file on WhatsApp's servers, from its URL
// (https://mmg.whatsapp.net/v/t62.7118-24/..._n.enc?ccb=11-4&oh=...).
func extractDirectPathFromURL(url string) string {
	parts := strings.SplitN(url, ".net/", 2)
	if len(parts) < 2 {
		return url
	}
	// Keep the query: oh/oe are the access signature, without them the CDN answers 403.
	// Only mms3=true is not part of the direct path.
	return "/" + strings.Replace(parts[1], "&mms3=true", "", 1)
}
