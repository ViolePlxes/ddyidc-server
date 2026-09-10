package api

import (
	"fmt"
	"net/http"
	"strconv"
)

// orderPage is deliberately read-only: it lets the storefront/admin preview
// real upstream constraints before any order or payment request is attempted.
func (h *Handler) orderPage(w http.ResponseWriter, r *http.Request) {
	supplierID := r.URL.Query().Get("supplierId")
	productID, err := strconv.Atoi(r.URL.Query().Get("productId"))
	if supplierID == "" || err != nil || productID <= 0 {
		respond(w, nil, fmt.Errorf("supplierId and a positive productId are required"))
		return
	}
	page, err := h.Store.GetOrderPage(supplierID, productID)
	respond(w, page, err)
}
