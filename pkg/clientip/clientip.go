// Package clientip resolves proxy headers only from explicitly trusted peers.
package clientip

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type Config struct {
	TrustedProxies []string `json:",optional"`
}
type Resolver struct{ trusted []netip.Prefix }
type resolvedKey struct{}

func New(c Config) (*Resolver, error) {
	r := &Resolver{}
	for _, raw := range c.TrustedProxies {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR")
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("trusting every proxy is prohibited")
		}
		r.trusted = append(r.trusted, p.Masked())
	}
	return r, nil
}

func parse(raw string) (netip.Addr, error) {
	raw = strings.TrimSpace(raw)
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	a, err := netip.ParseAddr(raw)
	if err == nil && a.Zone() != "" {
		return netip.Addr{}, fmt.Errorf("scoped address not allowed")
	}
	return a.Unmap(), err
}

func (r *Resolver) isTrusted(a netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (r *Resolver) Resolve(req *http.Request) string {
	peer, err := parse(req.RemoteAddr)
	if err != nil {
		return ""
	}
	if !r.isTrusted(peer) {
		return peer.String()
	}
	chain := strings.Join(req.Header.Values("X-Forwarded-For"), ",")
	if chain == "" {
		chain = req.Header.Get("X-Real-IP")
	}
	if chain == "" || len(chain) > 4096 {
		return peer.String()
	}
	parts := strings.Split(chain, ",")
	if len(parts) > 32 {
		return peer.String()
	}
	addresses := make([]netip.Addr, len(parts))
	for i, raw := range parts {
		a, e := parse(raw)
		if e != nil || !a.IsValid() || a.IsUnspecified() {
			return peer.String()
		}
		addresses[i] = a
	}
	current := peer
	for i := len(addresses) - 1; i >= 0 && r.isTrusted(current); i-- {
		current = addresses[i]
	}
	return current.String()
}

// FromRequest is the only client address entry point for rate limits and audit.
// Without the trusted-proxy middleware it safely uses the direct network peer.
func FromRequest(r *http.Request) string {
	if value, ok := r.Context().Value(resolvedKey{}).(string); ok {
		return value
	}
	a, err := parse(r.RemoteAddr)
	if err != nil {
		return ""
	}
	return a.String()
}

func (r *Resolver) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := context.WithValue(req.Context(), resolvedKey{}, r.Resolve(req))
		next(w, req.WithContext(ctx))
	}
}
