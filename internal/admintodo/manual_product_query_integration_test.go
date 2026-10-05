package admintodo

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"testing"
)

func TestPaidDeliveryBeforeInstanceMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	// Temporary tables shadow the real schema on this single isolated connection.
	for _, sql := range []string{
		"CREATE TEMPORARY TABLE `order`.orders(id BIGINT,order_no VARCHAR(64),customer_id BIGINT,status INT,paid_at DATETIME,created_at DATETIME)",
		"CREATE TEMPORARY TABLE `order`.order_items(id BIGINT,order_id BIGINT,product_id BIGINT,product_name VARCHAR(128),delivery_status INT,updated_at DATETIME)",
		"CREATE TEMPORARY TABLE product.upstream_products(product_id BIGINT,auto_setup INT)",
		"CREATE TEMPORARY TABLE system.durable_tasks(kind VARCHAR(32),status VARCHAR(32),payload JSON)",
		"CREATE TEMPORARY TABLE instance.instance_events(id BIGINT,instance_id BIGINT,status INT,event_type VARCHAR(32))",
		"CREATE TEMPORARY TABLE instance.instance_renewals(instance_id BIGINT,state VARCHAR(32))",
		"CREATE TEMPORARY TABLE system.todo_event_resolutions(event_id BIGINT)",
		"INSERT INTO `order`.orders VALUES(1,'PAID-FAILED',99,2,NOW(),NOW()),(2,'UNPAID',99,1,NULL,NOW()),(3,'REFUNDED',99,4,NOW(),NOW()),(4,'PAID-MANUAL',99,2,NOW(),NOW()),(5,'AUTO-PENDING',99,2,NOW(),NOW())",
		"INSERT INTO `order`.order_items VALUES(11,1,101,'failed before instance',3,NOW()),(12,2,101,'unpaid',3,NOW()),(13,3,101,'refunded',3,NOW()),(14,4,104,'manual opening',0,NOW()),(15,5,105,'normal automatic delivery',0,NOW())",
		"INSERT INTO product.upstream_products VALUES(101,1),(104,0),(105,1)",
		"INSERT INTO `order`.delivery_units(id,unit_no,instance_id,order_id,order_item_id,customer_id,status) VALUES(10111,1,111,1,11,99,'unknown'),(10112,2,112,1,11,99,'failed'),(10113,1,113,2,12,99,'failed'),(10114,1,114,3,13,99,'unknown')",
	} {
		if e := tx.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	ctx := context.Background()
	f := Filter{Page: 1, PageSize: 20, CustomerID: "99"}
	page, e := List(ctx, tx, "manual-products", f)
	if e != nil {
		t.Fatal(e)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("paid orphan/manual items missing, duplicated, or unpaid/refunded/normal items included: %+v", page)
	}
	for _, r := range page.Items {
		if r.Source != "order" || r.InstanceID != "" || r.Href != "/admin/orders?record="+r.OrderID || len(r.Actions) != 2 || r.Actions[0] != "open_business" || r.Actions[1] != "inspect_product" {
			t.Fatalf("nonexistent instance action exposed: %+v", r)
		}
	}
	summary := Read(ctx, tx)
	for _, s := range summary.Items {
		if s.Key == "manual-products" && (s.Count == nil || *s.Count != page.Total) {
			t.Fatal("summary/list diverged")
		}
	}
	f.Keyword = "PAID-FAILED"
	page, e = List(ctx, tx, "manual-products", f)
	if e != nil || page.Total != 1 {
		t.Fatal("order number search missing", e)
	}
	// Once that reserved instance exists, show its existing exception once instead.
	if e = tx.Exec("INSERT INTO instance.instances(id,customer_id,order_id,order_item_id,product_id,product_name,module_type,config_snapshot,instance_no) VALUES(111,99,1,11,101,'created instance','cloud','{}','HOST-111')").Error; e != nil {
		t.Fatal(e)
	}
	if e = tx.Exec("UPDATE `order`.delivery_units SET status='complete' WHERE instance_id=112").Error; e != nil {
		t.Fatal(e)
	}
	f.Keyword = ""
	page, e = List(ctx, tx, "manual-products", f)
	if e != nil || page.Total != 2 {
		t.Fatal("instance handover duplicated queue", e)
	}
	if e = tx.Exec("UPDATE `order`.delivery_units SET status='complete' WHERE order_id=1").Error; e != nil {
		t.Fatal(e)
	}
	if e = tx.Exec("UPDATE `order`.orders SET status=6 WHERE id IN (1,4)").Error; e != nil {
		t.Fatal(e)
	}
	page, e = List(ctx, tx, "manual-products", f)
	if e != nil || page.Total != 0 {
		t.Fatal("delivered items remain pending", e)
	}
	t.Log("paid failure before instance and manual-opening items visible once; unpaid/refunded/normal automatic orders excluded; safe order links; instance handover and completion verified")
}
