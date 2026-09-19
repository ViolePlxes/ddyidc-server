package marketplace

import (
	"context"
	"ddyidc/internal/pushtransfer"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type OriginalRefunder interface {
	RefundMarketplace(context.Context, int64) error
}

func (s *Service) SetRefunder(r OriginalRefunder) { s.refunder = r }

func (s *Service) ensureTransferUpgrade(ctx context.Context) error {
	db := s.db.WithContext(ctx)
	for _, column := range []struct{ table, name string }{{"marketplace_orders", "status"}, {"marketplace_flows", "type"}} {
		var kind string
		if err := db.Raw("SELECT DATA_TYPE FROM information_schema.columns WHERE table_schema='system' AND table_name=? AND column_name=?", column.table, column.name).Scan(&kind).Error; err != nil {
			return err
		}
		if kind == "enum" {
			if err := db.Exec("ALTER TABLE system." + column.table + " MODIFY " + column.name + " VARCHAR(32) NOT NULL").Error; err != nil {
				return err
			}
		}
	}
	for _, column := range []struct{ table, name, definition string }{
		{"marketplace_settings", "auto_confirm_hours", "INT NOT NULL DEFAULT 24"},
		{"marketplace_orders", "settlement_mode", "VARCHAR(24) NOT NULL DEFAULT 'legacy'"},
		{"marketplace_orders", "operator_ip", "VARCHAR(64) NOT NULL DEFAULT ''"},
		{"marketplace_listings", "risk_flag", "VARCHAR(100) NOT NULL DEFAULT ''"},
	} {
		var n int64
		if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='system' AND table_name=? AND column_name=?", column.table, column.name).Scan(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			if err := db.Exec("ALTER TABLE system." + column.table + " ADD COLUMN " + column.name + " " + column.definition).Error; err != nil {
				return err
			}
		}
	}
	// Preserve the existing administrator's switch on upgrade; a fresh install defaults on.
	if err := db.Exec("INSERT IGNORE INTO system.system_configs (id,`key`,value,description) SELECT ?,'marketplace.enabled',IF(enabled,'true','false'),'内置交易市场开关' FROM system.marketplace_settings ORDER BY id LIMIT 1", snowflake.GenID()).Error; err != nil {
		return err
	}
	return nil
}
func (s *Service) requireOpen(ctx context.Context) error {
	enabled, err := s.Enabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return errors.New("功能未开放")
	}
	return nil
}
func (s *Service) ConfirmReceipt(ctx context.Context, user, id int64) (*Order, error) {
	var row Order
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND buyer_id=?", id, user).Take(&row).Error; err != nil {
			return ErrNotFound
		}
		if row.Status == "settled" {
			return nil
		}
		if row.Status != "paid" {
			return ErrInvalid
		}
		if err := settleOrderTx(tx, &row, s.now()); err != nil {
			return err
		}
		row.Status = "settled"
		return nil
	})
	return &row, err
}
func (s *Service) retryRefunds(ctx context.Context) error {
	if s.refunder == nil {
		return nil
	}
	var ids []int64
	if err := s.db.WithContext(ctx).Table(ordersTable).Where("status='refund_pending'").Order("updated_at").Limit(50).Pluck("id", &ids).Error; err != nil {
		return err
	}
	var first error
	for _, id := range ids {
		if err := s.refunder.RefundMarketplace(ctx, id); err != nil && first == nil {
			first = fmt.Errorf("市场原路退款待重试，订单 %d", id)
		}
	}
	return first
}
func completeOwnership(tx *gorm.DB, o *Order, l *Listing, refund bool) error {
	from, to, source := o.SellerID, o.BuyerID, "marketplace"
	if refund {
		from, to, source = to, from, "marketplace_refund"
	}
	ip := o.OperatorIP
	if ip == "" {
		ip = "system"
	}
	_, err := pushtransfer.ChangeOwnerTx(tx, pushtransfer.Change{InstanceID: l.ProductID, From: from, To: to, MarketOrderID: o.ID, ListingID: l.ID, Source: source, Key: fmt.Sprintf("%s:%d", source, o.ID), IP: ip})
	return err
}

// Queued refund state is durable. Never credit a captured online payment to local balance.
func queueFailedRefund(tx *gorm.DB, id, buyer int64, at time.Time) error {
	var row Order
	if err := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND buyer_id=?", id, buyer).Take(&row).Error; err != nil {
		return err
	}
	if row.Status == "refund_pending" || row.Status == "refunded" {
		return nil
	}
	if row.Status != "pending_pay" && row.Status != "cancelled" {
		return ErrInvalid
	}
	if err := tx.Table(ordersTable).Where("id=?", id).Updates(map[string]any{"status": "refund_pending", "paid_at": at, "updated_at": at}).Error; err != nil {
		return err
	}
	if row.SettlementMode == "balance" {
		if err := tx.Table("order.orders").Where("id=?", id).Updates(map[string]any{"status": 2, "paid_at": at, "updated_at": at}).Error; err != nil {
			return err
		}
	}
	var listing Listing
	if err := tx.Table(listingsTable).Where("id=?", row.ListingID).Take(&listing).Error; err != nil {
		return err
	}
	record := pushtransfer.Record{ID: snowflake.GenID(), InstanceID: listing.ProductID, FromCustomerID: row.SellerID, ToCustomerID: buyer, MarketOrderID: id, RequestKey: fmt.Sprintf("marketplace_failed:%d", id), Source: "marketplace", Status: "failed", OperatorIP: row.OperatorIP, CreatedAt: at, UpdatedAt: at}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error; err != nil {
		return err
	}
	// Keep the listing unavailable until the original payment channel confirms its refund.
	return tx.Table(listingsTable).Where("id=? AND status IN ('locked','on_sale')", row.ListingID).Update("status", "off_shelf").Error
}
