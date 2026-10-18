package order

import (
	"context"
	rep "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/testenv"
	upstream "ddyidc/internal/upstream/service"
	crypt "ddyidc/pkg/crypto"
	"ddyidc/pkg/outbound"
	"fmt"
	"gorm.io/gorm"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupplierAdmissionMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if e := db.Exec(sql, args...).Error; e != nil {
			t.Fatal(e)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"isolated-jwt"}`)
		case "/api/product/353":
			fmt.Fprint(w, `{"status":200,"data":{"product":{"id":353,"name":"isolated fixed","product_price":"10.00","billingcycle":"monthly","pay_method":"recurring"}}}`)
		case "/cart/set_config":
			fmt.Fprint(w, `{"status":200,"product":{"name":"isolated fixed","cycle":[{"billingcycle":"monthly","product_price":"10.00","setup_fee":"0"}]},"option":[]}`)
		default:
			t.Errorf("unexpected supplier side effect: %s", r.URL.Path)
			http.Error(w, "refused", 500)
		}
	}))
	defer server.Close()
	if e := outbound.Configure(outbound.Config{PrivateCIDRs: []string{"127.0.0.1/32"}, Ports: []int{server.Listener.Addr().(*net.TCPAddr).Port}}); e != nil {
		t.Fatal(e)
	}
	defer outbound.Configure(outbound.Config{})
	key := []byte("0123456789abcdef0123456789abcdef")
	token, e := crypt.Encrypt("isolated-only", key)
	if e != nil {
		t.Fatal(e)
	}
	exec("UPDATE system.system_configs SET value='{\"enabled\":false}' WHERE `key`='certification.settings'")
	exec("INSERT INTO common.suppliers(id,name,type,url,username,token,secret,rate,status,created_at,updated_at) VALUES(951101,'admission fixture','finance',?,'isolated',?,?,1,1,UNIX_TIMESTAMP(),UNIX_TIMESTAMP())", server.URL, token, token)
	for _, id := range []int{951001, 951002} {
		exec("INSERT INTO product.products(id,group_id,name,module_type,stock) VALUES(?,1,'admission fixture','cloud',50)", id)
		exec("INSERT INTO product.product_specs(id,product_id,name,vcpu,memory,price_mode,price) VALUES(?,?,'fixed',1,'1 GB','fixed',10.00)", id+10000, id)
	}
	exec("INSERT INTO product.upstream_products(id,supplier_id,upstream_product_id,product_id,name,product_group_id,price,cycle,mode,price_basis,created_at,updated_at) VALUES(951201,951101,353,951002,'mapped fixture',1,10,'monthly','sync','standard',UNIX_TIMESTAMP(),UNIX_TIMESTAMP())")
	previous := upstream.Default
	upstream.Default = upstream.NewStore(db, key)
	defer func() { upstream.Default = previous }()
	service := NewService(rep.NewRepository(db), nil, key)
	input := func(id, key string) *CreateOrderInput {
		return &CreateOrderInput{RequestID: key, ProductID: id, DurationMonth: 1, Quantity: 1, BillingCycle: "monthly"}
	}
	// This combination reproduced 1054 before the fix: real Store + real schema.
	for _, id := range []string{"951001", "951002"} {
		in := input(id, "admission-create-"+id)
		order, e := service.CreateOrder(ctx, 951301, in)
		if e != nil {
			t.Fatal(id, e)
		}
		if order.PayAmount != 1000 || order.Status != 1 {
			t.Fatal("wrong pending price/status", order)
		}
		replay, e := service.CreateOrder(ctx, 951301, in)
		if e != nil || replay.ID != order.ID {
			t.Fatal("idempotency", e)
		}
	}
	p, e := service.PrepareOrder(ctx, 951301, input("951002", "admission-prepared-change"))
	if e != nil {
		t.Fatal(e)
	}
	exec("UPDATE product.upstream_products SET upstream_product_id=354 WHERE id=951201")
	if e = db.Transaction(func(tx *gorm.DB) error { return service.checkPreparedSupplierTx(ctx, tx, p) }); e == nil || !strings.Contains(e.Error(), "已变更") {
		t.Fatal("changed quote binding accepted", e)
	}
	exec("UPDATE product.upstream_products SET upstream_product_id=353 WHERE id=951201")
	exec("INSERT INTO product.upstream_products(id,supplier_id,upstream_product_id,product_id,name,product_group_id,created_at,updated_at) VALUES(951202,951101,355,951002,'duplicate fixture',1,UNIX_TIMESTAMP(),UNIX_TIMESTAMP())")
	if e = db.Transaction(func(tx *gorm.DB) error { return service.checkPreparedSupplierTx(ctx, tx, p) }); e == nil || !strings.Contains(e.Error(), "配置异常") {
		t.Fatal("ambiguous mapping accepted", e)
	}
	exec("DELETE FROM product.upstream_products WHERE id=951202")
	exec("UPDATE common.suppliers SET status=2 WHERE id=951101")
	if e = db.Transaction(func(tx *gorm.DB) error { return service.checkPreparedSupplierTx(ctx, tx, p) }); e == nil {
		t.Fatal("stopped supplier accepted")
	}
	exec("UPDATE common.suppliers SET status=1 WHERE id=951101")
	exec("UPDATE system.system_configs SET value='{\"status\":\"sales_stopped\",\"revision\":1}' WHERE `key`='upstream.module.zjmf'")
	if e = db.Transaction(func(tx *gorm.DB) error { _, e := service.CommitGroupOrderTx(ctx, tx, p, 951401, 1000, 0); return e }); e == nil {
		t.Fatal("group bypassed stopped sales")
	}
	var count int64
	db.Table("order.orders").Where("admin_remark=?", "groupbuy:951401").Count(&count)
	if count != 0 {
		t.Fatal("failed group committed")
	}
	exec("UPDATE system.system_configs SET value='{\"status\":\"enabled\",\"revision\":2}' WHERE `key`='upstream.module.zjmf'")
	if e = db.Transaction(func(tx *gorm.DB) error { _, e := service.CommitGroupOrderTx(ctx, tx, p, 951401, 1000, 0); return e }); e != nil {
		t.Fatal("group did not recover", e)
	}
	t.Log("real Store: self-managed/mapped fixed order, replay, changed/ambiguous mapping, stopped supplier, stopped-sales group rollback and recovery passed; no remote purchase or payment")
}
