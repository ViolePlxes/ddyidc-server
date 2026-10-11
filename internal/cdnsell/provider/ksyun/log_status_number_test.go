package ksyun

import (
	"encoding/json"
	"testing"
)

func TestLogStatusWireNumbers(t *testing.T) {
	for _, value := range []string{"5", "60", "1440"} {
		raw := map[string]any{"Datas": []any{map[string]any{"DomainId": "target", "Status": "start", "Granularity": json.Number(value)}}}
		got, err := normalizeLogStatus(raw, "target")
		if err != nil || got["granularity"] != value {
			t.Fatalf("wire number %s rejected: %v", value, err)
		}
	}
	for _, value := range []string{"5.5", "0", "1441"} {
		raw := map[string]any{"Datas": []any{map[string]any{"DomainId": "target", "Status": "start", "Granularity": json.Number(value)}}}
		if _, err := normalizeLogStatus(raw, "target"); err == nil {
			t.Fatalf("invalid number accepted: %s", value)
		}
	}
}
