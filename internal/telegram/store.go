// The store, `<cache>/telegram/telegram.db` (internal/telegramstore), as the sync and the live
// connection write it.
package telegram

import (
	"database/sql"
	"path/filepath"

	"everysaid/internal/db"
	"everysaid/internal/telegramstore"
)

// Folder is where the Telegram store and its media are.
func Folder() string    { return filepath.Dir(telegramstore.DB()) }
func DBPath() string    { return telegramstore.DB() }
func MediaPath() string { return telegramstore.Media() }

// openStore opens (making it if new) the store at path; one connection, so that its writes are
// in one transaction at a time as Python's sqlite3 connection makes them.
func openStore(path string) (*sql.DB, error) {
	d, err := telegramstore.Open(path)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	return d, nil
}

// noteRead is telegramstore.NoteRead, its failure a panic as the helpers of db make it.
func noteRead(q db.Querier, chatID int64, inbox, outbox *int64, seenNow bool) bool {
	moved, err := telegramstore.NoteRead(q, chatID, inbox, outbox, seenNow)
	if err != nil {
		panic(err)
	}
	return moved
}

func intp[T int | int64](v T) *int64 { n := int64(v); return &n }
