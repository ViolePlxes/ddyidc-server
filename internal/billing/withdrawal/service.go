package withdrawal

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/crypto"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

var ErrQuery = errors.New("提现参数无效")
var ErrConflict = errors.New("提现状态或配置已变化")

type BalanceWithdrawal struct {
	ID            int64     `json:"id,string" gorm:"primaryKey"`
	RequestID     string    `json:"requestId"`
	CustomerID    int64     `json:"customerId,string"`
	Amount        string    `json:"amount"`
	AccountNote   string    `json:"-"`
	Status        string    `json:"status"`
	Revision      int64     `json:"revision"`
	Reason        string    `json:"reason"`
	TransactionNo *string   `json:"transactionNo"`
	Evidence      string    `json:"evidence"`
	AdminID       int64     `json:"adminId,string"`
	OperatorIP    string    `json:"-"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (BalanceWithdrawal) TableName() string { return "payment.balance_withdrawals" }
func SubmitWithdrawal(ctx context.Context, db *gorm.DB, key []byte, customer int64, request, amount, account string) (BalanceWithdrawal, error) {
	return SubmitFrom(ctx, db, key, customer, request, amount, account, "finance", "")
}

// SubmitFrom is an internal module adapter, never a caller-controlled authorization scope.
func SubmitFrom(ctx context.Context, db *gorm.DB, key []byte, customer int64, request, amount, account, source, business string) (BalanceWithdrawal, error) {
	var out BalanceWithdrawal
	if source == "" || len(source) > 32 || len(business) > 64 {
		return out, ErrQuery
	}
	money, e := billing.ParseMoney(amount)
	if e != nil || money <= 0 || customer <= 0 || len(request) < 8 || len(request) > 64 || strings.TrimSpace(account) == "" || len(account) > 350 {
		return out, ErrQuery
	}
	if strings.HasPrefix(account, "{") {
		var recipient struct {
			Channel string `json:"channel"`
			Name    string `json:"name"`
			Account string `json:"account"`
		}
		if json.Unmarshal([]byte(account), &recipient) != nil || (recipient.Channel != "bank" && recipient.Channel != "alipay" && recipient.Channel != "wechat") || strings.TrimSpace(recipient.Name) == "" || len([]rune(recipient.Name)) > 50 || strings.TrimSpace(recipient.Account) == "" || len([]rune(recipient.Account)) > 120 {
			return out, ErrQuery
		}
	}
	encrypted, e := crypto.Encrypt(account, key)
	if e != nil {
		return out, e
	}
	e = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner struct{ ID int64 }
		if err := tx.Table("customer.customers").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL AND status=1", customer).Take(&owner).Error; err != nil {
			return err
		}
		err := tx.Where("request_id=?", request).Take(&out).Error
		if err == nil {
			var prior struct{ SourceModule, SourceBusinessID string }
			se := tx.Table("payment.withdrawal_snapshots").Where("withdrawal_id=?", out.ID).Take(&prior).Error
			if se != nil && se != gorm.ErrRecordNotFound {
				return se
			}
			if se == nil && (prior.SourceModule != source || prior.SourceBusinessID != business) {
				return ErrConflict
			}
			if se == gorm.ErrRecordNotFound && (source != "finance" || business != "") {
				return ErrConflict
			}
			original, e := crypto.Decrypt(out.AccountNote, key)
			if e != nil || out.CustomerID != customer || out.Amount != money.String() || original != account {
				return ErrConflict
			}
			return nil
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		var enabled int64
		if e := tx.Table("system.system_configs").Where("`key`='todos.withdrawals.enabled' AND value IN ('1','true')").Count(&enabled).Error; e != nil {
			return e
		}
		if enabled != 1 {
			return ErrConflict
		}
		var policy string
		if e := tx.Table("system.system_configs").Select("value").Where("`key`=?", "todos.withdrawals.fee_policy").Scan(&policy).Error; e != nil {
			return e
		}
		if !ValidFeePolicy(policy) {
			return ErrConflict
		}
		// Serialize eligibility reads with every balance mutation, including
		// concurrent spending that can consume the verified cash portion first.
		var lockedAccount billing.Account
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=?", customer).Take(&lockedAccount).Error; err != nil {
			return ErrConflict
		}
		quote, err := GetQuote(ctx, tx, customer, amount)
		if err != nil || quote.Policy.Reason != "" {
			return ErrConflict
		}
		now := time.Now().UTC()
		out = BalanceWithdrawal{ID: snowflake.GenID(), RequestID: request, CustomerID: customer, Amount: money.String(), AccountNote: encrypted, Status: "pending", Revision: 1, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&out).Error; e != nil {
			return e
		}
		snapshot, err := json.Marshal(quote.Policy)
		if err != nil {
			return err
		}
		if err = tx.Table("payment.withdrawal_snapshots").Create(map[string]any{"withdrawal_id": out.ID, "customer_id": customer, "source_module": source, "source_business_id": business, "fee": quote.Fee.String(), "net_amount": quote.Net.String(), "policy_json": string(snapshot), "created_at": now}).Error; err != nil {
			return err
		}
		_, e := billing.New(tx).Change(ctx, billing.ChangeRequest{CustomerID: customer, Type: "withdrawal_hold", Amount: (-money).String(), RefType: "balance_withdrawal", RefID: out.ID, Channel: "withdrawal", RequestID: fmt.Sprintf("BWH%d", out.ID), Notes: "余额提现申请冻结"})
		return e
	})
	return out, e
}
func ReviewWithdrawal(ctx context.Context, db *gorm.DB, id, revision, admin int64, name, ip, reason, action, request, transaction, evidence string, confirmed bool) (BalanceWithdrawal, error) {
	var out BalanceWithdrawal
	if id <= 0 || revision < 1 || admin <= 0 || name == "" || ip == "" || strings.TrimSpace(reason) == "" || len([]rune(reason)) > 500 || len(request) < 8 || len(request) > 64 {
		return out, ErrQuery
	}
	if action != "approve" && action != "reject" && action != "remit" {
		return out, ErrQuery
	}
	e := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&out, id).Error; e != nil {
			return e
		}
		replay, err := auditReplay(tx, request, "withdrawals", "balance", id, admin, action, reason, transaction+"\n"+evidence)
		if err != nil {
			return err
		}
		if replay {
			return nil
		}
		if out.Revision != revision {
			return ErrConflict
		}
		amount, e := billing.ParseMoney(out.Amount)
		if e != nil {
			return e
		}
		if amount > 5000000 && !confirmed {
			return ErrConflict
		}
		now := time.Now().UTC()
		command := billing.ChangeRequest{CustomerID: out.CustomerID, RefType: "balance_withdrawal", RefID: out.ID, Channel: "withdrawal", AdminID: admin, AdminName: name, OperatorIP: ip, Notes: reason}
		switch action {
		case "approve":
			if out.Status != "pending" {
				return ErrConflict
			}
			out.Status = "approved"
		case "reject":
			if out.Status != "pending" && out.Status != "approved" {
				return ErrConflict
			}
			command.Type = "withdrawal_release"
			command.Amount = amount.String()
			command.RequestID = fmt.Sprintf("BWR%d", out.ID)
			if _, e = billing.New(tx).Change(ctx, command); e != nil {
				return e
			}
			out.Status = "rejected"
		case "remit":
			if out.Status != "approved" || strings.TrimSpace(transaction) == "" || len(transaction) > 128 || strings.TrimSpace(evidence) == "" || len(evidence) > 380 {
				return ErrConflict
			}
			if e := tx.Table("payment.manual_remittances").Create(map[string]any{"transaction_no": transaction, "source": "balance", "object_id": id, "request_id": request, "created_at": now}).Error; e != nil {
				return e
			}
			command.Type = "withdrawal_release"
			command.Amount = amount.String()
			command.RequestID = fmt.Sprintf("BWR%d", out.ID)
			if _, e = billing.New(tx).Change(ctx, command); e != nil {
				return e
			}
			command.Type = "withdraw"
			command.Amount = (-amount).String()
			command.RequestID = fmt.Sprintf("BWP%d", out.ID)
			if _, e = billing.New(tx).Change(ctx, command); e != nil {
				return e
			}
			out.Status = "paid"
			out.TransactionNo = &transaction
			out.Evidence = evidence
		}
		if e := tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": request, "kind": "withdrawals", "source": "balance", "object_id": id, "action": action, "admin_id": admin, "operator_ip": ip, "notes": reason, "evidence": transaction + "\n" + evidence, "created_at": now}).Error; e != nil {
			return e
		}
		if out.Status == "paid" || out.Status == "rejected" {
			if err := tx.Table("payment.withdrawal_notifications").Create(map[string]any{"withdrawal_id": out.ID, "customer_id": out.CustomerID, "amount": out.Amount, "result": out.Status, "status": "pending", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		out.Reason = reason
		out.Revision++
		out.AdminID = admin
		out.OperatorIP = ip
		out.UpdatedAt = now
		return tx.Save(&out).Error
	})
	return out, e
}

// auditReplay binds a request key to its original actor, object and action.
// A reused key with different inputs must never authorize another mutation.
func auditReplay(tx *gorm.DB, request, kind, source string, id, admin int64, action, notes, evidence string) (bool, error) {
	var row struct {
		Kind, Source, Action, Notes, Evidence string
		ObjectID, AdminID                     int64
	}
	err := tx.Table("system.todo_audits").Where("request_id=?", request).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.Kind != kind || row.Source != source || row.ObjectID != id || row.AdminID != admin || row.Action != action || row.Notes != notes || row.Evidence != evidence {
		return false, ErrConflict
	}
	return true, nil
}
