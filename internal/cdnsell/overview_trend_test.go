package cdnsell

import (
	"testing"
	"time"
)

func TestDailyDeliveryTrendShanghaiBoundaryAndMissingDays(t *testing.T) {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	start := time.Date(2026, 10, 14, 0, 0, 0, 0, zone)
	windows := []TrendPoint{{Time: start.Add(-time.Second), Bytes: 900}, {Time: start.Add(23 * time.Hour).UTC(), Bytes: 10}, {Time: start.Add(25 * time.Hour).UTC(), Bytes: 20}, {Time: start.Add(26 * time.Hour), Bytes: 5}, {Time: start.AddDate(0, 0, 3), Bytes: 900}}
	points := dailyDeliveryTrend(start, 3, windows)
	if len(points) != 3 || points[0].Bytes != 10 || points[1].Bytes != 25 || points[2].Bytes != 0 {
		t.Fatalf("wrong daily buckets: %+v", points)
	}
	if !points[0].Time.Equal(start) {
		t.Fatal("timezone boundary changed")
	}
}
