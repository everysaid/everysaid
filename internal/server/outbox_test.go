package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	"everysaid/internal/core"
	"everysaid/internal/db"
)

// A kept file without text that went after all (the service said it failed) is known by its
// content, as a text is by its words; another file is not.
func TestArrivedKnowsAFileWithoutText(t *testing.T) {
	c := newServer(t)
	s := c.s.Host.Store()
	q := s.Read()
	conv := db.Int(q, "SELECT conversation_id FROM message WHERE outgoing LIMIT 1")
	chat := core.ChatOfConversation(s, conv)
	must(t, chat != "", "a chat with the user's messages")
	file := []byte("a document sent")
	sum := sha256.Sum256(file)
	sha := hex.EncodeToString(sum[:])
	created := time.Now().UnixMilli()
	k := outboxRequest{FileName: "report.rtf", FileType: "application/rtf"}
	must(t, !c.s.Host.arrived(chat, k, file, created), "not there yet")

	s.MustWrite(func(tx *sql.Tx) {
		r := db.Exec(tx, "INSERT INTO message (service_id, conversation_id, ts, outgoing, kind_id, text) "+
			"SELECT service_id, conversation_id, ?, 1, kind_id, NULL FROM message WHERE conversation_id = ? LIMIT 1",
			created+500, conv)
		mid, _ := r.LastInsertId()
		db.Exec(tx, "INSERT INTO media (sha256, size, mime, path) VALUES (?, ?, 'application/rtf', 'media/x.rtf')", sha, len(file))
		db.Exec(tx, "INSERT INTO attachment (message_id, sha256, source_id, source_path, name) "+
			"SELECT ?, ?, id, 'x.rtf', 'report.rtf' FROM source LIMIT 1", mid, sha)
	})
	must(t, c.s.Host.arrived(chat, k, file, created), "the file, there")
	must(t, !c.s.Host.arrived(chat, k, []byte("another document"), created), "another file")
	must(t, !c.s.Host.arrived(chat, k, file, created+60_000), "the same file, sent before this one was written")
}
