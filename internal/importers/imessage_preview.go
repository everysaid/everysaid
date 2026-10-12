package importers

// iMessage's link previews: a link sent alone is a "URL balloon" (balloon_bundle_id
// com.apple.messages.URLBalloonProvider), its preview in payload_data, an NSKeyedArchiver plist of
// a RichLink whose richLinkMetadata is Apple's LPLinkMetadata: the link (originalURL, as sent; URL,
// as resolved), title, summary, siteName. Its pictures (image, icon, ...) are files beside the
// message's own (.pluginPayloadAttachment), each named by its place among them
// (richLinkImageAttachmentSubstituteIndex). The sync copies only the image's (IMessagePreviewFiles);
// they are never files of the message (SMSAttachment leaves them out), only its preview's picture.

import (
	"cmp"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"howett.net/plist"

	"everysaid/internal/archive"
	"everysaid/internal/db"
)

const urlBalloon = "com.apple.messages.URLBalloonProvider"

// imessageLink is what a URL balloon's payload says: the preview, and the place of its picture
// among the message's payload files (-1: none, or its bytes in image where the payload has them).
type imessageLink struct {
	preview archive.Preview
	index   int
}

// imessagePreview reads a URL balloon's payload_data; false for none (or one that cannot be read).
func imessagePreview(blob []byte) (imessageLink, bool) {
	out := imessageLink{index: -1}
	if len(blob) == 0 {
		return out, false
	}
	var top map[string]any
	if _, err := plist.Unmarshal(blob, &top); err != nil {
		return out, false
	}
	objects, _ := top["$objects"].([]any)
	get := func(v any) any {
		if uid, ok := v.(plist.UID); ok {
			if uint64(uid) == 0 || uint64(uid) >= uint64(len(objects)) {
				return nil // 0 is $null
			}
			return objects[uid]
		}
		return v
	}
	dict := func(v any) map[string]any { d, _ := get(v).(map[string]any); return d }
	text := func(v any) string {
		switch x := get(v).(type) {
		case string:
			return x
		case map[string]any: // an NSString, an NSURL (relative to its base, none in these)
			if s, ok := get(x["NS.string"]).(string); ok {
				return s
			}
			s, _ := get(x["NS.relative"]).(string)
			return s
		}
		return ""
	}
	topRef, _ := top["$top"].(map[string]any)
	md := dict(topRef["root"])
	if inner := dict(md["richLinkMetadata"]); inner != nil { // a RichLink
		md = inner
	}
	if md == nil {
		return out, false
	}
	out.preview = archive.Preview{URL: strings.TrimSpace(cmp.Or(text(md["originalURL"]), text(md["URL"]))),
		Site: strings.TrimSpace(text(md["siteName"])), Title: strings.TrimSpace(text(md["title"])),
		Description: strings.TrimSpace(text(md["summary"]))}
	if img := dict(md["image"]); img != nil {
		if i, _, isInt, ok := num(img["richLinkImageAttachmentSubstituteIndex"]); ok && isInt && i >= 0 {
			out.index = int(i)
		} else if b, ok := get(img["data"]).([]byte); ok { // an LPImage with its bytes
			out.preview.Image = b
		} else if d := dict(img["data"]); d != nil {
			out.preview.Image, _ = get(d["NS.data"]).([]byte)
		}
	}
	return out, out.preview.URL != "" || out.preview.Title != ""
}

// imessagePreviews is each URL balloon's preview in sms.db (d), by message ROWID, with the path of
// its picture within the attachments' folder ("" for none); none from an sms.db of an iOS without
// balloons (or attachments).
func imessagePreviews(d db.Querier) (out map[int64]imessagePreviewRow) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	files := map[int64][]string{} // each message's payload files, in their order
	for _, r := range maps(d, "SELECT j.message_id, a.filename FROM message_attachment_join j JOIN attachment a ON "+
		"a.ROWID = j.attachment_id JOIN message m ON m.ROWID = j.message_id WHERE m.balloon_bundle_id = ? AND "+
		"a.filename LIKE '%.pluginPayloadAttachment' ORDER BY j.message_id, a.ROWID", urlBalloon) {
		id := toInt(r["message_id"])
		files[id] = append(files[id], str(r["filename"]))
	}
	out = map[int64]imessagePreviewRow{}
	for _, r := range maps(d, "SELECT ROWID, payload_data FROM message WHERE balloon_bundle_id = ? AND payload_data IS NOT NULL",
		urlBalloon) {
		l, ok := imessagePreview(asBytes(r["payload_data"]))
		if !ok {
			continue
		}
		id, rel := toInt(r["ROWID"]), ""
		if fs := files[id]; l.index >= 0 && l.index < len(fs) {
			rel = previewAttachment(fs[l.index])
		}
		out[id] = imessagePreviewRow{l.preview, rel}
	}
	return out
}

type imessagePreviewRow struct {
	preview archive.Preview
	file    string
}

// previewAttachment is a payload file's path within the attachments' folder ("" for one elsewhere).
func previewAttachment(filename string) string {
	for _, root := range []string{"~/Library/SMS/Attachments/", "/var/mobile/Library/SMS/Attachments/"} {
		if rel, ok := strings.CutPrefix(filename, root); ok && strings.HasSuffix(rel, ".pluginPayloadAttachment") &&
			!strings.Contains("/"+rel+"/", "/../") {
			return rel
		}
	}
	return ""
}

// IMessagePreviewFiles are the pictures of iMessage's link previews in sms.db (d), as paths within
// the attachments' folder: what the sync copies besides the messages' files.
func IMessagePreviewFiles(d *sql.DB) (out []string) {
	for _, p := range imessagePreviews(d) {
		if p.file != "" {
			out = append(out, p.file)
		}
	}
	return out
}

// withPicture is the preview with its picture read from the attachments' folder (media), where the
// sync copied it.
func (p imessagePreviewRow) withPicture(media string) *archive.Preview {
	out := p.preview
	if p.file != "" {
		if b, err := os.ReadFile(filepath.Join(media, filepath.FromSlash(p.file))); err == nil {
			out.Image = b
		}
	}
	return &out
}
