package cdnsell

import (
	"ddyidc/internal/cdnsell/provider"
	"errors"
	"testing"
	"time"
)

func TestStatisticsTenantScope(t *testing.T) {
	rows := []DomainRecord{{ID: 1, UserID: 7, ServiceNo: "cloud", OwnershipStatus: "verified"}}
	for _, c := range []struct {
		ids   []int64
		user  int64
		admin bool
		ok    bool
	}{{[]int64{1}, 7, false, true}, {[]int64{1}, 8, false, false}, {[]int64{1, 2}, 7, false, false}, {[]int64{1, 1}, 7, false, false}, {nil, 7, false, false}, {[]int64{1}, 8, true, true}} {
		_, e := statisticsDomains(rows, c.ids, c.user, c.admin)
		if (e == nil) != c.ok {
			t.Fatalf("scope result for %+v: %v", c, e)
		}
	}
	rows[0].Deleted = true
	if _, e := statisticsDomains(rows, []int64{1}, 7, true); !errors.Is(e, ErrForbidden) {
		t.Fatal("deleted accessible")
	}
}
func TestStatisticsLimits(t *testing.T) {
	now := time.Now()
	q := provider.StatisticsQuery{Kind: "bandwidth", Start: now.Add(-time.Hour), End: now, Interval: 5}
	if e := q.Validate(now, 1); e != nil {
		t.Fatal(e)
	}
	bad := q
	bad.Start = now.Add(-31 * 24 * time.Hour)
	if bad.Validate(now, 10) == nil {
		t.Fatal("unbounded points")
	}
	bad = q
	bad.End = now.Add(time.Hour)
	if bad.Validate(now, 1) == nil {
		t.Fatal("future range")
	}
	bad = q
	bad.Source = "origin"
	bad.Kind = "hit"
	if bad.Validate(now, 1) == nil {
		t.Fatal("unsupported source silently accepted")
	}
	bad = q
	bad.ISP = "CT"
	if bad.Validate(now, 1) == nil {
		t.Fatal("ISP without mainland")
	}
}
