package cdnsell

import (
	"ddyidc/internal/cdnsell/provider"
	"testing"
	"time"
)

func TestUsageCursorRequiresCompleteSeries(t *testing.T) {
	start := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Minute)
	good := []provider.UsagePoint{{Start: start.Add(5 * time.Minute), End: end, Bytes: 0}, {Start: start, End: start.Add(5 * time.Minute), Bytes: 10}}
	if err := validateUsageSeries(good, start, end); err != nil {
		t.Fatal(err)
	}
	for _, points := range [][]provider.UsagePoint{nil, good[:1], {good[0], good[0]}, {{Start: start, End: start.Add(5 * time.Minute), Bytes: -1}, good[1]}} {
		if validateUsageSeries(points, start, end) == nil {
			t.Fatal("unconfirmed window accepted")
		}
	}
}
