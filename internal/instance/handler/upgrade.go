package instance

import (
	svc "ddyidc/internal/instance/service"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
	"strings"
)

func (h *handler) upgradeRoutes() []rest.Route {
	routes := []rest.Route{}
	for _, base := range []string{"/api/v1/instance/:instanceId", "/admin/v1/customers/:customerId/instances/:instanceId"} {
		for _, entry := range []struct{ method, path string }{{http.MethodGet, "/upgrade-options"}, {http.MethodPost, "/upgrade-quote"}, {http.MethodPost, "/upgrades"}} {
			routes = append(routes, rest.Route{Method: entry.method, Path: base + entry.path, Handler: h.upgrade})
		}
	}
	return routes
}
func (h *handler) upgrade(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	if r.Method == http.MethodGet {
		rows, e := h.service.UpgradePlans(r.Context(), customer, id)
		if e != nil {
			writeServiceError(w, e)
			return
		}
		writeJSON(w, map[string]any{"items": rows})
		return
	}
	var in svc.UpgradeInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil {
		writeError(w, 400, "升降配参数无效")
		return
	}
	var out svc.UpgradeView
	var e error
	if strings.HasSuffix(r.URL.Path, "/upgrade-quote") {
		out, e = h.service.QuoteUpgrade(r.Context(), customer, id, in)
	} else {
		out, e = h.service.CreateUpgradeOrder(r.Context(), customer, id, in)
	}
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, out)
}
