package order_test

import (
	"context"
	marketingapi "ddyidc/internal/marketing/api"
	marketingrepo "ddyidc/internal/marketing/repository"
	marketingsvc "ddyidc/internal/marketing/service"
	orderrepo "ddyidc/internal/order/repository"
	ordersvc "ddyidc/internal/order/service"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
)

func TestZeroPayableMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := database.DB()
	defer pool.Close()
	ctx := context.Background()
	if err := snowflake.Configure(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"UPDATE system.system_configs SET value='{\"enabled\":false}' WHERE `key`='certification.settings'",
		"INSERT INTO product.products(id,group_id,name,module_type) VALUES(91001,1,'isolated zero-price test','cloud')",
		"INSERT INTO product.product_specs(id,product_id,name,vcpu,memory,price_mode,price) VALUES(91002,91001,'isolated',1,'1 GB','fixed',1.00)",
		"INSERT INTO marketing.coupons(id,name,type,face_value,min_amount) VALUES(91003,'isolated full discount',1,100,0)",
		"INSERT INTO marketing.user_coupons(id,user_id,coupon_id,code,expire_at) VALUES(91004,77,91003,'F04-ZERO-COUPON',DATE_ADD(NOW(),INTERVAL 1 DAY))",
	} {
		if err := database.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := ordersvc.NewService(orderrepo.NewRepository(database), marketingapi.NewOrderHooks(marketingsvc.NewService(database, marketingrepo.NewRepository(database))))
	input := &ordersvc.CreateOrderInput{ProductID: "91001", DurationMonth: 1, Quantity: 1, UserCouponID: "91004", BillingCycle: "monthly"}
	order, err := service.CreateOrder(ctx, 77, input)
	if err != nil {
		t.Fatal(err)
	}
	if order.PayAmount != 0 || order.Status != 2 || order.PayType != nil {
		t.Fatalf("zero payable not settled without channel: %+v", order)
	}
	var tasks, payments, used int64
	database.Table("system.durable_tasks").Where("task_key=?", "zero-order:"+order.ID).Count(&tasks)
	database.Table("payment.payments").Where("biz_type='order' AND biz_id=?", order.ID).Count(&payments)
	database.Raw("SELECT used_count FROM marketing.coupons WHERE id=91003").Scan(&used)
	if tasks != 1 || payments != 0 || used != 1 {
		t.Fatalf("tasks=%d payments=%d coupon_used=%d", tasks, payments, used)
	}
	if _, err = service.CreateOrder(ctx, 77, input); err == nil {
		t.Fatal("reused discount voucher")
	}
	var orders int64
	database.Table("order.orders").Where("customer_id=77 AND pay_amount=0").Count(&orders)
	if orders != 1 {
		t.Fatal("failed discount left another order", orders)
	}
	t.Log("real coupon transaction: full discount -> paid order + durable task, zero channel payments; reused coupon rolls back order; no supplier called")
}
