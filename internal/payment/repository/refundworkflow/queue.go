package refundworkflow

import (
	"crypto/sha256"
	balance "ddyidc/internal/billing/balance"
	payment "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"time"
)

// Queue creates an application only. Callers must lock and verify the source
// object first. No account, ledger, provider, inventory or entitlement is changed.
func Queue(tx *gorm.DB, source string, object, customer, orderID int64, paid balance.Money, reason, key string, applied time.Time, snapshots ...Snapshot) (payment.RefundApplication, error) {
	var out payment.RefundApplication
	reason = strings.TrimSpace(reason)
	if object <= 0 || customer <= 0 || paid <= 0 || paid > balance.MaxMoney || len(key) < 8 || len(key) > 64 || applied.IsZero() || reason == "" || len([]rune(reason)) > 255 {
		return out, ErrInput
	}
	if source != "groupbuy" && source != "receipt" && source != "marketplace" {
		return out, ErrInput
	}
	var req Request
	found := tx.Where("request_id=?", key).Take(&req).Error
	if found == nil {
		if req.Source != source || req.ObjectID != object || req.CustomerID != customer {
			return out, ErrConflict
		}
		if err := tx.First(&out, req.ApplicationID).Error; err != nil {
			return out, err
		}
		var original Snapshot
		if out.OrderID != orderID || out.Reason != reason || json.Unmarshal(req.Snapshot, &original) != nil || original.Paid != paid {
			return out, ErrConflict
		}
		return out, nil
	}
	if !errors.Is(found, gorm.ErrRecordNotFound) {
		return out, found
	}
	var count int64
	if err := tx.Table("payment.todo_refund_requests r").Joins("JOIN payment.payment_refunds a ON a.id=r.application_id").Where("r.source=? AND r.object_id=? AND a.status<>2", source, object).Count(&count).Error; err != nil {
		return out, err
	}
	if count != 0 {
		return out, ErrConflict
	}
	snapshot := Snapshot{Paid: paid, Periods: []Period{{Paid: paid, Unstarted: true}}, Warnings: []string{}}
	if len(snapshots) > 1 {
		return out, ErrInput
	}
	if len(snapshots) == 1 {
		snapshot = snapshots[0]
		if snapshot.Paid != paid || len(snapshot.Periods) == 0 {
			return out, ErrCalculation
		}
	}
	if source == "groupbuy" {
		snapshot.BalancePaid = paid
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256([]byte(key))
	no := "TODO" + hex.EncodeToString(sum[:])[:28]
	now := time.Now().UTC().Truncate(time.Second)
	applied = applied.UTC().Truncate(time.Second)
	out = payment.RefundApplication{ID: snowflake.GenID(), RefundNo: no, OrderID: orderID, UserID: customer, RefundAmount: paid.String(), Reason: reason, Status: 0, CreatedAt: applied, UpdatedAt: now}
	if err := tx.Create(&out).Error; err != nil {
		return out, err
	}
	return out, tx.Create(&Request{ApplicationID: out.ID, Source: source, ObjectID: object, CustomerID: customer, RequestID: key, Snapshot: body, CreatedAt: now}).Error
}

func PendingMarketplace(tx *gorm.DB, id int64) error {
	var count int64
	if err := tx.Table("payment.todo_refund_requests r").Joins("JOIN payment.payment_refunds a ON a.id=r.application_id").Where("r.source='marketplace' AND r.object_id=? AND a.status IN (0,1,3,5,6)", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("交易存在待后台审核的退款申请，暂不能确认收货或结算")
	}
	return nil
}

func PendingGroup(tx *gorm.DB, leader int64) error {
	var count int64
	if err := tx.Table("payment.todo_refund_requests r").Joins("JOIN payment.payment_refunds a ON a.id=r.application_id").Joins("JOIN system.groupbuy_records m ON r.source='groupbuy' AND r.object_id=m.id").Where("m.leader_id=? AND a.status IN (0,1,3,5,6)", leader).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("拼团存在待后台审核的退款申请，暂不能继续拼团或交付")
	}
	return nil
}
