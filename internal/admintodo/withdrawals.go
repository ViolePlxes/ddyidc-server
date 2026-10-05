package admintodo

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/crypto"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

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
	var out BalanceWithdrawal
	money, e := billing.ParseMoney(amount)
	if e != nil || money <= 0 || customer <= 0 || len(request) < 8 || len(request) > 64 || strings.TrimSpace(account) == "" || len(account) > 256 {
		return out, ErrQuery
	}
	encrypted, e := crypto.Encrypt(account, key)
	if e != nil {
		return out, e
	}
	e = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if policy != "free" {
			return ErrConflict
		}
		err := tx.Where("request_id=?", request).Take(&out).Error
		if err == nil {
			original, e := crypto.Decrypt(out.AccountNote, key)
			if e != nil || out.CustomerID != customer || out.Amount != money.String() || original != account {
				return ErrConflict
			}
			return nil
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		now := time.Now().UTC()
		out = BalanceWithdrawal{ID: snowflake.GenID(), RequestID: request, CustomerID: customer, Amount: money.String(), AccountNote: encrypted, Status: "pending", Revision: 1, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&out).Error; e != nil {
			return e
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
		out.Reason = reason
		out.Revision++
		out.AdminID = admin
		out.OperatorIP = ip
		out.UpdatedAt = now
		return tx.Save(&out).Error
	})
	return out, e
}
