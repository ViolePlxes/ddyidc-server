package api

import (
	"ddyidc/internal/shared/logger"
	"ddyidc/internal/upstream/service"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
	"strings"
)

type Handler struct{ Store *service.Store }

func RegisterHandlers(server *rest.Server, store *service.Store) {
	h := &Handler{Store: store}
	server.AddRoutes([]rest.Route{{Method: http.MethodGet, Path: "/admin/v1/upstream/suppliers", Handler: h.listSuppliers}, {Method: http.MethodPost, Path: "/admin/v1/upstream/suppliers", Handler: h.createSupplier}, {Method: http.MethodPut, Path: "/admin/v1/upstream/suppliers/:id", Handler: h.updateSupplier}, {Method: http.MethodDelete, Path: "/admin/v1/upstream/suppliers/:id", Handler: h.deleteSupplier}, {Method: http.MethodPost, Path: "/admin/v1/upstream/suppliers/:id/test", Handler: h.testSupplier}, {Method: http.MethodGet, Path: "/admin/v1/upstream/products", Handler: h.listProducts}, {Method: http.MethodGet, Path: "/admin/v1/upstream/suppliers/:id/products/pull", Handler: h.pullProducts}, {Method: http.MethodPost, Path: "/admin/v1/upstream/products", Handler: h.addProduct}, {Method: http.MethodPost, Path: "/admin/v1/upstream/products/batch-delete", Handler: h.batchDeleteProducts}, {Method: http.MethodPut, Path: "/admin/v1/upstream/products/:id/hidden", Handler: h.setHidden}, {Method: http.MethodPost, Path: "/admin/v1/upstream/products/:id/sync", Handler: h.syncProduct}, {Method: http.MethodGet, Path: "/admin/v1/upstream/groups", Handler: h.listGroups}, {Method: http.MethodPost, Path: "/admin/v1/upstream/groups", Handler: h.createGroup}, {Method: http.MethodDelete, Path: "/admin/v1/upstream/groups/:id", Handler: h.deleteGroup}, {Method: http.MethodPut, Path: "/admin/v1/upstream/config/loss-guard", Handler: h.setLossGuard}, {Method: http.MethodGet, Path: "/admin/v1/upstream/orders", Handler: h.listOrders}, {Method: http.MethodGet, Path: "/api/v1/upstream/order-page", Handler: h.orderPage}, {Method: http.MethodPost, Path: "/api/v1/upstream/sync_host", Handler: h.webhook}})
	server.AddRoute(rest.Route{Method: http.MethodPut, Path: "/admin/v1/upstream/products/:id", Handler: h.updateProduct})
	server.AddRoutes([]rest.Route{{Method: http.MethodGet, Path: "/admin/v1/upstream/suppliers/:id/catalog", Handler: h.catalog}, {Method: http.MethodGet, Path: "/admin/v1/upstream/tasks/:id", Handler: h.task}, {Method: http.MethodDelete, Path: "/admin/v1/upstream/tasks/:id", Handler: h.cancelTask}})
}
func respond(w http.ResponseWriter, data interface{}, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		logger.Error("upstream api error")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 1, "message": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": data})
}
func decode(r *http.Request, to interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(to)
}
func id(r *http.Request) string {
	return strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/v1/upstream/suppliers/"), "/")
}
func productID(r *http.Request) string {
	return strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/v1/upstream/products/"), "/sync")
}
