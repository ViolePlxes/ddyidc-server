package instance

import (
	svc "ddyidc/internal/instance/service"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"errors"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
	"strconv"
)

func (h *handler) managementRoutes() []rest.Route {
	out := []rest.Route{}
	for _, base := range []string{"/admin/v1/customers/:customerId/instances/:instanceId", "/api/v1/instance/:instanceId"} {
		out = append(out,
			rest.Route{Method: http.MethodGet, Path: base + "/production-lock/email", Handler: h.productionLockEmail},
			rest.Route{Method: http.MethodPost, Path: base + "/production-lock/email/send", Handler: h.sendProductionLockEmail},
			rest.Route{Method: http.MethodPut, Path: base + "/production-lock", Handler: h.productionLock},
			rest.Route{Method: http.MethodPut, Path: base + "/remark", Handler: h.remark},
			rest.Route{Method: http.MethodPost, Path: base + "/credentials", Handler: h.credentials},
			rest.Route{Method: http.MethodGet, Path: base + "/renewal-options", Handler: h.renewalOptions},
			rest.Route{Method: http.MethodPost, Path: base + "/renewals", Handler: h.createRenewal},
			rest.Route{Method: http.MethodPost, Path: base + "/renewals/pay", Handler: h.payRenewal},
			rest.Route{Method: http.MethodPut, Path: base + "/auto-renew", Handler: h.autoRenew})
	}
	return append(out, rest.Route{Method: http.MethodPatch, Path: "/admin/v1/customers/:customerId/instances/:instanceId", Handler: h.edit})
}
func (h *handler) productionLock(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	var in struct {
		Enabled   *bool  `json:"enabled"`
		Confirmed bool   `json:"confirmed"`
		Email     string `json:"email"`
		Code      string `json:"code"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || in.Enabled == nil || !in.Confirmed {
		writeError(w, 400, "请确认生产锁操作")
		return
	}
	if err := h.service.SetProductionLock(r.Context(), customer, id, *in.Enabled, svc.ProductionLockEmailInput{Email: in.Email, Code: in.Code}); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
func (h *handler) edit(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	var in svc.EditInput
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
		writeError(w, 400, "参数错误")
		return
	}
	if e := h.service.Edit(r.Context(), customer, id, in); e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
func (h *handler) credentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	out, e := h.service.Credentials(r.Context(), customer, id)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, out)
}
func (h *handler) renewalOptions(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	out, e := h.service.RenewalOptions(r.Context(), customer, id)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	cycle, err := h.service.OriginalRenewalCycle(r.Context(), customer, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	matched := false
	for _, option := range out {
		if option.Cycle == cycle {
			matched = true
			break
		}
	}
	if !matched {
		cycle = ""
	}
	writeJSON(w, map[string]any{"items": out, "currentCycle": cycle})
}
func (h *handler) createRenewal(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	var in struct {
		Cycle string `json:"cycle"`
		svc.RenewalIntent
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || in.Cycle == "" {
		writeError(w, 400, "请选择续费周期")
		return
	}
	if in.QuoteRevision == "" {
		writeError(w, 409, "续费报价需要重新确认，请刷新后重试")
		return
	}
	out, e := h.service.CreateRenewalIntent(r.Context(), customer, id, in.Cycle, in.RenewalIntent)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, out)
}
func (h *handler) payRenewal(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	var in struct {
		OrderID string `json:"orderId"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
		writeError(w, 400, "参数错误")
		return
	}
	oid, e := strconv.ParseInt(in.OrderID, 10, 64)
	if e != nil || oid <= 0 {
		writeError(w, 400, "订单 ID 无效")
		return
	}
	ip := clientip.FromRequest(r)
	if e = h.service.PayRenewalBalance(r.Context(), customer, id, oid, ip); e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, map[string]bool{"paid": true})
}
func (h *handler) autoRenew(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	var in struct {
		Enabled       *bool  `json:"enabled"`
		QuoteRevision string `json:"quoteRevision"`
		Cycle         string `json:"cycle"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || in.Enabled == nil {
		writeError(w, 400, "缺少开关状态")
		return
	}
	if e := h.service.SetAutoRenewCycle(r.Context(), customer, id, *in.Enabled, in.Cycle, in.QuoteRevision); e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}

func (h *handler) remark(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	var in svc.RemarkInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		writeError(w, 400, "备注参数错误")
		return
	}
	if err := h.service.SaveRemark(r.Context(), customer, id, in); err != nil {
		if errors.Is(err, svc.ErrInvalidRemark) {
			writeError(w, 400, err.Error())
		} else {
			writeServiceError(w, err)
		}
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
