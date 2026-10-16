package notify

import (
	"github.com/zeromicro/go-zero/rest/pathvar"
	"net/http"
	"strconv"
)

func (h *adminHandler) smsReceipt(w http.ResponseWriter, r *http.Request) {
	h.queryReceipt(w, r, false)
}
func (h *adminHandler) smsTaskReceipt(w http.ResponseWriter, r *http.Request) {
	h.queryReceipt(w, r, true)
}
func (h *adminHandler) queryReceipt(w http.ResponseWriter, r *http.Request, task bool) {
	if !h.authorize(w, r) {
		return
	}
	id, e := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
	if e != nil || id <= 0 {
		adminError(w, 400, 400, "通知编号无效")
		return
	}
	var result SMSReceipt
	if task {
		result, e = h.manager.receiptForTask(r.Context(), id)
	} else {
		result, e = h.manager.QuerySMSReceipt(r.Context(), id, true)
	}
	if e != nil {
		adminError(w, 409, 409, e.Error())
		return
	}
	adminOK(w, result)
}
