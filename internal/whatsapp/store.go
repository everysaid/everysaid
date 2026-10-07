// Package whatsapp is WhatsApp inside Everysaid's process: a whatsmeow client linked to the account
// as a device (like WhatsApp Web), keeping what arrives in the store folder for the importers, and
// the `whatsapp-bridge` plugin that runs it.
//
// It ports the standalone bridge (bridges/whatsapp/*.go, a separate program with a REST API) and the
// Python plugin WhatsappBridge (everysaid/plugins/sources.py). The store folder and its files are the
// bridge's, unchanged: `whatsapp.db` (whatsmeow's device store: the session's keys), `messages.db`
// (what arrived, read by the importer) and `media/`. A device the bridge linked keeps working, with no
// new pairing; the bridge and this package must never run on the same store at once (the second
// connection would take the session over).
//
// SQLite is modernc.org/sqlite (no cgo) where the bridge had mattn/go-sqlite3: times are written in
// mattn's format (`_time_format=sqlite`), booleans as 0/1, nil byte slices as NULL, as before.
package whatsapp

// Ports bridges/whatsapp/main.go (MessageStore, the chats' names, history sync).

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/db"
)

// dsn opens a store's database as the bridge did with mattn's driver: foreign keys on, a wait of
// 5 s on a lock (mattn's default), times written as mattn wrote them.
func dsn(path string) string {
	return db.URI(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_time_format=sqlite"
}

// MessageStore is messages.db, in the store folder dir.
type MessageStore struct {
	db   *sql.DB
	dir  string
	said func(event, code, detail string) // the connection's events, as they are recorded (the bridge printed them)
}

// OpenMessages opens (and makes, or brings up to date) the store folder's messages.db.
func OpenMessages(dir string) (*MessageStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %v", err)
	}
	d, err := sql.Open("sqlite", dsn(filepath.Join(dir, "messages.db")))
	if err != nil {
		return nil, fmt.Errorf("failed to open message database: %v", err)
	}
	store := &MessageStore{db: d, dir: dir}
	if err := store.create(); err != nil {
		d.Close()
		return nil, err
	}
	private(dir, filepath.Join(dir, "messages.db"))
	return store, nil
}

// create makes the tables, and adds what this version keeps to a store made by an older one.
func (store *MessageStore) create() error {
	_, err := store.db.Exec(`
		CREATE TABLE IF NOT EXISTS chats (
			jid TEXT PRIMARY KEY,
			name TEXT,
			last_message_time TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS messages (
			id TEXT,
			chat_jid TEXT,
			sender TEXT,
			content TEXT,
			timestamp TIMESTAMP,
			is_from_me BOOLEAN,
			media_type TEXT,
			filename TEXT,
			url TEXT,
			media_key BLOB,
			file_sha256 BLOB,
			file_enc_sha256 BLOB,
			file_length INTEGER,
			PRIMARY KEY (id, chat_jid),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid)
		);
	`)
	if err != nil {
		return fmt.Errorf("failed to create tables: %v", err)
	}
	if err := store.migrate(); err != nil {
		return fmt.Errorf("failed to update tables: %v", err)
	}
	if err := store.migrateStatus(); err != nil {
		return fmt.Errorf("failed to create status tables: %v", err)
	}
	if err := store.migrateGroups(); err != nil {
		return fmt.Errorf("failed to create group tables: %v", err)
	}
	if err := store.migrateReceipts(); err != nil {
		return fmt.Errorf("failed to create receipt tables: %v", err)
	}
	return nil
}

func (store *MessageStore) Close() error { return store.db.Close() }

