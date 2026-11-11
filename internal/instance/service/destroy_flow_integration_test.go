package instance

import (
	"context"
	domain "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	order "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestDestroyFlowMySQL covers the four customer-facing rules: a machine with a
// pending request leaves the destroyable list, a rejected request comes back
// with its last reason, an enabled production lock or auto renewal blocks both
// the list action and a direct submission, and the list quote equals the frozen
// refund amount written on submission.
func TestDestroyFlowMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	customer, hostID, orderID, itemID := int64(998401), int64(998402), int64(998403), int64(998404)
	email := fmt.Sprintf("destroy-%d@example.invalid", customer)
	if err := db.Exec("INSERT INTO customer.customers(id,username,password_hash,email) VALUES(?,?, '!login-disabled',?)", customer, fmt.Sprintf("isolated-destroy-flow-%d", customer), email).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	activated := now.Add(-11*24*time.Hour - 12*time.Hour)
	expiry := now.Add(18*24*time.Hour + 12*time.Hour)
	if err := db.Create(&order.Order{ID: orderID, OrderNo: fmt.Sprintf("DESTROY-FLOW-%d", orderID), CustomerID: customer, TotalAmount: 10000, PayAmount: 10000, Status: order.OrderStatusPaid}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&order.OrderItem{ID: itemID, OrderID: orderID, ProductID: 91031, ProductName: "destroy flow fixture", ModuleType: "cloud", ConfigSnapshot: `{"billingDays":30,"billingCycle":"monthly"}`, Qty: 1, UnitPrice: 10000, Subtotal: 10000, InstanceID: &hostID}).Error; err != nil {
		t.Fatal(err)
	}
	host := domain.Host{ID: hostID, InstanceNo: "DESTROY-FLOW", ProductName: "销毁流程测试机", CustomerID: customer, OrderID: orderID, OrderItemID: itemID, ProductID: 91031, ModuleType: "cloud", ConfigSnapshot: "{}", Status: 3, UpstreamInstanceID: "998405", PrimaryIP: "203.0.113.10", OwnershipVersion: 1, ActivatedAt: &activated, ExpiresAt: &expiry}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}

	s := NewService(repo.NewRepository(db), nil)
	s.db = db
	verifier := &lockEmailTestVerifier{codes: map[string]string{}}
	s.SetProductionLockEmailVerifier(verifier)

	options, err := s.DestroyOptions(ctx, customer)
	if err != nil {
		t.Fatalf("destroy options: %v", err)
	}
	option := findDestroyOption(options, hostID)
	if option == nil || !option.Eligible || option.Refundable == "" || option.Refundable == "0.00" {
		t.Fatalf("eligible option missing: %+v", option)
	}

	target, scope, err := s.ProductionLockEmailTarget(ctx, customer, hostID, false, "")
	if err != nil {
		t.Fatalf("production lock email target: %v", err)
	}
	verifier.codes[target+scope] = "654321"
	application, err := s.SubmitDestroy(ctx, customer, hostID, "客户申请销毁", "destroy-flow-0001", DestroyEmailInput{Code: "654321"})
	if err != nil {
		t.Fatalf("submit destroy: %v", err)
	}
	// The quote and the frozen submission amount share one calculation instant.
	if application.RefundAmount != option.Refundable {
		t.Fatalf("submitted amount=%s quote=%s", application.RefundAmount, option.Refundable)
	}
	history, err := s.DestroyRequests(ctx, customer)
	if err != nil {
		t.Fatalf("destroy requests: %v", err)
	}
	if len(history) != 1 || history[0].ProductName != "销毁流程测试机" || history[0].InstanceNo != "DESTROY-FLOW" || history[0].IP != "203.0.113.10" || history[0].Amount != application.RefundAmount {
		t.Fatalf("history projection incomplete: %+v", history)
	}
	options, err = s.DestroyOptions(ctx, customer)
	if err != nil {
		t.Fatalf("options after submit: %v", err)
	}
	if findDestroyOption(options, hostID) != nil {
		t.Fatal("pending machine still listed as destroyable")
	}
	if _, err := s.SubmitDestroy(ctx, customer, hostID, "重复提交", "destroy-flow-0002", DestroyEmailInput{Code: "000000"}); err == nil || !strings.Contains(err.Error(), "已有销毁申请在审核中") {
		t.Fatalf("duplicate submission accepted: %v", err)
	}

	if err := db.Table("payment.payment_refunds").Where("id=?", application.ID).Updates(map[string]any{"status": 2, "audit_remark": "资料不完整"}).Error; err != nil {
		t.Fatal(err)
	}
	options, err = s.DestroyOptions(ctx, customer)
	if err != nil {
		t.Fatalf("options after rejection: %v", err)
	}
	option = findDestroyOption(options, hostID)
	if option == nil || option.LastRejectReason != "资料不完整" {
		t.Fatalf("rejected machine missing or reason lost: %+v", option)
	}

	if err := db.Exec("INSERT INTO instance.instance_production_locks(instance_id,enabled,updated_at) VALUES(?,1,?)", hostID, now).Error; err != nil {
		t.Fatal(err)
	}
	options, err = s.DestroyOptions(ctx, customer)
	if err != nil {
		t.Fatalf("options with production lock: %v", err)
	}
	option = findDestroyOption(options, hostID)
	if option == nil || option.Eligible || option.Reason != "已开启生产锁，请先关闭生产锁" {
		t.Fatalf("production lock not listed as blocked: %+v", option)
	}
	if _, err := s.SubmitDestroy(ctx, customer, hostID, "生产锁下销毁", "destroy-flow-0003", DestroyEmailInput{}); err == nil || err.Error() != "已开启生产锁，请先关闭" {
		t.Fatalf("production lock submission not refused before mailbox proof: %v", err)
	}

	if err := db.Exec("DELETE FROM instance.instance_production_locks WHERE instance_id=?", hostID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO instance.instance_renewal_settings(instance_id,customer_id,enabled,updated_at) VALUES(?,?,1,?)", hostID, customer, now).Error; err != nil {
		t.Fatal(err)
	}
	options, err = s.DestroyOptions(ctx, customer)
	if err != nil {
		t.Fatalf("options with auto renewal: %v", err)
	}
	option = findDestroyOption(options, hostID)
	if option == nil || option.Eligible || option.Reason != "已开启自动续费，请先关闭自动续费" {
		t.Fatalf("auto renewal not listed as blocked: %+v", option)
	}
	if _, err := s.SubmitDestroy(ctx, customer, hostID, "自动续费下销毁", "destroy-flow-0004", DestroyEmailInput{}); err == nil || err.Error() != "已开启自动续费，请先关闭" {
		t.Fatalf("auto renewal submission not refused before mailbox proof: %v", err)
	}

	if err := db.Exec("DELETE FROM instance.instance_renewal_settings WHERE instance_id=?", hostID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitDestroy(ctx, customer, hostID, "缺少验证码", "destroy-flow-0005", DestroyEmailInput{}); err == nil || !strings.Contains(err.Error(), "请先完成邮箱验证码验证") {
		t.Fatalf("missing email code accepted: %v", err)
	}
}

func findDestroyOption(options []DestroyOption, id int64) *DestroyOption {
	for index := range options {
		if options[index].InstanceID == id {
			return &options[index]
		}
	}
	return nil
}
