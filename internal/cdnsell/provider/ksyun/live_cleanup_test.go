package ksyun

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestLiveCleanupVerified(t *testing.T) {
	if os.Getenv("CDN_LIVE_CLEANUP_VERIFY") != "1" {
		t.Skip("explicit cleanup verification required")
	}
	credential := os.Getenv("CDN_LIVE_CREDENTIAL_FILE")
	st, err := os.Stat(credential)
	if err != nil || st.Mode().Perm()&0077 != 0 {
		t.Fatal("private credential required")
	}
	raw, err := os.ReadFile(credential)
	if err != nil {
		t.Fatal(err)
	}
	var account provider.Account
	if json.Unmarshal(raw, &account) != nil {
		t.Fatal("invalid account")
	}
	raw = nil
	raw, err = os.ReadFile(os.Getenv("CDN_LIVE_TEST_STATE_FILE"))
	var state liveState
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Domain != liveTarget || !state.CreatedByTest || state.Phase != "cleanup_accepted" {
		t.Fatal("owned accepted cleanup intent required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for i := 0; i < 6; i++ {
		id, err := findLiveTarget(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		if id == "" {
			t.Log("authorized test domain absent from complete bounded cloud inventory")
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("cleanup not yet confirmed")
		case <-time.After(5 * time.Second):
		}
	}
	t.Fatal("cleanup remains pending; do not recreate or retry mutation")
}
