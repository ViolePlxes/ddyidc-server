package cdnsell

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/auth"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Explicit isolated fixture only; never runs for production or standard tests.
func TestPrepareRealBrowserFixtureMySQL(t *testing.T) {
	if os.Getenv("CDN_BROWSER_FIXTURE") != "1" {
		t.Skip("explicit isolated browser fixture required")
	}
	if !strings.HasPrefix(os.Getenv("FOUNDATION_MYSQL_SOCKET"), "/tmp/ddyidc-foundation-rollback-") {
		t.Fatal("non-rehearsal database refused")
	}
	db := testenv.MySQL(t)
	hash, err := auth.HashPassword("CdnLocalAcceptance123!")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("INSERT IGNORE INTO customer.customers(id,username,password_hash,phone,status,auth_version) VALUES(930100,'cdn-browser-isolated',?,'19900000000',1,1)", hash).Error; err != nil {
		t.Fatal(err)
	}
	var admin struct{ ID int64 }
	if err = db.Table("system.admins").Select("id").Where("username=?", "initial-admin").Take(&admin).Error; err != nil {
		t.Fatal(err)
	}
	_, err = balance.New(db).Change(context.Background(), balance.ChangeRequest{CustomerID: 930100, Type: "manual_credit", Amount: "10.00", RefType: "customer", RefID: 930100, Channel: "admin_manual", RequestID: "cdn-browser-isolated-credit", AdminID: admin.ID, AdminName: "isolated-admin", OperatorIP: "127.0.0.1", ClientNotes: "测试客户可见原因", Notes: "isolated CDN browser acceptance only"})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE system.cdnsell_settings SET enabled=1 WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE system.cdnsell_p2_runtime SET cloud_writes_enabled=0 WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, nil, nil)
	product := Product{Name: "隔离验收流量包", CapacityBytes: 1073741824, ValidDays: 30, Price: 100, TierPrices: json.RawMessage(`{}`), MinQty: 1, MaxQty: 10, Description: "仅本地隔离数据库", Status: true}
	if err = s.SaveProduct(context.Background(), &product); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated user and 10.00 audited BalanceService credit prepared; cloud writes remain disabled")
}
