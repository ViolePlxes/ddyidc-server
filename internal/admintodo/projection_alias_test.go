package admintodo

import (
	"strings"
	"testing"
)

// The order branch has no order_id column; a shared helper that emits it breaks
// the whole queue with an unknown-column error on production.
func TestManualProductProjectionColumnAliases(t *testing.T) {
	parts := strings.Split(manualProductProjection(), "UNION ALL")
	if len(parts) != 2 {
		t.Fatalf("projection shape changed: %d", len(parts))
	}
	if strings.Contains(parts[1], "o.order_id") || strings.Contains(parts[1], "o.created_at FROM") {
		t.Fatal("order branch references an instance-only column")
	}
	if !strings.Contains(parts[0], "i.order_id") {
		t.Fatal("instance branch lost its order join key")
	}
}