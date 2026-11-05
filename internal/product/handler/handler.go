// Package product exposes public catalogue endpoints backed by MySQL.
package product

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	domain "ddyidc/internal/product/domain"
	repository "ddyidc/internal/product/repository"
	upstreamcontracts "ddyidc/internal/upstream/provider/contracts"
	upstream "ddyidc/internal/upstream/service"
	"ddyidc/pkg/outbound"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
)

type publicHandler struct {
	products repository.Repository
	upstream *upstream.Store
}

func RegisterPublicHandlers(server *rest.Server, products repository.Repository, upstreamStore *upstream.Store) {
	h := &publicHandler{products: products, upstream: upstreamStore}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/products", Handler: h.list},
		{Method: http.MethodGet, Path: "/api/v1/products/:id", Handler: h.detail},
		{Method: http.MethodGet, Path: "/admin/v1/products", Handler: h.adminList},
		{Method: http.MethodPut, Path: "/admin/v1/products/sort", Handler: h.sortProducts},
		{Method: http.MethodPut, Path: "/admin/v1/products/move-group", Handler: h.moveProducts},
		{Method: http.MethodGet, Path: "/admin/v1/products/:id/specs", Handler: h.adminSpecs},
		{Method: http.MethodPut, Path: "/admin/v1/products/:id/specs", Handler: h.replaceSpecs},
		{Method: http.MethodGet, Path: "/admin/v1/product-groups", Handler: h.adminGroups},
		{Method: http.MethodPost, Path: "/admin/v1/product-groups", Handler: h.createGroup},
		{Method: http.MethodPut, Path: "/admin/v1/product-groups/sort", Handler: h.sortGroups},
		{Method: http.MethodPut, Path: "/admin/v1/product-groups/move", Handler: h.moveGroup},
		{Method: http.MethodPut, Path: "/admin/v1/product-groups/:id/hidden", Handler: h.setGroupHidden},
		{Method: http.MethodPut, Path: "/admin/v1/product-groups/:id", Handler: h.updateGroup},
		{Method: http.MethodDelete, Path: "/admin/v1/product-groups/:id", Handler: h.deleteGroup},
	})
}

func (h *publicHandler) detail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/v1/products/"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "商品 ID 无效")
		return
	}
	product, err := h.products.FindPublicByID(r.Context(), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(w, http.StatusNotFound, "该商品不存在或已下架")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取商品详情失败")
		return
	}
	config := map[string]string{}
	// Classify the real provider resource choices without changing SKU count.
	configMode := "fixed"
	configOptions := []upstreamcontracts.ConfigOption(nil)
	specs, specsErr := h.products.ListSpecs(r.Context(), id)
	if specsErr != nil {
		writeError(w, http.StatusInternalServerError, "读取商品规格失败")
		return
	}
	view, viewErr := h.products.Specification(r.Context(), id)
	if viewErr != nil {
		writeError(w, 500, "读取规格快照失败")
		return
	}
	stock, cycle := product.Stock, product.Cycle
	infoSource, warning := "local", ""
	if product.SupplierID != "" && product.UpstreamProductID > 0 && h.upstream != nil {
		infoSource = "snapshot"
		warning = "实时商品信息暂时不可用，当前显示已保存的信息"
		if detail, upstreamErr := h.upstream.GetProductDetail(product.SupplierID, product.UpstreamProductID); upstreamErr == nil && detail != nil && detail.ID == product.UpstreamProductID {
			infoSource, warning = "upstream", ""
			for key, value := range detail.Config {
				config[key] = value
			}
			if detail.Stock >= 0 {
				stockValue := detail.Stock
				stock = &stockValue
			}
			if cycle == "" {
				cycle = detail.Cycle
			}
			configOptions = detail.ConfigOptions
			configMode = productConfigMode(detail.ConfigOptions)
		}
		// Finance product-detail responses frequently omit configoptions even
		// though cart/set_config is authoritative and complete. Use the existing
		// cloud-to-legacy fallback to classify and expose the real purchase form.
		if page, pageErr := h.upstream.GetOrderPage(product.SupplierID, product.UpstreamProductID); pageErr == nil && page != nil {
			if len(configOptions) == 0 {
				configOptions = page.ConfigOptions
			}
			if pageMode := productConfigMode(page.ConfigOptions); len(page.ConfigOptions) > 0 {
				configMode = pageMode
			}
			if cycle == "" && len(page.Cycles) > 0 {
				cycle = page.Cycles[0].Billingcycle
			}
		}
	}
	for _, spec := range specs {
		if spec.PriceMode == "upstream" {
			spec.Price = product.Price
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": product.ID, "groupId": product.GroupID, "parentGroupId": product.ParentGroupID,
		"infoSource": infoSource, "warning": warning, "readOnly": outbound.DetailReadOnly(),
		"groupName": product.GroupName, "sort": product.Sort,
		"name": product.Name, "summary": product.Summary, "description": product.Description,
		"moduleType": "实例", "price": product.Price, "cycle": cycle, "stock": stock, "config": config,
		"configMode": configMode, "configOptions": configOptions, "specs": publicSpecs(specs),
		"catalogSpec": publicSpec(view.Spec), "upstreamStock": view.UpstreamStock,
		"configurable": product.SupplierID != "" && product.SupplierID != "0" && product.UpstreamProductID > 0,
	})
}

func parseProductPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(pathvar.Vars(r)["id"])
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "商品 ID 无效")
		return 0, false
	}
	return id, true
}

func (h *publicHandler) adminList(w http.ResponseWriter, r *http.Request) {
	page := positiveInt(r.URL.Query().Get("page"), 1)
	limit := positiveInt(r.URL.Query().Get("pageSize"), 100)
	var groupID *int64
	if raw := strings.TrimSpace(r.URL.Query().Get("groupId")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "商品分组 ID 无效")
			return
		}
		groupID = &parsed
	}
	items, total, err := h.products.ListAdminProducts(r.Context(), page, limit, groupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取商品列表失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "pageSize": limit})
}

func (h *publicHandler) adminSpecs(w http.ResponseWriter, r *http.Request) {
	id, ok := parseProductPathID(w, r)
	if !ok {
		return
	}
	view, err := h.products.Specification(r.Context(), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(w, 404, "商品不存在")
		return
	}
	if err != nil {
		writeError(w, 500, "读取本地规格快照失败")
		return
	}
	writeJSON(w, 200, map[string]any{"items": []domain.ProductSpec{view.Spec}, "configured": view.Configured, "upstreamStock": view.UpstreamStock, "capturedAt": view.CapturedAt})
}
func (h *publicHandler) replaceSpecs(w http.ResponseWriter, r *http.Request) {
	id, ok := parseProductPathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Items []*domain.ProductSpec `json:"items"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "规格配置格式错误")
		return
	}
	if len(input.Items) != 1 || input.Items[0] == nil {
		writeError(w, http.StatusBadRequest, "每个商品只能保存一条规格配置")
		return
	}
	for index, spec := range input.Items {
		spec.Name = strings.TrimSpace(spec.Name)
		spec.Memory = strings.TrimSpace(spec.Memory)
		spec.OSFamily = strings.ToLower(strings.TrimSpace(spec.OSFamily))
		if spec.OSFamily != "" && spec.OSFamily != "linux" && spec.OSFamily != "windows" {
			writeError(w, http.StatusBadRequest, "系统类型无效")
			return
		}
		spec.ProcessorModel = strings.TrimSpace(spec.ProcessorModel)
		spec.ProcessorFrequency = strings.TrimSpace(spec.ProcessorFrequency)
		spec.Defense = strings.TrimSpace(spec.Defense)
		spec.UpstreamBandwidth = strings.TrimSpace(spec.UpstreamBandwidth)
		spec.DownstreamRate = strings.TrimSpace(spec.DownstreamRate)
		spec.PriceMode = strings.ToLower(strings.TrimSpace(spec.PriceMode))
		spec.Sort = index
		if len([]rune(spec.ProcessorModel)) > 128 || len([]rune(spec.ProcessorFrequency)) > 64 || len([]rune(spec.Defense)) > 64 || len([]rune(spec.UpstreamBandwidth)) > 64 || len([]rune(spec.DownstreamRate)) > 64 {
			writeError(w, 400, "规格字段过长")
			return
		}
		if spec.Name == "" || len([]rune(spec.Name)) > 64 || spec.VCPU < 0 || spec.VCPU > 1024 || len([]rune(spec.Memory)) > 32 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行规格名称、vCPU 或内存无效", index+1))
			return
		}
		if spec.PriceMode != "fixed" && spec.PriceMode != "upstream" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行价格模式无效", index+1))
			return
		}
		if spec.PriceMode == "fixed" && (spec.Price <= 0 || spec.Price > 10000000) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行固定价格无效", index+1))
			return
		}
		if spec.Stock != nil && (*spec.Stock < 0 || *spec.Stock > 2147483647) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行库存不能小于 0", index+1))
			return
		}
	}
	if err := h.products.ReplaceSpecs(r.Context(), id, input.Items); errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(w, http.StatusNotFound, "商品不存在")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "保存商品规格失败")
		return
	}
	h.adminSpecs(w, r)
}

func productConfigMode(options []upstreamcontracts.ConfigOption) string {
	if len(options) == 0 {
		return "fixed"
	}
	hasCustomChoice := false
	for _, option := range options {
		field := strings.ToLower(option.Field)
		if option.OptionType == "qty" && option.Max > option.Min {
			return "elastic"
		}
		switch field {
		case "cpu", "memory", "system_disk", "data_disk", "bandwidth", "ip_num":
			if len(option.Options) > 1 {
				return "elastic"
			}
		case "image", "network", "data_center":
			// Image, network and location selectors are required provisioning
			// metadata. They do not turn an otherwise fixed SKU into elastic.
		default:
			if len(option.Options) > 1 {
				hasCustomChoice = true
			}
		}
	}
	if hasCustomChoice {
		return "semi"
	}
	return "fixed"
}

func (h *publicHandler) list(w http.ResponseWriter, r *http.Request) {
	page := positiveInt(r.URL.Query().Get("page"), 1)
	limit := positiveInt(r.URL.Query().Get("pageSize"), 20)
	if limit > 100 {
		limit = 100
	}
	items, total, err := h.products.ListPublic(r.Context(), page, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取产品列表失败")
		return
	}
	groups, err := h.products.ListPublicGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取商品分组失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": publicProducts(items), "groups": groups, "total": total, "page": page, "pageSize": limit})
}

func positiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}

func publicSpec(v domain.ProductSpec) map[string]any {
	return map[string]any{"id": fmt.Sprint(v.ID), "productId": fmt.Sprint(v.ProductID), "name": v.Name, "vcpu": v.VCPU, "memory": v.Memory, "processorModel": v.ProcessorModel, "processorFrequency": v.ProcessorFrequency, "defense": v.Defense, "upstreamBandwidth": v.UpstreamBandwidth, "downstreamRate": v.DownstreamRate, "stock": v.Stock, "priceMode": v.PriceMode, "price": v.Price, "sort": v.Sort}
}
func publicSpecs(values []*domain.ProductSpec) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, v := range values {
		out = append(out, publicSpec(*v))
	}
	return out
}
func publicProducts(values []*repository.PublicProduct) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, v := range values {
		item := map[string]any{"id": v.ID, "groupId": v.GroupID, "parentGroupId": v.ParentGroupID, "groupName": v.GroupName, "name": v.Name, "moduleType": "实例", "stock": v.Stock, "summary": v.Summary, "description": v.Description, "price": v.Price, "cycle": v.Cycle, "sort": v.Sort, "configurable": v.SupplierID != "" && v.SupplierID != "0" && v.UpstreamProductID > 0}
		if v.CatalogSpec != nil {
			item["catalogSpec"] = publicSpec(*v.CatalogSpec)
		}
		// Stock is part of the existing storefront availability contract.
		item["upstreamStock"] = v.UpstreamStock
		out = append(out, item)
	}
	return out
}
