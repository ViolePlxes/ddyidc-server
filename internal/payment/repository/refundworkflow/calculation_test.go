package refundworkflow

import (
	balance "ddyidc/internal/billing/balance"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCalculateConfirmedDailyRefundPolicy(t *testing.T) {
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(10 * 24 * time.Hour)
	snapshot := Snapshot{Paid: 12000, MinimumSetup: 1000, Periods: []Period{{Paid: 12000, Start: &start, End: &end}}}
	fees := Fees{Setup: "10.00", Installation: "5.00", Handling: "2.00", Gift: "3.00", Traffic: "7.00", ValueAdded: "8.00", Evidence: "逐项核对原始服务账单与赠送余额流水"}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var restored Snapshot
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Paid != snapshot.Paid || restored.MinimumSetup != snapshot.MinimumSetup {
		t.Fatal("snapshot money changed at JSON boundary")
	}
	snapshot = restored
	tests := []struct {
		name    string
		elapsed time.Duration
		refund  balance.Money
		used    int64
	}{
		{"未使用", 0, 8500, 0},
		{"不足一天扣一天", time.Minute, 7500, 1},
		{"恰好两天", 48 * time.Hour, 6500, 2},
		{"两天多一分钟扣三天", 48*time.Hour + time.Minute, 5500, 3},
		{"到期", 240 * time.Hour, 0, 10},
		{"超过到期", 241 * time.Hour, 0, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Calculate(snapshot, start.Add(tt.elapsed), fees)
			if err != nil {
				t.Fatal(err)
			}
			if got.Refund != tt.refund || got.Periods[0].UsedDays != tt.used {
				t.Fatalf("refund=%s used=%d", got.Refund.String(), got.Periods[0].UsedDays)
			}
		})
	}
	t.Run("不默认缺失费用为零", func(t *testing.T) {
		bad := fees
		bad.Gift = ""
		_, err := Calculate(snapshot, start, bad)
		if !errors.Is(err, ErrInput) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("不遗漏已知开通费", func(t *testing.T) {
		bad := fees
		bad.Setup = "0.00"
		_, err := Calculate(snapshot, start, bad)
		if !errors.Is(err, ErrInput) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("扣费超出不产生负数", func(t *testing.T) {
		bad := fees
		bad.Traffic = "999.00"
		got, err := Calculate(snapshot, start, bad)
		if err != nil || got.Refund != 0 {
			t.Fatalf("got %+v %v", got, err)
		}
	})
	t.Run("未知计费周期不能估算", func(t *testing.T) {
		bad := snapshot
		bad.Warnings = []string{"缺少原始账单"}
		_, err := Calculate(bad, start, fees)
		if !errors.Is(err, ErrCalculation) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestCalculatePartialProviderDayAndNoService(t *testing.T) {
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(25 * time.Hour)
	fees := Fees{Setup: "0.00", Installation: "0.00", Handling: "0.00", Gift: "0.00", Traffic: "0.00", ValueAdded: "0.00", Evidence: "原账单核查"}
	snapshot := Snapshot{Paid: 2000, Periods: []Period{{Paid: 2000, Start: &start, End: &end}}}
	got, err := Calculate(snapshot, start, fees)
	if err != nil || got.Refund != 1000 || got.Periods[0].PartialDayDeductionDays != 1 {
		t.Fatalf("got %+v %v", got, err)
	}
	fees.Setup = "20.00"
	snapshot.Periods = []Period{{Paid: 2000, Unstarted: true}}
	got, err = Calculate(snapshot, start, fees)
	if err != nil || got.Refund != 0 {
		t.Fatalf("got %+v %v", got, err)
	}
}
