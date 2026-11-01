package instance

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/notification"
	order "ddyidc/internal/order/domain"
	orderstate "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/pointsevents"
	"ddyidc/internal/shared/renewalquote"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"strings"
	"time"
)

func (s *Service) ProvisionRenewal(ctx context.Context, customer, orderID int64, method string) (handled bool, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
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
	defer func() {
		if resultErr != nil {
			// Queue an owner-bound, idempotent attention notice. Actual provider
			// details remain in the existing order/task audit, never in the inbox.
			call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if e := s.db.WithContext(call).Transaction(func(tx *gorm.DB) error {
				return notification.EnqueueProductLifecycle(tx, notification.Event{Key: fmt.Sprintf("renewal_attention:%d", r.ID), RecipientID: r.CustomerID, Type: "billing", Scene: "product_lifecycle", Title: "产品续费待核对", Summary: "续费订单已支付，但服务续费结果尚未确认，请核对订单或联系客服。系统不会重复扣费。", Content: "续费订单已支付，但服务续费结果尚未确认，请核对订单或联系客服。系统不会重复扣费。", BusinessType: "instance", BusinessID: fmt.Sprint(r.InstanceID), OwnershipVersion: r.OwnershipVersion})
			}); e != nil {
				log.Print("RENEWAL attention notice persistence failed")
			}
			notification.Wake()
		}
	}()
	h, e := s.owned(ctx, customer, r.InstanceID)
	if e != nil {
		return true, e
	}
	if h.OwnershipVersion != r.OwnershipVersion || h.UpstreamInstanceID != r.HostID || (h.UpstreamProviderID == nil || *h.UpstreamProviderID != r.SupplierID) {
		return true, errors.New("实例绑定已变更，续费待人工核对")
	}
	if h.Status == 6 {
		return true, errors.New("实例已删除，续费待人工核对")
	}
	p, warning := s.providerContext(ctx, h)
	if warning != "" {
		return true, ErrUnsupported
	}
	rp, ok := p.(contracts.RenewalProvider)
	if !ok {
		return true, ErrUnsupported
	}
	// Expiry alone cannot prove this invoice was paid (an administrator or another
	// order may have extended it). Only a persisted successful payment may settle.
	detail, e := p.GetInstanceDetail(parseHostID(r.HostID))
	if e != nil || detail == nil || detail.ID != r.HostID {
		return true, errors.New("读取续费结果失败")
	}
	if unavailableLifecycle(detail, r.HostID) == "deleted" {
		return true, errors.New("实例已删除，续费待人工核对")
	}
	if detail.ExpiresAt != nil && detail.ExpiresAt.After(r.BaseExpiry) {
		if r.State == "verifying" {
			return true, s.completeRenewal(ctx, r, *detail.ExpiresAt)
		}
		return true, errors.New("服务到期时间已变化但缺少本次付款证据，请人工核对")
	}
	if r.State == "creating" || r.State == "paying" || r.State == "uncertain" || r.State == "verifying" {
		return true, errors.New("续费结果待核对，请勿重复付款")
	}
	if r.State == "unpaid" {
		// A verified external receipt is retained by payment processing even when
		// its old renewal price cannot be fulfilled. Never buy against old pricing.
		var proof renewalquote.Evidence
		if e := s.db.WithContext(ctx).Where("order_id=? AND renewal_id=?", orderID, r.ID).Order("revision DESC").Take(&proof).Error; e != nil {
			return true, renewalquote.Unverified()
		}
		original, e := billing.ParseMoney(proof.RetailAmount)
		if e != nil || int64(original) != o.TotalAmount || !proof.Matches(renewalBinding(*h)) || proof.SourceVersion != renewalquote.RuleVersion {
			return true, renewalquote.Unverified()
		}
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
				if q.Cycle == r.Cycle && q.Amount == r.Cost.String() && q.Duration == r.Duration && renewalOriginalMatches(q.OriginalAmount, proof.ExchangeRate, proof.RetailAmount) {
					valid = true
				}
			}
		}
		if !valid {
			return true, s.renewalUncertain(ctx, r.ID, "续费成本或周期变化，请人工核对")
		}
		invoice, actual, e := rp.CreateRenewalInvoice(parseHostID(r.HostID), r.Cycle, r.Duration)
		if invoice != "" {
			if err := s.db.WithContext(ctx).Model(&Renewal{}).Where("id=? AND state='creating'", r.ID).Update("invoice_id", invoice).Error; err != nil {
				return true, err
			}
		}
		if e != nil {
			return true, s.renewalUncertain(ctx, r.ID, "创建续费账单或实际金额未确认，未付款，请人工核对")
		}
		actualCost, amountErr := billing.ParseMoney(actual)
		if amountErr != nil || actualCost <= 0 || actualCost > r.Cost {
			return true, s.renewalUncertain(ctx, r.ID, "续费实际金额超出已确认报价，未付款，请人工核对")
		}
		persisted := s.db.WithContext(ctx).Model(&Renewal{}).Where("id=? AND state='creating'", r.ID).Updates(map[string]any{"invoice_id": invoice, "cost": actualCost, "state": "invoiced_verified", "updated_at": time.Now()})
		if persisted.Error != nil {
			return true, persisted.Error
		}
		if persisted.RowsAffected != 1 {
			return true, errors.New("续费状态已变更，账单待核对，未再次支付")
		}
		r.InvoiceID = invoice
		r.Cost = actualCost
		r.State = "invoiced_verified"
	}
	if r.State == "invoiced" {
		return true, errors.New("旧续费账单缺少实际金额校验记录，未付款，请人工核对")
	}
	if r.State == "invoiced_verified" {
		claim := s.db.WithContext(ctx).Model(&Renewal{}).Where("id=? AND state='invoiced_verified'", r.ID).Updates(map[string]any{"state": "paying", "updated_at": time.Now()})
		if claim.Error != nil {
			return true, claim.Error
		}
		if claim.RowsAffected != 1 {
			return true, nil
		}
		if e = rp.PayRenewalInvoice(r.InvoiceID); e != nil {
			return true, s.renewalUncertain(ctx, r.ID, renewalPaymentReason(e))
		}
		persisted := s.db.WithContext(ctx).Model(&Renewal{}).Where("id=? AND state='paying'", r.ID).Updates(map[string]any{"state": "verifying", "updated_at": time.Now()})
		if persisted.Error != nil {
			return true, persisted.Error
		}
		if persisted.RowsAffected != 1 {
			return true, errors.New("续费状态已变更，请核对远端结果")
		}
		detail, e = p.GetInstanceDetail(parseHostID(r.HostID))
		if e == nil && detail != nil && detail.ID == r.HostID && detail.ExpiresAt != nil && detail.ExpiresAt.After(r.BaseExpiry) {
			return true, s.completeRenewal(ctx, r, *detail.ExpiresAt)
		}
	}
	return true, errors.New("已支付，等待服务到期时间更新")
}
func (s *Service) renewalUncertain(ctx context.Context, id int64, message string) error {
	// Late errors from an old executor must never overwrite completion.
	if err := s.db.WithContext(ctx).Model(&Renewal{}).Where("id=? AND state IN ('creating','paying','verifying')", id).Updates(map[string]any{"state": "uncertain", "last_error": message, "updated_at": time.Now()}).Error; err != nil {
		return err
	}
	return errors.New(message)
}

