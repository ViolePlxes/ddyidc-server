package zjmf

import (
	"bytes"
	"ddyidc/pkg/outbound"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// httptest uses ephemeral loopback ports. This exception exists only in the
// test executable; release defaults reject private destinations and odd ports.
func TestMain(m *testing.M) {
	ports := make([]int, 65535)
	for i := range ports {
		ports[i] = i + 1
	}
	if err := outbound.Configure(outbound.Config{PrivateCIDRs: []string{"127.0.0.1/32", "::1/128"}, Ports: ports}); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestSupplierOversizedResponseRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), (20<<20)+1))
	}))
	defer server.Close()
	p := &Provider{}
	payload, _, err := p.request(http.MethodGet, server.URL, nil, "", "")
	if err == nil || payload != nil {
		t.Fatal("oversized response silently accepted")
	}
}
