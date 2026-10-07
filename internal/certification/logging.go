package certification

import (
	"github.com/zeromicro/go-zero/core/logx"
	framework "github.com/zeromicro/go-zero/rest/handler"
	"net/http"
	"strings"
	"time"
)

// RequestLogger replaces the native body-copying logger. Certification routes
// never enter it, including authentication, rate-limit and server-error paths.
func RequestLogger(next http.HandlerFunc) http.HandlerFunc {
	other := framework.LogHandler(next)
	return func(w http.ResponseWriter, r *http.Request) {
		// Authentication payloads and MFA responses contain credentials. Never
		// copy their request/response bodies or entry-path query into access logs.
		if strings.HasPrefix(r.URL.Path, "/admin/v1/auth/") || strings.HasPrefix(r.URL.Path, "/admin/v1/security/") || strings.HasPrefix(r.URL.Path, "/api/v1/auth/") || strings.HasPrefix(r.URL.Path, "/api/v1/security/") || strings.HasPrefix(r.URL.Path, "/admin/v1/sales/auth/") {
			start := time.Now()
			defer func() {
				logx.Infof("authentication request method=%s elapsed_ms=%d", r.Method, time.Since(start).Milliseconds())
			}()
			next(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/admin/v1/backup/") || strings.HasPrefix(r.URL.Path, "/api/v1/video/") || strings.HasPrefix(r.URL.Path, "/admin/v1/video/") || strings.HasPrefix(r.URL.Path, "/api/v1/transfers") || strings.HasPrefix(r.URL.Path, "/api/v1/certification/") || strings.HasPrefix(r.URL.Path, "/console/v1/certification/") || strings.HasPrefix(r.URL.Path, "/admin/v1/certifications") {
			start := time.Now()
			defer func() {
				logx.Infof("certification request method=%s elapsed_ms=%d", r.Method, time.Since(start).Milliseconds())
			}()
			next(w, r)
			return
		}
		other.ServeHTTP(w, r)
	}
}
