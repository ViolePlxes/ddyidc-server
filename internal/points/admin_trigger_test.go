package points

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

func TestAdminTriggerMySQL(t *testing.T) {
	db := openQA(t)
	ctx := context.Background()
	s := NewService(db, nil)
	var previousEnabled bool
	if err := db.Table(settingsTable).Select("enabled").Where("id=1").Scan(&previousEnabled).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Table(settingsTable).Where("id=1").Update("enabled", previousEnabled) })
	if err := db.Table(settingsTable).Where("id=1").Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	const user int64 = 9959401
	cleanup := func() {
		cleanupCore(ctx, db, user)
		db.Exec("DELETE FROM system.points_admin_actions WHERE admin_id=?", user)
		db.Exec("DELETE FROM system.points_rules WHERE code='qa_admin_trigger'")
		db.Exec("DELETE FROM system.admins WHERE id=?", user)
		db.Exec("DELETE FROM customer.customers WHERE id=?", user)
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, query := range []string{
		"INSERT INTO system.admins(id,username,password_hash,real_name,status) VALUES(9959401,'qa_points_trigger','unusable','isolated',1)",
		"INSERT INTO customer.customers(id,username,password_hash,status) VALUES(9959401,'qa_points_trigger','unusable',1)",
	} {
		if err := db.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Table(rulesTable).Create(&Rule{Name: "isolated trigger", Code: "qa_admin_trigger", Event: "qa_admin_trigger", Type: "fixed", PointsFixed: 7, LimitType: "none", Enabled: true, LadderConfig: json.RawMessage(`[]`)}).Error; err != nil {
		t.Fatal(err)
	}
	call := func(users []int64, request, notes string) error {
		_, err := s.AdminTriggerRequest(ctx, user, "127.0.0.1", request, users, "qa_admin_trigger", 0, notes)
		return err
	}
	if call(nil, "qa-admin-trigger-empty", "reason") == nil {
		t.Fatal("empty user list accepted")
	}
	if call([]int64{user}, "qa-admin-trigger-no-notes", "") == nil {
		t.Fatal("missing audit reason accepted")
	}
	if call([]int64{user, user}, "qa-admin-trigger-duplicate", "reason") == nil {
		t.Fatal("duplicate users accepted")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- call([]int64{user}, "qa-admin-trigger-idempotent", "isolated acceptance")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	account, err := s.Account(ctx, user)
	if err != nil || account.Balance != 7 {
		t.Fatalf("repeated trigger %+v %v", account, err)
	}
	var count int64
	db.Table("system.points_admin_actions").Where("admin_id=?", user).Count(&count)
	if count != 1 {
		t.Fatalf("audit count=%d", count)
	}
	if call([]int64{user}, "qa-admin-trigger-idempotent", "changed reason") == nil {
		t.Fatal("changed request accepted")
	}
}
