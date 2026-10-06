package instance

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	order "ddyidc/internal/order/domain"
	orderstate "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/pointsevents"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/outbound"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"time"
)

type Upgrade struct {
	ID                                                                                              int64 `gorm:"primaryKey" json:"id,string"`
	OrderID, InstanceID, CustomerID, OwnershipVersion, SupplierID, SourceProductID, TargetProductID int64 `json:"-"`
	HostID, RemoteProductID, TargetName, Cycle                                                      string
	Amount, Cost                                                                                    balance.Money
	RequestID                                                                                       string `json:"-"`
	InvoiceID, State                                                                                string
	ExpiresAt, CreatedAt, UpdatedAt                                                                 time.Time
}

func (Upgrade) TableName() string { return "instance.instance_upgrades" }

type UpgradePlanView struct {
	ProductID string                   `json:"productId"`
	Name      string                   `json:"name"`
	Cycles    []contracts.UpgradeCycle `json:"cycles"`
}
type UpgradeInput struct {
	ProductID string `json:"productId"`
	Cycle     string `json:"cycle"`
	RequestID string `json:"requestId"`
	QuoteID   string `json:"quoteId"`
}
type UpgradeView struct {
	ID        string `json:"id"`
	ProductID string `json:"productId"`
	Name      string `json:"name"`
	Cycle     string `json:"cycle"`
	Amount    string `json:"amount"`
	State     string `json:"state"`
	ExpiresAt string `json:"expiresAt"`
	OrderID   string `json:"orderId,omitempty"`
	OrderNo   string `json:"orderNo,omitempty"`
}

