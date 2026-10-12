// Package netsafe reaches addresses on the internet only, for what the server fetches at a URL it
// was given by someone else (a push service's endpoint, a link preview's picture): never this
// machine or its network. The address is checked as each connection is made, after the name is
// resolved, so a name that resolves to a private address later than when it was checked (DNS
// rebinding), and a redirect to one, are refused too.
package netsafe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "golang.org/x/image/webp"
)

// notInternet are the ranges that are no address on the internet, beyond what netip says itself
// (private, loopback, link-local, multicast, unspecified).
var notInternet = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // the carriers' shared space (and Tailscale's)
	netip.MustParsePrefix("192.0.0.0/24"),   // protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, and the broadcast address
	netip.MustParsePrefix("::/96"),          // IPv4-compatible
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64: an IPv4 address behind it
	netip.MustParsePrefix("64:ff9b:1::/48"), // local NAT64
	netip.MustParsePrefix("2002::/16"),      // 6to4: an IPv4 address inside
	netip.MustParsePrefix("2001::/32"),      // Teredo: an IPv4 address inside
	netip.MustParsePrefix("2001:db8::/32"),  // documentation
	netip.MustParsePrefix("fc00::/7"),       // unique local (IsPrivate says it too)
	netip.MustParsePrefix("ff00::/8"),       // multicast (IsGlobalUnicast says it too)
	netip.MustParsePrefix("255.255.255.255/32"),
}

// PublicIP says whether an address is one on the internet: not this machine, a private network,
// link-local, multicast, unspecified, or a range that is no host on the internet or hides an IPv4
// address that may be one of these (an IPv4-mapped address is judged as its IPv4).
func PublicIP(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range notInternet {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// Dialable says whether a connection may be made to an address: one on the internet, at the web's
// ports (a variable: the tests' servers are on this machine).
var Dialable = func(ap netip.AddrPort) bool {
	return PublicIP(ap.Addr()) && (ap.Port() == 80 || ap.Port() == 443)
}

var (
	ErrNotPublic = errors.New("not an address on the internet")
	ErrURL       = errors.New("not an http(s) URL")
	ErrNotImage  = errors.New("not a picture")
	ErrTooLarge  = errors.New("too large")
)

// StatusError is a server's answer other than 200.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Code) }

const (
	connectTimeout = 5 * time.Second
	totalTimeout   = 10 * time.Second // the whole request, redirects and body included
	maxRedirects   = 3
	MaxBytes       = 5 << 20 // a picture's
	maxPixels      = 50_000_000
)

// control checks the address actually dialled (after the name is resolved, for each attempt).
func control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !Dialable(ap) {
		return fmt.Errorf("%s: %w", address, ErrNotPublic)
	}
	return nil
}

// checkURL refuses what is no plain web address: another scheme, no host, a user and password.
func checkURL(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ErrURL
	}
	return nil
}

var (
	clientOnce sync.Once
	client     *http.Client
)

// Client reaches the internet only (see the package): no proxy (its own address would be the one
// checked), no cookies, no Referer, at most three redirects, each checked again.
func Client() *http.Client {
	clientOnce.Do(func() {
		dialer := &net.Dialer{Timeout: connectTimeout, Control: control}
		tr := &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			ForceAttemptHTTP2:      true,
			TLSHandshakeTimeout:    connectTimeout,
			ResponseHeaderTimeout:  totalTimeout,
			MaxIdleConns:           8,
			IdleConnTimeout:        30 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
		}
		client = &http.Client{Transport: tr, Timeout: totalTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > maxRedirects {
					return fmt.Errorf("more than %d redirects", maxRedirects)
				}
				req.Header.Del("Referer") // set by the client from the page redirected from
				return checkURL(req.URL)
			}}
	})
	return client
}

// Image fetches a picture at a URL through Client: an image/* answer of at most MaxBytes that Go
// can read (its size, not the whole picture, is checked here). userAgent says who asks.
func Image(ctx context.Context, rawURL, userAgent string) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, ErrURL
	}
	if err := checkURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrURL
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/*")
	resp, err := Client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{resp.StatusCode}
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || !strings.HasPrefix(mt, "image/") {
		return nil, ErrNotImage
	}
	if resp.ContentLength > MaxBytes {
		return nil, ErrTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBytes {
		return nil, ErrTooLarge
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, ErrNotImage
	}
	if cfg.Width*cfg.Height > maxPixels {
		return nil, ErrTooLarge
	}
	return b, nil
}
