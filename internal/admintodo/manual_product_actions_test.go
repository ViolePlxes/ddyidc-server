package admintodo

import (
	"context"
	instance "ddyidc/internal/instance/service"
	"ddyidc/internal/shared/testenv"
	"errors"
	"gorm.io/gorm"
	"testing"
	"time"
)

func manualProductFixtureDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testenv.MySQL(t)
	// Regular tables avoid MySQL temporary-table reopen limits in correlated queries.
	statements := []string{
		"CREATE TABLE IF NOT EXISTS customer.customers(id BIGINT,username VARCHAR(64))",
		"CREATE TABLE IF NOT EXISTS instance.instances(id BIGINT PRIMARY KEY,customer_id BIGINT,order_id BIGINT,order_item_id BIGINT,product_id BIGINT DEFAULT 0,module_type VARCHAR(32) DEFAULT 'cloud',config_snapshot JSON,ownership_version BIGINT,upstream_provider_id BIGINT,upstream_instance_id VARCHAR(64),product_name VARCHAR(128),instance_no VARCHAR(64),created_at DATETIME(3),updated_at DATETIME(3),deleted_at DATETIME NULL)",
		"CREATE TABLE IF NOT EXISTS instance.instance_events(id BIGINT,instance_id BIGINT,status INT,event_type VARCHAR(32),updated_at DATETIME(3))",
		"CREATE TABLE IF NOT EXISTS instance.instance_renewals(instance_id BIGINT,state VARCHAR(32),updated_at DATETIME(3))",
		"CREATE TABLE IF NOT EXISTS system.todo_event_resolutions(event_id BIGINT)",
		"CREATE TABLE IF NOT EXISTS `order`.delivery_units(id BIGINT,unit_no BIGINT DEFAULT 1,instance_id BIGINT,order_item_id BIGINT,order_id BIGINT,customer_id BIGINT,status VARCHAR(32),updated_at DATETIME(3))",
		"CREATE TABLE IF NOT EXISTS `order`.orders(id BIGINT,customer_id BIGINT,status INT,order_no VARCHAR(64),created_at DATETIME,paid_at DATETIME)",
		"CREATE TABLE IF NOT EXISTS `order`.order_items(id BIGINT PRIMARY KEY,order_id BIGINT,product_id BIGINT,product_name VARCHAR(128),delivery_status INT,updated_at DATETIME(3))",
		"CREATE TABLE IF NOT EXISTS product.upstream_products(product_id BIGINT,auto_setup INT)",
		"CREATE TABLE IF NOT EXISTS system.durable_tasks(kind VARCHAR(32),status VARCHAR(32),payload JSON,updated_at DATETIME(3))",
		"CREATE TABLE IF NOT EXISTS system.todo_audits(id BIGINT PRIMARY KEY,request_id VARCHAR(64) UNIQUE,kind VARCHAR(32),source VARCHAR(32),object_id BIGINT,action VARCHAR(32),admin_id BIGINT,operator_ip VARCHAR(64),notes TEXT,evidence TEXT,created_at DATETIME(3))",
	}
	for _, sql := range statements {
		if e := db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	return db
}

func TestManualProductMarkAndReadOnlyRefreshMySQL(t *testing.T) {
	tx := manualProductFixtureDB(t).Begin()
	defer tx.Rollback()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	statements := []string{
		"INSERT INTO instance.instances(id,customer_id,order_id,order_item_id,ownership_version,upstream_provider_id,upstream_instance_id,product_name,instance_no,created_at,updated_at,deleted_at) VALUES(901,77,701,0,1,88,'900','fixture','HOST-901',NOW(),NOW(),NULL)",
		"INSERT INTO instance.instance_events VALUES(501,901,3,'create',NOW())",
	}
	for _, sql := range statements {
		if e := tx.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	ctx := context.Background()
	entry, e := manualProductEntry(ctx, tx, "instance", 901)
	if e != nil {
		t.Fatal(e)
	}
	in := ManualProductInput{Source: "instance", Revision: entry.Revision, Action: "refresh", Reason: "仅查询", RequestID: "REFRESH-TEST-01"}
	reader := &isolatedFactReader{facts: &instance.TodoFacts{CustomerID: 77, OwnershipVersion: 1, SupplierID: 88, RemoteID: "900", Status: 3, PowerStatus: "running", QueriedAt: time.Now().UTC()}}
	result, e := ProcessManualProduct(ctx, tx, reader, 1, 901, "127.0.0.1", in)
	if e != nil || result.Processed || result.Facts == nil || reader.calls != 1 {
		t.Fatal("read-only refresh failed", result, e)
	}
	if _, e = manualProductEntry(ctx, tx, "instance", 901); e != nil {
		t.Fatal("GET dismissed the item", e)
	}
	reader.err = errors.New("unavailable")
	in.RequestID = "REFRESH-TEST-02"
	if out, e := ProcessManualProduct(ctx, tx, reader, 1, 901, "127.0.0.1", in); e != nil || out.Outcome != "unavailable" {
		t.Fatal(out, e)
	}
	reader.err = nil
	reader.facts.RemoteID = "wrong"
	in.RequestID = "REFRESH-TEST-03"
	if _, e := ProcessManualProduct(ctx, tx, reader, 1, 901, "127.0.0.1", in); !errors.Is(e, ErrConflict) {
		t.Fatal("wrong resource accepted", e)
	}
	in.Action = "mark_processed"
	in.Reason = "已人工完成"
	in.RequestID = "MARK-TEST-01"
	if out, e := ProcessManualProduct(ctx, tx, nil, 1, 901, "127.0.0.1", in); e != nil || !out.Processed {
		t.Fatal(out, e)
	}
	if _, e := ProcessManualProduct(ctx, tx, nil, 1, 901, "127.0.0.1", in); e != nil {
		t.Fatal("replay failed", e)
	}
	if _, e := manualProductEntry(ctx, tx, "instance", 901); !errors.Is(e, ErrConflict) {
		t.Fatal("marked item remains pending", e)
	}
	history, e := List(ctx, tx, "manual-products", Filter{History: true, Page: 1, PageSize: 20})
	if e != nil || len(history.Items) != 1 {
		t.Fatal("history erased", history, e)
	}
	var state int
	tx.Table("instance.instance_events").Select("status").Where("id=501").Scan(&state)
	if state != 3 {
		t.Fatal("original failure rewritten")
	}
	if e := tx.Exec("INSERT INTO instance.instance_events VALUES(502,901,3,'unsuspend',DATE_ADD(NOW(),INTERVAL 1 SECOND))").Error; e != nil {
		t.Fatal(e)
	}
	if _, e := manualProductEntry(ctx, tx, "instance", 901); e != nil {
		t.Fatal("new exception suppressed", e)
	}
	in.RequestID = "STALE-MARK-01"
	if _, e := ProcessManualProduct(ctx, tx, nil, 1, 901, "127.0.0.1", in); !errors.Is(e, ErrConflict) {
		t.Fatal("stale mark accepted", e)
	}
	t.Log("isolated rollback: fresh provider GET only, query failures retain work, identity mismatch blocked, marks audited/idempotent, original failures retained, history visible, new failures reappear, stale selections rejected")
}