func upgradeView(row Upgrade) UpgradeView {
	out := UpgradeView{ID: fmt.Sprint(row.ID), ProductID: fmt.Sprint(row.TargetProductID), Name: row.TargetName, Cycle: row.Cycle, Amount: row.Amount.String(), State: row.State, ExpiresAt: formatTime(row.ExpiresAt)}
	if row.State != "quoted" {
		out.OrderID = fmt.Sprint(row.OrderID)
	}
	return out
}
func (s *Service) UpgradePlans(ctx context.Context, customer, id int64) ([]UpgradePlanView, error) {
	if s.db == nil {
		return nil, ErrUnsupported
	}
	host, e := s.owned(ctx, customer, id)
	if e != nil {
		return nil, e
	}
	if host.Status == 6 {
		return nil, ErrUnsupported
	}
	provider, w := s.providerContext(ctx, host)
	if w != "" {
		return nil, ErrUnsupported
	}
	up, ok := provider.(contracts.UpgradeProvider)
	if !ok {
		return nil, ErrUnsupported
	}
	plans, e := up.UpgradePlans(parseHostID(host.UpstreamInstanceID))
	if e != nil {
		return nil, e
	}
	var products []struct {
		ID       int64
		Name     string
		RemoteID string
	}
	if e = s.db.WithContext(ctx).Table("product.products p").Select("p.id,p.name,CAST(u.upstream_product_id AS CHAR) remote_id").Joins("JOIN product.upstream_products u ON u.product_id=p.id").Where("u.supplier_id=? AND u.hidden=0 AND p.status=1 AND p.hidden=0 AND p.tenant_id=?", host.UpstreamProviderID, host.TenantID).Find(&products).Error; e != nil {
		return nil, e
	}
	out := []UpgradePlanView{}
	for _, plan := range plans {
		for _, product := range products {
			if product.ID != host.ProductID && product.RemoteID == plan.ProductID {
				out = append(out, UpgradePlanView{ProductID: fmt.Sprint(product.ID), Name: product.Name, Cycles: plan.Cycles})
			}
		}
	}
	return out, nil
}
func (s *Service) QuoteUpgrade(ctx context.Context, customer, id int64, in UpgradeInput) (UpgradeView, error) {
	var row Upgrade
	if s.db == nil || outbound.DetailReadOnly() {
		return UpgradeView{}, ErrUnsupported
	}
	target, e := strconv.ParseInt(in.ProductID, 10, 64)
	if e != nil || target <= 0 || in.Cycle == "" || len(in.RequestID) < 8 || len(in.RequestID) > 64 {
		return UpgradeView{}, errors.New("升级报价参数无效")
	}
	plans, e := s.UpgradePlans(ctx, customer, id)
	if e != nil {
		return UpgradeView{}, e
	}
	allowed := false
	for _, plan := range plans {
		if plan.ProductID == in.ProductID {
			for _, cycle := range plan.Cycles {
				if cycle.ID == in.Cycle {
					allowed = true
				}
			}
		}
	}
	if !allowed {
		return UpgradeView{}, ErrUnsupported
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var host dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, customer).Take(&host).Error; e != nil {
			return ErrForbidden
		}
		if host.Status == 6 {
			return ErrUnsupported
		}
		if e := s.refundMutationAllowed(ctx, tx, id); e != nil {
			return e
		}
		if e := s.refundHostUnlocked(tx, id); e != nil {
			return e
		}
		var existing []Upgrade
		if e := tx.Where("instance_id=? AND state NOT IN ('completed','cancelled')", id).Find(&existing).Error; e != nil {
			return e
		}
		for _, previous := range existing {

			if previous.State == "unpaid" {
				var status uint8
				if e := tx.Table("`order`.orders").Select("status").Where("id=? AND customer_id=?", previous.OrderID, customer).Scan(&status).Error; e != nil {
					return e
				}
				if status == order.OrderStatusCancelled {
					if e := tx.Model(&previous).Update("state", "cancelled").Error; e != nil {
						return e
					}
					continue
				}
			}
			if previous.State == "quoted" && time.Now().After(previous.ExpiresAt) {
				if e := tx.Model(&previous).Update("state", "cancelled").Error; e != nil {
					return e
				}
				continue
			}
			if previous.CustomerID == customer && previous.TargetProductID == target && previous.Cycle == in.Cycle && previous.OwnershipVersion == host.OwnershipVersion {
				row = previous
				return nil
			}
			return errors.New("已有未结束的升降配报价或订单，请先核查")
		}
		var product struct{ RemoteID, Rate, Name string }
		if e := tx.Raw("SELECT CAST(u.upstream_product_id AS CHAR) remote_id,s.rate,p.name FROM product.upstream_products u JOIN product.products p ON p.id=u.product_id JOIN common.suppliers s ON s.id=u.supplier_id WHERE p.id=? AND s.id=? AND p.status=1 AND u.hidden=0", target, host.UpstreamProviderID).Take(&product).Error; e != nil {
			return e
		}
		provider, w := s.providerContext(ctx, &host)
		if w != "" {
			return ErrUnsupported
		}
		up, ok := provider.(contracts.UpgradeProvider)
		if !ok {
			return ErrUnsupported
		}
		real, e := provider.GetInstanceDetail(parseHostID(host.UpstreamInstanceID))
		if e != nil || real == nil || real.ID != host.UpstreamInstanceID || unavailableLifecycle(real, host.UpstreamInstanceID) != "" {
			return ErrUnsupported
		}
		quote, e := up.QuoteUpgrade(parseHostID(host.UpstreamInstanceID), product.RemoteID, in.Cycle)
		if e != nil {
			return e
		}
		cost, e := balance.ParseMoney(quote.Cost)
		if e != nil || cost <= 0 {
			return errors.New("升级成本未确认")
		}
		original, e := balance.ParseMoney(quote.OriginalAmount)
		if e != nil {
			return e
		}
		amount, e := retailRenewal(original, product.Rate, "0", 0)
		if e != nil || amount < cost {
			return errors.New("升级售价不能低于已确认成本")
		}
		now := time.Now().UTC()
		row = Upgrade{ID: snowflake.GenID(), OrderID: snowflake.GenID(), InstanceID: id, CustomerID: customer, OwnershipVersion: host.OwnershipVersion, SupplierID: *host.UpstreamProviderID, HostID: host.UpstreamInstanceID, SourceProductID: host.ProductID, TargetProductID: target, RemoteProductID: product.RemoteID, TargetName: product.Name, Cycle: in.Cycle, Amount: amount, Cost: cost, RequestID: in.RequestID, State: "quoted", ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now, UpdatedAt: now}
		return tx.Create(&row).Error
	})
	return upgradeView(row), e
}
func (s *Service) CreateUpgradeOrder(ctx context.Context, customer, id int64, in UpgradeInput) (UpgradeView, error) {
	var row Upgrade
	quoteID, e := strconv.ParseInt(in.QuoteID, 10, 64)
	if e != nil || quoteID <= 0 || s.db == nil || outbound.DetailReadOnly() {
		return UpgradeView{}, ErrUnsupported
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var host dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, customer).Take(&host).Error; e != nil {
			return ErrForbidden
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND instance_id=? AND customer_id=?", quoteID, id, customer).Take(&row).Error; e != nil {
			return ErrForbidden
		}
		if row.State == "unpaid" {
			return nil
		}
		if row.State != "quoted" || time.Now().After(row.ExpiresAt) || host.Status == 6 || host.OwnershipVersion != row.OwnershipVersion || host.UpstreamProviderID == nil || *host.UpstreamProviderID != row.SupplierID || host.UpstreamInstanceID != row.HostID {
			return errors.New("升级报价已过期或机器状态已变化")
		}
		if e := s.refundMutationAllowed(ctx, tx, id); e != nil {
			return e
		}
		if e := s.refundHostUnlocked(tx, id); e != nil {
			return e
		}
		now := time.Now().UTC()
		o := order.Order{ID: row.OrderID, OrderNo: fmt.Sprintf("UPG%d", row.ID), CustomerID: customer, TenantID: host.TenantID, TotalAmount: int64(row.Amount), PayAmount: int64(row.Amount), Status: 1, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&o).Error; e != nil {
			return e
		}
		snapshot, _ := json.Marshal(map[string]any{"type": "instance_upgrade", "instanceId": fmt.Sprint(id), "targetProductId": fmt.Sprint(row.TargetProductID), "billingCycle": row.Cycle})
		item := order.OrderItem{OrderID: o.ID, ProductID: row.TargetProductID, ProductName: row.TargetName + "（升降配）", ModuleType: "instance_upgrade", ConfigSnapshot: string(snapshot), Qty: 1, UnitPrice: int64(row.Amount), Subtotal: int64(row.Amount), InstanceID: &id, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&item).Error; e != nil {
			return e
		}
		row.State = "unpaid"
		row.UpdatedAt = now
		return tx.Save(&row).Error
	})
	out := upgradeView(row)
	if e == nil {
		out.OrderNo = fmt.Sprintf("UPG%d", row.ID)
	}
	return out, e
}
func (s *Service) PayUpgradeBalance(ctx context.Context, customer, id, orderID int64, ip string) error {
	if s.db == nil || outbound.DetailReadOnly() {
		return ErrUnsupported
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var o order.Order
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", orderID, customer).Take(&o).Error; e != nil {
			return ErrForbidden
		}
		var row Upgrade
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_id=? AND instance_id=? AND customer_id=?", orderID, id, customer).Take(&row).Error; e != nil {
			return ErrForbidden
		}
		if o.Status == 2 || o.Status == 6 {
			return nil
		}
		if o.Status != 1 || row.State != "unpaid" || o.PayAmount != int64(row.Amount) {
			return ErrUnsupported
		}
		var h dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&h, id).Error; e != nil {
			return e
		}
		if h.CustomerID != customer || h.OwnershipVersion != row.OwnershipVersion || h.UpstreamProviderID == nil || *h.UpstreamProviderID != row.SupplierID || h.UpstreamInstanceID != row.HostID || h.Status == 6 {
			return ErrForbidden
		}
		if e := s.refundMutationAllowed(ctx, tx, id); e != nil {
			return e
		}
		if e := s.refundHostUnlocked(tx, id); e != nil {
			return e
		}
		var pending int64
		if e := tx.Table("payment.payments").Where("biz_type='order' AND biz_id=? AND status='pending'", orderID).Count(&pending).Error; e != nil {
			return e
		}
		if pending > 0 {
			return errors.New("请先关闭正在进行的在线支付单")
		}
		if _, e := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: customer, Type: "instance_upgrade_pay", Amount: (-row.Amount).String(), RefType: "instance_upgrade", RefID: row.ID, Channel: "instance_upgrade", RequestID: fmt.Sprintf("upgrade-balance:%d", row.ID), Notes: "实例升降配订单余额支付"}); e != nil {
			return e
		}
		changed, e := orderstate.AcceptBalancePaymentTx(tx, orderID, customer, int64(row.Amount), time.Now())
		if e != nil {
			return e
		}
		if !changed {
			return ErrUnsupported
		}
		return tasks.EnqueuePayment(tx, fmt.Sprintf("upgrade-balance:%d", row.ID), tasks.PaymentPayload{CustomerID: customer, BusinessID: orderID, BusinessType: "order", Channel: "balance", AmountFen: int64(row.Amount)})
	})
}
func (s *Service) ProvisionUpgrade(ctx context.Context, customer, orderID int64, method string) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var row Upgrade
	e := s.db.WithContext(ctx).Where("order_id=?", orderID).Take(&row).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if e != nil {
		return true, e
	}
	if row.CustomerID != customer {
		return true, ErrForbidden
	}
	if row.State == "completed" {
		return true, nil
	}
	var paid order.Order
	if e = s.db.WithContext(ctx).First(&paid, orderID).Error; e != nil {
		return true, e
	}
	if paid.CustomerID != customer || paid.PayAmount != int64(row.Amount) || (paid.Status != 2 && paid.Status != 6) {
		return true, ErrUnsupported
	}
	host, e := s.owned(ctx, customer, row.InstanceID)
	if e != nil {
		return true, e
	}
	if host.Status == 6 || host.OwnershipVersion != row.OwnershipVersion || host.UpstreamProviderID == nil || *host.UpstreamProviderID != row.SupplierID || host.UpstreamInstanceID != row.HostID {
		return true, ErrForbidden
	}
	if e = s.refundMutationAllowed(ctx, s.db, host.ID); e != nil {
		return true, e
	}
	provider, w := s.providerContext(ctx, host)
	if w != "" {
		return true, ErrUnsupported
	}
	up, ok := provider.(contracts.UpgradeProvider)
	if !ok {
		return true, ErrUnsupported
	}
	if row.State == "verifying" {
		detail, e := provider.GetInstanceDetail(parseHostID(row.HostID))
		if e == nil && detail != nil && detail.ID == row.HostID && detail.ProductID == row.RemoteProductID {
			return true, s.completeUpgrade(ctx, row, detail)
		}
		return true, errors.New("已支付，等待配置变更完成")
	}

	if row.State == "uncertain" && row.InvoiceID != "" {
		proof, ok := provider.(interface {
			ConfirmUpgradeInvoice(int64, string, string) (bool, error)
		})
		if !ok {
			return true, ErrUnsupported
		}
		confirmed, e := proof.ConfirmUpgradeInvoice(parseHostID(row.HostID), row.InvoiceID, row.Cost.String())
		if e != nil || !confirmed {
			return true, errors.New("升级付款尚未确认，禁止重复付款")
		}
		detail, e := provider.GetInstanceDetail(parseHostID(row.HostID))
		if e != nil || detail == nil || detail.ID != row.HostID || detail.ProductID != row.RemoteProductID {
			return true, errors.New("升级配置尚未确认")
		}
		claim := s.db.WithContext(ctx).Model(&Upgrade{}).Where("id=? AND state='uncertain' AND invoice_id=?", row.ID, row.InvoiceID).Updates(map[string]any{"state": "verifying", "updated_at": time.Now().UTC()})
		if claim.Error != nil {
			return true, claim.Error
		}
		if claim.RowsAffected != 1 {
			return true, ErrUnsupported
		}
		row.State = "verifying"
		return true, s.completeUpgrade(ctx, row, detail)
	}
	if row.State != "unpaid" {
		return true, errors.New("升级结果待核对，禁止重复创建账单或付款")
	}
	claim := s.db.WithContext(ctx).Model(&Upgrade{}).Where("id=? AND state='unpaid'", row.ID).Updates(map[string]any{"state": "creating", "updated_at": time.Now()})
	if claim.Error != nil {
		return true, claim.Error
	}
	if claim.RowsAffected != 1 {
		return true, nil
	}
	var invoice, amount string
	e = s.withUpgradeHost(ctx, row, func() error {
		var err error
		invoice, amount, err = up.CreateUpgradeInvoice(parseHostID(row.HostID), row.RemoteProductID, row.Cycle, row.Cost.String())
		return err
	})
	if invoice != "" {
		if save := s.db.WithContext(ctx).Model(&Upgrade{}).Where("id=? AND state='creating'", row.ID).Updates(map[string]any{"invoice_id": invoice, "updated_at": time.Now()}); save.Error != nil {
			return true, save.Error
		}
	}
	actualCost, costErr := balance.ParseMoney(amount)
	if e != nil || costErr != nil || actualCost <= 0 || actualCost > row.Cost {
		_ = s.db.WithContext(ctx).Model(&Upgrade{}).Where("id=? AND state='creating'", row.ID).Update("state", "uncertain").Error
		return true, errors.New("升级账单或金额待核对，未重复采购或付款")
	}
	if e = s.db.WithContext(ctx).Model(&Upgrade{}).Where("id=? AND state='creating'", row.ID).Update("cost", actualCost.String()).Error; e != nil {
		return true, e
	}
	row.Cost = actualCost
	claim = s.db.WithContext(ctx).Model(&Upgrade{}).Where("id=? AND state='creating' AND invoice_id=?", row.ID, invoice).Updates(map[string]any{"state": "paying", "updated_at": time.Now()})
	if claim.Error != nil {
		return true, claim.Error
	}
	if claim.RowsAffected != 1 {
		return true, ErrUnsupported
	}
	if e = s.withUpgradeHost(ctx, row, func() error { return up.PayUpgradeInvoice(invoice) }); e != nil {
		_ = s.db.WithContext(ctx).Model(&Upgrade{}).Where("id=? AND state='paying'", row.ID).Update("state", "uncertain").Error
		return true, errors.New("升级付款结果待核对，禁止再次付款")
	}
	claim = s.db.WithContext(ctx).Model(&Upgrade{}).Where("id=? AND state='paying'", row.ID).Updates(map[string]any{"state": "verifying", "updated_at": time.Now()})
	if claim.Error != nil {
		return true, claim.Error
	}
	if claim.RowsAffected != 1 {
		return true, ErrUnsupported
	}
	row.State = "verifying"
	row.InvoiceID = invoice
	detail, e := provider.GetInstanceDetail(parseHostID(row.HostID))
	if e != nil || detail == nil || detail.ID != row.HostID || detail.ProductID != row.RemoteProductID {
		return true, errors.New("已支付，等待配置变更完成")
	}
	return true, s.completeUpgrade(ctx, row, detail)
}

