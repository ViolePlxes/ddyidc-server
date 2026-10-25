package instance

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdminRemarkRoutesRemoved(t *testing.T) {
	h := &handler{}
	customerRoutes := 0
	for _, route := range append(h.managementRoutes(), h.adminListRoutes()...) {
		if route.Method != http.MethodPut || !strings.HasSuffix(route.Path, "/remark") {
			continue
		}
		if strings.HasPrefix(route.Path, "/admin/") {
			t.Fatal("administrator remark edit route remains", route.Path)
		}
		if route.Path == "/api/v1/instance/:instanceId/remark" {
			customerRoutes++
		}
	}
	if customerRoutes != 1 {
		t.Fatal("customer's own remark route must remain exactly once")
	}
}
