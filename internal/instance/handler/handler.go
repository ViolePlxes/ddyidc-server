package instance

import (
	"ddyidc/internal/shared/renewalquote"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	svc "ddyidc/internal/instance/service"
	"ddyidc/internal/remoteconsole"
	"ddyidc/pkg/auth"
	"github.com/gorilla/websocket"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

type handler struct {
	service         *svc.Service
	jwt             *auth.Manager
	lockEmailSender interface {
		SendProductionLockCode(http.ResponseWriter, *http.Request, string, string)
	}
}

func RegisterHandlers(server *rest.Server, service *svc.Service, jwt *auth.Manager, emailSender ...interface {
	SendProductionLockCode(http.ResponseWriter, *http.Request, string, string)
}) {
	h := &handler{service: service, jwt: jwt}
	if len(emailSender) > 0 {
		h.lockEmailSender = emailSender[0]
	}
	server.AddRoutes(h.adminListRoutes())
	server.AddRoutes(h.managementRoutes())
	server.AddRoutes(h.securityRoutes())
	server.AddRoutes(h.upgradeRoutes())
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/instance/:instanceId/status", Handler: h.status},
		{Method: http.MethodGet, Path: "/admin/v1/customers/:customerId/instances/:instanceId/status", Handler: h.status},
		{Method: http.MethodGet, Path: "/admin/v1/customers/:customerId/instances/import-options", Handler: h.importOptions},
		{Method: http.MethodPost, Path: "/admin/v1/customers/:customerId/instances/import", Handler: h.importInstance},
		{Method: http.MethodGet, Path: "/admin/v1/customers/:customerId/instances/:instanceId", Handler: h.detail},
		{Method: http.MethodGet, Path: "/admin/v1/customers/:customerId/instances/:instanceId/images", Handler: h.images},
		{Method: http.MethodPost, Path: "/admin/v1/customers/:customerId/instances/:instanceId/action", Handler: h.action},
		{Method: http.MethodGet, Path: "/admin/v1/customers/:customerId/instances/:instanceId/monitor", Handler: h.monitor},
		{Method: http.MethodGet, Path: "/admin/v1/customers/:customerId/instances/:instanceId/vnc/ws", Handler: h.vncWebSocket},
		{Method: http.MethodGet, Path: "/api/v1/instances", Handler: h.legacyList},
		{Method: http.MethodGet, Path: "/api/v1/user/instances", Handler: h.list},
		{Method: http.MethodGet, Path: "/api/v1/instance/:instanceId", Handler: h.detail},
		{Method: http.MethodGet, Path: "/api/v1/instance/:instanceId/images", Handler: h.images},
		{Method: http.MethodPost, Path: "/api/v1/instance/:instanceId/action", Handler: h.action},
		{Method: http.MethodGet, Path: "/api/v1/instance/:instanceId/snapshots", Handler: h.snapshots},
		{Method: http.MethodPost, Path: "/api/v1/instance/:instanceId/snapshots", Handler: h.createSnapshot},
		{Method: http.MethodPost, Path: "/api/v1/instance/:instanceId/snapshots/:snapshotId/restore", Handler: h.restoreSnapshot},
		{Method: http.MethodDelete, Path: "/api/v1/instance/:instanceId/snapshots/:snapshotId", Handler: h.deleteSnapshot},
		{Method: http.MethodGet, Path: "/api/v1/instance/:instanceId/nat", Handler: h.natRules},
		{Method: http.MethodPost, Path: "/api/v1/instance/:instanceId/nat", Handler: h.createNATRule},
		{Method: http.MethodDelete, Path: "/api/v1/instance/:instanceId/nat/:ruleId", Handler: h.deleteNATRule},
		{Method: http.MethodGet, Path: "/api/v1/instance/:instanceId/monitor", Handler: h.monitor},
		{Method: http.MethodGet, Path: "/api/v1/instance/:instanceId/vnc/ws", Handler: h.vncWebSocket},
	})
}

func (h *handler) images(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	result, err := h.service.Images(r.Context(), customerID, instanceID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}

var vncUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	Subprotocols:     []string{"binary"},
	CheckOrigin: func(r *http.Request) bool {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			return true
		}
		parsed, err := url.Parse(origin)
		return err == nil && strings.EqualFold(parsed.Host, r.Host)
	},
}

