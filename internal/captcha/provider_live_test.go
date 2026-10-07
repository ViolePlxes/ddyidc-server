package captcha

import (
	"context"
	"ddyidc/pkg/outbound"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// This opt-in test sends only an intentionally invalid proof, never solves a
// challenge or prints vendor credentials. It validates the real HTTPS path.
func TestLiveInvalidProofRejected(t *testing.T) {
	path := os.Getenv("CAPTCHA_GEETEST_TEST_CONFIG")
	if path == "" {
		t.Skip("requires isolated VM private vendor configuration")
	}
	if !strings.HasPrefix(path, "/home/ddyidc/captcha-20261007/") {
		t.Fatal("non-isolated vendor config refused")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("private test configuration unavailable")
	}
	var c struct {
		SiteID string `json:"siteId"`
		Secret string `json:"secret"`
	}
	if json.Unmarshal(b, &c) != nil {
		t.Fatal("invalid test config")
	}
	if e = outbound.Configure(outbound.Config{DNSTLSAddress: "1.1.1.1:853", DNSTLSServerName: "one.one.one.one"}); e != nil {
		t.Fatal(e)
	}
	g := &Geetest{ID: c.SiteID, Key: c.Secret}
	e = g.Verify(context.Background(), Proof{"lot_number": strings.Repeat("0", 32), "captcha_output": "test-invalid", "pass_token": "test-invalid", "gen_time": "1"})
	if !errors.Is(e, ErrProof) {
		t.Fatalf("real HTTPS validation path unavailable: %v", e)
	}
}
