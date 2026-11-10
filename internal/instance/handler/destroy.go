package instance

import (
	"encoding/json"
	"net/http"
)

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
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "请求参数格式错误")
		return
	}
	application, err := h.service.SubmitDestroy(r.Context(), customer, id, input.Reason, input.RequestID)
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