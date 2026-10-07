package api

import (
	"ddyidc/internal/upstream/service"
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) listProducts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var need *bool
	if q.Get("needManualSync") != "" {
		v := q.Get("needManualSync") == "true"
		need = &v
	}
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	v, e := h.Store.ListAdminProducts(service.ProductListQuery{Page: page, PageSize: size, SupplierID: q.Get("supplierId"), Mode: q.Get("mode"), Keyword: q.Get("keyword"), NeedManualSync: need})
	respond(w, v, e)
}
func (h *Handler) pullProducts(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSuffix(id(r), "/products/pull")
	v, e := h.Store.StartCatalogSync(key, "manual")
	respond(w, v, e)
}
func (h *Handler) catalog(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSuffix(id(r), "/catalog")
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	v, e := h.Store.Catalog(key, q.Get("keyword"), q.Get("status"), page, size)
	respond(w, v, e)
}
func (h *Handler) task(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/admin/v1/upstream/tasks/")
	v, e := h.Store.Task(key)
	respond(w, v, e)
}
func (h *Handler) cancelTask(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/admin/v1/upstream/tasks/")
	respond(w, map[string]bool{"cancelled": true}, h.Store.CancelTask(key))
}
func (h *Handler) addProduct(w http.ResponseWriter, r *http.Request) {
	var in service.ProductInput
	if e := decode(r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Store.AddProduct(in)
	respond(w, v, e)
}
func (h *Handler) connectProducts(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SupplierID string                 `json:"supplierId"`
		GroupID    string                 `json:"groupId"`
		Items      []service.ProductInput `json:"items"`
	}
	if e := decode(r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Store.ConnectProducts(in.SupplierID, in.GroupID, in.Items)
	respond(w, map[string]interface{}{"items": v, "count": len(v)}, e)
}
func (h *Handler) updateProduct(w http.ResponseWriter, r *http.Request) {
	var in service.ProductInput
	if e := decode(r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/admin/v1/upstream/products/")
	v, e := h.Store.UpdateProduct(key, in)
	respond(w, v, e)
}
func (h *Handler) syncProduct(w http.ResponseWriter, r *http.Request) {
	key := productID(r)
	if r.URL.Query().Get("scope") == "price" {
		summary, e := h.Store.SyncProductPrice(key)
		respond(w, map[string]any{"status": "success", "summary": summary}, e)
		return
	}
	summary, e := h.Store.SyncProductWithSummary(key)
	respond(w, map[string]any{"status": "success", "summary": summary}, e)
}
func (h *Handler) batchDeleteProducts(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if e := decode(r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	respond(w, map[string]bool{"deleted": true}, h.Store.DeleteProducts(in.IDs))
}
func (h *Handler) setHidden(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Hidden bool `json:"hidden"`
	}
	if e := decode(r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	key := strings.TrimSuffix(productID(r), "/hidden")
	respond(w, map[string]bool{"hidden": in.Hidden}, h.Store.SetHidden(key, in.Hidden))
}
func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	v, e := h.Store.ListGroups()
	respond(w, v, e)
}
func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if e := decode(r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Store.CreateGroup(in.Name)
	respond(w, v, e)
}
func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/admin/v1/upstream/groups/")
	respond(w, map[string]bool{"deleted": true}, h.Store.DeleteGroup(key))
}
func (h *Handler) setLossGuard(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if e := decode(r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	respond(w, map[string]bool{"enabled": in.Enabled}, h.Store.SetLossGuard(in.Enabled))
}
func (h *Handler) listOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	v, e := h.Store.ListAdminOrders(page, size, q.Get("keyword"), q.Get("status"))
	respond(w, v, e)
}
