package admintodo

import (
	"ddyidc/internal/marketplace"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"ddyidc/pkg/crypto"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func registerWithdrawals(server *rest.Server, db *gorm.DB, manager *auth.Manager, key []byte) {
	registerMarketAccounts(server, db, manager, key)
	customer := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionUser, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if r.Method == http.MethodGet {
			var rows []BalanceWithdrawal
			e = db.WithContext(r.Context()).Where("customer_id=?", c.UserID).Order("created_at DESC").Limit(100).Find(&rows).Error
			if e != nil {
				reply(w, 503, nil, "查询暂不可用")
				return
			}
			reply(w, 200, rows, "")
			return
		}
		var in struct {
			RequestID string `json:"requestId"`
			Amount    string `json:"amount"`
			Account   string `json:"account"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil {
			reply(w, 400, nil, "参数无效")
			return
		}
		out, e := SubmitWithdrawal(r.Context(), db, key, c.UserID, in.RequestID, in.Amount, in.Account)
		if e != nil {
			reply(w, 400, nil, "提现未提交，请检查开关、可用余额和收款信息")
			return
		}
		reply(w, 200, out, "")
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/api/v1/finance/withdrawals", Handler: customer})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/api/v1/finance/withdrawals", Handler: customer})
	admin := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可操作")
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		if id <= 0 {
			reply(w, 400, nil, "编号无效")
			return
		}
		if r.Method == http.MethodGet {
			var out BalanceWithdrawal
			if e = db.WithContext(r.Context()).First(&out, id).Error; e != nil {
				reply(w, 404, nil, "记录不存在")
				return
			}
			account, e := crypto.Decrypt(out.AccountNote, key)
			if e != nil {
				reply(w, 503, nil, "收款信息暂不可用")
				return
			}
			if e = db.WithContext(r.Context()).Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": strconv.FormatInt(snowflake.GenID(), 10), "kind": "withdrawals", "source": "balance", "object_id": id, "action": "view_account", "admin_id": c.UserID, "operator_ip": clientip.FromRequest(r), "notes": "查看提现收款信息", "created_at": time.Now().UTC()}).Error; e != nil {
				reply(w, 503, nil, "审计未保存")
				return
			}
			reply(w, 200, map[string]any{"record": out, "account": account}, "")
			return
		}
		var in struct {
			Revision     int64  `json:"revision"`
			Action       string `json:"action"`
			Reason       string `json:"reason"`
			RequestID    string `json:"requestId"`
			Transaction  string `json:"transactionNo"`
			Evidence     string `json:"evidence"`
			ConfirmLarge bool   `json:"confirmLarge"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil {
			reply(w, 400, nil, "参数无效")
			return
		}
		out, e := ReviewWithdrawal(r.Context(), db, id, in.Revision, c.UserID, c.Username, clientip.FromRequest(r), in.Reason, in.Action, in.RequestID, in.Transaction, in.Evidence, in.ConfirmLarge)
		if e != nil {
			code := 409
			if e == ErrQuery {
				code = 400
			}
			reply(w, code, nil, "操作未完成，请核对状态、余额、汇款凭证与大额确认")
			return
		}
		reply(w, 200, out, "")
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/finance/withdrawals/:id", Handler: admin})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/finance/withdrawals/:id/review", Handler: admin})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/marketplace-withdrawals/:id/remit", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可操作")
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		if id <= 0 {
			reply(w, 400, nil, "编号无效")
			return
		}
		var in struct {
			Reason       string `json:"reason"`
			RequestID    string `json:"requestId"`
			Transaction  string `json:"transactionNo"`
			Evidence     string `json:"evidence"`
			ConfirmLarge bool   `json:"confirmLarge"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil {
			reply(w, 400, nil, "参数无效")
			return
		}
		out, e := marketplace.NewService(db, nil, nil).ConfirmWithdrawal(r.Context(), c.UserID, id, c.Username, clientip.FromRequest(r), in.Reason, in.Transaction, in.Evidence, in.RequestID, in.ConfirmLarge)
		if e != nil {
			reply(w, 409, nil, "汇款确认未完成，请核对状态和凭证")
			return
		}
		reply(w, 200, out, "")
	}})
}
