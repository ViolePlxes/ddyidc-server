package admintodo

import (
	"context"
	"testing"
)

func TestManualBillingURL(t *testing.T) {
	if got := manualBillingURL("https://www.qkidc.cn/", "946271"); got != "https://www.qkidc.cn/viewbilling?id=946271" {
		t.Fatal(got)
	}
	for _, v := range []string{"javascript:alert(1)", "https://user:secret@example.com", "https://example.com/api"} {
		if manualBillingURL(v, "946271") != "" {
			t.Fatal("unsafe portal")
		}
	}
	if manualBillingURL("https://example.com", "pending") != "" {
		t.Fatal("invalid invoice")
	}
}
func TestManualProductDisplayMySQL(t *testing.T) {
	tx := manualProductFixtureDB(t).Begin()
	defer tx.Rollback()
	for _, sql := range []string{
		"INSERT INTO instance.instances(id,customer_id,order_id,expires_at,upstream_provider_id,upstream_instance_id) VALUES(99001,99002,99003,'2026-11-13 01:30:00',99004,'remote1')",
		"INSERT INTO customer.customers(id,username) VALUES(99002,'V10-legacy-do-not-display')",
		"INSERT INTO customer.customer_realname(id,customer_id,name,status,updated_at) VALUES(99007,99002,'synthetic verified name',2,NOW())",
		"INSERT INTO instance.instance_events(id,instance_id,status,event_type,updated_at) VALUES(99008,99001,3,'create',NOW())",
		"UPDATE instance.instances SET product_name='test product',instance_no='test-99001',created_at=NOW(),updated_at=NOW() WHERE id=99001",
		"INSERT INTO common.suppliers(id,url) VALUES(99004,'https://www.qkidc.cn')",
		"INSERT INTO `order`.upstream_orders(id,order_id,supplier_id,upstream_order_id,upstream_host_id) VALUES(99005,99003,99004,'946271','remote1')",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A case-only different opaque host must not be attached to this instance.
	if err := tx.Exec("INSERT INTO `order`.upstream_orders(id,order_id,supplier_id,upstream_order_id,upstream_host_id) VALUES(99010,99003,99004,'946273','Remote1')").Error; err != nil {
		t.Fatal(err)
	}
	rows := []Entry{{Source: "instance", ID: "99001", InstanceID: "99001", CustomerID: "99002"}, {Source: "instance", ID: "99001", InstanceID: "99001", CustomerID: "99006"}}
	if err := enrichManualProductDisplay(context.Background(), tx, rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].UpstreamOrderID != "946271" || rows[0].UpstreamBillingURL == "" || rows[0].ExpiresAt == nil || rows[0].ExpiresAt.Year() != 2026 {
		t.Fatal("missing exact host display")
	}
	page, err := List(context.Background(), tx, "manual-products", Filter{Page: 1, PageSize: 20, CustomerID: "99002"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].CustomerRealName != "synthetic verified name" || page.Items[0].ExpiresAt == nil || page.Items[0].UpstreamOrderID != "946271" {
		t.Fatal("list display projection missing")
	}
	if err := tx.Exec("INSERT INTO `order`.upstream_orders(id,order_id,supplier_id,upstream_order_id,upstream_host_id) VALUES(99009,99003,99004,'946272','remote1')").Error; err != nil {
		t.Fatal(err)
	}
	ambiguous := []Entry{{Source: "instance", ID: "99001", InstanceID: "99001", CustomerID: "99002"}}
	if err := enrichManualProductDisplay(context.Background(), tx, ambiguous); err != nil {
		t.Fatal(err)
	}
	if ambiguous[0].UpstreamBillingURL != "" || ambiguous[0].UpstreamOrderID != "" {
		t.Fatal("ambiguous invoice guessed")
	}
	if rows[1].ExpiresAt != nil || rows[1].UpstreamOrderID != "" {
		t.Fatal("other customer leakage")
	}
}

// A failed renewal has no procurement row, so its supplier document is the
// renewal invoice. The queue must show that number instead of "未获取".
func TestManualProductRenewalInvoiceMySQL(t *testing.T) {
	tx := manualProductFixtureDB(t).Begin()
	defer tx.Rollback()
	for _, sql := range []string{
		"INSERT INTO customer.customers(id,username) VALUES(99202,'renewal customer')",
		"INSERT INTO instance.instances(id,customer_id,order_id,product_name,instance_no,created_at,updated_at,expires_at) VALUES(99201,99202,99203,'legacy plan','V10-476',NOW(),NOW(),'2026-10-21 21:47:28')",
		"INSERT INTO instance.instance_renewals(id,instance_id,order_id,customer_id,state,invoice_id,last_error,updated_at) VALUES(99208,99201,99203,99202,'uncertain','283705','upstream renewal payment failed',NOW())",
		"INSERT INTO instance.instance_events(id,instance_id,status,event_type,updated_at) VALUES(99209,99201,3,'renew',NOW())",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := []Entry{{Source: "instance", ID: "99201", InstanceID: "99201", CustomerID: "99202"}}
	if err := enrichManualProductDisplay(context.Background(), tx, rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].UpstreamOrderID != "283705" {
		t.Fatalf("renewal invoice not shown as upstream order: %q", rows[0].UpstreamOrderID)
	}
	if rows[0].UpstreamBillingURL != "" {
		t.Fatal("purchase invoice URL must not be guessed for a renewal")
	}
	page, err := List(context.Background(), tx, "manual-products", Filter{Page: 1, PageSize: 20, CustomerID: "99202"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].UpstreamOrderID != "283705" || page.Items[0].Reason != "upstream renewal payment failed" {
		t.Fatalf("renewal row missing invoice or reason: %+v", page.Items)
	}
}

func TestRenewalTodoAppearsOnceMySQL(t *testing.T) {
	tx := manualProductFixtureDB(t).Begin()
	defer tx.Rollback()
	for _, q := range []string{
		"INSERT INTO customer.customers(id,username) VALUES(99102,'isolated')",
		"INSERT INTO instance.instances(id,customer_id,order_id,order_item_id,product_id,product_name,instance_no,created_at,updated_at,expires_at) VALUES(99101,99102,99103,99104,99105,'fixed plan','HOST',NOW(),NOW(),'2030-01-01')",
		"INSERT INTO `order`.orders(id,customer_id,status,order_no,created_at,paid_at) VALUES(99106,99102,2,'RENEW',NOW(),NOW()),(99109,99102,2,'OTHER-PURCHASE',NOW(),NOW())",
		"INSERT INTO `order`.order_items(id,order_id,product_id,product_name,delivery_status,updated_at) VALUES(99107,99106,99105,'renewal',0,NOW()),(99110,99109,99105,'separate new purchase',3,NOW())",
		"INSERT INTO instance.instance_renewals(id,instance_id,order_id,customer_id,state,updated_at) VALUES(99108,99101,99106,99102,'uncertain',NOW())",
		"INSERT INTO system.durable_tasks(kind,status,payload,updated_at) VALUES('post_payment','failed','{\"customerId\":\"99102\",\"businessType\":\"order\",\"businessId\":\"99106\"}',NOW())",
	} {
		if e := tx.Exec(q).Error; e != nil {
			t.Fatal(e)
		}
	}
	if err := tx.Exec("UPDATE instance.instance_renewals SET last_error='isolated renewal failure' WHERE id=99108").Error; err != nil {
		t.Fatal(err)
	}
	page, e := List(context.Background(), tx, "manual-products", Filter{Page: 1, PageSize: 20, CustomerID: "99102"})
	if e != nil {
		t.Fatal(e)
	}
	if page.Total != 2 {
		t.Fatalf("want one renewal and one independent purchase, got %d", page.Total)
	}
	for _, row := range page.Items {
		if row.InstanceID == "99101" && row.Reason != "isolated renewal failure" {
			t.Fatalf("renewal reason missing: %q", row.Reason)
		}
		if row.OrderID == "99106" && row.Source == "order" {
			t.Fatal("duplicate renewal row")
		}
	}
	var state string
	tx.Table("instance.instance_renewals").Select("state").Where("id=99108").Scan(&state)
	if state != "uncertain" {
		t.Fatal("read changed manual state")
	}
}

// An empty upstream number is a stage, not an absence: the queue must explain
// whether the supplier was never called, is still being called, or answered.
func TestManualProductUpstreamStageMySQL(t *testing.T) {
	tx := manualProductFixtureDB(t).Begin()
	defer tx.Rollback()
	for _, sql := range []string{
		"INSERT INTO customer.customers(id,username) VALUES(99302,'stage customer')",
		"INSERT INTO instance.instances(id,customer_id,order_id,product_name,instance_no,created_at,updated_at) VALUES(99301,99302,99303,'plan','STAGE-1',NOW(),NOW())",
		"INSERT INTO instance.instance_events(id,instance_id,status,event_type,updated_at) VALUES(99309,99301,3,'create',NOW())",
		"INSERT INTO `order`.unit_procurements(id,order_id,supplier_id,upstream_order_id,status,last_error,updated_at) VALUES(99308,99303,1,'pending','not_submitted','quote timeout',1)",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	row := func() Entry {
		rows := []Entry{{Source: "instance", ID: "99301", InstanceID: "99301", CustomerID: "99302", OrderID: "99303"}}
		if err := enrichManualProductDisplay(context.Background(), tx, rows); err != nil {
			t.Fatal(err)
		}
		return rows[0]
	}
	if got := row(); got.UpstreamOrderID != "" || got.UpstreamOrderState != "not_submitted" {
		t.Fatalf("unsent procurement stage not explained: %+v", got)
	}
	if err := tx.Exec("UPDATE `order`.unit_procurements SET status='unknown', upstream_order_id='pending' WHERE id=99308").Error; err != nil {
		t.Fatal(err)
	}
	if got := row(); got.UpstreamOrderState != "unknown" {
		t.Fatalf("unknown procurement stage not explained: %+v", got)
	}
	if err := tx.Exec("DELETE FROM `order`.unit_procurements WHERE id=99308").Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("INSERT INTO instance.instance_renewals(id,instance_id,order_id,customer_id,state,invoice_id,created_at,updated_at) VALUES(99310,99301,99303,99302,'creating','',NOW(),NOW())").Error; err != nil {
		t.Fatal(err)
	}
	if got := row(); got.UpstreamOrderID != "" || got.UpstreamOrderState != "pending" {
		t.Fatalf("renewal without invoice not explained: %+v", got)
	}
	if err := tx.Exec("INSERT INTO `order`.unit_procurements(id,order_id,supplier_id,upstream_order_id,status,updated_at) VALUES(99311,99303,1,'946271','unknown',2)").Error; err != nil {
		t.Fatal(err)
	}
	if got := row(); got.UpstreamOrderState != "unmatched" {
		t.Fatalf("supplier document without a portal match not explained: %+v", got)
	}
}

// A machine can carry an older purchase invoice and a failing renewal invoice.
// The queue must bind the document of the failure the row is about.
func TestManualProductBindingFollowsReasonSourceMySQL(t *testing.T) {
	tx := manualProductFixtureDB(t).Begin()
	defer tx.Rollback()
	for _, sql := range []string{
		"INSERT INTO customer.customers(id,username) VALUES(99402,'binding customer')",
		"INSERT INTO instance.instances(id,customer_id,order_id,product_name,instance_no,created_at,updated_at) VALUES(99401,99402,99403,'legacy plan','V10-480',NOW(),NOW())",
		"INSERT INTO instance.instance_renewals(id,instance_id,order_id,customer_id,state,invoice_id,last_error,created_at,updated_at) VALUES(99408,99401,99403,99402,'uncertain','283706','renewal payment failed',NOW(),NOW())",
		"INSERT INTO common.suppliers(id,url) VALUES(99404,'https://www.qkidc.cn')",
		"INSERT INTO `order`.delivery_units(id,unit_no,instance_id,order_item_id,order_id,customer_id,status) VALUES(99411,1,99401,99412,99403,99402,'failed')",
		"INSERT INTO `order`.unit_procurements(id,order_id,supplier_id,upstream_order_id,status,updated_at) VALUES(99411,99403,99404,'946271','unknown',1)",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := []Entry{{Source: "instance", ID: "99401", InstanceID: "99401", CustomerID: "99402", OrderID: "99403", ReasonSource: "renewal"}}
	if err := enrichManualProductDisplay(context.Background(), tx, rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].UpstreamOrderID != "283706" || rows[0].UpstreamBillingURL != "" {
		t.Fatalf("renewal document must win over the purchase invoice: %+v", rows[0])
	}
	// The same machine without a renewal-sourced reason keeps the purchase invoice.
	other := []Entry{{Source: "instance", ID: "99401", InstanceID: "99401", CustomerID: "99402", OrderID: "99403", ReasonSource: "procurement"}}
	if err := enrichManualProductDisplay(context.Background(), tx, other); err != nil {
		t.Fatal(err)
	}
	if other[0].UpstreamOrderID != "946271" || other[0].UpstreamBillingURL == "" {
		t.Fatalf("procurement document lost: %+v", other[0])
	}
	page, err := List(context.Background(), tx, "manual-products", Filter{Page: 1, PageSize: 20, CustomerID: "99402"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ReasonSource != "renewal" || page.Items[0].UpstreamOrderID != "283706" {
		t.Fatalf("list must expose the reason source and the bound document: %+v", page.Items)
	}
}
