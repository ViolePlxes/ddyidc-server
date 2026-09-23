package clientip

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrustedProxyBoundary(t *testing.T) {
	r, err := New(Config{TrustedProxies: []string{"127.0.0.1/32", "10.0.0.0/8", "::1/128"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, peer, xff, want string }{
		{"direct spoof", "198.51.100.7:1234", "192.0.2.8", "198.51.100.7"},
		{"client one", "127.0.0.1:8", "198.51.100.1", "198.51.100.1"},
		{"client two", "127.0.0.1:8", "198.51.100.2", "198.51.100.2"},
		{"untrusted hop blocks forged left", "127.0.0.1:8", "192.0.2.9, 198.51.100.2", "198.51.100.2"},
		{"trusted hops", "127.0.0.1:8", "198.51.100.2, 10.1.2.3", "198.51.100.2"},
		{"ipv6", "[::1]:8", "2001:db8::3", "2001:db8::3"},
		{"mapped ipv4", "[::ffff:127.0.0.1]:8", "198.51.100.3", "198.51.100.3"},
		{"malformed chain", "127.0.0.1:8", "invalid, 10.1.2.3", "127.0.0.1"},
		{"missing header", "127.0.0.1:8", "", "127.0.0.1"},
		{"bounded chain", "127.0.0.1:8", strings.Repeat("10.1.1.1,", 40), "127.0.0.1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://localhost/", nil)
			req.RemoteAddr = c.peer
			req.Header.Set("X-Forwarded-For", c.xff)
			r.Middleware(func(_ http.ResponseWriter, req *http.Request) {
				if got := FromRequest(req); got != c.want {
					t.Fatalf("got %q want %q", got, c.want)
				}
			})(httptest.NewRecorder(), req)
		})
	}
}

func TestSafeDefaultsAndInvalidConfig(t *testing.T) {
	r, _ := New(Config{})
	req := httptest.NewRequest("GET", "http://localhost/", nil)
	req.RemoteAddr = "127.0.0.1:9"
	req.Header.Set("X-Real-IP", "198.51.100.1")
	if r.Resolve(req) != "127.0.0.1" {
		t.Fatal("default trusted a forwarding header")
	}
	for _, prefix := range []string{"garbage", "0.0.0.0/0", "::/0"} {
		if _, err := New(Config{TrustedProxies: []string{prefix}}); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
}
