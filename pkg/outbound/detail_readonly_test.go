package outbound

import (
	"errors"
	"net/http"
	"testing"
)

func TestDetailReadOnlyFence(t *testing.T) {
	if _, err := New(Config{DetailReadOnly: true}); !errors.Is(err, ErrDenied) { t.Fatal("read-only without hosts must fail closed") }
	if err := Configure(Config{DetailReadOnly: true, Hosts: []string{"supplier.example"}}); err != nil { t.Fatal(err) }
	t.Cleanup(func(){ _ = Configure(Config{}) })
	for _, tc := range []struct{method,path string; allow bool}{
		{"POST","/zjmf_api_login",true}, {"GET","/api/product/list",true},
		{"GET","/api/product/12?price_basis=agent",true}, {"GET","/host/header?host_id=19",true},
		{"GET","/v10/host/product/12/mf_cloud/order_page?price_basis=agent&is_downstream=1",true},
		{"GET","/cart/set_config?pid=12",false}, {"POST","/provision/default",false},
		{"GET","/provision/default?func=on&id=19",false}, {"GET","/host/renewpage?hostid=19",false},
		{"POST","/api/product/12?price_basis=agent",false}, {"GET","/host/header?host_id=19&func=on",false},
		{"GET","/host/header?host_id=19&host_id=20",false}, {"GET","/host/header?host_id=-1",false},
		{"GET","/api/product/12?price_basis=agent&bad=%zz",false}, {"GET","/host/%68eader?host_id=19",false},
		{"POST","/zjmf_api_login?func=pay",false},
	} {
		r, _ := http.NewRequest(tc.method,"https://supplier.example"+tc.path,nil)
		err := CheckDetailRequest(r)
		if (err == nil) != tc.allow { t.Errorf("%s %s: %v",tc.method,tc.path,err) }
	}
	for _, raw := range []string{"https://other.example/host/header?host_id=19","http://supplier.example/host/header?host_id=19"} {
		r,_ := http.NewRequest("GET",raw,nil)
		if CheckDetailRequest(r)==nil { t.Errorf("unsafe destination allowed: %s",raw) }
	}
}
