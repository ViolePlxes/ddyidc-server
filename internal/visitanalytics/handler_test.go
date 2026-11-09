package visitanalytics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ddyidc/pkg/auth"
)

func newHandlerService(t *testing.T) *Service {
	t.Helper()
	return New(Config{PageSamplePercent: 100, APISamplePercent: 100}, openWithoutConnecting(t), auth.NewManager("access", "refresh", 60, 60), []byte("secret"))
}

func requestWithSession(kind auth.SessionKind, claims *auth.Claims, target string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	return request.WithContext(auth.WithSession(request.Context(), kind, claims))
}

func TestAuthorizeReusesValidatedSession(t *testing.T) {
	service := newHandlerService(t)
	admin := requestWithSession(auth.SessionAdmin, &auth.Claims{UserID: 42, Role: "admin"}, "/admin/v1/visit-analytics/overview")
	claims, ok := service.authorize(httptest.NewRecorder(), admin)
	if !ok || claims == nil || claims.UserID != 42 {
		t.Fatalf("validated admin session rejected: ok=%v claims=%+v", ok, claims)
	}
	customer := requestWithSession(auth.SessionUser, &auth.Claims{UserID: 7, Role: "customer"}, "/admin/v1/visit-analytics/overview")
	recorder := httptest.NewRecorder()
	if _, ok := service.authorize(recorder, customer); ok {
		t.Fatal("customer session allowed on the admin analytics API")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	anonymous := httptest.NewRequest(http.MethodGet, "/admin/v1/visit-analytics/overview", nil)
	recorder = httptest.NewRecorder()
	if _, ok := service.authorize(recorder, anonymous); ok {
		t.Fatal("anonymous request allowed")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestAuthorizeQueryRejectsUnknownParameters(t *testing.T) {
	service := newHandlerService(t)
	admin := &auth.Claims{UserID: 42, Role: "admin"}
	cases := map[string]int{
		"/admin/v1/visit-analytics/overview?from=2026-11-01&to=2026-11-09":                                  http.StatusOK,
		"/admin/v1/visit-analytics/overview?from=2026-11-01&to=2026-11-09&identity=customer&client=browser": http.StatusOK,
		"/admin/v1/visit-analytics/overview?from=2026-11-01&to=2026-11-09&unknown=1":                        http.StatusBadRequest,
		"/admin/v1/visit-analytics/overview?from=2026-08-01&to=2026-11-09":                                  http.StatusBadRequest,
		"/admin/v1/visit-analytics/overview?from=2026-11-09&to=2026-11-01":                                  http.StatusBadRequest,
		"/admin/v1/visit-analytics/overview?from=2026-11-01&to=2026-11-09&identity=root":                    http.StatusBadRequest,
		"/admin/v1/visit-analytics/overview?from=2026-11-01&to=2026-11-09&client=crawler":                   http.StatusBadRequest,
	}
	for target, expected := range cases {
		request := requestWithSession(auth.SessionAdmin, admin, target)
		recorder := httptest.NewRecorder()
		_, _, _, ok := service.authorizeQuery(recorder, request, "overview", map[string]bool{"from": true, "to": true, "identity": true, "client": true})
		if ok != (expected == http.StatusOK) {
			t.Fatalf("%s: ok=%v, want status %d", target, ok, expected)
		}
		if !ok && recorder.Code != expected {
			t.Fatalf("%s: status = %d, want %d", target, recorder.Code, expected)
		}
	}
}

func TestTrafficDimensionWhitelist(t *testing.T) {
	for name := range marginalOrdinance {
		if _, ok := marginalOrdinance[name]; !ok {
			t.Fatalf("%s missing", name)
		}
	}
	for _, name := range []string{"", "browser", "status", "country"} {
		if _, ok := marginalOrdinance[name]; ok {
			t.Fatalf("%q must not be an accepted traffic dimension", name)
		}
	}
}

func TestWriteErrorShape(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeError(recorder, http.StatusBadRequest, "参数无效")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control = %q", got)
	}
	body := recorder.Body.String()
	if body != "{\"code\":1,\"message\":\"参数无效\"}\n" {
		t.Fatalf("body = %q", body)
	}
}
