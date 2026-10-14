package payment

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/marketplace"
	"ddyidc/internal/order/inventory"
	orderstate "ddyidc/internal/order/repository"
	dom "ddyidc/internal/payment/domain"
	types "ddyidc/internal/payment/types"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net"
	"strings"
	"time"
)

func enqueuePaid(tx *gorm.DB, p *dom.Payment) error {
	return tasks.EnqueuePayment(tx, fmt.Sprintf("payment:%d", p.ID), tasks.PaymentPayload{CustomerID: p.CustomerID, BusinessID: p.BizID, BusinessType: string(p.BizType), Channel: p.Channel, AmountFen: p.AmountFen})
}
func (r *GormRepository) DeliveryTasks(ctx context.Context, page int) ([]tasks.View, error) {
	return tasks.List(ctx, r.db, page)
}
func (r *GormRepository) ReconcileDeliveryTask(ctx context.Context, id, adminID int64, ip, reason string) error {
	return tasks.ReconcileLocal(ctx, r.db, id, adminID, ip, reason)
}

func (r *GormRepository) RecordDeliveryReview(ctx context.Context, id, adminID int64, attempt int, ip, reason string) error {
	return tasks.RecordReview(ctx, r.db, id, adminID, attempt, ip, reason)
}

func (r *GormRepository) ReceiptReviews(ctx context.Context, page int) ([]types.ReceiptReview, error) {
	rows := make([]types.ReceiptReview, 0)
	err := r.db.WithContext(ctx).Table("payment.receipt_reviews r").
		Select("CAST(p.id AS CHAR) AS payment_id,p.payment_no AS pay_no,CAST(p.biz_id AS CHAR) AS business_id,CAST(p.customer_id AS CHAR) AS customer_id,p.channel,p.amount,r.reason,r.status").
		Joins("JOIN payment.payments p ON p.id=r.payment_id").Order("r.created_at DESC,r.payment_id DESC").Offset((page - 1) * 20).Limit(20).Scan(&rows).Error
	return rows, err
}

// Lock the business object before its payment, matching checkout and fulfillment.
// The pre-read supplies immutable identity only; all mutable state is read locked.
func (r *GormRepository) lockPayment(tx *gorm.DB, payment *dom.Payment) error {
	var table string
	switch payment.BizType {
	case dom.BizOrder:
		table = "order.orders"
	case dom.BizRecharge:
		table = "payment.recharge_orders"
	case dom.BizMarketplace:
		table = "system.marketplace_orders"
	default:
		return ErrInvalidState
	}
	var row struct{ ID int64 }
	err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=?", payment.BizID).Take(&row).Error
	// Even a removed business object cannot erase a verified receipt.
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(payment, payment.ID).Error
}

func (r *GormRepository) paymentIdentity(ctx context.Context, id int64) (*dom.Payment, error) {
	var payment dom.Payment
	err := r.db.WithContext(ctx).First(&payment, id).Error
	return &payment, err
}

func recordReceipt(tx *gorm.DB, p *dom.Payment, n dom.VerifiedNotification, paidAt time.Time) error {
	if n.TransactionID == "" || len(n.TransactionID) > 128 || n.AmountFen <= 0 {
		return ErrInvalidState
	}
	// No upsert: a channel transaction cannot be reassigned to another payment.
	return tx.Table("payment.receipts").Create(map[string]any{
		"payment_id": p.ID, "channel": p.Channel, "transaction_id": n.TransactionID,
		"amount_fen": n.AmountFen, "currency": n.Currency, "paid_at": paidAt,
	}).Error
}

func reviewReceipt(tx *gorm.DB, p *dom.Payment, n dom.VerifiedNotification, paidAt time.Time, reason string) error {
	if err := tx.Table("payment.receipt_reviews").Create(map[string]any{"payment_id": p.ID, "reason": reason, "status": "pending"}).Error; err != nil {
		return err
	}
	if err := tx.Model(p).Updates(map[string]any{"status": dom.StatusReview, "transaction_id": n.TransactionID, "notify_raw": n.SafeRaw, "paid_at": paidAt, "updated_at": time.Now()}).Error; err != nil {
		return err
	}
	p.Status, p.TransactionID, p.PaidAt = dom.StatusReview, n.TransactionID, &paidAt
	return nil
}

