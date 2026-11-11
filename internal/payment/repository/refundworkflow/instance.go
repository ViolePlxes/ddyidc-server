package refundworkflow

import (
	"context"
	"crypto/sha256"
	balance "ddyidc/internal/billing/balance"
	instance "ddyidc/internal/instance/domain"
	order "ddyidc/internal/order/domain"
	payment "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// MachineAllocation is one order's share of a machine-level refund. Machine
// refunds collect every paid period of one instance, which can belong to the
// purchase order and to several renewal orders.
type MachineAllocation struct {
	OrderID int64
	Paid    balance.Money
	Refund  balance.Money
}

// MachineSnapshot is the frozen input of a machine refund. Allocations are
// derived from the periods of each order, so the invoice settlement never has
// to re-read the calculation.
type MachineSnapshot struct {
	Snapshot
	InstanceID  int64               `json:"instanceId,string"`
	Allocations []MachineAllocation `json:"allocations"`
}

// SubmitCheck runs inside the submission transaction after the machine row is
// locked. It lets the caller enforce current state (locks, renewals, pending
// requests, mailbox proof) without writing anything when it refuses.
type SubmitCheck func(*gorm.DB, instance.Host) error

func machineFees(snapshot MachineSnapshot) Fees {
	return Fees{Setup: snapshot.MinimumSetup.String(), Installation: "0.00", Handling: snapshot.MinimumHandling.String(), Gift: "0.00", Traffic: "0.00", ValueAdded: "0.00", Evidence: "机器退款：开通费仅在首购订单扣除一次，未使用天数按申请时间折算"}
}

// InstanceQuote computes the refundable amount of one machine: its purchase
// period plus every completed renewal, minus the setup fee charged once on the
// purchase order and the used days of each period.
func InstanceQuote(ctx context.Context, db *gorm.DB, id int64) (Detail, Calculation, Fees, error) {
	d, e := Load(ctx, db, id)
	if e != nil {
		return d, Calculation{}, Fees{}, e
	}
	if d.Status != 0 || d.Request == nil || d.Request.Source != "instance" {
		return d, Calculation{}, Fees{}, ErrConflict
	}
	var snapshot MachineSnapshot
	if json.Unmarshal(d.Request.Snapshot, &snapshot) != nil {
		return d, Calculation{}, Fees{}, ErrCalculation
	}
	fees := machineFees(snapshot)
	calc, e := Calculate(snapshot.Snapshot, d.CreatedAt, fees)
	return d, calc, fees, e
}

// InstancePreview builds the machine snapshot without creating an application:
// the customer sees the refundable amount before submitting destruction.
func InstancePreview(ctx context.Context, db *gorm.DB, customer, instanceID int64) (MachineSnapshot, Calculation, Fees, error) {
	var out MachineSnapshot
	var h instance.Host
	if err := db.WithContext(ctx).Where("id=? AND customer_id=?", instanceID, customer).Take(&h).Error; err != nil {
		return out, Calculation{}, Fees{}, ErrConflict
	}
	out, err := machineSnapshot(db.WithContext(ctx), h)
	if err != nil {
		return out, Calculation{}, Fees{}, err
	}
	if len(out.Warnings) > 0 {
		return out, Calculation{}, Fees{}, ErrCalculation
	}
	fees := machineFees(out)
	calc, err := Calculate(out.Snapshot, time.Now().UTC().Truncate(time.Second), fees)
	return out, calc, fees, err
}

// SubmitInstance creates a machine-level refund application for one instance.
// The customer no longer refunds per order: the request owns every paid period
// of the machine and the review settles each order's invoice with its share.
func SubmitInstance(ctx context.Context, db *gorm.DB, customer, instanceID int64, reason, key string, checks ...SubmitCheck) (payment.RefundApplication, error) {
	var out payment.RefundApplication
	reason = strings.TrimSpace(reason)
	if customer <= 0 || instanceID <= 0 || reason == "" || len([]rune(reason)) > 255 || len(key) < 8 || len(key) > 64 {
		return out, ErrInput
	}
	sum := sha256.Sum256([]byte(key))
	no := "TODO" + hex.EncodeToString(sum[:])[:28]
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var h instance.Host
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", instanceID, customer).Take(&h).Error; err != nil {
			return ErrConflict
		}
		var previous Request
		found := tx.Where("request_id=?", key).Take(&previous).Error
		if found == nil {
			if previous.Source != "instance" || previous.ObjectID != instanceID || previous.CustomerID != customer {
				return ErrConflict
			}
			if err := tx.First(&out, previous.ApplicationID).Error; err != nil {
				return err
			}
			if out.Reason != reason {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			return found
		}
		snapshot, e := machineSnapshot(tx, h)
		if e != nil {
			return e
		}
		if len(snapshot.Warnings) > 0 {
			// A machine refund must never estimate: unresolved periods are a
			// reviewable conflict, not a smaller number.
			return ErrCalculation
		}
		now := time.Now().UTC().Truncate(time.Second)
		// Freeze the quote at the submission instant: the customer's history and
		// the review both start from this same calculation.
		calc, e := Calculate(snapshot.Snapshot, now, machineFees(snapshot))
		if e != nil {
			return e
		}
		for _, check := range checks {
			if e := check(tx, h); e != nil {
				return e
			}
		}
		encoded, e := json.Marshal(snapshot)
		if e != nil {
			return e
		}
		out = payment.RefundApplication{ID: snowflake.GenID(), RefundNo: no, OrderID: h.OrderID, UserID: customer, RefundAmount: calc.Refund.String(), Reason: reason, Status: 0, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&out).Error; e != nil {
			return e
		}
		return tx.Create(&Request{ApplicationID: out.ID, Source: "instance", ObjectID: instanceID, CustomerID: customer, RequestID: key, Snapshot: encoded, CreatedAt: now}).Error
	})
	return out, err
}

