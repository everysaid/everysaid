// Package whatsapp is WhatsApp inside Everysaid's process: a whatsmeow client linked to the account
// as a device (like WhatsApp Web), writing what arrives straight into the archive, and the
// `whatsapp-bridge` plugin that runs it.
//
// Everything of it is in keys.db (internal/keys): whatsmeow's device store (the session's keys,
// devices.go) and what the connection needs to go on, in tables of the prefix whatsapp_
// (importers.WhatsAppSchema), none of them a message's content. The store folder holds only the
// files on their way into the archive: downloads (media/) and profile photos (photos/). Two
// connections on one device must never run at once (the second would take the session over).
package whatsapp

// Store, the chats' names, history sync.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/db"
	"everysaid/internal/importers"
	"everysaid/internal/keys"
)

// Writer writes into the archive, under its import lock: fn is given the store folder's writer, and
// what it wrote is committed once it returns nil (the plugin's, sourcekit.RunImporters).
type Writer func(fn func(w *importers.WhatsAppWriter) error) error

// Store is a store folder's part of keys.db, and its way into the archive.
type Store struct {
	db    *sql.DB // keys.db: shared, never closed here
	key   string  // the store folder, as keys.db names it
	dir   string
	said  func(event, code, detail string) // the connection's events, as they are recorded
	write Writer
}

// OpenStore opens a store folder's part of keys.db (its tables made where they are not yet).
func OpenStore(dir string, write Writer) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %v", err)
	}
	os.Chmod(dir, 0700) // the files on their way into the archive are the user's alone
	d, err := keys.Open()
	if err != nil {
		return nil, err
	}
	if err := importers.WhatsAppTables(d); err != nil {
		return nil, fmt.Errorf("failed to create tables: %v", err)
	}
	return &Store{db: d, key: key(dir), dir: dir, write: write}, nil
}

// Write writes a batch into the archive; false where it could not be (WhatsApp is then not told it
// arrived, and gives it again).
func (store *Store) Write(b *importers.WABatch, logger waLog.Logger) bool {
	if b.Empty() {
		return true
	}
	if store.write == nil {
		logger.Errorf("Nowhere to write what arrived")
		return false
	}
	if err := store.write(func(w *importers.WhatsAppWriter) error { w.Write(b); return nil }); err != nil {
		logger.Errorf("Failed to write into the archive: %v", err)
		return false
	}
	return true
}

// exec runs a statement on the store's part of keys.db; a failure is said, not fatal (what it
// keeps is the connection's own bookkeeping).
func (store *Store) exec(query string, args ...any) {
	if _, err := store.db.Exec(query, args...); err != nil && store.said != nil {
		store.said("store_error", "", err.Error())
	}
}

// storeChat records a chat's name and its newest message's time (history arrives newest first).
func (store *Store) storeChat(jid, name string, at time.Time) {
	store.exec(`INSERT INTO whatsapp_chat (store, jid, name, last_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (store, jid) DO UPDATE SET name = coalesce(nullif(excluded.name, ''), whatsapp_chat.name),
		last_at = max(coalesce(whatsapp_chat.last_at, 0), excluded.last_at)`, store.key, jid, name, at.UnixMilli())
}

// incoming records that the other side wrote in a chat (the bridge sends only there).
func (store *Store) incoming(jid string) {
	store.exec(`INSERT INTO whatsapp_chat (store, jid, incoming) VALUES (?, ?, 1)
		ON CONFLICT (store, jid) DO UPDATE SET incoming = 1`, store.key, jid)
}

// chatNameOf is the name the store has for a chat ("" for none).
func (store *Store) chatNameOf(jid string) string {
	var name sql.NullString
	store.db.QueryRow("SELECT name FROM whatsapp_chat WHERE store = ? AND jid = ?", store.key, jid).Scan(&name)
	return name.String
}

// chatName determines a chat's name: the one it has, else (a group) the conversation's or the
// group's, else (a person) their address-book name, the sender's, the jid's user.
func chatName(client *whatsmeow.Client, store *Store, jid types.JID, chatJID string, conversation any, sender string, logger waLog.Logger) string {
	existingName := store.chatNameOf(chatJID)
	// A name equal to the bare JID user is the fallback placeholder, not a real name.
	if existingName != "" && existingName != jid.User {
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
		if name == "" && client != nil {
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
	if client == nil {
		return ""
	}
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
func nameLIDChats(client *whatsmeow.Client, store *Store, logger waLog.Logger) {
	updates := map[string]string{}
	func() {
		defer db.Recover(new(error))
		db.Each(store.db, "SELECT jid, coalesce(name, '') FROM whatsapp_chat WHERE store = ? AND jid LIKE '%@lid'",
			[]any{store.key}, func(scan func(...any)) {
				var chatJID, name string
				scan(&chatJID, &name)
				jid, err := types.ParseJID(chatJID)
				if err != nil || (name != "" && name != jid.User) {
					return
				}
				if contactName := lookupContactName(client, jid); contactName != "" {
					updates[chatJID] = contactName
				}
			})
	}()
	for chatJID, name := range updates {
		store.exec("UPDATE whatsapp_chat SET name = ? WHERE store = ? AND jid = ?", name, store.key, chatJID)
	}
	logger.Infof("Named %d LID chats from contacts", len(updates))
}

// handleHistorySync writes each message of a history sync as a live one would be, with the
// reactions, receipts and votes it carries, in one batch.
func handleHistorySync(client *whatsmeow.Client, store *Store, historySync *events.HistorySync, logger waLog.Logger) bool {
	b := &importers.WABatch{Chats: map[string]string{}}
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
		b.Chats[chatJID] = name
		for _, msg := range conversation.Messages {
			if msg == nil || msg.Message == nil {
				continue
			}
			if msg.Message.GetMessage() == nil { // what WhatsApp shows of an entry with no message
				historyStub(client, b, jid, msg.Message)
				continue
			}
			historyVotes(client, b, jid, msg.Message)
			evt, err := client.ParseWebMessage(jid, msg.Message)
			if err != nil {
				logger.Warnf("Failed to parse history message: %v", err)
				continue
			}
			processMessage(client, store, b, evt, name, logger)
			historyReactions(client, b, jid, msg.Message)
			historyReceipts(b, jid, msg.Message)
			synced++
		}
	}
	logger.Infof("History sync: %d messages in %d conversations", synced, len(historySync.Data.Conversations))
	return store.Write(b, logger)
}
