package tasks

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/workgate"
	"errors"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDurableTasksMySQL(t *testing.T) {
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
	if err := snowflake.Configure(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// This script has its own isolated server; prior receipt fixtures are terminalized
	// so only the dedicated task participates in the claim concurrency barrier.
	if err := database.Exec("UPDATE system.durable_tasks SET status='succeeded'").Error; err != nil {
		t.Fatal(err)
	}
	payload := PaymentPayload{CustomerID: 77, BusinessID: 88, BusinessType: "order", Channel: "alipay", AmountFen: 100}
	sentinel := errors.New("rollback")
	if err := database.Transaction(func(tx *gorm.DB) error {
		p := NoticePayload{CustomerID: 77, BusinessID: 991, Scene: "marketplace_refund", Params: map[string]string{"order_no": "isolated"}}
		if err := EnqueueNotice(tx, p); err != nil {
			return err
		}
		if err := EnqueueNotice(tx, p); err != nil {
			return err
		}
		p.Params["order_no"] = "different"
		if EnqueueNotice(tx, p) == nil {
			t.Fatal("same notification identity accepted a different payload")
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var escapedNotice int64
	if err := database.Model(&Task{}).Where("task_key='notice:marketplace_refund:991:77'").Count(&escapedNotice).Error; err != nil || escapedNotice != 0 {
		t.Fatal("notification escaped business rollback", err)
	}

	if err := database.Transaction(func(tx *gorm.DB) error {
		if err := EnqueuePayment(tx, "rollback", payload); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var count int64
	database.Table("system.durable_tasks").Where("task_key='rollback'").Count(&count)
	if count != 0 {
		t.Fatal("task escaped business rollback")
	}
	if err := database.Transaction(func(tx *gorm.DB) error { return EnqueuePayment(tx, "claim", payload) }); err != nil {
		t.Fatal(err)
	}
	var claimed atomic.Int32
	var wg sync.WaitGroup
	var selected *Task
	var mu sync.Mutex
	start := make(chan struct{})
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			task, err := Claim(ctx, database)
			if err != nil {
				t.Error(err)
			}
			if task != nil {
				claimed.Add(1)
				mu.Lock()
				selected = task
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("claims=%d", claimed.Load())
	}
	oldToken := selected.LeaseToken
	selected.LeaseToken = "forged"
	if Finish(ctx, database, selected, nil) == nil {
		t.Fatal("forged lease finished")
	}
	selected.LeaseToken = oldToken
	if err := database.Exec("UPDATE system.durable_tasks SET lease_until=DATE_SUB(NOW(3),INTERVAL 1 SECOND) WHERE id=?", selected.ID).Error; err != nil {
		t.Fatal(err)
	}
	task, err := Claim(ctx, database)
	if err != nil || task != nil {
		t.Fatal("expired financial task replayed", err)
	}
	if Finish(ctx, database, selected, nil) == nil {
		t.Fatal("stale worker finished")
	}
	var status string
	database.Raw("SELECT status FROM system.durable_tasks WHERE id=?", selected.ID).Scan(&status)
	if status != "unknown" {
		t.Fatal(status)
	}
	t.Log("transaction rollback, 16 concurrent claims, forged/stale fencing token and expired lease -> unknown without side effect replay passed")
	if err := database.Exec("INSERT INTO `order`.orders(id,order_no,customer_id,status,pay_amount) VALUES(98001,'RECOVER_LOCAL',77,2,100)").Error; err != nil {
		t.Fatal(err)
	}
	payload.BusinessID = 98001
	if err := database.Exec("INSERT INTO `order`.order_items(id,order_id,product_id,product_name,module_type,config_snapshot,qty,unit_price,subtotal) VALUES(98002,98001,91001,'isolated','cloud','{}',1,100,100)").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(func(tx *gorm.DB) error { return EnqueuePayment(tx, "recover-local", payload) }); err != nil {
		t.Fatal(err)
	}
	var recoverable Task
	if err := database.Where("task_key='recover-local'").Take(&recoverable).Error; err != nil {
		t.Fatal(err)
	}
	if ReconcileLocal(ctx, database, recoverable.ID, 1, "127.0.0.1", "operator checked") != ErrReconcile {
		t.Fatal("non-unknown task reset")
	}
	database.Model(&Task{}).Where("id=?", recoverable.ID).Update("status", "unknown")
	if RecordReview(ctx, database, recoverable.ID, 1, recoverable.Attempt+1, "127.0.0.1", "stale review") != ErrReconcile {
		t.Fatal("review accepted stale attempt")
	}
	if err := RecordReview(ctx, database, recoverable.ID, 1, recoverable.Attempt, "127.0.0.1", "awaiting independent supplier evidence"); err != nil {
		t.Fatal(err)
	}
	var held Task
	if err := database.Where("id=?", recoverable.ID).Take(&held).Error; err != nil || held.Status != "unknown" || held.Attempt != recoverable.Attempt {
		t.Fatal("recording investigation resumed external work", err)
	}
	views, err := List(ctx, database, 1)
	if err != nil {
		t.Fatal(err)
	}
	foundReview := false
	for _, view := range views {
		if view.TaskKey == recoverable.TaskKey && view.ReviewNote == "awaiting independent supplier evidence" {
			foundReview = true
		}
	}
	if !foundReview {
		t.Fatal("review note absent from admin task view")
	}
	t.Log("manual review preserves unknown state and attempt, rejects stale generation, exposes audited note; no replay")
	if ReconcileLocal(ctx, database, recoverable.ID, 1, "127.0.0.1", "") != ErrReconcile {
		t.Fatal("missing audit reason accepted")
	}
	if err := ReconcileLocal(ctx, database, recoverable.ID, 1, "127.0.0.1", "operator checked local planner"); err != nil {
		t.Fatal(err)
	}
	database.Table("system.task_reconciliations").Where("task_id=? AND to_status='pending'", recoverable.ID).Count(&count)
	if count != 1 {
		t.Fatal("missing recovery audit")
	}
	if ReconcileLocal(ctx, database, recoverable.ID, 1, "127.0.0.1", "repeat") != ErrReconcile {
		t.Fatal("duplicate recovery accepted")
	}
	t.Log("local-only planner recovery requires unknown status and admin reason; single transactional audit; repeated request rejected")
	workgate.Configure(pool)
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	entered, release, workerDone := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(workerDone)
		Run(workerCtx, database, func(context.Context, *Task) error { entered <- struct{}{}; <-release; return nil })
	}()
	select {
	case <-entered:
	case <-time.After(6 * time.Second):
		t.Fatal("durable worker failed to run without cron leader context")
	}
	paused := make(chan error, 1)
	pauseCtx, cancelPause := context.WithTimeout(ctx, 10*time.Second)
	defer cancelPause()
	go func() { paused <- (&workgate.Gate{DB: pool}).Pause(pauseCtx, "isolated worker drain") }()
	select {
	case <-paused:
		t.Fatal("restore skipped active task")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-paused; err != nil {
		t.Fatal(err)
	}
	if err := database.Where("id=?", recoverable.ID).Take(&recoverable).Error; err != nil {
		t.Fatal(err)
	}
	if recoverable.Status != "succeeded" {
		t.Fatal("barrier released before task finish persisted")
	}
	if err := database.Transaction(func(tx *gorm.DB) error { return EnqueuePayment(tx, "paused-worker", payload) }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2200 * time.Millisecond)
	var pausedTask Task
	if err := database.Where("task_key='paused-worker'").Take(&pausedTask).Error; err != nil {
		t.Fatal(err)
	}
	if pausedTask.Status != "pending" || pausedTask.Attempt != 0 {
		t.Fatal("maintenance worker claimed new task")
	}
	stopWorker()
	<-workerDone
	if err := database.Exec("UPDATE system.operation_barrier SET mode='running',reason='' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&Task{}).Where("id=?", pausedTask.ID).Update("status", "succeeded").Error; err != nil {
		t.Fatal(err)
	}
	t.Log("durable worker independent of cron leader: barrier drains handler and completion write, paused worker leaves new task pending")

}
