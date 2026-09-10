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
		{Method: http.MethodGet, Path: "/admin/v1/products/:id/specs", Handler: h.adminSpecs},
		{Method: http.MethodPut, Path: "/admin/v1/products/:id/specs", Handler: h.replaceSpecs},
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
	// Configuration shape comes only from the agent product-detail
	// configoptions field. Finance cart/set_config describes that system's cart
	// form and must never turn one fixed upstream SKU into many local SKUs.
	configMode := "fixed"
	configOptions := []upstreamcontracts.ConfigOption(nil)
	specs, specsErr := h.products.ListSpecs(r.Context(), id)
	if specsErr != nil {
		writeError(w, http.StatusInternalServerError, "读取商品规格失败")
		return
	}
	stock, cycle := product.Stock, product.Cycle
	if product.SupplierID != "" && product.UpstreamProductID > 0 && h.upstream != nil {
		if detail, upstreamErr := h.upstream.GetProductDetail(product.SupplierID, product.UpstreamProductID); upstreamErr == nil {
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
			if detail.HasConfigOptions && configMode == "fixed" {
				configMode = "elastic"
			}
		}
		// Finance product-detail responses frequently omit configoptions even
		// though cart/set_config is authoritative and complete. Use the existing
		// cloud-to-legacy fallback to classify and expose the real purchase form.
		if page, pageErr := h.upstream.GetOrderPage(product.SupplierID, product.UpstreamProductID); pageErr == nil {
			if pageMode := productConfigMode(page.ConfigOptions); pageMode == "elastic" {
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
		"id": product.ID, "name": product.Name, "summary": product.Summary, "description": product.Description,
		"moduleType": product.ModuleType, "price": product.Price, "cycle": cycle, "stock": stock, "config": config,
		"configMode": configMode, "configOptions": configOptions, "specs": specs,
		// These identifiers are deliberately exposed to the catalogue client so
		// it can request the provider's order-page configuration for this mapping.
		"supplierId": product.SupplierID, "upstreamProductId": strconv.Itoa(product.UpstreamProductID),
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
	items, total, err := h.products.ListAdminProducts(r.Context(), page, limit)
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
	product, err := h.products.FindPublicByID(r.Context(), id)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(w, http.StatusInternalServerError, "读取商品价格失败")
		return
	}
	items, err := h.products.ListSpecs(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取商品规格失败")
		return
	}
	if product != nil {
		for _, spec := range items {
			if spec.PriceMode == "upstream" {
				spec.Price = product.Price
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
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
	if len(input.Items) > 64 {
		writeError(w, http.StatusBadRequest, "每个商品最多配置 64 行规格")
		return
	}
	for index, spec := range input.Items {
		spec.Name = strings.TrimSpace(spec.Name)
		spec.Memory = strings.TrimSpace(spec.Memory)
		spec.ProcessorModel = strings.TrimSpace(spec.ProcessorModel)
		spec.ProcessorFrequency = strings.TrimSpace(spec.ProcessorFrequency)
		spec.Defense = strings.TrimSpace(spec.Defense)
		spec.UpstreamBandwidth = strings.TrimSpace(spec.UpstreamBandwidth)
		spec.DownstreamRate = strings.TrimSpace(spec.DownstreamRate)
		spec.PriceMode = strings.ToLower(strings.TrimSpace(spec.PriceMode))
		spec.Sort = index
		if spec.Name == "" || len([]rune(spec.Name)) > 64 || spec.VCPU < 1 || spec.VCPU > 1024 || spec.Memory == "" {
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
		if spec.Stock != nil && *spec.Stock < 0 {
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
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "pageSize": limit})
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
