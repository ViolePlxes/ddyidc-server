package notification

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNotificationValidationAndHub(t *testing.T) {
	e := Event{Key: "ticket:1", RecipientID: 1, Type: "ticket", Scene: "reply", Title: "title"}
	if e.Validate() != nil {
		t.Fatal("valid event refused")
	}
	bad := e
	bad.BusinessType = "https://other.invalid"
	if bad.Validate() == nil {
		t.Fatal("arbitrary URL accepted")
	}
	bad = e
	bad.Content = strings.Repeat("a", 32769)
	if bad.Validate() == nil {
		t.Fatal("oversize accepted")
	}
	b := e
	b.OccurredAt = time.Now()
	if !PayloadEqual(e, b) {
		t.Fatal("timestamp included in dedupe payload")
	}
	h := NewHub()
	a, cancel, err := h.Subscribe(1)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	other, stop, err := h.Subscribe(2)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for i := 0; i < 1000; i++ {
		if h.Publish(context.Background(), 1) != nil {
			t.Fatal("publish failed")
		}
	}
	select {
	case <-a:
	default:
		t.Fatal("subscriber not notified")
	}
	select {
	case <-other:
		t.Fatal("cross-user event")
	default:
	}
	for i := 0; i < 7; i++ {
		_, c, e := h.Subscribe(1)
		if e != nil {
			t.Fatal(e)
		}
		defer c()
	}
	if _, _, err = h.Subscribe(1); err == nil {
		t.Fatal("unlimited per-user streams")
	}
}

type unavailablePublisher struct{}

func (unavailablePublisher) Publish(context.Context, int64) error {
	return errors.New("isolated transport down")
}

func TestInboxMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("requires isolated MySQL")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("non-isolated database refused")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	pool.SetMaxOpenConns(30)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(db.Exec("CREATE DATABASE IF NOT EXISTS `system`; CREATE TABLE IF NOT EXISTS system.system_configs(id BIGINT PRIMARY KEY,`key` VARCHAR(191) UNIQUE,value TEXT,description VARCHAR(255))").Error)
	raw, err := os.ReadFile("../shared/migrations/sql/050_notification_inbox.sql")
	must(err)
	must(db.Exec(string(raw)).Error)
	must(db.Exec(string(raw)).Error)
	later, err := os.ReadFile("../shared/migrations/sql/051_notification_ticket_reads.sql")
	must(err)
	must(db.Exec(string(later)).Error)
	must(db.Exec("CREATE TABLE IF NOT EXISTS system.id_sequences(name VARCHAR(64) PRIMARY KEY,high_water BIGINT)").Error)
	must(snowflake.Prepare(context.Background(), pool))
	must(snowflake.Configure(context.Background(), pool))
	must(db.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(1001,'notification.inapp.enabled','true','fixture'),(1002,'notification.broadcast.enabled','true','fixture')").Error)
	svc := New(db, []byte("isolated-notification-signing-key"))
	ctx := context.Background()
	base := Event{Key: "first", RecipientID: 101, Type: "billing", Scene: "order_paid", Title: "支付成功", Summary: "订单状态更新", Content: "完整消息", BusinessType: "order", BusinessID: "123"}
	// Same event, 100 concurrent deliveries, one inbox entry and one revision.
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- svc.Deliver(ctx, base) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		must(e)
	}
	snap, err := svc.Sync(ctx, 101, nil)
	must(err)
	if snap.UnreadCount != 1 || snap.Revision != 1 {
		t.Fatalf("duplicate delivery: %+v", snap)
	}
	var first Message
	must(db.Where("recipient_id=?", 101).Take(&first).Error)
	// Read-all uses the previous snapshot; arrivals after click remain unread.
	second := base
	second.Key = "second"
	must(svc.Deliver(ctx, second))
	n, err := svc.Read(ctx, 101, nil, snap.SnapshotToken)
	must(err)
	if n != 1 {
		t.Fatal("read-all boundary failed", n)
	}
	n, err = svc.Read(ctx, 101, []int64{first.ID}, "")
	must(err)
	if n != 0 {
		t.Fatal("read not idempotent")
	}
	var before Message
	must(db.Take(&before, "id=?", first.ID).Error)
	_, err = svc.Read(ctx, 101, []int64{first.ID}, "")
	must(err)
	var after Message
	must(db.Take(&after, "id=?", first.ID).Error)
	if !before.ReadAt.Equal(*after.ReadAt) {
		t.Fatal("read timestamp overwritten")
	}
	snap, err = svc.Sync(ctx, 101, nil)
	must(err)
	if snap.UnreadCount != 1 {
		t.Fatal("new arrival marked read")
	}
	if _, err = svc.Read(ctx, 102, []int64{first.ID}, ""); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("foreign batch accepted")
	}
	if _, err = svc.Read(ctx, 102, nil, snap.SnapshotToken); !errors.Is(err, ErrInput) {
		t.Fatal("foreign token accepted")
	}
	must(svc.Delete(ctx, 101, first.ID))
	must(svc.Delete(ctx, 101, first.ID))
	must(svc.Deliver(ctx, base))
	var count int64
	must(db.Model(&Message{}).Where("recipient_id=? AND source_event_key=?", 101, "first").Count(&count).Error)
	if count != 1 {
		t.Fatal("deleted event resurrected")
	}
	// Source transaction rollback must also roll back its durable event.
	sentinel := errors.New("rollback")
	e := db.Transaction(func(tx *gorm.DB) error {
		v := base
		v.Key = "rolled-back"
		if err := Enqueue(tx, v); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	must(db.Model(&Outbox{}).Where("event_key=?", "101:rolled-back").Count(&count).Error)
	if count != 0 {
		t.Fatal("rollback leaked outbox")
	}
	must(db.Transaction(func(tx *gorm.DB) error { return Enqueue(tx, base) }))
	must(db.Transaction(func(tx *gorm.DB) error { return Enqueue(tx, base) }))
	conflict := base
	conflict.Content = "different"
	if e = db.Transaction(func(tx *gorm.DB) error { return Enqueue(tx, conflict) }); !errors.Is(e, ErrConflict) {
		t.Fatal("idempotency conflict not detected", e)
	}
	// More than a sync page must be consumed without skipping the unseen tail.
	for i := 0; i < 120; i++ {
		v := base
		v.Key = fmt.Sprintf("event:%03d", i)
		must(svc.Deliver(ctx, v))
	}
	cursor := uint64(0)
	seen := map[uint64]bool{}
	for {
		v, e := svc.Sync(ctx, 101, &cursor)
		must(e)
		for _, c := range v.Changes {
			if seen[c.Revision] {
				t.Fatal("duplicate replay revision")
			}
			seen[c.Revision] = true
		}
		cursor = v.NextCursor
		if !v.HasMore {
			break
		}
	}
	current, e := svc.Sync(ctx, 101, nil)
	must(e)
	if cursor != current.Revision || len(seen) != int(current.Revision) {
		t.Fatal("incremental gap", cursor, current.Revision, len(seen))
	}
	// Slow commit must not reserve a revision that another commit can overtake.
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		done <- db.Transaction(func(tx *gorm.DB) error {
			st, e := lockState(tx, 201)
			if e != nil {
				return e
			}
			close(held)
			<-release
			return bump(tx, st, "read", nil)
		})
	}()
	<-held
	go func() {
		done <- db.Transaction(func(tx *gorm.DB) error {
			st, e := lockState(tx, 201)
			if e != nil {
				return e
			}
			return bump(tx, st, "read", nil)
		})
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	must(<-done)
	must(<-done)
	seq, e := svc.Sync(ctx, 201, nil)
	must(e)
	if seq.Revision != 2 {
		t.Fatal("concurrent revision failed")
	}

	// Exercise the source adapters using actual GORM transactions, not mock events.
	must(db.Exec("CREATE DATABASE `order`; CREATE DATABASE customer; CREATE DATABASE ticket; CREATE DATABASE instance").Error)
	must(db.Exec("CREATE TABLE `order`.orders(id BIGINT PRIMARY KEY,customer_id BIGINT,status INT); CREATE TABLE customer.customers(id BIGINT PRIMARY KEY,auth_version BIGINT,password_hash VARCHAR(255),status INT,deleted_at DATETIME NULL); CREATE TABLE ticket.ticket(id BIGINT PRIMARY KEY,client_id BIGINT,title VARCHAR(128)); CREATE TABLE ticket.ticket_reply(id BIGINT PRIMARY KEY,ticket_id BIGINT,type VARCHAR(16),reply_seq BIGINT UNSIGNED AUTO_INCREMENT UNIQUE); CREATE TABLE customer.auth_devices(id VARCHAR(64) PRIMARY KEY,user_id BIGINT,ip VARCHAR(64)); CREATE TABLE instance.instances(id BIGINT PRIMARY KEY,customer_id BIGINT,power_status VARCHAR(24),last_synced_at DATETIME(6))").Error)
	must(InstallBridge(db))
	must(db.Table("ticket.ticket").Create(map[string]any{"id": 301, "client_id": 401, "title": "fixture"}).Error)
	must(db.Transaction(func(tx *gorm.DB) error {
		return tx.Table("ticket.ticket_reply").Create(map[string]any{"id": 302, "ticket_id": 301, "type": "Admin"}).Error
	}))
	must(db.Table("ticket.ticket_reply").Create(map[string]any{"id": 303, "ticket_id": 301, "type": "Client"}).Error)
	must(db.Table("order.orders").Create(map[string]any{"id": 501, "customer_id": 401, "status": 1}).Error)
	must(db.Transaction(func(tx *gorm.DB) error { return tx.Table("order.orders").Where("id=?", 501).Update("status", 2).Error }))
	must(db.Table("customer.customers").Create(map[string]any{"id": 401, "auth_version": 1, "password_hash": "$2old", "status": 1}).Error)
	must(db.Table("customer.customers").Where("id=?", 401).Updates(map[string]any{"password_hash": "$2new", "auth_version": 2}).Error)
	must(db.Table("customer.auth_devices").Create(map[string]any{"id": "device-a", "user_id": 401, "ip": "192.0.2.1"}).Error)
	must(db.Table("customer.auth_devices").Create(map[string]any{"id": "device-b", "user_id": 401, "ip": "192.0.2.2"}).Error)
	must(db.Table("instance.instances").Create(map[string]any{"id": 601, "customer_id": 401, "power_status": "stopped", "last_synced_at": time.Now().Add(-time.Hour)}).Error)
	must(db.Table("instance.instances").Where("id=?", 601).Updates(map[string]any{"power_status": "running", "last_synced_at": time.Now().UTC()}).Error)
	for i := 0; i < 20; i++ {
		worked, err := svc.step(ctx)
		must(err)
		if !worked {
			break
		}
	}
	facts, e := svc.Sync(ctx, 401, nil)
	must(e)
	if facts.UnreadCount != 6 {
		var events []Outbox
		db.Find(&events)
		t.Fatalf("source facts expected 6, got %d, outbox=%+v", facts.UnreadCount, events)
	}

	must(svc.ReadTicket(ctx, 401, 301, 302))
	must(db.Table("ticket.ticket_reply").Create(map[string]any{"id": 304, "ticket_id": 301, "type": "Admin"}).Error)
	must(svc.ReadTicket(ctx, 401, 301, 304))
	worked, err := svc.step(ctx)
	must(err)
	if !worked {
		t.Fatal("new reply outbox missing")
	}
	var already Message
	must(db.Where("recipient_id=? AND source_event_key=?", 401, "ticket_reply:304").Take(&already).Error)
	if already.ReadAt == nil {
		t.Fatal("already displayed reply delivered unread")
	}
	if err := svc.ReadTicket(ctx, 402, 301, 304); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("foreign ticket read accepted", err)
	}

	// The wake fires at the outer SQL commit, never inside a source callback.
drainHints:
	for {
		select {
		case <-svc.wake:
		default:
			break drainHints
		}
	}
	must(db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("ticket.ticket_reply").Create(map[string]any{"id": 306, "ticket_id": 301, "type": "Admin"}).Error; err != nil {
			return err
		}
		select {
		case <-svc.wake:
			return errors.New("wake before outer commit")
		default:
		}
		return nil
	}))
	select {
	case <-svc.wake:
	default:
		t.Fatal("outer commit did not wake durable dispatcher")
	}
	rolled := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("ticket.ticket_reply").Create(map[string]any{"id": 307, "ticket_id": 301, "type": "Admin"}).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(rolled, sentinel) {
		t.Fatal(rolled)
	}
	select {
	case <-svc.wake:
		t.Fatal("rollback emitted commit wake")
	default:
	}
	must(db.Exec("CREATE USER 'ddyidc_runtime'@'127.0.0.1' IDENTIFIED BY 'isolated-unused-password'").Error)
	must(ApplyRuntimePrivileges(ctx, pool, "ddyidc_runtime", "127.0.0.1"))
	var forbidden int64
	must(db.Raw("SELECT COUNT(*) FROM information_schema.TABLE_PRIVILEGES WHERE GRANTEE=? AND TABLE_SCHEMA='system' AND TABLE_NAME='notification_messages' AND PRIVILEGE_TYPE IN ('DELETE','UPDATE','ALTER','DROP','CREATE')", "'ddyidc_runtime'@'127.0.0.1'").Scan(&forbidden).Error)
	if forbidden != 0 {
		t.Fatal("inbox runtime overprivileged")
	}
	must(db.Raw("SELECT COUNT(*) FROM information_schema.COLUMN_PRIVILEGES WHERE GRANTEE=? AND TABLE_SCHEMA='system' AND TABLE_NAME='notification_messages' AND PRIVILEGE_TYPE='UPDATE'", "'ddyidc_runtime'@'127.0.0.1'").Scan(&forbidden).Error)
	if forbidden != 2 {
		t.Fatal("inbox read/delete column grants missing", forbidden)
	}
	if ApplyRuntimePrivileges(ctx, pool, "root", "localhost") == nil {
		t.Fatal("unsafe runtime principal allowed")
	}
	if _, err := svc.parseToken(101, svc.token(101, 999, "broadcast")); !errors.Is(err, ErrInput) {
		t.Fatal("token purpose confusion")
	}
	// Both recipient ownership and broadcast idempotence are checked on real rows.
	input := BroadcastInput{RequestID: "broadcast-test-1", Title: "公告", Summary: "测试", Content: "仅隔离数据库", Recipients: []int64{401}, Reason: "isolated", Confirm: true}
	preview, e := svc.Preview(ctx, 900, input)
	must(e)
	input.PreviewToken = preview["previewToken"].(string)
	job, e := svc.CreateBroadcast(ctx, 900, "127.0.0.1", input)
	must(e)
	again, e := svc.CreateBroadcast(ctx, 900, "127.0.0.1", input)
	must(e)
	if job.ID != again.ID || job.Total != 1 {
		t.Fatal("broadcast repeated")
	}
	must(svc.processBroadcasts(ctx))
	for i := 0; i < 20; i++ {
		worked, err := svc.step(ctx)
		must(err)
		if !worked {
			break
		}
	}
	var targets int64
	must(db.Model(&Target{}).Where("broadcast_id=?", job.ID).Count(&targets).Error)
	if targets != 1 {
		t.Fatal("audience snapshot duplicated")
	}

	// A multi-batch broadcast actually exercises the bounded parallel workers.
	recipients := make([]int64, 100)
	for i := range recipients {
		recipients[i] = int64(2000 + i)
		must(db.Table("customer.customers").Create(map[string]any{"id": recipients[i], "auth_version": 1, "password_hash": "disabled-fixture", "status": 1}).Error)
	}
	many := BroadcastInput{RequestID: "parallel-broadcast-test", Title: "并发群发验收", Summary: "隔离测试", Content: "仅测试收件人", Recipients: recipients, Reason: "isolated", Confirm: true}
	mp, e := svc.Preview(ctx, 900, many)
	must(e)
	many.PreviewToken = mp["previewToken"].(string)
	mj, e := svc.CreateBroadcast(ctx, 900, "127.0.0.1", many)
	must(e)
	for i := 0; i < 5; i++ {
		must(svc.processBroadcasts(ctx))
	}
	must(db.Take(&mj, "id=?", mj.ID).Error)
	if mj.Status != "completed" || mj.Succeeded != 100 || mj.Failed != 0 {
		t.Fatalf("parallel batch incomplete: %+v", mj)
	}
	must(db.Model(&Message{}).Where("source_event_key=?", fmt.Sprintf("broadcast:%d", mj.ID)).Count(&count).Error)
	if count != 100 {
		t.Fatal("parallel broadcast inbox count", count)
	}
	// Platform role grants, disabled roles and narrowing caps are enforced independently.
	must(db.Exec("CREATE TABLE system.admins(id BIGINT PRIMARY KEY,is_super BOOLEAN,status INT); CREATE TABLE system.permissions(id BIGINT PRIMARY KEY,code VARCHAR(96),name VARCHAR(128),module VARCHAR(64)); CREATE TABLE system.roles(id BIGINT PRIMARY KEY,status INT); CREATE TABLE system.role_permissions(role_id BIGINT,permission_id BIGINT); CREATE TABLE system.admin_roles(admin_id BIGINT,role_id BIGINT)").Error)
	permissions, err := os.ReadFile("../shared/migrations/sql/052_notification_role_permissions.sql")
	must(err)
	must(db.Exec(string(permissions)).Error)
	must(db.Exec(string(permissions)).Error)
	must(db.Exec("INSERT INTO system.admins VALUES(900,1,1),(901,0,1),(902,1,2); INSERT INTO system.roles VALUES(910,1); INSERT INTO system.admin_roles VALUES(901,910); INSERT INTO system.role_permissions SELECT 910,id FROM system.permissions WHERE code='notification:view'").Error)
	if !svc.Allowed(ctx, 900, "config") || !svc.Allowed(ctx, 901, "view") || svc.Allowed(ctx, 901, "create") || svc.Allowed(ctx, 901, "config") || svc.Allowed(ctx, 902, "view") {
		t.Fatal("platform permission boundary")
	}
	must(db.Exec("INSERT INTO system.notification_permissions(admin_id,can_view,can_create) VALUES(901,0,1)").Error)
	if svc.Allowed(ctx, 901, "view") || svc.Allowed(ctx, 901, "create") {
		t.Fatal("cap unexpectedly granted authority")
	}
	must(db.Exec("DELETE FROM system.notification_permissions WHERE admin_id=901; UPDATE system.roles SET status=2 WHERE id=910").Error)
	if svc.Allowed(ctx, 901, "view") {
		t.Fatal("disabled role accepted")
	}
	// Transport failures preserve pending hints, then recover without another inbox row.
	svc.Publisher = unavailablePublisher{}
	if svc.flush(ctx) == nil {
		t.Fatal("transport failure swallowed")
	}
	var pending int64
	must(db.Model(&Change{}).Where("push_status='pending'").Count(&pending).Error)
	if pending == 0 {
		t.Fatal("failed push marked published")
	}
	svc.Publisher = svc.Hub
	must(svc.flush(ctx))
	// Expiry follows configured notification days, independently of the 72-hour admin list.
	must(db.Exec("ALTER TABLE instance.instances ADD status INT DEFAULT 1, ADD expires_at DATETIME(6), ADD deleted_at DATETIME NULL, ADD config_snapshot JSON").Error)
	expiry := time.Now().UTC().Add(5 * 24 * time.Hour).Truncate(time.Second)
	must(db.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(1003,'notify.expiry.days_before','7','fixture')").Error)
	for i, cycle := range []string{"monthly", "free", "onetime"} {
		must(db.Table("instance.instances").Create(map[string]any{"id": 701 + i, "customer_id": 401, "status": 1, "expires_at": expiry, "config_snapshot": fmt.Sprintf(`{"billingCycle":%q}`, cycle)}).Error)
	}
	must(EmitExpiry(db))
	must(db.Model(&Outbox{}).Where("event_key LIKE ?", "401:expiry:%").Count(&count).Error)
	if count != 1 {
		t.Fatal("configured expiry/free exclusion", count)
	}
	// Renewal after enqueue invalidates the old reminder at delivery.
	must(db.Table("instance.instances").Where("id=?", 701).Update("expires_at", expiry.Add(30*24*time.Hour)).Error)
	for i := 0; i < 20; i++ {
		worked, err := svc.step(ctx)
		must(err)
		if !worked {
			break
		}
	}
	must(db.Model(&Outbox{}).Where("event_key LIKE ? AND status='skipped'", "401:expiry:%").Count(&count).Error)
	if count != 1 {
		t.Fatal("renewed reminder not suppressed")
	}
	// Retention only removes published change hints; durable messages and pending recovery survive.
	must(db.Model(&Change{}).Where("recipient_id=?", 101).Updates(map[string]any{"created_at": time.Now().Add(-8 * 24 * time.Hour), "push_status": "published"}).Error)
	must(db.Model(&Change{}).Where("recipient_id=? AND revision=?", 101, current.Revision).Update("push_status", "pending").Error)
	svc.nextCleanup = time.Time{}
	must(svc.cleanup(ctx))
	must(db.Model(&Change{}).Where("recipient_id=?", 101).Count(&count).Error)
	if count != 1 {
		t.Fatal("pending hint removed", count)
	}
	zero := uint64(0)
	reset, e := svc.Sync(ctx, 101, &zero)
	must(e)
	if !reset.ResetRequired {
		t.Fatal("expired cursor not reset")
	}
	must(db.Model(&Message{}).Where("recipient_id=?", 101).Count(&count).Error)
	if count < 120 {
		t.Fatal("retention removed inbox")
	}

	t.Run("durable renewal pause delivery", func(t *testing.T) {
		must(db.Exec("ALTER TABLE instance.instances ADD ownership_version BIGINT NOT NULL DEFAULT 1").Error)
		must(db.Table("system.system_configs").Where("`key`=?", "notification.inapp.enabled").Update("value", "false").Error)
		event := Event{Key: "renewal_quote:701:proof", RecipientID: 401, Type: "billing", Scene: "renewal_quote_paused", Title: "自动续费已暂停", Summary: "价格变化请重新确认", Content: "价格变化请重新确认", BusinessType: "instance", BusinessID: "701", OwnershipVersion: 1}
		rollback := db.Begin()
		must(EnqueueRenewalPause(rollback, event))
		must(rollback.Rollback().Error)
		must(db.Model(&Outbox{}).Where("event_key=?", "401:"+event.Key).Count(&count).Error)
		if count != 0 {
			t.Fatal("rolled back alert escaped")
		}
		must(db.Transaction(func(tx *gorm.DB) error { return EnqueueRenewalPause(tx, event) }))
		must(db.Transaction(func(tx *gorm.DB) error { return EnqueueRenewalPause(tx, event) }))
		must(db.Model(&Outbox{}).Where("event_key=? AND status='pending'", "401:"+event.Key).Count(&count).Error)
		if count != 1 {
			t.Fatal("disabled warning lost or duplicated")
		}
		must(db.Table("system.system_configs").Where("`key`=?", "notification.inapp.enabled").Update("value", "true").Error)
		drain := func() {
			for n := 0; n < 100; n++ {
				worked, e := svc.step(ctx)
				must(e)
				if !worked {
					return
				}
			}
			t.Fatal("outbox failed to drain")
		}
		drain()
		must(db.Model(&Message{}).Where("recipient_id=? AND source_event_key=?", 401, event.Key).Count(&count).Error)
		if count != 1 {
			t.Fatal("warning not delivered exactly once")
		}
		hints, stop, e := svc.Hub.Subscribe(401)
		must(e)
		defer stop()
		must(svc.flush(ctx))
		select {
		case <-hints:
		default:
			t.Fatal("live notification missing")
		}
		next := event
		next.Key = "renewal_quote:701:transferred"
		must(db.Transaction(func(tx *gorm.DB) error { return EnqueueRenewalPause(tx, next) }))
		must(db.Table("instance.instances").Where("id=?", 701).Updates(map[string]any{"customer_id": 402, "ownership_version": 2}).Error)
		drain()
		must(db.Model(&Outbox{}).Where("event_key=? AND status='skipped'", "401:"+next.Key).Count(&count).Error)
		if count != 1 {
			t.Fatal("transferred instance alert exposed")
		}
		t.Log("disabled alert retained; rollback atomic; duplicate deduped; inbox delivered; live hint published; transferred owner fenced")
	})
	t.Log("migration replay, 100 concurrent duplicate sends, snapshot read-all, foreign ownership, idempotent read/delete, rollback, event-key conflicts, paginated replay and slow commits verified")
}

func TestActorSessionHintsAreScoped(t *testing.T) {
	h := NewHub()
	a, stop, e := h.SubscribeFor(101, 900)
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	b, end, e := h.SubscribeFor(102, 901)
	if e != nil {
		t.Fatal(e)
	}
	defer end()
	direct, closeDirect, e := h.Subscribe(103)
	if e != nil {
		t.Fatal(e)
	}
	defer closeDirect()
	_ = h.PublishActor(context.Background(), 900)
	select {
	case <-a:
	default:
		t.Fatal("delegated session not woken")
	}
	select {
	case <-b:
		t.Fatal("unrelated administrator stream woken")
	default:
	}
	select {
	case <-direct:
		t.Fatal("ordinary customer stream woken")
	default:
	}
	h.DisconnectAll()
	if len(h.actors) != 0 {
		t.Fatal("actor index retained after disconnect")
	}
	_ = h.PublishActor(context.Background(), 900)
}
