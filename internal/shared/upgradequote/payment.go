package upgradequote

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"encoding/json"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// CheckTx checks frozen retail evidence inside the existing order payment
// transaction. It performs no external requests and never changes balances.
func CheckTx(ctx context.Context, tx *gorm.DB, user, orderID, expectedAmount int64, at time.Time) error {
	var items []struct {
		ConfigSnapshot      string
		ProductID, Subtotal int64
		InstanceID          *int64
	}
	if e := tx.WithContext(ctx).Table("order.order_items").Where("order_id=? AND module_type='instance_upgrade'", orderID).Find(&items).Error; e != nil {
		return e
	}
	if len(items) == 0 {
		return nil
	}
	if len(items) != 1 {
		return ErrRequote
	}
	var payload struct {
		Pricing *Basis `json:"pricing"`
	}
	if json.Unmarshal([]byte(items[0].ConfigSnapshot), &payload) != nil || payload.Pricing == nil {
		return ErrRequote
	}
	b := payload.Pricing
	var row struct {
		ID, InstanceID, CustomerID, OwnershipVersion, SupplierID, SourceProductID, TargetProductID int64
		HostID, Cycle, Amount, Cost, State                                                         string
		CreatedAt, ExpiresAt                                                                       time.Time
	}
	if e := tx.Table("instance.instance_upgrades").Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_id=? AND customer_id=?", orderID, user).Take(&row).Error; e != nil {
		return ErrRequote
	}
	amount, e := b.Amount()
	if e != nil {
		return e
	}
	stored, e := balance.ParseMoney(row.Amount)
	if e != nil {
		return ErrRequote
	}
	cost, e := balance.ParseMoney(row.Cost)
	if e != nil || cost <= 0 || amount < cost {
		return ErrRequote
	}
	if b.Version != Version || b.CustomerID != user || row.InstanceID != b.InstanceID || row.OwnershipVersion != b.OwnershipVersion || row.SupplierID != b.SupplierID || row.HostID == "" || row.Cycle != b.Cycle || row.SourceProductID != b.SourceProductID || row.TargetProductID != b.TargetProductID || row.State != "unpaid" || !at.Before(row.ExpiresAt) || row.CreatedAt.Unix() != b.QuotedAt || int64(amount) != expectedAmount || stored != amount || items[0].Subtotal != expectedAmount || items[0].ProductID != b.TargetProductID || items[0].InstanceID == nil || *items[0].InstanceID != b.InstanceID {
		return ErrRequote
	}
	var host struct {
		ID, CustomerID, OwnershipVersion, ProductID int64
		UpstreamProviderID                          *int64
		UpstreamInstanceID                          string
		Status                                      uint8
		ExpiresAt, DeletedAt                        *time.Time
	}
	if e := tx.Table("instance.instances").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", b.InstanceID).Take(&host).Error; e != nil {
		return ErrRequote
	}
	if host.CustomerID != user || host.OwnershipVersion != b.OwnershipVersion || host.ProductID != b.LocalProductID || host.UpstreamProviderID == nil || *host.UpstreamProviderID != b.SupplierID || host.UpstreamInstanceID != row.HostID || host.Status == 6 || host.DeletedAt != nil || host.ExpiresAt == nil || host.ExpiresAt.Unix() != b.DueAt {
		return ErrRequote
	}
	return nil
}
