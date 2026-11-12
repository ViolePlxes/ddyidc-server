package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/upstream/metrics"
	"ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
	"time"
)

type adminReadMock struct {
	provider.UpstreamProvider
	contracts.InstanceProvider
	ctx    context.Context
	calls  *int
	detail *contracts.InstanceDetail
}

func (p *adminReadMock) WithInstanceContext(ctx context.Context) contracts.InstanceProvider {
	q := *p
	q.ctx = ctx
	return &q
}
func (p *adminReadMock) GetInstanceDetail(int64) (*contracts.InstanceDetail, error) {
	*p.calls++
	if !metrics.IsBackground(p.ctx) {
		return nil, errors.New("background rate scope missing")
	}
	return p.detail, nil
}

type adminReadResolver struct{ p *adminReadMock }

func (r adminReadResolver) ProviderFor(string) (provider.UpstreamProvider, error) { return r.p, nil }
func (r adminReadResolver) GetOrderPageByLocalProduct(string) (*contracts.OrderPage, error) {
	return nil, ErrUnsupported
}

func TestAdminInputAndSyncWhitelist(t *testing.T) {
	for _, in := range []AdminListInput{{Sort: "id;DROP TABLE"}, {Direction: "desc;--"}, {PageSize: 101}, {Search: strings.Repeat("字", 129)}, {MinAmount: "1.001"}, {CreatedFrom: "2026-10-05T00:00:00Z", CreatedTo: "2026-10-04T00:00:00Z"}} {
		if _, e := normalizeAdminInput(in); !errors.Is(e, ErrAdminListInput) {
			t.Fatalf("unsafe input accepted: %+v %v", in, e)
		}
	}
	now := time.Now().UTC()
	h := &dom.Host{UpstreamInstanceID: "42", ConfigSnapshot: `{"renewalAmount":"9.99","password":"LOCAL_SECRET","billingCycle":"monthly"}`}
	detail := &contracts.InstanceDetail{ID: "42", Hostname: "synced-host", Port: "2222", OperatingSystem: "Ubuntu", Password: "REMOTE_SECRET", Status: "deleted", PowerStatus: "suspended", Remark: "remote remark", RenewalAmount: "1.00", PrimaryIP: "203.0.113.8", ExpiresAt: &now, Config: []contracts.InstanceConfig{{Field: "cpu", Value: "4"}, {Field: "password", Value: "REMOTE_SECRET"}}, IPs: []contracts.InstanceIP{{Address: "203.0.113.9"}, {Address: "not-an-ip"}}}
	fields, e := safeSyncFields(h, detail, now)
	if e != nil {
		t.Fatal(e)
	}
	for _, k := range []string{"status", "power_status", "customer_id", "ownership_version", "order_id", "product_id", "remark", "upstream_provider_id", "upstream_instance_id", "auto_renew"} {
		if _, ok := fields[k]; ok {
			t.Fatal("forbidden overwrite", k)
		}
	}
	raw := fields["config_snapshot"].(string)
	if strings.Contains(raw, "REMOTE_SECRET") {
		t.Fatal("remote secret cached")
	}
	var cache map[string]any
	_ = json.Unmarshal([]byte(raw), &cache)
	if cache["renewalAmount"] != "9.99" || cache["password"] != "LOCAL_SECRET" {
		t.Fatal("existing snapshot overwritten")
	}
	updated := *h
	updated.ConfigSnapshot = raw
	row := listItem(&updated)
	if row.Hostname != "synced-host" || row.Port != "2222" || row.OperatingSystem != "Ubuntu" || row.RenewalAmount != "9.99" || row.BillingCycle != "monthly" {
		t.Fatalf("admin sync display facts or retail price lost: %+v", row)
	}
	detail.ID = "43"
	if _, e = safeSyncFields(h, detail, now); e == nil {
		t.Fatal("wrong resource accepted")
	}
	v := syncTaskView(AdminSyncTask{ID: 1, State: "running", Items: "[]", UpdatedAt: now.Add(-3 * time.Minute)})
	if v.State != "interrupted" {
		t.Fatal("crash not surfaced")
	}
}

