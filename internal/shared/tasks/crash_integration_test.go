package tasks

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDurableTaskCrashMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	ctx := context.Background()
	marker := filepath.Join(filepath.Dir(socket), "worker-claimed")
	if os.Getenv("FOUNDATION_CRASH_CHILD") == "1" {
		task, err := Claim(ctx, db)
		if err != nil || task == nil {
			t.Fatal("child could not claim", err)
		}
		if err := os.WriteFile(marker, []byte("claimed"), 0600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	if err := snowflake.Configure(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE system.durable_tasks SET status='succeeded'").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return EnqueuePayment(tx, "crash-test", PaymentPayload{CustomerID: 77, BusinessID: 98001, BusinessType: "order", Channel: "alipay"})
	}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDurableTaskCrashMySQL$", "-test.count=1")
	cmd.Env = append(os.Environ(), "FOUNDATION_CRASH_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child claim timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("worker not killed")
	}
	var task Task
	if err := db.Where("task_key='crash-test'").Take(&task).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != "running" {
		t.Fatal("crashed task disappeared", task.Status)
	}
	// Accelerate the database lease clock in this isolated test only.
	if err := db.Exec("UPDATE system.durable_tasks SET lease_until=DATE_SUB(NOW(3),INTERVAL 1 SECOND) WHERE id=?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	next, err := Claim(ctx, db)
	if err != nil || next != nil {
		t.Fatal("restart blindly replayed killed worker", err)
	}
	if err := db.Where("id=?", task.ID).Take(&task).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != "unknown" {
		t.Fatal("crash not surfaced", task.Status)
	}
	t.Log("actual child process SIGKILL after committed claim: durable row survives, restart expires lease to unknown, no remote call or blind replay")
}
