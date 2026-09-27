package billing

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
	"time"
)

type blockedNotice struct{ entered, release chan struct{} }

func (n blockedNotice) NotifyCustomer(ctx context.Context, _ string, _ int64, _ string, _ map[string]string) error {
	close(n.entered)
	select {
	case <-n.release:
		return errors.New("isolated transport failure")
	case <-ctx.Done():
		return ctx.Err()
	}
}

type failedNotice struct{}

func (failedNotice) NotifyCustomer(context.Context, string, int64, string, map[string]string) error {
	return errors.New("isolated transport failure")
}

func TestNotificationBoundariesMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := database.DB()
	defer pool.Close()
	ctx := context.Background()
	service := New(database)
	if err := snowflake.Configure(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("INSERT INTO billing.notification_outbox(id,customer_id,amount,reason,status,attempts,next_attempt) VALUES (89001,77,1.00,'isolated','pending',0,NOW())").Error; err != nil {
		t.Fatal(err)
	}
	row, err := service.claimNotice(ctx)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	sender := blockedNotice{make(chan struct{}), make(chan struct{})}
	finished := make(chan error, 1)
	go func() { finished <- service.deliverNotice(ctx, sender, row) }()
	<-sender.entered
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	err = database.WithContext(lockCtx).Exec("UPDATE billing.notification_outbox SET reason=reason WHERE id=89001").Error
	cancel()
	close(sender.release)
	if err != nil {
		t.Fatal("network call retained database row lock", err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	for n := 2; n <= 5; n++ {
		database.Exec("UPDATE billing.notification_outbox SET next_attempt=NOW() WHERE id=89001")
		row, err = service.claimNotice(ctx)
		if err != nil || row == nil {
			t.Fatal(err)
		}
		if err := service.deliverNotice(ctx, failedNotice{}, row); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	database.Raw("SELECT status FROM billing.notification_outbox WHERE id=89001").Scan(&state)
	if state != "dead" {
		t.Fatal("failure retries unbounded", state)
	}
	row, err = service.claimNotice(ctx)
	if err != nil || row != nil {
		t.Fatal("dead notification reclaimed", err)
	}
	t.Log("network call holds no row lock; five failures enter dead state, not reclaimed; no external notification sent")
	// A sender can outlive its lease after process suspension or network delay.
	// Another worker must retain uncertainty, not automatically resend, and the
	// old sender must not turn that uncertainty into a retry or success.
	if err := database.Exec("INSERT INTO billing.notification_outbox(id,customer_id,amount,reason,status,attempts,next_attempt) VALUES (89002,77,1.00,'isolated expired sender','sending',1,DATE_SUB(NOW(),INTERVAL 1 MINUTE))").Error; err != nil {
		t.Fatal(err)
	}
	row, err = service.claimNotice(ctx)
	if err != nil || row != nil {
		t.Fatal("expired sending notification was automatically retried", err)
	}
	if err := database.Raw("SELECT status FROM billing.notification_outbox WHERE id=89002").Scan(&state).Error; err != nil || state != "unknown" {
		t.Fatal("expired sending result not retained as unknown", err, state)
	}
	if err := service.deliverNotice(ctx, failedNotice{}, &notice{ID: 89002, CustomerID: 77, Amount: 100, Attempts: 1}); err == nil {
		t.Fatal("stale sender completion accepted")
	}
	if err := database.Raw("SELECT status FROM billing.notification_outbox WHERE id=89002").Scan(&state).Error; err != nil || state != "unknown" {
		t.Fatal("stale sender overwrote unknown result", err, state)
	}
	t.Log("expired sender enters unknown without resend; stale completion cannot overwrite it; controlled notifier only")
	if err := service.ReviewNotice(ctx, 89002, 1, "127.0.0.1", "isolated review", "retry", "unknown", 1, false); !errors.Is(err, ErrNoticeReview) {
		t.Fatal("retry without acknowledgement accepted", err)
	}
	if err := service.ReviewNotice(ctx, 89002, 1, "127.0.0.1", "isolated review", "retry", "unknown", 1, true); err != nil {
		t.Fatal(err)
	}
	if err := service.ReviewNotice(ctx, 89002, 1, "127.0.0.1", "duplicate", "retry", "unknown", 1, true); !errors.Is(err, ErrNoticeReview) {
		t.Fatal("duplicate review accepted", err)
	}
	row, err = service.claimNotice(ctx)
	if err != nil || row == nil || row.ID != 89002 || row.Attempts != 2 {
		t.Fatal("manual retry reset generation", err, row)
	}
	if err := service.deliverNotice(ctx, failedNotice{}, row); err != nil {
		t.Fatal(err)
	}
	if err := database.Raw("SELECT status FROM billing.notification_outbox WHERE id=89002").Scan(&state).Error; err != nil || state != "dead" {
		t.Fatal("manual retry caused automatic retries", err, state)
	}
	if err := service.ReviewNotice(ctx, 89002, 1, "127.0.0.1", "verified manually", "resolve", "dead", 1, false); !errors.Is(err, ErrNoticeReview) {
		t.Fatal("stale generation accepted", err)
	}
	if err := service.ReviewNotice(ctx, 89002, 1, "127.0.0.1", "verified manually", "resolve", "dead", 2, false); err != nil {
		t.Fatal(err)
	}
	var audits int64
	if err := database.Table("system.audit_logs").Where("target_type='balance_notification' AND target_id=89002").Count(&audits).Error; err != nil || audits != 2 {
		t.Fatal("review audit mismatch", err, audits)
	}
	if err := database.Raw("SELECT status FROM billing.notification_outbox WHERE id=89002").Scan(&state).Error; err != nil || state != "resolved" {
		t.Fatal("manual resolution not retained", err, state)
	}
	t.Log("manual retry requires acknowledgement, preserves generation, retries once; stale/duplicate decisions rejected; two atomic audit records; no external send")
}
