package ksyun

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStrictEdgeTLSProof(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	cert := server.Certificate()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	address := strings.TrimPrefix(server.URL, "https://")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := verifyEdgeTLS(ctx, address, "example.com", pool, cert); e != nil {
		t.Fatal("matching TLS certificate rejected", e)
	}
	if e := verifyEdgeTLS(ctx, address, "unrelated.invalid", pool, cert); e == nil {
		t.Fatal("wrong hostname accepted")
	}
	if e := verifyEdgeTLS(ctx, address, "example.com", x509.NewCertPool(), cert); e == nil {
		t.Fatal("untrusted certificate accepted")
	}
	mismatch := *cert
	mismatch.Raw = []byte("different uploaded certificate")
	if e := verifyEdgeTLS(ctx, address, "example.com", pool, &mismatch); e == nil {
		t.Fatal("different certificate fingerprint accepted")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if e := verifyEdgeTLS(cancelled, address, "example.com", pool, cert); e == nil {
		t.Fatal("cancelled TLS probe accepted")
	}
}
