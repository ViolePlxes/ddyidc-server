package certification

import (
	"errors"
	"net/http"
)

func (h *handler) alipayInitialize(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, false)
	if !ok {
		return
	}
	if !h.limit(w, r, actor.UserID, "alipay_initialize", 10, 60) {
		return
	}
	var in Input
	if !decode(w, r, &in) {
		return
	}
	result, err := h.s.initializeAlipay(r.Context(), actor.UserID, in, ip(r), r.Header.Get("Origin")+"/console/certification")
	if err != nil {
		_ = audit(h.s.db, actor.UserID, false, "alipay_initialize_failed", actor.UserID, map[string]any{"result": "rejected"}, ip(r))
		if errors.Is(err, errAlipayRate) {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, err.Error())
		} else {
			fail(w, 400, err.Error())
		}
		return
	}
	reply(w, 200, result, "")
}

func (h *handler) alipayCertify(w http.ResponseWriter, r *http.Request) { h.alipayRead(w, r, false) }
func (h *handler) alipayQuery(w http.ResponseWriter, r *http.Request)   { h.alipayRead(w, r, true) }
func (h *handler) alipayRead(w http.ResponseWriter, r *http.Request, query bool) {
	actor, ok := h.actor(w, r, false)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, 400, "无需传入认证编号，仅可查询本人认证")
		return
	}
	if !h.limit(w, r, actor.UserID, "alipay_status", 30, 60) {
		return
	}
	result, err := h.s.alipayStatus(r.Context(), actor.UserID, ip(r), query)
	if err != nil {
		_ = audit(h.s.db, actor.UserID, false, "alipay_query_failed", actor.UserID, map[string]any{"result": "unavailable"}, ip(r))
		if errors.Is(err, errAlipayRate) {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, err.Error())
		} else {
			fail(w, 400, err.Error())
		}
		return
	}
	reply(w, 200, result, "")
}
