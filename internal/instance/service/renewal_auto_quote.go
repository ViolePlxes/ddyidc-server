package instance

import (
	"context"
	"crypto/sha256"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/shared/snowflake"
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
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "instance_id"}}, DoUpdates: clause.AssignmentColumns([]string{"customer_id", "ownership_version", "quote_revision", "amount", "cycle", "updated_at"})}).Create(&AutoRenewalQuote{InstanceID: h.ID, CustomerID: h.CustomerID, OwnershipVersion: h.OwnershipVersion, QuoteRevision: autoRenewalApprovalRevision(h,quote), Amount: quote.Amount, Cycle: quote.Cycle, UpdatedAt: time.Now()}).Error
}
func (s *Service) autoRenewalQuoteAllowed(ctx context.Context, h dom.Host, cycle string) bool {
	var approval AutoRenewalQuote
	if e := s.db.WithContext(ctx).Where("instance_id=? AND customer_id=? AND ownership_version=?", h.ID, h.CustomerID, h.OwnershipVersion).Take(&approval).Error; e != nil {
		if e == gorm.ErrRecordNotFound {
			s.pauseAutoRenewalQuote(ctx, h, "approval_required", "", "", "")
		}
		return false
	}
	options, e := s.RenewalOptions(ctx, h.CustomerID, h.ID)
	if e != nil {
		s.pauseAutoRenewalQuote(ctx, h, "quote_unverified", approval.Amount, "", approval.QuoteRevision)
		return false
	}
	for _, q := range options {
		if q.Cycle == cycle {
			if approval.Cycle == cycle && approval.QuoteRevision == autoRenewalApprovalRevision(h,q) {
				return true
			}
			reason:="quote_changed";if approval.Amount!=q.Amount{reason="price_changed"}
 s.pauseAutoRenewalQuote(ctx, h, reason, approval.Amount, q.Amount, approval.QuoteRevision)
			return false
		}
	}
	s.pauseAutoRenewalQuote(ctx, h, "cycle_changed", approval.Amount, "", approval.QuoteRevision)
	return false
}
func (s *Service) pauseAutoRenewalQuote(ctx context.Context, h dom.Host, reason, oldAmount, newAmount, expectedRevision string) {
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s:%s:%s", h.OwnershipVersion, reason, oldAmount, newAmount,expectedRevision))))
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
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&alert).Error
	})
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
func autoRenewalApprovalRevision(h dom.Host,q RenewalOption)string{
 q.Cost=0;q.ExchangeRate=""
 return renewalIntentRevision(h,q)
}
