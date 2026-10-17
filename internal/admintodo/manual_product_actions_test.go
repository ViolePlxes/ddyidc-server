package admintodo

import (
	"context"
	instance "ddyidc/internal/instance/service"
	"ddyidc/internal/shared/testenv"
	"errors"
	"gorm.io/gorm"
	"os"
	"strings"
	"testing"
	"time"
)

func manualProductFixtureDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testenv.MySQL(t)
	// Regular tables avoid MySQL temporary-table reopen limits in correlated queries.
	statements := []string{
		"CREATE TABLE IF NOT EXISTS instance.instance_upgrades(id BIGINT,instance_id BIGINT,order_id BIGINT,customer_id BIGINT,state VARCHAR(32),updated_at DATETIME(3))",
		"CREATE TABLE IF NOT EXISTS customer.customer_realname(id BIGINT,customer_id BIGINT,name VARCHAR(64),status INT,updated_at DATETIME)",
		"CREATE TABLE IF NOT EXISTS common.suppliers(id BIGINT,url VARCHAR(255))",
		"CREATE TABLE IF NOT EXISTS `order`.unit_procurements(id BIGINT,order_id BIGINT,supplier_id BIGINT,upstream_order_id VARCHAR(100))",
		"CREATE TABLE IF NOT EXISTS `order`.upstream_orders(id BIGINT,order_id BIGINT,supplier_id BIGINT,upstream_order_id VARCHAR(100),upstream_host_id VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci)",

		"CREATE TABLE IF NOT EXISTS customer.customers(id BIGINT,username VARCHAR(64))",
		"CREATE TABLE IF NOT EXISTS instance.instances(id BIGINT PRIMARY KEY,customer_id BIGINT,order_id BIGINT,order_item_id BIGINT,product_id BIGINT DEFAULT 0,module_type VARCHAR(32) DEFAULT 'cloud',config_snapshot JSON,primary_ip VARCHAR(45) DEFAULT '',expires_at DATETIME(3),ownership_version BIGINT,upstream_provider_id BIGINT,upstream_instance_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci,product_name VARCHAR(128),instance_no VARCHAR(64),created_at DATETIME(3),updated_at DATETIME(3),deleted_at DATETIME NULL)",
		"CREATE TABLE IF NOT EXISTS instance.instance_events(id BIGINT,instance_id BIGINT,status INT,event_type VARCHAR(32),updated_at DATETIME(3))",
		"CREATE TABLE IF NOT EXISTS instance.instance_renewals(id BIGINT,instance_id BIGINT,order_id BIGINT,customer_id BIGINT,ownership_version BIGINT,supplier_id BIGINT,host_id VARCHAR(64),cycle VARCHAR(32),duration BIGINT,base_expiry DATETIME(3),state VARCHAR(32),updated_at DATETIME(3))",
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
	body, e := os.ReadFile("../shared/migrations/sql/025_manual_product_current_state.sql")
	if e != nil {
		t.Fatal(e)
	}
	lines := []string{}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			lines = append(lines, line)
		}
	}
	for _, statement := range strings.Split(strings.Join(lines, "\n"), ";") {
		if strings.TrimSpace(statement) != "" {
			if e := db.Exec(statement).Error; e != nil {
				t.Fatal(e)
			}
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
	if result.Status != "review" {
		t.Fatal("missing creation IP accepted", result)
	}
	reader.facts.PrimaryIP = "192.0.2.9"
	result, e = ProcessManualProduct(ctx, tx, reader, 1, 901, "127.0.0.1", in)
	if e != nil || result.Status != "normal" {
		t.Fatal("complete creation did not become normal", result, e)
	}
	normal, e := List(ctx, tx, "manual-products", Filter{Status: "normal", Page: 1, PageSize: 20})
	if e != nil || normal.Total != 1 || normal.Items[0].IPAddress != "192.0.2.9" {
		t.Fatal("normal state/IP not persisted", normal, e)
	}
	var auditCount int64
	if e := tx.Table("system.todo_audits").Count(&auditCount).Error; e != nil || auditCount != 0 {
		t.Fatal("unrelated refresh history stored", auditCount, e)
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
	in.Reason = ""
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
	if e != nil || len(history.Items) != 0 {
		t.Fatal("cleared items exposed as history", history, e)
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
	t.Log("isolated rollback: fresh provider GET only, query failures retain work, identity mismatch blocked, current state only, no reasons/history, direct clear idempotent, original business failures retained, cleared items not archived, new failures reappear, stale selections rejected")
}
