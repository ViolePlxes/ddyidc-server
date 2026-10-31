package notify

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	balance "ddyidc/internal/billing/balance"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
)

type adminHandler struct {
	manager *Manager
	auth    *auth.Manager
	outbox  *balance.BalanceService
}

func RegisterAdminHandlers(server *rest.Server, manager *Manager, authManager *auth.Manager, outbox *balance.BalanceService) {
	h := &adminHandler{manager: manager, auth: authManager, outbox: outbox}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/admin/v1/system/notify/task-summary", Handler: h.digestSummary},
		{Method: http.MethodGet, Path: "/admin/v1/system/notify/tasks", Handler: h.digestTasks},
		{Method: http.MethodGet, Path: "/admin/v1/system/notify/tasks/:id", Handler: h.digestDetail},
		{Method: http.MethodPost, Path: "/admin/v1/system/notify/tasks/:id/resolve", Handler: h.digestResolve},
		{Method: http.MethodPost, Path: "/admin/v1/system/notify/tasks/:id/retry", Handler: h.digestRetry},
		{Method: http.MethodPut, Path: "/admin/v1/system/notify/digest-settings", Handler: h.digestSettings},
		{Method: http.MethodPost, Path: "/admin/v1/system/notify/sms-template/apply", Handler: h.applyDigestSMSTemplate},
		{Method: http.MethodPost, Path: "/admin/v1/system/notify/sms-template/check", Handler: h.checkDigestSMSTemplate},
		{Method: http.MethodPost, Path: "/admin/v1/system/notify/diagnostics", Handler: h.digestDiagnostics},
		{Method: http.MethodGet, Path: "/admin/v1/system/notify/reviews", Handler: h.noticeReviews},
		{Method: http.MethodPost, Path: "/admin/v1/system/notify/reviews/:id", Handler: h.reviewNotice},
		{Method: http.MethodGet, Path: "/admin/v1/system/notify/config", Handler: h.getConfig},
		{Method: http.MethodPut, Path: "/admin/v1/system/notify/config", Handler: h.saveConfig},
		{Method: http.MethodGet, Path: "/admin/v1/system/notify/templates", Handler: h.listTemplates},
		{Method: http.MethodPut, Path: "/admin/v1/system/notify/templates/:id", Handler: h.updateTemplate},
		{Method: http.MethodPost, Path: "/admin/v1/system/notify/logs/:id/receipt", Handler: h.smsReceipt},
		{Method: http.MethodPost, Path: "/admin/v1/system/notify/tasks/:id/receipt", Handler: h.smsTaskReceipt},
		{Method: http.MethodGet, Path: "/admin/v1/system/notify/logs", Handler: h.listLogs},
		{Method: http.MethodPost, Path: "/admin/v1/system/notify/test", Handler: h.testNotify},
	})
}

func (h *adminHandler) noticeReviews(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	rows, err := h.outbox.NoticeReviews(r.Context())
	if err != nil {
		adminError(w, 500, 500, "读取待处置通知失败")
		return
	}
	adminOK(w, rows)
}

