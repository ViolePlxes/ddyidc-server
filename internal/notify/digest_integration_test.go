package notify

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type digestMailProbe struct {
	mu    sync.Mutex
	calls int
}

func (m *digestMailProbe) Send(context.Context, MailConfig, string, string, string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return nil
}

type digestSMSProbe struct{ calls int }

func (s *digestSMSProbe) Send(context.Context, SMSConfig, string, string, map[string]string) (string, error) {
	s.calls++
	return "", context.DeadlineExceeded
}

func TestDigestQueueMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
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
	pool.SetMaxOpenConns(10)
	for _, sql := range []string{"CREATE DATABASE IF NOT EXISTS `system`", "CREATE DATABASE IF NOT EXISTS instance", "CREATE TABLE IF NOT EXISTS system.id_registry (id BIGINT PRIMARY KEY)", "CREATE TABLE IF NOT EXISTS system.notify_logs (id BIGINT PRIMARY KEY,scene VARCHAR(64),channel VARCHAR(16),target VARCHAR(255),template_id BIGINT NULL,content_snapshot MEDIUMTEXT,status VARCHAR(24),error TEXT,request_id VARCHAR(128),business_id VARCHAR(128),created_at DATETIME)", "CREATE TABLE IF NOT EXISTS system.todo_audits (request_id VARCHAR(128))", "CREATE TABLE IF NOT EXISTS instance.instances (id BIGINT PRIMARY KEY,customer_id BIGINT,instance_no VARCHAR(128),product_name VARCHAR(255),primary_ip VARCHAR(255),expires_at DATETIME,deleted_at DATETIME NULL,status INT,config_snapshot JSON)"} {
		if e = db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	for _, sql := range NewRepository(db).ensureDigestSchema() {
		if e = db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e = snowflake.Configure(context.Background(), pool); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC().Truncate(time.Second)
	mail := &digestMailProbe{}
	repo := &fakeNotifyRepository{values: map[string]string{"notify.digest.enabled": "true", "notify.digest.dispatch_enabled": "true", "notify.digest.all_customers": "true"}, templates: map[string]*NotificationTemplate{"expiry_digest": {Enabled: true, Channel: ChannelBoth, Content: "您有{{server_count}}台服务器即将到期"}}, contact: Contact{Email: "isolated@example.test", Phone: "19900000000"}}
	manager := testManager(repo, mail)
	manager.now = func() time.Time { return now }
	d := &DigestService{db: db, manager: manager}
	manager.digest = d
	insert := func(id, customer int64) ExpiringInstance {
		due := now.Add(48 * time.Hour)
		item := ExpiringInstance{ID: id, CustomerID: customer, InstanceNo: "fixture", ProductName: "fixture", PrimaryIP: "127.0.0.1", ExpiresAt: &due}
		if e := db.Exec("INSERT INTO instance.instances VALUES (?,?,?,?,'127.0.0.1',?,NULL,1,JSON_OBJECT('billingCycle','monthly'))", id, customer, item.InstanceNo, item.ProductName, due).Error; e != nil {
			t.Fatal(e)
		}
		return item
	}
	first, second, other := insert(1001, 990000101), insert(1002, 990000101), insert(1003, 990000103)
	for _, item := range []ExpiringInstance{first, second, other, first} {
		if _, e := d.collect(context.Background(), item, now); e != nil {
			t.Fatal(e)
		}
	}
	var count int64
	db.Model(&DigestTask{}).Count(&count)
	if count != 4 {
		t.Fatalf("expected 2 users x 2 channels, got %d", count)
	}
	db.Model(&DigestItem{}).Count(&count)
	if count != 6 {
		t.Fatalf("cycle dedup failed: %d", count)
	}
	// Renew one item after collection: it must disappear from the frozen send snapshot.
	db.Exec("UPDATE instance.instances SET expires_at=DATE_ADD(expires_at,INTERVAL 30 DAY) WHERE id=1002")
	db.Model(&DigestTask{}).Where("status='pending'").Update("scheduled_at", now)
	var wait sync.WaitGroup
	for n := 0; n < 8; n++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if e := d.Work(context.Background()); e != nil {
				t.Error(e)
			}
		}()
	}
	wait.Wait()
	if mail.calls != 2 {
		t.Fatalf("same mailbox must receive separate per-user batches exactly once: %d", mail.calls)
	}
	var email DigestTask
	if e := db.Where("customer_id=? AND channel='email'", first.CustomerID).First(&email).Error; e != nil {
		t.Fatal(e)
	}
	if email.Status != "submitted" || email.Count != 1 {
		t.Fatalf("renewed item was sent: %+v", email)
	}
	if e = d.Work(context.Background()); e != nil {
		t.Fatal(e)
	}
	if mail.calls != 2 {
		t.Fatal("repeated scan re-sent email")
	}
	var skipped DigestItem
	db.Where("instance_id=1002 AND channel='email'").First(&skipped)
	if skipped.Status != "canceled" {
		t.Fatal("renewal cancellation missing")
	}
	// A new cycle after today's submission must be queued for tomorrow, never a second automatic email today.
	next := insert(1004, first.CustomerID)
	if _, e = d.collect(context.Background(), next, now); e != nil {
		t.Fatal(e)
	}
	var pending DigestTask
	db.Where("customer_id=? AND channel='email' AND status='pending'", first.CustomerID).First(&pending)
	if pending.NotifyDate == now.In(digestZone).Format("2006-01-02") {
		t.Fatal("daily quota bypass")
	}
	// Cancel all remaining SMS instances: an absent SMS template does not cause mock delivery.
	var sms DigestTask
	db.Where("customer_id=? AND channel='sms'", first.CustomerID).First(&sms)
	if sms.Status != "pending" || sms.Attempts != 0 {
		t.Fatal("unconfigured SMS sent or consumed quota")
	}
	probe := &digestSMSProbe{}
	manager.sms = probe
	manager.yaml.SMS.Templates["expiry_digest"] = "SMS_ISOLATED_FIXTURE"
	if e = d.Work(context.Background()); e != nil {
		t.Fatal(e)
	}
	if probe.calls != 0 {
		t.Fatal("unapproved SMS template sent")
	}
	repo.values["notify.digest.sms.application"] = `{"status":"approved","code":"SMS_ISOLATED_FIXTURE"}`
	if e = d.Work(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = d.Work(context.Background()); e != nil {
		t.Fatal(e)
	}
	if probe.calls != 2 || mail.calls != 2 {
		t.Fatal("unknown SMS result retried or successful email duplicated")
	}
	db.First(&sms, "id=?", sms.ID)
	if sms.Status != "unknown" {
		t.Fatal("timeout must remain unknown")
	}
	checkSMSReceiptsMySQL(t, db)
}
