package order_test

import (
	"context"
	orderrepo "ddyidc/internal/order/repository"
	ordersvc "ddyidc/internal/order/service"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestCreateRequestIdempotencyMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	const customer = 777047
	for _, sql := range []string{
		`UPDATE system.system_configs SET value='{"enabled":false}' WHERE ` + "`key`" + `='certification.settings'`,
		`INSERT INTO product.products(id,group_id,name,module_type,stock) VALUES(947001,1,'request identity fixture','cloud',100)`,
		`INSERT INTO product.product_specs(id,product_id,name,vcpu,memory,price_mode,price) VALUES(947002,947001,'fixed',1,'1 GB','fixed',10.00)`,
	} {
		if e := db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	service := ordersvc.NewService(orderrepo.NewRepository(db), nil, []byte(strings.Repeat("k", 32)))
	input := func(key string) *ordersvc.CreateOrderInput {
		return &ordersvc.CreateOrderInput{RequestID: key, ProductID: "947001", DurationMonth: 1, Quantity: 1, BillingCycle: "monthly"}
	}
	const key = "request-concurrent-0001"
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	errs := make(chan error, 20)
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := service.CreateOrder(ctx, customer, input(key))
			if e != nil {
				errs <- e
				return
			}
			ids <- v.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if t.Failed() {
		t.FailNow()
	}
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("multiple orders", id, got)
		}
		id = got
	}
	var count int64
	db.Table("order.orders").Where("customer_id=?", customer).Count(&count)
	if count != 1 {
		t.Fatal("order count", count)
	}
	var stock int
	db.Table("product.products").Select("stock").Where("id=947001").Scan(&stock)
	if stock != 99 {
		t.Fatal("reserved more than once", stock)
	}
	// A different process/service returns the same committed order, even if catalogue changes.
	db.Exec("UPDATE product.products SET hidden=1 WHERE id=947001")
	other := ordersvc.NewService(orderrepo.NewRepository(db), nil, []byte(strings.Repeat("k", 32)))
	replay, e := other.CreateOrder(ctx, customer, input(key))
	if e != nil || replay.ID != id {
		t.Fatal("replay repriced or failed", replay, e)
	}
	changed := input(key)
	changed.Quantity = 2
	if _, e = other.CreateOrder(ctx, customer, changed); e == nil {
		t.Fatal("key reused with changed configuration")
	}
	db.Exec("UPDATE product.products SET hidden=0,stock=0 WHERE id=947001")
	const failed = "request-rollback-0001"
	if _, e = service.CreateOrder(ctx, customer, input(failed)); e == nil {
		t.Fatal("empty stock accepted")
	}
	db.Table("order.create_requests").Where("customer_id=? AND request_id=?", customer, failed).Count(&count)
	if count != 0 {
		t.Fatal("failed request committed")
	}
	db.Exec("UPDATE product.products SET stock=10 WHERE id=947001")
	if _, e = service.CreateOrder(ctx, customer, input(failed)); e != nil {
		t.Fatal("rollback could not retry", e)
	}
	// Identical request ID belongs to a different customer namespace.
	if _, e = service.CreateOrder(ctx, customer+1, input(key)); e != nil {
		t.Fatal("customer identity collision", e)
	}
	// New explicit keys still permit intentional separate orders.
	if _, e = service.CreateOrder(ctx, customer, input(fmt.Sprintf("request-intentional-%d", 1))); e != nil {
		t.Fatal(e)
	}
	t.Log("20 concurrent submissions: one order/reservation; persistent replay, payload conflict, rollback retry and customer isolation passed; no payments or supplier calls")
}
