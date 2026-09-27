package order_test

import (
	"context"
	orderrepo "ddyidc/internal/order/repository"
	ordersvc "ddyidc/internal/order/service"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	upstreammodel "ddyidc/internal/upstream/model"
	upstream "ddyidc/internal/upstream/service"
	crypt "ddyidc/pkg/crypto"
	"ddyidc/pkg/outbound"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type unitHooks struct{}

func (unitHooks) ChargeCoupon(context.Context, *gorm.DB, int64, int64, int64, int64) (int64, error) {
	return 0, nil
}
func (unitHooks) ReleaseCoupon(context.Context, *gorm.DB, int64) error              { return nil }
func (unitHooks) OnOrderPaid(context.Context, int64, string, int64, int64) error    { return nil }
func (unitHooks) OnRechargePaid(context.Context, int64, string, int64, int64) error { return nil }

func TestDeliveryUnitsMySQL(t *testing.T) {
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
	service := ordersvc.NewService(orderrepo.NewRepository(database), nil)
	for _, qty := range []int{1, 2, 10} {
		orderID := int64(93000 + qty)
		itemID := int64(94000 + qty)
		if err := database.Exec("INSERT INTO `order`.orders(id,order_no,customer_id,status,pay_amount) VALUES(?,?,77,2,?)", orderID, fmt.Sprintf("UNITS%d", qty), qty*100).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Exec("INSERT INTO `order`.order_items(id,order_id,product_id,product_name,module_type,config_snapshot,qty,unit_price,subtotal) VALUES(?,?,91001,'isolated','cloud','{}',?,100,?)", itemID, orderID, qty, qty*100).Error; err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for n := 0; n < 8; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := service.ProvisionAfterPayment(ctx, 77, orderID, "alipay"); err != nil {
					t.Error(err)
				}
			}()
		}
		close(start)
		wg.Wait()
		var units, instances, jobs int64
		database.Table("order.delivery_units").Where("order_id=?", orderID).Count(&units)
		database.Raw("SELECT COUNT(DISTINCT instance_id) FROM `order`.delivery_units WHERE order_id=?", orderID).Scan(&instances)
		database.Raw("SELECT COUNT(*) FROM system.durable_tasks t JOIN `order`.delivery_units u ON t.task_key=CONCAT('delivery-unit:',u.id) WHERE u.order_id=?", orderID).Scan(&jobs)
		if units != int64(qty) || instances != units || jobs != units {
			t.Fatalf("qty=%d units=%d instances=%d jobs=%d", qty, units, instances, jobs)
		}
		if err := service.ProvisionAfterPayment(ctx, 78, orderID, "alipay"); err == nil {
			t.Fatal("wrong owner planned units")
		}
	}
	t.Log("quantities 1/2/10 with 8 concurrent planners each: exactly N distinct instance reservations and N durable tasks; wrong owner denied; no supplier I/O")
	var invoices, charges atomic.Int64
	var recoveryDeleted atomic.Bool
	recoveryDeleted.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"isolated-test-jwt"}`)
		case "/user_info":
			fmt.Fprint(w, `{"status":200,"user":{"credit":"50"}}`)
		case "/host/header":
			if r.URL.Query().Get("host_id") == "300002" {
				status := "Active"
				if recoveryDeleted.Load() {
					status = "Deleted"
				}
				fmt.Fprintf(w, `{"status":200,"data":{"host_data":{"id":"300002","productid":"353","invoiceid":"200002","domainstatus":"%s"}}}`, status)
				return
			}

			if r.URL.Query().Get("host_id") == "300003" {
				fmt.Fprintf(w, `{"status":200,"data":{"host_data":{"id":"300003","productid":"353","regdate":"2025-01-02","nextduedate":"2025-01-19"}}}`)
			} else {
				fmt.Fprintf(w, `{"status":200,"data":{"host_data":{"id":"%s","productid":"353"}}}`, r.URL.Query().Get("host_id"))
			}
		case "/cart/get_total":
			fmt.Fprint(w, `{"status":200,"products":{"billingcycle":"monthly","sale_total":"1.00","type":{"type":1}}}`)
		case "/cart/add_to_shop":
			fmt.Fprint(w, `{"status":200,"data":{"i":1}}`)
		case "/cart/settle":
			fmt.Fprintf(w, `{"status":200,"data":{"invoiceid":%d,"amount":"1.00"}}`, 200000+invoices.Add(1))
		case "/apply_credit":
			n := charges.Add(1)
			if n == 2 {
				http.Error(w, "isolated unknown payment result", 500)
				return
			}
			fmt.Fprintf(w, `{"status":1001,"data":{"hostid":%d}}`, 300000+n)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	// Only this controlled loopback listener is permitted in the test process.
	port := server.Listener.Addr().(*net.TCPAddr).Port
	if err := outbound.Configure(outbound.Config{PrivateCIDRs: []string{"127.0.0.1/32"}, Ports: []int{port}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbound.Configure(outbound.Config{}) })
	key := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := crypt.Encrypt("isolated-test-only", key)
	if err != nil {
		t.Fatal(err)
	}
	supplier := upstreammodel.Supplier{ID: "99101", Name: "isolated protocol server", Type: "finance", URL: server.URL, Username: "isolated", Token: cipher, Secret: cipher, Rate: 1, Status: 1}
	if err := database.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	product := upstreammodel.UpstreamProduct{ID: "99102", SupplierID: supplier.ID, ProductID: "91001", UpstreamProductID: 353, Name: "isolated units", ProductGroupID: "1", AutoSetup: true, UpstreamPrice: 1, Price: 1}
	if err := database.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	previous := upstream.Default
	upstream.Default = upstream.NewStore(database, key)
	defer func() { upstream.Default = previous }()
	service = ordersvc.NewService(orderrepo.NewRepository(database), unitHooks{}, key)
	if err := database.Exec("UPDATE `order`.order_items SET duration_months=1 WHERE order_id IN (93001,93002,93010)").Error; err != nil {
		t.Fatal(err)
	}
	for _, orderID := range []int64{93002, 93001, 93010} {
		var units []struct{ ID int64 }
		if err := database.Table("order.delivery_units").Select("id").Where("order_id=?", orderID).Order("unit_no").Find(&units).Error; err != nil {
			t.Fatal(err)
		}
		for index, unit := range units {
			err := service.ProvisionDeliveryUnit(ctx, 77, unit.ID, "alipay")
			if orderID == 93002 && index == 1 {
				if err == nil {
					t.Fatal("unknown remote result accepted")
				}
				before := charges.Load()
				if service.ProvisionDeliveryUnit(ctx, 77, unit.ID, "alipay") == nil || charges.Load() != before {
					t.Fatal("unknown unit repurchased")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		}
		var count int64
		database.Table("instance.instances").Where("order_id=?", orderID).Count(&count)
		want := int64(len(units))
		if orderID == 93002 {
			want = 1
		}
		if count != want {
			t.Fatalf("order=%d instances=%d want=%d", orderID, count, want)
		}
		var invented int64
		if err := database.Table("instance.instances").Where("order_id=? AND upstream_instance_id <> ? AND (activated_at IS NOT NULL OR expires_at IS NOT NULL)", orderID, "300003").Count(&invented).Error; err != nil {
			t.Fatal(err)
		}
		if invented != 0 {
			t.Fatal("missing provider dates fabricated")
		}
		if orderID == 93001 {
			var actual string
			if err := database.Raw("SELECT DATE_FORMAT(expires_at,'%Y-%m-%d') FROM instance.instances WHERE order_id=?", orderID).Scan(&actual).Error; err != nil {
				t.Fatal(err)
			}
			if actual != "2025-01-19" {
				t.Fatal("provider expiry replaced by local duration", actual)
			}
		}
		var state int
		database.Raw("SELECT status FROM `order`.orders WHERE id=?", orderID).Scan(&state)
		if (orderID == 93002 && state != 2) || (orderID != 93002 && state != 6) {
			t.Fatal("partial delivery order state wrong", orderID, state)
		}
	}
	var failedUnit int64
	database.Raw("SELECT id FROM `order`.delivery_units WHERE order_id=93002 AND status='unknown'").Scan(&failedUnit)
	var failedTask tasks.Task
	if err := database.Where("task_key=?", fmt.Sprintf("delivery-unit:%d", failedUnit)).Take(&failedTask).Error; err != nil {
		t.Fatal(err)
	}
	database.Model(&tasks.Task{}).Where("id=?", failedTask.ID).Update("status", "unknown")
	before := charges.Load()
	if tasks.ReconcileLocal(ctx, database, failedTask.ID, 1, "127.0.0.1", "checked unknown remote invoice") != tasks.ErrReconcile {
		t.Fatal("unsafe remote replay accepted")
	}
	if charges.Load() != before {
		t.Fatal("reconciliation purchased again")
	}
	var unknownUnit struct{ ID int64 }
	if err := database.Table("order.delivery_units").Select("id").Where("order_id=93002 AND status='unknown'").Take(&unknownUnit).Error; err != nil {
		t.Fatal(err)
	}
	var recoveryTask tasks.Task
	if err := database.Where("task_key=?", fmt.Sprintf("delivery-unit:%d", unknownUnit.ID)).Take(&recoveryTask).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&tasks.Task{}).Where("id=?", recoveryTask.ID).Update("status", "unknown").Error; err != nil {
		t.Fatal(err)
	}
	chargedBefore, invoicesBefore := charges.Load(), invoices.Load()
	if service.ReconcileRemoteDelivery(ctx, recoveryTask.ID, 1, "127.0.0.1", "isolated mismatched invoice", "300003") == nil {
		t.Fatal("unrelated invoice accepted")
	}
	if service.ReconcileRemoteDelivery(ctx, recoveryTask.ID, 1, "127.0.0.1", "isolated deleted instance", "300002") == nil {
		t.Fatal("deleted host accepted")
	}
	recoveryDeleted.Store(false)
	if err := service.ReconcileRemoteDelivery(ctx, recoveryTask.ID, 1, "127.0.0.1", "controlled HTTP evidence only", "300002"); err != nil {
		t.Fatal(err)
	}
	if service.ReconcileRemoteDelivery(ctx, recoveryTask.ID, 1, "127.0.0.1", "repeat", "300002") == nil {
		t.Fatal("reconciliation replay accepted")
	}
	// Disabling NEW purchases cannot strand an already paid and verified host.
	if err := database.Model(&upstreammodel.UpstreamProduct{}).Where("id=?", product.ID).Update("auto_setup", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ProvisionDeliveryUnit(ctx, 77, unknownUnit.ID, "alipay"); err != nil {
		t.Fatal(err)
	}
	if charges.Load() != chargedBefore || invoices.Load() != invoicesBefore {
		t.Fatal("reconciliation purchased or charged again")
	}
	var completed int
	if err := database.Raw("SELECT status FROM `order`.orders WHERE id=93002").Scan(&completed).Error; err != nil || completed != 6 {
		t.Fatal("verified local attachment did not finish order", err)
	}
	var audit int64
	if err := database.Table("system.task_reconciliations").Where("task_id=? AND reason LIKE '%invoice=200002%'", recoveryTask.ID).Count(&audit).Error; err != nil || audit != 1 {
		t.Fatal("missing atomic remote evidence audit", err)
	}
	t.Log("controlled remote recovery: unrelated invoice/deleted instance/replay denied; verified active host attaches locally with NEW purchases disabled; zero additional invoices/charges")

	t.Log("CONTROLLED HTTP supplier + real MySQL: 1/10 units fully delivered; 2 units partial success preserves remote invoice, unknown unit never repurchased; not real third-party acceptance")

}
