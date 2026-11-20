package certification

import (
	"errors"
	"net/http"
)

func (h *handler) tencentInitialize(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, false)
	if !ok {
		return
	}
	if !h.limit(w, r, actor.UserID, "tencent_initialize", 10, 60) {
		return
	}
	var in Input
	if !decode(w, r, &in) {
		_ = audit(h.s.db, actor.UserID, false, "tencent_initialize_failed", actor.UserID, map[string]any{"result": "invalid_request"}, ip(r))
		return
	}
	result, err := h.s.initializeTencent(r.Context(), actor.UserID, in, ip(r), r.Header.Get("Origin"))
	if err != nil {
		_ = audit(h.s.db, actor.UserID, false, "tencent_initialize_failed", actor.UserID, map[string]any{"result": "rejected"}, ip(r))
		if errors.Is(err, errTencentRate) {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, err.Error())
		} else {
			fail(w, 400, err.Error())
		}
		return
	}
	reply(w, 200, result, "")
}

func (h *handler) tencentCertify(w http.ResponseWriter, r *http.Request) { h.tencentRead(w, r, false) }
func (h *handler) tencentStatus(w http.ResponseWriter, r *http.Request)  { h.tencentRead(w, r, true) }

func (h *handler) tencentRead(w http.ResponseWriter, r *http.Request, query bool) {
	actor, ok := h.actor(w, r, false)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, 400, "无需传入认证编号，仅可查询本人认证")
		return
	}
	if !h.limit(w, r, actor.UserID, "tencent_status", 30, 60) {
		return
	}
	result, err := h.s.tencentStatus(r.Context(), actor.UserID, ip(r), query)
	if err != nil {
		_ = audit(h.s.db, actor.UserID, false, "tencent_query_failed", actor.UserID, map[string]any{"result": "unavailable"}, ip(r))
		if errors.Is(err, errTencentRate) {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, err.Error())
		} else {
			fail(w, 400, err.Error())
		}
		return
	}
	reply(w, 200, result, "")
}

// The provider redirect lands here. It only proves that the returned token
// belongs to this session's pending record and then hands the browser back to
// the console; every state change stays in tencentStatus.
func (h *handler) tencentCallback(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, false)
	if !ok {
		return
	}
	if !h.limit(w, r, actor.UserID, "tencent_callback", 20, 60) {
		return
	}
	token := r.URL.Query().Get("bizToken")
	if token == "" || len(token) > 512 {
		fail(w, 400, "回跳参数无效")
		return
	}
	if err := h.s.tencentVerifyToken(r.Context(), actor.UserID, token); err != nil {
		fail(w, 404, "核身记录不存在")
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, "/console/certification?tencent=done", http.StatusFound)
}
