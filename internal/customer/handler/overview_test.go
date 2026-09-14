package customer

import (
	"testing"
	"time"
)

func TestOverviewSeries(t *testing.T) {
	start := time.Date(2026, 9, 12, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
	points := overviewSeries(start, 3, []overviewDay{{Day: "2026-09-12", Value: 12.5}, {Day: "2026-09-14", Value: -3.25}})
	if len(points) != 3 || points[0].Value != 12.5 || points[1].Value != 0 || points[2].Value != -3.25 {
		t.Fatalf("daily aggregation lost signed values or empty days: %+v", points)
	}
	if points[2].Time-points[0].Time != 48*3600*1000 {
		t.Fatal("incorrect time axis")
	}
}
