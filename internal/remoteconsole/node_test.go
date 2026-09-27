package remoteconsole

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestNodeBoundSessionsFailClosed(t *testing.T) {
	source := staticSource{endpoint: &RemoteUpstream{URL: "ws://127.0.0.1/websockify"}}
	a, b := NewNoVNCProvider(source), NewNoVNCProvider(source)
	for _, bad := range []string{"http://host", "../node", "node.a", "A", "a b", strings.Repeat("a", 33)} {
		if a.SetNodeID(bad) == nil {
			t.Fatal("unsafe node identifier accepted")
		}
	}
	if a.SetNodeID("node-a") != nil || b.SetNodeID("node-b") != nil {
		t.Fatal("valid nodes rejected")
	}
	sa, err := a.CreateSession(context.Background(), 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := b.CreateSession(context.Background(), 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sa.Token, "node-a.") {
		t.Fatal("routing prefix missing")
	}
	secret, err := hex.DecodeString(strings.TrimPrefix(sa.Token, "node-a."))
	if err != nil || len(secret) != 32 {
		t.Fatal("session entropy reduced")
	}
	if _, err := b.Claim(123, 456, sa.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("foreign node accepted token")
	}
	if _, err := b.Claim(123, 456, strings.Replace(sa.Token, "node-a.", "node-b.", 1)); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("altered routing prefix accepted")
	}
	if _, err := a.Claim(123, 456, sa.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Claim(123, 456, sb.Token); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(a.SetNodeID("node-c"), ErrSessionBusy) {
		t.Fatal("live node identity changed")
	}
	restarted := NewNoVNCProvider(source)
	_ = restarted.SetNodeID("node-a")
	if _, err := restarted.Claim(123, 456, sa.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("restart accepted an absent session")
	}
}
