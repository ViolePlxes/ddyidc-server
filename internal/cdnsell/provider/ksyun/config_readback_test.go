package ksyun

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOfficialConfigReadback(t *testing.T) {
	raw := map[string]any{"ReferProtectionConfig": map[string]any{"Enable": "on", "ReferType": "allow", "ReferList": "a.example.com,b.example.com", "AllowEmpty": "off"}}
	want := map[string]any{"enabled": true, "type": "whitelist", "values": []any{"a.example.com", "b.example.com"}, "allowEmpty": false}
	if got := normalizeReadback("referer", raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected mapping %#v", got)
	}
	missing := normalizeReadback("http2", map[string]any{"Http2OptionConfig": map[string]any{}})
	if _, ok := missing["enabled"]; ok {
		t.Fatal("missing cloud field fabricated")
	}
	if protectionType(map[string]any{"type": "blacklist"}) != "block" {
		t.Fatal("API protection type mismatch")
	}
}

func TestBasicOriginReadback(t *testing.T) {
	got := normalizeReadback("basic_origin", map[string]any{"region": "OverSea", "originType": "ipaddr", "originHost": "192.0.2.10", "originProtocol": "http"})
	if got["accelerateRegion"] != "overseas" || got["originType"] != "ip" || got["originHost"] != "192.0.2.10" {
		t.Fatal("basic origin mapping mismatch", got)
	}
}

func TestOriginHostBooleanAndMissingReadback(t *testing.T) {
	got := normalizeReadback("origin_host", map[string]any{"BackOriginHostConfig": map[string]any{"BackOriginHost": "test.example.com", "FollowReqDomain": "false"}})
	if got["followRequestDomain"] != false || got["host"] != "test.example.com" {
		t.Fatal("official boolean mapping mismatch", got)
	}
	got = normalizeReadback("origin_host", map[string]any{"BackOriginHostConfig": map[string]any{"BackOriginHost": "test.example.com"}})
	if _, exists := got["followRequestDomain"]; exists {
		t.Fatal("missing follow flag fabricated")
	}
}
func TestLiveConfigResponseShapes(t *testing.T) {
	for _, body := range []string{
		`{"BackOriginHostConfig":{"BackOriginHost":"test.example.com"}}`,
		`{"DomainConfigs":"{\"BackOriginHostConfig\":{\"BackOriginHost\":\"test.example.com\"}}"}`,
		`{"DomainConfigs":{"BackOriginHostConfig":{"BackOriginHost":"test.example.com"}}}`,
	} {
		var response map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatal(err)
		}
		raw, err := decodeDomainConfigs(response)
		if err != nil {
			t.Fatal(err)
		}
		got := normalizeReadback("origin_host", raw)
		if got["host"] != "test.example.com" {
			t.Fatal("live config lost", got)
		}
	}
	for _, body := range []string{`{"DomainConfigs":"not JSON"}`, `{"DomainConfigs":[]}`} {
		var response map[string]json.RawMessage
		json.Unmarshal([]byte(body), &response)
		if _, err := decodeDomainConfigs(response); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
}

func TestStructuredConfigReadback(t *testing.T) {
	got := normalizeReadback("cache_rules", map[string]any{"CacheRuleConfig": map[string]any{"CacheRules": []any{map[string]any{"CacheRuleType": "global", "CacheTime": float64(60), "CacheEnable": "on", "RespectOrigin": "off"}}}})
	want := map[string]any{"rules": []any{map[string]any{"type": "global", "seconds": float64(60), "enabled": true, "respectOrigin": false}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cache mapping mismatch %#v", got)
	}
	got = normalizeReadback("performance", map[string]any{"PageCompressConfig": map[string]any{"Enable": "on", "FileType": "text", "FileSize": float64(1024)}, "BrCompressConfig": map[string]any{"Enable": "off"}})
	if got["gzip"] != true || got["br"] != false || got["fileTypes"] != "text" {
		t.Fatal("compression mapping mismatch", got)
	}
	got = normalizeReadback("cache_rules", map[string]any{"CacheRuleConfig": map[string]any{}})
	if _, exists := got["rules"]; exists {
		t.Fatal("missing cache rules fabricated")
	}
}

func TestLogStatusIdentityAndGranularity(t *testing.T) {
	raw := map[string]any{"Datas": []any{map[string]any{"DomainId": "other", "Status": "start", "Granularity": float64(5)}, map[string]any{"DomainId": "target", "Status": "stop", "Granularity": float64(1440)}}}
	got, err := normalizeLogStatus(raw, "target")
	if err != nil || got["action"] != "stop" || got["granularity"] != "1440" {
		t.Fatal("log mapping mismatch", got, err)
	}
	if _, err := normalizeLogStatus(raw, "missing"); err == nil {
		t.Fatal("missing domain treated as verified")
	}
	if _, err := normalizeLogStatus(map[string]any{}, "target"); err == nil {
		t.Fatal("empty response treated as verified")
	}
}
