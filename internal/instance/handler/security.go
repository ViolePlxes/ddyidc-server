package instance

import (
	svc "ddyidc/internal/instance/service"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
)

func (h *handler) securityRoutes() []rest.Route {
	out := []rest.Route{}
	for _, base := range []string{"/api/v1/instance/:instanceId", "/admin/v1/customers/:customerId/instances/:instanceId"} {
		out = append(out, rest.Route{Method: http.MethodGet, Path: base + "/security-groups", Handler: h.securityGroups}, rest.Route{Method: http.MethodPost, Path: base + "/security-groups", Handler: h.securityGroups})
	}
	return out
}
func (h *handler) securityGroups(w http.ResponseWriter, r *http.Request) {
	c, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		rows, e := h.service.SecurityGroups(r.Context(), c, id)
		if e != nil {
			writeServiceError(w, e)
			return
		}
		writeJSON(w, map[string]any{"items": rows})
		return
	}
	var in svc.SecurityInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil {
		writeError(w, 400, "安全组参数无效")
		return
	}
	if e := h.service.SecurityOperation(r.Context(), c, id, in); e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
