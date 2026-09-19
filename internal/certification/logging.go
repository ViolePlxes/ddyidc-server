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
		if strings.HasPrefix(r.URL.Path, "/api/v1/video/") || strings.HasPrefix(r.URL.Path, "/admin/v1/video/") || strings.HasPrefix(r.URL.Path, "/api/v1/transfers") || strings.HasPrefix(r.URL.Path, "/api/v1/certification/") || strings.HasPrefix(r.URL.Path, "/console/v1/certification/") || strings.HasPrefix(r.URL.Path, "/admin/v1/certifications") {
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
