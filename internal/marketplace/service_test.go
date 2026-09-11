package marketplace

import "testing"

func TestMoneyParsingIsExact(t *testing.T) {
	tests := []struct {
		input string
		want  int64
		ok    bool
	}{
		{"0", 0, true}, {"1", 100, true}, {"12.3", 1230, true}, {"12.34", 1234, true},
		{"-1", 0, false}, {"1.234", 0, false}, {"1e3", 0, false}, {"99999999.99", 9999999999, true}, {"100000000", 0, false},
	}
	for _, tc := range tests {
		got, err := parseMoney(tc.input)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Fatalf("parseMoney(%q)=(%d,%v), want (%d,ok=%v)", tc.input, got, err, tc.want, tc.ok)
		}
	}
}

func TestCalculateFeeByBearer(t *testing.T) {
	for _, tc := range []struct {
		bearer             string
		buyer, seller, fee int64
	}{
		{"seller", 10000, 9500, 500},
		{"buyer", 10500, 10000, 500},
		{"platform", 10000, 10000, 500},
	} {
		fee, buyer, seller, err := calculateFee(10000, &Settings{FeeType: "ratio", FeeRate: "5.00", FeeBearer: tc.bearer})
		if err != nil || fee != tc.fee || buyer != tc.buyer || seller != tc.seller {
			t.Fatalf("bearer=%s got fee/buyer/seller=%d/%d/%d err=%v", tc.bearer, fee, buyer, seller, err)
		}
	}
}

func TestFixedFeeCannotConsumeSellerAmount(t *testing.T) {
	_, _, _, err := calculateFee(100, &Settings{FeeType: "fixed", FeeFixed: "1.01", FeeBearer: "seller"})
	if err == nil {
		t.Fatal("expected fee-over-amount rejection")
	}
}