func (h *handler) vncWebSocket(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	session, err := h.service.ClaimRemoteSession(r.Context(), customerID, instanceID, r.URL.Query().Get("token"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	connection, err := vncUpgrader.Upgrade(w, r, nil)
	if err != nil {
		h.service.DiscardRemoteSession(session)
		return
	}
	defer connection.Close()
	_ = h.service.ProxyRemoteSession(r.Context(), session, remoteconsole.NewWebSocketConn(connection))
}

func (h *handler) legacyList(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	result, err := h.service.LegacyList(r.Context(), customerID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	page, pageSize := pageParams(r)
	input := svc.ListInput{Page: page, PageSize: pageSize, Search: strings.TrimSpace(r.URL.Query().Get("search"))}
	if raw := strings.TrimSpace(r.URL.Query().Get("status")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 8)
		if err != nil {
			writeError(w, http.StatusBadRequest, "状态筛选参数错误")
			return
		}
		status := uint8(value)
		input.Status = &status
	}
	result, err := h.service.List(r.Context(), customerID, input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}

func (h *handler) detail(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	result, err := h.service.Detail(r.Context(), customerID, instanceID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/admin/v1/") {
		// 上游订单号、发票号与首付金额属于平台采购数据，客户接口不得输出。
		result.HideProcurement()
	}
	writeJSON(w, result)
}

func (h *handler) action(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	var input svc.ActionInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "请求参数格式错误")
		return
	}
	result, err := h.service.Action(r.Context(), customerID, instanceID, input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if result.Remote != nil && strings.HasPrefix(r.URL.Path, "/admin/v1/") {
		remoteURL, parseErr := url.Parse(result.Remote.URL)
		if parseErr != nil {
			writeError(w, 500, "远程会话地址无效")
			return
		}
		remoteURL.Path = "/admin/customers/" + strconv.FormatInt(customerID, 10) + "/instances/" + strconv.FormatInt(instanceID, 10) + "/vnc"
		result.Remote.URL = remoteURL.String()
	}
	writeJSON(w, result)
}

func (h *handler) monitor(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	end := time.Now()
	start := end.Add(-24 * time.Hour)
	if raw := strings.TrimSpace(r.URL.Query().Get("start")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "监控开始时间格式错误")
			return
		}
		start = value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("end")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "监控结束时间格式错误")
			return
		}
		end = value
	}
	if !start.Before(end) {
		writeError(w, http.StatusBadRequest, "监控时间范围错误")
		return
	}
	result, err := h.service.Monitor(r.Context(), customerID, instanceID, start, end)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}

func (h *handler) claims(w http.ResponseWriter, r *http.Request) (int64, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return 0, false
	}
	claims, err := h.jwt.ParseAccessToken(strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims == nil {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return 0, false
	}
	if strings.HasPrefix(r.URL.Path, "/admin/v1/") {
		if claims.Role != "admin" {
			writeError(w, http.StatusForbidden, "需要管理员权限")
			return 0, false
		}
		customerID, err := strconv.ParseInt(pathvar.Vars(r)["customerId"], 10, 64)
		if err != nil || customerID <= 0 {
			writeError(w, http.StatusBadRequest, "客户 ID 无效")
			return 0, false
		}
		*r = *r.WithContext(svc.WithAdminActor(r.Context(), claims.UserID))
		return customerID, true
	}
	if claims.Role != "customer" {
		writeError(w, http.StatusForbidden, "没有访问权限")
		return 0, false
	}
	return claims.UserID, true
}

func pathID(w http.ResponseWriter, r *http.Request) int64 {
	raw := pathvar.Vars(r)["instanceId"]
	id, err := strconv.ParseInt(strings.Trim(raw, "/"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "实例 ID 格式错误")
		return 0
	}
	return id
}

func pageParams(r *http.Request) (int, int) {
	page, pageSize := 1, 20
	if value, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && value > 0 {
		page = value
	}
	if value, err := strconv.Atoi(r.URL.Query().Get("pageSize")); err == nil && value > 0 && value <= 100 {
		pageSize = value
	}
	return page, pageSize
}

func writeServiceError(w http.ResponseWriter, err error) {
	var gate *renewalquote.Error
	if errors.As(err, &gate) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 40901, "message": gate.Error(), "data": gate})
		return
	}

	switch {
	case errors.Is(err, svc.ErrForbidden):
		writeError(w, http.StatusForbidden, "没有权限访问该实例")
	case errors.Is(err, svc.ErrNotFound):
		writeError(w, http.StatusNotFound, "实例不存在")
	case errors.Is(err, svc.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, remoteconsole.ErrSessionInvalid):
		writeError(w, http.StatusUnauthorized, "远程会话无效或已过期")
	case errors.Is(err, remoteconsole.ErrSessionBusy):
		writeError(w, http.StatusConflict, "该远程会话正在使用中")
	case errors.Is(err, remoteconsole.ErrEngineUnavailable), errors.Is(err, remoteconsole.ErrUpstreamUnavailable):
		writeError(w, http.StatusUnprocessableEntity, "远程控制暂时不可用，请稍后重试")
	default:
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func writeError(w http.ResponseWriter, status int, message string) {
	if strings.Contains(message, "BALANCE_LEDGER_MISMATCH:") {
		status = 409
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	result, err := h.service.Status(r.Context(), customer, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}
