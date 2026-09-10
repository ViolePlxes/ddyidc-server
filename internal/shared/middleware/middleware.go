package middleware

import (
	"context"
	"ddyidc/pkg/auth"
	"encoding/json"
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
			kind, protected := sessionKindForRequest(r.URL.Path)
			if kind != "" {
				cookie, err := r.Cookie(manager.CookieName(kind))
				if err == nil && cookie.Value != "" {
					if _, parseErr := manager.ParseAccessTokenFor(r.Context(), kind, cookie.Value); parseErr == nil {
						r.Header.Set("Authorization", "Bearer "+cookie.Value)
					} else {
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
			next(w, r)
		}
	}
}

func sessionKindForRequest(path string) (auth.SessionKind, bool) {
	switch {
	case path == "/admin/v1/auth/login":
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

// RequestID 请求ID中间件
func RequestID() func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			requestID := r.Header.Get("X-Request-ID")
			if requestID == "" {
				requestID = uuid.New().String()
			}
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
			next(w, r)
			// TODO: 记录请求日志（方法、路径、状态码、耗时）
			_ = start
		}
	}
}
