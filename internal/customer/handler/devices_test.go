package customer

import (
	"ddyidc/pkg/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefreshOriginAndExplicitCredentialGuard(t *testing.T) {
	h := &authHandler{jwt: auth.NewManager("test", "test", 3600, 3600)}
	for _, tc := range []struct {
		header, site, body string
		want               int
	}{
		{"", "same-origin", `{"refresh_token":"test"}`, 403},
		{"1", "cross-site", `{"refresh_token":"test"}`, 403},
		{"1", "same-origin", `invalid`, 400},
		{"1", "same-origin", `{}`, 503},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", strings.NewReader(tc.body))
		req.Header.Set("X-Refresh-Request", tc.header)
		req.Header.Set("Sec-Fetch-Site", tc.site)
		recorder := httptest.NewRecorder()
		h.refreshLogin(recorder, req)
		if recorder.Code != tc.want {
			t.Fatalf("status=%d want=%d", recorder.Code, tc.want)
		}
	}
}
