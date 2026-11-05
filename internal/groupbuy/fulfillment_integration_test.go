package groupbuy

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	instance "ddyidc/internal/instance/domain"
	orderrepo "ddyidc/internal/order/repository"
	orders "ddyidc/internal/order/service"
	"ddyidc/internal/shared/snowflake"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDurableGroupMySQL(t *testing.T) {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Exec("UPDATE system.system_configs SET value='{\"enabled\":false}' WHERE `key`='certification.settings'").Error)
	must(db.Exec("UPDATE system.system_configs SET value='1' WHERE `key`='groupbuy.enabled'").Error)
	must(db.Exec("UPDATE system.groupbuy_settings SET new_user_group_limit=0").Error)
	must(db.Exec("INSERT INTO product.products(id,group_id,name,module_type,stock) VALUES(93001,1,'isolated group test','cloud',4)").Error)
	must(db.Exec("INSERT INTO product.product_specs(id,product_id,name,vcpu,memory,price_mode,price) VALUES(93002,93001,'isolated',1,'1 GB','fixed',1.00)").Error)
	for _, id := range []int64{93011, 93012, 93013, 93014} {
		must(db.Exec("INSERT INTO customer.customers(id,username,password_hash,status) VALUES(?,?,?,1)", id, fmt.Sprint("isolated-group-", id), "disabled-test-login").Error)
		_, err := balance.New(db).Change(ctx, balance.ChangeRequest{CustomerID: id, Type: "manual_credit", Amount: "10.00", RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: fmt.Sprint("group-seed:", id), AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", ClientNotes: "测试客户可见原因", Notes: "isolated test"})
		must(err)
	}
	svc := NewService(db, orders.NewService(orderrepo.NewRepository(db), nil), nil)
	price := int64(100)
	activity := &Activity{ProductID: 93001, BillingCycle: "monthly", ConfigOptions: []byte(`{"durationMonth":1,"quantity":1,"billingCycle":"monthly","configOptions":{}}`), OriginalPrice: 100, PriceMode: 1, TwoPrice: &price, ThreePrice: &price, FivePrice: &price, RenewType: 1, TimeoutDays: 3, EndTime: time.Now().Add(time.Hour), Status: 1}
	must(svc.SaveActivity(ctx, activity))
	leader, err := svc.StartGroup(ctx, 93011, StartInput{ActivityID: activity.ID, GroupType: "two"})
	must(err)
	_, err = svc.JoinGroup(ctx, 93012, leader.ID, "", nil)
	must(err)
	var count int64
	must(db.Table("system.durable_tasks").Where("task_key=?", fmt.Sprint("groupbuy-plan:", leader.ID)).Count(&count).Error)
	if count != 1 {
		t.Fatal("missing atomic group intent")
	}
	expired, err := svc.StartGroup(ctx, 93013, StartInput{ActivityID: activity.ID, GroupType: "two"})
	must(err)
	must(db.Model(&Record{}).Where("id=?", expired.ID).Update("timeout_time", time.Now().Add(-time.Minute)).Error)
	must(db.Exec("CREATE TRIGGER system.reject_group_delivery BEFORE INSERT ON system.durable_tasks FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated task insertion failure'").Error)
	defer func() {
		db.Exec("DROP TRIGGER IF EXISTS system.reject_group_delivery")
		db.Exec("DROP TRIGGER IF EXISTS payment.reject_group_refund")
	}()
	if svc.CompleteGroup(ctx, leader.ID) == nil {
		t.Fatal("task failure did not abort planning")
	}
	must(db.Exec("CREATE TRIGGER payment.reject_group_refund BEFORE INSERT ON payment.todo_refund_requests FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated refund application failure'").Error)
	if svc.refundGroup(ctx, expired.ID) == nil {
		t.Fatal("application insertion failure did not roll back refund submission")
	}
	var unchanged Record
	must(db.First(&unchanged, expired.ID).Error)
	if unchanged.Status != 1 {
		t.Fatal("refund fact committed without notification intention")
	}
	var refundFlows int64
	must(db.Table("billing.transactions").Where("tx_no=?", fmt.Sprintf("GB:groupbuy_refund:%d", expired.ID)).Count(&refundFlows).Error)
	if refundFlows != 0 {
		t.Fatal("failed refund notification left balance mutation")
	}
	must(db.Exec("DROP TRIGGER system.reject_group_delivery").Error)
	must(db.Exec("DROP TRIGGER payment.reject_group_refund").Error)
	must(db.Table("order.orders").Where("customer_id IN (93011,93012)").Count(&count).Error)
	if count != 0 {
		t.Fatal("failed plan left partial paid order")
	}
	// Disable only this isolated database; accepted groups must still complete.
	must(db.Exec("UPDATE system.system_configs SET value='0' WHERE `key`='groupbuy.enabled'").Error)
	if _, err := svc.StartGroup(ctx, 93013, StartInput{ActivityID: activity.ID, GroupType: "two"}); err == nil {
		t.Fatal("disabled plugin accepted new business")
	}
	must(svc.RunMaintenance(ctx))
	var expiredRow Record
	must(db.First(&expiredRow, expired.ID).Error)
	if expiredRow.Status != 1 {
		t.Fatal("timeout automatically completed a refund")
	}
	must(db.Table("payment.todo_refund_requests").Where("source='groupbuy' AND object_id=?", expired.ID).Count(&count).Error)
	if count != 1 {
		t.Fatal("timeout did not queue exactly one reviewed refund application")
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.CompleteGroup(ctx, leader.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// Joining paid once; delivery planning must reuse, not issue another bill.
	var invoiceCount int64
	must(db.Table("billing.payable_invoices").Where("customer_id IN (93011,93012)").Count(&invoiceCount).Error)
	if invoiceCount != 2 {
		t.Fatal("group delivery duplicated/lost paid invoices", invoiceCount)
	}
	var paidInvoiceCount int64
	must(db.Table("billing.payable_invoices").Where("customer_id IN (93011,93012) AND source_type='groupbuy' AND status='paid' AND amount_due=1.00 AND amount_paid=1.00").Count(&paidInvoiceCount).Error)
	if paidInvoiceCount != 2 {
		t.Fatal("group payment was not recorded in billing", paidInvoiceCount)
	}
	var members []Record
	must(db.Where("leader_id=?", leader.ID).Order("id ASC").Find(&members).Error)
	if len(members) != 2 {
		t.Fatal("member count")
	}
	for _, m := range members {
		if m.Status != 4 || m.OrderID == nil || m.HostID != nil {
			t.Fatal("enqueue incorrectly marked success")
		}
	}
	must(db.Table("order.orders").Where("customer_id IN (93011,93012)").Count(&count).Error)
	if count != 2 {
		t.Fatal("duplicate orders", count)
	}
	if svc.Refund(ctx, leader.ID, true) == nil {
		t.Fatal("delivery refunded without remote reconciliation")
	}
	for index, m := range members {
		must(db.Table("order.orders").Where("id=?", *m.OrderID).Update("status", 6).Error)
		must(db.Create(&instance.Host{ID: int64(93100 + index), InstanceNo: fmt.Sprint("GROUP", index), CustomerID: m.ClientID, OrderID: *m.OrderID, ProductID: 93001, ModuleType: "cloud", ConfigSnapshot: "{}", Status: 3}).Error)
		must(svc.SyncDeliveredOrder(ctx, m.ClientID, *m.OrderID))
		var check Record
		must(db.First(&check, leader.ID).Error)
		if index == 0 && check.Status != 4 {
			t.Fatal("partial delivery marked group complete")
		}
		if index == 1 && check.Status != 2 {
			t.Fatal("complete facts not reflected")
		}
	}
	t.Log("isolated real MySQL: charge + group intent, 8 planners -> two linked paid orders, disabled gate blocks new groups but allows accepted delivery, partial delivery stays pending, complete local facts finish group; no supplier call")
}
