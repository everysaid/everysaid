package main

// The state of the connection, as WhatsApp reports it, kept in messages.db for whoever reads the
// bridge (bridge_state, bridge_events) and served at /api/status. Any sign that WhatsApp is unhappy
// with the account (a temporary ban, a logout, a connect failure that means one, the session taken
// over elsewhere, an outdated client) blocks sending until someone clears it on purpose
// (POST /api/unblock); the block survives restarts.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow"
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
	fmt.Printf("[bridge] %s %s %s\n", event, code, detail)
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

type statusEvent struct {
	At     time.Time `json:"at"`
	Event  string    `json:"event"`
	Code   string    `json:"code,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

func registerStatusHandlers(client *whatsmeow.Client, store *MessageStore, sender *Sender) {
	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		connection, connectionAt := store.state("connection")
		blocked, blockedAt := store.state("send_blocked")
		banUntil, _ := store.state("ban_until")
		events := []statusEvent{}
		if rows, err := store.db.Query("SELECT at, event, code, detail FROM bridge_events ORDER BY rowid DESC LIMIT 20"); err == nil {
			for rows.Next() {
				var e statusEvent
				var code, detail sql.NullString
				if rows.Scan(&e.At, &e.Event, &code, &detail) == nil {
					e.Code, e.Detail = code.String, detail.String
					events = append(events, e)
				}
			}
			rows.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"connected":     client.IsConnected(),
			"logged_in":     client.IsLoggedIn(),
			"connection":    connection,
			"connection_at": connectionAt,
			"ban_until":     banUntil,
			"send_enabled":  sender.enabled,
			"send_blocked":  blocked,
			"blocked_at":    blockedAt,
			"limits":        sender.limits,
			"sent":          sender.counts(),
			"events":        events,
		})
	})

	// Clearing a block is a person's decision: nothing in the bridge calls this.
	http.HandleFunc("/api/unblock", func(w http.ResponseWriter, r *http.Request) {
		if fromBrowser(w, r) {
			return
		}
		old, _ := store.state("send_blocked")
		store.setState("send_blocked", "")
		store.logEvent("unblocked", "", old)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "was": old})
	})
}