// The durable upgrade reservation is committed before a remote write. The host
// lock then fences changes of ownership until the bounded supplier call ends.
func (s *Service) withUpgradeHost(ctx context.Context, row Upgrade, call func() error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var host dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&host, row.InstanceID).Error; e != nil {
			return e
		}
		if host.CustomerID != row.CustomerID || host.OwnershipVersion != row.OwnershipVersion || host.UpstreamProviderID == nil || *host.UpstreamProviderID != row.SupplierID || host.UpstreamInstanceID != row.HostID || host.Status == 6 {
			return ErrForbidden
		}
		if e := s.refundMutationAllowed(ctx, tx, host.ID); e != nil {
			return e
		}
		if e := s.refundHostUnlocked(tx, host.ID); e != nil {
			return e
		}
		return call()
	})
}

func (s *Service) upgradeMutationAllowed(ctx context.Context, tx *gorm.DB, id int64) error {
	if tx == nil {
		return nil
	}
	var count int64
	if e := tx.WithContext(ctx).Model(&Upgrade{}).Where("instance_id=? AND state NOT IN ('completed','cancelled') AND NOT (state='unpaid' AND order_id IN (SELECT id FROM `order`.orders WHERE status=3)) AND NOT (state='quoted' AND expires_at<?)", id, time.Now()).Count(&count).Error; e != nil {
		return e
	}
	if count > 0 {
		return errors.New("实例存在未结束的升降配订单，暂不能变更服务权益")
	}
	return nil
}

