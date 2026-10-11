package cdnsell

import (
	"context"
	"fmt"
	"sync"
	"testing"

	order "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/testenv"
)

func TestPaidFulfillmentMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	service := NewService(db, nil, nil)
	ctx := context.Background()
	for _, status := range []uint8{1, 2, 3, 4, 6} {
		id := int64(991000) + int64(status)
		row := order.Order{ID: id, OrderNo: fmt.Sprintf("CDN-ISOLATED-%d", status), CustomerID: 7799, TotalAmount: 100, PayAmount: 100, Status: status}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO system.cdnsell_orders(id,order_no,user_id,order_type,ref_order_id,product_id,traffic_bytes,amount,snapshot,status) VALUES(?,?,?,'traffic_pack',?,1,1024,1.00,?,'pending')", id, row.OrderNo, 7799, id, `{"validDays":30}`).Error; err != nil {
			t.Fatal(err)
		}
		if status != 2 {
			if matched, err := service.ProvisionPlatformOrder(ctx, 7799, id, "alipay"); !matched || err == nil {
				t.Fatalf("non-paid state %d accepted", status)
			}
		} else {
			if _, err := service.ProvisionPlatformOrder(ctx, 8800, id, "alipay"); err == nil {
				t.Fatal("wrong customer accepted")
			}
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if matched, err := service.ProvisionPlatformOrder(ctx, 7799, id, "alipay"); err != nil || !matched {
						t.Error("paid delivery", err)
					}
				}()
			}
			wg.Wait()
		}
		var count int64
		if err := db.Table(batchesTable).Where("order_id=?", id).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		var actual order.Order
		if err := db.Where("id=?", id).Take(&actual).Error; err != nil {
			t.Fatal(err)
		}
		if status == 2 {
			if count != 1 || actual.Status != 6 {
				t.Fatal("concurrent delivery not exactly one local batch", count, actual.Status)
			}
		} else if count != 0 || actual.Status != status {
			t.Fatal("rejected delivery mutated state", status, count, actual.Status)
		}
	}
	t.Log("isolated MySQL: unpaid/cancelled/refunded/inconsistent completion denied; customer isolation; 10 concurrent deliveries create one local batch, no provider called")
}

func TestFulfillmentAmountMismatchMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	service := NewService(db, nil, nil)
	id := int64(991099)
	row := order.Order{ID: id, OrderNo: "CDN-ISOLATED-MISMATCH", CustomerID: 7799, TotalAmount: 100, PayAmount: 100, Status: 2}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO system.cdnsell_orders(id,order_no,user_id,order_type,ref_order_id,product_id,traffic_bytes,amount,snapshot,status) VALUES(?,?,?,'traffic_pack',?,1,1024,100.00,?,'pending')", id, row.OrderNo, 7799, id, `{"validDays":30}`).Error; err != nil {
		t.Fatal(err)
	}
	if matched, err := service.ProvisionPlatformOrder(context.Background(), 7799, id, "balance"); !matched || err == nil {
		t.Fatal("mismatched amount accepted")
	}
	var count int64
	if err := db.Table(batchesTable).Where("order_id=?", id).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("rejected delivery mutated quota", count, err)
	}
	var actual order.Order
	if err := db.Where("id=?", id).Take(&actual).Error; err != nil || actual.Status != 2 {
		t.Fatal("rejected delivery mutated platform order", actual.Status, err)
	}
}
