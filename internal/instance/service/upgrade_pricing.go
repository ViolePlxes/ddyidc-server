package instance

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/shared/upgradequote"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strconv"
	"strings"
	"time"
)

type upgradeRetailProduct struct {
	ID                                   int64
	RemoteID, Name, Cycle, Amount, Basis string
}

func upgradeRetailCycle(tx *gorm.DB, p upgradeRetailProduct, cycle string) (string, error) {
	if strings.ToLower(p.Basis) != "standard" {
		return "", errors.New("升降配完整售价未确认，请先核对商品售价")
	}
	if p.Cycle == cycle {
		money, e := balance.ParseMoney(p.Amount)
		if e == nil && money > 0 {
			return money.String(), nil
		}
	}
	months := upgradequote.Months(cycle)
	if months == 0 {
		return "", upgradequote.ErrRequote
	}
	var rows []struct{ Price string }
	if e := tx.Table("product.product_prices").Select("price").Where("product_id=? AND duration_months=? AND status=1 AND (config_snapshot IS NULL OR JSON_LENGTH(config_snapshot)=0)", p.ID, months).Find(&rows).Error; e != nil {
		return "", e
	}
	if len(rows) != 1 {
		return "", errors.New("该周期完整套餐售价未确认，不能以代理价或倍乘月价代替")
	}
	price, e := balance.ParseMoney(rows[0].Price)
	if e != nil || price <= 0 {
		return "", upgradequote.ErrRequote
	}
	return price.String(), nil
}

// Resolve the ACTUAL current remote product (including a manual supplier-side
// upgrade), without changing local product bindings or historical order data.
func upgradeRetailBasis(ctx context.Context, tx *gorm.DB, h *dom.Host, detail *contracts.InstanceDetail, target int64, cycle string, at time.Time) (upgradequote.Basis, error) {
	var b upgradequote.Basis
	if h.UpstreamProviderID == nil || detail == nil || detail.ID != h.UpstreamInstanceID || detail.ProductID == "" || detail.BillingCycle != cycle || detail.ExpiresAt == nil || h.ExpiresAt == nil || detail.ExpiresAt.Unix() != h.ExpiresAt.Unix() || normalizePower(detail.Status) == "provisioning" || unavailableLifecycle(detail, h.UpstreamInstanceID) != "" {
		return b, errors.New("当前套餐、周期或到期时间未确认，请先刷新实例信息")
	}
	var source, targetRows []upgradeRetailProduct
	base := func() *gorm.DB {
		return tx.WithContext(ctx).Table("product.upstream_products u").Select("p.id,CAST(u.upstream_product_id AS CHAR) remote_id,p.name,u.cycle,CAST(u.price AS CHAR) amount,u.price_basis basis").Joins("JOIN product.products p ON p.id=u.product_id").Where("u.supplier_id=? AND p.tenant_id=? AND p.status=1 AND p.hidden=0 AND u.hidden=0", h.UpstreamProviderID, h.TenantID)
	}
	if e := base().Where("CAST(u.upstream_product_id AS CHAR)=?", detail.ProductID).Find(&source).Error; e != nil {
		return b, e
	}
	if e := base().Where("p.id=?", target).Find(&targetRows).Error; e != nil {
		return b, e
	}
	if len(source) != 1 || len(targetRows) != 1 || source[0].ID == target {
		return b, errors.New("实际套餐与本平台商品未唯一匹配，不能核算差价")
	}
	oldPrice, e := upgradeRetailCycle(tx, source[0], cycle)
	if e != nil {
		return b, e
	}
	newPrice, e := upgradeRetailCycle(tx, targetRows[0], cycle)
	if e != nil {
		return b, e
	}
	b = upgradequote.Basis{Version: upgradequote.Version, InstanceID: h.ID, CustomerID: h.CustomerID, OwnershipVersion: h.OwnershipVersion, SupplierID: *h.UpstreamProviderID, LocalProductID: h.ProductID, SourceProductID: source[0].ID, TargetProductID: target, SourceRemoteID: detail.ProductID, TargetRemoteID: targetRows[0].RemoteID, SourceName: source[0].Name, Cycle: cycle, SourcePrice: oldPrice, TargetPrice: newPrice, QuotedAt: at.Unix(), DueAt: detail.ExpiresAt.Unix()}
	_, e = b.Amount()
	return b, e
}

func (s *Service) verifyUpgradeRetailRow(ctx context.Context, tx *gorm.DB, h *dom.Host, row Upgrade) (upgradequote.Basis, error) {
	p, w := s.providerContext(ctx, h)
	if w != "" {
		return upgradequote.Basis{}, ErrUnsupported
	}
	real, e := p.GetInstanceDetail(parseHostID(h.UpstreamInstanceID))
	if e != nil {
		return upgradequote.Basis{}, e
	}
	b, e := upgradeRetailBasis(ctx, tx, h, real, row.TargetProductID, row.Cycle, row.CreatedAt)
	if e != nil {
		return b, e
	}
	amount, e := b.Amount()
	if e != nil || b.SourceProductID != row.SourceProductID || b.TargetRemoteID != row.RemoteProductID || amount != row.Amount || row.Amount < row.Cost {
		return b, upgradequote.ErrRequote
	}
	return b, nil
}

func fillUpgradePricing(view *UpgradeView, b upgradequote.Basis) {
	old, _ := balance.ParseMoney(b.SourcePrice)
	newPrice, _ := balance.ParseMoney(b.TargetPrice)
	view.SourceName = b.SourceName
	view.SourceUnitPrice = b.SourcePrice
	view.TargetUnitPrice = b.TargetPrice
	view.FullCycleDifference = (newPrice - old).String()
	view.RemainingSeconds = b.DueAt - b.QuotedAt
	view.CycleSeconds, _ = upgradequote.PeriodSeconds(b.Cycle, time.Unix(b.QuotedAt, 0))
}

func (s *Service) upgradeSourceUnchanged(ctx context.Context, row Upgrade, real *contracts.InstanceDetail) error {
	var raw string
	if e := s.db.WithContext(ctx).Table("order.order_items").Select("config_snapshot").Where("order_id=? AND module_type='instance_upgrade'", row.OrderID).Scan(&raw).Error; e != nil {
		return e
	}
	var payload struct {
		Pricing *upgradequote.Basis `json:"pricing"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil || payload.Pricing == nil {
		return upgradequote.ErrRequote
	}
	b := payload.Pricing
	if real == nil || real.ID != row.HostID || real.ProductID != b.SourceRemoteID || real.BillingCycle != row.Cycle || real.ExpiresAt == nil || real.ExpiresAt.Unix() != b.DueAt {
		return upgradequote.ErrRequote
	}
	if _, e := strconv.ParseInt(real.ProductID, 10, 64); e != nil {
		return fmt.Errorf("actual upgrade source product invalid")
	}
	return nil
}