// ReconcileRechargeReceipt resumes a verified ledger-blocked recharge or order.
// Payment row serializes this decision with receipt refunds; no gateway is called.
func (r *GormRepository) ReconcileRechargeReceipt(ctx context.Context, id, adminID int64, name, ip, reason string, confirmed bool) error {
	reason = strings.TrimSpace(reason)
	if id <= 0 || adminID <= 0 || strings.TrimSpace(name) == "" || net.ParseIP(ip) == nil || reason == "" || len([]rune(reason)) > 256 {
		return ErrInvalidState
	}
	identity, e := r.paymentIdentity(ctx, id)
	if e != nil {
		return e
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p := *identity
		if e := r.lockPayment(tx, &p); e != nil {
			return e
		}
		var review struct{ Reason, Status string }
		if e := tx.Table("payment.receipt_reviews").Clauses(clause.Locking{Strength: "UPDATE"}).Where("payment_id=?", id).Take(&review).Error; e != nil {
			return e
		}
		external := (p.BizType == dom.BizOrder || p.BizType == dom.BizMarketplace) && review.Reason == "external_balance_ledger_mismatch"
		if !external && (p.BizType != dom.BizRecharge || review.Reason != "balance_ledger_mismatch") {
			return ErrInvalidState
		}
		if external {
			var allowed int64
			if e := tx.Table("system.admins").Where("id=? AND status=1 AND is_super=1", adminID).Count(&allowed).Error; e != nil {
				return e
			}
			if allowed != 1 {
				return ErrForbidden
			}
		}
		if p.Status == dom.StatusPaid && review.Status == "credited" {
			return nil
		}
		if p.Status != dom.StatusReview || review.Status != "pending" || p.PaidAt == nil || p.TransactionID == "" || p.Currency != "CNY" {
			return ErrInvalidState
		}
		money, e := balance.ParseMoney(p.Amount)
		if e != nil || int64(money) != p.AmountFen || money <= 0 || money > 5000000 && !confirmed {
			return ErrInvalidState
		}
		var verified int64
		if e := tx.Table("payment.receipts").Where("payment_id=? AND channel=? AND transaction_id=? AND amount_fen=? AND currency=?", id, p.Channel, p.TransactionID, p.AmountFen, p.Currency).Count(&verified).Error; e != nil {
			return e
		}
		if verified != 1 {
			return ErrInvalidState
		}
		var conflicting int64
		if e := tx.Table("payment.original_refunds").Where("payment_id=?", id).Count(&conflicting).Error; e != nil {
			return e
		}
		if conflicting > 0 {
			return ErrInvalidState
		}
		if e := tx.Table("payment.todo_refund_requests r").Joins("JOIN payment.payment_refunds a ON a.id=r.application_id").Where("r.source='receipt' AND r.object_id=? AND a.status<>2", id).Count(&conflicting).Error; e != nil {
			return e
		}
		if conflicting > 0 {
			return ErrInvalidState
		}
		now := time.Now()
		if external {
			if p.BizType == dom.BizMarketplace {
				if e := marketplace.CompletePaidOrderTx(tx, p.BizID, p.CustomerID, *p.PaidAt, p.Channel); e != nil {
					return e
				}
			} else {
				changed, e := orderstate.AcceptPaymentTx(tx, p.BizID, p.CustomerID, p.AmountFen, channelPayType(p.Channel), *p.PaidAt)
				if e != nil {
					return e
				}
				if !changed {
					return ErrInvalidState
				}
				if e := inventory.Consume(tx, p.BizID); e != nil {
					return e
				}
			}
		} else {
			changed := tx.Table("payment.recharge_orders").Where("id=? AND customer_id=? AND status=1 AND amount=?", p.BizID, p.CustomerID, p.AmountFen).Updates(map[string]any{"status": 2, "channel": p.Channel, "paid_at": p.PaidAt, "updated_at": now})
			if changed.Error != nil {
				return changed.Error
			}
			if changed.RowsAffected != 1 {
				return ErrInvalidState
			}
		}
		if e := tx.Model(&p).Updates(map[string]any{"status": dom.StatusPaid, "updated_at": now}).Error; e != nil {
			return e
		}
		p.Status = dom.StatusPaid
		action := "resume_verified_recharge"
		if external {
			action = "resume_verified_order_payment"
			if e := settleExternalPayment(tx, &p, externalLedgerActor{adminID, name, ip}); e != nil {
				return e
			}
		} else if e := creditRecharge(tx, &p, *p.PaidAt); e != nil {
			return e
		}
		if e := tx.Table("payment.receipt_reviews").Where("payment_id=? AND status='pending'", id).Updates(map[string]any{"status": "credited", "resolved_at": now}).Error; e != nil {
			return e
		}
		details, _ := json.Marshal(map[string]any{"paymentId": id, "reason": reason, "action": action})
		if e := tx.Table("customer.admin_customer_audits").Create(map[string]any{"id": snowflake.GenID(), "customer_id": p.CustomerID, "admin_id": adminID, "admin_name": name, "action": "receipt_reconcile", "fields": string(details), "operator_ip": ip, "created_at": now}).Error; e != nil {
			return e
		}
		return enqueuePaid(tx, &p)
	})
}
