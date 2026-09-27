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
	configureLocalProductPolicy(t)
	p := New(contracts.Supplier{URL: "https://www.qkidc.cn"})
	if e := p.enforceTestProductPolicy(353); e != nil {
		t.Fatal(e)
	}
	if e := p.enforceTestProductPolicy(354); e == nil {
		t.Fatal("unrelated product allowed")
	}
}

func TestLoginPortPreservesFinanceDefaultAndExplicitOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		host map[string]any
		data map[string]any
		want string
	}{
		{"finance default", map[string]any{"port": float64(0)}, nil, "默认"},
		{"explicit", map[string]any{"port": "2222"}, nil, "2222"},
		{"zero does not mask alias", map[string]any{"port": 0, "ssh_port": "22022"}, nil, "22022"},
		{"missing is not default", map[string]any{}, nil, ""},
		{"invalid", map[string]any{"port": "65536"}, nil, ""},
		{"custom field", map[string]any{"port": 0}, map[string]any{"custom_field_data": []any{map[string]any{"fieldname": "端口", "value": "10022"}}}, "10022"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := instanceLoginPort(tc.data, tc.host); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
func TestReinstallCustomPortUsesV10FinanceParameters(t *testing.T) {
	form := instanceActionForm(53559, "reinstall", contracts.InstanceActionInput{Action: "reinstall", ImageID: "123", Port: "22022"})
	if form.Get("port") != "22022" || form.Get("os") != "123" || form.Get("format_data_disk") != "0" || form.Get("is_api") != "true" {
		t.Fatal("reinstall parameters changed")
	}
}

func TestExplicitFinanceDefaultResolvedOnlyForKnownOS(t *testing.T) {
	for _, tc := range []struct {
		os, port, want string
		isDefault      bool
	}{
		{"Ubuntu-24.04", "0", "22", true}, {"Windows Server 2022", "0", "3389", true},
		{"Unknown", "0", "默认", true}, {"Ubuntu-24.04", "22022", "22022", false},
		{"Ubuntu-24.04", "", "", false},
	} {
		result := &contracts.InstanceDetail{OperatingSystem: tc.os}
		if err := parseInstanceSnapshot(nil, map[string]any{"id": "53559", "port": tc.port}, 53559, result); err != nil {
			t.Fatal(err)
		}
		if result.Port != tc.want || result.PortIsDefault != tc.isDefault {
			t.Fatalf("incorrect port interpretation for %s", tc.os)
		}
	}
}
