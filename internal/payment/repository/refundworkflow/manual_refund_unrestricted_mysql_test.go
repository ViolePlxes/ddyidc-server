package refundworkflow

import (
	order "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/testenv"
	"testing"
	"time"
)

// A manual order refund is approved after the administrator has settled the
// upstream side: the machine may already be deleted or its live expiry may
// differ from the purchased period. The order, customer and paid-amount checks
// still run, but the live entitlement must not block the approval.
func TestManualOrderRefundIgnoresChangedMachineEntitlementMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	customer := int64(880019101)
	orderID := int64(880019102)
	now := time.Now().UTC().Truncate(time.Second)
	if err := tx.Table("`order`.orders").Create(map[string]any{
		"id": orderID, "order_no": "REFUND-MANUAL-ISOLATED", "customer_id": customer,
		"total_amount": 3000, "pay_amount": 3000, "setup_fee": 0, "status": order.OrderStatusCompleted,
		"created_at": now, "updated_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 14, 17, 47, 15, 0, time.Local)
	end := start.Add(30 * 24 * time.Hour)
	req := Request{Source: "order", ObjectID: orderID, CustomerID: customer}
	snap := Snapshot{Paid: 3000, Periods: []Period{{InstanceID: 880019103, BillingCycle: "monthly", Paid: 3000, EntitlementStart: &start, EntitlementEnd: &end}}}
	// The live host no longer exists; the automatic path must fail closed.
	if err := verifyOrderEntitlementPolicy(tx, req, snap, false); err != ErrConflict {
		t.Fatalf("automatic refund should fail on changed entitlement: %v", err)
	}
	// The manual path proceeds on the purchased snapshot.
	if err := verifyOrderEntitlementPolicy(tx, req, snap, true); err != nil {
		t.Fatalf("manual refund blocked: %v", err)
	}
	// Order and customer checks stay enforced for manual refunds.
	wrongCustomer := req
	wrongCustomer.CustomerID = customer + 1
	if err := verifyOrderEntitlementPolicy(tx, wrongCustomer, snap, true); err != ErrConflict {
		t.Fatalf("manual refund accepted a foreign order: %v", err)
	}
}