func machineSnapshot(tx *gorm.DB, h instance.Host) (MachineSnapshot, error) {
	out := MachineSnapshot{Snapshot: Snapshot{Periods: []Period{}, Warnings: []string{}}, InstanceID: h.ID}
	if h.CustomerID <= 0 || h.OrderID <= 0 {
		return out, ErrCalculation
	}
	var o order.Order
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND customer_id=?", h.OrderID, h.CustomerID).Take(&o).Error; err != nil {
		return out, ErrCalculation
	}
	if o.Status != order.OrderStatusPaid && o.Status != order.OrderStatusCompleted && o.Status != order.OrderStatusPartialRefund {
		return out, ErrConflict
	}
	var item order.OrderItem
	if err := tx.Where("id=? AND order_id=?", h.OrderItemID, h.OrderID).Take(&item).Error; err != nil {
		return out, ErrCalculation
	}
	if item.Subtotal <= 0 || o.TotalAmount <= 0 || o.PayAmount <= 0 || o.SetupFee < 0 || o.SetupFee > o.TotalAmount {
		return out, ErrCalculation
	}
	paid := proportion(balance.Money(o.PayAmount), balance.Money(item.Subtotal), balance.Money(o.TotalAmount))
	out.Paid = paid
	out.MinimumSetup = proportion(balance.Money(o.PayAmount), balance.Money(o.SetupFee), balance.Money(o.TotalAmount))

	purchase := Period{BillingDays: originalBillingDays(item), BillingCycle: originalBillingCycle(item), InstanceID: h.ID, OwnershipVersion: h.OwnershipVersion, Paid: paid,
		Start: h.ActivatedAt, EntitlementStart: h.ActivatedAt, EntitlementEnd: h.ExpiresAt}
	if h.ActivatedAt == nil || h.ExpiresAt == nil {
		out.Warnings = append(out.Warnings, "机器服务起止日期缺失，需核实原始账单")
	}
	end, err := originalPeriodEnd(tx, h.ID, h.ExpiresAt)
	if err != nil {
		return out, err
	}
	purchase.End = end
	out.Periods = append(out.Periods, purchase)
	out.Allocations = append(out.Allocations, MachineAllocation{OrderID: o.ID, Paid: paid})

	var renewals []struct {
		ID, OrderID      int64
		Amount           balance.Money
		BaseExpiry       time.Time
		State            string
		OwnershipVersion int64
		CustomerID       int64
		InstanceID       int64
	}
	if err := tx.Table("instance.instance_renewals").Select("id,order_id,amount,base_expiry,state,ownership_version,customer_id,instance_id").Where("instance_id=? AND customer_id=?", h.ID, h.CustomerID).Order("base_expiry ASC,id ASC").Find(&renewals).Error; err != nil {
		return out, err
	}
	for _, r := range renewals {
		if r.State != "completed" {
			continue
		}
		var ro order.Order
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=?", r.OrderID).Take(&ro).Error; err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("续费单 %d 记录缺失，需核实原始账单", r.OrderID))
			continue
		}
		if ro.CustomerID != h.CustomerID || (ro.Status != order.OrderStatusPaid && ro.Status != order.OrderStatusCompleted && ro.Status != order.OrderStatusPartialRefund) {
			out.Warnings = append(out.Warnings, fmt.Sprintf("续费单 %d 状态异常，需核实原始账单", r.OrderID))
			continue
		}
		if r.OwnershipVersion != h.OwnershipVersion {
			out.Warnings = append(out.Warnings, "续费期间机器归属已变更，需核实退款权益")
		}
		start := r.BaseExpiry
		period := Period{BillingDays: originalBillingDays(item), BillingCycle: originalBillingCycle(item), InstanceID: h.ID, OwnershipVersion: r.OwnershipVersion, RenewalID: r.ID, Paid: r.Amount,
			Start: &start, EntitlementStart: h.ActivatedAt, EntitlementEnd: h.ExpiresAt}
		renewalEnd, e := renewalCompletionExpiry(tx, h.ID, r.OrderID, start, r.Amount)
		if e != nil {
			return out, e
		}
		period.End = renewalEnd
		out.Periods = append(out.Periods, period)
		out.Allocations = append(out.Allocations, MachineAllocation{OrderID: r.OrderID, Paid: r.Amount})
	}
	var total balance.Money
	for _, p := range out.Periods {
		if p.Paid < 0 || p.Paid > balance.MaxMoney-total {
			return out, ErrCalculation
		}
		total += p.Paid
	}
	if total != out.Paid {
		// Later renewals are separate purchases: the machine total is their sum.
		out.Paid = total
	}
	return out, nil
}

// renewalCompletionExpiry returns the entitlement end proven by the renewal's
// own completion evidence, never a later renewal's base expiry.
func renewalCompletionExpiry(tx *gorm.DB, instanceID, orderID int64, start time.Time, amount balance.Money) (*time.Time, error) {
	var rows []struct{ UpstreamResponse string }
	if err := tx.Table("instance.instance_events").Select("upstream_response").Where("instance_id=? AND event_type='renew' AND status=2 AND JSON_UNQUOTE(JSON_EXTRACT(upstream_response,'$.orderId'))=?", instanceID, fmt.Sprint(orderID)).Find(&rows).Error; err != nil {
		return nil, err
	}
	var end *time.Time
	for _, row := range rows {
		var proof struct {
			ExpiresAt string `json:"expiresAt"`
		}
		if json.Unmarshal([]byte(row.UpstreamResponse), &proof) != nil {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, proof.ExpiresAt)
		if err != nil || !parsed.After(start) {
			continue
		}
		if end != nil && !end.Equal(parsed) {
			return nil, ErrCalculation
		}
		end = &parsed
	}
	if end == nil {
		_ = amount
		return nil, ErrCalculation
	}
	return end, nil
}
