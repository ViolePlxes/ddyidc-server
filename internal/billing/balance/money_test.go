package billing

import "testing"

func TestMoneyExactBoundary(t *testing.T) {
	for input, want := range map[string]Money{"0": 0, "0.01": 1, "-0.01": -1, "12.3": 1230, "999999999999.99": MaxMoney} {
		got, err := ParseMoney(input)
		if err != nil || got != want {
			t.Fatalf("%s: %v %v", input, got, err)
		}
		var scanned Money
		if err = scanned.Scan([]byte(got.String())); err != nil || scanned != got {
			t.Fatalf("round trip: %s %v", input, err)
		}
	}
	for _, input := range []string{"", "+1", "1e2", "NaN", "1.001", " 1", "01", "1000000000000", "--1", ".1", "1."} {
		if _, err := ParseMoney(input); err == nil {
			t.Errorf("accepted invalid amount %q", input)
		}
	}
	var scanned Money
	if err := scanned.Scan(float64(0.1)); err == nil {
		t.Fatal("accepted float")
	}
}
