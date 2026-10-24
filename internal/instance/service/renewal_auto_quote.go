package instance

import (
	"context"
	"crypto/sha256"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/notification"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type AutoRenewalQuote struct {
	InstanceID                   int64 `gorm:"primaryKey"`
	CustomerID, OwnershipVersion int64
	QuoteRevision, Amount, Cycle string
	UpdatedAt                    time.Time
}

func (AutoRenewalQuote) TableName() string { return "instance.instance_renewal_auto_quotes" }

type RenewalQuoteAlert struct {
	ID, CustomerID, InstanceID                       int64
	Fingerprint, Reason, OldAmount, NewAmount, State string
	CreatedAt                                        time.Time
}

func (RenewalQuoteAlert) TableName() string { return "instance.instance_renewal_quote_alerts" }
func (s *Service) approveAutoRenewalTx(tx *gorm.DB, h dom.Host, quote RenewalOption) error {
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "instance_id"}}, DoUpdates: clause.AssignmentColumns([]string{"customer_id", "ownership_version", "quote_revision", "amount", "cycle", "updated_at"})}).Create(&AutoRenewalQuote{InstanceID: h.ID, CustomerID: h.CustomerID, OwnershipVersion: h.OwnershipVersion, QuoteRevision: autoRenewalApprovalRevision(h, quote), Amount: quote.Amount, Cycle: quote.Cycle, UpdatedAt: time.Now()}).Error
}

