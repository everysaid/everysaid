package importers

// The previews of links WhatsApp's apps made (the sender's app: title, description, picture), as
// the iPhone keeps them (ZWAMESSAGEDATAITEM, the media item of a link message) and as the bridge
// kept them (messages.preview_*). WhatsApp names no site.

import (
	"cmp"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"everysaid/internal/archive"
)

// WhatsAppPreviewFolder is where iphone-sync puts the previews' pictures, beside the iPhone's
// WhatsApp database: <folder>/<path in WhatsApp's Media/>.
const WhatsAppPreviewFolder = "whatsapp-previews"

// iphonePreviewsSQL: each message's first link (ZINDEX 0) and, for a link message (type 7), its
// media item, which says the same (the first link's title, its URL, its picture).
const iphonePreviewsSQL = "SELECT m.Z_PK AS pk, d.ZCONTENT1 AS url, d.ZMATCHEDTEXT AS matched, d.ZTITLE AS title, " +
	"d.ZSUMMARY AS summary, d.ZTHUMBNAILPATH AS thumb, i.ZTITLE AS mtitle, i.ZMEDIAURL AS murl, i.ZXMPPTHUMBPATH AS mthumb " +
	"FROM ZWAMESSAGE m LEFT JOIN ZWAMESSAGEDATAITEM d ON d.ZMESSAGE = m.Z_PK AND d.ZINDEX = 0 " +
	"LEFT JOIN ZWAMEDIAITEM i ON i.Z_PK = m.ZMEDIAITEM AND m.ZMESSAGETYPE = 7 " +
	"WHERE d.Z_PK IS NOT NULL OR m.ZMESSAGETYPE = 7 ORDER BY m.Z_PK, d.Z_PK"

// whatsappPreviewRows are the rows of iphonePreviewsSQL; none where the database has no data items.
func whatsappPreviewRows(iphone *sql.DB) []row {
	if !hasTable(iphone, "ZWAMESSAGEDATAITEM") || !columns(iphone, "ZWAMEDIAITEM")["ZXMPPTHUMBPATH"] {
		return nil
	}
	return maps(iphone, iphonePreviewsSQL)
}

// previewThumb is a preview's picture's path under WhatsApp's Media/ (the folder's), "" for none.
func previewThumb(r row) string {
	p := str(r["thumb"])
	if p == "" {
		p = str(r["mthumb"])
	}
	if rel, ok := strings.CutPrefix(p, "Media/"); ok && rel != "" {
		return rel
	}
	return ""
}

// WhatsAppPreviewThumbs is the previews' pictures the iPhone's WhatsApp database names, as paths
// under its Media/ folder: what iphone-sync takes out of the backup (no other .thumb: a picture's
// own thumbnail is not a preview).
func WhatsAppPreviewThumbs(database string) map[string]bool {
	out := map[string]bool{}
	if !exists(database) {
		return out
	}
	d := ro(database)
	defer d.Close()
	for _, r := range whatsappPreviewRows(d) {
		if p := previewThumb(r); p != "" {
			out[p] = true
		}
	}
	return out
}

// iphonePreviews is message Z_PK -> its link's preview, the picture read from folder (where
// iphone-sync put it).
func iphonePreviews(iphone *sql.DB, folder string) map[int64]*archive.Preview {
	out := map[int64]*archive.Preview{}
	for _, r := range whatsappPreviewRows(iphone) {
		pk := toInt(r["pk"])
		if _, seen := out[pk]; seen {
			continue
		}
		url := strings.TrimSpace(cmp.Or(str(r["url"]), str(r["matched"]), str(r["murl"])))
		p := &archive.Preview{URL: url, Title: cmp.Or(str(r["title"]), str(r["mtitle"]))}
		// with no description the app keeps the link there
		if s := strings.TrimSpace(str(r["summary"])); s != url && s != strings.TrimSpace(str(r["matched"])) {
			p.Description = s
		}
		if rel := previewThumb(r); rel != "" {
			path := filepath.Join(append([]string{folder}, strings.Split(rel, "/")...)...)
			if waPreviewInside(folder, path) {
				p.Image, _ = os.ReadFile(path)
			}
		}
		if p.Title != "" || p.Description != "" || len(p.Image) > 0 {
			out[pk] = p
		}
	}
	return out
}

// bridgePreview is the preview the bridge kept of a message: its picture the high-quality one it
// downloaded, else the small one the message carried; nil for none.
func bridgePreview(r row) *archive.Preview {
	p := &archive.Preview{URL: str(r["preview_url"]), Title: str(r["preview_title"]),
		Description: str(r["preview_description"]), Image: asBytes(r["preview_image"])}
	if len(p.Image) == 0 {
		p.Image = asBytes(r["preview_thumb"])
	}
	if p.Title == "" && p.Description == "" && len(p.Image) == 0 {
		return nil
	}
	return p
}

// setPreview brings the preview of a message the archive had already up to date (true: changed).
// keepImage: a picture already kept stays, this one is not read again (the iPhone's never change).
func setPreview(a *archive.Archive, mid int64, p *archive.Preview, keepImage bool) bool {
	if keepImage && a.Exists("SELECT 1 FROM link_preview WHERE message_id = ? AND image IS NOT NULL", mid) {
		q := *p
		q.Image = nil
		p = &q
	}
	return a.SetPreview(mid, p)
}

// waPreviewInside: path is within root (a database's path cannot lead out of it).
func waPreviewInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
