package ksyun

import (
	"context"
	"crypto/x509"
	"ddyidc/internal/cdnsell/provider"
	"encoding/json"
	"encoding/pem"
	"os"
	"testing"
	"time"
)

func TestLiveEdgeTLSViaAuthorizedTunnel(t *testing.T) {
	if os.Getenv("CDN_EDGE_TUNNEL_TEST") != "1" {
		t.Skip("explicit read-only edge tunnel required")
	}
	path := os.Getenv("CDN_LIVE_CREDENTIAL_FILE")
	st, e := os.Stat(path)
	if e != nil || st.Mode().Perm()&0077 != 0 {
		t.Fatal("private configuration required")
	}
	raw, e := os.ReadFile(path)
	var a provider.Account
	if e != nil || json.Unmarshal(raw, &a) != nil {
		t.Fatal("configuration unavailable")
	}
	raw = nil
	raw, e = os.ReadFile("/home/ddyidc/cdn-live-target-state-20261011.json")
	var state liveState
	if e != nil || json.Unmarshal(raw, &state) != nil || state.Domain != liveTarget || !state.CreatedByTest || state.ServiceNo == "" {
		t.Fatal("owned test resource required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, e := New().GetDomain(ctx, a, state.ServiceNo)
	if e != nil || info["domain"] != liveTarget {
		t.Fatal("exact cloud domain not confirmed")
	}
	raw, e = os.ReadFile("/home/ddyidc/cdn-live-test.crt")
	if e != nil {
		t.Fatal("test certificate unavailable")
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("invalid certificate")
	}
	cert, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(raw)
	if e = verifyEdgeTLS(ctx, "10.0.2.2:19243", liveTarget, pool, cert); e != nil {
		t.Fatal(e)
	}
	t.Log("VM end-to-end TLS hostname, validity, trust and exact uploaded fingerprint verified through authorized TCP transport")
}
