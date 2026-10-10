// Ports everysaid/server/push.py.
//
// Push notifications (Web Push): new incoming messages reach the user's devices while the app is
// closed. The payload is end-to-end encrypted for each subscription (RFC 8291), so the browser's
// push service (Apple's, Google's, Mozilla's) carries it without reading it. The server's VAPID key
// is a secret (`vapid-private`, in the keyring, a PEM as the Python made it: the same key, so the
// subscriptions made with it keep working). Muted chats send nothing, nor what was read already
// (host.go, describeNew); the setting `push_preview`
// (default on) decides whether the text is shown or only who wrote.
package server

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/i18n"
)

// kindLabel is what a message without text shows as (said through i18n).
var kindLabel = map[string]string{"image": "📷 Photo", "video": "🎬 Video", "voice": "🎤 Voice message", "file": "📎 File",
	"sticker": "Sticker", "location": "📍 Location", "contact": "👤 Contact"}

// Push sends notifications to the devices of an archive's users.
type Push struct {
	auth *Auth
	log  *slog.Logger

	mu         sync.Mutex
	private    string // base64url of the raw private scalar, as webpush-go wants it
	public     string // base64url of the raw public point, as the browser wants it
	httpClient webpush.HTTPClient
}

func NewPush(auth *Auth, log *slog.Logger) *Push {
	return &Push{auth: auth, log: log, httpClient: pushClient()}
}

// pushClient reaches push services only on the internet: an endpoint is the browser's word, so the
// address is checked as the connection is made (a name may resolve to this machine or its network
// later than when it was given: DNS rebinding), redirects included; no proxy (its own address would
// be the one checked).
func pushClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	dialer := &net.Dialer{Timeout: 15 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !publicIP(ap.Addr()) {
			return fmt.Errorf("push: %s is not an address on the internet", address)
		}
		return nil
	}}
	tr.DialContext = dialer.DialContext
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}
}

// cgnat is the carriers' shared space (and Tailscale's): not the internet either.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// publicIP says whether an address is one on the internet: not this machine, a private network,
// link-local, multicast or unspecified.
func publicIP(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !cgnat.Contains(a)
}

// pushEndpointOK is pushEndpoint (a variable: the tests' push service is on this machine).
var pushEndpointOK = pushEndpoint

// pushEndpoint checks a subscription's endpoint: https, at a name (or an address) on the internet.
func pushEndpoint(ctx context.Context, endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return false
	}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil {
		return publicIP(a)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if !publicIP(a) {
			return false
		}
	}
	return true
}

// Key is the VAPID public key, as the browser wants it (base64url of the raw point); made and kept
// the first time.
func (p *Push) Key() (string, error) {
	if _, err := p.keys(); err != nil {
		return "", err
	}
	return p.public, nil
}

func (p *Push) keys() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.private != "" {
		return p.private, nil
	}
	secret, err := config.Secret("vapid-private")
	if err != nil {
		return "", err
	}
	if secret == "" {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return "", err
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return "", err
		}
		secret = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		if _, err := config.SaveSecret("vapid-private", secret); err != nil {
			return "", err
		}
	}
	priv, err := parseVAPID(secret)
	if err != nil {
		return "", err
	}
	p.private = base64.RawURLEncoding.EncodeToString(priv.Bytes())
	p.public = base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	return p.private, nil
}

// parseVAPID reads the key as py_vapid kept it (a PKCS#8 or SEC 1 PEM), or a raw base64url scalar.
func parseVAPID(s string) (*ecdh.PrivateKey, error) {
	if block, _ := pem.Decode([]byte(strings.TrimSpace(s))); block != nil {
		var key any
		var err error
		if key, err = x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
			if key, err = x509.ParseECPrivateKey(block.Bytes); err != nil {
				return nil, err
			}
		}
		ec, ok := key.(*ecdsa.PrivateKey)
		if !ok || ec.Curve != elliptic.P256() {
			return nil, errors.New("vapid-private: not a P-256 key")
		}
		return ec.ECDH()
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
	if err != nil {
		return nil, err
	}
	return ecdh.P256().NewPrivateKey(raw)
}

// Subscribe keeps a browser's subscription: {endpoint, keys: {p256dh, auth}}.
func (p *Push) Subscribe(uid int64, sub map[string]any) {
	keys := sub["keys"]
	if !truthy(keys) {
		keys = map[string]any{}
	}
	b, _ := json.Marshal(keys)
	db.Exec(p.auth.db, "INSERT OR REPLACE INTO push_subscription VALUES (?, ?, ?, ?)", fmt.Sprint(sub["endpoint"]), uid,
		string(b), time.Now().Unix())
}

