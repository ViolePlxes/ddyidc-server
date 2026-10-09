package admintodo

import (
	billing "ddyidc/internal/billing/balance"
	withdrawal "ddyidc/internal/billing/withdrawal"
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
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/api/v1/finance/withdrawals/quote", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, err := manager.ParseAccessTokenFor(r.Context(), auth.SessionUser, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		q, err := withdrawal.GetQuote(r.Context(), db, c.UserID, r.URL.Query().Get("amount"))
		if err != nil {
			reply(w, 400, nil, "提现核算未通过，请核对金额和规则")
			return
		}
		reply(w, 200, q, "")
	}})
	customer := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionUser, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if r.Method == http.MethodGet {
			rows := []BalanceWithdrawal{}
			query := db.WithContext(r.Context()).Model(&BalanceWithdrawal{}).Where("customer_id=?", c.UserID)
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
			if page < 1 {
				page = 1
			}
			if size < 1 || size > 100 {
				size = 10
			}
			if page > 100000 {
				reply(w, 400, nil, "页码无效")
				return
			}
			status := r.URL.Query().Get("status")
			if status != "" {
				if status != "pending" && status != "approved" && status != "paid" && status != "rejected" {
					reply(w, 400, nil, "状态无效")
					return
				}
				query = query.Where("status=?", status)
			}
			for _, item := range []struct{ Key, Operator string }{{"createdFrom", ">="}, {"createdTo", "<"}} {
				value := r.URL.Query().Get(item.Key)
				if value != "" {
					date, err := time.ParseInLocation("2006-01-02", value, time.FixedZone("Asia/Shanghai", 8*3600))
					if err != nil {
						reply(w, 400, nil, "日期无效")
						return
					}
					if item.Key == "createdTo" {
						date = date.AddDate(0, 0, 1)
					}
					query = query.Where("created_at "+item.Operator+" ?", date.UTC())
				}
			}
			var total int64
			if e = query.Count(&total).Error; e != nil {
				reply(w, 503, nil, "查询暂不可用")
				return
			}
			if r.URL.Query().Get("page") == "" {
				size = 100
			}
			e = query.Order("created_at DESC,id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error
			if e != nil {
				reply(w, 503, nil, "查询暂不可用")
				return
			}
			for i := range rows {
				rows[i].AdminID = 0
				rows[i].Evidence = ""
				rows[i].TransactionNo = nil
			}
			if r.URL.Query().Get("page") != "" {
				reply(w, 200, map[string]any{"list": rows, "total": total}, "")
			} else {
				reply(w, 200, rows, "")
			}
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
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/api/v1/finance/withdrawals/:id", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, err := manager.ParseAccessTokenFor(r.Context(), auth.SessionUser, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		var record BalanceWithdrawal
		if id <= 0 || db.WithContext(r.Context()).Where("id=? AND customer_id=?", id, c.UserID).Take(&record).Error != nil {
			reply(w, 404, nil, "提现账单不存在")
			return
		}
		flows := []billing.Transaction{}
		if db.WithContext(r.Context()).Where("customer_id=? AND related_type='balance_withdrawal' AND related_id=?", c.UserID, id).Order("account_version ASC").Find(&flows).Error != nil {
			reply(w, 503, nil, "账单流水暂不可用")
			return
		}
		for i := range flows {
			flows[i].OperatorIP = ""
			flows[i].AdminName = ""
			flows[i].AdminID = 0
		}
		record.AdminID = 0
		record.Evidence = ""
		record.TransactionNo = nil
		accountMasked := "已登记收款信息"
		if plain, err := crypto.Decrypt(record.AccountNote, key); err == nil {
			var recipient struct{ Account string }
			if json.Unmarshal([]byte(plain), &recipient) == nil {
				chars := []rune(recipient.Account)
				if len(chars) > 4 {
					accountMasked = "****" + string(chars[len(chars)-4:])
				}
			}
		}
		fee, net, err := withdrawal.SavedAmounts(r.Context(), db, id, record.Amount)
		if err != nil {
			reply(w, 503, nil, "费用快照暂不可用")
			return
		}
		reply(w, 200, map[string]any{"fee": fee, "net": net, "accountMasked": accountMasked, "record": record, "direction": "out", "businessType": "balance_withdrawal", "billId": strconv.FormatInt(id, 10), "flows": flows}, "")
	}})
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/api/v1/finance/withdrawals", Handler: customer})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/api/v1/finance/withdrawals", Handler: customer})
	admin := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !withdrawal.HasPermission(r.Context(), db, c.UserID, "view") {
			reply(w, 403, nil, "无提现查看权限")
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
			fee, net, err := withdrawal.SavedAmounts(r.Context(), db, id, out.Amount)
			if err != nil {
				reply(w, 503, nil, "费用快照暂不可用")
				return
			}
			var notice struct{ Status string }
			ne := db.WithContext(r.Context()).Table("payment.withdrawal_notifications").Where("withdrawal_id=?", id).Take(&notice).Error
			if ne != nil && ne != gorm.ErrRecordNotFound {
				reply(w, 503, nil, "通知状态暂不可用")
				return
			}
			canReview := withdrawal.HasPermission(r.Context(), db, c.UserID, "review")
			canRemit := withdrawal.HasPermission(r.Context(), db, c.UserID, "remit")
			if !withdrawal.HasPermission(r.Context(), db, c.UserID, "account") {
				reply(w, 200, map[string]any{"fee": fee, "net": net, "record": out, "account": "无收款信息查看权限", "canReview": canReview, "canRemit": canRemit, "notificationStatus": notice.Status}, "")
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
			reply(w, 200, map[string]any{"fee": fee, "net": net, "record": out, "account": account, "canReview": canReview, "canRemit": canRemit, "notificationStatus": notice.Status}, "")
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
		permission := "review"
		if in.Action == "remit" {
			permission = "remit"
		}
		if !withdrawal.HasPermission(r.Context(), db, c.UserID, permission) {
			reply(w, 403, nil, "无提现操作权限")
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
