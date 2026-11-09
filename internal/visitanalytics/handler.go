package visitanalytics

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"github.com/zeromicro/go-zero/rest"
)

// Register mounts the administrator query endpoints and the public page-view
// beacon. It also starts the batch writer and the retention task.
func (s *Service) Register(server *rest.Server, jwt *auth.Manager) {
	if s == nil {
		return
	}
	if jwt != nil {
		s.jwt = jwt
	}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/admin/v1/visit-analytics/overview", Handler: s.adminOverview},
		{Method: http.MethodGet, Path: "/admin/v1/visit-analytics/traffic", Handler: s.adminTraffic},
		{Method: http.MethodGet, Path: "/admin/v1/visit-analytics/flow", Handler: s.adminFlow},
		{Method: http.MethodGet, Path: "/admin/v1/visit-analytics/geography", Handler: s.adminGeography},
		{Method: http.MethodGet, Path: "/admin/v1/visit-analytics/clients", Handler: s.adminClients},
		{Method: http.MethodGet, Path: "/admin/v1/visit-analytics/status", Handler: s.adminStatus},
		{Method: http.MethodPost, Path: beaconPath, Handler: s.handleBeacon},
	})
	if s.Enabled() {
		s.StartWriter(context.Background())
	}
}

func (s *Service) adminOverview(w http.ResponseWriter, r *http.Request) {
	window, filter, claims, ok := s.authorizeQuery(w, r, "overview", map[string]bool{"from": true, "to": true, "identity": true, "client": true})
	if !ok {
		return
	}
	overview, err := s.Overview(r.Context(), window, filter)
	if err != nil {
		s.auditFailure(r, claims, "overview", err)
		writeError(w, http.StatusBadGateway, "访问统计读取失败，请稍后重试")
		return
	}
	writeJSON(w, overview)
}

func (s *Service) adminTraffic(w http.ResponseWriter, r *http.Request) {
	window, _, claims, ok := s.authorizeQuery(w, r, "traffic", map[string]bool{"from": true, "to": true, "dimension": true})
	if !ok {
		return
	}
	dimension := strings.TrimSpace(r.URL.Query().Get("dimension"))
	if dimension == "" {
		dimension = "source"
	}
	if _, allowed := marginalOrdinance[dimension]; !allowed {
		writeError(w, http.StatusBadRequest, "不支持的访问维度")
		return
	}
	rows, err := s.Traffic(r.Context(), window, dimension)
	if err != nil {
		s.auditFailure(r, claims, "traffic", err)
		writeError(w, http.StatusBadGateway, "访问统计读取失败，请稍后重试")
		return
	}
	writeJSON(w, map[string]any{"dimension": dimension, "rows": rows, "from": window.FromDate, "to": window.ToDate})
}

func (s *Service) adminFlow(w http.ResponseWriter, r *http.Request) {
	window, _, claims, ok := s.authorizeQuery(w, r, "flow", map[string]bool{"from": true, "to": true})
	if !ok {
		return
	}
	rows, err := s.Flow(r.Context(), window)
	if err != nil {
		s.auditFailure(r, claims, "flow", err)
		writeError(w, http.StatusBadGateway, "访问统计读取失败，请稍后重试")
		return
	}
	writeJSON(w, map[string]any{"rows": rows, "from": window.FromDate, "to": window.ToDate})
}

func (s *Service) adminGeography(w http.ResponseWriter, r *http.Request) {
	window, _, claims, ok := s.authorizeQuery(w, r, "geography", map[string]bool{"from": true, "to": true})
	if !ok {
		return
	}
	rows, err := s.Geography(r.Context(), window)
	if err != nil {
		s.auditFailure(r, claims, "geography", err)
		writeError(w, http.StatusBadGateway, "访问统计读取失败，请稍后重试")
		return
	}
	writeJSON(w, map[string]any{"regions": rows, "from": window.FromDate, "to": window.ToDate, "geo": s.geo.GeoStatus()})
}

