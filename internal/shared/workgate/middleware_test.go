package workgate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPFailsClosedWithoutControlPool(t *testing.T) {
	old := active.Swap(nil)
	defer active.Store(old)
	for _, path := range []string{"/api/v1/payment/alipay/notify", "/api/v1/payment/notify/wechat", "/console/v1/orders"} {
		called := false
		out := httptest.NewRecorder()
		Middleware(func(http.ResponseWriter, *http.Request) { called = true })(out, httptest.NewRequest("POST", path, nil))
		if called || out.Code != 503 || out.Header().Get("Retry-After") == "" {
			t.Fatal("maintenance acknowledged work", path)
		}
	}
	out := httptest.NewRecorder()
	Middleware(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })(out, httptest.NewRequest("GET", "/health/live", nil))
	if out.Code != 200 {
		t.Fatal("liveness blocked")
	}
}
