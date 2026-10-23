package update

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

type adminHandler struct {
	manager *Manager
	auth    *auth.Manager
}

func RegisterAdminHandlers(server *rest.Server, manager *Manager, authManager *auth.Manager) {
	handler := &adminHandler{manager: manager, auth: authManager}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/admin/v1/update/status", Handler: handler.status},
		{Method: http.MethodGet, Path: "/admin/v1/update/reclamation", Handler: handler.reclamation},
		{Method: http.MethodGet, Path: "/admin/v1/update/reclamation/history", Handler: handler.reclamationHistory},
		{Method: http.MethodPost, Path: "/admin/v1/update/reclamation/retry", Handler: handler.retryReclamation},
		{Method: http.MethodPost, Path: "/admin/v1/update/reclamation/preview", Handler: handler.retryReclamation},
		{Method: http.MethodPost, Path: "/admin/v1/update/check", Handler: handler.check},
		{Method: http.MethodPost, Path: "/admin/v1/update/backend", Handler: handler.backend},
		{Method: http.MethodPost, Path: "/admin/v1/update/frontend", Handler: handler.frontend},
		{Method: http.MethodGet, Path: "/admin/v1/update/tasks", Handler: handler.tasks},
		{Method: http.MethodGet, Path: "/admin/v1/update/tasks/:id", Handler: handler.task},
	})
}

func (h *adminHandler) authorize(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	cookie, err := r.Cookie(h.auth.CookieName(auth.SessionAdmin))
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		updateError(w, http.StatusUnauthorized, 401, "登录已失效，请重新登录")
		return nil, false
	}
	claims, err := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, cookie.Value)
	if err != nil {
		updateError(w, http.StatusUnauthorized, 401, "登录已失效，请重新登录")
		return nil, false
	}
	if claims.Role != "admin" {
		updateError(w, http.StatusForbidden, 403, "没有系统更新权限")
		return nil, false
	}
	return claims, true
}

func (h *adminHandler) status(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	data, err := h.manager.Status(r.Context(), false)
	if err != nil {
		updateError(w, http.StatusInternalServerError, 500, "读取版本信息失败："+sanitizeOutput(err.Error()))
		return
	}
	updateOK(w, data)
}

func (h *adminHandler) check(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	data, err := h.manager.Status(r.Context(), true)
	if err != nil {
		updateError(w, http.StatusBadGateway, 502, "检查远端更新失败："+sanitizeOutput(err.Error()))
		return
	}
	updateOK(w, data)
}

func (h *adminHandler) backend(w http.ResponseWriter, r *http.Request) {
	h.trigger(w, r, TargetBackend)
}
func (h *adminHandler) frontend(w http.ResponseWriter, r *http.Request) {
	h.trigger(w, r, TargetFrontend)
}

func (h *adminHandler) trigger(w http.ResponseWriter, r *http.Request, target string) {
	claims, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, err := h.manager.Trigger(r.Context(), target, claims.UserID)
	if errors.Is(err, ErrUpdateRunning) {
		updateError(w, http.StatusConflict, 409, err.Error())
		return
	}
	if err != nil {
		updateError(w, http.StatusBadRequest, 400, sanitizeOutput(err.Error()))
		return
	}
	updateOK(w, map[string]string{"taskId": id})
}

func (h *adminHandler) task(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(pathvar.Vars(r)["id"]), 10, 64)
	if err != nil || id <= 0 {
		updateError(w, http.StatusBadRequest, 400, "任务 ID 无效")
		return
	}
	item, err := h.manager.Task(r.Context(), id)
	if err != nil {
		updateError(w, http.StatusInternalServerError, 500, "读取更新任务失败")
		return
	}
	if item == nil {
		updateError(w, http.StatusNotFound, 404, "更新任务不存在")
		return
	}
	updateOK(w, item)
}

func (h *adminHandler) tasks(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	items, total, err := h.manager.Tasks(r.Context(), page, pageSize)
	if err != nil {
		updateError(w, http.StatusInternalServerError, 500, "读取更新历史失败")
		return
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	updateOK(w, map[string]any{"items": items, "total": total, "page": page, "pageSize": pageSize})
}

func updateOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func updateError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message})
}