// The manual todo must always show why the payment is uncertain, so the supplier
// failure is carried into last_error instead of a generic sentence. Only the
// classified message is kept: no credentials, URLs or response bodies.
func renewalPaymentReason(err error) string {
	detail := ""
	if err != nil {
		detail = strings.Join(strings.Fields(err.Error()), " ")
	}
	if runes := []rune(detail); len(runes) > 120 {
		detail = string(runes[:120]) + "…"
	}
	if detail == "" {
		return "服务续费支付结果未确认，请人工核对"
	}
	return "服务续费支付结果未确认：" + detail + "，请人工核对"
}
func (s *Service) completeRenewal(ctx context.Context, r Renewal, expiry time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var paid order.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", r.OrderID, r.CustomerID).Take(&paid).Error; err != nil {
			return err
		}
		if paid.Status != 2 && paid.Status != 6 {
			return errors.New("续费订单状态已变化，请核对")
		}
		var current Renewal
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", r.ID).Take(&current).Error; e != nil {
			return e
		}
		if current.State == "completed" {
			return nil
		}
		if current.State != "verifying" || current.InvoiceID == "" {
			return errors.New("缺少本次续费支付成功证据，请人工核对")
		}
		now := time.Now()
		var host dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND ownership_version=?", r.InstanceID, r.CustomerID, r.OwnershipVersion).Take(&host).Error; e != nil {
			return ErrForbidden
		}
		// Bind completion to the persisted renewal, not a stale executor snapshot.
		if current.OrderID != r.OrderID || current.CustomerID != r.CustomerID || current.InstanceID != r.InstanceID ||
			current.OwnershipVersion != r.OwnershipVersion || current.HostID != r.HostID || current.SupplierID != r.SupplierID ||
			host.UpstreamInstanceID != current.HostID || (host.UpstreamProviderID == nil || *host.UpstreamProviderID != current.SupplierID) {
			return errors.New("实例绑定已变更，续费待人工核对")
		}
		if !expiry.After(current.BaseExpiry) || (host.ExpiresAt != nil && expiry.Before(*host.ExpiresAt)) {
			return errors.New("续费到期时间证据已过期，请重新核对服务状态")
		}
		snapshot := map[string]any{}
		if e := json.Unmarshal([]byte(host.ConfigSnapshot), &snapshot); e != nil {
			return e
		}
		if _, exists := snapshot["originalBillingCycle"]; !exists {
			snapshot["originalBillingCycle"] = snapshot["billingCycle"]
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
		if paid.Status == order.OrderStatusPaid {
			changed, err := orderstate.CompletePaidTx(tx, r.OrderID, r.CustomerID, now)
			if err != nil {
				return err
			}
			if !changed {
				return errors.New("续费订单状态已变化，交付已回滚")
			}
		}
		if e := tx.Model(&order.OrderItem{}).Where("order_id=? AND module_type='instance_renewal'", r.OrderID).Updates(map[string]any{"delivery_status": 2, "updated_at": now}).Error; e != nil {
			return e
		}
		if err := pointsevents.Enqueue(tx, pointsevents.Event{Key: fmt.Sprintf("renew:%d", r.ID), UserID: r.CustomerID, Event: "renew", RefID: paid.OrderNo, AmountCents: int64(r.Amount)}); err != nil {
			return err
		}
		if err := notification.EnqueueProductLifecycle(tx, notification.Event{Key: fmt.Sprintf("renewal_complete:%d", r.ID), RecipientID: r.CustomerID, Type: "billing", Scene: "product_lifecycle", Title: "产品续费成功", Summary: "续费成功，到期时间已更新。", Content: fmt.Sprintf("产品续费成功，到期时间已更新至%s。", formatTime(expiry)), BusinessType: "instance", BusinessID: fmt.Sprint(r.InstanceID), OwnershipVersion: r.OwnershipVersion}); err != nil {
			return err
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
			_ = distsync.Work(ctx, func(ctx context.Context) error { s.renewalTick(ctx); return nil })
		}
	}
}
func (s *Service) renewalTick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if s.db == nil {
		return
	}
	s.reconcileAutoRenewAttempts(ctx)
	// Paid renewals execute only through their transactional durable intention.
	// Unknown outcomes remain visible for reconciliation, not minute-by-minute
	// unbounded provider calls or a second execution path.
	hosts, err := s.autoRenewalCandidates(ctx)
	if err != nil {
		log.Print("RENEWAL candidate scan failed; no renewal initiated")
		return
	}
	for _, h := range hosts {
		if ctx.Err() != nil {
			return
		}
		var approval AutoRenewalQuote
		if e := s.db.WithContext(ctx).Where("instance_id=? AND customer_id=? AND ownership_version=?", h.ID, h.CustomerID, h.OwnershipVersion).Take(&approval).Error; e != nil {
			if !errors.Is(e, gorm.ErrRecordNotFound) {
				continue
			}
			cycle, err := s.OriginalRenewalCycle(ctx, h.CustomerID, h.ID)
			if err != nil {
				continue
			}
			approval.Cycle = cycle
		}

		if !s.autoRenewalQuoteAllowed(ctx, h, approval.Cycle) {
			continue
		}
		s.autoRenewOnce(ctx, h, approval.Cycle)
	}
}

func (s *Service) autoRenewalCandidates(ctx context.Context) ([]dom.Host, error) {
	var hosts []dom.Host
	// GORM requires explicit AS with a schema-qualified table to scope its
	// implicit soft-delete predicate to i instead of the model table name.
	err := s.db.WithContext(ctx).Table("instance.instances AS i").Select("i.*").Joins("JOIN instance.instance_renewal_settings r ON r.instance_id=i.id AND r.customer_id=i.customer_id AND r.enabled=1").Where("i.deleted_at IS NULL AND i.status IN (3,4) AND i.expires_at<=? AND i.expires_at>?", time.Now().Add(24*time.Hour), time.Now()).Where("NOT EXISTS (SELECT 1 FROM instance.instance_product_lifecycle l WHERE l.instance_id=i.id AND l.ownership_version=i.ownership_version AND l.base_expiry=i.expires_at AND l.kind='auto_renew_attempt' AND (l.state<>'insufficient' OR l.next_attempt_at>NOW(3)))").Limit(50).Find(&hosts).Error
	return hosts, err
}
