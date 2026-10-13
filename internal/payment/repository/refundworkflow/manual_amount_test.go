package refundworkflow

import "testing"

func TestSelectManualRefundAmount(t *testing.T) {
	base := Calculation{Paid: 10000, Refund: 8700, Unused: 8700}
	for _, tt := range []struct {
		mode, amount string
		want         int64
		valid        bool
	}{{"daily", "", 8700, true}, {"full", "", 10000, true}, {"custom", "25.50", 2550, true}, {"custom", "100.00", 10000, true}, {"custom", "100.01", 0, false}, {"custom", "-1.00", 0, false}, {"custom", "0.00", 0, false}, {"custom", "1.001", 0, false}, {"bad", "", 0, false}} {
		got, err := SelectAmount(base, tt.mode, tt.amount)
		if tt.valid {
			if err != nil || int64(got.Refund) != tt.want || got.Unused != base.Unused {
				t.Fatalf("%+v: %+v %v", tt, got, err)
			}
		} else if err == nil {
			t.Fatalf("invalid amount accepted: %+v", tt)
		}
	}
}
