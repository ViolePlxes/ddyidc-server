package cdnsell

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

func cdnFixture(t *testing.T, db *gorm.DB, user, domain int64, now time.Time) {
	t.Helper()
	for _, q := range []string{
		fmt.Sprintf("INSERT INTO customer.customers(id,username,password_hash,status) VALUES(%d,'cdn-local-%d','disabled',1)", user, user),
		fmt.Sprintf("INSERT INTO system.cdnsell_domains(id,user_id,domain,business_type,origin_type,origin_host,origin_protocol) VALUES(%d,%d,'cdn-local-%d.example.com','web','domain','origin.example.com','https')", domain, user, domain),
		fmt.Sprintf("INSERT INTO system.cdnsell_batches(id,user_id,product_id,order_id,original_bytes,remaining_bytes,effective_time,expire_time) VALUES(%d,%d,1,%d,1000,1000,'%s','%s')", domain, user, domain, now.Add(-time.Hour).In(time.Local).Format("2006-01-02 15:04:05"), now.Add(time.Hour).In(time.Local).Format("2006-01-02 15:04:05")),
	} {
		if e := db.Exec(q).Error; e != nil {
			t.Fatal(e)
		}
	}
}
func TestUsageLedgerMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	now := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	s := NewService(db, nil, nil)
	s.now = func() time.Time { return now }
	cdnFixture(t, db, 882001, 882101, now)
	p := provider.UsagePoint{Start: now.Add(-5 * time.Minute), End: now, Bytes: 600}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.ApplyUsage(ctx, 882001, 882101, p); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	check := func(remain, consumed, over int64) {
		t.Helper()
		var b Batch
		if e := db.Table(batchesTable).Where("id=882101").Take(&b).Error; e != nil {
			t.Fatal(e)
		}
		var w usageWindow
		db.Table(usageWindowsTable).Where("domain_id=882101").Take(&w)
		if b.RemainingBytes != remain || b.ConsumedBytes != consumed || w.OverageBytes != over {
			t.Fatalf("batch %d/%d window-over %d", b.RemainingBytes, b.ConsumedBytes, w.OverageBytes)
		}
	}
	check(400, 600, 0)
	p.Bytes = 1200
	if e := s.ApplyUsage(ctx, 882001, 882101, p); e != nil {
		t.Fatal(e)
	}
	check(0, 1000, 200)
	cdnFixture(t, db, 882002, 882102, now)
	if e := s.ApplyUsage(ctx, 882002, 882102, provider.UsagePoint{Start: p.Start, End: p.End, Bytes: 75}); e != nil {
		t.Fatal(e)
	}
	overview, e := s.Overview(ctx, 882001)
	if e != nil {
		t.Fatal(e)
	}
	if overview.DistributedBytes != 1200 || overview.UsedBytes != 1000 || overview.TodayBytes != 1200 || len(overview.DailyTrend) != 30 || overview.DailyTrend[29].Bytes != 1200 {
		t.Fatalf("delivery must include overage and exclude another customer: %+v", overview)
	}
	p.Bytes = 300
	if e := s.ApplyUsage(ctx, 882001, 882101, p); e != nil {
		t.Fatal(e)
	}
	check(700, 300, 0)
	p.Bytes = 0
	if e := s.ApplyUsage(ctx, 882001, 882101, p); e != nil {
		t.Fatal(e)
	}
	check(1000, 0, 0)
	if e := s.ApplyUsage(ctx, 882002, 882101, p); e == nil {
		t.Fatal("foreign customer metering accepted")
	}
	p.Bytes = -1
	if e := s.ApplyUsage(ctx, 882001, 882101, p); e == nil {
		t.Fatal("negative bytes accepted")
	}
	p.Bytes = 10
	p.End = p.End.Add(time.Minute)
	if e := s.ApplyUsage(ctx, 882001, 882101, p); e == nil {
		t.Fatal("bad window accepted")
	}
	t.Log("12 concurrent copies deducted once; overage, downward and zero corrections restored exact original pool; customer isolation")
}
func TestTaskRecoveryMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	s := NewService(db, nil, nil)
	now := time.Now()
	s.now = func() time.Time { return now }
	if e := db.Exec("INSERT INTO system.cdnsell_tasks(id,task_no,task_type,biz_type,biz_id,status,lease_until) VALUES(883001,'local-recovery','domain_add','domain',883001,'running',?)", now.Add(-time.Minute)).Error; e != nil {
		t.Fatal(e)
	}
	if e := s.recoverExpiredTasks(context.Background()); e != nil {
		t.Fatal(e)
	}
	var status string
	db.Table(tasksTable).Select("status").Where("id=883001").Scan(&status)
	if status != "need_manual" {
		t.Fatal("unknown write retried", status)
	}
	t.Log("expired external-write lease moved to manual reconciliation without replay")
}

func TestDomainTaskAtomicityMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	s := NewService(db, nil, nil)
	now := time.Now()
	cdnFixture(t, db, 884001, 884101, now)
	if e := db.Table(domainsTable).Where("id=884101").Updates(map[string]any{"service_no": "local-only", "upstream_status": "active"}).Error; e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.enqueueDomainTask(context.Background(), "domain_sync", 884101) }()
	}
	wg.Wait()
	var count int64
	if e := db.Table(tasksTable).Where("biz_id=884101").Count(&count).Error; e != nil || count != 1 {
		t.Fatalf("concurrent tasks=%d err=%v", count, e)
	}
	if _, e := s.DeleteDomain(context.Background(), 884001, 884101); e == nil {
		t.Fatal("deletion should reject while sync pending")
	}
	var d DomainRecord
	if e := db.Table(domainsTable).Where("id=884101").Take(&d).Error; e != nil {
		t.Fatal(e)
	}
	if d.Deleted {
		t.Fatal("failed enqueue hid domain")
	}
	t.Log("12 concurrent enqueues produced one task; failed removal retained domain visibility")
}
func TestProductZeroValueUpdateMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	s := NewService(db, nil, nil)
	p := Product{Name: "local-edit", CapacityBytes: 1000, ValidDays: 30, Price: 100, MinQty: 1, MaxQty: 1, Status: true, IsHot: true, Sort: 7}
	if e := s.SaveProduct(context.Background(), &p); e != nil {
		t.Fatal(e)
	}
	p.Status = false
	p.IsHot = false
	p.Sort = 0
	if e := s.SaveProduct(context.Background(), &p); e != nil {
		t.Fatal(e)
	}
	var got Product
	if e := db.Table(productsTable).Where("id=?", p.ID).Take(&got).Error; e != nil {
		t.Fatal(e)
	}
	if got.Status || got.IsHot || got.Sort != 0 {
		t.Fatal("zero values were dropped")
	}
}
func TestReferencedProductCannotBeDeletedMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	s := NewService(db, nil, nil)
	p := Product{Name: "local-retained", CapacityBytes: 1000, ValidDays: 30, Price: 100, MinQty: 1, MaxQty: 1, Status: true}
	if e := s.SaveProduct(context.Background(), &p); e != nil {
		t.Fatal(e)
	}
	cdnFixture(t, db, 887001, 887101, time.Now())
	if e := db.Table(batchesTable).Where("id=887101").Update("product_id", p.ID).Error; e != nil {
		t.Fatal(e)
	}
	if e := s.DeleteProduct(context.Background(), p.ID); e == nil {
		t.Fatal("referenced product deleted")
	}
	var count int64
	if e := db.Table(productsTable).Where("id=?", p.ID).Count(&count).Error; e != nil || count != 1 {
		t.Fatal("product history lost")
	}
}
