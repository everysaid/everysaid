package whatsapp

// The connection's state and events (Bridge.Status), and unblocking sending (Bridge.Unblock).

// The state of the connection, as WhatsApp reports it, kept in keys.db (whatsapp_state,
// whatsapp_event) and said on the plugin's card (Bridge.Status). Any sign that
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

func (store *Store) setState(key, value string) {
	store.exec("INSERT OR REPLACE INTO whatsapp_state (store, key, value, at) VALUES (?, ?, ?, ?)", store.key, key, value,
		time.Now().Unix())
}

func (store *Store) state(key string) (string, time.Time) {
	var value sql.NullString
	var at sql.NullInt64
	store.db.QueryRow("SELECT value, at FROM whatsapp_state WHERE store = ? AND key = ?", store.key, key).Scan(&value, &at)
	if !at.Valid {
		return value.String, time.Time{}
	}
	return value.String, time.Unix(at.Int64, 0)
}

// How many of the connection's events are kept.
const eventsKept = 200

func (store *Store) logEvent(event, code, detail string) {
	store.exec("INSERT INTO whatsapp_event (store, at, event, code, detail) VALUES (?, ?, ?, ?, ?)", store.key,
		time.Now().UnixMilli(), event, code, detail)
	store.exec("DELETE FROM whatsapp_event WHERE store = ?1 AND rowid NOT IN "+
		"(SELECT rowid FROM whatsapp_event WHERE store = ?1 ORDER BY rowid DESC LIMIT ?2)", store.key, eventsKept)
	if store.said != nil {
		store.said(event, code, detail)
	}
}

// block stops sending; only the first reason is kept until it is cleared.
func (store *Store) block(reason string) {
	if old, _ := store.state("send_blocked"); old == "" {
		store.setState("send_blocked", reason)
	}
}

// handleStateEvent records what WhatsApp says about the connection and the account.
func handleStateEvent(store *Store, evt interface{}, logger waLog.Logger) {
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
