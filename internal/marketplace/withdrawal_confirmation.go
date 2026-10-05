package marketplace

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
)

type operatorIPKey struct{}

func WithOperatorIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, operatorIPKey{}, ip)
}

func (s *Service) ConfirmWithdrawal(ctx context.Context, admin, id int64, name, ip, reason, transaction, evidence, request string, confirmed bool) (*Withdrawal, error) {
	var out Withdrawal
	if admin <= 0 || id <= 0 || name == "" || ip == "" || strings.TrimSpace(reason) == "" || len([]rune(reason)) > 500 || strings.TrimSpace(transaction) == "" || len(transaction) > 128 || strings.TrimSpace(evidence) == "" || len(evidence) > 380 || len(request) < 8 || len(request) > 64 {
		return nil, ErrInvalid
	}
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&out, id).Error; e != nil {
			return e
		}
		var prior struct {
			Source, Action, Notes, Evidence string
			ObjectID, AdminID               int64
		}
		err := tx.Table("system.todo_audits").Where("request_id=?", request).Take(&prior).Error
		if err == nil {
			if prior.Source == "marketplace" && prior.ObjectID == id && prior.AdminID == admin && prior.Action == "remit" && prior.Notes == reason && prior.Evidence == transaction+"\n"+evidence {
				return nil
			}
			return ErrInvalid
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		if out.Status != "approved" {
			return ErrInvalid
		}
		var account int64
		if e := tx.Table("payment.todo_withdrawal_accounts").Where("source='marketplace' AND withdrawal_id=? AND customer_id=? AND ciphertext<>''", id, out.UserID).Count(&account).Error; e != nil {
			return e
		}
		if account != 1 {
			return ErrInvalid
		}
		amount, e := billing.ParseMoney(out.Amount)
		if e != nil || amount <= 0 {
			return ErrInvalid
		}
		fee, e := billing.ParseMoney(out.Fee)
		if e != nil || fee < 0 {
			return ErrInvalid
		}
		net, e := billing.ParseMoney(out.ActualAmount)
		if e != nil || net <= 0 || net+fee != amount {
			return ErrInvalid
		}
		if amount > 5000000 && !confirmed {
			return ErrInvalid
		}
		if e = tx.Table("payment.manual_remittances").Create(map[string]any{"transaction_no": transaction, "source": "marketplace", "object_id": id, "request_id": request, "created_at": s.now()}).Error; e != nil {
			return e
		}
		_, e = billing.New(tx).Change(ctx, billing.ChangeRequest{Scope: "marketplace_wallet", CustomerID: out.UserID, Type: "withdraw", Amount: (-amount).String(), RefType: "marketplace_withdrawal", RefID: id, Channel: "marketplace", RequestID: fmt.Sprintf("MW:withdraw:%d", id), AdminID: admin, AdminName: name, OperatorIP: ip, Notes: reason})
		if e != nil {
			return e
		}
		if e = tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": request, "kind": "withdrawals", "source": "marketplace", "object_id": id, "action": "remit", "admin_id": admin, "operator_ip": ip, "notes": reason, "evidence": transaction + "\n" + evidence, "created_at": s.now()}).Error; e != nil {
			return e
		}
		out.Status = "paid"
		out.Remark = reason
		out.AuditorID = &admin
		out.UpdatedAt = s.now()
		return tx.Save(&out).Error
	})
	return &out, e
}
