package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

var errInvalidExecutionID = errors.New("执行记录 ID 无效")

// financeExecutionID extracts the numeric id from
// /admin/v1/finance/executions/:id/<action>.
func financeExecutionID(r *http.Request, action string) (int64, bool) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/v1/finance/executions/")
	path = strings.TrimSuffix(path, "/"+action)
	value, err := strconv.ParseInt(strings.Trim(path, "/"), 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func (h *Handler) listFinanceExecutions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	page, _ := strconv.Atoi(query.Get("page"))
	size, _ := strconv.Atoi(query.Get("size"))
	rows, total, err := h.Store.ListFinanceExecutions(query.Get("module"), query.Get("status"), page, size)
	if err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, map[string]any{"items": rows, "total": total, "page": max(1, page), "size": size}, nil)
}

func (h *Handler) completeFinanceExecution(w http.ResponseWriter, r *http.Request) {
	id, ok := financeExecutionID(r, "complete")
	if !ok {
		respond(w, nil, errInvalidExecutionID)
		return
	}
	var body struct {
		UpstreamRef string `json:"upstreamRef"`
		Note        string `json:"note"`
	}
	if err := decode(r, &body); err != nil && err.Error() != "EOF" {
		respond(w, nil, err)
		return
	}
	if err := h.Store.CompleteFinanceExecution(id, strings.TrimSpace(body.UpstreamRef), strings.TrimSpace(body.Note)); err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, map[string]bool{"completed": true}, nil)
}

func (h *Handler) reconcileFinanceExecution(w http.ResponseWriter, r *http.Request) {
	id, ok := financeExecutionID(r, "reconcile")
	if !ok {
		respond(w, nil, errInvalidExecutionID)
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if err := decode(r, &body); err != nil && err.Error() != "EOF" {
		respond(w, nil, err)
		return
	}
	if err := h.Store.ReconcileFinanceExecution(id, strings.TrimSpace(body.Note)); err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, map[string]bool{"reconciling": true}, nil)
}