func (p *Push) Unsubscribe(uid int64, endpoint any) {
	db.Exec(p.auth.db, "DELETE FROM push_subscription WHERE user_id = ? AND endpoint = ?", uid, endpoint)
}

type subscription struct{ endpoint, keys string }

// Subscriptions are those of the users of an archive.
func (p *Push) Subscriptions(archivePath string) []subscription {
	var out []subscription
	db.Each(p.auth.db, "SELECT s.endpoint, s.keys FROM push_subscription s JOIN user u ON u.id = s.user_id WHERE u.archive = ?",
		[]any{archivePath}, func(scan func(...any)) {
			var s subscription
			scan(&s.endpoint, &s.keys)
			out = append(out, s)
		})
	return out
}

// Alert is a notice about the app itself (a plugin's warning), to every device of the archive's users.
func (p *Push) Alert(store *core.Store, title, body string) {
	subs := p.Subscriptions(store.Path)
	if len(subs) > 0 {
		payload := map[string]any{"title": title, "body": core.Cut(body, 240), "chat": nil, "tag": "alert"}
		go p.Send(subs, []map[string]any{payload})
	}
}

// Incoming is a new message from someone else.
type Incoming struct {
	Chat    string
	Message int64
	TS      int64
	Text    string
	Kind    string
}

// Notify tells the devices of an archive's users about new incoming messages: the notifications
// made by Notifications.
func (p *Push) Notify(store *core.Store, payloads []map[string]any) {
	if subs := p.Subscriptions(store.Path); len(subs) > 0 && len(payloads) > 0 {
		go p.Send(subs, payloads)
	}
}

// Notifications are those of new incoming messages, one per chat (muted chats left out): sent as
// pushes, and in the "new" event for the apps a browser without push shows them in.
func Notifications(store *core.Store, incoming []Incoming) []map[string]any {
	states := core.States(store)
	preview := truthy(store.SettingAny("push_preview", true))
	byChat := map[string][]Incoming{}
	var order []string
	for _, m := range incoming {
		if states[m.Chat].Muted {
			continue
		}
		if _, ok := byChat[m.Chat]; !ok {
			order = append(order, m.Chat)
		}
		byChat[m.Chat] = append(byChat[m.Chat], m)
	}
	ix := core.Index(store)
	lang := store.SettingString("language", "en")
	var payloads []map[string]any
	for _, cid := range order {
		chat := ix.Chats[cid]
		if chat == nil {
			continue
		}
		msgs := byChat[cid]
		last := msgs[len(msgs)-1]
		newWord := i18n.Tr("New message", lang)
		body := newWord
		if preview {
			body = last.Text
			if body == "" {
				if label, ok := kindLabel[last.Kind]; ok {
					body = i18n.Tr(label, lang)
				} else {
					body = newWord
				}
			}
		}
		if len(msgs) > 1 {
			body = fmt.Sprintf("(%d) %s", len(msgs), body)
		}
		payloads = append(payloads, map[string]any{"title": core.ChatTitle(store, chat), "body": core.Cut(body, 240),
			"chat": cid, "message": last.Message, "ts": last.TS, "tag": cid})
	}
	return payloads
}

// Send sends each payload to each subscription; one the push service says is gone is forgotten.
func (p *Push) Send(subs []subscription, payloads []map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("push", "panic", r)
		}
	}()
	private, err := p.keys()
	if err != nil {
		p.log.Error("push: the VAPID key", "error", err)
		return
	}
	contact := strings.TrimPrefix(config.String("server", "contact", "everysaid@localhost"), "mailto:")
	for _, s := range subs {
		var keys webpush.Keys
		json.Unmarshal([]byte(s.keys), &keys)
		for _, payload := range payloads {
			var b bytes.Buffer
			enc := json.NewEncoder(&b)
			enc.SetEscapeHTML(false)
			enc.Encode(payload)
			resp, err := webpush.SendNotification(bytes.TrimRight(b.Bytes(), "\n"), &webpush.Subscription{Endpoint: s.endpoint, Keys: keys},
				&webpush.Options{Subscriber: contact, TTL: 3600, VAPIDPublicKey: p.public, VAPIDPrivateKey: private,
					HTTPClient: p.httpClient})
			if err != nil {
				continue
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 404 || resp.StatusCode == 410 { // gone
				db.Exec(p.auth.db, "DELETE FROM push_subscription WHERE endpoint = ?", s.endpoint)
				break
			}
		}
	}
}
