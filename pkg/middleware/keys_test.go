package middleware_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luvkumar404/rate-limiter/pkg/middleware"
)

func TestClientIPTrustBoundary(t *testing.T) {
	trusted, err := middleware.TrustedProxyClientIP("10.0.0.0/8", "2001:db8:ffff::/48", "::ffff:192.0.2.0/120")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, peer, xff, want string
		key                   middleware.KeyFunc
	}{
		{"untrusted spoof", "198.51.100.7:123", "1.2.3.4", "198.51.100.7", trusted},
		{"default ignores headers", "10.0.0.2:123", "1.2.3.4", "10.0.0.2", middleware.ClientIP()},
		{"single proxy", "10.0.0.2:123", "198.51.100.7", "198.51.100.7", trusted},
		{"two proxies", "10.0.0.2:123", "198.51.100.7, 10.0.0.3", "198.51.100.7", trusted},
		{"spoofed prefix", "10.0.0.2:123", "1.2.3.4, 198.51.100.7", "198.51.100.7", trusted},
		{"ignored malformed prefix", "10.0.0.2:123", "bad, 198.51.100.7", "198.51.100.7", trusted},
		{"ipv6", "[2001:db8:ffff::2]:123", "2001:db8::7", "2001:db8::7", trusted},
		{"mapped ipv4", "[::ffff:192.0.2.2]:123", "::ffff:198.51.100.7", "198.51.100.7", trusted},
		{"no forwarding header", "10.0.0.2:123", "", "10.0.0.2", trusted},
		{"bare peer", "2001:db8::1", "", "2001:db8::1", trusted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			got, err := tc.key(r)
			if err != nil || got != tc.want {
				t.Fatalf("IP=%q err=%v want %q", got, err, tc.want)
			}
		})
	}
	for _, xff := range []string{"bad", "unknown", strings.Repeat("1", 4097), strings.Repeat("10.0.0.1,", 33)} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.2:123"
		r.Header.Set("X-Forwarded-For", xff)
		if _, err := trusted(r); err == nil {
			t.Fatalf("malformed trusted chain accepted: %q", xff)
		}
	}
	if _, err := middleware.TrustedProxyClientIP("not-a-cidr"); err == nil {
		t.Fatal("invalid CIDR accepted")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "not-an-ip"
	if _, err := middleware.ClientIP()(r); err == nil {
		t.Fatal("invalid socket peer accepted")
	}
}

func TestHeaderKey(t *testing.T) {
	key := middleware.Header("X-API-Key")
	r := httptest.NewRequest("GET", "/", nil)
	if _, err := key(r); err == nil {
		t.Fatal("missing header accepted")
	}
	r.Header.Set("X-API-Key", "   ")
	if _, err := key(r); err == nil {
		t.Fatal("blank header accepted")
	}
	r.Header.Set("X-API-Key", " user:42 ")
	if got, err := key(r); err != nil || got != "user:42" {
		t.Fatalf("key=%q err=%v", got, err)
	}
	r.Header.Add("X-API-Key", "other")
	if _, err := key(r); err == nil {
		t.Fatal("multiple header values accepted")
	}
}
