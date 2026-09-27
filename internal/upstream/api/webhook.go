package api

import (
	"io"
	"net/http"
)

func (h *Handler) webhook(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if e != nil {
		respond(w, nil, e)
		return
	}
	respond(w, map[string]bool{"accepted": true}, h.Store.HandleWebhook(r.Context(), r.Header.Get("X-Supplier-ID"), r.Header.Get("X-Upstream-Signature"), body))
}
