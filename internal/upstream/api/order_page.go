package api

import (
	repository "ddyidc/internal/product/repository"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/db"
	"fmt"
	"net/http"
	"strconv"
)

func (h *Handler) orderPage(w http.ResponseWriter, r *http.Request) {
	localID, err := strconv.ParseInt(r.URL.Query().Get("localProductId"), 10, 64)
	if err != nil || localID <= 0 {
		respond(w, nil, fmt.Errorf("商品参数无效"))
		return
	}
	product, err := repository.NewRepository(db.G).FindPublicByID(r.Context(), localID)
	if err != nil || product.SupplierID == "" || product.UpstreamProductID <= 0 {
		respond(w, nil, fmt.Errorf("商品配置暂不可用"))
		return
	}
	page, err := h.Store.GetOrderPage(product.SupplierID, product.UpstreamProductID)
	if err != nil || page == nil {
		respond(w, nil, fmt.Errorf("商品配置暂不可用"))
		return
	}
	respond(w, publicOrderPage(page), nil)
}

// Copy the cached provider response into a customer-facing allowlist.
func publicOrderPage(page *contracts.OrderPage) map[string]any {
	product := page.Product
	return map[string]any{"product": map[string]any{"id": strconv.Itoa(product.ID), "name": product.Name, "description": product.Description, "price": product.Price, "cycle": product.Cycle, "payType": product.PayType, "currencyCode": product.CurrencyCode, "setupFee": product.SetupFee, "stock": product.Stock, "trialEnabled": product.TrialEnabled, "trialPrice": product.TrialPrice}, "dataCenters": page.DataCenters, "images": page.Images, "imageGroups": page.ImageGroups, "configOptions": page.ConfigOptions, "cycles": page.Cycles, "networkTypes": page.NetworkTypes}
}