func (s *Service) adminClients(w http.ResponseWriter, r *http.Request) {
	window, _, claims, ok := s.authorizeQuery(w, r, "clients", map[string]bool{"from": true, "to": true})
	if !ok {
		return
	}
	rows, err := s.Clients(r.Context(), window)
	if err != nil {
		s.auditFailure(r, claims, "clients", err)
		writeError(w, http.StatusBadGateway, "访问统计读取失败，请稍后重试")
		return
	}
	writeJSON(w, map[string]any{"rows": rows, "from": window.FromDate, "to": window.ToDate})
}

func (s *Service) adminStatus(w http.ResponseWriter, r *http.Request) {
	window, _, claims, ok := s.authorizeQuery(w, r, "status", map[string]bool{"from": true, "to": true})
	if !ok {
		return
	}
	rows, err := s.Status(r.Context(), window)
	if err != nil {
		s.auditFailure(r, claims, "status", err)
		writeError(w, http.StatusBadGateway, "访问统计读取失败，请稍后重试")
		return
	}
	writeJSON(w, map[string]any{"rows": rows, "from": window.FromDate, "to": window.ToDate})
}

// authorizeQuery enforces the administrator session, the parameter whitelist,
// the date range limits and the audit requirement.
func (s *Service) authorizeQuery(w http.ResponseWriter, r *http.Request, action string, allowed map[string]bool) (Range, Filter, *auth.Claims, bool) {
	claims, ok := s.authorize(w, r)
	if !ok {
		return Range{}, Filter{}, nil, false
	}
	for name := range r.URL.Query() {
		if !allowed[name] {
			writeError(w, http.StatusBadRequest, "不支持的查询参数")
			return Range{}, Filter{}, nil, false
		}
	}
	window, err := ParseRange(r.URL.Query(), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "时间范围无效，最多查询 90 天")
		return Range{}, Filter{}, nil, false
	}
	filter, err := ParseFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "筛选条件无效")
		return Range{}, Filter{}, nil, false
	}
	s.audit(r, claims, action)
	return window, filter, claims, true
}

func (s *Service) authorize(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	if s.jwt == nil {
		writeError(w, http.StatusServiceUnavailable, "访问统计暂不可用")
		return nil, false
	}
	// The cookie middleware already validated this session for /admin/v1/.
	if kind, claims, ok := auth.SessionFrom(r.Context()); ok {
		if kind != auth.SessionAdmin || claims.Role != "admin" {
			writeError(w, http.StatusForbidden, "没有管理端访问权限")
			return nil, false
		}
		return claims, true
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return nil, false
	}
	claims, err := s.jwt.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims == nil || claims.Role != "admin" {
		writeError(w, http.StatusForbidden, "没有管理端访问权限")
		return nil, false
	}
	return claims, true
}

// audit records every administrator query. A failure is logged and never fails
// the read, because the audit table is a separate write path.
func (s *Service) audit(r *http.Request, claims *auth.Claims, action string) {
	if claims == nil || s.database == nil {
		return
	}
	target := sanitizedQuery(r)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.database.ExecContext(ctx,
		"INSERT INTO system.visit_analytics_audits (admin_id,action,target,request_id,operator_ip) VALUES (?,?,?,?,?)",
		claims.UserID, action, target, truncate(r.Header.Get("X-Request-ID"), 64), truncate(operatorIP(r), 64)); err != nil {
		log.Printf("visit_analytics_audit_failed action=%s admin=%d error=%v", action, claims.UserID, err)
	}
}

func (s *Service) auditFailure(r *http.Request, claims *auth.Claims, action string, err error) {
	log.Printf("visit_analytics_query_failed action=%s error=%v request_id=%s", action, err, truncate(r.Header.Get("X-Request-ID"), 64))
	if claims != nil {
		s.audit(r, claims, action+"_failed")
	}
}

// sanitizedQuery keeps the whitelisted filters in the audit line.
func sanitizedQuery(r *http.Request) string {
	values := r.URL.Query()
	parts := make([]string, 0, 4)
	for _, name := range []string{"from", "to", "dimension", "identity", "client"} {
		if value := values.Get(name); value != "" {
			parts = append(parts, name+"="+sanitizeText(value, 40))
		}
	}
	return truncate(strings.Join(parts, "&"), 512)
}

func operatorIP(r *http.Request) string { return clientip.FromRequest(r) }

func writeJSON(w http.ResponseWriter, data any) {
	writeJSONStatus(w, http.StatusOK, data)
}

func writeJSONStatus(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}
