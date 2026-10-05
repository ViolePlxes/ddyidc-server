package admintodo

import (
	"context"
	payment "ddyidc/internal/payment/domain"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"ddyidc/pkg/auth"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Compatibility endpoint: old clients must never close an external refund after
// the platform adopted reviewed balance-only refunds. This has no side effects.
func ConfirmManualRefund(_ context.Context, _ *gorm.DB, _, _ int64, _, _, _, _, _, _ string, _ time.Time, _ bool) error {
	return workflow.ErrPolicy
}

func registerManualRefunds(server *rest.Server, db *gorm.DB, manager *auth.Manager) {
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/refunds/:source/:id/receipt", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可核对")
			return
		}
		v := pathvar.Vars(r)
		id, e := strconv.ParseInt(v["id"], 10, 64)
		if e != nil || id <= 0 || (v["source"] != "payment" && v["source"] != "marketplace") {
			reply(w, 400, nil, "查询参数无效")
			return
		}
		var p payment.Payment
		q := db.WithContext(r.Context())
		if v["source"] == "payment" {
			q = q.Where("id=?", id)
		} else {
			q = q.Where("biz_type=? AND biz_id=?", payment.BizMarketplace, id)
		}
		if q.Take(&p).Error != nil {
			reply(w, 404, nil, "原始收款记录缺失，需人工核对")
			return
		}
		reply(w, 200, map[string]any{"paymentId": strconv.FormatInt(p.ID, 10), "amount": p.Amount, "channel": p.Channel, "status": p.Status, "payNo": p.PaymentNo, "updatedAt": p.UpdatedAt, "destination": "balance"}, "")
	}})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/refunds/:id/confirm-manual", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可核对")
			return
		}
		reply(w, http.StatusGone, nil, workflow.ErrPolicy.Error())
	}})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/refunds/:id/application", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可核查")
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		var in struct {
			Reason            string    `json:"reason"`
			RequestID         string    `json:"requestId"`
			ExpectedUpdatedAt time.Time `json:"expectedUpdatedAt"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil {
			reply(w, 400, nil, "申请参数无效")
			return
		}
		out, e := workflow.SubmitReceipt(r.Context(), db, id, in.Reason, in.RequestID, in.ExpectedUpdatedAt)
		if e != nil {
			reply(w, 409, nil, "收款不能转为退款申请，请核查多余收款事实、历史退款结果及最新状态")
			return
		}
		reply(w, 200, out, "")
	}})
}
