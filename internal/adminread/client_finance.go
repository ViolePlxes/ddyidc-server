package adminread

import (
	"crypto/sha256"
	"ddyidc/pkg/clientip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	balance "ddyidc/internal/billing/balance"
	payment "ddyidc/internal/payment/domain"
	paymentrepo "ddyidc/internal/payment/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type balanceInput struct {
	Type              string   `json:"type"`
	Amount            string   `json:"amount"`
	FreezeAmount      string   `json:"freeze_amount"`
	Notes             string   `json:"notes"`
	ClientNotes       string   `json:"client_notes"`
	RequestID         string   `json:"request_id"`
	Gateway           string   `json:"gateway"`
	TransactionNumber string   `json:"transaction_number"`
	CreditIDs         []string `json:"credit_ids"`
	ConfirmLarge      bool     `json:"confirm_large"`
}

func requestIP(r *http.Request) string { return clientip.FromRequest(r) }
func (h *handler) balanceActor(r *http.Request) (int64, string, error) {
	claims, err := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if err != nil || claims.Role != "admin" {
		return 0, "", errors.New("管理员会话无效")
	}
	var actor struct{ Username string }
	if err = h.db.WithContext(r.Context()).Table("system.admins").Select("username").Where("id=? AND status=1", claims.UserID).Take(&actor).Error; err != nil {
		return 0, "", err
	}
	return claims.UserID, actor.Username, nil
}
func (h *handler) credit(w http.ResponseWriter, r *http.Request) {
	id, ok := h.detailID(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		h.creditList(w, r, id, false)
		return
	}
	var in balanceInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, 400, "余额参数格式错误")
		return
	}
	if in.RequestID == "" {
		in.RequestID = r.Header.Get("Idempotency-Key")
	}
	actor, name, err := h.balanceActor(r)
	if err != nil {
		writeError(w, 403, "管理员不可用")
		return
	}
	command := balance.ChangeRequest{CustomerID: id, Type: in.Type, Amount: in.Amount, RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: in.RequestID, AdminID: actor, AdminName: name, OperatorIP: requestIP(r), Notes: strings.TrimSpace(in.Notes), ClientNotes: strings.TrimSpace(in.ClientNotes), BizTrace: in.TransactionNumber}
	action := "adjust"
	if strings.HasSuffix(r.URL.Path, "/recharge") {
		action = "recharge"
		command.Type = "recharge"
	}
	if strings.HasSuffix(r.URL.Path, "/freeze") {
		action = "freeze"
		command.Type = "freeze"
		command.Amount = in.FreezeAmount
	}
	if strings.HasSuffix(r.URL.Path, "/unfreeze") {
		action = "unfreeze"
		command.Type = "unfreeze"
	}
	if !hasBalancePermission(r.Context(), h.db, actor, action) {
		writeError(w, 403, "没有该余额操作权限")
		return
	}
	if action == "adjust" && in.Type != "manual_credit" && in.Type != "manual_debit" {
		writeError(w, 400, "请选择调增或调减")
		return
	}
	if command.RequestID == "" || len(command.RequestID) > 64 || command.Notes == "" {
		writeError(w, 400, "幂等键与操作原因必填")
		return
	}
	if action != "unfreeze" {
		value, e := balance.ParseMoney(command.Amount)
		if e != nil || value <= 0 {
			writeError(w, 400, "金额必须为正数字符串且最多两位小数")
			return
		}
		command.Amount = value.String()
		if command.Type == "manual_debit" || command.Type == "freeze" {
			command.Amount = balance.Money(-value).String()
		}
		if (command.Type == "manual_credit" || command.Type == "manual_debit") && value > 5000000 {
			var permission struct{ Value string }
			e = h.db.Table("system.system_configs").Select("value").Where("`key`=?", fmt.Sprintf("billing.large_adjustment.admin.%d", actor)).Take(&permission).Error
			if e != nil || permission.Value != "1" || !in.ConfirmLarge {
				writeError(w, 403, "超过五万元需要大额调整权限及二次确认")
				return
			}
			command.LargeAdjustmentAllowed = true
		}
	}
	if action == "adjust" && command.ClientNotes == "" {
		var existing balance.Transaction
		if e := h.db.Where("tx_no=?", command.RequestID).Take(&existing).Error; e != nil {
			writeError(w, 400, "客户可见原因必填")
			return
		}
	}
	var result any
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		service := balance.New(tx)
		switch action {
		case "recharge":
			if (in.Gateway != "alipay" && in.Gateway != "wechat" && in.Gateway != "bank_transfer") || strings.TrimSpace(in.TransactionNumber) == "" || len(in.TransactionNumber) > 128 {
				return errors.New("支付方式或交易流水号无效")
			}
			// Serializes retries before allocating the recharge/payment business IDs.
			var customer struct{ ID int64 }
			if e := tx.Table("customer.customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=?", id).Take(&customer).Error; e != nil {
				return e
			}
			var previous balance.Transaction
			e := tx.Where("tx_no=?", command.RequestID).Take(&previous).Error
			if e == nil {
				var original payment.Payment
				if e = tx.Where("id=?", previous.RelatedID).Take(&original).Error; e != nil {
					return e
				}
				if original.Channel != in.Gateway {
					return balance.ErrReplay
				}
				command.RefType = "payment"
				command.RefID = previous.RelatedID
				result, e = service.Change(r.Context(), command)
				return e
			}
			if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			value, _ := balance.ParseMoney(command.Amount)
			now := time.Now()
			paymentID, e := paymentrepo.RecordAdminRechargeTx(tx, id, actor, command.Notes, value, in.Gateway, in.TransactionNumber, now)
			if e != nil {
				return e
			}
			command.RefType = "payment"
			command.RefID = paymentID
			result, e = service.Change(r.Context(), command)
			return e
		case "unfreeze":
			if len(in.CreditIDs) == 0 || len(in.CreditIDs) > 50 {
				return errors.New("请选择 1-50 条冻结记录")
			}
			seen := map[int64]bool{}
			rows := []*balance.Transaction{}
			batchRaw, _ := json.Marshal(in.CreditIDs)
			batchHash := sha256.Sum256(batchRaw)
			command.BizTrace = hex.EncodeToString(batchHash[:])
			for index, raw := range in.CreditIDs {
				ref, e := strconv.ParseInt(raw, 10, 64)
				if e != nil || ref <= 0 || seen[ref] {
					return errors.New("冻结记录无效或重复")
				}
				seen[ref] = true
				var frozen balance.Transaction
				if e = tx.Where("id=? AND customer_id=? AND type='freeze'", ref, id).Take(&frozen).Error; e != nil {
					return balance.ErrBusiness
				}
				cmd := command
				cmd.RefType = "freeze"
				cmd.RefID = ref
				cmd.Amount = balance.Money(-frozen.Amount).String()
				cmd.RequestID = command.RequestID
				if index > 0 {
					key := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", command.RequestID, index)))
					cmd.RequestID = hex.EncodeToString(key[:])
				}
				row, e := service.Change(r.Context(), cmd)
				if e != nil {
					return e
				}
				rows = append(rows, row)
			}
			result = rows
			return nil
		default:
			var e error
			result, e = service.Change(r.Context(), command)
			return e
		}
	})
	if err != nil {
		if errors.Is(err, balance.ErrLedgerIntegrity) {
			writeError(w, 409, balance.ErrLedgerIntegrity.Error())
			return
		}
		writeError(w, 400, err.Error())
		return
	}
	_ = balance.New(h.db).CheckManualFrequency(r.Context(), id)
	writeJSON(w, result)
}
func (h *handler) frozenCredits(w http.ResponseWriter, r *http.Request) {
	id, ok := h.detailID(w, r)
	if !ok {
		return
	}
	h.creditList(w, r, id, true)
}
func (h *handler) creditList(w http.ResponseWriter, r *http.Request, id int64, frozen bool) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	q := h.db.WithContext(r.Context()).Model(&balance.Transaction{}).Where("customer_id=?", id)
	if frozen {
		q = q.Where("type='freeze'")
	} else if typ := r.URL.Query().Get("type"); typ != "" {
		q = q.Where("type=?", typ)
	}
	if keyword := r.URL.Query().Get("keywords"); keyword != "" {
		q = q.Where("CAST(id AS CHAR) LIKE ? OR remark LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if order := r.URL.Query().Get("order_id"); order != "" {
		q = q.Where("related_id=?", order)
	}
	for _, key := range []string{"start_time", "end_time"} {
		if v := r.URL.Query().Get(key); v != "" {
			at, e := time.Parse(time.RFC3339, v)
			if e != nil {
				writeError(w, 400, "时间筛选须为 ISO 8601")
				return
			}
			op := ">="
			if key == "end_time" {
				op = "<="
			}
			q = q.Where("created_at "+op+" ?", at)
		}
	}
	var count int64
	var total balance.Money
	if e := q.Session(&gorm.Session{}).Count(&count).Error; e != nil {
		writeError(w, 500, "读取流水失败")
		return
	}
	if e := q.Session(&gorm.Session{}).Select("COALESCE(SUM(amount),0)").Scan(&total).Error; e != nil {
		writeError(w, 500, "汇总流水失败")
		return
	}
	rows := []balance.Transaction{}
	if e := q.Order("id DESC").Offset((page - 1) * limit).Limit(limit).Find(&rows).Error; e != nil {
		writeError(w, 500, "读取流水失败")
		return
	}
	list := []map[string]any{}
	var pageTotal balance.Money
	for _, row := range rows {
		raw, _ := json.Marshal(row)
		var item map[string]any
		_ = json.Unmarshal(raw, &item)
		if frozen {
			var n int64
			if e := h.db.Model(&balance.Transaction{}).Where("type='unfreeze' AND related_type='freeze' AND related_id=?", row.ID).Count(&n).Error; e != nil {
				writeError(w, 500, "读取解冻状态失败")
				return
			}
			item["is_unfrozen"] = n > 0
		}
		pageTotal += row.Amount
		list = append(list, item)
	}
	writeJSON(w, map[string]any{"list": list, "count": count, "page_total_amount": pageTotal.String(), "total_amount": total.String()})
}
func (h *handler) clientLogin(w http.ResponseWriter, r *http.Request) {
	id, ok := h.detailID(w, r)
	if !ok {
		return
	}
	actor, name, e := h.balanceActor(r)
	if e != nil {
		writeError(w, 403, "管理员会话无效")
		return
	}
	var customer struct {
		Username string
		Status   int
	}
	if e = h.db.Table("customer.customers").Select("username,status").Where("id=?", id).Take(&customer).Error; e != nil || customer.Status != 1 {
		writeError(w, 400, "用户不存在或已停用")
		return
	}
	actorClaims, e := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if e != nil {
		writeError(w, 403, "管理员会话无效")
		return
	}
	token, e := h.auth.IssueImpersonationSession(r.Context(), id, customer.Username, actorClaims)
	if e != nil {
		writeError(w, 500, "创建用户会话失败")
		return
	}
	now := time.Now()
	e = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if e := tx.Table("customer.login_logs").Create(map[string]any{"id": snowflake.GenID(), "customer_id": id, "username": customer.Username, "ip": requestIP(r), "status": 1, "created_at": now, "updated_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Table("customer.customers").Where("id=?", id).Updates(map[string]any{"last_login_at": now, "last_login_ip": requestIP(r)}).Error; e != nil {
			return e
		}
		return auditCustomer(tx, id, actor, name, "impersonate", "user_type=client", requestIP(r))
	})
	if e != nil {
		_ = h.auth.RevokeSession(r.Context(), auth.SessionUser, id)
		writeError(w, 500, "记录登录审计失败")
		return
	}
	h.auth.SetSessionCookie(w, auth.SessionUser, token)
	writeJSON(w, map[string]any{"redirect": "/console"})
}
func auditCustomer(tx *gorm.DB, id, actor int64, name, action, fields, ip string) error {
	return tx.Table("customer.admin_customer_audits").Create(map[string]any{"id": snowflake.GenID(), "customer_id": id, "admin_id": actor, "admin_name": name, "action": action, "fields": fields, "operator_ip": ip, "created_at": time.Now()}).Error
}
