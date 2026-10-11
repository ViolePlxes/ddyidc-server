package ksyun

import (
	"encoding/json"
	"testing"
)

func TestLogServiceOfficialWire(t *testing.T) {
	r, e := newLogServiceRequest("target", "start", "60")
	if e != nil {
		t.Fatal(e)
	}
	if r.GetHttpMethod() != "GET" {
		t.Fatal("legacy log API must use query parameters")
	}
	if r.GetVersion() != "2016-09-01" || r.GetPath() != "/2016-09-01/log/SetDomainLogService" {
		t.Fatal("wrong route/version")
	}
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	var body map[string]any
	json.Unmarshal(b, &body)
	if body["Granularity"] != float64(60) {
		t.Fatal("granularity is not JSON integer", string(b))
	}
	if _, e = newLogServiceRequest("target", "start", "61"); e == nil {
		t.Fatal("invalid granularity accepted")
	}
	r, e = newLogServiceRequest("target", "stop", "")
	if e != nil || r.Granularity != 0 {
		t.Fatal("stop requires optional granularity", e)
	}
}
