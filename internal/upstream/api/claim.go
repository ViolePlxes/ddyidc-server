package api

import (
	"net/http"
	"strconv"
	"time"
)

// invoiceHost reconciles a procurement whose payment result was lost: it finds
// the upstream host created for a paid invoice. Read-only; the administrator
// still binds the host through the existing instance edit flow.
func (h *Handler) invoiceHost(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	productID, _ := strconv.Atoi(query.Get("productId"))
	since := time.Time{}
	if raw := query.Get("since"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			since = parsed
		} else if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
			since = time.Unix(unix, 0)
		}
	}
	result, err := h.Store.FindInvoiceHost(r.Context(), query.Get("supplierId"), query.Get("invoice"), productID, since)
	respond(w, result, err)
}
