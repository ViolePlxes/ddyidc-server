package instance

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	order "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRenewalQueueMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	ctx := context.Background()
	if err := snowflake.Configure(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO customer.customers(id,username,password_hash) VALUES(77,'isolated-renewal','!login-disabled')").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := balance.New(db).Change(ctx, balance.ChangeRequest{CustomerID: 77, Type: "manual_credit", Amount: "10.00", RefType: "customer", RefID: 77, Channel: "admin_manual", RequestID: "isolated-renew-fund", AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", ClientNotes: "测试客户可见原因", Notes: "isolated integration funds"}); err != nil {
		t.Fatal(err)
	}
	supplierID := int64(99004)
	seedGateSupplier(t, db, supplierID, 91001)
	host := dom.Host{UpstreamProviderID: &supplierID, ID: 99001, InstanceNo: "RENEW-ISOLATED", CustomerID: 77, ProductID: 91001, ModuleType: "cloud", ConfigSnapshot: "{}", Status: 3, UpstreamInstanceID: "99005", OwnershipVersion: 1}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	o := order.Order{ID: 99002, OrderNo: "REN-ISOLATED", CustomerID: 77, TotalAmount: 100, PayAmount: 100, Status: 1}
	if err := db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Create(&order.OrderItem{OrderID: o.ID, ProductID: host.ProductID, ProductName: "renewal fixture", ModuleType: "instance_renewal", ConfigSnapshot: "{}", Qty: 1, UnitPrice: 100, Subtotal: 100, InstanceID: &host.ID}).Error; err != nil {
		t.Fatal(err)
	}
	r := Renewal{SupplierID: supplierID, ID: 99003, OrderID: o.ID, InstanceID: host.ID, CustomerID: 77, OwnershipVersion: 1, HostID: host.UpstreamInstanceID, Cycle: "monthly", Duration: 1, Amount: 100, Cost: 100, BaseExpiry: time.Now(), State: "unpaid"}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(repo.NewRepository(db), securityResolver{provider: &gateRenewalFixture{retail: "1.00", cost: "1.00", duration: 1, expiry: r.BaseExpiry}})
	service.db = db
	// Exercise the real joined candidate query without running the renewal loop
	// or contacting a supplier. Schema-qualified aliases must retain soft deletes.
	if err := db.Model(&host).Update("expires_at", time.Now().Add(24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	setting := RenewalSettings{InstanceID: host.ID, CustomerID: 77, Enabled: true, UpdatedAt: time.Now().Add(-time.Hour)}
	if err := db.Create(&setting).Error; err != nil {
		t.Fatal(err)
	}
	candidates, e := service.autoRenewalCandidates(ctx)
	if e != nil || len(candidates) != 1 || candidates[0].ID != host.ID {
		t.Fatalf("candidate query: count=%d err=%v", len(candidates), e)
	}
	if err := db.Model(&setting).Update("customer_id", 78).Error; err != nil {
		t.Fatal(err)
	}
	candidates, e = service.autoRenewalCandidates(ctx)
	if e != nil || len(candidates) != 0 {
		t.Fatal("foreign renewal settings selected", e)
	}
	if err := db.Model(&setting).Updates(map[string]any{"customer_id": 77, "enabled": false}).Error; err != nil {
		t.Fatal(err)
	}
	candidates, e = service.autoRenewalCandidates(ctx)
	if e != nil || len(candidates) != 0 {
		t.Fatal("disabled renewal selected", e)
	}
	t.Log("real MySQL auto-renewal candidate join: eligible selected; ownership mismatch/disabled rejected; no supplier call")
	if err := db.Model(&o).Update("pay_amount", 101).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.PayRenewalBalance(ctx, 77, host.ID, o.ID, "127.0.0.1"); err == nil {
		t.Fatal("renewal accepted mismatching platform amount")
	}
	var rejectedDebits int64
	if err := db.Table("billing.transactions").Where("tx_no='instance-renewal-99003'").Count(&rejectedDebits).Error; err != nil || rejectedDebits != 0 {
		t.Fatal("mismatching renewal left balance debit", err, rejectedDebits)
	}
	if err := db.Model(&o).Update("pay_amount", 100).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := service.PayRenewalBalance(ctx, 77, host.ID, o.ID, "127.0.0.1"); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	var debits, jobs int64
	db.Table("billing.transactions").Where("tx_no='instance-renewal-99003'").Count(&debits)
	db.Table("system.durable_tasks").Where("task_key='renewal-balance:99003' AND status='pending'").Count(&jobs)
	var state int
	db.Raw("SELECT status FROM `order`.orders WHERE id=99002").Scan(&state)
	if debits != 1 || jobs != 1 || state != 2 {
		t.Fatalf("debits=%d tasks=%d state=%d", debits, jobs, state)
	}
	t.Log("12 concurrent balance renewal payments: one BalanceService debit + one durable intention, atomically paid; read-only fixture quote; no supplier mutation")
	freshExpiry := r.BaseExpiry.Add(60 * 24 * time.Hour).Truncate(time.Second)
	if err := db.Model(&dom.Host{}).Where("id=?", host.ID).Update("expires_at", freshExpiry).Error; err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"unpaid", "creating", "invoiced", "paying", "uncertain"} {
		if err := db.Model(&Renewal{}).Where("id=?", r.ID).Updates(map[string]any{"state": state, "invoice_id": "isolated-invoice"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := service.completeRenewal(ctx, r, freshExpiry); err == nil {
			t.Fatalf("expiry alone settled renewal without payment evidence: %s", state)
		}
	}
	if err := db.Model(&Renewal{}).Where("id=?", r.ID).Updates(map[string]any{"state": "verifying", "invoice_id": "isolated-invoice"}).Error; err != nil {
		t.Fatal(err)
	}
	t.Log("external expiry change cannot settle unpaid or uncertain renewal; successful invoice evidence required")
	if err := service.completeRenewal(ctx, r, r.BaseExpiry.Add(30*24*time.Hour)); err == nil {
		t.Fatal("stale renewal expiry accepted")
	}
	var unchanged dom.Host
	if err := db.Where("id=?", host.ID).Take(&unchanged).Error; err != nil || unchanged.ExpiresAt == nil || !unchanged.ExpiresAt.Equal(freshExpiry) {
		t.Fatal("stale renewal shortened instance lifetime", err)
	}
	if err := db.Model(&dom.Host{}).Where("id=?", host.ID).Update("upstream_provider_id", 123).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.completeRenewal(ctx, r, freshExpiry); err == nil {
		t.Fatal("stale completion accepted after supplier rebinding")
	}
	var stillPending Renewal
	if err := db.Where("id=?", r.ID).Take(&stillPending).Error; err != nil || stillPending.State != "verifying" {
		t.Fatal("rejected rebinding changed renewal state", err)
	}
	if err := db.Model(&dom.Host{}).Where("id=?", host.ID).Update("upstream_provider_id", host.UpstreamProviderID).Error; err != nil {
		t.Fatal(err)
	}
	t.Log("supplier rebinding rejects stale completion and preserves pending evidence")
	var reviewTask tasks.Task
	if err := db.Where("task_key='renewal-balance:99003'").Take(&reviewTask).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&reviewTask).Update("status", "unknown").Error; err != nil {
		t.Fatal(err)
	}
	reviewTask.Status = "unknown"
	if err := db.Where("id=?", r.ID).Take(&stillPending).Error; err != nil {
		t.Fatal(err)
	}
	// A remote read must not authorize a result after another executor or review
	// has changed the durable intention. Exercise each persisted CAS boundary.
	for _, changed := range []map[string]any{
		{"attempt": reviewTask.Attempt + 1},
		{"payload": "{}"},
		{"lease_until": time.Now().Add(time.Minute)},
	} {
		if err := db.Model(&tasks.Task{}).Where("id=?", reviewTask.ID).Updates(changed).Error; err != nil {
			t.Fatal(err)
		}
		if err := service.finishRenewalReview(ctx, reviewTask, stillPending, freshExpiry, 1, "127.0.0.1", "stale isolated review"); err == nil {
			t.Fatalf("changed task accepted: %v", changed)
		}
		if err := db.Model(&tasks.Task{}).Where("id=?", reviewTask.ID).Updates(map[string]any{"attempt": reviewTask.Attempt, "payload": reviewTask.Payload, "lease_until": nil}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&Renewal{}).Where("id=?", r.ID).Update("invoice_id", "changed-isolated-invoice").Error; err != nil {
		t.Fatal(err)
	}
	if err := service.finishRenewalReview(ctx, reviewTask, stillPending, freshExpiry, 1, "127.0.0.1", "changed invoice review"); err == nil {
		t.Fatal("changed renewal invoice accepted")
	}
	if err := db.Model(&Renewal{}).Where("id=?", r.ID).Update("invoice_id", stillPending.InvoiceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id=?", r.ID).Take(&stillPending).Error; err != nil {
		t.Fatal(err)
	}
	var rejectedAudits int64
	if err := db.Table("system.task_reconciliations").Where("task_id=?", reviewTask.ID).Count(&rejectedAudits).Error; err != nil || rejectedAudits != 0 {
		t.Fatal("rejected stale review left success audit", err, rejectedAudits)
	}
	t.Log("changed task attempt/payload/lease and renewal invoice reject stale review without success audit")
	if err := db.Exec("CREATE TRIGGER system.reject_renewal_review BEFORE INSERT ON system.task_reconciliations FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated audit failure'").Error; err != nil {
		t.Fatal(err)
	}
	if err := service.finishRenewalReview(ctx, reviewTask, stillPending, freshExpiry, 1, "127.0.0.1", "isolated persisted payment verification"); err == nil {
		t.Fatal("audit failure accepted")
	}
	if err := db.Exec("DROP TRIGGER system.reject_renewal_review").Error; err != nil {
		t.Fatal(err)
	}
	var rolled Renewal
	if err := db.Where("id=?", r.ID).Take(&rolled).Error; err != nil || rolled.State != "verifying" {
		t.Fatal("audit failure did not roll back renewal", err)
	}
	if err := service.finishRenewalReview(ctx, reviewTask, stillPending, freshExpiry, 1, "127.0.0.1", "isolated persisted payment verification"); err != nil {
		t.Fatal(err)
	}
	if err := service.finishRenewalReview(ctx, reviewTask, stillPending, freshExpiry, 1, "127.0.0.1", "duplicate review"); err == nil {
		t.Fatal("duplicate review accepted")
	}
	var audits int64
	if err := db.Table("system.task_reconciliations").Where("task_id=?", reviewTask.ID).Count(&audits).Error; err != nil || audits != 1 {
		t.Fatal("expected single review audit", err, audits)
	}
	t.Log("renewal completion and task review atomic: audit failure rolls back; duplicate rejected; no supplier write")
	_ = service.renewalUncertain(ctx, r.ID, "late isolated timeout")
	var completed Renewal
	if err := db.Where("id=?", r.ID).Take(&completed).Error; err != nil || completed.State != "completed" || completed.LastError != "" {
		t.Fatal("late executor overwrote completed renewal", err)
	}
	t.Log("late executor error cannot overwrite completed renewal")
	o.ID = 99004
	o.OrderNo = "REN-ROLLBACK"
	o.Status = 1
	if err := db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	r.ID = 99005
	r.OrderID = o.ID
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER system.reject_test_task BEFORE INSERT ON system.durable_tasks FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated queue failure'").Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP TRIGGER IF EXISTS system.reject_test_task")
	if err := service.PayRenewalBalance(ctx, 77, host.ID, o.ID, "127.0.0.1"); err == nil {
		t.Fatal("queue write failure accepted")
	}
	db.Table("billing.transactions").Where("tx_no='instance-renewal-99005'").Count(&debits)
	db.Raw("SELECT status FROM `order`.orders WHERE id=99004").Scan(&state)
	if debits != 0 || state != 1 {
		t.Fatal("queue failure failed to roll back balance/order", debits, state)
	}
	t.Log("forced durable queue failure rolls back balance debit and paid order together")
}
