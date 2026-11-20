package instance

import (
	svc "ddyidc/internal/instance/service"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"net/http"
	"strings"
)

// The mailbox code for destruction is requested through the existing
// production-lock endpoint; no second verification channel exists.

// The customer-facing 实例销毁申请 page: a refundable preview, the request itself
// and the customer's own request history.

func (h *handler) destroyQuote(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	view, err := h.service.DestroyQuote(r.Context(), customer, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, view)
}

func (h *handler) submitDestroy(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	var input struct {
		Reason    string `json:"reason"`
		RequestID string `json:"requestId"`
		Email     string `json:"email"`
		Code      string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "请求参数格式错误")
		return
	}
	application, err := h.service.SubmitDestroy(r.Context(), customer, id, input.Reason, input.RequestID, svc.DestroyEmailInput{Email: input.Email, Code: input.Code})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"id": application.ID, "refundNo": application.RefundNo, "amount": application.RefundAmount, "status": application.Status})
}

func (h *handler) destroyRequests(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	rows, err := h.service.DestroyRequests(r.Context(), customer)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": rows})
}

func (h *handler) destroyOptions(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	rows, err := h.service.DestroyOptions(r.Context(), customer)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": rows})
}

// destroySettings exposes only the switches the console needs to render the
// application dialog; the admin view uses the same shape.
func (h *handler) destroySettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r); !ok {
		return
	}
	settings, err := h.service.DestroySettings(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, settings)
}

func (h *handler) adminDestroySettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.globalAccess(w, r); !ok {
		return
	}
	if r.Method == http.MethodGet {
		settings, err := h.service.DestroySettings(r.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, settings)
		return
	}
	var input svc.DestroySettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "请求参数格式错误")
		return
	}
	// globalAccess already validated the session and CSRF token; the admin id is
	// only needed for the audit row.
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := h.jwt.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, token)
	if err != nil || claims == nil {
		writeError(w, http.StatusUnauthorized, "需要管理员登录")
		return
	}
	settings, err := h.service.SaveDestroySettings(r.Context(), input, claims.UserID, clientip.FromRequest(r))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, settings)
}
