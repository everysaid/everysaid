package whatsapp

// Ports bridges/whatsapp/status.go (/api/status is now Bridge.Status, /api/unblock Bridge.Unblock).

// The state of the connection, as WhatsApp reports it, kept in messages.db for whoever reads the
// bridge (bridge_state, bridge_events) and said on the plugin's card (Bridge.Status). Any sign that
// WhatsApp is unhappy with the account (a temporary ban, a logout, a connect failure that means one, the session taken
// over elsewhere, an outdated client) blocks sending until someone clears it on purpose
// (Bridge.Unblock, the plugin's action); the block survives restarts.

import (
	"database/sql"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func (store *MessageStore) migrateStatus() error {
	_, err := store.db.Exec(`
		CREATE TABLE IF NOT EXISTS bridge_state (
			key TEXT PRIMARY KEY,     -- connection, send_blocked, ban_until
			value TEXT,
			at TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS bridge_events (
			at TIMESTAMP,
			event TEXT,
			code TEXT,
			detail TEXT
		);
		CREATE TABLE IF NOT EXISTS sent (
			at INTEGER,               -- Unix seconds
			chat_jid TEXT,
			id TEXT,
			text_hash TEXT
		);
		CREATE TABLE IF NOT EXISTS started (
			at INTEGER,               -- Unix seconds
			chat_jid TEXT PRIMARY KEY -- a chat the owner wrote in first, from here
		);
	`)
	return err
}

func (store *MessageStore) setState(key, value string) {
	store.db.Exec("INSERT OR REPLACE INTO bridge_state (key, value, at) VALUES (?, ?, ?)", key, value, time.Now())
}

func (store *MessageStore) state(key string) (string, time.Time) {
	var value string
	var at sql.NullTime
	store.db.QueryRow("SELECT value, at FROM bridge_state WHERE key = ?", key).Scan(&value, &at)
	return value, at.Time
}

func (store *MessageStore) logEvent(event, code, detail string) {
	store.db.Exec("INSERT INTO bridge_events (at, event, code, detail) VALUES (?, ?, ?, ?)", time.Now(), event, code, detail)
	if store.said != nil {
		store.said(event, code, detail)
	}
}

// block stops sending; only the first reason is kept until it is cleared.
func (store *MessageStore) block(reason string) {
	if old, _ := store.state("send_blocked"); old == "" {
		store.setState("send_blocked", reason)
	}
}

// handleStateEvent records what WhatsApp says about the connection and the account.
func handleStateEvent(store *MessageStore, evt interface{}, logger waLog.Logger) {
	switch v := evt.(type) {
	case *events.Connected:
		store.setState("connection", "connected")
		store.logEvent("connected", "", "")
	case *events.Disconnected:
		store.setState("connection", "disconnected")
		store.logEvent("disconnected", "", "")
	case *events.LoggedOut:
		store.setState("connection", "logged_out")
		store.logEvent("logged_out", v.Reason.NumberString(), v.Reason.String())
		store.block("logged out: " + v.Reason.String())
		logger.Warnf("Device logged out, please scan QR code to log in again")
	case *events.TemporaryBan:
		until := time.Now().Add(v.Expire)
		store.setState("connection", "temp_banned")
		store.setState("ban_until", until.Format(time.RFC3339))
		store.logEvent("temporary_ban", fmt.Sprint(int(v.Code)), v.String())
		store.block("temporary ban: " + v.String())
	case *events.ConnectFailure:
		store.logEvent("connect_failure", v.Reason.NumberString(), v.Reason.String()+" "+v.Message)
		switch {
		case v.Reason == events.ConnectFailureTempBanned:
			store.setState("connection", "temp_banned")
			store.block("connect failure: " + v.Reason.String())
		case v.Reason.IsLoggedOut():
			store.setState("connection", "logged_out")
			store.block("connect failure: " + v.Reason.String())
		default:
			store.setState("connection", "failed")
		}
	case *events.StreamReplaced:
		store.setState("connection", "replaced")
		store.logEvent("stream_replaced", "", "another client took over this session")
		store.block("session taken over by another client")
	case *events.ClientOutdated:
		store.setState("connection", "outdated")
		store.logEvent("client_outdated", "", "WhatsApp rejected this client version")
		store.block("client outdated")
	}
}
