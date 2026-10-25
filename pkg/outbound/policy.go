// Package outbound validates destinations at the connection boundary. DNS names
// are resolved once per new connection and only validated IP literals are dialed.
package outbound

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var ErrDenied = errors.New("outbound destination denied by network policy")

type Config struct {
	DetailReadOnly   bool     `json:",optional"`
	PrivateCIDRs     []string `json:",optional"`
	Ports            []int    `json:",optional"`
	Hosts            []string `json:",optional"`
	DNSTLSAddress    string   `json:",optional"`
	DNSTLSServerName string   `json:",optional"`
}

type Policy struct {
	detailReadOnly bool
	private        []netip.Prefix
	ports          map[int]bool
	hosts          map[string]bool
	lookup         func(context.Context, string) ([]net.IPAddr, error)
	dial           func(context.Context, string, string) (net.Conn, error)
}

var current atomic.Pointer[Policy]

func init() { p, _ := New(Config{}); current.Store(p) }

func Configure(c Config) error {
	p, err := New(c)
	if err != nil {
		return err
	}
	current.Store(p)
	return nil
}

func New(c Config) (*Policy, error) {
	if c.DetailReadOnly && len(c.Hosts) == 0 {
		return nil, ErrDenied
	}
	p := &Policy{ports: map[int]bool{}, hosts: map[string]bool{}, lookup: net.DefaultResolver.LookupIPAddr,
		dial: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	p.detailReadOnly = c.DetailReadOnly
	if len(c.Ports) == 0 {
		c.Ports = []int{80, 443}
	}
	for _, port := range c.Ports {
		if port < 1 || port > 65535 {
			return nil, ErrDenied
		}
		p.ports[port] = true
	}
	for _, host := range c.Hosts {
		host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
		if host == "" || strings.ContainsAny(host, "/*@?# ") {
			return nil, ErrDenied
		}
		p.hosts[host] = true
	}
	for _, cidr := range c.PrivateCIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || !privatePrefix(prefix) {
			return nil, ErrDenied
		}
		p.private = append(p.private, prefix.Masked())
	}
	if c.DNSTLSAddress != "" || c.DNSTLSServerName != "" {
		host, port, err := net.SplitHostPort(c.DNSTLSAddress)
		ip := net.ParseIP(host)
		strict := &Policy{}
		if err != nil || port != "853" || ip == nil || !strict.allowed(ip) || c.DNSTLSServerName == "" || strings.ContainsAny(c.DNSTLSServerName, "/:@?# \r\n") {
			return nil, ErrDenied
		}
		dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.DNSTLSServerName}}
		resolver := &net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", c.DNSTLSAddress)
		}}
		p.lookup = resolver.LookupIPAddr
	}
	return p, nil
}

func privatePrefix(p netip.Prefix) bool {
	for _, raw := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7", "127.0.0.0/8", "::1/128"} {
		allowed := netip.MustParsePrefix(raw)
		if allowed.Contains(p.Addr()) && p.Bits() >= allowed.Bits() {
			return true
		}
	}
	return false
}

func (p *Policy) address(host, port string) error {
	n, err := strconv.Atoi(port)
	if err != nil || !p.ports[n] || host == "" || strings.Contains(host, "%") {
		return ErrDenied
	}
	if len(p.hosts) > 0 && !p.hosts[strings.ToLower(strings.TrimSuffix(host, "."))] {
		return ErrDenied
	}
	return nil
}

func (p *Policy) URL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return ErrDenied
	}
	port := u.Port()
	switch u.Scheme {
	case "https", "wss":
		if port == "" {
			port = "443"
		}
	case "http", "ws":
		if port == "" {
			port = "80"
		}
	default:
		return ErrDenied
	}
	return p.address(u.Hostname(), port)
}

var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32"),
}

func (p *Policy) allowed(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsMulticast() {
		return false
	}
	for _, prefix := range reserved {
		if prefix.Contains(a) {
			return false
		}
	}
	if a.IsPrivate() || a.IsLoopback() {
		for _, prefix := range p.private {
			if prefix.Contains(a) {
				return true
			}
		}
		return false
	}
	return a.IsGlobalUnicast()
}

func (p *Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || (network != "tcp" && network != "tcp4" && network != "tcp6") {
		return nil, ErrDenied
	}
	if err = p.address(host, port); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var ips []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IPAddr{{IP: ip}}
	} else {
		ips, err = p.lookup(ctx, host)
		if err != nil {
			return nil, errors.New("outbound DNS lookup failed")
		}
	}
	if len(ips) == 0 {
		return nil, ErrDenied
	}
	for _, ip := range ips {
		if !p.allowed(ip.IP) {
			return nil, ErrDenied
		}
	}
	for _, ip := range ips {
		conn, e := p.dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if e == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("outbound connection failed")
}

func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return current.Load().DialContext(ctx, network, address)
}
func ValidateURL(raw string) error { return current.Load().URL(raw) }

type transport struct{ base *http.Transport }

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := CheckDetailRequest(r); err != nil {
		return nil, err
	}
	if err := ValidateURL(r.URL.String()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(r)
}

func NewClient(timeout time.Duration) *http.Client {
	return newClient(timeout, false)
}

// NewSingleAttemptClient avoids replay on reused connections. Some registrars
// use GET for charged writes, so HTTP method alone cannot establish idempotency.
func NewSingleAttemptClient(timeout time.Duration) *http.Client {
	return newClient(timeout, true)
}

func newClient(timeout time.Duration, singleAttempt bool) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DisableKeepAlives = singleAttempt
	base.Proxy = nil // Do not permit environment proxies to bypass IP validation.
	base.DialContext = DialContext
	base.MaxConnsPerHost = 16
	base.ResponseHeaderTimeout = 15 * time.Second
	base.MaxResponseHeaderBytes = 1 << 20
	return &http.Client{Timeout: timeout, Transport: transport{base}, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) == 0 || len(via) > 3 {
			return ErrDenied
		}
		first := via[0]
		if first.Method != http.MethodGet && first.Method != http.MethodHead {
			return ErrDenied
		}
		if !strings.EqualFold(next.URL.Host, first.URL.Host) || next.URL.Scheme != first.URL.Scheme {
			return ErrDenied
		}
		return ValidateURL(next.URL.String())
	}}
}
