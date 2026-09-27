package groupbuy

import (
	"encoding/json"
	"testing"
	"time"
)

func int64Ptr(v int64) *int64 { return &v }
func intPtr(v int) *int       { return &v }

func TestActivityPriceCustomAndLeader(t *testing.T) {
	a := &Activity{OriginalPrice: 10000, PriceMode: 1, TwoPrice: int64Ptr(8000), TwoLeaderPrice: int64Ptr(7000)}
	member, err := activityPrice(a, "two", false)
	if err != nil || member != 8000 {
		t.Fatalf("member price = %d, err=%v", member, err)
	}
	leader, err := activityPrice(a, "two", true)
	if err != nil || leader != 7000 {
		t.Fatalf("leader price = %d, err=%v", leader, err)
	}
}

func TestActivityPriceDiscount(t *testing.T) {
	a := &Activity{OriginalPrice: 10001, PriceMode: 2, ThreeDiscount: intPtr(10), ThreeLeaderDiscount: intPtr(20)}
	member, err := activityPrice(a, "three", false)
	if err != nil || member != 9000 {
		t.Fatalf("member price = %d, err=%v", member, err)
	}
	leader, err := activityPrice(a, "three", true)
	if err != nil || leader != 8000 {
		t.Fatalf("leader price = %d, err=%v", leader, err)
	}
}

func TestGroupSizesAndDeadline(t *testing.T) {
	for kind, want := range map[string]int{"two": 2, "three": 3, "five": 5, "bad": 0} {
		if got := groupSize(kind); got != want {
			t.Fatalf("groupSize(%s)=%d want %d", kind, got, want)
		}
	}
	now := time.Now()
	if got := minTime(now.Add(time.Hour), now); !got.Equal(now) {
		t.Fatal("minTime did not choose the earlier deadline")
	}
}

func TestOrderInputUsesActivitySnapshot(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"durationMonth": 1,
		"configOptions": map[string]any{"1": map[string]any{"value": "2", "field": "cpu", "displayName": "CPU", "displayValue": "2核"}},
	})
	input, err := orderInput(raw, Activity{ProductID: 42, BillingCycle: "monthly"}, 99)
	if err != nil {
		t.Fatal(err)
	}
	if input.ProductID != "42" || input.BillingCycle != "monthly" || input.Hostname != "gb-99" || len(input.Password) != 20 {
		t.Fatal("unexpected product, cycle, hostname or generated password length")
	}
}

func TestInvalidGroupPriceRejected(t *testing.T) {
	_, err := activityPrice(&Activity{OriginalPrice: 10000, PriceMode: 2, FiveDiscount: intPtr(100)}, "five", false)
	if err == nil {
		t.Fatal("expected invalid discount error")
	}
}
