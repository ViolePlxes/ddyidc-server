package instance

import (
	"context"
	"crypto/sha256"
	billing "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	order "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/renewalquote"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func renewalBinding(h dom.Host) renewalquote.Binding {
	b := renewalquote.Binding{ID: h.ID, CustomerID: h.CustomerID, ProductID: h.ProductID, OwnershipVersion: h.OwnershipVersion, UpstreamProviderID: h.UpstreamProviderID, UpstreamInstanceID: h.UpstreamInstanceID, ConfigSnapshot: h.ConfigSnapshot, Status: h.Status}
	if h.DeletedAt.Valid {
		b.DeletedAt = &h.DeletedAt.Time
	}
	return b
}

// VerifyRenewalPayment performs remote reads BEFORE any transaction/row locks.
// Its request-scoped proof must be rechecked by the actual payment transaction.
type autoRenewPaymentKey struct{}

func (s *Service) VerifyRenewalPayment(ctx context.Context, customer, orderID int64) (context.Context, error) {
	return s.verifyRenewalPayment(ctx, customer, orderID, true)
}
func (s *Service) verifyRenewalPayment(ctx context.Context, customer, orderID int64, canCorrect bool) (context.Context, error) {
	var r Renewal
	err := s.db.WithContext(ctx).Where("order_id=? AND customer_id=?", orderID, customer).Take(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var n int64
		if e := s.db.WithContext(ctx).Table("order.order_items").Where("order_id=? AND module_type='instance_renewal'", orderID).Count(&n).Error; e != nil {
			return ctx, e
		}
		if n > 0 {
			return ctx, renewalquote.Unverified()
		}
		return ctx, nil
	}
	if err != nil {
		return ctx, err
	}
	var o order.Order
	if err = s.db.WithContext(ctx).Where("id=? AND customer_id=?", orderID, customer).Take(&o).Error; err != nil {
		return ctx, err
	}
	if o.Status == 2 || o.Status == 6 {
		return ctx, nil
	}
	if o.Status != 1 || (r.State != "unpaid" && r.State != "invoiced_verified") {
		return ctx, &renewalquote.Error{Code: "RENEWAL_PAYMENT_RECONCILING"}
	}
	if r.State == "invoiced_verified" && r.InvoiceID != "" {
		var proof renewalquote.Evidence
		if e := s.db.WithContext(ctx).Where("order_id=? AND customer_id=?", orderID, customer).Order("revision DESC").Take(&proof).Error; e != nil {
			return ctx, renewalquote.Unverified()
		}
		return renewalquote.WithProof(ctx, proof.ID), nil
	}
	h, err := s.owned(ctx, customer, r.InstanceID)
	if err != nil {
		return ctx, err
	}
	provider, warning := s.providerContext(ctx, h)
	if warning != "" {
		return ctx, renewalquote.Unverified()
	}
	remote, readErr := provider.GetInstanceDetail(parseHostID(h.UpstreamInstanceID))
	var mapped int64
	if readErr != nil || remote == nil || remote.ID != h.UpstreamInstanceID || remote.ProductID == "" {
		return ctx, renewalquote.Unverified()
	}
	if e := s.db.WithContext(ctx).Table("product.upstream_products").Where("product_id=? AND supplier_id=?", h.ProductID, h.UpstreamProviderID).Select("upstream_product_id").Scan(&mapped).Error; e != nil {
		return ctx, e
	}
	if mapped <= 0 || remote.ProductID != fmt.Sprint(mapped) {
		return ctx, renewalquote.Unverified()
	}
	if remote.ExpiresAt == nil || remote.ExpiresAt.Unix() != r.BaseExpiry.Unix() {
		return ctx, &renewalquote.Error{Code: "RENEWAL_PAYMENT_RECONCILING"}
	}

	quotes, err := s.RenewalOptions(ctx, customer, r.InstanceID)
	if err != nil {
		return ctx, renewalquote.Unverified()
	}
	var selected *RenewalOption
	for i := range quotes {
		if quotes[i].Cycle == r.Cycle {
			selected = &quotes[i]
			break
		}
	}
	if selected == nil {
		return ctx, renewalquote.Unverified()
	}
	if auto, _ := ctx.Value(autoRenewPaymentKey{}).(bool); auto {
		if err := s.refreshAutoRenewalQuote(ctx, *h, *selected); err != nil {
			return ctx, err
		}
	}
	gross, err := billing.ParseMoney(selected.Amount)
	if err != nil {
		return ctx, renewalquote.Unverified()
	}
	if canCorrect && (int64(gross) != o.TotalAmount || selected.Duration != r.Duration || selected.Cost != r.Cost) && s.renewalPriceCorrector != nil {
		if e := s.correctUnpaidRenewal(ctx, r, *h, *selected); e != nil {
			return ctx, e
		}
		return s.verifyRenewalPayment(ctx, customer, orderID, false)
	}
	if int64(gross) != o.TotalAmount {
		return ctx, &renewalquote.Error{Code: "RENEWAL_PRICE_CHANGED", OldOrderID: fmt.Sprint(orderID), OldAmount: billing.Money(o.TotalAmount).String(), NewAmount: selected.Amount}
	}
	if selected.Duration != r.Duration || selected.Cost != r.Cost {
		return ctx, &renewalquote.Error{Code: "RENEWAL_QUOTE_UNVERIFIED"}
	}
	rate := selected.ExchangeRate
	b := renewalBinding(*h)
	ttl, ttlErr := s.renewalQuoteLifetime(ctx)
	if ttlErr != nil {
		return ctx, renewalquote.Unverified()
	}
	evidence := renewalquote.Evidence{ID: snowflake.GenID(), RenewalID: r.ID, OrderID: orderID, CustomerID: customer, InstanceID: h.ID, OwnershipVersion: h.OwnershipVersion, BaseExpiry: r.BaseExpiry, SupplierID: r.SupplierID, RemoteHostID: r.HostID, ProductID: h.ProductID, ConfigFingerprint: b.ConfigHash(), Cycle: r.Cycle, Duration: r.Duration, Currency: "CNY", RetailAmount: selected.Amount, ProcurementAmount: selected.Cost.String(), ExchangeRate: rate, SourceKind: "upstream_original", SourceVersion: renewalquote.RuleVersion}
	if !evidence.Matches(b) {
		return ctx, renewalquote.Unverified()
	}
	alreadyPaid := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current order.Order
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", orderID, customer).Take(&current).Error; e != nil {
			return e
		}
		if current.Status == 2 || current.Status == 6 {
			alreadyPaid = true
			return nil
		}
		if current.Status != 1 || current.TotalAmount != o.TotalAmount || current.PayAmount != o.PayAmount || current.DiscountAmount != o.DiscountAmount {
			return renewalquote.Unverified()
		}
		var renewal Renewal
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", r.ID).Take(&renewal).Error; e != nil {
			return e
		}
		if renewal.State != "unpaid" || renewal.Amount != r.Amount || renewal.Cost != r.Cost || renewal.Duration != r.Duration || renewal.Cycle != r.Cycle || renewal.HostID != r.HostID || renewal.SupplierID != r.SupplierID || renewal.OwnershipVersion != r.OwnershipVersion {
			return renewalquote.Unverified()
		}
		var host dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", h.ID).Take(&host).Error; e != nil {
			return e
		}
		if !evidence.Matches(renewalBinding(host)) {
			return renewalquote.Unverified()
		}
		var revision int64
		if e := tx.Model(&renewalquote.Evidence{}).Where("order_id=?", orderID).Select("COALESCE(MAX(revision),0)").Scan(&revision).Error; e != nil {
			return e
		}
		evidence.Revision = revision + 1
		evidence.Stamp(time.Now(), ttl)
		raw, _ := json.Marshal(evidence)
		evidence.Fingerprint = fmt.Sprintf("%x", sha256.Sum256(raw))
		if e := tx.Create(&evidence).Error; e != nil {
			return e
		}
		return renewalquote.CheckTx(renewalquote.WithProof(ctx, evidence.ID), tx, customer, orderID)
	})
	if err != nil {
		return ctx, err
	}
	if alreadyPaid {
		return ctx, nil
	}
	if prep, ok := provider.(contracts.RenewalInvoicePreparer); ok && r.InvoiceID == "" {
		invoice, costText, e := prep.PrepareRenewalInvoice(parseHostID(h.UpstreamInstanceID), r.Cycle, r.Duration)
		if e != nil {
			return ctx, errors.New("上游续费账单创建失败，请稍后重试或联系客服")
		}
		cost, e := billing.ParseMoney(costText)
		if e != nil || cost <= 0 || cost > r.Cost {
			return ctx, errors.New("上游续费金额与报价不一致，已停止付款")
		}
		// Never lose a supplier invoice that already exists: persist it from
		// either payable state and refuse to continue if the row moved on.
		persisted := s.db.WithContext(ctx).Model(&Renewal{}).Where("id=? AND state IN ('unpaid','invoiced_verified')", r.ID).Updates(map[string]any{"invoice_id": invoice, "cost": cost, "state": "invoiced_verified", "last_error": "", "updated_at": time.Now()})
		if persisted.Error != nil {
			return ctx, persisted.Error
		}
		if persisted.RowsAffected != 1 {
			return ctx, errors.New("续费状态已变化，上游账单待核对，请刷新后重试")
		}
	}
	return renewalquote.WithProof(ctx, evidence.ID), nil
}

func renewalOriginalMatches(original, rate, expected string) bool {
	value, e := billing.ParseMoney(original)
	if e != nil {
		return false
	}
	converted, e := retailRenewal(value, rate, "0", 0)
	return e == nil && converted.String() == expected
}

func (s *Service) SetRenewalReplacementCanceller(fn func(context.Context, *gorm.DB, int64, int64) error) {
	s.renewalReplacementCancel = fn
}
func renewalIntentRevision(h dom.Host, q RenewalOption) string {
	b := renewalBinding(h)
	raw, _ := json.Marshal([]any{h.ID, h.CustomerID, h.OwnershipVersion, h.ProductID, h.UpstreamProviderID, h.UpstreamInstanceID, b.ConfigHash(), q.Cycle, q.Duration, q.Amount, q.Cost.String(), q.ExchangeRate, renewalquote.RuleVersion})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
