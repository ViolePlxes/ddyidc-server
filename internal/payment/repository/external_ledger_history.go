package payment

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net"
	"strings"
	"time"
)

// BackfillExternalLedger only appends a verified, zero-net pair. It neither
// replays fulfillment nor changes timestamps or balances of old transactions.
// It is explicit and audited; startup and ordinary GET requests never call it.
func (r *GormRepository) BackfillExternalLedger(ctx context.Context, id, adminID int64, name, ip, reason string) (bool, error) {
	if id <= 0 || adminID <= 0 || strings.TrimSpace(name) == "" || net.ParseIP(ip) == nil || strings.TrimSpace(reason) == "" || len([]rune(reason)) > 256 {
		return false, ErrInvalidState
	}
	var authorized int64
	if e := r.db.WithContext(ctx).Table("system.admins").Where("id=? AND status=1 AND is_super=1", adminID).Count(&authorized).Error; e != nil {
		return false, e
	}
	if authorized != 1 {
		return false, ErrForbidden
	}
	identity, e := r.paymentIdentity(ctx, id)
	if e != nil {
		return false, e
	}
	changed := false
	e = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p := *identity
		if e := r.lockPayment(tx, &p); e != nil {
			return e
		}
		if p.Status != dom.StatusPaid || p.BizType != dom.BizOrder || p.PaidAt == nil {
			return ErrInvalidState
		}
		var valid int64
		if e := tx.Table("order.orders").Where("id=? AND customer_id=? AND status IN (2,6) AND pay_amount=? AND balance_amount=0", p.BizID, p.CustomerID, p.AmountFen).Count(&valid).Error; e != nil {
			return e
		}
		if valid != 1 {
			return ErrInvalidState
		}
		var pair int64
		if e := tx.Table("billing.transactions").Where("customer_id=? AND related_type='external_payment' AND related_id=?", p.CustomerID, p.ID).Count(&pair).Error; e != nil {
			return e
		}
		if pair == 2 {
			return verifyExternalPair(tx, &p)
		}
		if pair != 0 {
			return errors.New("incomplete external payment ledger requires review")
		}
		if e := settleExternalPayment(tx, &p, externalLedgerActor{adminID, name, ip}); e != nil {
			return e
		}
		fields, err := json.Marshal(map[string]any{"paymentId": p.ID, "action": "append_verified_external_pair", "originalPaidAt": p.PaidAt.Format(time.RFC3339), "reason": strings.TrimSpace(reason)})
		if err != nil {
			return err
		}
		if e := tx.Table("customer.admin_customer_audits").Create(map[string]any{"id": snowflake.GenID(), "customer_id": p.CustomerID, "admin_id": adminID, "admin_name": name, "action": "external_payment_ledger_backfill", "fields": string(fields), "operator_ip": ip, "created_at": time.Now()}).Error; e != nil {
			return e
		}
		changed = true
		return nil
	})
	return changed, e
}

func verifyExternalPair(tx *gorm.DB, p *dom.Payment) error {
	var a balance.Account
	if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=?", p.CustomerID).Take(&a).Error; e != nil {
		return e
	}
	if e := balance.VerifyAccountLedger(tx, a); e != nil {
		return e
	}
	var rows []balance.Transaction
	if e := tx.Where("customer_id=? AND related_type='external_payment' AND related_id=?", p.CustomerID, p.ID).Order("account_version").Find(&rows).Error; e != nil {
		return e
	}
	if len(rows) != 2 {
		return ErrInvalidState
	}
	c, d := rows[0], rows[1]
	if c.TxNo != fmt.Sprintf("EPC%d", p.ID) || d.TxNo != fmt.Sprintf("EPD%d", p.ID) || c.Type != "recharge" || d.Type != "order_pay" || int64(c.Amount) != p.AmountFen || d.Amount != -c.Amount || c.BalanceBefore != d.BalanceAfter || c.BalanceAfter != d.BalanceBefore || c.AccountVersion+1 != d.AccountVersion {
		return ErrInvalidState
	}
	return nil
}
