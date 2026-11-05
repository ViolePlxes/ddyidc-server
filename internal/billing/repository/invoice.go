package billing

import (
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/billing/domain"
	typ "ddyidc/internal/billing/types"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func EnabledTx(tx *gorm.DB) error {
	var row struct{ Enabled bool }
	if e := tx.Table("billing.invoice_settings").Select("enabled").Where("id=1").Take(&row).Error; e != nil {
		return e
	}
	if !row.Enabled {
		return typ.ErrDisabled
	}
	return nil
}
func FindInvoiceTx(tx *gorm.DB, id string, customer int64, lock bool) (dom.Invoice, error) {
	var out dom.Invoice
	q := tx.Where("id=? AND customer_id=?", id, customer)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if e := q.Take(&out).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return out, typ.ErrNotFound
		}
		return out, e
	}
	return out, nil
}

// ReadSourceTx is an adapter to the platform's original business objects. It
// always locks the original source before a caller can lock an invoice.
func ReadSourceTx(tx *gorm.DB, kind string, id, user int64) (typ.Source, error) {
	var s typ.Source
	if id <= 0 || user <= 0 {
		return s, typ.ErrNotFound
	}
	s.Kind, s.ID, s.CustomerID = kind, id, user
	if kind == "marketplace" {
		var platform int64
		if e := tx.Table("order.orders").Where("id=? AND customer_id=?", id, user).Count(&platform).Error; e != nil {
			return s, e
		}
		if platform == 1 {
			return ReadSourceTx(tx, "order", id, user)
		}
	}
	switch kind {
	case "order":
		var row struct {
			ID, CustomerID, PayAmount int64
			OrderNo                   string
			Status                    uint8
			CreatedAt, UpdatedAt      time.Time
			PaidAt                    *time.Time
		}
		e := tx.Table("order.orders").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, user).Take(&row).Error
		if e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return s, typ.ErrNotFound
			}
			return s, e
		}
		s.Number, s.Amount, s.CreatedAt, s.UpdatedAt, s.PaidAt = row.OrderNo, balance.Money(row.PayAmount), row.CreatedAt, row.UpdatedAt, row.PaidAt
		s.Status = map[uint8]string{1: "open", 2: "paid", 3: "void", 4: "refunded", 5: "partial_refund", 6: "paid"}[row.Status]
		var items []struct {
			ID                      int64
			ProductName, ModuleType string
			Qty                     uint
			UnitPrice, Subtotal     int64
		}
		if e = tx.Table("order.order_items").Where("order_id=?", id).Order("id ASC").Find(&items).Error; e != nil {
			return s, e
		}
		if len(items) == 0 {
			return s, typ.ErrConflict
		}
		s.Title = items[0].ProductName
		for _, v := range items {
			item := v
			s.Items = append(s.Items, typ.Line{SourceItemID: &item.ID, Description: item.ProductName, ModuleType: item.ModuleType, Quantity: &item.Qty, UnitAmount: balance.Money(item.UnitPrice), Amount: balance.Money(item.Subtotal)})
		}
	case "recharge":
		var row struct {
			ID, CustomerID, Amount int64
			RechargeNo             string
			Status                 uint8
			CreatedAt, UpdatedAt   time.Time
			PaidAt                 *time.Time
		}
		e := tx.Table("payment.recharge_orders").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, user).Take(&row).Error
		if e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return s, typ.ErrNotFound
			}
			return s, e
		}
		s.Number, s.Amount, s.CreatedAt, s.UpdatedAt, s.PaidAt = row.RechargeNo, balance.Money(row.Amount), row.CreatedAt, row.UpdatedAt, row.PaidAt
		s.Status = map[uint8]string{1: "open", 2: "paid", 3: "failed", 4: "void"}[row.Status]
		s.Title = "账户充值"
		qty := uint(1)
		s.Items = []typ.Line{{Description: s.Title, ModuleType: "recharge", Quantity: &qty, UnitAmount: s.Amount, Amount: s.Amount}}
	case "groupbuy":
		var row struct {
			ID, ClientID, PayAmount int64
			Status                  uint8
			CreatedAt, UpdatedAt    time.Time
		}
		e := tx.Table("system.groupbuy_records").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND client_id=?", id, user).Take(&row).Error
		if e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return s, typ.ErrNotFound
			}
			return s, e
		}
		s.Number, s.Title, s.Amount, s.CreatedAt, s.UpdatedAt = fmt.Sprint("GB", id), "云服务器拼团", balance.Money(row.PayAmount), row.CreatedAt, row.UpdatedAt
		s.Status = "open"
		var proof struct {
			Count  int64
			PaidAt *time.Time
		}
		if e = tx.Table("billing.transactions").Select("COUNT(*) count,MIN(created_at) paid_at").Where("customer_id=? AND related_type='groupbuy' AND related_id=? AND type='order_pay' AND channel='groupbuy' AND amount=?", user, id, -s.Amount).Scan(&proof).Error; e != nil {
			return s, e
		}
		if proof.Count == 1 || s.Amount == 0 {
			s.Status = "paid"
			s.PaidAt = proof.PaidAt
		}
		if row.Status == 3 {
			if s.Status == "paid" {
				s.Status = "refunded"
			} else {
				s.Status = "review"
			}
		}
		qty := uint(1)
		s.Items = []typ.Line{{Description: s.Title, ModuleType: "groupbuy", Quantity: &qty, UnitAmount: s.Amount, Amount: s.Amount}}
	case "marketplace":
		var row struct {
			ID, BuyerID                int64
			OrderNo, PayAmount, Status string
			CreatedAt, UpdatedAt       time.Time
			PaidAt                     *time.Time
		}
		e := tx.Table("system.marketplace_orders").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND buyer_id=?", id, user).Take(&row).Error
		if e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return s, typ.ErrNotFound
			}
			return s, e
		}
		s.Amount, e = balance.ParseMoney(row.PayAmount)
		if e != nil {
			return s, e
		}
		s.Number, s.CreatedAt, s.UpdatedAt, s.PaidAt = row.OrderNo, row.CreatedAt, row.UpdatedAt, row.PaidAt
		switch row.Status {
		case "pending_pay":
			s.Status = "open"
		case "paid", "settled", "refund_pending":
			s.Status = "paid"
		case "cancelled", "expired":
			s.Status = "void"
		case "refunded":
			s.Status = "refunded"
		default:
			s.Status = "review"
		}
		s.Title = "资源交易"
		qty := uint(1)
		s.Items = []typ.Line{{Description: s.Title, ModuleType: "marketplace", Quantity: &qty, UnitAmount: s.Amount, Amount: s.Amount}}
	default:
		return s, typ.ErrNotFound
	}
	if s.Status == "" || s.Amount < 0 || s.Amount > balance.MaxMoney {
		return s, typ.ErrConflict
	}
	return s, nil
}
