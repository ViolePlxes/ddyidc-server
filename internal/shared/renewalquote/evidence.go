// Package renewalquote owns immutable renewal price evidence and payment gates.
// It performs no supplier requests and never writes an order or a balance.
package renewalquote

import (
	"context"
	"crypto/sha256"
	billing "ddyidc/internal/billing/balance"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"math/big"
	"time"
)

const RuleVersion = "explicit-original-v2"
const Lifetime = 5 * time.Minute
const MaxLifetime = 15 * time.Minute

type Error struct {
	Code       string `json:"code"`
	OldOrderID string `json:"oldOrderId,omitempty"`
	OldAmount  string `json:"oldAmount,omitempty"`
	NewAmount  string `json:"newAmount,omitempty"`
}

func (e *Error) Error() string {
	switch e.Code {
	case "RENEWAL_PRICE_CHANGED":
		return "续费金额已更新，请重新发起支付"
	case "RENEWAL_QUOTE_EXPIRED":
		return "续费信息已更新，请重新发起支付"
	case "RENEWAL_PAYMENT_RECONCILING":
		return "续费付款或执行结果待核对，请勿重复付款"
	default:
		return "暂时无法完成续费，请稍后重试或联系客服"
	}
}
func Unverified() error { return &Error{Code: "RENEWAL_QUOTE_UNVERIFIED"} }

type Evidence struct {
	ID                int64 `gorm:"primaryKey"`
	RenewalID         int64
	OrderID           int64
	CustomerID        int64
	InstanceID        int64
	OwnershipVersion  int64
	BaseExpiry        time.Time
	SupplierID        int64
	RemoteHostID      string
	ProductID         int64
	ConfigFingerprint string
	Cycle             string
	Duration          int64
	Currency          string
	RetailAmount      string
	ProcurementAmount string
	ExchangeRate      string
	SourceKind        string
	SourceVersion     string
	Revision          int64
	Fingerprint       string
	QuotedAt          time.Time
	ExpiresAt         time.Time
	ConfirmedAt       time.Time
	CreatedAt         time.Time
}

func (Evidence) TableName() string { return "instance.instance_renewal_quote_evidence" }

// Stamp uses the DATETIME(3) storage precision before hashing or validating.
// Letting MySQL round nanoseconds can put a freshly read quote in the future.
func (e *Evidence) Stamp(now time.Time, ttl time.Duration) {
	now = now.Truncate(time.Millisecond)
	e.QuotedAt, e.ConfirmedAt, e.CreatedAt = now, now, now
	e.ExpiresAt = now.Add(ttl)
}

type Binding struct {
	ID                 int64
	CustomerID         int64
	ProductID          int64
	OwnershipVersion   int64
	UpstreamProviderID *int64
	UpstreamInstanceID string
	ConfigSnapshot     string
	Status             uint8
	DeletedAt          *time.Time
}

func (b Binding) ConfigHash() string {
	// Supplier observations and display caches change independently of configuration.
	// Select only immutable purchase/instance configuration inputs, never retain raw values.
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(b.ConfigSnapshot), &raw) != nil {
		return ""
	}
	picked := map[string]json.RawMessage{}
	for _, k := range []string{"billingCycle", "configOptions", "customFieldsCiphertext", "selectedOptions", "pricingSpecId", "config", "options", "configuration"} {
		if v, ok := raw[k]; ok {
			picked[k] = v
		}
	}
	value, _ := json.Marshal(picked)
	return fmt.Sprintf("%x", sha256.Sum256(value))
}
func (e Evidence) Matches(b Binding) bool {
	return b.UpstreamProviderID != nil && b.DeletedAt == nil && b.Status != 6 && e.CustomerID == b.CustomerID && e.InstanceID == b.ID && e.ProductID == b.ProductID && e.OwnershipVersion == b.OwnershipVersion && e.SupplierID == *b.UpstreamProviderID && e.RemoteHostID == b.UpstreamInstanceID && e.ConfigFingerprint != "" && e.ConfigFingerprint == b.ConfigHash()
}
func (e Evidence) Valid(now time.Time) bool {
	return e.ID > 0 && e.Revision > 0 && e.Currency == "CNY" && e.SourceVersion == RuleVersion && e.Fingerprint != "" && !e.QuotedAt.After(now) && e.ExpiresAt.After(now) && e.ExpiresAt.Sub(e.QuotedAt) <= MaxLifetime && !e.ConfirmedAt.IsZero()
}

type proofKey struct{}

func WithProof(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, proofKey{}, id)
}

