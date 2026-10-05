package admintodo

import (
	dom "ddyidc/internal/payment/domain"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"errors"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
)

func registerApplications(server *rest.Server, db *gorm.DB, manager *auth.Manager) {
	user := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionUser, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if r.Method == http.MethodGet {
			order, e := strconv.ParseInt(r.URL.Query().Get("orderId"), 10, 64)
			if e != nil || order <= 0 {
				reply(w, 400, nil, "订单编号无效")
				return
			}
			var applications []dom.RefundApplication
			if e := db.WithContext(r.Context()).Where("user_id=? AND order_id=? AND refund_no LIKE 'TODO%'", c.UserID, order).Order("created_at DESC,id DESC").Limit(100).Find(&applications).Error; e != nil {
				reply(w, 503, nil, "退款记录暂不可用")
				return
			}
			var eligible int64
			if e := db.WithContext(r.Context()).Table("`order`.orders").Where("id=? AND customer_id=? AND status IN (2,5,6) AND pay_amount>0", order, c.UserID).Count(&eligible).Error; e != nil {
				reply(w, 503, nil, "订单记录暂不可用")
				return
			}
			reply(w, 200, map[string]any{"applications": applications, "canApply": eligible == 1, "destination": "balance"}, "")
			return
		}
		var in struct {
			OrderID           string
			Reason, RequestID string
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil {
			reply(w, 400, nil, "申请参数无效")
			return
		}
		id, _ := strconv.ParseInt(in.OrderID, 10, 64)
		out, e := workflow.SubmitOrder(r.Context(), db, c.UserID, id, in.Reason, in.RequestID)
		if e != nil {
			refundWorkflowError(w, e)
			return
		}
		reply(w, 200, out, "")
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/api/v1/payment/refund-applications", Handler: user})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/api/v1/payment/refund-applications", Handler: user})
	admin := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可处理")
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		if id <= 0 {
			reply(w, 400, nil, "编号无效")
			return
		}
		if r.Method == http.MethodGet {
			out, e := workflow.Load(r.Context(), db, id)
			if e != nil {
				reply(w, 404, nil, "申请不存在")
				return
			}
			reply(w, 200, out, "")
			return
		}
		var in workflow.ReviewInput
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || len(in.RequestID) < 8 || len(in.RequestID) > 64 {
			reply(w, 400, nil, "审核参数无效")
			return
		}
		_, e = workflow.Review(r.Context(), db, id, workflow.Actor{ID: c.UserID, Name: c.Username, IP: clientip.FromRequest(r)}, in)
		if e != nil {
			refundWorkflowError(w, e)
			return
		}
		reply(w, 200, map[string]bool{"reviewed": true}, "")
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/refund-applications/:id", Handler: admin})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/refund-applications/:id/review", Handler: admin})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/refund-applications/:id/preview", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可核算")
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		var in workflow.Fees
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if id <= 0 || d.Decode(&in) != nil {
			reply(w, 400, nil, "费用明细无效")
			return
		}
		calc, e := workflow.Preview(r.Context(), db, id, in)
		if e != nil {
			refundWorkflowError(w, e)
			return
		}
		reply(w, 200, calc, "")
	}})
}

func refundWorkflowError(w http.ResponseWriter, err error) {
	for _, known := range []error{workflow.ErrInput, workflow.ErrConflict, workflow.ErrCalculation, workflow.ErrEntitlement, workflow.ErrPolicy} {
		if errors.Is(err, known) {
			code := http.StatusConflict
			if errors.Is(err, workflow.ErrInput) {
				code = http.StatusBadRequest
			}
			reply(w, code, nil, known.Error())
			return
		}
	}
	reply(w, http.StatusServiceUnavailable, nil, "退款处理暂不可用，请稍后核查，勿重复入账")
}
