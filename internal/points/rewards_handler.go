package points

import "net/http"

func (h *Handler) activity(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.Activity(r.Context(), id))
}
func (h *Handler) referrals(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	p, z := queryPage(r)
	rows, total, err := h.service.ReferralChildren(r.Context(), id, p, z)
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) publicCategories(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.user(w, r); !ok {
		return
	}
	rows, err := h.service.Categories(r.Context())
	writeResult(w, rows, err)
}
