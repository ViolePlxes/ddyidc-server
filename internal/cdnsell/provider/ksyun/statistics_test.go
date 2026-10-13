package ksyun

import (
	"ddyidc/internal/cdnsell/provider"
	"errors"
	"testing"
	"time"
)

func TestStatisticsExplicitDomainAndParameters(t *testing.T) {
	q := provider.StatisticsQuery{Kind: "bandwidth", Start: time.Now().Add(-time.Hour), End: time.Now().Add(-time.Minute), Interval: 5}
	d := provider.Domain{ServiceNo: "known", BusinessType: "web"}
	a, v, p, e := StatisticsRequest(d, "test.example.invalid", q)
	if e != nil || a != "GetServerData" || v != "2020-06-30" || p["Domains"] != "test.example.invalid" || p["Metric"] != "bandwidth,flow" {
		t.Fatalf("%s %s %#v %v", a, v, p, e)
	}
	if _, _, _, e = StatisticsRequest(d, "", q); e == nil {
		t.Fatal("empty domain became all-account query")
	}
	q.Kind = "live"
	if _, _, _, e = StatisticsRequest(d, "test.example.invalid", q); !errors.Is(e, provider.ErrCapabilityNotOpen) {
		t.Fatal("non-live queried")
	}
	d.BusinessType = "live"
	q.Metric = "bandwidth"
	q.StreamURL = "https://other.invalid/live/stream"
	if _, _, _, e = StatisticsRequest(d, "test.example.invalid", q); e == nil {
		t.Fatal("cross-domain stream allowed")
	}
}
func TestStatisticsResponseDoesNotPretendZero(t *testing.T) {
	for _, body := range []string{`{}`, `{"Error":{"Code":"AccessDenied","Message":"secret"}}`, `{"Datas":[{"Time":"2026-10-12T00:00+0800","Flow":"bad"}]}`, `{"Datas":[{"Unknown":123}]}`} {
		if _, e := ParseStatistics("test", body); e == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	v, e := ParseStatistics("test", `{"RequestId":"r","Datas":[]}`)
	if e != nil || len(v.Rows) != 0 {
		t.Fatal(v, e)
	}
	v, e = ParseStatistics("test", `{"Datas":[{"Condition":{"Schema":"https"},"Data":[{"Time":"2026-10-12T00:00+0800","Flow":123,"Request":"5"}]}]}`)
	if e != nil || len(v.Rows) != 1 || v.Rows[0].Values["flow"] != 123 || v.Rows[0].Dimensions["schema"] != "https" {
		t.Fatal(v, e)
	}
}

func TestProvinceBandwidthUsesClientAPIWithoutSpecificFilter(t *testing.T) {
	q := provider.StatisticsQuery{Kind: "bandwidth", View: "province_isp", Source: "edge", Region: "CN", Start: time.Now().Add(-time.Hour), End: time.Now().Add(-time.Minute), Interval: 5}
	d := provider.Domain{ServiceNo: "known", BusinessType: "web"}
	action, _, params, e := StatisticsRequest(d, "test.example.invalid", q)
	if e != nil || action != "GetClientRequestData" || params["Areas"] != "CN" || params["Metric"] != "bandwidth" {
		t.Fatalf("wrong province API: %s %#v %v", action, params, e)
	}
	q.Province = "beijing"
	q.ISP = "CT"
	_, _, params, e = StatisticsRequest(d, "test.example.invalid", q)
	if e != nil || params["Provinces"] != "beijing" || params["Isps"] != "CT" {
		t.Fatal("filters missing", e)
	}
	q.Source = "origin"
	if _, _, _, e = StatisticsRequest(d, "test.example.invalid", q); e == nil {
		t.Fatal("province origin source allowed")
	}
}
