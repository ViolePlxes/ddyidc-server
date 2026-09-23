package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSignedSessionBoundCSRF(t *testing.T) {
	m := NewManager("test-secret", "test-refresh", 3600, 3600)
	token, err := m.GenerateAccessToken(1, "test", "customer")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := m.parseAccessToken(token)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, header, cookie, site string
		want                               bool
	}{
		{"valid", "POST", m.csrfValue(claims), m.csrfValue(claims), "same-origin", true},
		{"missing", "POST", "", "", "", false},
		{"missing-header", "DELETE", "", m.csrfValue(claims), "", false},
		{"forged", "PUT", "forged", "forged", "", false},
		{"cross-site", "POST", m.csrfValue(claims), m.csrfValue(claims), "cross-site", false},
		{"read", "GET", "", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://localhost/api/v1/example", nil)
			r.Header.Set("X-CSRF-Token", tc.header)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.AddCookie(&http.Cookie{Name: CSRFCookieName(SessionUser), Value: tc.cookie})
			if got := m.ValidateCSRF(r, SessionUser, claims); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	other, err := m.GenerateAccessToken(1, "test", "customer")
	if err != nil {
		t.Fatal(err)
	}
	otherClaims, err := m.parseAccessToken(other)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://localhost/api/v1/example", nil)
	r.Header.Set("X-CSRF-Token", m.csrfValue(claims))
	r.AddCookie(&http.Cookie{Name: CSRFCookieName(SessionUser), Value: m.csrfValue(claims)})
	if m.ValidateCSRF(r, SessionUser, otherClaims) {
		t.Fatal("old-session csrf accepted")
	}
	w := httptest.NewRecorder()
	m.SetSessionCookie(w, SessionUser, token)
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatal("missing session/CSRF cookie")
	}
	if !cookies[0].HttpOnly || cookies[1].HttpOnly || cookies[1].Value == token {
		t.Fatal("credential exposure or unreadable csrf cookie")
	}
}
