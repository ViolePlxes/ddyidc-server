package points

import (
	"net/http"
)

func (h *Handler) activities(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if !ok {
		return
	}
	rows, err := h.service.Activities(r.Context(), user, false)
	writeResult(w, rows, err)
}
func (h *Handler) claimActivity(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	points, err := h.service.ClaimActivity(r.Context(), user, id)
	writeResult(w, map[string]any{"points": points}, err)
}
func (h *Handler) adminActivities(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	rows, err := h.service.Activities(r.Context(), 0, true)
	writeResult(w, rows, err)
}
func (h *Handler) saveActivity(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var a Activity
	if !decode(w, r, &a) {
		return
	}
	if id, ok := optionalPathID(r); ok {
		a.ID = id
	}
	err := h.service.SaveActivity(r.Context(), &a)
	writeResult(w, a, err)
}
