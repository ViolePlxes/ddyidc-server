// Shared HTTP plumbing for the marketing handlers: JWT extraction, role
// guards and the unified {code,data,message} response helpers.
package marketing

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ddyidc/pkg/auth"
)

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}

func writeFail(w http.ResponseWriter, err error) {
	writeError(w, http.StatusOK, err.Error())
}

// claimsFromRequest parses the Bearer token; 401 is returned for invalid
// tokens so the frontend redirects to login.
func claimsFromRequest(r *http.Request, jwt *auth.Manager) (*auth.Claims, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, errUnauthorized
	}
	claims, err := jwt.ParseAccessToken(strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims == nil {
		return nil, errUnauthorized
	}
	return claims, nil
}

var errUnauthorized = errors.New("登录已失效，请重新登录")

// requireRoles 401 for missing/invalid tokens, 403 for wrong roles.
func requireRoles(w http.ResponseWriter, r *http.Request, jwt *auth.Manager, roles ...string) (*auth.Claims, bool) {
	claims, err := claimsFromRequest(r, jwt)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return nil, false
	}
	for _, role := range roles {
		if claims.Role == role {
			return claims, true
		}
	}
	writeError(w, http.StatusForbidden, "没有访问权限")
	return nil, false
}

func queryInt(r *http.Request, key string, fallback int) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func queryInt64(r *http.Request, key string) int64 {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func pageParams(r *http.Request) (int, int) {
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "pageSize", 10)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}
	return page, pageSize
}

func decodeBody(w http.ResponseWriter, r *http.Request, to any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(to); err != nil {
		writeError(w, http.StatusBadRequest, "请求参数格式错误: "+err.Error())
		return false
	}
	return true
}
