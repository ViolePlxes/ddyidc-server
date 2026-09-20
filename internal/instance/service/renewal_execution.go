package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	order "ddyidc/internal/order/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func (s *Service) ProvisionRenewal(ctx context.Context, customer, orderID int64, method string) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	var r Renewal
	e := s.db.WithContext(ctx).Where("order_id=?", orderID).Take(&r).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if e != nil {
		return true, e
	}
	if r.CustomerID != customer {
		return true, ErrForbidden
	}
	var o order.Order
	if e = s.db.WithContext(ctx).Where("id=? AND customer_id=?", orderID, customer).Take(&o).Error; e != nil {
		return true, e
	}
	if r.State == "completed" {
		return true, nil
	}
	if o.Status != 2 && o.Status != 6 {
		return true, errors.New("续费订单尚未支付")
	}
	h, e := s.owned(ctx, customer, r.InstanceID)
	if e != nil {
		return true, e
	}
	if h.OwnershipVersion != r.OwnershipVersion || h.UpstreamInstanceID != r.HostID {
		return true, errors.New("实例绑定已变更，续费待人工核对")
	}
	p, warning := s.provider(h)
	if warning != "" {
		return true, ErrUnsupported
	}
	rp, ok := p.(contracts.RenewalProvider)
	if !ok {
		return true, ErrUnsupported
	}
	// Every re-entry first reconciles actual expiry; never replay an uncertain write.
	detail, e := p.GetInstanceDetail(parseHostID(r.HostID))
	if e != nil || detail == nil || detail.ID != r.HostID {
		return true, errors.New("读取续费结果失败")
	}
	if detail.ExpiresAt != nil && detail.ExpiresAt.After(r.BaseExpiry) {
		return true, s.completeRenewal(ctx, r, *detail.ExpiresAt)
	}
	if r.State == "creating" || r.State == "paying" || r.State == "uncertain" || r.State == "verifying" {
		return true, errors.New("续费结果待核对，请勿重复付款")
	}
	if r.State == "unpaid" {
		// Only one process may create an upstream invoice, even after duplicate callbacks.
		claim := s.db.WithContext(ctx).Model(&Renewal{}).Where("id=? AND state='unpaid'", r.ID).Updates(map[string]any{"state": "creating", "updated_at": time.Now()})
		if claim.Error != nil {
			return true, claim.Error
		}
		if claim.RowsAffected != 1 {
			return true, nil
		}
		quotes, e := rp.RenewalQuotes(parseHostID(r.HostID))
		valid := false
		if e == nil {
			for _, q := range quotes {
				if q.Cycle == r.Cycle && q.Amount == r.Cost.String() && q.Duration == r.Duration {
					valid = true
				}
			}
		}
		if !valid {
			return true, s.renewalUncertain(ctx, r.ID, "续费成本或周期变化，请人工核对")
		}
		invoice, e := rp.CreateRenewalInvoice(parseHostID(r.HostID), r.Cycle, r.Duration)
		if e != nil {
			return true, s.renewalUncertain(ctx, r.ID, "创建续费账单结果未确认，请人工核对")
		}
		if e = s.db.WithContext(ctx).Model(&Renewal{}).Where("id=? AND state='creating'", r.ID).Updates(map[string]any{"invoice_id": invoice, "state": "invoiced", "updated_at": time.Now()}).Error; e != nil {
			return true, e
		}
		r.InvoiceID = invoice
		r.State = "invoiced"
	}
	if r.State == "invoiced" {
		claim := s.db.WithContext(ctx).Model(&Renewal{}).Where("id=? AND state='invoiced'", r.ID).Updates(map[string]any{"state": "paying", "updated_at": time.Now()})
		if claim.Error != nil {
			return true, claim.Error
		}
		if claim.RowsAffected != 1 {
			return true, nil
		}
		if e = rp.PayRenewalInvoice(r.InvoiceID); e != nil {
			return true, s.renewalUncertain(ctx, r.ID, "服务续费支付结果未确认，请人工核对")
		}
		if e = s.db.WithContext(ctx).Model(&Renewal{}).Where("id=?", r.ID).Updates(map[string]any{"state": "verifying", "updated_at": time.Now()}).Error; e != nil {
			return true, e
		}
		detail, e = p.GetInstanceDetail(parseHostID(r.HostID))
		if e == nil && detail != nil && detail.ID == r.HostID && detail.ExpiresAt != nil && detail.ExpiresAt.After(r.BaseExpiry) {
			return true, s.completeRenewal(ctx, r, *detail.ExpiresAt)
		}
	}
	return true, errors.New("已支付，等待服务到期时间更新")
}
func (s *Service) renewalUncertain(ctx context.Context, id int64, message string) error {
	s.db.WithContext(ctx).Model(&Renewal{}).Where("id=?", id).Updates(map[string]any{"state": "uncertain", "last_error": message, "updated_at": time.Now()})
	return errors.New(message)
}
func (s *Service) completeRenewal(ctx context.Context, r Renewal, expiry time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Renewal
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", r.ID).Take(&current).Error; e != nil {
			return e
		}
		if current.State == "completed" {
			return nil
		}
		now := time.Now()
		var host dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND ownership_version=?", r.InstanceID, r.CustomerID, r.OwnershipVersion).Take(&host).Error; e != nil {
			return ErrForbidden
		}
		snapshot := map[string]any{}
		if e := json.Unmarshal([]byte(host.ConfigSnapshot), &snapshot); e != nil {
			return e
		}
		snapshot["billingCycle"] = r.Cycle
		raw, e := json.Marshal(snapshot)
		if e != nil {
			return e
		}
		res := tx.Model(&dom.Host{}).Where("id=? AND customer_id=? AND ownership_version=? AND upstream_instance_id=?", r.InstanceID, r.CustomerID, r.OwnershipVersion, r.HostID).Updates(map[string]any{"expires_at": expiry, "config_snapshot": string(raw), "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrForbidden
		}
		if e := tx.Model(&current).Updates(map[string]any{"state": "completed", "last_error": "", "updated_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Model(&order.Order{}).Where("id=? AND status=2", r.OrderID).Updates(map[string]any{"status": 6, "updated_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Model(&order.OrderItem{}).Where("order_id=? AND module_type='instance_renewal'", r.OrderID).Updates(map[string]any{"delivery_status": 2, "updated_at": now}).Error; e != nil {
			return e
		}
		return tx.Create(&dom.Event{InstanceID: r.InstanceID, EventType: "renew", OperatorType: 3, Status: 2, UpstreamRequest: `{}`, UpstreamResponse: fmt.Sprintf(`{"orderId":"%d","expiresAt":%q}`, r.OrderID, formatTime(expiry)), CreatedAt: now, UpdatedAt: now, ProcessedAt: &now}).Error
	})
}
func (s *Service) StartRenewalMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.renewalTick(ctx)
		}
	}
}
func (s *Service) renewalTick(ctx context.Context) {
	if s.db == nil {
		return
	}
	var pending []Renewal
	s.db.WithContext(ctx).Table("instance.instance_renewals r").Select("r.*").Joins("JOIN `order`.orders o ON o.id=r.order_id").Where("o.status=2 AND r.state NOT IN ?", []string{"completed", "cancelled"}).Order("r.updated_at ASC").Limit(50).Find(&pending)
	for _, r := range pending {
		if ctx.Err() != nil {
			return
		}
		s.ProvisionRenewal(ctx, r.CustomerID, r.OrderID, "reconcile")
		s.db.WithContext(ctx).Model(&Renewal{}).Where("id=?", r.ID).Update("updated_at", time.Now())
	}
	var hosts []dom.Host
	s.db.WithContext(ctx).Table("instance.instances i").Select("i.*").Joins("JOIN instance.instance_renewal_settings r ON r.instance_id=i.id AND r.customer_id=i.customer_id AND r.enabled=1").Where("i.deleted_at IS NULL AND i.status IN (3,4,5) AND i.expires_at<=? AND i.expires_at>? AND r.updated_at<=?", time.Now().Add(72*time.Hour), time.Now().Add(-7*24*time.Hour), time.Now().Add(-15*time.Minute)).Limit(50).Find(&hosts)
	for _, h := range hosts {
		if ctx.Err() != nil {
			return
		}
		claim := s.db.WithContext(ctx).Model(&RenewalSettings{}).Where("instance_id=? AND customer_id=? AND enabled=1 AND updated_at<=?", h.ID, h.CustomerID, time.Now().Add(-15*time.Minute)).Update("updated_at", time.Now())
		if claim.Error != nil || claim.RowsAffected != 1 {
			continue
		}
		_, snap := parseSnapshot(h.ConfigSnapshot)
		if snap.BillingCycle == "" {
			continue
		}
		o, e := s.CreateRenewal(ctx, h.CustomerID, h.ID, snap.BillingCycle)
		if e != nil {
			continue
		}
		oid := parseHostID(o.OrderID)
		s.PayRenewalBalance(ctx, h.CustomerID, h.ID, oid, "system_job")
	}
}
