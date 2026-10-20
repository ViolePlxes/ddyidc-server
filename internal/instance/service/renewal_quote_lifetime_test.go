package instance

import (
	"testing"
	"time"
)

func TestRenewalQuoteLifetimeBounds(t *testing.T) {
	for raw, want := range map[string]int{"": 300, "300": 300, "30": 30, "900": 900} {
		got, err := quoteLifetime(raw)
		if err != nil || got != time.Duration(want)*time.Second {
			t.Fatal(raw, got, err)
		}
	}
	for _, raw := range []string{"0", "29", "901", "-1", "5m", "300.5", "99999999999999999999999"} {
		if _, err := quoteLifetime(raw); err == nil {
			t.Fatal("invalid explicit validity accepted", raw)
		}
	}
}
