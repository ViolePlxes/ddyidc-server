package groupbuy

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	orderservice "ddyidc/internal/order/service"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/db"
	"github.com/zeromicro/go-zero/core/conf"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type integrationOrders struct {
	db  *gorm.DB
	mu  sync.Mutex
	ids []int64
}

func (f *integrationOrders) CreateOrder(ctx context.Context, customerID int64, input *orderservice.CreateOrderInput) (*orderservice.OrderView, error) {
	productID, _ := strconv.ParseInt(input.ProductID, 10, 64)
	orderID, itemID := snowflake.GenID(), snowflake.GenID()
	now := time.Now()
	err := f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT INTO `order`.orders (id,order_no,customer_id,total_amount,pay_amount,status,created_at,updated_at) VALUES (?,?,?,?,?,1,?,?)", orderID, fmt.Sprintf("GBT%d", orderID), customerID, 1000, 1000, now, now).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO `order`.order_items (id,order_id,product_id,product_name,module_type,config_snapshot,duration_months,qty,unit_price,subtotal,delivery_status,created_at,updated_at) VALUES (?,?,?,?,?,'{}',1,1,1000,1000,0,?,?)", itemID, orderID, productID, "拼团测试商品", "test", now, now).Error
	})
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.ids = append(f.ids, orderID)
	f.mu.Unlock()
	return &orderservice.OrderView{ID: strconv.FormatInt(orderID, 10)}, nil
}

func (f *integrationOrders) ProvisionAfterPayment(ctx context.Context, _ int64, orderID int64, _ string) error {
	instanceID := snowflake.GenID()
	return f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("order.order_items").Where("order_id=?", orderID).Updates(map[string]any{"instance_id": instanceID, "delivery_status": 2}).Error; err != nil {
			return err
		}
		return tx.Table("order.orders").Where("id=?", orderID).Update("status", 6).Error
	})
}

func openIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("DDYIDC_GROUPBUY_INTEGRATION") != "1" {
		t.Skip("set DDYIDC_GROUPBUY_INTEGRATION=1 to run MySQL lifecycle tests")
	}
	var c struct{ MySQL db.Config }
	conf.MustLoad(filepath.Join("..", "..", "etc", "config.yaml"), &c)
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&multiStatements=true", c.MySQL.User, c.MySQL.Password, c.MySQL.Host, c.MySQL.Port, c.MySQL.Database)
	database, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func TestIntegrationLifecycle(t *testing.T) {
	database := openIntegrationDB(t)
	ctx := context.Background()
	fake := &integrationOrders{db: database}
	service := NewService(database, fake, nil)
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
		fake.mu.Lock()
		orderIDs := append([]int64(nil), fake.ids...)
		fake.mu.Unlock()
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
	if err := database.Exec("INSERT INTO product.products (id,tenant_id,group_id,name,module_type,pay_type,stock,sold_count,status,hidden,created_at,updated_at) VALUES (?,0,0,'拼团测试商品','test',1,20,0,1,0,?,?)", productID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	for index, id := range clientIDs {
		if err := database.Exec("INSERT INTO customer.customers (id,tenant_id,username,password_hash,status,level,created_at,updated_at) VALUES (?,0,?,'test',1,1,?,?)", id, fmt.Sprintf("gbtest%d-%d", index, id), now, now).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := balance.New(database).Change(ctx, balance.ChangeRequest{CustomerID: id, Type: "manual_credit", Amount: "1000.00", RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: fmt.Sprintf("GBTEST:%d", id), AdminID: 1, AdminName: "integration", OperatorIP: "127.0.0.1", Notes: "测试资金"}); err != nil {
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
	refundDetail, _ := service.Group(ctx, refundLeader.ID, 0, true)
	if refundDetail.Leader.CurrentNum != 1 {
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
	if timedOut.Status != 3 {
		t.Fatalf("timeout status=%d want 3", timedOut.Status)
	}
}
