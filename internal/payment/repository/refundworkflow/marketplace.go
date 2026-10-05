package refundworkflow

import (
	balance "ddyidc/internal/billing/balance"
	orderstate "ddyidc/internal/order/repository"
	"ddyidc/internal/pushtransfer"
	"ddyidc/internal/shared/tasks"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type marketplaceFacts struct {
	ID, BuyerID, SellerID, ListingID int64
	PayAmount, SellerIncome          balance.Money
	Status, SettlementMode           string
}

func marketplaceOrder(tx *gorm.DB, r Request) (marketplaceFacts, error) {
	var m marketplaceFacts
	err := tx.Table("system.marketplace_orders").Where("id=? AND buyer_id=?", r.ObjectID, r.CustomerID).Take(&m).Error
	return m, err
}
func verifyMarketplace(tx *gorm.DB, r Request, s Snapshot) error {
	m, err := marketplaceOrder(tx, r)
	if err != nil {
		return ErrConflict
	}
	if m.PayAmount != s.Paid || len(s.Periods) != 1 {
		return ErrConflict
	}
	var legacy int64
	if err := tx.Table("payment.original_refunds f").Joins("JOIN payment.payments p ON p.id=f.payment_id").Where("p.biz_type='marketplace' AND p.biz_id=?", m.ID).Count(&legacy).Error; err != nil {
		return err
	}
	if legacy > 0 {
		return ErrConflict
	}
	if m.Status == "refund_pending" && s.Periods[0].Unstarted {
		var failed int64
		if err := tx.Model(&pushtransfer.Record{}).Where("market_order_id=? AND to_customer_id=? AND status='failed'", m.ID, m.BuyerID).Count(&failed).Error; err != nil {
			return err
		}
		if failed != 1 {
			return ErrEntitlement
		}
		var income int64
		if err := tx.Table("system.marketplace_flows").Where("ref_id=? AND user_id=? AND type='seller_pending'", m.ID, m.SellerID).Count(&income).Error; err != nil {
			return err
		}
		if income != 0 {
			return ErrEntitlement
		}
		return nil
	}
	if m.Status != "paid" && m.Status != "settled" {
		return ErrConflict
	}
	if m.Status == "settled" && m.SettlementMode == "balance" {
		return ErrEntitlement
	}
	p := s.Periods[0]
	if p.Unstarted || p.Start == nil || p.End == nil || p.InstanceID <= 0 {
		return ErrCalculation
	}
	var h struct {
		ID, CustomerID, OwnershipVersion int64
		Status                           uint8
		ExpiresAt                        *time.Time
	}
	if err := tx.Table("instance.instances").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", p.InstanceID).Take(&h).Error; err != nil {
		return ErrConflict
	}
	if h.CustomerID != m.BuyerID || h.OwnershipVersion != p.OwnershipVersion || h.ExpiresAt == nil || !h.ExpiresAt.Equal(*p.End) || h.Status == 6 {
		return ErrConflict
	}
	var listing struct{ ProductID int64 }
	if err := tx.Table("system.marketplace_listings").Where("id=?", m.ListingID).Take(&listing).Error; err != nil {
		return err
	}
	if listing.ProductID != h.ID {
		return ErrConflict
	}
	return nil
}

func finalizeMarketplace(tx *gorm.DB, r Request, calc Calculation, now time.Time, actor Actor, confirmLarge bool) error {
	m, err := marketplaceOrder(tx, r)
	if err != nil {
		return err
	}
	if m.Status != "refund_pending" {
		if calc.Refund > m.SellerIncome {
			return ErrCalculation
		}
		change := func(typ string, amount balance.Money) error {
			if amount == 0 {
				return nil
			}
			_, err := balance.New(tx).Change(tx.Statement.Context, balance.ChangeRequest{Scope: "marketplace_wallet", CustomerID: m.SellerID, Type: typ, Amount: amount.String(), RefType: "marketplace", RefID: m.ID, Channel: "marketplace", RequestID: fmt.Sprintf("MW:%s:%d", typ, r.ApplicationID), AdminID: actor.ID, AdminName: actor.Name, OperatorIP: actor.IP, Notes: "后台审核退款：按核算结果处理卖家资金", LargeAdjustmentAllowed: confirmLarge})
			return err
		}
		if err := change("refund_reversed", -calc.Refund); err != nil {
			return err
		}
		retained := m.SellerIncome - calc.Refund
		if m.Status == "paid" {
			typ := "refund_retained"
			if m.SettlementMode == "balance" {
				typ = "refund_retained_to_balance"
			}
			if err := change(typ, retained); err != nil {
				return err
			}
			if m.SettlementMode == "balance" && retained > 0 {
				_, err := balance.New(tx).Change(tx.Statement.Context, balance.ChangeRequest{CustomerID: m.SellerID, Type: "marketplace_income", Amount: retained.String(), RefType: "marketplace_refund_retained", RefID: r.ApplicationID, Channel: "marketplace", RequestID: fmt.Sprintf("MRI:%d", r.ApplicationID), AdminID: actor.ID, AdminName: actor.Name, OperatorIP: actor.IP, Notes: "后台审核退款后结算已使用期间收入", LargeAdjustmentAllowed: confirmLarge})
				if err != nil {
					return err
				}
			}
		}
		var listing struct{ ProductID int64 }
		if err := tx.Table("system.marketplace_listings").Where("id=?", m.ListingID).Take(&listing).Error; err != nil {
			return err
		}
		if _, err := pushtransfer.ChangeOwnerTx(tx, pushtransfer.Change{InstanceID: listing.ProductID, From: m.BuyerID, To: m.SellerID, MarketOrderID: m.ID, ListingID: m.ListingID, Source: "marketplace_refund", Key: fmt.Sprintf("marketplace_refund:%d", m.ID), IP: actor.IP}); err != nil {
			return err
		}
	}
	if m.SettlementMode == "balance" {
		if _, err := orderstate.CompleteReviewedModuleRefundTx(tx, m.ID, m.BuyerID, r.ApplicationID, now); err != nil {
			return err
		}
	}
	if err := tx.Table("system.marketplace_orders").Where("id=? AND buyer_id=?", m.ID, m.BuyerID).Updates(map[string]any{"status": "refunded", "updated_at": now}).Error; err != nil {
		return err
	}
	if err := tx.Table("system.marketplace_listings").Where("id=?", m.ListingID).Updates(map[string]any{"status": "refunded", "updated_at": now}).Error; err != nil {
		return err
	}
	if err := tx.Table("payment.payments").Where("biz_type='marketplace' AND biz_id=? AND status IN ('paid','received_review')", m.ID).Updates(map[string]any{"status": "refunded", "updated_at": now}).Error; err != nil {
		return err
	}
	for _, customer := range []int64{m.BuyerID, m.SellerID} {
		if err := tasks.EnqueueNotice(tx, tasks.NoticePayload{CustomerID: customer, BusinessID: m.ID, Scene: "marketplace_refund", Params: map[string]string{"refund_amount": calc.Refund.String()}}); err != nil {
			return err
		}
	}
	return nil
}