// Reconciliation only reads supplier state. A timeout must never cause another
// checkout or payment; payment delivery retries use the same durable claim.
func (s *Service) StartUpgradeMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = distsync.Work(ctx, func(ctx context.Context) error {
				var rows []Upgrade
				if e := s.db.WithContext(ctx).Where("state IN ('verifying','uncertain') AND invoice_id<>'' AND updated_at<=?", time.Now().Add(-time.Minute)).Order("updated_at").Limit(20).Find(&rows).Error; e != nil {
					return e
				}
				for _, row := range rows {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					job, cancel := context.WithTimeout(ctx, 30*time.Second)
					claim := s.db.WithContext(job).Model(&Upgrade{}).Where("id=? AND state=? AND updated_at=?", row.ID, row.State, row.UpdatedAt).Update("updated_at", time.Now().UTC())
					if claim.Error == nil && claim.RowsAffected == 1 {
						_, _ = s.ProvisionUpgrade(job, row.CustomerID, row.OrderID, "reconcile")
					}
					cancel()
				}
				return nil
			})
		}
	}
}
func (s *Service) completeUpgrade(ctx context.Context, row Upgrade, detail *contracts.InstanceDetail) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var paid order.Order
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&paid, row.OrderID).Error; e != nil {
			return e
		}
		if paid.CustomerID != row.CustomerID || (paid.Status != 2 && paid.Status != 6) {
			return ErrForbidden
		}
		var current Upgrade
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, row.ID).Error; e != nil {
			return e
		}
		if current.State == "completed" {
			return nil
		}
		if current.State != "verifying" || current.InvoiceID == "" {
			return ErrUnsupported
		}
		var host dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&host, row.InstanceID).Error; e != nil {
			return e
		}
		if host.CustomerID != row.CustomerID || host.OwnershipVersion != row.OwnershipVersion || host.UpstreamProviderID == nil || *host.UpstreamProviderID != row.SupplierID || host.UpstreamInstanceID != row.HostID || detail.ID != row.HostID || detail.ProductID != row.RemoteProductID || host.Status == 6 {
			return ErrForbidden
		}
		snapshot := snapshotFromUpstream(detail)
		snapshot["billingCycle"] = row.Cycle
		raw, e := json.Marshal(snapshot)
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		fields := map[string]any{"product_id": row.TargetProductID, "product_name": row.TargetName, "config_snapshot": string(raw), "updated_at": now}
		if detail.ExpiresAt != nil && detail.ExpiresAt.After(now) {
			fields["expires_at"] = *detail.ExpiresAt
		}
		if e := tx.Model(&host).Updates(fields).Error; e != nil {
			return e
		}
		if e := tx.Model(&current).Updates(map[string]any{"state": "completed", "updated_at": now}).Error; e != nil {
			return e
		}
		if paid.Status == 2 {
			changed, e := orderstate.CompletePaidTx(tx, row.OrderID, row.CustomerID, now)
			if e != nil {
				return e
			}
			if !changed {
				return ErrUnsupported
			}
		}
		if e := tx.Model(&order.OrderItem{}).Where("order_id=? AND module_type='instance_upgrade'", row.OrderID).Updates(map[string]any{"delivery_status": 2, "updated_at": now}).Error; e != nil {
			return e
		}
		if e := pointsevents.Enqueue(tx, pointsevents.Event{Key: fmt.Sprintf("upgrade:%d", row.ID), UserID: row.CustomerID, Event: "order", RefID: paid.OrderNo, AmountCents: int64(row.Amount)}); e != nil {
			return e
		}
		proof, _ := json.Marshal(map[string]any{"orderId": fmt.Sprint(row.OrderID), "invoiceId": current.InvoiceID, "productId": fmt.Sprint(row.TargetProductID), "cost": row.Cost.String()})
		return tx.Create(&dom.Event{InstanceID: row.InstanceID, EventType: "upgrade", OperatorType: 3, Status: 2, UpstreamRequest: `{}`, UpstreamResponse: string(proof), CreatedAt: now, UpdatedAt: now, ProcessedAt: &now}).Error
	})
}
