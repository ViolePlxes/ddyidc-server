package adminread

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestOperationsWireContract(t *testing.T) {
	out := httptest.NewRecorder()
	writeOperations(out, []operationMetric{{Key: "backupAgeSeconds", Label: "备份距今", Value: nil, Unit: "秒"}})
	var result struct {
		Code int `json:"code"`
		Data struct {
			SampledAt string            `json:"sampledAt"`
			Metrics   []operationMetric `json:"metrics"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != 0 || result.Data.SampledAt == "" || len(result.Data.Metrics) != 1 {
		t.Fatal("invalid frontend response envelope")
	}
	if result.Data.Metrics[0].Value != nil {
		t.Fatal("missing observation disguised as zero")
	}
}
