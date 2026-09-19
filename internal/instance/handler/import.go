package instance

import (
	svc "ddyidc/internal/instance/service"
	"encoding/json"
	"net/http"
	"strconv"
)

func (h *handler) importOptions(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r); !ok {
		return
	}
	rows, e := h.service.ImportProducts(r.Context())
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, map[string]any{"items": rows})
}
func (h *handler) importInstance(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	var in svc.ImportInput
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); e != nil {
		writeError(w, 400, "请求参数错误")
		return
	}
	product, e := strconv.ParseInt(in.ProductID, 10, 64)
	host, hostErr := strconv.ParseInt(in.UpstreamInstanceID, 10, 64)
	if e != nil || hostErr != nil || product <= 0 || host <= 0 {
		writeError(w, 400, "产品 ID 和上游实例 ID 必须为正整数")
		return
	}
	result, e := h.service.Import(r.Context(), customerID, in)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, map[string]any{"id": strconv.FormatInt(result.ID, 10), "upstreamInstanceId": result.UpstreamInstanceID})
}
