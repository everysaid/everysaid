// The WebSocket of live events (app.py, /api/events): a hello, then each event of the host as it
// happens, and a ping every 25 seconds so that proxies keep the connection.
package server

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Get("Origin"); o != "" && !s.allowed[o] {
		writeJSON(w, 403, detail("auth.foreign_origin"))
		return
	}
	if _, _, ok := s.Auth.Session(cookieOf(r)); !ok {
		writeJSON(w, 403, detail("auth.sign_in_needed"))
		return
	}
	// the origin is checked above (this server's, or none: not a browser's page elsewhere)
	c, err := websocket.Accept(unwrap(w), r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context()) // ends when the app goes
	events := s.Host.Listen()
	defer s.Host.Unlisten(events)
	send := func(v any) bool {
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return wsjson.Write(wctx, c, v) == nil
	}
	if !send(M{"type": "hello", "ts": time.Now().UnixMilli()}) {
		return
	}
	ping := time.NewTimer(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case e := <-events:
			if !send(e) {
				return
			}
		case <-ping.C:
			if !send(M{"type": "ping"}) {
				return
			}
		}
		if !ping.Stop() {
			select {
			case <-ping.C:
			default:
			}
		}
		ping.Reset(25 * time.Second)
	}
}

// unwrap is the connection's own writer (the WebSocket takes it over, which a wrapper may not allow).
func unwrap(w http.ResponseWriter) http.ResponseWriter {
	for {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		w = u.Unwrap()
	}
}
