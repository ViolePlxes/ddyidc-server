package admintodo

import (
	"crypto/sha256"
	"ddyidc/internal/marketplace"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"ddyidc/pkg/crypto"
	"encoding/hex"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func registerMarketAccounts(server *rest.Server, db *gorm.DB, manager *auth.Manager, key []byte) {
	h := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可查阅")
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		if id <= 0 {
			reply(w, 400, nil, "编号无效")
			return
		}
		var row marketplace.Withdrawal
		if db.WithContext(r.Context()).First(&row, id).Error != nil {
			reply(w, 404, nil, "提现记录不存在")
			return
		}
		if r.Method == http.MethodGet {
			var account struct{ Ciphertext string }
			if db.WithContext(r.Context()).Table("payment.todo_withdrawal_accounts").Where("source='marketplace' AND withdrawal_id=? AND customer_id=?", id, row.UserID).Take(&account).Error != nil {
				reply(w, 404, nil, "历史申请缺少收款账户，请先向客户核实登记")
				return
			}
			plain, e := crypto.Decrypt(account.Ciphertext, key)
			if e != nil {
				reply(w, 503, nil, "收款账户暂不可用")
				return
			}
			if db.WithContext(r.Context()).Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": strconv.FormatInt(snowflake.GenID(), 10), "kind": "withdrawals", "source": "marketplace", "object_id": id, "action": "view_account", "admin_id": c.UserID, "operator_ip": clientip.FromRequest(r), "notes": "查看市场提现收款账户", "created_at": time.Now().UTC()}).Error != nil {
				reply(w, 503, nil, "访问审计保存失败")
				return
			}
			reply(w, 200, map[string]any{"account": plain, "fee": row.Fee, "actualAmount": row.ActualAmount}, "")
			return
		}
		var in struct {
			Account, Reason, RequestID string
			ExpectedUpdatedAt          time.Time
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || strings.TrimSpace(in.Account) == "" || len(in.Account) > 256 || strings.TrimSpace(in.Reason) == "" || len([]rune(in.Reason)) > 256 || len(in.RequestID) < 8 || len(in.RequestID) > 64 || in.ExpectedUpdatedAt.IsZero() {
			reply(w, 400, nil, "请填写客户确认的收款账户与核验说明")
			return
		}
		cipher, e := crypto.Encrypt(in.Account, key)
		if e != nil {
			reply(w, 503, nil, "账户保存暂不可用")
			return
		}
		sum := sha256.Sum256([]byte(in.Account))
		evidence := hex.EncodeToString(sum[:])
		e = db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
			replay, e := auditReplay(tx, in.RequestID, "withdrawals", "marketplace", id, c.UserID, "register_account", in.Reason, evidence)
			if e != nil {
				return e
			}
			if replay {
				return nil
			}
			var current marketplace.Withdrawal
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error; e != nil {
				return e
			}
			if (current.Status != "pending" && current.Status != "approved") || !current.UpdatedAt.Equal(in.ExpectedUpdatedAt) {
				return ErrConflict
			}
			if e := tx.Table("payment.todo_withdrawal_accounts").Create(map[string]any{"source": "marketplace", "withdrawal_id": id, "customer_id": current.UserID, "ciphertext": cipher, "request_id": in.RequestID, "created_at": time.Now().UTC()}).Error; e != nil {
				return e
			}
			return tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": in.RequestID, "kind": "withdrawals", "source": "marketplace", "object_id": id, "action": "register_account", "admin_id": c.UserID, "operator_ip": clientip.FromRequest(r), "notes": in.Reason, "evidence": evidence, "created_at": time.Now().UTC()}).Error
		})
		if e != nil {
			reply(w, 409, nil, "账户未登记，请刷新；已登记账户不能直接替换")
			return
		}
		reply(w, 200, map[string]bool{"registered": true}, "")
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/marketplace-withdrawals/:id/account", Handler: h})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/marketplace-withdrawals/:id/account", Handler: h})
}
