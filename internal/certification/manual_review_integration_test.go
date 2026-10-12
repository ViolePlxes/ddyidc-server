package certification

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"sync"
	"testing"
	"time"
)

// Runs only against a disposable VM/CI database; no real identity provider.
func TestPendingManualReviewMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	for _, q := range []string{"CREATE DATABASE IF NOT EXISTS customer", "CREATE DATABASE IF NOT EXISTS `system`", `CREATE TABLE IF NOT EXISTS system.audit_logs (id bigint PRIMARY KEY, operator_type int, operator_id bigint, action varchar(128), target_type varchar(128), target_id bigint, ip varchar(128), detail text, created_at datetime(3), updated_at datetime(3))`} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("CREATE TABLE IF NOT EXISTS customer.customer_realname (id bigint PRIMARY KEY, customer_id bigint, certify_channel varchar(16), certify_id varchar(128), certify_config text, certify_attempts int, certify_fee_order bigint, type int, name text, phone text, document_type int, id_card_no text, document_hash text, id_card_front text, id_card_back text, company_name text, company_license text, company_hash text, company_license_img text, legal_type int, authorization text, status int, reject_reason text, auditor_id bigint, audited_at datetime(3), created_at datetime(3), updated_at datetime(3))").Error; err != nil {
		t.Fatal(err)
	}
	s := New(db, nil, []byte("0123456789abcdef0123456789abcdef"), t.TempDir())
	ctx := context.Background()
	for _, channel := range []string{"manual", "alipay"} {
		for _, action := range []string{"pass", "reject"} {
			t.Run(channel+"_"+action, func(t *testing.T) {
				row := Record{ID: snowflake.GenID(), CustomerID: snowflake.GenID(), CertifyChannel: channel, Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
				defer db.Delete(&row)
				defer db.Table("system.audit_logs").Where("target_id=?", row.CustomerID).Delete(map[string]any{})
				if action == "reject" {
					if err := s.Review(ctx, row.ID, 11, action, "", "isolated"); err == nil {
						t.Fatal("empty rejection accepted")
					}
				}
				if err := s.Review(ctx, row.ID, 11, action, "isolated review", "isolated"); err != nil {
					t.Fatal(err)
				}
				var got Record
				if err := db.First(&got, row.ID).Error; err != nil {
					t.Fatal(err)
				}
				want := 2
				if action == "reject" {
					want = 3
				}
				if got.Status != want || got.AuditorID == nil || *got.AuditorID != 11 || got.AuditedAt == nil {
					t.Fatalf("missing decision/audit: %+v", got)
				}
				if err := s.Review(ctx, row.ID, 12, "pass", "duplicate", "isolated"); err == nil {
					t.Fatal("duplicate accepted")
				}
				if channel == "alipay" {
					result, err := s.alipayStatus(ctx, row.CustomerID, "isolated", true)
					if err != nil || result.Status != want {
						t.Fatalf("terminal decision overwritten or queried provider: %+v %v", result, err)
					}
				}
				var n int64
				db.Table("system.audit_logs").Where("target_id=? AND operator_type=1", row.CustomerID).Count(&n)
				if n != 1 {
					t.Fatalf("audit count=%d", n)
				}
			})
		}
	}
	row := Record{ID: snowflake.GenID(), CustomerID: snowflake.GenID(), CertifyChannel: "alipay", Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&row)
	defer db.Table("system.audit_logs").Where("target_id=?", row.CustomerID).Delete(map[string]any{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, action := range []string{"pass", "reject"} {
		wg.Add(1)
		go func(a string) {
			defer wg.Done()
			results <- s.Review(ctx, row.ID, 11, a, "concurrent review", "isolated")
		}(action)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("concurrent successful decisions=%d", success)
	}
}
