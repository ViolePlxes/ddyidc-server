package outbound

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestDestinationBoundary(t *testing.T) {
	p, _ := New(Config{})
	for _, raw := range []string{"http://user:pass@example.org/", "file:///etc/passwd", "https://example.org:22/", "https://example.org/#fragment"} {
		if p.URL(raw) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "169.254.169.254", "10.0.0.1", "100.64.0.1", "192.0.2.1", "224.0.0.1", "2001:db8::1", "fe80::1", "0.0.0.0", "64:ff9b::a00:1", "2002:7f00:1::"} {
		if p.allowed(net.ParseIP(raw)) {
			t.Errorf("accepted forbidden IP %s", raw)
		}
	}
	if !p.allowed(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address denied")
	}
	private, err := New(Config{PrivateCIDRs: []string{"10.1.0.0/16"}})
	if err != nil || !private.allowed(net.ParseIP("10.1.2.3")) || private.allowed(net.ParseIP("10.2.2.3")) {
		t.Fatal("private allowlist widened")
	}
	for _, raw := range []string{"0.0.0.0/0", "10.0.0.0/1", "169.254.0.0/16"} {
		if _, e := New(Config{PrivateCIDRs: []string{raw}}); e == nil {
			t.Fatal("unsafe prefix accepted")
		}
	}
}

func TestDNSPinnedAndRebindingRejected(t *testing.T) {
	p, _ := New(Config{})
	calls := 0
	p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		calls++
		if calls == 1 {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	dials := 0
	p.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		dials++
		if address != "8.8.8.8:443" {
			t.Fatal("hostname re-resolved at dial")
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	c, e := p.DialContext(context.Background(), "tcp", "supplier.example:443")
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	if _, e = p.DialContext(context.Background(), "tcp", "supplier.example:443"); !errors.Is(e, ErrDenied) || dials != 1 {
		t.Fatal("rebinding reached dial")
	}
	p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("10.0.0.1")}}, nil
	}
	if _, e = p.DialContext(context.Background(), "tcp", "supplier.example:443"); !errors.Is(e, ErrDenied) {
		t.Fatal("mixed private answer accepted")
	}
}

func TestRedirectCannotLeakCredentialsOrReplayWrites(t *testing.T) {
	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer other.Close()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cross":
			http.Redirect(w, r, other.URL, http.StatusFound)
		case "/same":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/write":
			http.Redirect(w, r, "/ok", http.StatusTemporaryRedirect)
		default:
			w.WriteHeader(200)
		}
	}))
	defer local.Close()
	ports := []int{}
	for _, raw := range []string{local.URL, other.URL} {
		u, _ := url.Parse(raw)
		port, _ := strconv.Atoi(u.Port())
		ports = append(ports, port)
	}
	if e := Configure(Config{PrivateCIDRs: []string{"127.0.0.1/32"}, Ports: ports}); e != nil {
		t.Fatal(e)
	}
	defer Configure(Config{})
	client := NewClient(time.Second)
	req, _ := http.NewRequest("GET", local.URL+"/cross", nil)
	req.Header.Set("Authorization", "test-only-secret")
	if _, e := client.Do(req); e == nil || leaked {
		t.Fatal("cross-origin redirect accepted")
	}
	response, e := client.Get(local.URL + "/same")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	req, _ = http.NewRequest("POST", local.URL+"/write", nil)
	if _, e = client.Do(req); e == nil {
		t.Fatal("write replayed on redirect")
	}
}

func TestCancelledDNSAndExplicitHostPort(t *testing.T) {
	p, _ := New(Config{Hosts: []string{"supplier.example"}, Ports: []int{443, 6080}})
	if p.URL("wss://supplier.example:6080/socket") != nil || p.URL("wss://other.example/socket") == nil {
		t.Fatal("host boundary failed")
	}
	p.lookup = func(ctx context.Context, _ string) ([]net.IPAddr, error) { <-ctx.Done(); return nil, ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, e := p.DialContext(ctx, "tcp", "supplier.example:443"); e == nil || time.Since(start) > time.Second {
		t.Fatal("cancelled DNS blocked")
	}
}

func TestTLSResolverRequiresPublicLiteralAndVerifiedName(t *testing.T) {
	for _, c := range []Config{
		{DNSTLSAddress: "127.0.0.1:853", DNSTLSServerName: "resolver.example"},
		{DNSTLSAddress: "198.18.0.1:853", DNSTLSServerName: "resolver.example"},
		{DNSTLSAddress: "1.1.1.1:53", DNSTLSServerName: "one.one.one.one"},
		{DNSTLSAddress: "1.1.1.1:853"},
		{DNSTLSServerName: "one.one.one.one"},
	} {
		if _, e := New(c); e == nil {
			t.Fatal("invalid TLS resolver accepted")
		}
	}
	if _, e := New(Config{DNSTLSAddress: "1.1.1.1:853", DNSTLSServerName: "one.one.one.one"}); e != nil {
		t.Fatal(e)
	}
}
