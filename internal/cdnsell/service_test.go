package cdnsell

import (
	"testing"
	"time"
)

func TestAllocateFIFOByExpiry(t *testing.T) {
	now := time.Now()
	rows := []Batch{{ID: 2, RemainingBytes: 80, ExpireTime: now.Add(48 * time.Hour), Status: "running"}, {ID: 1, RemainingBytes: 40, ExpireTime: now.Add(24 * time.Hour), Status: "running"}}
	over, out := allocateFIFO(rows, 70)
	if over != 0 || out[0].ID != 1 || out[0].RemainingBytes != 0 || out[0].Status != "exhausted" || out[1].RemainingBytes != 50 {
		t.Fatalf("unexpected allocation: over=%d rows=%+v", over, out)
	}
}
func TestAllocateFIFOReportsOverage(t *testing.T) {
	over, _ := allocateFIFO([]Batch{{ID: 1, RemainingBytes: 10, ExpireTime: time.Now(), Status: "running"}}, 25)
	if over != 15 {
		t.Fatalf("want 15, got %d", over)
	}
}
