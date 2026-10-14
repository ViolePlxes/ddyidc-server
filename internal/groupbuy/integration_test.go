package groupbuy

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/payment/repository/refundworkflow"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	instance "ddyidc/internal/instance/domain"
	orderrepo "ddyidc/internal/order/repository"
	orderservice "ddyidc/internal/order/service"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL lifecycle test required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe MySQL socket")
	}
	dsn := "root@unix(" + socket + ")/?parseTime=true"
	database, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := database.DB()
	t.Cleanup(func() { pool.Close() })
	if err := snowflake.Configure(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestIntegrationLifecycle(t *testing.T) {
	database := openIntegrationDB(t)
	ctx := context.Background()
	service := NewService(database, orderservice.NewService(orderrepo.NewRepository(database), nil), nil)
	if err := database.Exec("UPDATE system.system_configs SET value='1' WHERE `key`='groupbuy.enabled'").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("UPDATE system.system_configs SET value='{\"enabled\":false}' WHERE `key`='certification.settings'").Error; err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	productID := snowflake.GenID()
	clientIDs := make([]int64, 9)
	for index := range clientIDs {
		clientIDs[index] = snowflake.GenID()
	}
	activityIDs := make([]int64, 0, 3)
	defer func() {
		var orderIDs []int64
		database.Table("order.orders").Where("customer_id IN ?", clientIDs).Pluck("id", &orderIDs)
		if len(orderIDs) > 0 {
			database.Exec("DELETE FROM `order`.order_items WHERE order_id IN ?", orderIDs)
			database.Exec("DELETE FROM `order`.orders WHERE id IN ?", orderIDs)
		}
		database.Exec("DELETE FROM system.groupbuy_records WHERE activity_id IN ?", activityIDs)
		database.Exec("DELETE FROM system.groupbuy_activities WHERE id IN ?", activityIDs)
		database.Exec("DELETE FROM billing.transactions WHERE customer_id IN ?", clientIDs)
		database.Exec("DELETE FROM billing.accounts WHERE customer_id IN ?", clientIDs)
		database.Exec("DELETE FROM customer.customers WHERE id IN ?", clientIDs)
		database.Exec("DELETE FROM product.products WHERE id=?", productID)
	}()

	now := time.Now()
	if err := database.Exec("INSERT INTO product.products (id,tenant_id,group_id,name,module_type,pay_type,stock,sold_count,status,hidden,created_at,updated_at) VALUES (?,0,0,'拼团测试商品','cloud',1,20,0,1,0,?,?)", productID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("INSERT INTO product.product_specs(id,product_id,name,vcpu,memory,price_mode,price) VALUES(?,?,'isolated',1,'1 GB','fixed',10.00)", snowflake.GenID(), productID).Error; err != nil {
		t.Fatal(err)
	}
	for index, id := range clientIDs {
		if err := database.Exec("INSERT INTO customer.customers (id,tenant_id,username,password_hash,status,level,created_at,updated_at) VALUES (?,0,?,'test',1,1,?,?)", id, fmt.Sprintf("gbtest%d-%d", index, id), now, now).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := balance.New(database).Change(ctx, balance.ChangeRequest{CustomerID: id, Type: "manual_credit", Amount: "1000.00", RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: fmt.Sprintf("GBTEST:%d", id), AdminID: 1, AdminName: "integration", OperatorIP: "127.0.0.1", ClientNotes: "测试客户可见原因", Notes: "测试资金"}); err != nil {
			t.Fatal(err)
		}
	}
	settings, _ := service.Settings(ctx)
	originalNewUserLimit := settings.NewUserGroupLimit
	settings.NewUserGroupLimit = 0
	if err := service.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	defer func() { settings.NewUserGroupLimit = originalNewUserLimit; _ = service.SaveSettings(ctx, settings) }()

	newActivity := func() *Activity {
		value := int64(1000)
		activity := &Activity{ProductID: productID, BillingCycle: "monthly", ConfigOptions: []byte(`{"durationMonth":1,"quantity":1,"billingCycle":"monthly","configOptions":{}}`), OriginalPrice: 2000, PriceMode: 1, TwoPrice: &value, ThreePrice: &value, FivePrice: &value, RenewType: 1, TimeoutDays: 3, EndTime: now.Add(24 * time.Hour), Status: 1, IsPublicGroupEnabled: true}
		if err := service.SaveActivity(ctx, activity); err != nil {
			t.Fatal(err)
		}
		activityIDs = append(activityIDs, activity.ID)
		return activity
	}

	activity := newActivity()
	leader, err := service.StartGroup(ctx, clientIDs[0], StartInput{ActivityID: activity.ID, GroupType: "five", IsPublic: true})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range clientIDs[1:3] {
		wg.Add(1)
		go func(clientID int64) {
			defer wg.Done()
			_, joinErr := service.JoinGroup(ctx, clientID, leader.ID, "", nil)
			errs <- joinErr
		}(id)
	}
	wg.Wait()
	close(errs)
	for joinErr := range errs {
		if joinErr != nil {
			t.Fatal(joinErr)
		}
	}
	if _, err = service.JoinGroup(ctx, clientIDs[3], leader.ID, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = service.JoinGroup(ctx, clientIDs[4], leader.ID, "", nil); err != nil {
		t.Fatal(err)
	}
	if err = service.CompleteGroup(ctx, leader.ID); err != nil {
		t.Fatal(err)
	}
	var delivering []Record
	if err = database.Where("leader_id=?", leader.ID).Find(&delivering).Error; err != nil {
		t.Fatal(err)
	}
	for _, member := range delivering {
		if member.Status != 4 || member.OrderID == nil {
			t.Fatal("group skipped durable delivery state")
		}
		if err = database.Table("order.orders").Where("id=?", *member.OrderID).Update("status", 6).Error; err != nil {
			t.Fatal(err)
		}
		hostID := snowflake.GenID()
		if err = database.Create(&instance.Host{ID: hostID, InstanceNo: fmt.Sprint("GBTEST", hostID), CustomerID: member.ClientID, OrderID: *member.OrderID, ProductID: productID, ModuleType: "cloud", ConfigSnapshot: "{}", Status: 3}).Error; err != nil {
			t.Fatal(err)
		}
		if err = service.SyncDeliveredOrder(ctx, member.ClientID, *member.OrderID); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := service.Group(ctx, leader.ID, 0, true)
	if err != nil || detail.Leader.Status != 2 || len(detail.Members) != 5 {
		t.Fatalf("completed group invalid: status=%d members=%d err=%v", detail.Leader.Status, len(detail.Members), err)
	}
	if err := service.Refund(ctx, leader.ID, true); err == nil {
		t.Fatal("successful group must not be refundable")
	}

	refundActivity := newActivity()
	refundLeader, err := service.StartGroup(ctx, clientIDs[5], StartInput{ActivityID: refundActivity.ID, GroupType: "three", IsPublic: true})
	if err != nil {
		t.Fatal(err)
	}
	member, err := service.JoinGroup(ctx, clientIDs[6], refundLeader.ID, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Refund(ctx, member.ID, false); err != nil {
		t.Fatal(err)
	}
	var initial refundworkflow.Request
	if err := database.Where("source='groupbuy' AND object_id=?", member.ID).Take(&initial).Error; err != nil {
		t.Fatal(err)
	}
	application, err := refundworkflow.Load(ctx, database, initial.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refundworkflow.Review(ctx, database, initial.ApplicationID, refundworkflow.Actor{ID: 1, Name: "integration", IP: "127.0.0.1"}, refundworkflow.ReviewInput{Action: "reject", Reason: "隔离测试驳回后允许重新申请", RequestID: fmt.Sprintf("GREJECT:%d", initial.ApplicationID), ExpectedUpdatedAt: application.UpdatedAt}); err != nil {
		t.Fatal(err)
	}
	if err := service.refund(ctx, member.ID, false); err != nil {
		t.Fatal(err)
	}
	var requests int64
	database.Model(&refundworkflow.Request{}).Where("source='groupbuy' AND object_id=?", member.ID).Count(&requests)
	if requests != 1 {
		t.Fatal("automatic retry created a new rejected refund application")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.Refund(ctx, member.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	database.Model(&refundworkflow.Request{}).Where("source='groupbuy' AND object_id=?", member.ID).Count(&requests)
	if requests != 2 {
		t.Fatalf("manual resubmission and replay created %d applications, want 2", requests)
	}
	refundDetail, _ := service.Group(ctx, refundLeader.ID, 0, true)
	if refundDetail.Leader.CurrentNum != 2 {
		t.Fatalf("member refund current_num=%d", refundDetail.Leader.CurrentNum)
	}
	if err := service.Refund(ctx, refundLeader.ID, true); err != nil {
		t.Fatal(err)
	}

	timeoutActivity := newActivity()
	timeoutLeader, err := service.StartGroup(ctx, clientIDs[7], StartInput{ActivityID: timeoutActivity.ID, GroupType: "two", IsPublic: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Table(recordTable).Where("id=?", timeoutLeader.ID).Update("timeout_time", now.Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	var original struct{ Value string }
	database.Table("system.system_configs").Select("value").Where("`key`='groupbuy.enabled'").Take(&original)
	database.Table("system.system_configs").Where("`key`='groupbuy.enabled'").Update("value", "1")
	defer database.Table("system.system_configs").Where("`key`='groupbuy.enabled'").Update("value", original.Value)
	if err := service.RunMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	var timedOut Record
	database.Table(recordTable).Where("id=?", timeoutLeader.ID).Take(&timedOut)
	if timedOut.Status != 1 {
		t.Fatalf("timeout status=%d want pending review without automatic refund", timedOut.Status)
	}
}
