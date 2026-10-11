package ksyun

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Explicit opt-in, read-only smoke check. Never called by normal tests.
func TestLiveReadOnlyConnection(t *testing.T) {
	path := os.Getenv("CDN_LIVE_CREDENTIAL_FILE")
	if path == "" {
		t.Skip("explicit private credential file required")
	}
	st, e := os.Stat(path)
	if e != nil || st.Mode().Perm()&0077 != 0 {
		t.Fatal("credential file must be private")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("credential file unavailable")
	}
	var a provider.Account
	if json.Unmarshal(raw, &a) != nil {
		t.Fatal("invalid credential file")
	}
	raw = nil
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if e = New().TestConnection(ctx, a); e != nil {
		t.Fatal("read-only connection failed:", e)
	}
	t.Log("official CDN API read-only connection passed; no domain mutations")
}
