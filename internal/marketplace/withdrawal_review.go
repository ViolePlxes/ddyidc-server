package marketplace

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

func (s *Service) ReviewWithdrawal(ctx context.Context, admin, id int64, approved bool, reason, request string, expected time.Time, confirmed bool) (*Withdrawal, error) {
	if admin <= 0 || id <= 0 || strings.TrimSpace(reason) == "" || len([]rune(reason)) > 500 || len(request) < 8 || len(request) > 64 || expected.IsZero() {
		return nil, ErrInvalid
	}
	ip, _ := ctx.Value(operatorIPKey{}).(string)
	if ip == "" {
		return nil, ErrInvalid
	}
	action := "reject"
	if approved {
		action = "approve"
	}
	var out Withdrawal
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&out, id).Error; e != nil {
			return e
		}
		var audit struct {
			Source, Action, Notes string
			ObjectID, AdminID     int64
		}
		err := tx.Table("system.todo_audits").Where("request_id=?", request).Take(&audit).Error
		if err == nil {
			if audit.Source == "marketplace" && audit.ObjectID == id && audit.AdminID == admin && audit.Action == action && audit.Notes == reason {
				return nil
			}
			return ErrInvalid
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		if out.Status != "pending" || !out.UpdatedAt.Equal(expected) {
			return ErrInvalid
		}
		amount, e := billing.ParseMoney(out.Amount)
		if e != nil {
			return e
		}
		if amount > 5000000 && !confirmed {
			return ErrInvalid
		}
		scoped := *s
		scoped.db = tx
		result, e := scoped.AuditWithdrawal(ctx, admin, id, approved, reason)
		if e != nil {
			return e
		}
		out = *result
		return tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": request, "kind": "withdrawals", "source": "marketplace", "object_id": id, "action": action, "admin_id": admin, "operator_ip": ip, "notes": reason, "created_at": s.now()}).Error
	})
	return &out, e
}