// This test adds only reserved fixtures in one rolled-back transaction. It
// neither drops schemas nor changes existing customers, balances or machines.
func TestAdminListMySQL(t *testing.T) {
	socket := os.Getenv("ADMIN_INSTANCES_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated VM MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, e := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	pool, _ := db.DB()
	defer pool.Close()
	ctx := context.Background()
	if e = snowflake.Configure(ctx, pool); e != nil {
		t.Fatal(e)
	}
	service := NewService(repo.NewRepository(db), nil)
	service.db = db
	if e = service.ensureAdminSchema(ctx); e != nil {
		t.Fatal(e)
	}
	tx := db.Begin()
	defer tx.Rollback()
	service.db = tx
	service.repo = repo.NewRepository(tx)
	exec := func(query string, args ...any) {
		t.Helper()
		if e := tx.Exec(query, args...).Error; e != nil {
			t.Fatal(e)
		}
	}
	exec("INSERT INTO customer.customers(id,username,password_hash,phone,email) VALUES(99004001,'admin-list-fixture','!disabled','13800138000','fixture@example.invalid')")
	exec("INSERT INTO system.admins(id,username,password_hash,real_name,status,is_super) VALUES(99004002,'admin-list-super','!disabled','VM fixture',1,1),(99004003,'admin-list-denied','!disabled','VM fixture',1,0)")
	a, e := service.AdminAccess(ctx, 99004002)
	if e != nil || !a.Export || !a.Sync || !a.Contact {
		t.Fatal("super access", e, a)
	}
	if _, e = service.AdminAccess(ctx, 99004003); !errors.Is(e, ErrForbidden) {
		t.Fatal("unauthorized staff", e)
	}
	a.CustomerIDs = []int64{99004001}
	now := time.Now().UTC()
	tomorrow := now.Add(24 * time.Hour)
	yesterday := now.Add(-24 * time.Hour)
	for i := 0; i < 7; i++ {
		cycle := "monthly"
		amount := `"10.01"`
		state := uint8(3)
		power := "running"
		expiry := &tomorrow
		customer := int64(99004001)
		if i == 1 {
			power = "suspended"
			state = 4
		}
		if i == 2 {
			expiry = &yesterday
		}
		if i == 3 {
			cycle = "onetime"
		}
		if i == 4 {
			amount = "null"
		}
		if i == 5 {
			state = 6
		}
		if i == 6 {
			customer = 99004099
		}
		snapshot := `{"billingCycle":"` + cycle + `","renewalAmount":` + amount + `,"hostname":"vm-fixture","syncedIps":[{"address":"203.0.113.77"},{"address":"203.0.113.77"}],"password":"NEVER_EXPORT"}`
		var outer map[string]any
		if err := json.Unmarshal([]byte(snapshot), &outer); err != nil {
			t.Fatal(err)
		}
		outer["renewalAmount"] = "0.01" // Legacy procurement cost must never win.
		outer["listDisplay"] = map[string]any{"billingCycle": cycle, "renewalAmount": "0.02"}
		outer["billingCycle"] = "" // Current cycle also exists in the new display path.
		var amountValue any
		if err := json.Unmarshal([]byte(amount), &amountValue); err != nil {
			t.Fatal(err)
		}
		outer["supplierReadSnapshot"] = map[string]any{"customerId": customer, "ownershipVersion": 1, "supplierId": 1, "remoteId": "42", "billingCycle": cycle, "amount": amountValue, "priceSource": "upstream_original", "priceState": "verified", "verifiedAt": now.Unix()}
		encoded, err := json.Marshal(outer)
		if err != nil {
			t.Fatal(err)
		}
		snapshot = string(encoded)
		fixtureSupplier := int64(1)
		h := dom.Host{ID: int64(99004100 + i), InstanceNo: "ADMIN-LIST-" + string(rune('A'+i)), CustomerID: customer, ProductID: 99004200, ProductName: "fixture product", UpstreamProviderID: &fixtureSupplier, UpstreamInstanceID: "42", ModuleType: "cloud", ConfigSnapshot: snapshot, Status: state, PowerStatus: power, CreatedAt: now.Add(time.Duration(i) * time.Hour), UpdatedAt: now, ExpiresAt: expiry, OwnershipVersion: 1, Remark: "fixture remark"}
		if e = tx.Create(&h).Error; e != nil {
			t.Fatal(e)
		}
	}
	result, e := service.AdminList(ctx, a, AdminListInput{PageSize: 2})
	if e != nil {
		t.Fatal(e)
	}
	if result.Total != 6 || len(result.Items) != 2 || result.Counts["suspended"] != 1 || result.Counts["due"] != 3 || result.Counts["expired"] != 1 || result.Counts["terminated"] != 1 || result.TotalRenewalSum != "50.05" || result.MissingPriceCount != 1 {
		t.Fatalf("wrong statistics: %+v", result)
	}
	missing, e := service.AdminList(ctx, a, AdminListInput{MissingPrice: "1", PageSize: 100})
	if e != nil || missing.Total != result.MissingPriceCount || len(missing.Items) != int(missing.Total) {
		t.Fatalf("missing quote filter mismatch: %+v %v", missing, e)
	}
	for _, row := range missing.Items {
		if row.RenewalAmount != nil {
			t.Fatalf("filtered row has a quote: %+v", row)
		}
	}
	exec("UPDATE customer.customers SET username='v10_11722339' WHERE id=99004001")
	current, e := service.AdminList(ctx, a, AdminListInput{})
	if e != nil || current.Items[0].Username != "99004001" {
		t.Fatal("migration username still displayed", e)
	}
	choices, e := service.AdminFilterOptions(ctx, a)
	if e != nil || choices["customers"][0].Name != "99004001" {
		t.Fatal("migration username in filter", e)
	}
	exported, e := service.AdminExportRows(ctx, a, AdminListInput{})
	if e != nil || exported[0].Username != "99004001" {
		t.Fatal("migration username in export", e)
	}
	var originalSnapshot string
	if e = tx.Table("instance.instances").Select("config_snapshot").Where("id=99004100").Scan(&originalSnapshot).Error; e != nil {
		t.Fatal(e)
	}
	for field, bad := range map[string]any{"customerId": 1, "ownershipVersion": 2, "supplierId": 2, "remoteId": "other", "billingCycle": "yearly", "priceSource": "upstream_cost", "priceState": "unavailable", "amount": "0.00"} {
		var outer map[string]any
		_ = json.Unmarshal([]byte(originalSnapshot), &outer)
		outer["supplierReadSnapshot"].(map[string]any)[field] = bad
		raw, _ := json.Marshal(outer)
		exec("UPDATE instance.instances SET config_snapshot=? WHERE id=99004100", string(raw))
		denied, e := service.AdminList(ctx, a, AdminListInput{Search: "99004100"})
		if e != nil || len(denied.Items) != 1 || denied.Items[0].RenewalAmount != nil {
			t.Fatal("mismatched quote admitted", field, e)
		}
	}
	exec("UPDATE instance.instances SET config_snapshot=? WHERE id=99004100", originalSnapshot)
	valid, e := service.AdminList(ctx, a, AdminListInput{Search: "99004100"})
	if e != nil || valid.Items[0].RenewalAmount == nil || *valid.Items[0].RenewalAmount != "10.01" || valid.Items[0].RenewalStale {
		t.Fatal("current verified quote missing", e)
	}
	exec("UPDATE instance.instances SET config_snapshot=JSON_SET(config_snapshot,'$.supplierReadSnapshot.priceState','last_known') WHERE id=99004100")
	stale, e := service.AdminList(ctx, a, AdminListInput{Search: "99004100"})
	if e != nil || !stale.Items[0].RenewalStale || stale.Items[0].RenewalAmount == nil {
		t.Fatal("last known quote not marked", e)
	}
	exec("UPDATE instance.instances SET config_snapshot=? WHERE id=99004100", originalSnapshot)
	exec("UPDATE customer.customers SET real_name='Unverified profile' WHERE id=99004001")
	if result, e = service.AdminList(ctx, a, AdminListInput{}); e != nil || result.Items[0].RealName != "" {
		t.Fatal("unverified profile exposed as certified name", e)
	}
	exec("INSERT INTO customer.customer_realname(id,customer_id,type,name,status,created_at,updated_at) VALUES(99004400,99004001,1,'VM Verified Name',2,NOW(),NOW())")
	if result, e = service.AdminList(ctx, a, AdminListInput{}); e != nil || result.Total != 6 || (result.Items[0].RealName != "VM Verified Name" || result.Items[0].RealNameRecordID != "99004400") {
		t.Fatal("approved identity name not projected or rows duplicated", e)
	}
	exec("UPDATE customer.customer_realname SET status=3 WHERE id=99004400")
	if result, e = service.AdminList(ctx, a, AdminListInput{}); e != nil || result.Items[0].RealName != "" {
		t.Fatal("rejected identity name leaked", e)
	}
	masked := a
	masked.Contact = false
	result, e = service.AdminList(ctx, masked, AdminListInput{Search: "203.0.113.77", PageSize: 100})
	if e != nil || result.Total != 6 {
		t.Fatalf("multi IP duplicated rows: %v %+v", e, result)
	}
	if result.Items[0].Phone == "13800138000" || result.Items[0].Email == "fixture@example.invalid" {
		t.Fatal("contacts leaked")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "NEVER_EXPORT") || strings.Contains(string(raw), "password") {
		t.Fatal("secret leaked")
	}
	empty, e := service.AdminList(ctx, a, AdminListInput{Search: "%"})
	if e != nil || empty.Total != 0 {
		t.Fatal("wildcard injection", e)
	}
	filtered, e := service.AdminList(ctx, a, AdminListInput{View: "expired", MinAmount: "10.01", MaxAmount: "10.01"})
	if e != nil || filtered.Total != 1 || filtered.Counts["all"] != 5 {
		t.Fatal("filters/count scope", e, filtered)
	}
	rows, e := service.AdminExportRows(ctx, masked, AdminListInput{View: "suspended"})
	if e != nil || len(rows) != 1 || rows[0].PowerStatus != "suspended" {
		t.Fatal("export filter mismatch", e, rows)
	}
	masked.Export = false
	if _, e = service.AdminExportRows(ctx, masked, AdminListInput{}); !errors.Is(e, ErrForbidden) {
		t.Fatal("export authorization", e)
	}
	value := "fixture remark"
	var audit struct{ BeforeValue, AfterValue string }
	if _, e = service.StartAdminSync(ctx, a, "127.0.0.1", AdminSyncInput{RequestID: "test-stale-owner-20261004", Items: []AdminSyncSelection{{ID: "99004100", CustomerID: "99004001", OwnershipVersion: "9"}}}); e == nil {
		t.Fatal("stale sync accepted")
	}
	supplier := int64(1)
	exec("UPDATE instance.instances SET upstream_provider_id=?,upstream_instance_id='42' WHERE id=99004100", supplier)
	calls := 0
	later := tomorrow.Add(time.Hour)
	service.providers = adminReadResolver{&adminReadMock{calls: &calls, detail: &contracts.InstanceDetail{ID: "42", ExpiresAt: &later, PrimaryIP: "203.0.113.8", Password: "REMOTE_PRIVATE", Status: "deleted", RenewalAmount: "0.01", Remark: "remote remark"}}}
	selection := AdminSyncSelection{ID: "99004100", CustomerID: "99004001", OwnershipVersion: "2"}
	task := AdminSyncTask{ID: 99004005, AdminID: 99004002, OperatorIP: "127.0.0.1"}
	if e = service.syncAdminItem(metrics.WithBackground(ctx), task, selection); e == nil || calls != 0 {
		t.Fatal("stale binding reached provider", e, calls)
	}
	selection.OwnershipVersion = "1"
	if e = service.syncAdminItem(metrics.WithBackground(ctx), task, selection); e != nil {
		t.Fatal("mock sync", e)
	}
	var synced dom.Host
	if e = tx.Where("id=99004100").Take(&synced).Error; e != nil || calls != 1 || synced.Status != 6 || synced.PowerStatus != "deleted" || synced.Remark != value || synced.CustomerID != 99004001 || synced.PrimaryIP != "203.0.113.8" || strings.Contains(synced.ConfigSnapshot, "REMOTE_PRIVATE") {
		t.Fatal("whitelist or ownership", e, synced.Status)
	}
	if e = tx.Table("instance.admin_instance_audits").Where("task_id=?", task.ID).Take(&audit).Error; e != nil || !strings.Contains(audit.AfterValue, "203.0.113.8") || strings.Contains(audit.BeforeValue, "203.0.113.8") || strings.Contains(audit.AfterValue, "PRIVATE") {
		t.Fatal("sync before/after audit", e, audit)
	}
	exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(99004004,'instance.scope.admin.99004002','[\"99004001\"]')")
	scoped, e := service.AdminAccess(ctx, 99004002)
	if e != nil || scoped.Allows(99004099) {
		t.Fatal("scope", e)
	}
	exec("UPDATE system.system_configs SET value='[]' WHERE id=99004004")
	if _, e = service.AdminAccess(ctx, 99004002); !errors.Is(e, ErrForbidden) {
		t.Fatal("empty scope not denied", e)
	}
	t.Log("real VM MySQL: exact rounded totals, scope, statuses, multi-IP dedupe, masks, export, stale ownership, binding-verified retail quotes, current customer IDs and certification links passed; fixtures rolled back")
}
