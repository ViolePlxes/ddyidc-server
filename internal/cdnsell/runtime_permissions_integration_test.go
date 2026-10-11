package cdnsell

import (
	"context"
	orderdom "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/testenv"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"testing"
	"time"
)

func TestCDNRestrictedRuntimeMySQL(t *testing.T) {
	root := testenv.MySQL(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	cdnFixture(t, root, 895001, 895101, now)
	if err := root.Exec("UPDATE system.cdnsell_settings SET enabled=1 WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	defer root.Exec("UPDATE system.cdnsell_settings SET enabled=0 WHERE id=1")
	if err := root.Exec("CREATE USER 'cdn_runtime_test'@'localhost' IDENTIFIED BY 'isolated-runtime-only'").Error; err != nil {
		t.Fatal(err)
	}
	defer root.Exec("DROP USER 'cdn_runtime_test'@'localhost'")
	for _, table := range []string{"system.cdnsell_settings", "system.cdnsell_overage_bills", "system.cdnsell_orders", "customer.customers", "order.orders", "order.order_items"} {
		if err := root.Exec("GRANT SELECT,INSERT,UPDATE,DELETE ON " + table + " TO 'cdn_runtime_test'@'localhost'").Error; err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := gorm.Open(mysql.Open("cdn_runtime_test:isolated-runtime-only@unix("+os.Getenv("FOUNDATION_MYSQL_SOCKET")+")/?parseTime=true&loc=Local"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := runtime.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = runtime.Exec("CREATE TABLE system.cdn_runtime_ddl_probe(id BIGINT)").Error; err == nil {
		t.Fatal("runtime unexpectedly has DDL permission")
	}
	if err = runtime.Exec("UPDATE system.schema_migrations SET state=state WHERE 1=0").Error; err == nil {
		t.Fatal("runtime unexpectedly can write migration history")
	}
	bill := OverageBill{ID: 895201, BillNo: "CDNB895201", UserID: 895001, DomainID: 895101, StartTime: now.Add(-72 * time.Hour), EndTime: now.Add(-48 * time.Hour), TrafficBytes: 1073741824, UnitPrice: 100, Amount: 100, Status: "unpaid", RecalcHistory: []byte("[]"), CreatedAt: now}
	if err = root.Table(overageBillsTable).Create(&bill).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(runtime, nil, nil)
	service.now = func() time.Time { return now }
	rows, err := service.OverageBills(ctx, 895001)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	purchase, err := service.PurchaseOverage(ctx, 895001, bill.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.PurchaseOverage(ctx, 895001, bill.ID)
	if err != nil || again.OrderID != purchase.OrderID {
		t.Fatal("runtime replay creates another order", err)
	}
	if err = root.Exec("UPDATE `order`.orders SET status=? WHERE id=?", orderdom.OrderStatusPaid, purchase.OrderID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = service.ProvisionPlatformOrder(ctx, 895001, purchase.OrderID, "balance"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ProvisionPlatformOrder(ctx, 895001, purchase.OrderID, "balance"); err != nil {
		t.Fatal(err)
	}
	rows, err = service.OverageBills(ctx, 895001)
	if err != nil || len(rows) != 1 || rows[0].Status != "paid" {
		t.Fatal(rows, err)
	}
	t.Log("restricted runtime can read/create/settle CDN bill idempotently; DDL and migration history writes denied")
}
