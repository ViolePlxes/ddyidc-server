package instance

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	billingapi "ddyidc/internal/billing/api"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	order "ddyidc/internal/order/domain"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestMachineDestroyRefundMySQL covers the machine-level refund: the customer
// destroys one instance whose paid periods live in a purchase order and a
// completed renewal order, and the review must settle every invoice with its own
// share, credit the balance once and end the machine entitlement.
func TestMachineDestroyRefundMySQL(t *testing.T) {
	db := openRenewalBalanceDB(t)
	ctx := context.Background()
	customer, supplierID, productID := int64(95), int64(99034), int64(91031)
	hostID, orderID, itemID := int64(99031), int64(99032), int64(99033)
	renewalID, renewalOrderID := int64(99034), int64(99036)
	if err := db.Exec("INSERT INTO customer.customers(id,username,password_hash) VALUES(?,?,'!login-disabled')", customer, fmt.Sprintf("isolated-destroy-%d", customer)).Error; err != nil {
		t.Fatal(err)
	}
	seedGateSupplier(t, db, supplierID, productID)
	// Period boundaries are local midnights: the refund policy prorates by
	// calendar day, so a fixture pinned to the current instant would count a
	// used day whenever the test crosses local midnight.
	now := time.Now().In(time.Local)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	// The renewal period must not have started yet: the policy charges any
	// partial used day, so a period starting "today" would always refund less
	// than its paid amount.
	activated := day.AddDate(0, 0, -31)
	expiry := day.AddDate(0, 0, 1)
	purchase := order.Order{ID: orderID, OrderNo: fmt.Sprintf("DESTROY-%d", orderID), CustomerID: customer, TotalAmount: 10000, PayAmount: 10000, Status: order.OrderStatusPaid}
	if err := db.Create(&purchase).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&order.OrderItem{ID: itemID, OrderID: orderID, ProductID: productID, ProductName: "destroy fixture", ModuleType: "cloud", ConfigSnapshot: "{}", Qty: 1, UnitPrice: 10000, Subtotal: 10000, InstanceID: &hostID}).Error; err != nil {
		t.Fatal(err)
	}
	host := dom.Host{UpstreamProviderID: &supplierID, ID: hostID, InstanceNo: "DESTROY-ISOLATED", CustomerID: customer, OrderID: orderID, OrderItemID: itemID, ProductID: productID, ModuleType: "cloud", ConfigSnapshot: "{}", Status: 3, UpstreamInstanceID: "99037", OwnershipVersion: 1, ActivatedAt: &activated, ExpiresAt: &expiry}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	renewalOrder := order.Order{ID: renewalOrderID, OrderNo: fmt.Sprintf("REN-%d", renewalOrderID), CustomerID: customer, TotalAmount: 10000, PayAmount: 10000, Status: order.OrderStatusPaid}
	if err := db.Create(&renewalOrder).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&order.OrderItem{ID: 99038, OrderID: renewalOrderID, ProductID: productID, ProductName: "renewal fixture", ModuleType: "instance_renewal", ConfigSnapshot: "{}", Qty: 1, UnitPrice: 10000, Subtotal: 10000, InstanceID: &hostID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Renewal{ID: renewalID, OrderID: renewalOrderID, InstanceID: hostID, CustomerID: customer, OwnershipVersion: 1, HostID: host.UpstreamInstanceID, Cycle: "monthly", Duration: 1, Amount: 10000, Cost: 10000, BaseExpiry: expiry, State: "completed"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{orderID, renewalOrderID} {
		if _, e := billingapi.IssueSourceTx(db, "order", id, customer); e != nil {
			t.Fatalf("issue invoice for order %d: %v", id, e)
		}
	}
	renewedTo := expiry.AddDate(0, 0, 30)
	proof, _ := json.Marshal(map[string]any{"orderId": fmt.Sprint(renewalOrderID), "expiresAt": renewedTo.Format(time.RFC3339)})
	if err := db.Exec("INSERT INTO instance.instance_events(id,instance_id,event_type,operator_type,status,upstream_response,created_at) VALUES(?,?,'renew',3,2,?,NOW())", 99100, hostID, string(proof)).Error; err != nil {
		t.Fatal(err)
	}

	quoteService := NewService(repo.NewRepository(db), securityResolver{provider: &gateRenewalFixture{retail: "100.00", cost: "100.00", duration: 1, expiry: expiry}})
	quoteService.db = db
	view, err := quoteService.DestroyQuote(ctx, customer, hostID)
	_ = view
	if err != nil {
		t.Fatalf("destroy quote: %v", err)
	}
	if view.Paid != "200.00" {
		t.Fatalf("machine paid=%s want 200.00", view.Paid)
	}
	if view.Refundable == "0.00" || view.Refundable == "" {
		t.Fatalf("refundable=%s", view.Refundable)
	}

	application, err := workflow.SubmitInstance(ctx, db, customer, hostID, "客户申请销毁机器", "destroy-test-0001")
	if err != nil {
		t.Fatalf("submit destroy: %v", err)
	}
	detail, calc, _, err := workflow.MachineQuote(ctx, db, application.ID)
	if err != nil {
		t.Fatalf("machine quote: %v", err)
	}
	if calc.Paid != 20000 {
		t.Fatalf("quote paid=%s want 200.00", calc.Paid.String())
	}

	service := NewService(repo.NewRepository(db), securityResolver{provider: &gateRenewalFixture{retail: "100.00", cost: "100.00", duration: 1, expiry: expiry}})
	service.db = db
	actor := workflow.Actor{ID: 1, Name: "isolated", IP: "127.0.0.1"}
	action, err := service.MachineRefundAction(ctx, application.ID, actor, MachineRefundInput{
		Action: "direct_balance", AmountMode: "daily", RequestID: "destroy-action-0001",
		ExpectedAmount: calc.Refund.String(), ExpectedUpdatedAt: detail.UpdatedAt, Reason: "后台审核机器退款",
	})
	if err != nil {
		t.Fatalf("machine refund action: %v", err)
	}
	if action.State != "completed" {
		t.Fatalf("action state=%s", action.State)
	}

	var stored Renewal
	if err := db.Where("id=?", renewalID).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var destroyed dom.Host
	if err := db.Unscoped().Where("id=?", hostID).Take(&destroyed).Error; err != nil {
		t.Fatal(err)
	}
	if destroyed.Status != 6 {
		t.Fatalf("host status=%d want 6", destroyed.Status)
	}
	var refreshed dom.Host
	_ = db.Unscoped().Where("id=?", hostID).Take(&refreshed)
	var debits int64
	if err := db.Table("billing.transactions").Where("related_type='refund_application' AND related_id=? AND type='refund'", application.ID).Count(&debits).Error; err != nil {
		t.Fatal(err)
	}
	if debits != 1 {
		t.Fatalf("refund ledger rows=%d want 1", debits)
	}
	var account balance.Account
	if err := db.Where("customer_id=?", customer).Take(&account).Error; err != nil {
		t.Fatal(err)
	}
	if account.Balance != calc.Refund {
		t.Fatalf("balance=%s want %s", account.Balance.String(), calc.Refund.String())
	}
	var purchaseStatus, renewalStatus uint8
	if err := db.Raw("SELECT status FROM `order`.orders WHERE id=?", orderID).Scan(&purchaseStatus).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT status FROM `order`.orders WHERE id=?", renewalOrderID).Scan(&renewalStatus).Error; err != nil {
		t.Fatal(err)
	}
	if purchaseStatus != order.OrderStatusPaid || renewalStatus != order.OrderStatusRefunded {
		t.Fatalf("orders purchase=%d renewal=%d want paid/refunded", purchaseStatus, renewalStatus)
	}
	var invoices int64
	if err := db.Table("billing.payable_invoices").Where("status='refunded' AND source_id IN ?", []int64{orderID, renewalOrderID}).Count(&invoices).Error; err != nil {
		t.Fatal(err)
	}
	if invoices != 1 {
		t.Fatalf("refunded invoices=%d want 1", invoices)
	}
	var applicationStatus uint8
	if err := db.Table("payment.payment_refunds").Where("id=?", application.ID).Select("status").Scan(&applicationStatus).Error; err != nil {
		t.Fatal(err)
	}
	if applicationStatus != 4 {
		t.Fatalf("application status=%d want 4", applicationStatus)
	}
	t.Log("machine refund: fully used purchase period untouched, completed renewal refunded to balance, machine destroyed")
}
