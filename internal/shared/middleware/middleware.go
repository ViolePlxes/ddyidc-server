package middleware

import (
	"bufio"
	"context"
	"ddyidc/pkg/auth"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CORS 跨域中间件
func CORS() func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "http://localhost:3100" || origin == "http://127.0.0.1:3100" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization,X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "600")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next(w, r)
		}
	}
}

// CookieSession converts a validated httpOnly cookie into the legacy internal
// Authorization header. Existing business handlers keep their authorization
// code while the browser can no longer read or submit the JWT directly.
func CookieSession(manager *auth.Manager) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sales/auth/login" {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusGone)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": "请使用销售端独立登录入口"})
				return
			}
			r.Header.Del("Authorization")
			if r.URL.Path == "/api/v1/auth/refresh" && r.Method == http.MethodPost {
				// Refresh authenticates an explicit rotating credential and enforces
				// its own same-origin header check even after access/CSRF expiry.
				next(w, r)
				return
			}
			var sessionClaims *auth.Claims
			kind, protected := sessionKindForRequest(r.URL.Path)
			if kind != "" {
				cookie, err := r.Cookie(manager.CookieName(kind))
				if err == nil && cookie.Value != "" {
					if claims, parseErr := manager.ParseAccessTokenFor(r.Context(), kind, cookie.Value); parseErr == nil {
						sessionClaims = claims
						r.Header.Set("Authorization", "Bearer "+cookie.Value)
					} else if kind != auth.SessionUser {
						manager.ClearSessionCookie(w, kind)
					}
				}
			}
			if protected && r.Header.Get("Authorization") == "" {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": "登录已失效，请重新登录"})
				return
			}
			if sessionClaims != nil && !manager.ValidateCSRF(r, kind, sessionClaims) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": "请求安全校验失败，请刷新页面后重试"})
				return
			}
			if sessionClaims != nil && sessionClaims.ActorID != 0 {
				finish, err := manager.BeginImpersonationAudit(r.Context(), sessionClaims, w.Header().Get("X-Request-ID"), r.Method, r.URL.Path)
				if err != nil {
					http.Error(w, "代登录审计暂时不可用，请稍后重试", http.StatusServiceUnavailable)
					return
				}
				writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
				completed := false
				defer func() {
					status := writer.status
					if !completed {
						status = 0
					}
					if err := finish(status); err != nil {
						log.Printf("impersonation_audit_completion_failed actor=%d subject=%d request_id=%s", sessionClaims.ActorID, sessionClaims.UserID, w.Header().Get("X-Request-ID"))
					}
				}()
				next(writer, r)
				completed = true
				return
			}
			next(w, r)
		}
	}
}

func sessionKindForRequest(path string) (auth.SessionKind, bool) {
	switch {
	case path == "/admin/v1/auth/login" || path == "/admin/v1/auth/login/mfa":
		return auth.SessionAdmin, false
	case path == "/admin/v1/sales/auth/login":
		return auth.SessionSales, false
	case strings.HasPrefix(path, "/admin/v1/sales/"):
		return auth.SessionSales, true
	case strings.HasPrefix(path, "/admin/v1/"):
		return auth.SessionAdmin, true
	case strings.HasPrefix(path, "/api/v1/"):
		return auth.SessionUser, false
	default:
		return "", false
	}
}

// RedactSensitiveHeaders removes credential values after request processing so
// framework-level error logging cannot dump secrets from the shared request.
func RedactSensitiveHeaders() func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			defer redactSensitiveHeaders(r.Header)
			next(w, r)
		}
	}
}

func redactSensitiveHeaders(headers http.Header) {
	for name := range headers {
		if isSensitiveHeader(name) {
			headers[name] = []string{"[REDACTED]"}
		}
	}
}

func isSensitiveHeader(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	switch normalized {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-token", "x-admin-entry":
		return true
	}
	return strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "api-key") ||
		strings.Contains(normalized, "apikey") ||
		strings.Contains(normalized, "credential")
}

// RequestID 请求ID中间件
func RequestID() func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Server-generated IDs cannot contain attacker-controlled secrets or newlines.
			requestID := uuid.New().String()
			ctx := context.WithValue(r.Context(), "request_id", requestID)
			w.Header().Set("X-Request-ID", requestID)
			next(w, r.WithContext(ctx))
		}
	}
}

// Logger 日志中间件
func Logger() func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			completed := false
			defer func() {
				status := writer.status
				if !completed {
					status = 0
				}
				entry, _ := json.Marshal(map[string]any{"event": "http_request", "method": r.Method, "path": r.URL.Path, "status": status, "completed": completed, "duration_ms": time.Since(start).Milliseconds(), "request_id": w.Header().Get("X-Request-ID")})
				log.Print(string(entry))
			}()
			next(writer, r)
			completed = true
		}
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.status = code
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, r, e := http.NewResponseController(w.ResponseWriter).Hijack()
	if e == nil {
		w.status = http.StatusSwitchingProtocols
		w.wrote = true
	}
	return c, r, e
}
