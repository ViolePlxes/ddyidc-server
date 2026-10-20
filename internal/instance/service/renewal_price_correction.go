package instance

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	order "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/renewalquote"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) SetRenewalPriceCorrector(fn func(context.Context, *gorm.DB, int64, int64, int64) (int64, error)) {
	s.renewalPriceCorrector = fn
}
func (s *Service) correctUnpaidRenewal(ctx context.Context, before Renewal, h dom.Host, q RenewalOption) error {
	if s.renewalPriceCorrector == nil {
		return renewalquote.Unverified()
	}
	gross, e := balance.ParseMoney(q.Amount)
	if e != nil || gross <= 0 || q.Cycle != before.Cycle || q.Duration <= 0 {
		return renewalquote.Unverified()
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var o order.Order
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", before.OrderID, before.CustomerID).Take(&o).Error; e != nil {
			return e
		}
		var current Renewal
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", before.ID).Take(&current).Error; e != nil {
			return e
		}
		var host dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", h.ID).Take(&host).Error; e != nil {
			return e
		}
		if host.Status == 6 || host.DeletedAt.Valid || current.CustomerID != before.CustomerID || o.Status != 1 || current.State != "unpaid" || current.InvoiceID != "" || current.Cycle != before.Cycle || current.HostID != before.HostID || current.SupplierID != before.SupplierID || current.OwnershipVersion != before.OwnershipVersion || !current.BaseExpiry.Equal(before.BaseExpiry) || renewalIntentRevision(host, q) != renewalIntentRevision(h, q) {
			return renewalquote.Unverified()
		}
		if o.TotalAmount == int64(gross) && current.Cost == q.Cost && current.Duration == q.Duration {
			return nil
		}
		net, e := s.renewalPriceCorrector(ctx, tx, before.CustomerID, before.OrderID, int64(gross))
		if e != nil {
			return e
		}
		return tx.Model(&current).Updates(map[string]any{"amount": balance.Money(net).String(), "cost": q.Cost.String(), "duration": q.Duration}).Error
	})
}
