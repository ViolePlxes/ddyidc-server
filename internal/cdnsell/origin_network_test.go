package cdnsell

import (
	"net"
	"testing"
)

func TestOriginPublicAddress(t *testing.T) {
	for _, raw := range []string{"0.1.2.3", "100.64.0.1", "198.18.0.1", "192.0.2.1", "203.0.113.1", "240.1.2.3", "2001:db8::1", "127.0.0.1", "10.0.0.1", "::1", "ff02::1"} {
		if isPublicIP(net.ParseIP(raw)) {
			t.Errorf("reserved address accepted: %s", raw)
		}
	}
	for _, raw := range []string{"103.36.221.244", "8.8.8.8", "2606:4700:4700::1111"} {
		if !isPublicIP(net.ParseIP(raw)) {
			t.Errorf("public address rejected: %s", raw)
		}
	}
}