// CheckTx runs after the order lock, before accepting money or creating a payment.
// A freshly verified proof is request-scoped; another request cannot reuse a stale preflight.
func CheckTx(ctx context.Context, tx *gorm.DB, user, orderID int64) error {
	return checkTxAt(ctx, tx, user, orderID, time.Now())
}
func checkTxAt(ctx context.Context, tx *gorm.DB, user, orderID int64, at time.Time) error {
	var r struct {
		ID, OrderID, InstanceID, CustomerID, OwnershipVersion, SupplierID int64
		HostID, Cycle, Amount, Cost, State                                string
		Duration                                                          int64
		BaseExpiry                                                        time.Time
	}
	err := tx.Table("instance.instance_renewals").Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_id=? AND customer_id=?", orderID, user).Take(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var count int64
		if err := tx.Table("order.order_items").Where("order_id=? AND module_type='instance_renewal'", orderID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return Unverified()
		}
		return nil
	}
	if err != nil {
		return err
	}
	id, _ := ctx.Value(proofKey{}).(int64)
	if id <= 0 {
		return Unverified()
	}
	var e Evidence
	if err := tx.Where("id=? AND order_id=? AND customer_id=?", id, orderID, user).Take(&e).Error; err != nil {
		return Unverified()
	}
	var b Binding
	if err := tx.Table("instance.instances").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", r.InstanceID).Take(&b).Error; err != nil {
		return Unverified()
	}
	if !e.Valid(at) {
		return &Error{Code: "RENEWAL_QUOTE_EXPIRED"}
	}
	if !e.Matches(b) || (r.State != "unpaid" && r.State != "invoiced_verified") || e.RenewalID != r.ID || e.Cycle != r.Cycle || e.Duration != r.Duration || e.OwnershipVersion != r.OwnershipVersion || e.RemoteHostID != r.HostID || e.SupplierID != r.SupplierID || !e.BaseExpiry.Equal(r.BaseExpiry) {
		return Unverified()
	}
	var o struct{ TotalAmount, PayAmount, DiscountAmount, SetupFee int64 }
	if err := tx.Table("order.orders").Select("total_amount,pay_amount,discount_amount,setup_fee").Where("id=? AND customer_id=? AND status=1", orderID, user).Take(&o).Error; err != nil {
		return Unverified()
	}

	var rate string
	if err := tx.Table("common.suppliers").Select("rate").Where("id=?", e.SupplierID).Scan(&rate).Error; err != nil {
		return err
	}
	current, ok := new(big.Rat).SetString(rate)
	quoted, valid := new(big.Rat).SetString(e.ExchangeRate)
	if !ok || !valid || current.Sign() <= 0 || current.Cmp(quoted) != 0 {
		return Unverified()
	}
	gross, er := billing.ParseMoney(e.RetailAmount)
	net, en := billing.ParseMoney(r.Amount)
	cost, ec := billing.ParseMoney(r.Cost)
	expected, ee := billing.ParseMoney(e.ProcurementAmount)
	if er != nil || en != nil || ec != nil || ee != nil || gross <= 0 || net <= 0 || cost <= 0 || cost != expected || int64(gross) != o.TotalAmount || int64(net) != o.PayAmount || o.DiscountAmount < 0 || o.SetupFee != 0 || o.PayAmount != o.TotalAmount-o.DiscountAmount {
		return Unverified()
	}
	var items int64
	if err := tx.Table("order.order_items").Where("order_id=?", orderID).Count(&items).Error; err != nil {
		return err
	}
	if items != 1 {
		return Unverified()
	}
	if err := tx.Table("order.order_items").Where("order_id=? AND module_type='instance_renewal' AND instance_id=? AND product_id=? AND qty=1 AND unit_price=? AND subtotal=?", orderID, e.InstanceID, e.ProductID, o.TotalAmount, o.TotalAmount).Count(&items).Error; err != nil {
		return err
	}
	if items != 1 {
		return Unverified()
	}

	return nil
}

// PaymentEvidence binds a real channel checkout to its exact renewal quote.
type PaymentEvidence struct {
	PaymentID  int64 `gorm:"primaryKey"`
	EvidenceID int64
	OrderID    int64
	CustomerID int64
	AmountFen  int64
	CreatedAt  time.Time
}

func (PaymentEvidence) TableName() string { return "instance.instance_renewal_payment_quotes" }
func BindPaymentTx(ctx context.Context, tx *gorm.DB, user, orderID, paymentID, amount int64) error {
	id, _ := ctx.Value(proofKey{}).(int64)
	if id == 0 {
		return nil
	}
	var link PaymentEvidence
	err := tx.Where("payment_id=?", paymentID).Take(&link).Error
	if err == nil {
		if link.OrderID != orderID || link.CustomerID != user || link.AmountFen != amount {
			return Unverified()
		}
		return CheckTx(WithProof(ctx, link.EvidenceID), tx, user, orderID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&PaymentEvidence{PaymentID: paymentID, EvidenceID: id, OrderID: orderID, CustomerID: user, AmountFen: amount, CreatedAt: time.Now()}).Error
}
func ReceiptAllowedTx(ctx context.Context, tx *gorm.DB, user, orderID, paymentID, amount int64, paidAt time.Time) error {
	var n int64
	if err := tx.Table("order.order_items").Where("order_id=? AND module_type='instance_renewal'", orderID).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		if err := tx.Table("instance.instance_renewals").Where("order_id=?", orderID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
	var link PaymentEvidence
	if err := tx.Where("payment_id=? AND order_id=? AND customer_id=? AND amount_fen=?", paymentID, orderID, user, amount).Take(&link).Error; err != nil {
		return Unverified()
	}
	return checkTxAt(WithProof(ctx, link.EvidenceID), tx, user, orderID, paidAt)
}

func PaymentDeadline(ctx context.Context, tx *gorm.DB, limit time.Time) (time.Time, error) {
	id, _ := ctx.Value(proofKey{}).(int64)
	if id == 0 {
		return limit, nil
	}
	var e Evidence
	if err := tx.Where("id=?", id).Take(&e).Error; err != nil {
		return limit, err
	}
	if e.ExpiresAt.Before(limit) {
		return e.ExpiresAt, nil
	}
	return limit, nil
}
