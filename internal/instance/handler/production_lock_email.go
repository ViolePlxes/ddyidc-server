package instance

import (
	svc "ddyidc/internal/instance/service"
	"encoding/json"
	"net/http"
)

func (h *handler) productionLockEmail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	var out *svc.ProductionLockEmailStatus
	var e error
	if r.URL.Query().Get("purpose") == "destroy" {
		out, e = h.service.DestroyEmailStatus(r.Context(), customer, id)
	} else {
		out, e = h.service.ProductionLockEmailStatus(r.Context(), customer, id)
	}
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, out)
}
func (h *handler) sendProductionLockEmail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	var in struct {
		Enabled *bool  `json:"enabled"`
		Email   string `json:"email"`
		Purpose string `json:"purpose"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || (in.Purpose != "destroy" && in.Enabled == nil) {
		writeError(w, 400, "请求参数无效")
		return
	}
	if h.lockEmailSender == nil {
		writeError(w, 503, "邮箱验证服务暂时不可用")
		return
	}
	var email, scope string
	var e error
	if in.Purpose == "destroy" {
		email, scope, e = h.service.DestroyEmailTarget(r.Context(), customer, id, in.Email)
	} else {
		email, scope, e = h.service.ProductionLockEmailTarget(r.Context(), customer, id, *in.Enabled, in.Email)
	}
	if e != nil {
		writeServiceError(w, e)
		return
	}
	h.lockEmailSender.SendProductionLockCode(w, r, email, scope)
}