// StoreChat records a chat.
func (store *MessageStore) StoreChat(jid, name string, lastMessageTime time.Time) error {
	_, err := store.db.Exec("INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		jid, name, lastMessageTime)
	return err
}

// chatName determines a chat's name: the one it has, else (a group) the conversation's or the
// group's, else (a person) their address-book name, the sender's, the jid's user.
func chatName(client *whatsmeow.Client, store *MessageStore, jid types.JID, chatJID string, conversation any, sender string, logger waLog.Logger) string {
	var existingName string
	err := store.db.QueryRow("SELECT name FROM chats WHERE jid = ?", chatJID).Scan(&existingName)
	// A name equal to the bare JID user is the fallback placeholder, not a real name.
	if err == nil && existingName != "" && existingName != jid.User {
		return existingName
	}
	var name string
	if jid.Server == "g.us" {
		if conversation != nil {
			var displayName, convName *string
			v := reflect.ValueOf(conversation)
			if v.Kind() == reflect.Ptr && !v.IsNil() {
				v = v.Elem()
				if f := v.FieldByName("DisplayName"); f.IsValid() && f.Kind() == reflect.Ptr && !f.IsNil() {
					dn := f.Elem().String()
					displayName = &dn
				}
				if f := v.FieldByName("Name"); f.IsValid() && f.Kind() == reflect.Ptr && !f.IsNil() {
					n := f.Elem().String()
					convName = &n
				}
			}
			if displayName != nil && *displayName != "" {
				name = *displayName
			} else if convName != nil && *convName != "" {
				name = *convName
			}
		}
		if name == "" {
			groupInfo, err := client.GetGroupInfo(context.Background(), jid)
			if err == nil && groupInfo.Name != "" {
				name = groupInfo.Name
			} else {
				name = fmt.Sprintf("Group %s", jid.User)
			}
		}
	} else {
		if contactName := lookupContactName(client, jid); contactName != "" {
			name = contactName
		} else if sender != "" {
			name = sender
		} else {
			name = jid.User
		}
	}
	return name
}

// lookupContactName returns the address-book name of a person. Chats keyed by a LID carry
// no contact info of their own: the name is stored under the phone number, so resolve it.
func lookupContactName(client *whatsmeow.Client, jid types.JID) string {
	ctx := context.Background()
	if contact, err := client.Store.Contacts.GetContact(ctx, jid); err == nil && contact.FullName != "" {
		return contact.FullName
	}
	if jid.Server == types.HiddenUserServer {
		if pn, err := client.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
			if contact, err := client.Store.Contacts.GetContact(ctx, pn); err == nil && contact.FullName != "" {
				return contact.FullName
			}
		}
	}
	return ""
}

// nameLIDChats gives a contact name to LID chats still stored under the placeholder name.
func nameLIDChats(client *whatsmeow.Client, store *MessageStore, logger waLog.Logger) {
	rows, err := store.db.Query("SELECT jid, name FROM chats WHERE jid LIKE '%@lid'")
	if err != nil {
		return
	}
	updates := map[string]string{}
	for rows.Next() {
		var chatJID, name string
		if rows.Scan(&chatJID, &name) != nil {
			continue
		}
		jid, err := types.ParseJID(chatJID)
		if err != nil || (name != "" && name != jid.User) {
			continue
		}
		if contactName := lookupContactName(client, jid); contactName != "" {
			updates[chatJID] = contactName
		}
	}
	rows.Close()
	for chatJID, name := range updates {
		store.db.Exec("UPDATE chats SET name = ? WHERE jid = ?", name, chatJID)
	}
	logger.Infof("Named %d LID chats from contacts", len(updates))
}

// handleHistorySync stores each message of a history sync as a live one would be, with the
// reactions and receipts it carries.
func handleHistorySync(client *whatsmeow.Client, store *MessageStore, historySync *events.HistorySync, logger waLog.Logger) {
	synced := 0
	for _, conversation := range historySync.Data.Conversations {
		if conversation.ID == nil {
			continue
		}
		chatJID := *conversation.ID
		jid, err := types.ParseJID(chatJID)
		if err != nil {
			logger.Warnf("Failed to parse JID %s: %v", chatJID, err)
			continue
		}
		if isChannel(jid) {
			continue
		}
		name := chatName(client, store, jid, chatJID, conversation, "", logger)
		for _, msg := range conversation.Messages {
			if msg == nil || msg.Message == nil {
				continue
			}
			evt, err := client.ParseWebMessage(jid, msg.Message)
			if err != nil {
				logger.Warnf("Failed to parse history message: %v", err)
				continue
			}
			processMessage(client, store, evt, name, logger)
			historyReactions(client, store, jid, msg.Message, logger)
			historyReceipts(store, jid, msg.Message)
			synced++
		}
	}
	logger.Infof("History sync: %d messages in %d conversations", synced, len(historySync.Data.Conversations))
}
