package admintodo

import (
	"ddyidc/internal/instance/service"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
)

func RegisterMachineRefundActions(server *rest.Server, db *gorm.DB, manager *auth.Manager, svc *instance.Service) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可处理退款")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		id, e := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		if e != nil || id <= 0 {
			reply(w, 400, nil, "退款编号无效")
			return
		}
		if r.Method == http.MethodGet {
			out, e := svc.MachineRefundQuote(r.Context(), id)
			if e != nil {
				refundWorkflowError(w, e)
				return
			}
			reply(w, 200, out, "")
			return
		}
		var in instance.MachineRefundInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil {
			reply(w, 400, nil, "退款参数无效")
			return
		}
		out, e := svc.MachineRefundAction(r.Context(), id, workflow.Actor{ID: c.UserID, Name: c.Username, IP: clientip.FromRequest(r)}, in)
		if e != nil {
			refundWorkflowError(w, e)
			return
		}
		reply(w, 200, out, "")
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/refund-applications/:id/machine-actions", Handler: handler})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/refund-applications/:id/machine-actions", Handler: handler})
}
