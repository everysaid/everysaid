package whatsapp

// The plugin's way into the archive: what the connection brings, written as it comes (writeTo), and
// what it keeps of chats and people in keys.db, its downloads and its profile photos, brought in
// from time to time (runImport).

import (
	"context"
	"database/sql"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/importers"
	"everysaid/internal/keys"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

// writer is the instance's writer into an archive, its source tied to the instance (so that the
// chats' state it reports is the instance's from the first).
func writer(c *plugins.Context, a *archive.Archive, keys *sql.DB) *importers.WhatsAppWriter {
	w := importers.NewWhatsAppWriter(a, keys, key(storeDir(c)))
	a.Exec("UPDATE source SET instance_id = ? WHERE id = ? AND instance_id IS NULL", c.ID, w.Source)
	return w
}

// writeTo is how the instance's connection writes into the archive: under the import lock, a little
// at a time (the archive breathes), committed at the end.
func writeTo(c *plugins.Context) Writer {
	return func(fn func(w *importers.WhatsAppWriter) error) error {
		d, err := keys.Open() // its tables made when the connection opened its store
		if err != nil {
			return err
		}
		_, _, err = sourcekit.RunImporters(c, []sourcekit.Step{{Label: "WhatsApp live", Run: func(a *archive.Archive, out func(string)) error {
			w := writer(c, a, d)
			if err := fn(w); err != nil {
				return err
			}
			w.Done()
			return nil
		}}})
		return err
	}
}

// runImport brings what the connection keeps of chats and people, the files it downloaded and the
// profile photos it asked for; and lets go of what it no longer needs.
func runImport(c *plugins.Context) error {
	keys, err := Devices(context.Background()) // whatsmeow's tables made, also before a first connection
	if err != nil {
		return err
	}
	if err := importers.WhatsAppTables(keys); err != nil {
		return err
	}
	dir := storeDir(c)
	own := DeviceJID(keys, dir)
	changed := false
	_, _, err = sourcekit.RunImporters(c, []sourcekit.Step{
		{Label: "WhatsApp (bridge)", Run: func(a *archive.Archive, out func(string)) error {
			updated, err := importers.WhatsAppState(writer(c, a, keys), own)
			changed = changed || len(updated) > 0
			return err
		}},
		{Label: "files", Run: func(a *archive.Archive, out func(string)) error {
			writer(c, a, keys).Files(dir)
			return importers.Media(a, out, importers.PhotosWhatsAppLive(keys, key(dir), dir))
		}},
	})
	if err != nil {
		return err
	}
	prune(keys, key(dir))
	if changed { // edits, deletions, reactions on messages already shown
		c.Emit(M{"type": "changed"})
	}
	return nil
}

// prune lets go of what the connection no longer needs: its record of messages older than a week
// (it sends, edits, deletes and marks read within that), sends older than a day (the limits of
// pace), and the files that could not be had, a week on.
func prune(keys *sql.DB, store string) {
	week := time.Now().Add(-readBack).UnixMilli()
	keys.Exec("DELETE FROM whatsapp_recent WHERE store = ? AND at < ?", store, week)
	keys.Exec("DELETE FROM whatsapp_sent WHERE store = ? AND at < ?", store, time.Now().Add(-24*time.Hour).Unix())
	keys.Exec("DELETE FROM whatsapp_media WHERE store = ? AND error IS NOT NULL AND coalesce(path, '') = '' "+
		"AND coalesce(preview_direct_path, '') = '' AND at < ?", store, week)
}
