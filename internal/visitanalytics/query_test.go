package visitanalytics

import (
	"net/url"
	"testing"
	"time"
)

func shanghaiTime(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 11, 9, 15, 4, 5, 0, time.Local)
}

func TestParseRangeDefaultsToToday(t *testing.T) {
	window, err := ParseRange(url.Values{}, shanghaiTime(t))
	if err != nil {
		t.Fatal(err)
	}
	if window.FromDate != "2026-11-09" || window.ToDate != "2026-11-09" || window.Days != 1 {
		t.Fatalf("range = %+v", window)
	}
	if window.To.Sub(window.From) != 24*time.Hour {
		t.Fatalf("exclusive end = %v", window.To)
	}
}

func TestParseRangeAcceptsInclusiveWindow(t *testing.T) {
	values := url.Values{"from": {"2026-11-01"}, "to": {"2026-11-09"}}
	window, err := ParseRange(values, shanghaiTime(t))
	if err != nil {
		t.Fatal(err)
	}
	if window.Days != 9 {
		t.Fatalf("days = %d, want 9", window.Days)
	}
	if window.ToDate != "2026-11-09" {
		t.Fatalf("to = %s", window.ToDate)
	}
}

func TestParseRangeRejectsInvalidWindows(t *testing.T) {
	cases := map[string]url.Values{
		"reversed":  {"from": {"2026-11-09"}, "to": {"2026-11-01"}},
		"malformed": {"from": {"09/11/2026"}},
		"too long":  {"from": {"2026-08-01"}, "to": {"2026-11-09"}},
	}
	for name, values := range cases {
		if _, err := ParseRange(values, shanghaiTime(t)); err == nil {
			t.Fatalf("%s range accepted", name)
		}
	}
}

func TestParseRangeAllowsNinetyDays(t *testing.T) {
	values := url.Values{"from": {"2026-08-12"}, "to": {"2026-11-09"}}
	window, err := ParseRange(values, shanghaiTime(t))
	if err != nil {
		t.Fatal(err)
	}
	if window.Days != MaxRangeDays {
		t.Fatalf("days = %d, want %d", window.Days, MaxRangeDays)
	}
}

func TestParseFilterWhitelist(t *testing.T) {
	filter, err := ParseFilter(url.Values{})
	if err != nil || filter.Identity != "all" || filter.Client != "all" {
		t.Fatalf("default filter = %+v, err = %v", filter, err)
	}
	valid := url.Values{"identity": {"customer"}, "client": {"bot"}}
	if filter, err := ParseFilter(valid); err != nil || filter.Identity != "customer" || filter.Client != "bot" {
		t.Fatalf("valid filter rejected: %+v err=%v", filter, err)
	}
	if _, err := ParseFilter(url.Values{"identity": {"root"}}); err == nil {
		t.Fatal("unknown identity accepted")
	}
	if _, err := ParseFilter(url.Values{"client": {"crawler"}}); err == nil {
		t.Fatal("unknown client accepted")
	}
}

func TestTotalDimensionSelection(t *testing.T) {
	cases := []struct {
		filter    Filter
		dimension string
	}{
		{Filter{Identity: "all", Client: "all"}, dimAll},
		{Filter{Identity: "customer", Client: "all"}, dimIdentity},
		{Filter{Identity: "all", Client: "bot"}, dimClientType},
		{Filter{Identity: "admin", Client: "browser"}, dimIdentityClient},
	}
	for _, testCase := range cases {
		dimension, _ := testCase.filter.totalDimension()
		if dimension != testCase.dimension {
			t.Fatalf("filter %+v -> %s, want %s", testCase.filter, dimension, testCase.dimension)
		}
	}
}

func TestDisplayLabel(t *testing.T) {
	if displayLabel("") != "（未知）" || displayLabel("unknown") != "未知" || displayLabel("Chrome") != "Chrome" {
		t.Fatal("unexpected display label")
	}
}
