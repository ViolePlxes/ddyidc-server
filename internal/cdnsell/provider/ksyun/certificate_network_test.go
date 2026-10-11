package ksyun

import (
	"net"
	"testing"
)

func TestEdgeAddressIsPublic(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "100.64.1.1", "198.18.0.2", "192.0.2.1", "203.0.113.1", "::1", "fc00::1", "2001:db8::1"} {
		if publicEdgeIP(net.ParseIP(raw)) {
			t.Fatalf("unsafe edge address accepted: %s", raw)
		}
	}
	for _, raw := range []string{"103.155.16.130", "162.128.226.4", "2606:4700::1111"} {
		if !publicEdgeIP(net.ParseIP(raw)) {
			t.Fatalf("public edge address rejected: %s", raw)
		}
	}
}