// Automatic renewal authorizes the selected product/configuration/cycle, while
// its amount follows the current quote. Never re-enable a disabled setting or
// carry an approval across an ownership/configuration change.
func (s *Service) refreshAutoRenewalQuote(ctx context.Context, h dom.Host, q RenewalOption) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current dom.Host
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND ownership_version=?", h.ID, h.CustomerID, h.OwnershipVersion).Take(&current).Error; err != nil {
			return err
		}
		if current.Status == 6 || current.DeletedAt.Valid || renewalIntentRevision(current, q) != renewalIntentRevision(h, q) {
			return errors.New("产品配置已变化，请刷新后重试")
		}
		var settings RenewalSettings
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=? AND customer_id=? AND enabled=1", h.ID, h.CustomerID).Take(&settings).Error; err != nil {
			return errors.New("自动续费已关闭，未扣款")
		}
		var before AutoRenewalQuote
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=?", h.ID).Take(&before).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			old := q
			old.Amount = before.Amount
			if before.CustomerID != h.CustomerID || before.OwnershipVersion != h.OwnershipVersion || before.Cycle != q.Cycle || before.QuoteRevision != autoRenewalApprovalRevision(h, old) {
				return errors.New("自动续费配置已变化，请重新开启")
			}
		}
		return s.approveAutoRenewalTx(tx, current, q)
	})
}
func (s *Service) autoRenewalQuoteAllowed(ctx context.Context, h dom.Host, cycle string) bool {
	var approval AutoRenewalQuote
	if e := s.db.WithContext(ctx).Where("instance_id=? AND customer_id=? AND ownership_version=?", h.ID, h.CustomerID, h.OwnershipVersion).Take(&approval).Error; e != nil {
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return false
		}
	}
	options, e := s.RenewalOptions(ctx, h.CustomerID, h.ID)
	if e != nil {
		s.pauseAutoRenewalQuote(ctx, h, "quote_unverified", approval.Amount, "", approval.QuoteRevision)
		return false
	}
	for _, q := range options {
		if q.Cycle == cycle {
			if s.refreshAutoRenewalQuote(ctx, h, q) == nil {
				return true
			}
			s.pauseAutoRenewalQuote(ctx, h, "quote_changed", approval.Amount, q.Amount, approval.QuoteRevision)
			return false
		}
	}
	s.pauseAutoRenewalQuote(ctx, h, "cycle_changed", approval.Amount, "", approval.QuoteRevision)
	return false
}
func (s *Service) pauseAutoRenewalQuote(ctx context.Context, h dom.Host, reason, oldAmount, newAmount, expectedRevision string) {
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s:%s:%s", h.OwnershipVersion, reason, oldAmount, newAmount, expectedRevision))))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Match owner/version atomically; never disable the new owner's preferences.
		var current dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND ownership_version=?", h.ID, h.CustomerID, h.OwnershipVersion).Take(&current).Error; e != nil {
			return e
		}

		var latest AutoRenewalQuote
		readErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=? AND customer_id=?", h.ID, h.CustomerID).Take(&latest).Error
		if expectedRevision == "" {
			if readErr == nil {
				return nil
			}
			if readErr != gorm.ErrRecordNotFound {
				return readErr
			}
		} else {
			if readErr != nil {
				return readErr
			}
			if latest.QuoteRevision != expectedRevision {
				return nil
			}
		}
		if e := tx.Model(&RenewalSettings{}).Where("instance_id=? AND customer_id=? AND enabled=1", h.ID, h.CustomerID).Updates(map[string]any{"enabled": false, "updated_at": time.Now()}).Error; e != nil {
			return e
		}
		alert := RenewalQuoteAlert{ID: snowflake.GenID(), CustomerID: h.CustomerID, InstanceID: h.ID, Fingerprint: fingerprint, Reason: reason, OldAmount: oldAmount, NewAmount: newAmount, State: "pending", CreatedAt: time.Now()}
		if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&alert).Error; e != nil {
			return e
		}
		content := "自动续费已暂停：续费价格或配置需要重新核验，请进入产品详情核对后重新开启。"
		if reason == "price_changed" {
			content = fmt.Sprintf("自动续费已暂停：原确认金额%s元，最新报价%s元。请核对后重新开启，系统不会自动按新价格扣款。", oldAmount, newAmount)
		}
		return notification.EnqueueRenewalPause(tx, notification.Event{Key: fmt.Sprintf("renewal_quote:%d:%s", h.ID, fingerprint), RecipientID: h.CustomerID, Type: "billing", Scene: "renewal_quote_paused", Title: "自动续费已暂停", Summary: content, Content: content, BusinessType: "instance", BusinessID: fmt.Sprint(h.ID), OwnershipVersion: h.OwnershipVersion})
	})
	notification.Wake()
	// Failed persistence remains retryable by the existing bounded scheduler.
	if err != nil {
		return
	}
}
func (s *Service) AutoRenewalWarning(ctx context.Context, user, id int64) string {
	var settings RenewalSettings
	if s.db.WithContext(ctx).Where("instance_id=? AND customer_id=?", id, user).Take(&settings).Error != nil || settings.Enabled {
		return ""
	}
	var alert RenewalQuoteAlert
	if s.db.WithContext(ctx).Where("instance_id=? AND customer_id=?", id, user).Order("created_at DESC").Take(&alert).Error != nil {
		return ""
	}
	if alert.Reason == "price_changed" {
		return "自动续费已暂停：报价已变化，请核对最新价格后重新开启"
	}
	return "自动续费已暂停：续费价格需要重新核验，请手动确认"
}

// Supplier cost/discount changes do not change the customer's approved retail.
// Procurement independently verifies the actual cost when creating/paying an order.
func autoRenewalApprovalRevision(h dom.Host, q RenewalOption) string {
	q.Cost = 0
	q.ExchangeRate = ""
	// The user chooses the automatic cycle independently of a manual renewal.
	// Completing that cycle must not invalidate its own recurring approval.
	var snapshot map[string]json.RawMessage
	if json.Unmarshal([]byte(h.ConfigSnapshot), &snapshot) == nil && snapshot != nil {
		value, _ := json.Marshal(q.Cycle)
		snapshot["billingCycle"] = value
		raw, _ := json.Marshal(snapshot)
		h.ConfigSnapshot = string(raw)
	}
	return renewalIntentRevision(h, q)
}