func (h *adminHandler) reviewNotice(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, err := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
	if err != nil || id <= 0 {
		adminError(w, 400, 400, "通知编号无效")
		return
	}
	cookie, err := r.Cookie(h.auth.CookieName(auth.SessionAdmin))
	if err != nil {
		adminError(w, 401, 401, "登录已失效")
		return
	}
	claims, err := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, cookie.Value)
	if err != nil || claims == nil {
		adminError(w, 401, 401, "登录已失效")
		return
	}
	var in struct {
		Reason      string `json:"reason"`
		Action      string `json:"action"`
		Status      string `json:"status"`
		Attempts    int    `json:"attempts"`
		Acknowledge bool   `json:"acknowledge"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil {
		adminError(w, 400, 400, "处置参数无效")
		return
	}
	err = h.outbox.ReviewNotice(r.Context(), id, claims.UserID, clientip.FromRequest(r), in.Reason, in.Action, in.Status, in.Attempts, in.Acknowledge)
	if errors.Is(err, balance.ErrNoticeReview) {
		adminError(w, 409, 409, err.Error())
		return
	}
	if err != nil {
		adminError(w, 500, 500, "通知处置失败")
		return
	}
	adminOK(w, map[string]bool{"saved": true})
}

func (h *adminHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie(h.auth.CookieName(auth.SessionAdmin))
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		adminError(w, http.StatusUnauthorized, 401, "登录已失效，请重新登录")
		return false
	}
	claims, err := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, cookie.Value)
	if err != nil {
		adminError(w, http.StatusUnauthorized, 401, "登录已失效，请重新登录")
		return false
	}
	if claims.Role != "admin" {
		adminError(w, http.StatusForbidden, 403, "没有系统设置权限")
		return false
	}
	return true
}

func (h *adminHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	data, err := h.manager.Config(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, 500, "读取通知配置失败")
		return
	}
	adminOK(w, data)
}

func (h *adminHandler) saveConfig(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var input ConfigInput
	if !adminDecode(w, r, &input) {
		return
	}
	if err := h.manager.SaveConfig(r.Context(), input); err != nil {
		adminError(w, http.StatusBadRequest, 400, err.Error())
		return
	}
	data, err := h.manager.Config(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, 500, "配置已保存，但重新读取失败")
		return
	}
	adminOK(w, data)
}

func (h *adminHandler) listTemplates(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	items, err := h.manager.repo.ListTemplates(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, 500, "读取通知模板失败")
		return
	}
	adminOK(w, map[string]any{"items": items})
}

func (h *adminHandler) updateTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(pathvar.Vars(r)["id"]), 10, 64)
	if err != nil || id <= 0 {
		adminError(w, http.StatusBadRequest, 400, "模板 ID 无效")
		return
	}
	var input struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Channel string `json:"channel"`
		Enabled bool   `json:"enabled"`
	}
	if !adminDecode(w, r, &input) {
		return
	}
	input.Title, input.Content, input.Channel = strings.TrimSpace(input.Title), strings.TrimSpace(input.Content), strings.TrimSpace(input.Channel)
	if input.Title == "" || input.Content == "" {
		adminError(w, http.StatusBadRequest, 400, "标题和内容不能为空")
		return
	}
	if input.Channel != ChannelSMS && input.Channel != ChannelEmail && input.Channel != ChannelBoth {
		adminError(w, http.StatusBadRequest, 400, "通知渠道无效")
		return
	}
	if err := h.manager.repo.UpdateTemplate(r.Context(), id, input.Title, input.Content, input.Channel, input.Enabled); errors.Is(err, gorm.ErrRecordNotFound) {
		adminError(w, http.StatusNotFound, 404, "通知模板不存在")
		return
	} else if err != nil {
		adminError(w, http.StatusInternalServerError, 500, "保存通知模板失败")
		return
	}
	adminOK(w, map[string]bool{"saved": true})
}

func (h *adminHandler) listLogs(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	items, total, err := h.manager.repo.ListLogs(r.Context(), page, pageSize, r.URL.Query().Get("scene"), r.URL.Query().Get("channel"), r.URL.Query().Get("status"))
	if err != nil {
		adminError(w, http.StatusInternalServerError, 500, "读取通知记录失败")
		return
	}
	adminOK(w, map[string]any{"items": items, "total": total, "page": max(page, 1), "pageSize": pageSize})
}

func (h *adminHandler) testNotify(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var input struct {
		Scene  string            `json:"scene"`
		Target string            `json:"target"`
		Params map[string]string `json:"params"`
	}
	if !adminDecode(w, r, &input) {
		return
	}
	if !validScene(input.Scene) {
		adminError(w, http.StatusBadRequest, 400, "通知场景无效")
		return
	}
	if input.Params == nil {
		input.Params = map[string]string{}
	}
	if err := h.manager.SendTestNotifyTask(r.Context(), input.Scene, strings.TrimSpace(input.Target), input.Params); err != nil {
		adminError(w, http.StatusBadGateway, 502, "测试通知发送失败："+safeProviderError(err))
		return
	}
	adminOK(w, map[string]bool{"sent": true})
}

func adminDecode(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(target); err != nil {
		adminError(w, http.StatusBadRequest, 400, "请求参数格式错误")
		return false
	}
	return true
}
func adminOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}
func adminError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message})
}
