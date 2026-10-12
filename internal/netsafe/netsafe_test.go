package netsafe

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPublicIP(t *testing.T) {
	for _, s := range []string{"10.1.2.3", "172.16.0.1", "192.168.1.7", "127.0.0.1", "169.254.1.1", "0.0.0.0",
		"0.1.2.3", "100.64.0.1", "100.127.255.254", "192.0.0.8", "198.18.0.1", "198.19.255.1", "240.0.0.1",
		"255.255.255.255", "224.0.0.1", "::", "::1", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:192.168.1.1", "::ffff:10.0.0.1", "::127.0.0.1", "64:ff9b::7f00:1",
		"64:ff9b:1::1", "2002:7f00:1::1", "2001:0:4136:e378::1", "2001:db8::1"} {
		if PublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s taken as public", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "100.128.0.1", "198.20.0.1",
		"2606:4700::1111", "2a00:1450:4001::200e", "::ffff:8.8.8.8"} {
		if !PublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s refused", s)
		}
	}
}

func pngBytes(w, h int) []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

// local lets the client reach this machine's test servers, at any port; restored after the test.
func local(t *testing.T) {
	old := Dialable
	Dialable = func(netip.AddrPort) bool { return true }
	t.Cleanup(func() { Dialable = old })
}

// The test servers are on this machine: refused as they are, a redirect from an allowed one too.
func TestRefusesThisMachine(t *testing.T) {
	pic := pngBytes(4, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pic)
	}))
	defer srv.Close()
	if _, err := Image(context.Background(), srv.URL+"/a.png", "test"); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("loopback: %v", err)
	}
	// "outside" is allowed (as if on the internet); it sends the client to this machine's server
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/a.png", http.StatusFound)
	}))
	defer outside.Close()
	old := Dialable
	Dialable = func(ap netip.AddrPort) bool { return strings.HasSuffix(outside.URL, ap.String()) }
	defer func() { Dialable = old }()
	if _, err := Image(context.Background(), outside.URL+"/r", "test"); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("redirect to this machine: %v", err)
	}
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/a.png", "http://user:pw@example.com/a.png",
		"gopher://x", "http:///a.png"} {
		if _, err := Image(context.Background(), u, "test"); !errors.Is(err, ErrURL) {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestLimits(t *testing.T) {
	local(t)
	var gotUA, gotRef string
	big := pngBytes(8000, 7000) // 56 MP: refused by its size, not read
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			gotUA, gotRef = r.UserAgent(), r.Referer()
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngBytes(4, 3))
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			w.Write(pngBytes(4, 3))
		case "/svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write([]byte("<svg/>"))
		case "/huge":
			w.Header().Set("Content-Type", "image/png")
			w.Write(bytes.Repeat([]byte{0}, MaxBytes+10))
		case "/pixels":
			w.Header().Set("Content-Type", "image/png")
			w.Write(big)
		case "/gone":
			http.NotFound(w, r)
		case "/hop":
			http.Redirect(w, r, "/ok.png", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()
	b, err := Image(context.Background(), srv.URL+"/hop", "Everysaid (test)")
	if err != nil || len(b) == 0 {
		t.Fatalf("ok: %v", err)
	}
	if gotUA != "Everysaid (test)" || gotRef != "" {
		t.Fatalf("user agent %q, referer %q", gotUA, gotRef)
	}
	for path, want := range map[string]error{"/html": ErrNotImage, "/svg": ErrNotImage, "/huge": ErrTooLarge,
		"/pixels": ErrTooLarge} {
		if _, err := Image(context.Background(), srv.URL+path, "test"); !errors.Is(err, want) {
			t.Errorf("%s: %v", path, err)
		}
	}
	var se *StatusError
	if _, err := Image(context.Background(), srv.URL+"/gone", "test"); !errors.As(err, &se) || se.Code != 404 {
		t.Errorf("gone: %v", err)
	}
	if _, err := Image(context.Background(), srv.URL+"/loop", "test"); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Errorf("loop: %v", err)
	}
}

// The web's ports only, by default.
func TestPorts(t *testing.T) {
	for ap, want := range map[string]bool{"8.8.8.8:443": true, "8.8.8.8:80": true, "8.8.8.8:22": false,
		"8.8.8.8:8080": false, "127.0.0.1:443": false} {
		if Dialable(netip.MustParseAddrPort(ap)) != want {
			t.Errorf("%s: %v", ap, !want)
		}
	}
}
