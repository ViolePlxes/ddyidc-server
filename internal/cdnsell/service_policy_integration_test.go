package cdnsell

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"sync"
	"testing"
	"time"
)

func TestServicePolicyMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	s := NewService(db, nil, nil)
	s.now = func() time.Time { return now }
	cdnFixture(t, db, 891001, 891101, now)
	if e := db.Exec("UPDATE system.cdnsell_domains SET service_no='isolated',upstream_status='active',enabled=1 WHERE id=891101").Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Exec("UPDATE system.cdnsell_batches SET remaining_bytes=0,status='exhausted' WHERE id=891101").Error; e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.planServicePolicy(ctx, 891101, true, true); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	var tasks []DomainTaskRecord
	if e := db.Table(tasksTable).Where("biz_id=891101").Find(&tasks).Error; e != nil {
		t.Fatal(e)
	}
	if len(tasks) != 1 || tasks[0].TaskType != "auto_domain_disable" {
		t.Fatalf("duplicate or wrong stop tasks: %+v", tasks)
	}
	// Refill before dispatch must cancel a stale stop without contacting cloud.
	if e := db.Exec("UPDATE system.cdnsell_batches SET remaining_bytes=1000,status='running' WHERE id=891101").Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Exec("UPDATE system.cdnsell_settings SET enabled=1,auto_suspend_enabled=1,auto_restore_enabled=1 WHERE id=1").Error; e != nil {
		t.Fatal(e)
	}
	defer db.Exec("UPDATE system.cdnsell_settings SET enabled=0,auto_suspend_enabled=0,auto_restore_enabled=0 WHERE id=1")
	if e := s.executeAutomaticState(ctx, tasks[0]); e != nil {
		t.Fatal(e)
	}
	var d DomainRecord
	db.Table(domainsTable).Where("id=891101").Take(&d)
	if d.AutoTaskID != 0 || !d.Enabled {
		t.Fatal("refill did not cancel stale stop")
	}
	db.Table(tasksTable).Where("id=?", tasks[0].ID).Update("status", "success")
	db.Exec("UPDATE system.cdnsell_domains SET enabled=0,auto_suspended=1 WHERE id=891101")
	if e := s.planServicePolicy(ctx, 891101, true, true); e != nil {
		t.Fatal(e)
	}
	tasks = nil
	db.Table(tasksTable).Where("biz_id=891101 AND status='pending'").Find(&tasks)
	if len(tasks) != 1 || tasks[0].TaskType != "auto_domain_enable" {
		t.Fatal("automatic pause did not recover")
	}
	// An uncertain cloud result keeps the marker; subsequent scans cannot resubmit.
	db.Table(tasksTable).Where("id=?", tasks[0].ID).Update("status", "need_manual")
	if e := s.planServicePolicy(ctx, 891101, true, true); e != nil {
		t.Fatal(e)
	}
	var count int64
	db.Table(tasksTable).Where("biz_id=891101").Count(&count)
	if count != 2 {
		t.Fatal("uncertain operation resubmitted")
	}
	db.Exec("UPDATE system.cdnsell_domains SET auto_task_id=0,manual_disabled=1 WHERE id=891101")
	if e := s.planServicePolicy(ctx, 891101, true, true); e != nil {
		t.Fatal(e)
	}
	db.Table(tasksTable).Where("biz_id=891101").Count(&count)
	if count != 2 {
		t.Fatal("manual pause overwritten")
	}
	// Expired credit never permits automatic restore.
	db.Exec("UPDATE system.cdnsell_domains SET manual_disabled=0 WHERE id=891101")
	db.Exec("UPDATE system.cdnsell_tasks SET status='success' WHERE biz_id=891101")
	db.Exec("UPDATE system.cdnsell_batches SET expire_time=? WHERE id=891101", now.Add(-time.Second))
	if e := s.planServicePolicy(ctx, 891101, true, true); e != nil {
		t.Fatal(e)
	}
	db.Table(tasksTable).Where("biz_id=891101").Count(&count)
	if count != 2 {
		t.Fatal("expired credit permitted recovery")
	}
	t.Log("12 scans queued one stop; refill cancels stale stop; only automatic pauses recover; uncertain outcome not resubmitted; manual pause and expired credit preserved")
}
