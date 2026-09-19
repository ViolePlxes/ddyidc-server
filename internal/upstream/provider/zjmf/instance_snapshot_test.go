package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInstanceSnapshotUsesProvisionedValues(t *testing.T) {
	result := &contracts.InstanceDetail{}
	data := map[string]any{"config_options": []any{map[string]any{"id": "1", "name_k": "cpu", "name": "CPU", "sub_name": "4核"}, map[string]any{"id": "2", "name_k": "os", "name": "系统", "sub_name": "Ubuntu 24.04"}, map[string]any{"name_k": "password", "name": "密码", "sub_name": "private"}}}
	host := map[string]any{"id": "100", "productid": "353", "billingcycle": "monthly", "regdate": "2026-09-20", "nextduedate": "2026-10-20"}
	if e := parseInstanceSnapshot(data, host, 100, result); e != nil {
		t.Fatal(e)
	}
	if result.ProductID != "353" || len(result.Config) != 2 || result.Config[0].Value != "4核" || result.OperatingSystem != "Ubuntu 24.04" || result.ActivatedAt == nil || result.ExpiresAt == nil {
		t.Fatal("actual snapshot not preserved")
	}
	if e := parseInstanceSnapshot(data, host, 101, result); e == nil {
		t.Fatal("mismatched identity accepted")
	}
}
func TestWriteTransportFailureIsNotRetried(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer s.Close()
	p := New(contracts.Supplier{})
	if _, _, e := p.request(http.MethodPost, s.URL+"/cart/settle", nil, "", ""); e == nil {
		t.Fatal("failure ignored")
	}
	if calls != 1 {
		t.Fatalf("write retried %d times", calls)
	}
}
func TestQKIDC353ExplicitlyAllowed(t *testing.T) {
	p := New(contracts.Supplier{URL: "https://www.qkidc.cn"})
	if e := p.enforceQKIDCProduct(353); e != nil {
		t.Fatal(e)
	}
	if e := p.enforceQKIDCProduct(354); e == nil {
		t.Fatal("unrelated product allowed")
	}
}
