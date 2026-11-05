package billing

import (
	"context"
	"crypto/rand"
	dom "ddyidc/internal/billing/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"encoding/hex"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInvoiceRuntimePrivilegesMySQL(t *testing.T) {
	root := testenv.MySQL(t)
	name := fmt.Sprint("invoice_rt_", snowflake.GenID())
	var raw [24]byte
	if _, e := rand.Read(raw[:]); e != nil {
		t.Fatal(e)
	}
	password := hex.EncodeToString(raw[:])
	ctx := context.Background()
	rootPool, e := root.DB()
	if e != nil {
		t.Fatal(e)
	}
	for _, principal := range [][2]string{{"root", "127.0.0.1"}, {name, "localhost"}, {"ddyidc_runtime", "%"}} {
		if ApplyRuntimePrivileges(ctx, rootPool, principal[0], principal[1]) == nil {
			t.Fatal("unexpected runtime identity accepted")
		}
	}
	// The socket has already been checked by testenv; this role lives solely in
	// the disposable, networking-disabled server and is removed after the test.
	if e := root.Exec("CREATE USER '" + name + "'@'localhost' IDENTIFIED BY '" + password + "'").Error; e != nil {
		t.Fatal("cannot create isolated runtime role")
	}
	t.Cleanup(func() {
		if e := root.Exec("DROP USER IF EXISTS '" + name + "'@'localhost'").Error; e != nil {
			t.Error("isolated runtime cleanup failed")
		}
	})
	script, e := os.ReadFile("../../../deploy/linux/grant-invoice-runtime.sql")
	if e != nil {
		t.Fatal(e)
	}
	sql := strings.ReplaceAll(string(script), "'ddyidc_runtime'@'127.0.0.1'", "'"+name+"'@'localhost'")
	for _, statement := range strings.Split(sql, ";") {
		if strings.TrimSpace(statement) != "" {
			if e := root.Exec(statement).Error; e != nil {
				t.Fatal("invoice runtime grant failed")
			}
		}
	}
	// Repeat the exact updater implementation: grants are additive/idempotent.
	if e := applyInvoiceRuntimePrivileges(ctx, rootPool, name, "localhost"); e != nil {
		t.Fatal(e)
	}
	restricted, e := gorm.Open(mysql.Open(name+":"+password+"@unix("+os.Getenv("FOUNDATION_MYSQL_SOCKET")+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal("isolated runtime connection failed")
	}
	pool, e := restricted.DB()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { pool.Close() })
	if e := VerifyRuntimeAccess(ctx, pool); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"invoice_settings", "payable_invoices", "invoice_items", "invoice_bindings", "invoice_payment_links", "invoice_events", "invoice_unmatched_receipts"} {
		var count int64
		if e := restricted.Table("billing." + table).Count(&count).Error; e != nil {
			t.Fatalf("runtime cannot read %s", table)
		}
	}
	tx := restricted.Begin()
	defer tx.Rollback()
	id := snowflake.GenID()
	docID := fmt.Sprint("IP", id)
	now := time.Now()
	doc := dom.Invoice{ID: docID, InvoiceNo: docID, CustomerID: id, SourceType: "order", SourceID: id, Revision: 1, SourceNo: docID, Currency: "CNY", AmountDue: 100, Status: "open", Integrity: "isolated_privilege_fixture", CreatedAt: now, UpdatedAt: now}
	for _, row := range []any{&doc, &dom.InvoiceItem{ID: docID + ":item", InvoiceID: docID, Description: "isolated privilege fixture", ModuleType: "test", UnitAmount: 100, Amount: 100}, &dom.InvoiceBinding{SourceType: "order", SourceID: id, InvoiceID: docID}, &dom.InvoicePaymentLink{PaymentID: id, InvoiceID: docID, Amount: 100, Currency: "CNY", CreatedAt: now}, &dom.InvoiceEvent{InvoiceID: docID, EventKey: docID, EventType: "isolated_privilege_fixture", CreatedAt: now}} {
		if e := tx.Create(row).Error; e != nil {
			t.Fatal("required invoice append permission missing")
		}
	}
	if e := tx.Model(&doc).Update("status", "void").Error; e != nil {
		t.Fatal("required invoice transition permission missing")
	}
	if e := tx.Table("billing.invoice_bindings").Where("source_type='order' AND source_id=?", id).Update("invoice_id", docID).Error; e != nil {
		t.Fatal("required invoice binding update permission missing")
	}
	if e := tx.Rollback().Error; e != nil {
		t.Fatal("isolated invoice write rollback failed")
	}
	for _, sql := range []string{
		"DELETE FROM billing.payable_invoices WHERE 1=0",
		"UPDATE billing.invoice_items SET description='forged' WHERE 1=0",
		"UPDATE billing.invoice_events SET event_type='forged' WHERE 1=0",
		"UPDATE billing.invoice_settings SET enabled=0 WHERE 1=0",
		"UPDATE billing.accounts SET balance=0 WHERE 1=0",
		"SELECT * FROM system.system_configs LIMIT 0",
		"SELECT * FROM billing.invoice_migration_checks LIMIT 0",
		"CREATE TABLE billing.invoice_forbidden_ddl(id INT)",
	} {
		if e := restricted.Exec(sql).Error; e == nil {
			t.Fatal("runtime exceeded invoice-only grants")
		}
	}
	if e := root.Exec("REVOKE SELECT ON billing.invoice_settings FROM '" + name + "'@'localhost'").Error; e != nil {
		t.Fatal(e)
	}
	if VerifyRuntimeAccess(ctx, pool) == nil {
		t.Fatal("preflight accepted missing invoice runtime access")
	}
	if e := applyInvoiceRuntimePrivileges(ctx, rootPool, name, "localhost"); e != nil {
		t.Fatal(e)
	}
	if e := VerifyRuntimeAccess(ctx, pool); e != nil {
		t.Fatal(e)
	}
}
