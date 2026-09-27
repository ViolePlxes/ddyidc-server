package zjmf

import "testing"

func TestPurchaseQuoteStrictFinanceAmounts(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"products":{"billingcycle":"monthly","sale_total":"21.00","total":"30.00","type":{"type":1,"bates":"70.00"}}}`, "21.00"},
		{`{"products":{"billingcycle":"monthly","sale_total":"30.00","type":{"type":0,"bates":"9.00"}}}`, "21.00"},
		{`{"products":{"billingcycle":"annually","sale_total":"21.00","type":{"type":1}}}`, ""},
		{`{"products":{"billingcycle":"monthly","total":"30.00","type":{"type":1}}}`, ""},
		{`{"products":{"billingcycle":"monthly","sale_total":"0","type":{"type":1}}}`, ""},
		{`{"products":{"billingcycle":"monthly","sale_total":21.001,"type":{"type":1}}}`, ""},
		{`{"products":{"billingcycle":"monthly","sale_total":2.1e1,"type":{"type":1}}}`, ""},
		{`{"products":{"billingcycle":"monthly","sale_total":"21.00","type":{"type":0}}}`, ""},
		{`{"products":{"billingcycle":"monthly","sale_total":"21.00","type":{"type":0,"bates":"22.00"}}}`, ""},
	} {
		got, err := purchaseQuoteAmount([]byte(tc.body), "monthly")
		if tc.want == "" {
			if err == nil {
				t.Fatal("invalid quote accepted")
			}
			continue
		}
		if err != nil || got.String() != tc.want {
			t.Fatalf("quote=%v error=%v", got, err)
		}
	}
}
