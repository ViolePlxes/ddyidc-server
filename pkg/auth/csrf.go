package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

func CSRFCookieName(kind SessionKind) string { return "ddyidc_csrf_" + string(kind) }

func (m *Manager) csrfValue(claims *Claims) string {
	mac := hmac.New(sha256.New, m.accessSecret)
	mac.Write([]byte("ddyidc-csrf-v1:" + claims.Role + ":" + claims.ID))
	return hex.EncodeToString(mac.Sum(nil))
}

// Signed double-submit token is bound to the authenticated session. The
// readable cookie contains no credential and cannot authenticate a request.
func (m *Manager) ValidateCSRF(r *http.Request, kind SessionKind, claims *Claims) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	if claims == nil {
		return false
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	cookie, err := r.Cookie(CSRFCookieName(kind))
	if err != nil {
		return false
	}
	expected := m.csrfValue(claims)
	return hmac.Equal([]byte(cookie.Value), []byte(expected)) && hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(expected))
}
